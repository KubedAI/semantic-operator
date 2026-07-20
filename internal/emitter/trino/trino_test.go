package trino

import (
	"strings"
	"testing"
	"time"

	"github.com/KubedAI/semantic-operator/internal/emitter"
)

func TestRegistered(t *testing.T) {
	d, err := emitter.Get("trino")
	if err != nil {
		t.Fatal(err)
	}
	if d.Name() != "trino" {
		t.Fatalf("Name() = %q", d.Name())
	}
}

func TestQuoteIdent(t *testing.T) {
	d := Dialect{}
	if got := d.QuoteIdent("i_category"); got != `"i_category"` {
		t.Errorf("QuoteIdent = %q", got)
	}
	if got := d.QuoteIdent(`weird"name`); got != `"weird""name"` {
		t.Errorf("QuoteIdent with embedded quote = %q", got)
	}
	if got := d.QualifyTable("iceberg", "osi_demo", "store_sales"); got != `"iceberg"."osi_demo"."store_sales"` {
		t.Errorf("QualifyTable = %q", got)
	}
}

func TestLiteral(t *testing.T) {
	d := Dialect{}
	cases := []struct {
		in   any
		want string
	}{
		{nil, "NULL"},
		{"TX", "'TX'"},
		{"O'Brien", "'O''Brien'"},
		// Backslash is an ordinary character in Trino strings; it must NOT
		// be doubled the way the MySQL family requires.
		{`a\b`, `'a\b'`},
		{true, "TRUE"},
		{int64(42), "42"},
		{float64(2001), "2001"},
		{float64(1.5), "1.5"},
		{time.Date(2001, 7, 19, 10, 30, 0, 0, time.UTC), "TIMESTAMP '2001-07-19 10:30:00'"},
	}
	for _, c := range cases {
		got, err := d.Literal(c.in)
		if err != nil {
			t.Errorf("Literal(%v): %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("Literal(%v) = %q, want %q", c.in, got, c.want)
		}
	}
	if _, err := d.Literal(struct{}{}); err == nil {
		t.Error("unsupported type must error, not render")
	}
}

func TestSQLAtoms(t *testing.T) {
	d := Dialect{}
	if got := d.DateTrunc("month", `"d"."d_date"`); got != `DATE_TRUNC('month', "d"."d_date")` {
		t.Errorf("DateTrunc = %q", got)
	}
	if got := d.NullSafeEq("a", "b"); got != "a IS NOT DISTINCT FROM b" {
		t.Errorf("NullSafeEq = %q", got)
	}
	if got := d.CreateSchema(`"iceberg"."semantic_views"`); !strings.HasPrefix(got, "CREATE SCHEMA IF NOT EXISTS ") {
		t.Errorf("CreateSchema = %q", got)
	}
}
