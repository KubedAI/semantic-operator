package ossie

import (
	"strings"
	"testing"

	"github.com/KubedAI/semantic-operator/api/v1alpha1"
)

func field(name, expr string, isTime bool) v1alpha1.Field {
	f := v1alpha1.Field{
		Name: name,
		Expression: v1alpha1.Expression{Dialects: []v1alpha1.DialectExpression{
			{Dialect: "ANSI_SQL", Expression: expr},
		}},
	}
	if isTime {
		f.Dimension = &v1alpha1.Dimension{IsTime: true}
	}
	return f
}

func validSpec() *v1alpha1.SemanticModelSpec {
	return &v1alpha1.SemanticModelSpec{
		Connection: v1alpha1.ConnectionSpec{Catalog: "iceberg", Database: "osi_demo"},
		Ossie: v1alpha1.OssieModel{
			Name: "test_model",
			Datasets: []v1alpha1.Dataset{
				{
					Name:       "store_sales",
					Source:     "store_sales",
					PrimaryKey: []string{"ss_item_sk", "ss_ticket_number"},
					Fields: []v1alpha1.Field{
						field("ss_sold_date_sk", "ss_sold_date_sk", false),
						field("ss_customer_sk", "ss_customer_sk", false),
						field("ss_ext_sales_price", "ss_ext_sales_price", false),
					},
				},
				{
					Name:       "date_dim",
					Source:     "date_dim",
					PrimaryKey: []string{"d_date_sk"},
					Fields: []v1alpha1.Field{
						field("d_date_sk", "d_date_sk", false),
						field("d_date", "d_date", true),
						field("d_year", "d_year", false),
					},
				},
				{
					Name:       "customer",
					Source:     "customer",
					PrimaryKey: []string{"c_customer_sk"},
					Fields: []v1alpha1.Field{
						field("c_customer_sk", "c_customer_sk", false),
						field("c_email_address", "c_email_address", false),
					},
				},
			},
			Relationships: []v1alpha1.Relationship{
				{Name: "sales_to_date", From: "store_sales", To: "date_dim",
					FromColumns: []string{"ss_sold_date_sk"}, ToColumns: []string{"d_date_sk"}},
				{Name: "sales_to_customer", From: "store_sales", To: "customer",
					FromColumns: []string{"ss_customer_sk"}, ToColumns: []string{"c_customer_sk"}},
			},
			Metrics: []v1alpha1.Metric{
				{Name: "total_sales", Expression: v1alpha1.Expression{Dialects: []v1alpha1.DialectExpression{
					{Dialect: "ANSI_SQL", Expression: "SUM(store_sales.ss_ext_sales_price)"},
				}}},
				{Name: "clv", Expression: v1alpha1.Expression{Dialects: []v1alpha1.DialectExpression{
					{Dialect: "ANSI_SQL", Expression: "SUM(store_sales.ss_ext_sales_price) / COUNT(DISTINCT customer.c_customer_sk)"},
				}}},
			},
		},
		Governance: &v1alpha1.GovernanceSpec{
			DefaultRole: "analyst",
			Roles: []v1alpha1.RolePolicy{
				{Name: "analyst", AllowMetrics: []string{"*"},
					DenyFields: []string{"customer.c_email_address"},
					RowFilters: []v1alpha1.RowFilter{{Dataset: "date_dim", Predicate: "d_year >= 2000"}}},
			},
		},
		Views: []v1alpha1.ViewSpec{
			{Name: "sales_by_year", Metrics: []string{"total_sales"}, Dimensions: []string{"date_dim.d_year"}},
		},
	}
}

func TestValidateSpecOK(t *testing.T) {
	if err := ValidateSpec(validSpec()); err != nil {
		t.Fatalf("valid spec rejected: %v", err)
	}
}

func wantErr(t *testing.T, spec *v1alpha1.SemanticModelSpec, substr string) {
	t.Helper()
	err := ValidateSpec(spec)
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", substr)
	}
	if !strings.Contains(err.Error(), substr) {
		t.Fatalf("expected error containing %q, got: %v", substr, err)
	}
}

func TestValidateDuplicateDataset(t *testing.T) {
	s := validSpec()
	s.Ossie.Datasets = append(s.Ossie.Datasets, s.Ossie.Datasets[0])
	wantErr(t, s, "duplicate name")
}

