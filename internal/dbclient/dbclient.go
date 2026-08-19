// Package dbclient defines the query-engine connection boundary and the
// factory that selects a concrete client by engine name. It is the transport
// half of engine pluggability: emitter.Dialect renders SQL text for an
// engine, dbclient carries it there. One SQL_DIALECT value selects both.
//
// Implementations register themselves from their package init(), mirroring
// database/sql drivers and emitter.Register, so binaries choose engines by
// blank import:
//
//	_ "github.com/KubedAI/semantic-operator/internal/starrocks"
//	_ "github.com/KubedAI/semantic-operator/internal/trino"
package dbclient

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Column is one physical column as reported by engine introspection.
type Column struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Client is the engine surface the operator and server need. Implementations
// must be safe for concurrent use.
type Client interface {
	// Query runs a statement and returns column names plus JSON-friendly rows
	// ([]byte values are converted to string so results marshal cleanly). The
	// credential selects the execution identity; a zero credential uses the
	// client's own static credential.
	Query(ctx context.Context, cred EngineCredential, sql string) ([]string, [][]any, error)
	// Exec runs DDL or other statements without results.
	Exec(ctx context.Context, sql string) error
	// Ping verifies connectivity for readiness probes.
	Ping(ctx context.Context) error
	// DescribeTable introspects a table as the engine sees it (the drift
	// detection primitive). A missing table must return an error, never an
	// empty column list, so the reconciler can tell drift from an empty table.
	DescribeTable(ctx context.Context, catalog, database, table string) ([]Column, error)
	// Close releases the underlying connection pool.
	Close() error
}

// Config carries connection settings common to every engine. Engine factories
// apply their own defaults for zero values (e.g. the default port).
type Config struct {
	Host     string
	Port     int
	User     string
	Password string
	// QueryTimeout bounds a single statement. Default 60s.
	QueryTimeout time.Duration
	// MaxResultBytes bounds what one result may allocate while it is being
	// read. Zero means DefaultMaxResultBytes, never unbounded.
	MaxResultBytes int
	// TLSEnabled forces an HTTPS engine connection. It is needed when the
	// engine requires TLS but no password is set, for example token-based
	// authentication. A password also implies HTTPS.
	TLSEnabled bool
	// TLSInsecureSkipVerify disables server certificate verification for the
	// engine connection. It is for isolated development against a self-signed
	// engine only, and must stay false in production. Honored by the Trino
	// client; the StarRocks client does not yet consume it.
	TLSInsecureSkipVerify bool
}

// Factory builds a client for one engine.
type Factory func(cfg Config) (Client, error)

// PerRequestIdentityClient is implemented by engine clients that execute each
// query under the EngineCredential passed to Query, rather than under a fixed
// connection identity. Engines that ignore the credential must not implement
// it, so the server can refuse passthrough and exchange for them instead of
// silently running every caller's query under the server's own identity.
type PerRequestIdentityClient interface {
	// SupportsPerRequestIdentity is a marker; its presence signals the
	// capability.
	SupportsPerRequestIdentity()
}

var registry = map[string]Factory{}

// Register adds an engine factory. Called from implementation package init().
func Register(name string, f Factory) { registry[name] = f }

// Open builds a client for the named engine.
func Open(name string, cfg Config) (Client, error) {
	f, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown query engine %q (registered: %v)", name, Registered())
	}
	return f(cfg)
}

// Registered returns the registered engine names, sorted, for error messages.
func Registered() []string {
	out := make([]string, 0, len(registry))
	for name := range registry {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// EnvConfig reads the engine connection from the ENGINE_* environment
// variables (set by the Helm chart). Host is the one setting with no
// default; everything else falls back to the engine's own defaults.
//
// Deprecated fallbacks: the STARROCKS_* names are honored for installs and
// scripts that predate multi-engine support. They will be removed at
// v1beta1; new deployments must use ENGINE_*.
func EnvConfig() (Config, error) {
	host := firstEnv("ENGINE_HOST", "STARROCKS_HOST")
	if host == "" {
		return Config{}, errors.New("ENGINE_HOST is required (the deprecated STARROCKS_HOST is also accepted)")
	}
	cfg := Config{
		Host:     host,
		User:     firstEnv("ENGINE_USER", "STARROCKS_USER"),
		Password: firstEnv("ENGINE_PASSWORD", "STARROCKS_PASSWORD"),
	}
	if v := firstEnv("ENGINE_PORT", "STARROCKS_PORT"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return Config{}, fmt.Errorf("ENGINE_PORT %q: %w", v, err)
		}
		cfg.Port = n
	}
	switch strings.ToLower(os.Getenv("ENGINE_TLS_ENABLED")) {
	case "true", "1", "yes":
		cfg.TLSEnabled = true
	}
	switch strings.ToLower(os.Getenv("ENGINE_TLS_INSECURE_SKIP_VERIFY")) {
	case "true", "1", "yes":
		cfg.TLSInsecureSkipVerify = true
	}
	return cfg, nil
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}
