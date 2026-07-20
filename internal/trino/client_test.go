package trino

import (
	"strings"
	"testing"
)

func TestDescribeQuery(t *testing.T) {
	q := describeQuery("iceberg", "osi_demo", "store_sales")
	want := `SELECT column_name, data_type FROM "iceberg".information_schema.columns ` +
		`WHERE table_schema = 'osi_demo' AND table_name = 'store_sales' ORDER BY ordinal_position`
	if q != want {
		t.Errorf("describeQuery =\n%q\nwant\n%q", q, want)
	}
}

func TestDescribeQueryEscapesLiterals(t *testing.T) {
	q := describeQuery(`ice"berg`, "o'demo", "ta'ble")
	if !strings.Contains(q, `"ice""berg"`) {
		t.Errorf("catalog identifier not escaped: %q", q)
	}
	if !strings.Contains(q, "'o''demo'") || !strings.Contains(q, "'ta''ble'") {
		t.Errorf("string literals not escaped: %q", q)
	}
}
