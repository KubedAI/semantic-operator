// Package trino is the Trino HTTP-protocol client used for schema
// introspection (drift detection), view DDL, and query execution. It
// registers itself with dbclient under the engine name "trino".
package trino

import (
	"context"
	"crypto/tls"
	"database/sql"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	trinodriver "github.com/trinodb/trino-go-client/trino" // registers the "trino" driver

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
	host    string
	port    int
	scheme  string
	timeout time.Duration
	// customClient is the registered driver client key used for TLS, empty
	// unless certificate verification is skipped.
	customClient string
	// maxBytes bounds one result while it is being read. Never zero after
	// Open, so a client cannot exist without a ceiling.
	maxBytes int
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
	if cfg.TLSEnabled || cfg.Password != "" {
		scheme = "https"
	}
	t := cfg.QueryTimeout
	if t == 0 {
		t = 60 * time.Second
	}
	maxBytes := cfg.MaxResultBytes
	if maxBytes <= 0 {
		maxBytes = dbclient.DefaultMaxResultBytes
	}
	c := &Client{host: cfg.Host, port: cfg.Port, scheme: scheme, timeout: t, maxBytes: maxBytes}

	// Skip-verify is opt-in for a self-signed engine in isolated development.
	// It is wired through a registered custom client, because the driver only
	// exposes certificate trust through a provided CA or a custom client.
	if scheme == "https" && cfg.TLSInsecureSkipVerify {
		key := fmt.Sprintf("semantic-insecure-%s-%d", cfg.Host, cfg.Port)
		if err := trinodriver.RegisterCustomClient(key, &http.Client{
			Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
		}); err != nil {
			return nil, err
		}
		c.customClient = key
	}

	// The static pool runs under the server's own credential and serves both
	// schema introspection and static (non-passthrough) query execution.
	var user *url.Userinfo
	if scheme == "https" && cfg.Password != "" {
		user = url.UserPassword(cfg.User, cfg.Password)
	} else {
		user = url.User(cfg.User)
	}
	dsn, err := c.dsn(user, "")
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("trino", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)
	c.db = db
	return c, nil
}

// dsn builds a Trino DSN. A nil user omits the session user so the coordinator
// derives it from the access token's principal, which is the passthrough case.
// A non-empty token adds the Authorization: Bearer header.
func (c *Client) dsn(user *url.Userinfo, token string) (string, error) {
	u := url.URL{Scheme: c.scheme, Host: fmt.Sprintf("%s:%d", c.host, c.port)}
	if user != nil {
		u.User = user
	}
	conf := trinodriver.Config{ServerURI: u.String(), Source: "semantic-operator", AccessToken: token, CustomClientName: c.customClient}
	return conf.FormatDSN()
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
// ([]byte becomes string, so results marshal cleanly). A zero credential runs
// on the static pool. A non-zero credential forwards the caller's token on a
// per-request connection, so the coordinator authenticates and authorizes the
// query as the caller.
func (c *Client) Query(ctx context.Context, cred dbclient.EngineCredential, query string) (cols []string, out [][]any, err error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	if cred.IsZero() {
		return runQuery(ctx, c.db, c.maxBytes, query)
	}
	if !cred.Expiry.IsZero() && time.Now().After(cred.Expiry) {
		return nil, nil, fmt.Errorf("trino: caller token has expired")
	}
	if cred.EngineUser == "" {
		return nil, nil, fmt.Errorf("trino: passthrough requires an engine user for the session user")
	}
	// The session user is the caller's resolved engine user, which matches the
	// token's principal field, so the coordinator authorizes the query as the
	// caller without treating it as impersonation.
	dsn, err := c.dsn(url.User(cred.EngineUser), cred.Token)
	if err != nil {
		return nil, nil, err
	}
	db, err := sql.Open("trino", dsn)
	if err != nil {
		return nil, nil, err
	}
	defer db.Close()
	return runQuery(ctx, db, c.maxBytes, query)
}

// runQuery executes one statement and scans bounded rows.
func runQuery(ctx context.Context, db *sql.DB, maxBytes int, query string) (cols []string, out [][]any, err error) {
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if closeErr := rows.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	// Bounded while scanning, so an oversized result is abandoned rather than
	// fully allocated and then rejected.
	return dbclient.ScanRows(rows, maxBytes)
}

// DescribeTable introspects a table through information_schema, which every
// Trino connector serves. A table with zero matching columns is reported as
// an error, not an empty list, so the reconciler treats it as drift.
func (c *Client) DescribeTable(ctx context.Context, catalog, database, table string) ([]dbclient.Column, error) {
	cols, rows, err := c.Query(ctx, dbclient.EngineCredential{}, describeQuery(catalog, database, table))
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
