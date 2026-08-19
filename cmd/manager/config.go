package main

import "time"

// Config is the controller manager's configuration surface, grouped by concern.
// A loader maps it onto ctrl.Options and the reconciler, since controller-runtime
// v0.24 no longer ingests a config object directly.
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
	BindAddress string `yaml:"bindAddress"`
}

type HealthConfig struct {
	HealthProbeBindAddress string `yaml:"healthProbeBindAddress"`
}

// LeaderElectionConfig configures leader election for HA. The duration fields
// map onto controller-runtime's *time.Duration options, where a zero value
// means "use the built-in default", so the loader must not pass a zero through.
type LeaderElectionConfig struct {
	LeaderElect bool `yaml:"leaderElect"`
	// ResourceName is the Lease name (controller-runtime's LeaderElectionID).
	ResourceName      string        `yaml:"resourceName"`
	ResourceNamespace string        `yaml:"resourceNamespace"`
	LeaseDuration     time.Duration `yaml:"leaseDuration"`
	RenewDeadline     time.Duration `yaml:"renewDeadline"`
	RetryPeriod       time.Duration `yaml:"retryPeriod"`
}

type CacheConfig struct {
	// WatchNamespaces is a fixed namespace list to watch. Empty means
	// cluster-wide (a ClusterRole); narrowing it limits the manager's blast radius.
	WatchNamespaces []string `yaml:"watchNamespaces"`
}

type EngineConfig struct {
	// Dialect selects both the SQL dialect (emitter) and the connection client
	// (dbclient). Examples: starrocks, trino.
	Dialect    string           `yaml:"dialect"`
	Connection ConnectionConfig `yaml:"connection"`
}

// ConnectionConfig carries engine connection settings. Engine factories apply
// their own defaults for zero values (for example the default port).
type ConnectionConfig struct {
	// Host has no default and is required.
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
	User string `yaml:"user"`
	// Password should come from a Secret-backed env var, not the config document.
	Password     string        `yaml:"password"`
	QueryTimeout time.Duration `yaml:"queryTimeout"`
	// TLSEnabled forces HTTPS, needed when the engine requires TLS but no
	// password is set. A password also implies HTTPS.
	TLSEnabled bool `yaml:"tlsEnabled"`
	// TLSInsecureSkipVerify disables certificate verification; development only.
	TLSInsecureSkipVerify bool `yaml:"tlsInsecureSkipVerify"`
}

type ControllerConfig struct {
	// ViewDatabase is the engine schema the manager owns for governed views.
	ViewDatabase string `yaml:"viewDatabase"`
	// ResyncPeriod is how often the reconciler re-runs drift checks absent an event.
	ResyncPeriod time.Duration `yaml:"resyncPeriod"`
}

type LoggingConfig struct {
	// Development selects the zap development logger (human-readable, verbose).
	Development bool `yaml:"development"`
	// Level is the log level: debug, info, warn, or error.
	Level string `yaml:"level"`
}
