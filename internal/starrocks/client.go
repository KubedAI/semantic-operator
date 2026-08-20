// Package starrocks is the MySQL-protocol client used for schema
// introspection (drift detection), view DDL, and query execution. It
// registers itself with dbclient under the engine name "starrocks".
package starrocks

import (
	"context"
	"crypto/tls"
	"database/sql"
	"fmt"
	"time"

	"github.com/KubedAI/semantic-operator/internal/dbclient"
	mysql "github.com/KubedAI/semantic-operator/internal/starrocks/mysqldriver"
)

// DefaultPort is the StarRocks FE MySQL-protocol port.
const DefaultPort = 9030

func init() {
	dbclient.Register("starrocks", func(cfg dbclient.Config) (dbclient.Client, error) {
		return Open(cfg)
	})
}

// Config comes from Helm values via env; nothing is hardcoded.
type Config = dbclient.Config

// Column is one physical column as reported by DESC.
type Column = dbclient.Column

// Client wraps database/sql for StarRocks. The static pool serves the
// operator's metadata reads and DDL, and any query that runs under the
// server's own credential. A per-request connection is opened only when a
// query carries a caller credential (passthrough).
type Client struct {
	db      *sql.DB
	addr    string
	timeout time.Duration
	// maxBytes bounds one result while it is being read. Never zero after
	// Open, so a client cannot exist without a ceiling.
	maxBytes int
	// tlsConfig is non-nil when the engine connection uses TLS. Passthrough
	// requires it, because the caller's token travels in the password field.
	tlsConfig *tls.Config
}

// Open creates a pooled client. It does not dial; use Ping for readiness.
func Open(cfg Config) (*Client, error) {
	if cfg.Port == 0 {
		cfg.Port = DefaultPort
	}
	if cfg.User == "" {
		cfg.User = "root"
	}
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)

	var tlsConfig *tls.Config
	if cfg.TLSEnabled || cfg.TLSInsecureSkipVerify {
		tlsConfig = &tls.Config{
			ServerName:         cfg.Host,
			InsecureSkipVerify: cfg.TLSInsecureSkipVerify, //nolint:gosec // opt-in for self-signed development engines only
		}
	}

	connector, err := mysql.NewConnector(baseConfig(addr, cfg.User, cfg.Password, tlsConfig))
	if err != nil {
		return nil, err
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(16)
	db.SetMaxIdleConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)
	t := cfg.QueryTimeout
	if t == 0 {
		t = 60 * time.Second
	}
	maxBytes := cfg.MaxResultBytes
	if maxBytes <= 0 {
		maxBytes = dbclient.DefaultMaxResultBytes
	}
	return &Client{db: db, addr: addr, timeout: t, maxBytes: maxBytes, tlsConfig: tlsConfig}, nil
}

// baseConfig builds the driver config shared by the static pool and by each
// per-request connection. TLS is programmatic (a *tls.Config), so it is set
// here rather than through a registered DSN name.
func baseConfig(addr, user, passwd string, tlsConfig *tls.Config) *mysql.Config {
	mc := mysql.NewConfig()
	mc.Addr = addr
	mc.Net = "tcp"
	mc.User = user
	mc.Passwd = passwd
	mc.InterpolateParams = true
	mc.ParseTime = true
	mc.Timeout = 10 * time.Second
	mc.TLS = tlsConfig
	return mc
}

// SupportsPerRequestIdentity marks the StarRocks client as honoring the
// per-request EngineCredential: a query with a caller credential runs on a
// dedicated connection that authenticates to StarRocks as the caller through
// the OpenID Connect client plugin. Its presence lets the server enable
// passthrough and exchange for StarRocks.
func (c *Client) SupportsPerRequestIdentity() {}

func (c *Client) Ping(ctx context.Context) error { return c.db.PingContext(ctx) }

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
// on the static pool under the server's own engine user. A non-zero credential
// opens a dedicated connection that authenticates to StarRocks as the caller:
// the engine user is the login name and the caller's JWT is presented through
// the OpenID Connect client plugin, so StarRocks authorizes and audits the
// query as the caller.
func (c *Client) Query(ctx context.Context, cred dbclient.EngineCredential, query string) (cols []string, out [][]any, err error) {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	if cred.IsZero() {
		return runQuery(ctx, c.db, c.maxBytes, query)
	}
	if !cred.Expiry.IsZero() && time.Now().After(cred.Expiry) {
		return nil, nil, fmt.Errorf("starrocks: caller token has expired")
	}
	if cred.EngineUser == "" {
		return nil, nil, fmt.Errorf("starrocks: passthrough requires an engine user for the login name")
	}
	// The JWT travels in the password field, so it must never cross the wire in
	// cleartext. Refuse passthrough unless the engine connection uses TLS.
	if c.tlsConfig == nil {
		return nil, nil, fmt.Errorf("starrocks: passthrough requires TLS on the engine connection; enable engine TLS")
	}
	// The login name is the caller's resolved engine user, which must equal the
	// user's JWT principal field in StarRocks, so the FE authenticates the query
	// as the caller through the authentication_jwt plugin rather than treating
	// it as impersonation.
	connector, err := mysql.NewConnector(baseConfig(c.addr, cred.EngineUser, cred.Token, c.tlsConfig))
	if err != nil {
		return nil, nil, err
	}
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	// One connection per request: the caller's token-authenticated session must
	// not be pooled and reused for another caller.
	db.SetMaxOpenConns(1)
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

// DescribeTable introspects a table through a StarRocks catalog, external or
// default. This is the drift-detection primitive: it sees exactly what the
// query engine sees.
func (c *Client) DescribeTable(ctx context.Context, catalog, database, table string) ([]Column, error) {
	q := fmt.Sprintf("DESC `%s`.`%s`.`%s`", catalog, database, table)
	cols, rows, err := c.Query(ctx, dbclient.EngineCredential{}, q)
	if err != nil {
		return nil, err
	}
	nameIdx, typeIdx := -1, -1
	for i, col := range cols {
		switch col {
		case "Field":
			nameIdx = i
		case "Type":
			typeIdx = i
		}
	}
	if nameIdx < 0 || typeIdx < 0 {
		return nil, fmt.Errorf("unexpected DESC output columns %v", cols)
	}
	var out []Column
	for _, r := range rows {
		out = append(out, Column{
			Name: fmt.Sprint(r[nameIdx]),
			Type: fmt.Sprint(r[typeIdx]),
		})
	}
	return out, nil
}

// ShowCreateTable returns the DDL as StarRocks renders it. The NL demo uses
// this as the raw-schema context for text-to-SQL.
func (c *Client) ShowCreateTable(ctx context.Context, catalog, database, table string) (string, error) {
	q := fmt.Sprintf("SHOW CREATE TABLE `%s`.`%s`.`%s`", catalog, database, table)
	cols, rows, err := c.Query(ctx, dbclient.EngineCredential{}, q)
	if err != nil {
		return "", err
	}
	if len(rows) == 0 {
		return "", fmt.Errorf("no DDL returned for %s.%s.%s", catalog, database, table)
	}
	for i, col := range cols {
		if col == "Create Table" || col == "Create View" {
			return fmt.Sprint(rows[0][i]), nil
		}
	}
	return fmt.Sprint(rows[0][len(cols)-1]), nil
}
