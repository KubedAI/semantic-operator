// Package emitter defines the SQL dialect boundary. The planner builds a
// logical plan and renders it through a Dialect, so adding another engine
// (Trino, ClickHouse, DuckDB) means implementing this interface only.
package emitter

import "fmt"

// Dialect renders the engine-specific atoms of a SQL statement.
type Dialect interface {
	// Name identifies the dialect (e.g. "starrocks").
	Name() string
	// QuoteIdent quotes a single identifier part.
	QuoteIdent(ident string) string
	// QualifyTable renders a fully qualified table reference from
	// catalog.database.table parts.
	QualifyTable(catalog, database, table string) string
	// Literal renders a Go value as a SQL literal. Strings are escaped;
	// unsupported types return an error rather than unsafe output.
	Literal(v any) (string, error)
	// DateTrunc renders time-grain truncation over a scalar expression.
	// Grain is one of day, week, month, quarter, year.
	DateTrunc(grain, scalar string) string
	// NullSafeEq renders a null-safe equality predicate for joining
	// aggregation subqueries on dimension columns.
	NullSafeEq(a, b string) string
	// CreateSchema renders the idempotent DDL that creates the (already
	// quoted, possibly catalog-qualified) schema governed views live in.
	// Engines disagree on the keyword (DATABASE vs SCHEMA), hence a method.
	CreateSchema(quotedSchema string) string
}

// Registry of available dialects, keyed by name.
var registry = map[string]Dialect{}

// Register adds a dialect. Called from dialect package init().
func Register(d Dialect) { registry[d.Name()] = d }

// Get returns a registered dialect.
func Get(name string) (Dialect, error) {
	d, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("unknown SQL dialect %q", name)
	}
	return d, nil
}
