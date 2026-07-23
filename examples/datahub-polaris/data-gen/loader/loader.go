// Package loader creates the demo tables in a StarRocks Iceberg (REST-catalog)
// external catalog and bulk-loads the generated rows. It is deliberately
// minimal: the REST catalog (Polaris) owns table locations, so there is no S3
// preflight, no explicit LOCATION, no manifest, and no force/reset. Tables are
// created with CREATE TABLE IF NOT EXISTS and loaded with batched multi-row
// INSERTs; each table's row count is verified after load.
package loader

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"chd.local/datagen/constants"
	"chd.local/datagen/gen"
)

const (
	// DefaultBatchSize is the number of rows per INSERT statement.
	DefaultBatchSize = 1000

	verifyPollInterval = 2 * time.Second
	verifyTimeout      = 90 * time.Second
)

var identifierPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// DB is the small StarRocks surface the loader needs. Keeping rows as ordinary
// values makes the load path testable without a cluster.
type DB interface {
	Exec(context.Context, string, ...any) error
	Query(context.Context, string, ...any) ([][]any, error)
}

// SQLDB adapts database/sql to DB.
type SQLDB struct{ DB *sql.DB }

func (d SQLDB) Exec(ctx context.Context, query string, args ...any) error {
	if d.DB == nil {
		return errors.New("loader: nil SQL database")
	}
	_, err := d.DB.ExecContext(ctx, query, args...)
	return err
}

