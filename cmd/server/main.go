// The semantic-server: planner + governance + caches behind the MCP and
// REST adapters. Stateless; scales horizontally. All configuration comes
// from env (set by the Helm chart).
package main

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/KubedAI/semantic-operator/internal/serving/exchange"
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
	// The client stops reading past this, so it must match the service's own
	// ceiling or one of the two would never be reached.
	cfg.MaxResultBytes = envInt("QUERY_MAX_RESULT_BYTES", 0)
	if err != nil {
		log.Error("reading engine connection config", "err", err)
		return err
	}
	engineClient, err := dbclient.Open(engine, cfg)
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
	authorizers, err := authorizationRegistryFromEnv()
	if err != nil {
		log.Error("configuring external authorization", "err", err)
		return err
	}
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
		Store:         store,
		Dialect:       dialect,
		Cache:         valkey,
		DB:            engineClient,
		Metrics:       metrics,
		Log:           log,
		Tracer:        tracer,
		Authorization: authorizers,
		// Off unless asked for. A metric listing is meant to ground an agent on
		// certified names, and the raw SQL is the definition itself.
		ExposeExpressions: envOr("EXPOSE_METRIC_EXPRESSIONS", "false") == "true",
		// Anything left at zero falls back to the built-in default, so a
		// missing variable can never mean "unbounded".
		Limits: serving.Limits{
			DefaultRowLimit:      envInt("QUERY_DEFAULT_ROW_LIMIT", 0),
			MaxRowLimit:          envInt("QUERY_MAX_ROW_LIMIT", 0),
			MaxMetrics:           envInt("QUERY_MAX_METRICS", 0),
			MaxDimensions:        envInt("QUERY_MAX_DIMENSIONS", 0),
			MaxFilters:           envInt("QUERY_MAX_FILTERS", 0),
			MaxFilterValues:      envInt("QUERY_MAX_FILTER_VALUES", 0),
			MaxResultBytes:       envInt("QUERY_MAX_RESULT_BYTES", 0),
			MaxCacheEntryBytes:   envInt("QUERY_MAX_CACHE_ENTRY_BYTES", 0),
			MaxRequestBytes:      envInt("QUERY_MAX_REQUEST_BYTES", 0),
			MaxConcurrentQueries: envInt("QUERY_MAX_CONCURRENT", 0),
		},
	}

	// Identity resolution. Header mode trusts X-Semantic-User and
	// X-Semantic-Role and therefore requires an authenticating proxy in front;
	// jwt mode validates a bearer token and ignores both headers.
	authn, err := auth.New(ctx, auth.Options{
		Mode:            auth.Mode(envOr("AUTH_MODE", string(auth.ModeHeader))),
		JWKSURL:         os.Getenv("OIDC_JWKS_URL"),
		Issuer:          os.Getenv("OIDC_ISSUER"),
		Audience:        os.Getenv("OIDC_AUDIENCE"),
		PrincipalClaim:  os.Getenv("OIDC_PRINCIPAL_CLAIM"),
		RoleClaim:       os.Getenv("OIDC_ROLE_CLAIM"),
		GroupsClaim:     os.Getenv("OIDC_GROUPS_CLAIM"),
		ClaimsToCopy:    splitList(os.Getenv("OIDC_CLAIMS_TO_COPY")),
		EngineUserClaim: os.Getenv("ENGINE_USER_CLAIM"),
	})
	if err != nil {
		log.Error("configuring authentication", "err", err)
		return err
	}
	if authn.Mode() == auth.ModeHeader {
		log.Warn("AUTH_MODE=header: the X-Semantic-User and X-Semantic-Role headers are trusted verbatim; " +
			"put an authenticating proxy in front of this service or set AUTH_MODE=jwt")
	}

	// Engine identity mode selects how a query authenticates to the engine.
	// static uses the server's own credential. passthrough forwards the
	// caller's validated token. exchange swaps the caller's token for an
	// engine-audience token via RFC 8693. passthrough and exchange require a
	// real token, so they are valid only in jwt mode with an engine user claim.
	engineIdentityMode := envOr("ENGINE_IDENTITY_MODE", "static")
	var resolver serving.CredentialResolver
	switch engineIdentityMode {
	case "static":
		resolver = serving.StaticResolver()
	case "passthrough":
		if err := requireEngineIdentity(authn, engineClient, engine); err != nil {
			log.Error("invalid engine identity configuration", "err", err)
			return err
		}
		resolver = serving.PassthroughResolver()
	case "exchange":
		if err := requireEngineIdentity(authn, engineClient, engine); err != nil {
			log.Error("invalid engine identity configuration", "err", err)
			return err
		}
		ex, err := exchange.New(exchange.Options{
			TokenURL:          os.Getenv("ENGINE_EXCHANGE_TOKEN_URL"),
			ClientID:          os.Getenv("ENGINE_EXCHANGE_CLIENT_ID"),
			ClientSecret:      os.Getenv("ENGINE_EXCHANGE_CLIENT_SECRET"),
			AllowInsecureHTTP: envOr("ENGINE_EXCHANGE_ALLOW_INSECURE_HTTP", "false") == "true",
			Logger:            log,
		})
		if err != nil {
			log.Error("invalid engine identity configuration", "err", err)
			return err
		}
		resolver = serving.ExchangeResolver(ex, os.Getenv("ENGINE_USER_CLAIM"))
	default:
		err := fmt.Errorf("unknown ENGINE_IDENTITY_MODE %q: use static, passthrough, or exchange", engineIdentityMode)
		log.Error("invalid engine identity configuration", "err", err)
		return err
	}

	mux := http.NewServeMux()
	// MCP uses a streamable HTTP transport, so it must not sit behind a
	// response deadline. REST always writes one bounded JSON document, so it
	// gets a per-route deadline instead of the global WriteTimeout that would
	// cut MCP streams short.
	restTimeout := time.Duration(envInt("REST_TIMEOUT_SECONDS", 60)) * time.Second

	// One bound shared by both surfaces, held for the whole request rather
	// than only while the engine runs. A result stays allocated until the
	// response has been written, so releasing earlier would let a slow client
	// keep its result while the next request takes the freed slot. This also
	// covers cache hits, which allocate a full result without touching the
	// engine.
	limits := svc.Limits.WithDefaults()
	limiter := serving.NewLimiter(limits.MaxConcurrentQueries,
		time.Duration(envInt("QUERY_QUEUE_WAIT_SECONDS", 5))*time.Second)
	log.Info("query concurrency bounded", "max_in_flight", limits.MaxConcurrentQueries,
		"max_result_bytes", limits.MaxResultBytes)

	mux.Handle("/mcp", limiter.Middleware(mcpadapter.Handler(svc, version, authn, resolver)))
	mux.Handle("/v1/", limiter.Middleware(http.TimeoutHandler(rest.Handler(svc, authn, resolver), restTimeout,
		`{"error":"request timed out"}`)))
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/readyz", readyzHandler(store, engineClient, engine))

	addr := envOr("LISTEN_ADDR", ":8090")
	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
		// No WriteTimeout on purpose. It applies to every route, and MCP
		// streams responses, so a global write deadline would truncate them.
		// The REST routes carry their own deadline above.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       time.Duration(envInt("READ_TIMEOUT_SECONDS", 30)) * time.Second,
		IdleTimeout:       time.Duration(envInt("IDLE_TIMEOUT_SECONDS", 120)) * time.Second,
		MaxHeaderBytes:    envInt("MAX_HEADER_BYTES", 1<<20),
	}
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

// requireEngineIdentity enforces the preconditions shared by passthrough and
// exchange: a real caller token (jwt mode), a configured engine user claim, and
// an engine that actually executes each query under the per-request credential.
// The last check prevents a silent security downgrade on engines that ignore
// the credential and would run every caller's query under the server identity.
func requireEngineIdentity(authn *auth.Authenticator, engineClient dbclient.Client, engine string) error {
	if authn.Mode() != auth.ModeJWT {
		return fmt.Errorf("ENGINE_IDENTITY_MODE passthrough or exchange requires AUTH_MODE=jwt")
	}
	if os.Getenv("ENGINE_USER_CLAIM") == "" {
		return fmt.Errorf("ENGINE_IDENTITY_MODE passthrough or exchange requires ENGINE_USER_CLAIM, the token claim used as the engine session user")
	}
	if _, ok := engineClient.(dbclient.PerRequestIdentityClient); !ok {
		return fmt.Errorf("ENGINE_IDENTITY_MODE passthrough or exchange requires an engine that honors per-request identity; engine %q runs every query under the server's own credential", engine)
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
