// The semantic-server: planner + governance + caches behind the MCP and
// REST adapters. Stateless; scales horizontally. Configuration comes from
// built-in defaults, an optional YAML file (--config or SEMANTIC_CONFIG), and
// SEMANTIC__ environment overrides. See config.go and the confload package.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
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

// configEnv names the config file path when --config is not given.
const configEnv = "SEMANTIC_CONFIG"

func main() {
	err := run()
	if err != nil {
		os.Exit(1)
	}
}

func run() error {
	var configPath string
	flag.StringVar(&configPath, "config", os.Getenv(configEnv), "path to the YAML config file (also "+configEnv+")")
	flag.Parse()

	cfg, err := Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "loading configuration: %v\n", err)
		return err
	}

	log := observability.Logger(cfg.Logging.Level)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The dialect selects both halves of the engine boundary: the SQL dialect
	// (emitter) and the connection client (dbclient).
	dialect, err := emitter.Get(cfg.Engine.Dialect)
	if err != nil {
		log.Error("resolving dialect", "err", err)
		return err
	}
	engineCfg := toEngineConfig(cfg.Engine, cfg.Query)
	if engineCfg.Host == "" {
		err := errors.New("engine.connection.host is required")
		log.Error("reading engine connection config", "err", err)
		return err
	}
	engineClient, err := dbclient.Open(cfg.Engine.Dialect, engineCfg)
	if err != nil {
		log.Error("opening query engine client", "engine", cfg.Engine.Dialect, "err", err)
		return err
	}

	valkey := cache.New(toCacheOptions(cfg.Cache), log)
	if valkey == nil {
		log.Warn("cache.addr not set; running without plan/result caches")
	}

	tracer, shutdownTracing, err := observability.Tracer(ctx, "semantic-server", cfg.Observability.OTLPEndpoint)
	if err != nil {
		log.Error("configuring tracing", "err", err)
		return err
	}
	defer func() { _ = shutdownTracing(context.Background()) }()

	store := serving.NewStore()
	metrics := observability.NewMetrics()
	metrics.StoreSynced.Set(0)
	metrics.LoadedModels.Set(0)
	authorizers, err := buildAuthorizationRegistry(cfg.Authorization.Providers, "authorization.providers")
	if err != nil {
		log.Error("configuring external authorization", "err", err)
		return err
	}
	namespace := cfg.Store.WatchNamespace
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
		Store:             store,
		Dialect:           dialect,
		Cache:             valkey,
		DB:                engineClient,
		Metrics:           metrics,
		Log:               log,
		Tracer:            tracer,
		Authorization:     authorizers,
		ExposeExpressions: cfg.Store.ExposeMetricExpressions,
		Limits:            toLimits(cfg.Query),
	}

	// Identity resolution. header mode trusts X-Semantic-User and
	// X-Semantic-Role and requires an authenticating proxy in front; jwt mode
	// validates a bearer token and ignores both headers.
	authn, err := auth.New(ctx, toAuthOptions(cfg.Auth))
	if err != nil {
		log.Error("configuring authentication", "err", err)
		return err
	}
	if authn.Mode() == auth.ModeHeader {
		log.Warn("auth.mode=header: the X-Semantic-User and X-Semantic-Role headers are trusted verbatim; " +
			"put an authenticating proxy in front of this service or set auth.mode=jwt")
	}

	// Engine identity mode selects how a query authenticates to the engine.
	// static uses the server's own credential, passthrough forwards the
	// caller's validated token, exchange swaps it for an engine-audience token
	// (RFC 8693). passthrough and exchange require jwt auth and an engine user
	// claim.
	var resolver serving.CredentialResolver
	switch cfg.Engine.Identity.Mode {
	case "static":
		resolver = serving.StaticResolver()
	case "passthrough":
		if err := requireEngineIdentity(authn, engineClient, cfg.Engine.Dialect, cfg.Auth.EngineUserClaim); err != nil {
			log.Error("invalid engine identity configuration", "err", err)
			return err
		}
		resolver = serving.PassthroughResolver()
	case "exchange":
		if err := requireEngineIdentity(authn, engineClient, cfg.Engine.Dialect, cfg.Auth.EngineUserClaim); err != nil {
			log.Error("invalid engine identity configuration", "err", err)
			return err
		}
		ex, err := exchange.New(toExchangeOptions(cfg.Engine.Identity.Exchange, log))
		if err != nil {
			log.Error("invalid engine identity configuration", "err", err)
			return err
		}
		resolver = serving.ExchangeResolver(ex, cfg.Auth.EngineUserClaim)
	default:
		err := fmt.Errorf("unknown engine.identity.mode %q: use static, passthrough, or exchange", cfg.Engine.Identity.Mode)
		log.Error("invalid engine identity configuration", "err", err)
		return err
	}

	mux := http.NewServeMux()
	// MCP streams over HTTP, so it must not sit behind a response deadline.
	// REST always writes one bounded JSON document, so it gets a per-route
	// deadline instead of a global WriteTimeout that would cut MCP streams short.
	restTimeout := cfg.Server.RESTTimeout

	// One concurrency bound shared by both surfaces, held for the whole request:
	// a result stays allocated until the response is written, so releasing
	// earlier would let a slow client keep its result while the next request
	// takes the freed slot. This also covers cache hits.
	limits := svc.Limits.WithDefaults()
	limiter := serving.NewLimiter(limits.MaxConcurrentQueries, cfg.Query.QueueWait)
	log.Info("query concurrency bounded", "max_in_flight", limits.MaxConcurrentQueries,
		"max_result_bytes", limits.MaxResultBytes)

	mux.Handle("/mcp", limiter.Middleware(mcpadapter.Handler(svc, version, authn, resolver)))
	mux.Handle("/v1/", limiter.Middleware(http.TimeoutHandler(rest.Handler(svc, authn, resolver), restTimeout,
		`{"error":"request timed out"}`)))
	mux.Handle("/metrics", promhttp.Handler())
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("/readyz", readyzHandler(store, engineClient, cfg.Engine.Dialect))

	srv := &http.Server{
		Addr:    cfg.Server.ListenAddr,
		Handler: mux,
		// No WriteTimeout on purpose: it applies to every route, and MCP
		// streams responses, so a global write deadline would truncate them.
		ReadHeaderTimeout: cfg.Server.ReadHeaderTimeout,
		ReadTimeout:       cfg.Server.ReadTimeout,
		IdleTimeout:       cfg.Server.IdleTimeout,
		MaxHeaderBytes:    cfg.Server.MaxHeaderBytes,
	}
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	log.Info("semantic-server listening", "addr", cfg.Server.ListenAddr, "namespace", namespace, "version", version)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("server exited", "err", err)
		return err
	}
	return nil
}

// requireEngineIdentity enforces the preconditions shared by passthrough and
// exchange: jwt auth, a configured engine user claim, and an engine that
// executes each query under the per-request credential. The last check prevents
// a silent downgrade on engines that ignore the credential and would run every
// caller's query under the server identity.
func requireEngineIdentity(authn *auth.Authenticator, engineClient dbclient.Client, engine, engineUserClaim string) error {
	if authn.Mode() != auth.ModeJWT {
		return fmt.Errorf("engine.identity.mode passthrough or exchange requires auth.mode=jwt")
	}
	if engineUserClaim == "" {
		return fmt.Errorf("engine.identity.mode passthrough or exchange requires auth.engineUserClaim, the token claim used as the engine session user")
	}
	if _, ok := engineClient.(dbclient.PerRequestIdentityClient); !ok {
		return fmt.Errorf("engine.identity.mode passthrough or exchange requires an engine that honors per-request identity; engine %q runs every query under the server's own credential", engine)
	}
	return nil
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
