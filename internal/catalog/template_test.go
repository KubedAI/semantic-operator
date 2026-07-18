package catalog_test

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/vara-bonthu/osi-semantic-operator/api/v1alpha1"
	"github.com/vara-bonthu/osi-semantic-operator/internal/catalog"
	"github.com/vara-bonthu/osi-semantic-operator/internal/osi"
)

func sampleTables() []catalog.Table {
	return []catalog.Table{
		{
			Name: "store_sales",
			Columns: []catalog.Column{
				{Name: "ss_item_sk", Type: "bigint"},
				{Name: "ss_store_sk", Type: "bigint"},
				{Name: "ss_sold_date_sk", Type: "bigint"},
				{Name: "ss_ext_sales_price", Type: "decimal(7,2)"},
			},
		},
		{
			Name: "store",
			Columns: []catalog.Column{
				{Name: "s_store_sk", Type: "bigint"},
				{Name: "s_state", Type: "char(2)", Comment: "two-letter: state code"},
			},
		},
		{
			Name: "date_dim",
			Columns: []catalog.Column{
				{Name: "d_date_sk", Type: "bigint"},
				{Name: "d_date", Type: "date"},
			},
		},
	}
}

// TestRenderTemplate_ValidOutOfBox is the key guarantee: the generated scaffold
// parses and passes osi.ValidateSpec with no edits, so `osictl validate`
// succeeds immediately on a freshly derived file.
func TestRenderTemplate_ValidOutOfBox(t *testing.T) {
	var buf bytes.Buffer
	opts := catalog.TemplateOptions{
		CRName:    "derived-model",
		Namespace: "semantic-system",
		Catalog:   "glue",
		Database:  "tpcds_3tb",
		Model:     "tpcds_3tb_model",
	}
	if err := catalog.RenderTemplate(&buf, opts, sampleTables()); err != nil {
		t.Fatalf("RenderTemplate: %v", err)
	}

	var cr v1alpha1.SemanticModel
	if err := yaml.UnmarshalStrict(buf.Bytes(), &cr); err != nil {
		t.Fatalf("generated template is not parseable YAML: %v\n---\n%s", err, buf.String())
	}
	if err := osi.ValidateSpec(&cr.Spec); err != nil {
		t.Fatalf("generated template failed validation: %v\n---\n%s", err, buf.String())
	}

	if cr.Spec.OSI.Name != "tpcds_3tb_model" {
		t.Errorf("osi.name = %q, want tpcds_3tb_model", cr.Spec.OSI.Name)
	}
	if cr.Spec.Connection.Catalog != "glue" || cr.Spec.Connection.Database != "tpcds_3tb" {
		t.Errorf("connection = %+v, want catalog=glue database=tpcds_3tb", cr.Spec.Connection)
	}
	if len(cr.Spec.OSI.Datasets) != 3 {
		t.Fatalf("datasets = %d, want 3", len(cr.Spec.OSI.Datasets))
	}
}

// TestRenderTemplate_TimeDimensionAndFields checks physical extraction: fields
// are populated, is_time is set on date columns, and descriptions are carried.
func TestRenderTemplate_TimeDimensionAndFields(t *testing.T) {
	var buf bytes.Buffer
	opts := catalog.TemplateOptions{CRName: "m", Namespace: "ns", Catalog: "glue", Database: "db", Model: "m"}
	if err := catalog.RenderTemplate(&buf, opts, sampleTables()); err != nil {
		t.Fatalf("RenderTemplate: %v", err)
	}
	var cr v1alpha1.SemanticModel
	if err := yaml.UnmarshalStrict(buf.Bytes(), &cr); err != nil {
		t.Fatalf("parse: %v", err)
	}

	dateDim := cr.Spec.OSI.FindDataset("date_dim")
	if dateDim == nil {
		t.Fatal("date_dim dataset missing")
	}
	dDate := dateDim.FindField("d_date")
	if dDate == nil || dDate.Dimension == nil || !dDate.Dimension.IsTime {
		t.Errorf("d_date should have dimension.is_time=true, got %+v", dDate)
	}

	store := cr.Spec.OSI.FindDataset("store")
	if store == nil {
		t.Fatal("store dataset missing")
	}
	sState := store.FindField("s_state")
	if sState == nil || sState.Description != "two-letter: state code" {
		t.Errorf("s_state description not carried: %+v", sState)
	}
}

// TestRenderTemplate_EdgeCases covers arbitrary catalogs: a column name that is
// not a bare identifier must be quoted, and a table with no columns must still
// produce a parseable, valid dataset. The output must remain schema-valid.
func TestRenderTemplate_EdgeCases(t *testing.T) {
	tables := []catalog.Table{
		{
			Name: "odd_table",
			Columns: []catalog.Column{
				{Name: "weird col", Type: "varchar"}, // space forces quoting
				{Name: "amount", Type: "decimal(10,2)"},
			},
		},
		{Name: "empty_table"}, // no columns
	}
	var buf bytes.Buffer
	opts := catalog.TemplateOptions{CRName: "m", Namespace: "ns", Catalog: "glue", Database: "db", Model: "m"}
	if err := catalog.RenderTemplate(&buf, opts, tables); err != nil {
		t.Fatalf("RenderTemplate: %v", err)
	}
	var cr v1alpha1.SemanticModel
	if err := yaml.UnmarshalStrict(buf.Bytes(), &cr); err != nil {
		t.Fatalf("edge-case template is not parseable YAML: %v\n---\n%s", err, buf.String())
	}
	if err := osi.ValidateSpec(&cr.Spec); err != nil {
		t.Fatalf("edge-case template failed validation: %v\n---\n%s", err, buf.String())
	}
	odd := cr.Spec.OSI.FindDataset("odd_table")
	if odd == nil || odd.FindField("weird col") == nil {
		t.Errorf("quoted field name %q not round-tripped", "weird col")
	}
	if empty := cr.Spec.OSI.FindDataset("empty_table"); empty == nil || len(empty.Fields) != 0 {
		t.Errorf("empty_table should exist with zero fields, got %+v", empty)
	}
}

// TestRenderTemplate_WriteError verifies write failures are surfaced rather than
// silently swallowed.
func TestRenderTemplate_WriteError(t *testing.T) {
	opts := catalog.TemplateOptions{CRName: "m", Namespace: "ns", Catalog: "glue", Database: "db", Model: "m"}
	if err := catalog.RenderTemplate(failWriter{}, opts, sampleTables()); err == nil {
		t.Fatal("expected a write error to propagate, got nil")
	}
}

type failWriter struct{}

func (failWriter) Write(p []byte) (int, error) { return 0, io.ErrShortWrite }

// TestRenderTemplate_Placeholders verifies the human-owned sections are present
// as guidance so business users know what to fill.
func TestRenderTemplate_Placeholders(t *testing.T) {
	var buf bytes.Buffer
	opts := catalog.TemplateOptions{CRName: "m", Namespace: "ns", Catalog: "glue", Database: "db", Model: "m"}
	if err := catalog.RenderTemplate(&buf, opts, sampleTables()); err != nil {
		t.Fatalf("RenderTemplate: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"primary_key: []",
		"# ai_context:",
		"relationships: []",
		"metrics: []",
		"# governance:",
		"# views:",
		"TODO",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("template missing expected placeholder %q", want)
		}
	}

	// Candidate relationships should be inferred from the *_sk naming and
	// emitted commented-out (never as live relationships).
	if !strings.Contains(out, "#  - name: store_sales_to_store") {
		t.Errorf("expected commented candidate relationship store_sales_to_store, got:\n%s", out)
	}
}
