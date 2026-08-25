package catalog_test

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/KubedAI/semantic-operator/api/v1alpha1"
	"github.com/KubedAI/semantic-operator/internal/catalog"
	"github.com/KubedAI/semantic-operator/internal/ossie"
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
// parses and passes ossie.ValidateSpec with no edits, so `ossiectl validate`
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
	if err := ossie.ValidateSpec(&cr.Spec); err != nil {
		t.Fatalf("generated template failed validation: %v\n---\n%s", err, buf.String())
	}

	if cr.Spec.Ossie.Name != "tpcds_3tb_model" {
		t.Errorf("ossie.name = %q, want tpcds_3tb_model", cr.Spec.Ossie.Name)
	}
	if cr.Spec.Connection.Catalog != "glue" || cr.Spec.Connection.Database != "tpcds_3tb" {
		t.Errorf("connection = %+v, want catalog=glue database=tpcds_3tb", cr.Spec.Connection)
	}
	if len(cr.Spec.Ossie.Datasets) != 3 {
		t.Fatalf("datasets = %d, want 3", len(cr.Spec.Ossie.Datasets))
	}
}

func TestRenderTemplate_Golden(t *testing.T) {
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

	want, err := os.ReadFile("testdata/semanticmodel.golden.yaml")
	if err != nil {
		t.Fatalf("read golden file: %v", err)
	}
	if !bytes.Equal(buf.Bytes(), want) {
		t.Errorf("rendered template differs from golden file\n--- want\n%s\n--- got\n%s", want, buf.Bytes())
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

	dateDim := cr.Spec.Ossie.FindDataset("date_dim")
	if dateDim == nil {
		t.Fatal("date_dim dataset missing")
	}
	dDate := dateDim.FindField("d_date")
	if dDate == nil || dDate.Dimension == nil || !dDate.Dimension.IsTime {
		t.Errorf("d_date should have dimension.is_time=true, got %+v", dDate)
	}

	store := cr.Spec.Ossie.FindDataset("store")
	if store == nil {
		t.Fatal("store dataset missing")
	}
	sState := store.FindField("s_state")
	if sState == nil || sState.Description != "two-letter: state code" {
		t.Errorf("s_state description not carried: %+v", sState)
	}
	if sState.Dimension != nil {
		t.Errorf("derive must not certify non-time fields as dimensions: %+v", sState)
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
	if err := ossie.ValidateSpec(&cr.Spec); err != nil {
		t.Fatalf("edge-case template failed validation: %v\n---\n%s", err, buf.String())
	}
	odd := cr.Spec.Ossie.FindDataset("odd_table")
	if odd == nil || odd.FindField("weird col") == nil {
		t.Errorf("quoted field name %q not round-tripped", "weird col")
	}
	if empty := cr.Spec.Ossie.FindDataset("empty_table"); empty == nil || len(empty.Fields) != 0 {
		t.Errorf("empty_table should exist with zero fields, got %+v", empty)
	}
}

func TestRenderTemplate_QuotesDynamicStrings(t *testing.T) {
	comment := "line one\nline \"two\" \\ done"
	tables := []catalog.Table{{
		Name: "true",
		Columns: []catalog.Column{{
			Name:    "null",
			Type:    "varchar",
			Comment: comment,
		}},
	}}
	opts := catalog.TemplateOptions{
		CRName:    "true",
		Namespace: "null",
		Catalog:   "catalog: prod #1",
		Database:  "2026",
		Model:     "false",
	}

	var buf bytes.Buffer
	if err := catalog.RenderTemplate(&buf, opts, tables); err != nil {
		t.Fatalf("RenderTemplate: %v", err)
	}
	var cr v1alpha1.SemanticModel
	if err := yaml.UnmarshalStrict(buf.Bytes(), &cr); err != nil {
		t.Fatalf("quoted template is not parseable YAML: %v\n---\n%s", err, buf.String())
	}

	if cr.Name != opts.CRName || cr.Namespace != opts.Namespace {
		t.Errorf("metadata = %q/%q, want %q/%q", cr.Namespace, cr.Name, opts.Namespace, opts.CRName)
	}
	if cr.Spec.Connection.Catalog != opts.Catalog || cr.Spec.Connection.Database != opts.Database {
		t.Errorf("connection = %+v, want catalog=%q database=%q", cr.Spec.Connection, opts.Catalog, opts.Database)
	}
	if cr.Spec.Ossie.Name != opts.Model {
		t.Errorf("ossie.name = %q, want %q", cr.Spec.Ossie.Name, opts.Model)
	}
	dataset := cr.Spec.Ossie.FindDataset("true")
	if dataset == nil {
		t.Fatal("quoted dataset name did not round-trip")
	}
	field := dataset.FindField("null")
	if field == nil || field.Description != comment {
		t.Errorf("quoted field did not round-trip: %+v", field)
	}

	for _, want := range []string{
		`name: "true"`,
		`namespace: "null"`,
		`catalog: "catalog: prod #1"`,
		`database: "2026"`,
		`name: "false"`,
		`description: "line one\nline \"two\" \\ done"`,
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("rendered template missing quoted value %q\n%s", want, buf.String())
		}
	}
}

func TestRenderTemplate_QuotesUnicodeLineSeparatorInRelationshipComment(t *testing.T) {
	tables := []catalog.Table{
		{
			Name:    "orders",
			Columns: []catalog.Column{{Name: "o_item_sk", Type: "bigint"}},
		},
		{
			Name:    "item_\u2028injected",
			Columns: []catalog.Column{{Name: "i_item_sk", Type: "bigint"}},
		},
	}
	opts := catalog.TemplateOptions{CRName: "m", Namespace: "ns", Catalog: "glue", Database: "db", Model: "m"}

	var buf bytes.Buffer
	if err := catalog.RenderTemplate(&buf, opts, tables); err != nil {
		t.Fatalf("RenderTemplate: %v", err)
	}
	var cr v1alpha1.SemanticModel
	if err := yaml.UnmarshalStrict(buf.Bytes(), &cr); err != nil {
		t.Fatalf("template with Unicode line separator is not parseable YAML: %v\n---\n%s", err, buf.String())
	}
	if strings.Contains(buf.String(), "\u2028") {
		t.Fatalf("relationship comment contains an unescaped Unicode line separator:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), `\u2028`) {
		t.Fatalf("relationship comment does not contain the escaped Unicode line separator:\n%s", buf.String())
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
	if !strings.Contains(out, `#  - name: "store_sales_to_store"`) {
		t.Errorf("expected commented candidate relationship store_sales_to_store, got:\n%s", out)
	}
}
