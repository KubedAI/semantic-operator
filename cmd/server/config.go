package main

import "time"

// Config is the semantic-server configuration surface, grouped by concern.
//
// Keys are camelCase. Environment variables override case-insensitively and
// ignoring underscores, so SEMANTIC__ENGINE__CONNECTION__HOST overrides
// engine.connection.host. See Load for layering and precedence.
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
	Level string `yaml:"level"`
}

// ServerConfig configures the HTTP server. There is no global write timeout by
// design: MCP streams responses, so REST carries its own deadline (RESTTimeout).
type ServerConfig struct {
	ListenAddr        string        `yaml:"listenAddr"`
	ReadHeaderTimeout time.Duration `yaml:"readHeaderTimeout"`
	ReadTimeout       time.Duration `yaml:"readTimeout"`
	IdleTimeout       time.Duration `yaml:"idleTimeout"`
	// RESTTimeout is the per-route deadline for REST responses; MCP is exempt.
	RESTTimeout    time.Duration `yaml:"restTimeout"`
	MaxHeaderBytes int           `yaml:"maxHeaderBytes"`
}

type EngineConfig struct {
	// Dialect selects both the SQL dialect (emitter) and the connection client
	// (dbclient). Examples: starrocks, trino.
	Dialect    string               `yaml:"dialect"`
	Connection ConnectionConfig     `yaml:"connection"`
	Identity   EngineIdentityConfig `yaml:"identity"`
}

// ConnectionConfig carries engine connection settings. Engine factories apply
// their own defaults for zero values (for example the default port).
type ConnectionConfig struct {
	// Host has no default and is required.
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
	// User and Password are the server's own credential, used in static
	// identity mode and as the fallback identity.
	User         string        `yaml:"user"`
	Password     string        `yaml:"password"`
	QueryTimeout time.Duration `yaml:"queryTimeout"`
	// TLSEnabled forces HTTPS, needed when the engine requires TLS but no
	// password is set. A password also implies HTTPS.
	TLSEnabled bool `yaml:"tlsEnabled"`
	// TLSInsecureSkipVerify disables certificate verification; development only.
	TLSInsecureSkipVerify bool `yaml:"tlsInsecureSkipVerify"`
}

// EngineIdentityConfig selects how a query authenticates to the engine. static
// uses the server credential, passthrough forwards the caller token, exchange
// swaps it for an engine-audience token (RFC 8693). passthrough and exchange
// require jwt auth and AuthConfig.EngineUserClaim.
type EngineIdentityConfig struct {
	// Mode is static, passthrough, or exchange.
	Mode     string         `yaml:"mode"`
	Exchange ExchangeConfig `yaml:"exchange"`
}

type ExchangeConfig struct {
	// TokenURL must be https unless AllowInsecureHTTP is set.
	TokenURL     string `yaml:"tokenURL"`
	ClientID     string `yaml:"clientID"`
	ClientSecret string `yaml:"clientSecret"`
	// AllowInsecureHTTP permits a plaintext http TokenURL. The exchange sends
	// the caller token and client secret, so keep this false unless the
	// endpoint is reached only over trusted in-cluster networking.
	AllowInsecureHTTP bool `yaml:"allowInsecureHTTP"`
}

// AuthConfig configures identity resolution. header mode trusts the
// X-Semantic-User and X-Semantic-Role headers and needs an authenticating
// proxy in front. jwt mode validates a bearer token and ignores those headers.
type AuthConfig struct {
	// Mode is header or jwt.
	Mode string `yaml:"mode"`
	// JWKSURL, Issuer, and Audience are required in jwt mode.
	JWKSURL  string `yaml:"jwksURL"`
	Issuer   string `yaml:"issuer"`
	Audience string `yaml:"audience"`
	// PrincipalClaim, RoleClaim, and GroupsClaim map token claims to identity
	// fields; empty uses the auth package defaults.
	PrincipalClaim string   `yaml:"principalClaim"`
	RoleClaim      string   `yaml:"roleClaim"`
	GroupsClaim    string   `yaml:"groupsClaim"`
	ClaimsToCopy   []string `yaml:"claimsToCopy"`
	// EngineUserClaim is the token claim used as the engine session user,
	// required by the passthrough and exchange identity modes.
	EngineUserClaim string `yaml:"engineUserClaim"`
}

// CacheConfig configures the Valkey caches. An empty Addr disables caching.
type CacheConfig struct {
	Addr      string        `yaml:"addr"`
	Password  string        `yaml:"password"`
	DB        int           `yaml:"db"`
	PlanTTL   time.Duration `yaml:"planTTL"`
	ResultTTL time.Duration `yaml:"resultTTL"`
}

// QueryConfig bounds query shape, result size, and concurrency. A zero limit
// means the built-in default applies, never "unbounded".
type QueryConfig struct {
	DefaultRowLimit int `yaml:"defaultRowLimit"`
	MaxRowLimit     int `yaml:"maxRowLimit"`
	MaxMetrics      int `yaml:"maxMetrics"`
	MaxDimensions   int `yaml:"maxDimensions"`
	MaxFilters      int `yaml:"maxFilters"`
	MaxFilterValues int `yaml:"maxFilterValues"`
	// MaxResultBytes bounds one result while it is read; it bounds both the
	// engine client and the service, which must share the same ceiling.
	MaxResultBytes     int `yaml:"maxResultBytes"`
	MaxCacheEntryBytes int `yaml:"maxCacheEntryBytes"`
	MaxRequestBytes    int `yaml:"maxRequestBytes"`
	MaxConcurrent      int `yaml:"maxConcurrent"`
	// QueueWait bounds how long a query waits for a concurrency slot.
	QueueWait time.Duration `yaml:"queueWait"`
}

// AuthorizationConfig configures the external authorization (PDP) providers,
// which are additive to compile-time governance and fail closed. Provider
// bearer tokens are referenced by env var name (BearerTokenEnv), not held here.
type AuthorizationConfig struct {
	Providers []authorizationProviderConfig `yaml:"providers"`
}

type ObservabilityConfig struct {
	// OTLPEndpoint is the OpenTelemetry OTLP endpoint; empty disables tracing.
	OTLPEndpoint string `yaml:"otlpEndpoint"`
}

type StoreConfig struct {
	// WatchNamespace is the namespace whose compiled-model ConfigMaps are watched.
	WatchNamespace string `yaml:"watchNamespace"`
	// ExposeMetricExpressions includes raw metric SQL in metric listings; off by
	// default since a listing is meant to ground an agent on certified names.
	ExposeMetricExpressions bool `yaml:"exposeMetricExpressions"`
}
