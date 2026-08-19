package main

import "time"

// Config is the full configuration surface of the semantic-server, grouped by
// concern. It is the single source of truth for every option the server
// accepts.
//
// Keys are camelCase for the YAML file. Each leaf carries an env tag naming the
// environment variable that overrides it; the effective variable is the env tag
// prefixed with SEMANTIC__ (for example SEMANTIC__ENGINE_HOST). See Load.
type Config struct {
	Logging       LoggingConfig       `yaml:"logging"`
	Server        ServerConfig        `yaml:"server"`
	Engine        EngineConfig        `yaml:"engine"`
	Auth          AuthConfig          `yaml:"auth"`
	Cache         CacheConfig         `yaml:"cache"`
	Query         QueryConfig         `yaml:"query"`
	Authorization AuthorizationConfig `yaml:"authorization"`
	Observability ObservabilityConfig `yaml:"observability"`
	Store         StoreConfig         `yaml:"store"`
}

type LoggingConfig struct {
	// Level is the slog level: debug, info, warn, or error.
	Level string `yaml:"level" env:"LOG_LEVEL"`
}

// ServerConfig configures the HTTP server. There is no global write timeout by
// design: MCP streams responses, so REST carries its own deadline (RESTTimeout).
type ServerConfig struct {
	ListenAddr        string        `yaml:"listenAddr" env:"LISTEN_ADDR"`
	ReadHeaderTimeout time.Duration `yaml:"readHeaderTimeout" env:"READ_HEADER_TIMEOUT"`
	ReadTimeout       time.Duration `yaml:"readTimeout" env:"READ_TIMEOUT"`
	IdleTimeout       time.Duration `yaml:"idleTimeout" env:"IDLE_TIMEOUT"`
	// RESTTimeout is the per-route deadline for REST responses; MCP is exempt.
	RESTTimeout    time.Duration `yaml:"restTimeout" env:"REST_TIMEOUT"`
	MaxHeaderBytes int           `yaml:"maxHeaderBytes" env:"MAX_HEADER_BYTES"`
}

type EngineConfig struct {
	// Dialect selects both the SQL dialect (emitter) and the connection client
	// (dbclient). Examples: starrocks, trino.
	Dialect    string               `yaml:"dialect" env:"ENGINE_DIALECT"`
	Connection ConnectionConfig     `yaml:"connection"`
	Identity   EngineIdentityConfig `yaml:"identity"`
}

// ConnectionConfig carries engine connection settings. Engine factories apply
// their own defaults for zero values (for example the default port).
type ConnectionConfig struct {
	// Host has no default and is required.
	Host string `yaml:"host" env:"ENGINE_HOST"`
	Port int    `yaml:"port" env:"ENGINE_PORT"`
	// User and Password are the server's own credential, used in static
	// identity mode and as the fallback identity.
	User         string        `yaml:"user" env:"ENGINE_USER"`
	Password     string        `yaml:"password" env:"ENGINE_PASSWORD"`
	QueryTimeout time.Duration `yaml:"queryTimeout" env:"ENGINE_QUERY_TIMEOUT"`
	// TLSEnabled forces HTTPS, needed when the engine requires TLS but no
	// password is set. A password also implies HTTPS.
	TLSEnabled bool `yaml:"tlsEnabled" env:"ENGINE_TLS_ENABLED"`
	// TLSInsecureSkipVerify disables certificate verification; development only.
	TLSInsecureSkipVerify bool `yaml:"tlsInsecureSkipVerify" env:"ENGINE_TLS_INSECURE_SKIP_VERIFY"`
}

// EngineIdentityConfig selects how a query authenticates to the engine. static
// uses the server credential, passthrough forwards the caller token, exchange
// swaps it for an engine-audience token (RFC 8693). passthrough and exchange
// require jwt auth and AuthConfig.EngineUserClaim.
type EngineIdentityConfig struct {
	// Mode is static, passthrough, or exchange.
	Mode     string         `yaml:"mode" env:"ENGINE_IDENTITY_MODE"`
	Exchange ExchangeConfig `yaml:"exchange"`
}

type ExchangeConfig struct {
	// TokenURL must be https unless AllowInsecureHTTP is set.
	TokenURL     string `yaml:"tokenURL" env:"ENGINE_EXCHANGE_TOKEN_URL"`
	ClientID     string `yaml:"clientID" env:"ENGINE_EXCHANGE_CLIENT_ID"`
	ClientSecret string `yaml:"clientSecret" env:"ENGINE_EXCHANGE_CLIENT_SECRET"`
	// AllowInsecureHTTP permits a plaintext http TokenURL. The exchange sends
	// the caller token and client secret, so keep this false unless the
	// endpoint is reached only over trusted in-cluster networking.
	AllowInsecureHTTP bool `yaml:"allowInsecureHTTP" env:"ENGINE_EXCHANGE_ALLOW_INSECURE_HTTP"`
}

