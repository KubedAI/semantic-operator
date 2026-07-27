package infoschema

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/KubedAI/semantic-operator/internal/dbclient"
	"github.com/KubedAI/semantic-operator/internal/emitter"
	_ "github.com/KubedAI/semantic-operator/internal/emitter/starrocks"
	_ "github.com/KubedAI/semantic-operator/internal/emitter/trino"
)

// fakeDB fails queries whose text matches failOn and records every query.
type fakeDB struct {
	dbclient.Client
	queries []string
	failOn  string
	rows    [][]any
}

func (f *fakeDB) Query(_ context.Context, sql string) ([]string, [][]any, error) {
	f.queries = append(f.queries, sql)
	if f.failOn != "" && strings.Contains(sql, f.failOn) {
		return nil, nil, errors.New("column does not exist")
	}
	return []string{"table_name", "column_name", "data_type", "comment"}, f.rows, nil
}

func dialect(t *testing.T, name string) emitter.Dialect {
	t.Helper()
	d, err := emitter.Get(name)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func demoRows() [][]any {
	return [][]any{
		{"date_dim", "d_date_sk", "bigint", ""},
		{"date_dim", "d_date", "date", "calendar day"},
		{"store_sales", "ss_item_sk", "bigint", ""},
		{"store_sales", "ss_ext_sales_price", "decimal(7,2)", "extended price"},
	}
}

func TestListTablesGroupsAndOrders(t *testing.T) {
	db := &fakeDB{rows: demoRows()}
	src := New(db, dialect(t, "trino"), "iceberg")
	tables, err := src.ListTables(context.Background(), "osi_demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) != 2 {
		t.Fatalf("tables = %d, want 2", len(tables))
	}
	if tables[0].Name != "date_dim" || tables[1].Name != "store_sales" {
		t.Fatalf("order not preserved: %s, %s", tables[0].Name, tables[1].Name)
	}
	d := tables[0]
	if len(d.Columns) != 2 || d.Columns[1].Name != "d_date" || d.Columns[1].Type != "date" || d.Columns[1].Comment != "calendar day" {
		t.Fatalf("columns mismapped: %+v", d.Columns)
	}
}

func TestQueryUsesDialectQuotingAndSchemaLiteral(t *testing.T) {
	db := &fakeDB{rows: demoRows()}
	src := New(db, dialect(t, "trino"), "iceberg")
	if _, err := src.ListTables(context.Background(), "osi_demo"); err != nil {
		t.Fatal(err)
	}
	q := db.queries[0]
	for _, want := range []string{`"iceberg".information_schema.columns`, `table_schema = 'osi_demo'`, "ORDER BY table_name, ordinal_position"} {
		if !strings.Contains(q, want) {
			t.Errorf("query missing %q:\n%s", want, q)
		}
	}

	// The same source under a backtick dialect quotes with backticks.
	db2 := &fakeDB{rows: demoRows()}
	src2 := New(db2, dialect(t, "starrocks"), "iceberg")
	if _, err := src2.ListTables(context.Background(), "osi_demo"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(db2.queries[0], "`iceberg`.information_schema.columns") {
		t.Errorf("backtick dialect not applied:\n%s", db2.queries[0])
	}
}

func TestFallsBackToMySQLCommentColumn(t *testing.T) {
	// First (ANSI) variant fails as it would on a MySQL-family engine; the
	// column_comment variant must be tried and succeed.
	db := &fakeDB{rows: demoRows(), failOn: "COALESCE(comment"}
	src := New(db, dialect(t, "starrocks"), "iceberg")
	tables, err := src.ListTables(context.Background(), "osi_demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(db.queries) != 2 || !strings.Contains(db.queries[1], "column_comment") {
		t.Fatalf("expected fallback to column_comment variant, queries: %v", db.queries)
	}
	if len(tables) != 2 {
		t.Fatalf("tables = %d, want 2", len(tables))
	}
}

func TestBothVariantsFailingReturnsJoinedError(t *testing.T) {
	db := &fakeDB{failOn: "COALESCE"}
	src := New(db, dialect(t, "trino"), "iceberg")
	if _, err := src.ListTables(context.Background(), "osi_demo"); err == nil {
		t.Fatal("expected error when every variant fails")
	}
}

func TestSchemaLiteralEscaped(t *testing.T) {
	db := &fakeDB{rows: demoRows()}
	src := New(db, dialect(t, "trino"), "iceberg")
	if _, err := src.ListTables(context.Background(), "o'demo"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(db.queries[0], "table_schema = 'o''demo'") {
		t.Errorf("schema literal not escaped:\n%s", db.queries[0])
	}
}
