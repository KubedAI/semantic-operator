package catalog

import (
	"context"
	"sort"
)

// Enrichment is the applied result of an Enricher over a set of tables: the
// per-table metadata plus the derived governance consequences. Keeping the
// consequences (denied fields, deprecated datasets) separate from the raw
// metadata lets the template render them where they belong, in governance
// and in review comments, rather than scattering them through the datasets.
type Enrichment struct {
	// Tables is metadata keyed by physical table name. Never nil.
	Tables map[string]TableMeta
	// DeniedFields are dataset.field references an upstream classification
	// marks sensitive, sorted, ready to render as governance.denyFields.
	DeniedFields []string
	// DeprecatedTables are tables an upstream catalog no longer certifies,
	// sorted. Derivation flags them for human review rather than dropping
	// them: only a person can decide whether a model should stop using one.
	DeprecatedTables []string
}

// Enrich collects metadata for the given tables. A nil Enricher, or one that
// fails, yields an empty Enrichment and no error: enrichment is additive
// decoration on a scaffold that is already valid without it, so it must never
// block derivation. Callers that want to report a failure (because the user
// explicitly asked for enrichment) should call the Enricher themselves and
// pass the result to EnrichWith, which avoids a second round trip.
func Enrich(ctx context.Context, e Enricher, database string, tables []Table) Enrichment {
	if e == nil || len(tables) == 0 {
		return Enrichment{Tables: map[string]TableMeta{}}
	}
	names := make([]string, 0, len(tables))
	for _, t := range tables {
		names = append(names, t.Name)
	}
	meta, err := e.DescribeTables(ctx, database, names)
	if err != nil {
		return Enrichment{Tables: map[string]TableMeta{}}
	}
	return EnrichWith(tables, meta)
}

// EnrichWith derives the governance consequences of already-fetched metadata.
// It is pure, so the fetch and the interpretation can be tested and reported
// on separately.
func EnrichWith(tables []Table, meta map[string]TableMeta) Enrichment {
	out := Enrichment{Tables: map[string]TableMeta{}}
	if meta == nil {
		return out
	}
	out.Tables = meta

	for _, t := range tables {
		tm, ok := meta[t.Name]
		if !ok {
			continue
		}
		if tm.Deprecated {
			out.DeprecatedTables = append(out.DeprecatedTables, t.Name)
		}
		// Walk the physical columns, not the metadata map, so a classification
		// on a column that no longer exists cannot produce a policy referencing
		// a missing field (which would fail validation).
		for _, c := range t.Columns {
			if fm, ok := tm.Fields[c.Name]; ok && fm.Sensitive {
				out.DeniedFields = append(out.DeniedFields, t.Name+"."+c.Name)
			}
		}
	}
	sort.Strings(out.DeniedFields)
	sort.Strings(out.DeprecatedTables)
	return out
}

// Field returns the metadata for one column, and whether any was found.
func (e Enrichment) Field(table, column string) (FieldMeta, bool) {
	tm, ok := e.Tables[table]
	if !ok {
		return FieldMeta{}, false
	}
	fm, ok := tm.Fields[column]
	return fm, ok
}

// Table returns the metadata for one table, and whether any was found.
func (e Enrichment) Table(table string) (TableMeta, bool) {
	tm, ok := e.Tables[table]
	return tm, ok
}

// Empty reports whether nothing was enriched, so callers can skip rendering
// enrichment-specific sections entirely.
func (e Enrichment) Empty() bool { return len(e.Tables) == 0 }
