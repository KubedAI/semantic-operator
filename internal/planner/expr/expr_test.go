package expr

import (
	"reflect"
	"testing"
)

func TestParseSimpleSum(t *testing.T) {
	m, err := Parse("SUM(store_sales.ss_ext_sales_price)")
	if err != nil {
		t.Fatal(err)
	}
	if m.Numerator.Func != "SUM" || m.Numerator.Distinct || m.Denominator != nil {
		t.Fatalf("unexpected parse: %+v", m)
	}
	want := []FieldRef{{Dataset: "store_sales", Field: "ss_ext_sales_price"}}
	if !reflect.DeepEqual(m.Numerator.Refs, want) {
		t.Fatalf("refs = %v, want %v", m.Numerator.Refs, want)
	}
}

func TestParseCountDistinct(t *testing.T) {
	m, err := Parse("COUNT(DISTINCT customer.c_customer_sk)")
	if err != nil {
		t.Fatal(err)
	}
	if m.Numerator.Func != "COUNT" || !m.Numerator.Distinct {
		t.Fatalf("unexpected parse: %+v", m)
	}
	if m.Numerator.Scalar != "customer.c_customer_sk" {
		t.Fatalf("scalar = %q", m.Numerator.Scalar)
	}
}

func TestParseRatio(t *testing.T) {
	m, err := Parse("SUM(store_sales.ss_ext_sales_price) / COUNT(DISTINCT customer.c_customer_sk)")
	if err != nil {
		t.Fatal(err)
	}
	if m.Denominator == nil || m.Denominator.Func != "COUNT" || !m.Denominator.Distinct {
		t.Fatalf("unexpected parse: %+v", m)
	}
	if got := m.Datasets(); !reflect.DeepEqual(got, []string{"customer", "store_sales"}) {
		t.Fatalf("datasets = %v", got)
	}
}

func TestParseRatioWithNullif(t *testing.T) {
	m, err := Parse("SUM(store_sales.ss_ext_sales_price) / NULLIF(SUM(store.s_number_employees), 0)")
	if err != nil {
		t.Fatal(err)
	}
	if m.Denominator == nil || m.Denominator.Func != "SUM" || m.Denominator.Scalar != "store.s_number_employees" {
		t.Fatalf("unexpected parse: %+v", m)
	}
}

func TestParseScalarExpression(t *testing.T) {
	m, err := Parse("SUM(store_sales.ss_quantity * store_sales.ss_sales_price)")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Numerator.Refs) != 2 {
		t.Fatalf("refs = %v", m.Numerator.Refs)
	}
}

func TestParseRejectsBareColumns(t *testing.T) {
	if _, err := Parse("SUM(ss_ext_sales_price)"); err == nil {
		t.Fatal("expected error for bare column")
	}
}

func TestParseRejectsUnknownFunction(t *testing.T) {
	if _, err := Parse("MEDIAN(store_sales.ss_quantity)"); err == nil {
		t.Fatal("expected error for unsupported aggregate")
	}
}

func TestParseRejectsTrailingInput(t *testing.T) {
	if _, err := Parse("SUM(a.b) + SUM(a.c)"); err == nil {
		t.Fatal("expected error for '+' operator outside grammar")
	}
}

func TestParseIsDeterministic(t *testing.T) {
	const in = "SUM(store_sales.ss_ext_sales_price) / NULLIF(SUM(store.s_number_employees), 0)"
	a, err := Parse(in)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Parse(in)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("parse is not deterministic")
	}
}

func TestRewriteRefs(t *testing.T) {
	out := RewriteRefs("a.x * b.y + 'lit.eral'", func(r FieldRef) string {
		return "<" + r.String() + ">"
	})
	if out != "<a.x> * <b.y> + 'lit.eral'" {
		t.Fatalf("out = %q", out)
	}
}

func TestQualifyBareColumns(t *testing.T) {
	cases := map[string]string{
		"s_state = 'TX'":                    "`s`.`s_state` = 'TX'",
		"c_first_name || ' ' || c_last_name": "`s`.`c_first_name` || ' ' || `s`.`c_last_name`",
		"LOWER(email)":                      "LOWER(`s`.`email`)",
		"price > 100 AND qty IS NOT NULL":   "`s`.`price` > 100 AND `s`.`qty` IS NOT NULL",
	}
	for in, want := range cases {
		if got := QualifyBareColumns(in, "`s`"); got != want {
			t.Errorf("QualifyBareColumns(%q) = %q, want %q", in, got, want)
		}
	}
}
