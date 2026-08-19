package main

import (
	"time"

	"github.com/KubedAI/semantic-operator/internal/confload"
)

// defaults returns the built-in manager configuration so the binary is runnable
// with no config file; the file and env layers override only what they set.
//
// The leader-election durations are left at zero on purpose: zero means "use
// controller-runtime's default", and the mapping onto ctrl.Options must leave
// those nil rather than pass a literal zero.
func defaults() Config {
	return Config{
		Metrics: MetricsConfig{BindAddress: ":8080"},
		Health:  HealthConfig{HealthProbeBindAddress: ":8081"},
		LeaderElection: LeaderElectionConfig{
			LeaderElect:  false,
			ResourceName: "semantic-operator.semantic.ossie.io",
		},
		Engine: EngineConfig{Dialect: "starrocks"},
		Controller: ControllerConfig{
			ViewDatabase: "semantic_views",
			ResyncPeriod: 5 * time.Minute,
		},
		Logging: LoggingConfig{Level: "info"},
	}
}

// Load builds the manager configuration from defaults, an optional YAML config
// file at path, and SEMANTIC__ environment overrides. See the confload package.
// The controller-runtime flags remain the highest-precedence layer, folded in
// and mapped onto ctrl.Options when main wires the manager.
func Load(path string) (Config, error) {
	return confload.Load(defaults(), path)
}
