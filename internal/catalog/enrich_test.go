package catalog

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type fakeEnricher struct {
	meta      map[string]TableMeta
	err       error
	gotTables []string
	gotDB     string
}

func (f *fakeEnricher) DescribeTables(_ context.Context, db string, tables []string) (map[string]TableMeta, error) {
	f.gotDB, f.gotTables = db, tables
	return f.meta, f.err
}

func demoTables() []Table {
	return []Table{
		{Name: "customer", Columns: []Column{{Name: "c_customer_sk"}, {Name: "c_email_address"}}},
		{Name: "store_sales", Columns: []Column{{Name: "ss_item_sk"}, {Name: "ss_ext_sales_price"}}},
	}
}

func TestEnrichCollectsMetadataAndConsequences(t *testing.T) {
	e := &fakeEnricher{meta: map[string]TableMeta{
		"customer": {
			Description: "One row per customer",
			Fields: map[string]FieldMeta{
				"c_email_address": {Description: "Contact email", Sensitive: true},
			},
		},
		"store_sales": {
			Synonyms:   []string{"sales", "transactions"},
			Deprecated: true,
			Fields: map[string]FieldMeta{
				"ss_ext_sales_price": {Synonyms: []string{"revenue"}},
			},
		},
	}}
	got := Enrich(context.Background(), e, "osi_demo", demoTables())

	if e.gotDB != "osi_demo" || !reflect.DeepEqual(e.gotTables, []string{"customer", "store_sales"}) {
		t.Fatalf("enricher called with db=%q tables=%v", e.gotDB, e.gotTables)
	}
	if want := []string{"customer.c_email_address"}; !reflect.DeepEqual(got.DeniedFields, want) {
		t.Errorf("DeniedFields = %v, want %v", got.DeniedFields, want)
	}
	if want := []string{"store_sales"}; !reflect.DeepEqual(got.DeprecatedTables, want) {
		t.Errorf("DeprecatedTables = %v, want %v", got.DeprecatedTables, want)
	}
	if fm, ok := got.Field("store_sales", "ss_ext_sales_price"); !ok || fm.Synonyms[0] != "revenue" {
		t.Errorf("field lookup = %+v, ok=%v", fm, ok)
	}
	if tm, ok := got.Table("customer"); !ok || tm.Description == "" {
		t.Errorf("table lookup = %+v, ok=%v", tm, ok)
	}
}

func TestEnrichIsNeverFatal(t *testing.T) {
	// A nil Enricher, a failing one, and a nil result must all degrade to an
	// empty Enrichment: derivation still produces a valid scaffold.
	for name, e := range map[string]Enricher{
		"nil":         nil,
		"error":       &fakeEnricher{err: errors.New("datahub unreachable")},
		"no-metadata": &fakeEnricher{},
	} {
		got := Enrich(context.Background(), e, "osi_demo", demoTables())
		if !got.Empty() || got.DeniedFields != nil || got.DeprecatedTables != nil {
			t.Errorf("%s: expected empty enrichment, got %+v", name, got)
		}
		if _, ok := got.Field("customer", "c_email_address"); ok {
			t.Errorf("%s: unexpected field metadata", name)
		}
	}
}

func TestEnrichIgnoresClassificationsOnMissingColumns(t *testing.T) {
	// A sensitivity tag on a column that no longer exists physically must not
	// become a denyFields entry: the resulting model would fail validation.
	e := &fakeEnricher{meta: map[string]TableMeta{
		"customer": {Fields: map[string]FieldMeta{
			"c_email_address": {Sensitive: true},
			"c_ssn_dropped":   {Sensitive: true},
		}},
	}}
	got := Enrich(context.Background(), e, "osi_demo", demoTables())
	if want := []string{"customer.c_email_address"}; !reflect.DeepEqual(got.DeniedFields, want) {
		t.Fatalf("DeniedFields = %v, want only physically present columns %v", got.DeniedFields, want)
	}
}

func TestEnrichOutputIsSorted(t *testing.T) {
	e := &fakeEnricher{meta: map[string]TableMeta{
		"store_sales": {Deprecated: true, Fields: map[string]FieldMeta{"ss_item_sk": {Sensitive: true}}},
		"customer":    {Deprecated: true, Fields: map[string]FieldMeta{"c_email_address": {Sensitive: true}}},
	}}
	got := Enrich(context.Background(), e, "osi_demo", demoTables())
	if !reflect.DeepEqual(got.DeniedFields, []string{"customer.c_email_address", "store_sales.ss_item_sk"}) {
		t.Errorf("DeniedFields not sorted: %v", got.DeniedFields)
	}
	if !reflect.DeepEqual(got.DeprecatedTables, []string{"customer", "store_sales"}) {
		t.Errorf("DeprecatedTables not sorted: %v", got.DeprecatedTables)
	}
}
