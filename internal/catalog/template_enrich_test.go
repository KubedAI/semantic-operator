package catalog

import (
	"bytes"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"

	"github.com/KubedAI/semantic-operator/api/v1alpha1"
)

func enrichTestTables() []Table {
	return []Table{
		{Name: "customer", Columns: []Column{
			{Name: "c_customer_sk", Type: "bigint"},
			{Name: "c_email_address", Type: "varchar"},
		}},
		{Name: "store_sales", Columns: []Column{
			{Name: "ss_item_sk", Type: "bigint"},
			{Name: "ss_ext_sales_price", Type: "decimal(7,2)", Comment: "raw catalog comment"},
		}},
	}
}

func enrichTestOptions() TemplateOptions {
	return TemplateOptions{
		CRName:    "retail",
		Namespace: "semantic-system",
		Catalog:   "polaris",
		Database:  "osi_demo",
		Model:     "retail_model",
	}
}

func fullEnrichment() Enrichment {
	return Enrichment{
		Tables: map[string]TableMeta{
			"customer": {
				Description: "One row per customer",
				Synonyms:    []string{"buyers", "accounts"},
				Fields: map[string]FieldMeta{
					"c_email_address": {Description: "Contact email", Sensitive: true},
				},
			},
			"store_sales": {
				Deprecated: true,
				Fields: map[string]FieldMeta{
					"ss_ext_sales_price": {Description: "Extended sales price", Synonyms: []string{"revenue"}},
				},
			},
		},
		DeniedFields: []string{"customer.c_email_address"},
	}
}

// renderEnriched renders and parses the scaffold, asserting it stays valid
// YAML that round-trips into the CRD type.
func renderEnriched(t *testing.T, e Enrichment) (string, v1alpha1.SemanticModel) {
	t.Helper()
	var buf bytes.Buffer
	if err := RenderTemplateEnriched(&buf, enrichTestOptions(), enrichTestTables(), e); err != nil {
		t.Fatalf("render: %v", err)
	}
	var cr v1alpha1.SemanticModel
	if err := yaml.UnmarshalStrict(buf.Bytes(), &cr); err != nil {
		t.Fatalf("enriched scaffold is not valid SemanticModel YAML: %v\n%s", err, buf.String())
	}
	return buf.String(), cr
}

func TestEnrichedScaffoldEmitsMetadataAsRealYAML(t *testing.T) {
	out, cr := renderEnriched(t, fullEnrichment())

	var customer, storeSales *v1alpha1.Dataset
	for i := range cr.Spec.Ossie.Datasets {
		switch cr.Spec.Ossie.Datasets[i].Name {
		case "customer":
			customer = &cr.Spec.Ossie.Datasets[i]
		case "store_sales":
			storeSales = &cr.Spec.Ossie.Datasets[i]
		}
	}
	if customer == nil || storeSales == nil {
		t.Fatalf("datasets missing from scaffold:\n%s", out)
	}
	if customer.Description != "One row per customer" {
		t.Errorf("dataset description = %q", customer.Description)
	}
	if ctx := v1alpha1.DecodeAIContext(customer.AIContext); len(ctx.Synonyms) != 2 || ctx.Synonyms[0] != "buyers" {
		t.Errorf("dataset synonyms = %+v", ctx.Synonyms)
	}
	// Curated upstream description wins over the raw catalog column comment.
	price := storeSales.FindField("ss_ext_sales_price")
	if price == nil || price.Description != "Extended sales price" {
		t.Errorf("field description not taken from enrichment: %+v", price)
	}
	if ctx := v1alpha1.DecodeAIContext(price.AIContext); len(ctx.Synonyms) != 1 || ctx.Synonyms[0] != "revenue" {
		t.Errorf("field synonyms = %+v", ctx.Synonyms)
	}
}

func TestEnrichedScaffoldEmitsGovernanceFromClassifications(t *testing.T) {
	_, cr := renderEnriched(t, fullEnrichment())
	g := cr.Spec.Governance
	if g == nil {
		t.Fatal("expected a governance block generated from sensitivity tags")
	}
	role := g.Role("analyst")
	if role == nil {
		t.Fatalf("expected an analyst role, got %+v", g.Roles)
	}
	if len(role.DenyFields) != 1 || role.DenyFields[0] != "customer.c_email_address" {
		t.Errorf("denyFields = %v", role.DenyFields)
	}
	if g.DefaultRole != "analyst" {
		t.Errorf("defaultRole = %q", g.DefaultRole)
	}
}

func TestEnrichedScaffoldFlagsDeprecatedForReview(t *testing.T) {
	out, cr := renderEnriched(t, fullEnrichment())
	if !strings.Contains(out, "REVIEW: your metadata catalog marks this dataset deprecated") {
		t.Errorf("deprecated dataset not flagged:\n%s", out)
	}
	// Flagged, never silently dropped: a human decides.
	if cr.Spec.Ossie.FindDataset("store_sales") == nil {
		t.Error("deprecated dataset must remain in the scaffold for review")
	}
}

func TestEnrichmentShrinksTODOs(t *testing.T) {
	var plain bytes.Buffer
	if err := RenderTemplate(&plain, enrichTestOptions(), enrichTestTables()); err != nil {
		t.Fatal(err)
	}
	enriched, _ := renderEnriched(t, fullEnrichment())

	before := strings.Count(plain.String(), "TODO")
	after := strings.Count(enriched, "TODO")
	if after >= before {
		t.Errorf("enrichment should reduce TODO count: plain=%d enriched=%d", before, after)
	}
	// Metrics are never imported: that decision stays with a human.
	if !strings.Contains(enriched, "metrics: []") {
		t.Error("metrics must remain a human decision in the enriched scaffold")
	}
}

func TestUnenrichedScaffoldIsUnchanged(t *testing.T) {
	// The zero Enrichment must produce exactly what the plain renderer does,
	// so enrichment cannot regress the default path.
	var plain, empty bytes.Buffer
	if err := RenderTemplate(&plain, enrichTestOptions(), enrichTestTables()); err != nil {
		t.Fatal(err)
	}
	if err := RenderTemplateEnriched(&empty, enrichTestOptions(), enrichTestTables(), Enrichment{}); err != nil {
		t.Fatal(err)
	}
	if plain.String() != empty.String() {
		t.Error("empty enrichment changed the scaffold output")
	}
	if strings.Contains(plain.String(), "imported from your") {
		t.Error("un-enriched scaffold must not claim imported metadata")
	}
}
