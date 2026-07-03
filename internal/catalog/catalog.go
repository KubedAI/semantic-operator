// Package catalog defines the catalog-source extension point. A Source
// reads physical table metadata from a lake catalog so dataset bindings and
// field lists can be derived instead of hand-maintained. Implementations:
// glue. Candidates for later: Unity, Polaris, Hive Metastore.
package catalog

import "context"

// Column is a physical column.
type Column struct {
	Name    string
	Type    string
	Comment string
}

// Table is a physical table with its columns.
type Table struct {
	Name    string
	Columns []Column
}

// Source lists tables in a catalog database.
type Source interface {
	ListTables(ctx context.Context, database string) ([]Table, error)
}
