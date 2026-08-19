package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.ListenAddr != ":8090" {
		t.Errorf("listen_addr = %q, want :8090", cfg.Server.ListenAddr)
	}
	if cfg.Server.ReadTimeout != 30*time.Second {
		t.Errorf("read_timeout = %v, want 30s", cfg.Server.ReadTimeout)
	}
	if cfg.Engine.Dialect != "starrocks" {
		t.Errorf("engine.dialect = %q, want starrocks", cfg.Engine.Dialect)
	}
	if cfg.Engine.Identity.Mode != "static" {
		t.Errorf("engine.identity.mode = %q, want static", cfg.Engine.Identity.Mode)
	}
	if cfg.Auth.Mode != "header" {
		t.Errorf("auth.mode = %q, want header", cfg.Auth.Mode)
	}
	if cfg.Cache.PlanTTL != 24*time.Hour {
		t.Errorf("cache.plan_ttl = %v, want 24h", cfg.Cache.PlanTTL)
	}
	if cfg.Store.WatchNamespace != "semantic-system" {
		t.Errorf("store.watch_namespace = %q, want semantic-system", cfg.Store.WatchNamespace)
	}
}

func TestLoadFileOverridesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	doc := `
logging:
  level: debug
server:
  listenAddr: ":9000"
  readTimeout: 45s
engine:
  dialect: trino
  connection:
    host: trino.svc
    port: 8443
    tlsEnabled: true
  identity:
    mode: exchange
    exchange:
      tokenURL: https://idp/token
      clientID: semantic-server
auth:
  mode: jwt
  jwksURL: https://idp/jwks
  issuer: https://idp
  audience: semantic-api
  claimsToCopy: [tenant, sub]
query:
  maxConcurrent: 12
store:
  exposeMetricExpressions: true
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	// Overridden by the file.
	if cfg.Logging.Level != "debug" {
		t.Errorf("logging.level = %q, want debug", cfg.Logging.Level)
	}
	if cfg.Server.ListenAddr != ":9000" {
		t.Errorf("listen_addr = %q, want :9000", cfg.Server.ListenAddr)
	}
	if cfg.Server.ReadTimeout != 45*time.Second {
		t.Errorf("read_timeout = %v, want 45s", cfg.Server.ReadTimeout)
	}
	if cfg.Engine.Dialect != "trino" || cfg.Engine.Connection.Host != "trino.svc" || cfg.Engine.Connection.Port != 8443 {
		t.Errorf("engine = %+v", cfg.Engine)
	}
	if !cfg.Engine.Connection.TLSEnabled {
		t.Error("engine.connection.tls_enabled = false, want true")
	}
	if cfg.Engine.Identity.Mode != "exchange" || cfg.Engine.Identity.Exchange.TokenURL != "https://idp/token" {
		t.Errorf("engine.identity = %+v", cfg.Engine.Identity)
	}
	if cfg.Auth.Mode != "jwt" || cfg.Auth.Issuer != "https://idp" || cfg.Auth.Audience != "semantic-api" {
		t.Errorf("auth = %+v", cfg.Auth)
	}
	if len(cfg.Auth.ClaimsToCopy) != 2 || cfg.Auth.ClaimsToCopy[0] != "tenant" {
		t.Errorf("auth.claims_to_copy = %v", cfg.Auth.ClaimsToCopy)
	}
	if cfg.Query.MaxConcurrent != 12 {
		t.Errorf("query.max_concurrent = %d, want 12", cfg.Query.MaxConcurrent)
	}
	if !cfg.Store.ExposeMetricExpressions {
		t.Error("store.expose_metric_expressions = false, want true")
	}
	// Not set by the file, so the default survives.
	if cfg.Server.IdleTimeout != 120*time.Second {
		t.Errorf("idle_timeout = %v, want default 120s", cfg.Server.IdleTimeout)
	}
}

func TestLoadEnvOverridesFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	doc := `
server:
  listenAddr: ":9000"
engine:
  connection:
    host: from-file
query:
  maxConcurrent: 5
store:
  exposeMetricExpressions: false
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}

	// Conventional uppercase env vars with underscores; matched case- and
	// underscore-insensitively. String, nested string, int, bool, duration.
	t.Setenv("SEMANTIC__SERVER__LISTEN_ADDR", ":7777")
	t.Setenv("SEMANTIC__ENGINE__CONNECTION__HOST", "from-env")
	t.Setenv("SEMANTIC__QUERY__MAX_CONCURRENT", "20")
	t.Setenv("SEMANTIC__STORE__EXPOSE_METRIC_EXPRESSIONS", "true")
	t.Setenv("SEMANTIC__SERVER__IDLE_TIMEOUT", "300s")

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.ListenAddr != ":7777" {
		t.Errorf("listen_addr = %q, want env :7777", cfg.Server.ListenAddr)
	}
	if cfg.Engine.Connection.Host != "from-env" {
		t.Errorf("engine.connection.host = %q, want env from-env", cfg.Engine.Connection.Host)
	}
	if cfg.Query.MaxConcurrent != 20 {
		t.Errorf("query.max_concurrent = %d, want env 20", cfg.Query.MaxConcurrent)
	}
	if !cfg.Store.ExposeMetricExpressions {
		t.Error("store.expose_metric_expressions = false, want env true")
	}
	if cfg.Server.IdleTimeout != 300*time.Second {
		t.Errorf("idle_timeout = %v, want env 300s", cfg.Server.IdleTimeout)
	}
}

func TestLoadProvidersFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	doc := `
engine:
  connection:
    host: h
authorization:
  providers:
    - name: corp-opa
      type: opa
      url: https://opa:8181
      opa:
        decisionPath: semantic/query/allow
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Authorization.Providers) != 1 {
		t.Fatalf("providers = %+v, want one", cfg.Authorization.Providers)
	}
	p := cfg.Authorization.Providers[0]
	if p.Name != "corp-opa" || p.Type != "opa" || p.OPA == nil || p.OPA.DecisionPath != "semantic/query/allow" {
		t.Fatalf("provider = %+v", p)
	}
}

func TestLoadRejectsUnknownProviderField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	doc := `
authorization:
  providers:
    - name: corp-opa
      type: opa
      url: https://opa:8181
      bogusField: x
      opa:
        decisionPath: allow
`
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("expected an error for an unknown provider field, got nil")
	}
	if !strings.Contains(err.Error(), "bogusField") {
		t.Errorf("error = %v, want it to name the unknown provider field", err)
	}
}
