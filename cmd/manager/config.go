package main

import "time"

// Config is the controller manager's configuration surface, grouped by concern.
// A loader maps it onto ctrl.Options and the reconciler, since controller-runtime
// v0.24 no longer ingests a config object directly.
//
// Each leaf carries an env tag; the effective variable is the tag prefixed with
// SEMANTIC__ (for example SEMANTIC__ENGINE_HOST).
type Config struct {
	Metrics        MetricsConfig        `yaml:"metrics"`
	Health         HealthConfig         `yaml:"health"`
	LeaderElection LeaderElectionConfig `yaml:"leaderElection"`
	Cache          CacheConfig          `yaml:"cache"`
	Engine         EngineConfig         `yaml:"engine"`
	Controller     ControllerConfig     `yaml:"controller"`
	Logging        LoggingConfig        `yaml:"logging"`
}

type MetricsConfig struct {
	// BindAddress is the metrics endpoint address, for example ":8080"; "0" disables it.
	BindAddress string `yaml:"bindAddress" env:"METRICS_BIND_ADDRESS"`
}

type HealthConfig struct {
	HealthProbeBindAddress string `yaml:"healthProbeBindAddress" env:"HEALTH_PROBE_BIND_ADDRESS"`
}

// LeaderElectionConfig configures leader election for HA. The duration fields
// map onto controller-runtime's *time.Duration options, where a zero value
// means "use the built-in default", so the loader must not pass a zero through.
type LeaderElectionConfig struct {
	LeaderElect bool `yaml:"leaderElect" env:"LEADER_ELECT"`
	// ResourceName is the Lease name (controller-runtime's LeaderElectionID).
	ResourceName      string        `yaml:"resourceName" env:"LEADER_ELECTION_RESOURCE_NAME"`
	ResourceNamespace string        `yaml:"resourceNamespace" env:"LEADER_ELECTION_RESOURCE_NAMESPACE"`
	LeaseDuration     time.Duration `yaml:"leaseDuration" env:"LEADER_ELECTION_LEASE_DURATION"`
	RenewDeadline     time.Duration `yaml:"renewDeadline" env:"LEADER_ELECTION_RENEW_DEADLINE"`
	RetryPeriod       time.Duration `yaml:"retryPeriod" env:"LEADER_ELECTION_RETRY_PERIOD"`
}

type CacheConfig struct {
	// WatchNamespaces is a fixed namespace list to watch. Empty means
	// cluster-wide (a ClusterRole); narrowing it limits the manager's blast radius.
	WatchNamespaces []string `yaml:"watchNamespaces" env:"WATCH_NAMESPACES"`
}

type EngineConfig struct {
	// Dialect selects both the SQL dialect (emitter) and the connection client
	// (dbclient). Examples: starrocks, trino.
	Dialect    string           `yaml:"dialect" env:"ENGINE_DIALECT"`
	Connection ConnectionConfig `yaml:"connection"`
}

// ConnectionConfig carries engine connection settings. Engine factories apply
// their own defaults for zero values (for example the default port).
type ConnectionConfig struct {
	// Host has no default and is required.
	Host string `yaml:"host" env:"ENGINE_HOST"`
	Port int    `yaml:"port" env:"ENGINE_PORT"`
	User string `yaml:"user" env:"ENGINE_USER"`
	// Password should come from a Secret-backed env var, not the config document.
	Password     string        `yaml:"password" env:"ENGINE_PASSWORD"`
	QueryTimeout time.Duration `yaml:"queryTimeout" env:"ENGINE_QUERY_TIMEOUT"`
	// TLSEnabled forces HTTPS, needed when the engine requires TLS but no
	// password is set. A password also implies HTTPS.
	TLSEnabled bool `yaml:"tlsEnabled" env:"ENGINE_TLS_ENABLED"`
	// TLSInsecureSkipVerify disables certificate verification; development only.
	TLSInsecureSkipVerify bool `yaml:"tlsInsecureSkipVerify" env:"ENGINE_TLS_INSECURE_SKIP_VERIFY"`
}

type ControllerConfig struct {
	// ViewDatabase is the engine schema the manager owns for governed views.
	ViewDatabase string `yaml:"viewDatabase" env:"VIEW_DATABASE"`
	// ResyncPeriod is how often the reconciler re-runs drift checks absent an event.
	ResyncPeriod time.Duration `yaml:"resyncPeriod" env:"RESYNC_PERIOD"`
}

type LoggingConfig struct {
	// Development selects the zap development logger (human-readable, verbose).
	Development bool `yaml:"development" env:"LOG_DEVELOPMENT"`
	// Level is the log level: debug, info, warn, or error.
	Level string `yaml:"level" env:"LOG_LEVEL"`
}