func (d SQLDB) Query(ctx context.Context, query string, args ...any) ([][]any, error) {
	if d.DB == nil {
		return nil, errors.New("loader: nil SQL database")
	}
	rows, err := d.DB.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var result [][]any
	for rows.Next() {
		values := make([]any, len(columns))
		dest := make([]any, len(columns))
		for i := range values {
			dest[i] = &values[i]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		for i, value := range values {
			if b, ok := value.([]byte); ok {
				values[i] = string(b)
			}
		}
		result = append(result, values)
	}
	return result, rows.Err()
}

// Config selects the target external catalog, namespace, and INSERT batch size.
type Config struct {
	Catalog   string
	Database  string
	BatchSize int
}

// Loader loads the generated tables through a DB.
type Loader struct {
	db  DB
	cfg Config
	log func(string, ...any)
}

// New validates the config and returns a Loader. log may be nil.
func New(db DB, cfg Config, log func(string, ...any)) (*Loader, error) {
	cfg.Catalog = strings.TrimSpace(cfg.Catalog)
	cfg.Database = strings.TrimSpace(cfg.Database)
	if cfg.Database == "" {
		cfg.Database = constants.DatabaseName
	}
	if !identifierPattern.MatchString(cfg.Catalog) {
		return nil, fmt.Errorf("loader: invalid or empty catalog %q", cfg.Catalog)
	}
	if !identifierPattern.MatchString(cfg.Database) {
		return nil, fmt.Errorf("loader: invalid database %q", cfg.Database)
	}
	if cfg.BatchSize == 0 {
		cfg.BatchSize = DefaultBatchSize
	}
	if cfg.BatchSize < 1 || cfg.BatchSize > 5000 {
		return nil, fmt.Errorf("loader: batch size must be between 1 and 5000, got %d", cfg.BatchSize)
	}
	if log == nil {
		log = func(string, ...any) {}
	}
	return &Loader{db: db, cfg: cfg, log: log}, nil
}

// Run creates the database and all tables, then loads and verifies each table
// in the fixed generation order. It is idempotent for schema creation
// (CREATE ... IF NOT EXISTS); rows are appended, so run it against empty tables.
func (l *Loader) Run(ctx context.Context) error {
	if err := l.db.Exec(ctx, "CREATE DATABASE IF NOT EXISTS "+l.databaseName()); err != nil {
		return fmt.Errorf("loader: create database %s: %w", l.databaseName(), err)
	}
	for _, table := range gen.Tables() {
		ddl, err := l.createTableSQL(table)
		if err != nil {
			return err
		}
		if err := l.db.Exec(ctx, ddl); err != nil {
			return fmt.Errorf("loader: create table %s: %w", table, err)
		}
		if err := l.insertRows(ctx, table); err != nil {
			return err
		}
		expected := int64(constants.ExpectedTableRowCounts[table])
		count, err := l.verifyExactCount(ctx, table, expected)
		if err != nil {
			return err
		}
		l.log("loaded %-32s %d rows", table, count)
	}
	return nil
}

func (l *Loader) databaseName() string {
	return fmt.Sprintf("`%s`.`%s`", l.cfg.Catalog, l.cfg.Database)
}

func (l *Loader) fq(table string) string {
	return fmt.Sprintf("`%s`.`%s`.`%s`", l.cfg.Catalog, l.cfg.Database, table)
}

// createTableSQL builds a catalog-owned CREATE TABLE. The REST catalog assigns
// the location under its warehouse, so no LOCATION is emitted.
func (l *Loader) createTableSQL(table string) (string, error) {
	schema := gen.Schema(table)
	if schema.Name == "" {
		return "", fmt.Errorf("loader: no schema for table %q", table)
	}
	columns := make([]string, len(schema.Columns))
	for i, column := range schema.Columns {
		nullability := " NOT NULL"
		if column.Nullable {
			nullability = ""
		}
		columns[i] = fmt.Sprintf("`%s` %s%s", column.Name, column.Type, nullability)
	}
	return fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (%s)", l.fq(table), strings.Join(columns, ", ")), nil
}

func (l *Loader) insertRows(ctx context.Context, table string) error {
	schema := gen.Schema(table)
	columns := make([]string, len(schema.Columns))
	for i, column := range schema.Columns {
		columns[i] = "`" + column.Name + "`"
	}
	one := "(" + strings.TrimSuffix(strings.Repeat("?,", len(columns)), ",") + ")"
	return gen.Rows(table, l.cfg.BatchSize, func(rows []gen.Row) error {
		placeholders := make([]string, len(rows))
		args := make([]any, 0, len(rows)*len(columns))
		for i, row := range rows {
			if len(row) != len(columns) {
				return fmt.Errorf("loader: %s row has %d values, want %d", table, len(row), len(columns))
			}
			placeholders[i] = one
			args = append(args, row...)
		}
		query := fmt.Sprintf("INSERT INTO %s (%s) VALUES %s", l.fq(table), strings.Join(columns, ","), strings.Join(placeholders, ","))
		if err := l.db.Exec(ctx, query, args...); err != nil {
			return fmt.Errorf("loader: insert %s batch: %w", table, err)
		}
		return nil
	})
}

func (l *Loader) countTable(ctx context.Context, table string) (int64, error) {
	rows, err := l.db.Query(ctx, "SELECT COUNT(*) FROM "+l.fq(table))
	if err != nil {
		return 0, err
	}
	if len(rows) != 1 || len(rows[0]) != 1 {
		return 0, fmt.Errorf("loader: count %s returned an unexpected result", table)
	}
	return scalarInt64(rows[0][0])
}

// verifyExactCount polls COUNT(*) until it equals expected or the timeout
// elapses. The short retry tolerates the brief lag between an Iceberg commit
// and its visibility through the external catalog.
func (l *Loader) verifyExactCount(ctx context.Context, table string, expected int64) (int64, error) {
	retryCtx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()

	var last int64
	observed := false
	for {
		count, err := l.countTable(retryCtx, table)
		if err != nil {
			if retryCtx.Err() != nil {
				return last, countError(table, last, expected, observed, retryCtx.Err())
			}
			return 0, fmt.Errorf("loader: verify %s row count: %w", table, err)
		}
		last, observed = count, true
		if count == expected {
			return count, nil
		}
		timer := time.NewTimer(verifyPollInterval)
		select {
		case <-retryCtx.Done():
			timer.Stop()
			return last, countError(table, last, expected, observed, retryCtx.Err())
		case <-timer.C:
		}
	}
}

func countError(table string, last, expected int64, observed bool, err error) error {
	seen := "none"
	if observed {
		seen = strconv.FormatInt(last, 10)
	}
	return fmt.Errorf("loader: verify %s row count did not converge: last observed %s, expected %d: %w", table, seen, expected, err)
}

func scalarInt64(value any) (int64, error) {
	switch v := value.(type) {
	case int64:
		return v, nil
	case int:
		return int64(v), nil
	case uint64:
		return int64(v), nil
	case string:
		return strconv.ParseInt(v, 10, 64)
	case []byte:
		return strconv.ParseInt(string(v), 10, 64)
	default:
		return strconv.ParseInt(fmt.Sprint(value), 10, 64)
	}
}
