package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Metrics.BindAddress != ":8080" {
		t.Errorf("metrics.bindAddress = %q, want :8080", cfg.Metrics.BindAddress)
	}
	if cfg.Health.HealthProbeBindAddress != ":8081" {
		t.Errorf("health.healthProbeBindAddress = %q, want :8081", cfg.Health.HealthProbeBindAddress)
	}
	if cfg.LeaderElection.ResourceName != "semantic-operator.semantic.ossie.io" {
		t.Errorf("leaderElection.resourceName = %q", cfg.LeaderElection.ResourceName)
	}
	if cfg.LeaderElection.LeaderElect {
		t.Error("leaderElection.leaderElect = true, want false")
	}
	if cfg.Engine.Dialect != "starrocks" {
		t.Errorf("engine.dialect = %q, want starrocks", cfg.Engine.Dialect)
	}
	if cfg.Controller.ViewDatabase != "semantic_views" {
		t.Errorf("controller.viewDatabase = %q, want semantic_views", cfg.Controller.ViewDatabase)
	}
	if cfg.Controller.ResyncPeriod != 5*time.Minute {
		t.Errorf("controller.resyncPeriod = %v, want 5m", cfg.Controller.ResyncPeriod)
	}
}

func TestLoadFileOverridesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	doc := `
metrics:
  bindAddress: ":9090"
leaderElection:
  leaderElect: true
  leaseDuration: 20s
cache:
  watchNamespaces: [team-a, team-b]
engine:
  dialect: trino
  connection:
    host: trino.svc
    port: 8443
    tlsEnabled: true
controller:
  viewDatabase: views2
  resyncPeriod: 10m
logging:
  development: true
  level: debug
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Metrics.BindAddress != ":9090" {
		t.Errorf("metrics.bindAddress = %q, want :9090", cfg.Metrics.BindAddress)
	}
	if !cfg.LeaderElection.LeaderElect || cfg.LeaderElection.LeaseDuration != 20*time.Second {
		t.Errorf("leaderElection = %+v", cfg.LeaderElection)
	}
	if len(cfg.Cache.WatchNamespaces) != 2 || cfg.Cache.WatchNamespaces[0] != "team-a" {
		t.Errorf("cache.watchNamespaces = %v", cfg.Cache.WatchNamespaces)
	}
	if cfg.Engine.Dialect != "trino" || cfg.Engine.Connection.Host != "trino.svc" || cfg.Engine.Connection.Port != 8443 {
		t.Errorf("engine = %+v", cfg.Engine)
	}
	if !cfg.Engine.Connection.TLSEnabled {
		t.Error("engine.connection.tlsEnabled = false, want true")
	}
	if cfg.Controller.ViewDatabase != "views2" || cfg.Controller.ResyncPeriod != 10*time.Minute {
		t.Errorf("controller = %+v", cfg.Controller)
	}
	if !cfg.Logging.Development || cfg.Logging.Level != "debug" {
		t.Errorf("logging = %+v", cfg.Logging)
	}
	// Not set by the file, so the default survives.
	if cfg.Health.HealthProbeBindAddress != ":8081" {
		t.Errorf("health.healthProbeBindAddress = %q, want default :8081", cfg.Health.HealthProbeBindAddress)
	}
}

func TestLoadEnvOverridesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	doc := `
metrics:
  bindAddress: ":9090"
engine:
  connection:
    host: from-file
controller:
  resyncPeriod: 1m
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("SEMANTIC__METRICS_BIND_ADDRESS", ":7070")
	t.Setenv("SEMANTIC__ENGINE_HOST", "from-env")
	t.Setenv("SEMANTIC__RESYNC_PERIOD", "3m")
	t.Setenv("SEMANTIC__LEADER_ELECT", "true")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Metrics.BindAddress != ":7070" {
		t.Errorf("metrics.bindAddress = %q, want env :7070", cfg.Metrics.BindAddress)
	}
	if cfg.Engine.Connection.Host != "from-env" {
		t.Errorf("engine.connection.host = %q, want env from-env", cfg.Engine.Connection.Host)
	}
	if cfg.Controller.ResyncPeriod != 3*time.Minute {
		t.Errorf("controller.resyncPeriod = %v, want env 3m", cfg.Controller.ResyncPeriod)
	}
	if !cfg.LeaderElection.LeaderElect {
		t.Error("leaderElection.leaderElect = false, want env true")
	}
}
