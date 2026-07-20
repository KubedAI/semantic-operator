// Package starrocks is the MySQL-protocol client used for schema
// introspection (drift detection), view DDL, and query execution. It
// registers itself with dbclient under the engine name "starrocks".
package starrocks

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/go-sql-driver/mysql"

	"github.com/KubedAI/semantic-operator/internal/dbclient"
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

// Client wraps database/sql for StarRocks.
type Client struct {
	db      *sql.DB
	timeout time.Duration
}

// Open creates a pooled client. It does not dial; use Ping for readiness.
func Open(cfg Config) (*Client, error) {
	if cfg.Port == 0 {
		cfg.Port = DefaultPort
	}
	if cfg.User == "" {
		cfg.User = "root"
	}
	mc := mysql.NewConfig()
	mc.Addr = fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	mc.Net = "tcp"
	mc.User = cfg.User
	mc.Passwd = cfg.Password
	mc.InterpolateParams = true
	mc.ParseTime = true
	mc.Timeout = 10 * time.Second
	db, err := sql.Open("mysql", mc.FormatDSN())
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

// DescribeTable introspects a table through a StarRocks catalog, external or
// default. This is the drift-detection primitive: it sees exactly what the
// query engine sees.
func (c *Client) DescribeTable(ctx context.Context, catalog, database, table string) ([]Column, error) {
	q := fmt.Sprintf("DESC `%s`.`%s`.`%s`", catalog, database, table)
	cols, rows, err := c.Query(ctx, q)
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
	cols, rows, err := c.Query(ctx, q)
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
