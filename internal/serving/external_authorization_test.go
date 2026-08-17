package serving

import (
	"context"
	"errors"
	"testing"

	"github.com/KubedAI/semantic-operator/api/v1alpha1"
	"github.com/KubedAI/semantic-operator/internal/emitter/starrocks"
	"github.com/KubedAI/semantic-operator/internal/governance"
	"github.com/KubedAI/semantic-operator/internal/planner"
	"github.com/KubedAI/semantic-operator/internal/serving/authorization"
)

type stubAuthorizer struct {
	decision authorization.Decision
	err      error
	calls    int
	ref      string
	input    authorization.Input
}

func (s *stubAuthorizer) Authorize(_ context.Context, ref string, input authorization.Input) (authorization.Decision, error) {
	s.calls++
	s.ref, s.input = ref, input
	return s.decision, s.err
}

func externalModel(t *testing.T) *planner.CompiledModel {
	t.Helper()
	spec := &v1alpha1.SemanticModelSpec{
		Connection: v1alpha1.ConnectionSpec{Catalog: "iceberg", Database: "retail"},
		Ossie: v1alpha1.OssieModel{
			Name: "retail",
			Datasets: []v1alpha1.Dataset{{
				Name: "sales", Source: "sales",
				Fields: []v1alpha1.Field{{
					Name: "amount", Expression: v1alpha1.Expression{Dialects: []v1alpha1.DialectExpression{{Dialect: "ANSI_SQL", Expression: "amount"}}},
				}},
			}},
			Metrics: []v1alpha1.Metric{
				{Name: "revenue", Expression: v1alpha1.Expression{Dialects: []v1alpha1.DialectExpression{{Dialect: "ANSI_SQL", Expression: "SUM(sales.amount)"}}}},
				{Name: "payroll", Expression: v1alpha1.Expression{Dialects: []v1alpha1.DialectExpression{{Dialect: "ANSI_SQL", Expression: "SUM(sales.amount)"}}}},
			},
		},
		Governance: &v1alpha1.GovernanceSpec{
			DefaultRole: "analyst",
			Roles:       []v1alpha1.RolePolicy{{Name: "analyst", AllowMetrics: []string{"revenue"}}},
			External:    &v1alpha1.ExternalAuthorizationSpec{ProviderRef: "corp-opa"},
		},
	}
	model, err := planner.Compile(spec, "analytics", "retail-model")
	if err != nil {
		t.Fatal(err)
	}
	return model
}

func TestExternalAuthorizationRunsBeforePlanningAndFailsClosed(t *testing.T) {
	model := externalModel(t)
	deny := &stubAuthorizer{err: governance.ErrUnauthorized}
	svc := &Service{Dialect: starrocks.Dialect{}, Authorization: deny}

	// The metric is intentionally unknown. External denial must win, proving
	// the provider runs before planning and before anything can be cached.
	_, _, err := svc.Plan(context.Background(), "test", model, planner.Request{Metrics: []string{"unknown"}}, governance.Single("analyst"))
	if !errors.Is(err, governance.ErrUnauthorized) {
		t.Fatalf("error = %v, want external denial", err)
	}
	if deny.calls != 1 || deny.ref != "corp-opa" {
		t.Fatalf("authorizer call = %+v", deny)
	}

	svc.Authorization = nil
	_, _, err = svc.Plan(context.Background(), "test", model, planner.Request{Metrics: []string{"revenue"}}, governance.Single("analyst"))
	if !errors.Is(err, authorization.ErrUnavailable) {
		t.Fatalf("missing provider error = %v, want ErrUnavailable", err)
	}
}

func TestExternalAllowPreservesBuiltInGovernanceAndFingerprint(t *testing.T) {
	model := externalModel(t)
	allow := &stubAuthorizer{decision: authorization.Decision{Allow: true, Revision: "bundle-8"}}
	svc := &Service{Dialect: starrocks.Dialect{}, Authorization: allow}

	plan, cached, err := svc.Plan(context.Background(), "test", model, planner.Request{Metrics: []string{"revenue"}}, governance.Single("analyst"))
	if err != nil {
		t.Fatal(err)
	}
	if cached || plan.AuthorizationFingerprint == "" {
		t.Fatalf("plan provenance = %+v cached=%v", plan, cached)
	}
	if allow.input.Model.Namespace != "analytics" || allow.input.Model.Resource != "retail-model" {
		t.Fatalf("provider did not receive model identity: %+v", allow.input.Model)
	}
	if allow.input.Environment.Adapter != "test" || allow.input.Environment.AccessTimeUnixMilli <= 0 {
		t.Fatalf("provider did not receive trusted request environment: %+v", allow.input.Environment)
	}

	// OPA is additive. It cannot grant a metric that the built-in role denies.
	_, _, err = svc.Plan(context.Background(), "test", model, planner.Request{Metrics: []string{"payroll"}}, governance.Single("analyst"))
	if !errors.Is(err, governance.ErrUnauthorized) {
		t.Fatalf("built-in denial was bypassed: %v", err)
	}
	if allow.calls != 2 {
		t.Fatalf("provider should run for every plan attempt, calls=%d", allow.calls)
	}
}

func TestModelWithoutExternalAuthorizationRemainsCompatible(t *testing.T) {
	model := externalModel(t)
	model.Governance.External = nil
	svc := &Service{Dialect: starrocks.Dialect{}}
	plan, _, err := svc.Plan(context.Background(), "test", model, planner.Request{Metrics: []string{"revenue"}}, governance.Single("analyst"))
	if err != nil {
		t.Fatal(err)
	}
	if plan.AuthorizationFingerprint != "" {
		t.Fatalf("legacy model received external fingerprint %q", plan.AuthorizationFingerprint)
	}
}