// AuthConfig configures identity resolution. header mode trusts the
// X-Semantic-User and X-Semantic-Role headers and needs an authenticating
// proxy in front. jwt mode validates a bearer token and ignores those headers.
type AuthConfig struct {
	// Mode is header or jwt.
	Mode string `yaml:"mode" env:"AUTH_MODE"`
	// JWKSURL, Issuer, and Audience are required in jwt mode.
	JWKSURL  string `yaml:"jwksURL" env:"AUTH_JWKS_URL"`
	Issuer   string `yaml:"issuer" env:"AUTH_ISSUER"`
	Audience string `yaml:"audience" env:"AUTH_AUDIENCE"`
	// PrincipalClaim, RoleClaim, and GroupsClaim map token claims to identity
	// fields; empty uses the auth package defaults.
	PrincipalClaim string   `yaml:"principalClaim" env:"AUTH_PRINCIPAL_CLAIM"`
	RoleClaim      string   `yaml:"roleClaim" env:"AUTH_ROLE_CLAIM"`
	GroupsClaim    string   `yaml:"groupsClaim" env:"AUTH_GROUPS_CLAIM"`
	ClaimsToCopy   []string `yaml:"claimsToCopy" env:"AUTH_CLAIMS_TO_COPY"`
	// EngineUserClaim is the token claim used as the engine session user,
	// required by the passthrough and exchange identity modes.
	EngineUserClaim string `yaml:"engineUserClaim" env:"AUTH_ENGINE_USER_CLAIM"`
}

// CacheConfig configures the Valkey caches. An empty Addr disables caching.
type CacheConfig struct {
	Addr      string        `yaml:"addr" env:"CACHE_ADDR"`
	Password  string        `yaml:"password" env:"CACHE_PASSWORD"`
	DB        int           `yaml:"db" env:"CACHE_DB"`
	PlanTTL   time.Duration `yaml:"planTTL" env:"CACHE_PLAN_TTL"`
	ResultTTL time.Duration `yaml:"resultTTL" env:"CACHE_RESULT_TTL"`
}

// QueryConfig bounds query shape, result size, and concurrency. A zero limit
// means the built-in default applies, never "unbounded".
type QueryConfig struct {
	DefaultRowLimit int `yaml:"defaultRowLimit" env:"QUERY_DEFAULT_ROW_LIMIT"`
	MaxRowLimit     int `yaml:"maxRowLimit" env:"QUERY_MAX_ROW_LIMIT"`
	MaxMetrics      int `yaml:"maxMetrics" env:"QUERY_MAX_METRICS"`
	MaxDimensions   int `yaml:"maxDimensions" env:"QUERY_MAX_DIMENSIONS"`
	MaxFilters      int `yaml:"maxFilters" env:"QUERY_MAX_FILTERS"`
	MaxFilterValues int `yaml:"maxFilterValues" env:"QUERY_MAX_FILTER_VALUES"`
	// MaxResultBytes bounds one result while it is read; it bounds both the
	// engine client and the service, which must share the same ceiling.
	MaxResultBytes     int `yaml:"maxResultBytes" env:"QUERY_MAX_RESULT_BYTES"`
	MaxCacheEntryBytes int `yaml:"maxCacheEntryBytes" env:"QUERY_MAX_CACHE_ENTRY_BYTES"`
	MaxRequestBytes    int `yaml:"maxRequestBytes" env:"QUERY_MAX_REQUEST_BYTES"`
	MaxConcurrent      int `yaml:"maxConcurrent" env:"QUERY_MAX_CONCURRENT"`
	// QueueWait bounds how long a query waits for a concurrency slot.
	QueueWait time.Duration `yaml:"queueWait" env:"QUERY_QUEUE_WAIT"`
}

// AuthorizationConfig configures the external authorization (PDP) providers,
// which are additive to compile-time governance and fail closed. The provider
// list is file-only (not env-settable); bearer tokens are referenced by env var
// name (BearerTokenEnv), not held here.
type AuthorizationConfig struct {
	Providers []authorizationProviderConfig `yaml:"providers"`
}

type ObservabilityConfig struct {
	// OTLPEndpoint is the OpenTelemetry OTLP endpoint; empty disables tracing.
	OTLPEndpoint string `yaml:"otlpEndpoint" env:"OTLP_ENDPOINT"`
}

// StoreConfig configures the compiled-model store and what the served API
// exposes about models.
type StoreConfig struct {
	WatchNamespace string `yaml:"watchNamespace" env:"WATCH_NAMESPACE"`
	// ExposeMetricExpressions includes raw metric SQL in metric listings; off by
	// default since a listing is meant to ground an agent on certified names.
	ExposeMetricExpressions bool `yaml:"exposeMetricExpressions" env:"EXPOSE_METRIC_EXPRESSIONS"`
}
