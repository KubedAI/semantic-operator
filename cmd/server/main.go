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
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/KubedAI/ossie-semantic-operator/internal/cache"
	"github.com/KubedAI/ossie-semantic-operator/internal/emitter"
	_ "github.com/KubedAI/ossie-semantic-operator/internal/emitter/starrocks"
	"github.com/KubedAI/ossie-semantic-operator/internal/observability"
	"github.com/KubedAI/ossie-semantic-operator/internal/serving"
	mcpadapter "github.com/KubedAI/ossie-semantic-operator/internal/serving/mcp"
	"github.com/KubedAI/ossie-semantic-operator/internal/serving/rest"
	"github.com/KubedAI/ossie-semantic-operator/internal/starrocks"
)

var version = "dev" // set via -ldflags

func main() {
	log := observability.Logger(envOr("LOG_LEVEL", "info"))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srClient, err := starrocks.Open(starrocks.Config{
		Host:     mustEnv(log, "STARROCKS_HOST"),
		Port:     envInt("STARROCKS_PORT", 9030),
		User:     envOr("STARROCKS_USER", "root"),
		Password: os.Getenv("STARROCKS_PASSWORD"),
	})
	if err != nil {
		log.Error("opening StarRocks client", "err", err)
		os.Exit(1)
	}

	dialect, err := emitter.Get(envOr("SQL_DIALECT", "starrocks"))
	if err != nil {
		log.Error("resolving dialect", "err", err)
		os.Exit(1)
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
		os.Exit(1)
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

	mux := http.NewServeMux()
	mux.Handle("/mcp", mcpadapter.Handler(svc, version))
	mux.Handle("/v1/", rest.Handler(svc))
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/readyz", readyzHandler(store, srClient))

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
		os.Exit(1)
	}
}

func mustEnv(log interface{ Error(string, ...any) }, key string) string {
	v := os.Getenv(key)
	if v == "" {
		log.Error("required environment variable missing", "key", key)
		os.Exit(1)
	}
	return v
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

func readyzHandler(store *serving.Store, db pinger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !store.Synced() {
			http.Error(w, "compiled-model store not synced", http.StatusServiceUnavailable)
			return
		}
		if err := db.Ping(r.Context()); err != nil {
			http.Error(w, "starrocks unreachable: "+err.Error(), http.StatusServiceUnavailable)
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
