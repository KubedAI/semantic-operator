// The semantic-server: planner + governance + caches behind the MCP and
// REST adapters. Stateless; scales horizontally. All configuration comes
// from env (set by the Helm chart).
package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/KubedAI/semantic-operator/internal/cache"
	"github.com/KubedAI/semantic-operator/internal/dbclient"
	"github.com/KubedAI/semantic-operator/internal/emitter"
	_ "github.com/KubedAI/semantic-operator/internal/emitter/starrocks"
	_ "github.com/KubedAI/semantic-operator/internal/emitter/trino"
	"github.com/KubedAI/semantic-operator/internal/observability"
	"github.com/KubedAI/semantic-operator/internal/serving"
	"github.com/KubedAI/semantic-operator/internal/serving/auth"
	mcpadapter "github.com/KubedAI/semantic-operator/internal/serving/mcp"
	"github.com/KubedAI/semantic-operator/internal/serving/rest"
	_ "github.com/KubedAI/semantic-operator/internal/starrocks"
	_ "github.com/KubedAI/semantic-operator/internal/trino"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var version = "dev" // set via -ldflags

func main() {
	err := run()
	if err != nil {
		os.Exit(1)
	}
}

func run() error {
	log := observability.Logger(envOr("LOG_LEVEL", "info"))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// SQL_DIALECT selects both halves of the engine boundary: the SQL
	// dialect (emitter) and the connection client (dbclient). The ENGINE_*
	// env vars configure the connection.
	engine := envOr("SQL_DIALECT", "starrocks")
	dialect, err := emitter.Get(engine)
	if err != nil {
		log.Error("resolving dialect", "err", err)
		return err
	}
	cfg, err := dbclient.EnvConfig()
	if err != nil {
		log.Error("reading engine connection config", "err", err)
		return err
	}
	srClient, err := dbclient.Open(engine, cfg)
	if err != nil {
		log.Error("opening query engine client", "engine", engine, "err", err)
		return err
	}

	valkey := cache.New(cache.Options{
		Addr:      os.Getenv("VALKEY_ADDR"),
		Password:  os.Getenv("VALKEY_PASSWORD"),
		DB:        envInt("VALKEY_DB", 0),
		PlanTTL:   envDuration("PLAN_CACHE_TTL", 24*time.Hour),
		ResultTTL: envDuration("RESULT_CACHE_TTL", 60*time.Second),
	}, log)
	if valkey == nil {
		log.Warn("VALKEY_ADDR not set; running without plan/result caches")
	}

	tracer, shutdownTracing, err := observability.Tracer(ctx, "semantic-server", os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"))
	if err != nil {
		log.Error("configuring tracing", "err", err)
		return err
	}
	defer func() { _ = shutdownTracing(context.Background()) }()

	store := serving.NewStore()
	metrics := observability.NewMetrics()
	metrics.StoreSynced.Set(0)
	metrics.LoadedModels.Set(0)
	namespace := envOr("WATCH_NAMESPACE", "semantic-system")
	go func() {
		callbacks := &serving.WatchCallbacks{
			OnModelCount: func(count int) { metrics.LoadedModels.Set(float64(count)) },
			OnSync:       func(synced bool) { metrics.StoreSynced.Set(boolFloat64(synced)) },
		}
		if err := serving.WatchConfigMaps(ctx, namespace, store, log, callbacks); err != nil && !errors.Is(err, context.Canceled) {
			log.Error("configmap watcher exited", "err", err)
			stop()
		}
	}()

	svc := &serving.Service{
		Store:   store,
		Dialect: dialect,
		Cache:   valkey,
		DB:      srClient,
		Metrics: metrics,
		Log:     log,
		Tracer:  tracer,
	}

	// Identity resolution. Header mode trusts X-Semantic-Role and therefore
	// requires an authenticating proxy in front; jwt mode validates a bearer
	// token against the issuer's JWKS and ignores the header entirely.
	authn, err := auth.New(ctx, auth.Options{
		Mode:         auth.Mode(envOr("AUTH_MODE", string(auth.ModeHeader))),
		JWKSURL:      os.Getenv("OIDC_JWKS_URL"),
		Issuer:       os.Getenv("OIDC_ISSUER"),
		Audience:     os.Getenv("OIDC_AUDIENCE"),
		RoleClaim:    os.Getenv("OIDC_ROLE_CLAIM"),
		ClaimsToCopy: splitList(os.Getenv("OIDC_CLAIMS_TO_COPY")),
	})
	if err != nil {
		log.Error("configuring authentication", "err", err)
		return err
	}
	if authn.Mode() == auth.ModeHeader {
		log.Warn("AUTH_MODE=header: the X-Semantic-Role header is trusted verbatim; " +
			"put an authenticating proxy in front of this service or set AUTH_MODE=jwt")
	}

	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpadapter.Handler(svc, version, authn))
	mux.Handle("/v1/", rest.Handler(svc, authn))
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/readyz", readyzHandler(store, srClient, engine))

	addr := envOr("LISTEN_ADDR", ":8090")
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	log.Info("semantic-server listening", "addr", addr, "namespace", namespace, "version", version)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("server exited", "err", err)
		return err
	}
	return nil
}

// splitList parses a comma-separated env value, ignoring blanks.
func splitList(v string) []string {
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}

type pinger interface {
	Ping(context.Context) error
}

func readyzHandler(store *serving.Store, db pinger, engine string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !store.Synced() {
			http.Error(w, "compiled-model store not synced", http.StatusServiceUnavailable)
			return
		}
		if err := db.Ping(r.Context()); err != nil {
			http.Error(w, "query engine ("+engine+") unreachable: "+err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

func boolFloat64(v bool) float64 {
	if v {
		return 1
	}
	return 0
}
