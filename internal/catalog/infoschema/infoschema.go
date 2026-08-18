// Package infoschema implements catalog.Source over a live query engine's
// information_schema. Because the engine already mounts the physical catalog
// (Glue, Polaris or any Iceberg REST catalog, Hive Metastore, Unity), one
// implementation makes model derivation work for every catalog and engine
// combination with no catalog-specific SDK, and it sees exactly the schema
// the engine will see at query time — the same property drift detection
// relies on.
package infoschema

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/KubedAI/semantic-operator/internal/catalog"
	"github.com/KubedAI/semantic-operator/internal/dbclient"
	"github.com/KubedAI/semantic-operator/internal/emitter"
)

// Source lists tables through the engine's information_schema.
type Source struct {
	db  dbclient.Client
	d   emitter.Dialect
	cat string
}

// New builds a Source that introspects the given engine catalog. The dialect
// supplies identifier quoting, so the same code serves double-quote (Trino)
// and backtick (StarRocks) engines.
func New(db dbclient.Client, d emitter.Dialect, engineCatalog string) *Source {
	return &Source{db: db, d: d, cat: engineCatalog}
}

// ListTables returns every table in the database with its columns in
// ordinal order. One query fetches the whole schema; rows are grouped
// client-side, preserving the engine's ordering.
func (s *Source) ListTables(ctx context.Context, database string) ([]catalog.Table, error) {
	var errs []error
	for _, q := range s.columnQueries(database) {
		cols, rows, err := s.db.Query(ctx, dbclient.EngineCredential{}, q)
		if err != nil {
			// The comment column name differs by engine family; try the
			// next variant rather than guessing from the dialect name, so
			// future engines work without a code change here.
			errs = append(errs, err)
			continue
		}
		if len(cols) < 4 {
			return nil, fmt.Errorf("unexpected information_schema output columns %v", cols)
		}
		return groupTables(rows), nil
	}
	return nil, fmt.Errorf("querying information_schema of catalog %q: %w", s.cat, errors.Join(errs...))
}

// columnQueries returns the introspection statement in the two dialect
// families' spellings: ANSI/Trino names the comment column "comment", the
// MySQL family (StarRocks) names it "column_comment". The first that
// executes wins.
func (s *Source) columnQueries(database string) []string {
	base := "SELECT table_name, column_name, data_type, %s " +
		"FROM %s.information_schema.columns WHERE table_schema = %s " +
		"ORDER BY table_name, ordinal_position"
	from := s.d.QuoteIdent(s.cat)
	schema := quoteString(database)
	return []string{
		fmt.Sprintf(base, "COALESCE(comment, '')", from, schema),
		fmt.Sprintf(base, "COALESCE(column_comment, '')", from, schema),
	}
}

// groupTables folds ordered (table, column, type, comment) rows into Tables,
// preserving the engine's table and ordinal order.
func groupTables(rows [][]any) []catalog.Table {
	var out []catalog.Table
	idx := map[string]int{}
	for _, r := range rows {
		if len(r) < 4 {
			continue
		}
		table := fmt.Sprint(r[0])
		i, ok := idx[table]
		if !ok {
			i = len(out)
			idx[table] = i
			out = append(out, catalog.Table{Name: table})
		}
		out[i].Columns = append(out[i].Columns, catalog.Column{
			Name:    fmt.Sprint(r[1]),
			Type:    fmt.Sprint(r[2]),
			Comment: fmt.Sprint(r[3]),
		})
	}
	return out
}

func quoteString(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}
