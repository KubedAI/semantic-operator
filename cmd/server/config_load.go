package main

import (
	"time"

	"github.com/KubedAI/semantic-operator/internal/confload"
)

// defaults returns the built-in configuration so the binary is runnable with no
// config file; the file and env layers override only what they set. Query
// limits left at zero fall back to the serving package's built-in bounds.
func defaults() Config {
	return Config{
		Logging: LoggingConfig{Level: "info"},
		Server: ServerConfig{
			ListenAddr:        ":8090",
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			IdleTimeout:       120 * time.Second,
			RESTTimeout:       60 * time.Second,
			MaxHeaderBytes:    1 << 20,
		},
		Engine: EngineConfig{
			Dialect:  "starrocks",
			Identity: EngineIdentityConfig{Mode: "static"},
		},
		Auth: AuthConfig{Mode: "header"},
		Cache: CacheConfig{
			PlanTTL:   24 * time.Hour,
			ResultTTL: 60 * time.Second,
		},
		Query: QueryConfig{
			QueueWait: 5 * time.Second,
		},
		Store: StoreConfig{
			WatchNamespace: "semantic-system",
		},
	}
}

// Load builds the server configuration from defaults, an optional YAML config
// file at path, and SEMANTIC__ environment overrides. See the confload package.
func Load(path string) (Config, error) {
	return confload.Load(defaults(), path)
}