func TestValidateUnknownRelationshipDataset(t *testing.T) {
	s := validSpec()
	s.Ossie.Relationships[0].To = "nope"
	wantErr(t, s, `to dataset "nope" does not exist`)
}

func TestValidateColumnCountMismatch(t *testing.T) {
	s := validSpec()
	s.Ossie.Relationships[0].ToColumns = []string{"a", "b"}
	wantErr(t, s, "same length")
}

func TestValidateCycle(t *testing.T) {
	s := validSpec()
	s.Ossie.Relationships = append(s.Ossie.Relationships, v1alpha1.Relationship{
		Name: "back", From: "date_dim", To: "store_sales",
		FromColumns: []string{"d_date_sk"}, ToColumns: []string{"ss_sold_date_sk"},
	})
	wantErr(t, s, "cycle")
}

func TestValidateBadMetricExpression(t *testing.T) {
	s := validSpec()
	s.Ossie.Metrics[0].Expression.Dialects[0].Expression = "SUM(ss_ext_sales_price)"
	wantErr(t, s, "dataset.field")
}

func TestValidateMetricUnknownField(t *testing.T) {
	s := validSpec()
	s.Ossie.Metrics[0].Expression.Dialects[0].Expression = "SUM(store_sales.nope)"
	wantErr(t, s, `unknown field "nope"`)
}

func TestValidateRatioDenominatorNeedsPK(t *testing.T) {
	s := validSpec()
	s.Ossie.Datasets[2].PrimaryKey = nil
	s.Ossie.Metrics[1].Expression.Dialects[0].Expression =
		"SUM(store_sales.ss_ext_sales_price) / NULLIF(SUM(customer.c_customer_sk), 0)"
	wantErr(t, s, "no primary_key")
}

func TestValidateGovernanceBadDenyField(t *testing.T) {
	s := validSpec()
	s.Governance.Roles[0].DenyFields = []string{"not_a_ref"}
	wantErr(t, s, "denyFields")
}

func TestValidateGovernanceUnknownDefaultRole(t *testing.T) {
	s := validSpec()
	s.Governance.DefaultRole = "ghost"
	wantErr(t, s, "defaultRole")
}

func TestValidateViewUnknownMetric(t *testing.T) {
	s := validSpec()
	s.Views[0].Metrics = []string{"ghost_metric"}
	wantErr(t, s, `metric "ghost_metric" does not exist`)
}

// A row filter interpolating a claim must pass publication. The predicate
// grammar accepts only SQL literals, so before the shared substitution helper
// existed the whole feature was rejected here and could never be used.
func TestValidateAcceptsClaimPlaceholderInRowFilter(t *testing.T) {
	spec := validSpec()
	spec.Governance = &v1alpha1.GovernanceSpec{
		DefaultRole: "analyst",
		Roles: []v1alpha1.RolePolicy{{
			Name: "analyst", AllowMetrics: []string{"*"},
			RowFilters: []v1alpha1.RowFilter{
				{Dataset: "store_sales", Predicate: "s_state = {{claim.tenant_id}}"},
			},
		}},
	}
	if err := ValidateSpec(spec); err != nil {
		t.Fatalf("claim-based row filter rejected at publication: %v", err)
	}
}

// A typo in the template must not be silently passed through into a WHERE
// clause on the engine.
func TestValidateRejectsMalformedClaimTemplate(t *testing.T) {
	spec := validSpec()
	spec.Governance = &v1alpha1.GovernanceSpec{
		DefaultRole: "analyst",
		Roles: []v1alpha1.RolePolicy{{
			Name: "analyst", AllowMetrics: []string{"*"},
			RowFilters: []v1alpha1.RowFilter{
				{Dataset: "store_sales", Predicate: "s_state = {{claim tenant_id}}"},
			},
		}},
	}
	wantErr(t, spec, "malformed template syntax")
}

func TestValidateExternalAuthorization(t *testing.T) {
	s := validSpec()
	s.Governance.External = &v1alpha1.ExternalAuthorizationSpec{ProviderRef: "corp-opa"}
	if err := ValidateSpec(s); err != nil {
		t.Fatalf("valid external authorization rejected: %v", err)
	}

	for _, provider := range []string{"Corp-OPA", "-opa", "opa-", "opa/provider"} {
		t.Run(provider, func(t *testing.T) {
			s := validSpec()
			s.Governance.External = &v1alpha1.ExternalAuthorizationSpec{ProviderRef: provider}
			wantErr(t, s, "providerRef")
		})
	}
}
