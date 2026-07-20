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
	// ([]byte values are converted to string so results marshal cleanly).
	Query(ctx context.Context, sql string) ([]string, [][]any, error)
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
}

// Factory builds a client for one engine.
type Factory func(cfg Config) (Client, error)

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
