// Package catalog defines the two model-derivation extension points, used by
// `ossiectl derive` only (the runtime introspects through the engine client).
//
// A Source reads physical structure: which tables exist and what columns they
// have. Physical truth must come from something authoritative about the
// current schema, so implementations read the catalog the engine itself uses
// (glue) or the engine's own information_schema (infoschema).
//
// An Enricher reads business meaning for structure that already exists:
// descriptions, business vocabulary, and sensitivity classifications. Metadata
// platforms hold this but serve an ingested copy of the schema, so they
// decorate a scaffold rather than define it. Implementations: datahub.
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

// FieldMeta is the business meaning attached to one column.
type FieldMeta struct {
	Description string
	// Synonyms become ai_context.synonyms, which is how an agent grounds a
	// user's words onto a certified field.
	Synonyms []string
	// Sensitive marks a column that policy should not expose (a PII or
	// confidentiality classification upstream). Derivation turns these into
	// governance.denyFields entries.
	Sensitive bool
}

// TableMeta is the business meaning attached to one table and its columns.
type TableMeta struct {
	Description string
	Synonyms    []string
	// Deprecated marks a dataset an upstream catalog no longer certifies.
	// Derivation keeps it but flags it, so a human decides.
	Deprecated bool
	// Fields is keyed by physical column name. Columns absent from the map
	// simply carry no enrichment.
	Fields map[string]FieldMeta
}

// Enricher supplies business meaning for tables discovered by a Source. It is
// additive and best-effort: an unknown table or column yields no entry, never
// an error, so enrichment can never block derivation.
type Enricher interface {
	// DescribeTables returns metadata keyed by physical table name for the
	// requested tables in a database.
	DescribeTables(ctx context.Context, database string, tables []string) (map[string]TableMeta, error)
}
