package planner

import (
	"strings"
	"testing"

	"github.com/KubedAI/semantic-operator/internal/emitter"
	_ "github.com/KubedAI/semantic-operator/internal/emitter/trino"
	"github.com/KubedAI/semantic-operator/internal/governance"
)

// These tests compile plans under a double-quote dialect and prove no
// MySQL-family backtick leaks out of the planner. The composite ratio path
// (CTE join, dedup subquery, val/mval columns) and governance row filters
// historically hardcoded backticks, so they get explicit coverage.

func trinoDialect(t *testing.T) emitter.Dialect {
	t.Helper()
	d, err := emitter.Get("trino")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestTrinoSimplePlanHasNoBackticks(t *testing.T) {
	cm := compiled(t)
	plan, err := Build(cm, trinoDialect(t), Request{
		Metrics:    []string{"total_sales"},
		Dimensions: []string{"item.i_category"},
		Filters:    []Filter{{Field: "date_dim.d_year", Op: "=", Value: 2001}},
	}, governance.Single("admin"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plan.SQL, "`") {
		t.Fatalf("backtick leaked into trino SQL:\n%s", plan.SQL)
	}
	for _, want := range []string{
		`"iceberg"."osi_demo"."store_sales"`,
		`SUM("store_sales"."ss_ext_sales_price") AS "total_sales"`,
		`WHERE ("date_dim"."d_year" = 2001)`,
	} {
		if !strings.Contains(plan.SQL, want) {
			t.Errorf("missing %q in:\n%s", want, plan.SQL)
		}
	}
}

func TestTrinoCompositeRatioWithRowFilterHasNoBackticks(t *testing.T) {
	cm := compiled(t)
	// store_productivity splits into num/den CTEs; the denominator aggregates
	// store across a fan-out join, forcing the dedup subquery (t/mval/val).
	// The tx_analyst role adds a row-filter predicate, exercising
	// QualifyBareColumns under the dialect quote function.
	plan, err := Build(cm, trinoDialect(t), Request{
		Metrics:    []string{"store_productivity"},
		Dimensions: []string{"store.s_state"},
	}, governance.Single("tx_analyst"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plan.SQL, "`") {
		t.Fatalf("backtick leaked into trino SQL:\n%s", plan.SQL)
	}
	for _, want := range []string{
		"IS NOT DISTINCT FROM",       // NullSafeEq on the CTE join
		`"val"`,                      // side-query output column
		`("store"."s_state" = 'TX')`, // governance row filter, dialect-quoted
		`"m_store_productivity_num"`, // CTE names quoted by dialect
	} {
		if !strings.Contains(plan.SQL, want) {
			t.Errorf("missing %q in:\n%s", want, plan.SQL)
		}
	}
}

func TestTrinoPlanIsDeterministic(t *testing.T) {
	cm := compiled(t)
	req := Request{
		Metrics:    []string{"customer_lifetime_value", "total_sales"},
		Dimensions: []string{"date_dim.d_year"},
	}
	a, err := Build(cm, trinoDialect(t), req, governance.Single("admin"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Build(cm, trinoDialect(t), req, governance.Single("admin"))
	if err != nil {
		t.Fatal(err)
	}
	if a.SQL != b.SQL {
		t.Fatalf("same request produced different SQL:\n%s\n---\n%s", a.SQL, b.SQL)
	}
}
