// Package trino is the Trino HTTP-protocol client used for schema
// introspection (drift detection), view DDL, and query execution. It
// registers itself with dbclient under the engine name "trino".
package trino

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"time"

	_ "github.com/trinodb/trino-go-client/trino" // registers the "trino" driver

	"github.com/KubedAI/semantic-operator/internal/dbclient"
)

// DefaultPort is the Trino coordinator HTTP port.
const DefaultPort = 8080

func init() {
	dbclient.Register("trino", func(cfg dbclient.Config) (dbclient.Client, error) {
		return Open(cfg)
	})
}

// Client wraps database/sql for Trino.
type Client struct {
	db      *sql.DB
	timeout time.Duration
}

// Open creates a pooled client. It does not dial; use Ping for readiness.
// A password implies HTTPS: the Trino client refuses basic auth over plain
// HTTP, so password-protected deployments must terminate TLS at or before
// the coordinator.
func Open(cfg dbclient.Config) (*Client, error) {
	if cfg.Port == 0 {
		cfg.Port = DefaultPort
	}
	if cfg.User == "" {
		cfg.User = "semantic-operator"
	}
	scheme := "http"
	user := url.User(cfg.User)
	if cfg.Password != "" {
		scheme = "https"
		user = url.UserPassword(cfg.User, cfg.Password)
	}
	dsn := (&url.URL{
		Scheme:   scheme,
		User:     user,
		Host:     fmt.Sprintf("%s:%d", cfg.Host, cfg.Port),
		RawQuery: url.Values{"source": []string{"semantic-operator"}}.Encode(),
	}).String()
	db, err := sql.Open("trino", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)
	t := cfg.QueryTimeout
	if t == 0 {
		t = 60 * time.Second
	}
	return &Client{db: db, timeout: t}, nil
}

// Ping verifies connectivity by running a trivial query. The driver's own
// Ping is not guaranteed to reach the coordinator, so this goes end to end.
func (c *Client) Ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	var one int
	return c.db.QueryRowContext(ctx, "SELECT 1").Scan(&one)
}

func (c *Client) Close() error { return c.db.Close() }

// Exec runs DDL or other statements without results.
func (c *Client) Exec(ctx context.Context, query string) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	_, err := c.db.ExecContext(ctx, query)
	return err
}

// Query runs a statement and returns column names and JSON-friendly rows
// ([]byte becomes string, so results marshal cleanly).
func (c *Client) Query(ctx context.Context, query string) ([]string, [][]any, error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	rows, err := c.db.QueryContext(ctx, query)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}
	var out [][]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, nil, err
		}
		for i, v := range vals {
			if b, ok := v.([]byte); ok {
				vals[i] = string(b)
			}
		}
		out = append(out, vals)
	}
	return cols, out, rows.Err()
}

// DescribeTable introspects a table through information_schema, which every
// Trino connector serves. A table with zero matching columns is reported as
// an error, not an empty list, so the reconciler treats it as drift.
func (c *Client) DescribeTable(ctx context.Context, catalog, database, table string) ([]dbclient.Column, error) {
	cols, rows, err := c.Query(ctx, describeQuery(catalog, database, table))
	if err != nil {
		return nil, err
	}
	if len(cols) < 2 {
		return nil, fmt.Errorf("unexpected information_schema output columns %v", cols)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("table %s.%s.%s not found in information_schema", catalog, database, table)
	}
	out := make([]dbclient.Column, 0, len(rows))
	for _, r := range rows {
		out = append(out, dbclient.Column{
			Name: fmt.Sprint(r[0]),
			Type: fmt.Sprint(r[1]),
		})
	}
	return out, nil
}

// describeQuery builds the introspection statement. The catalog is an
// identifier (quoted with doubling); schema and table are string literals
// (quoted with doubling). Split out for testing.
func describeQuery(catalog, database, table string) string {
	return fmt.Sprintf(
		"SELECT column_name, data_type FROM %s.information_schema.columns "+
			"WHERE table_schema = %s AND table_name = %s ORDER BY ordinal_position",
		quoteIdent(catalog), quoteString(database), quoteString(table))
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func quoteString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
