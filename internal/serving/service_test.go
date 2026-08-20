package serving

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/KubedAI/semantic-operator/api/v1alpha1"
	"github.com/KubedAI/semantic-operator/internal/governance"
	"github.com/KubedAI/semantic-operator/internal/planner"
)

func identityBlob(t *testing.T, name, version, ns, resource string) []byte {
	t.Helper()
	b, err := json.Marshal(planner.CompiledModel{
		Name: name, Version: version, Namespace: ns, Resource: resource,
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// A model-name collision must be attributable: the listing carries the
// publishing resource, and the resolve error names both resources.
func TestModelsListingCarriesResourceIdentity(t *testing.T) {
	store := NewStore()
	_ = store.Put("sm-a-compiled", identityBlob(t, "sales", "v1", "team-a", "retail"))
	_ = store.Put("sm-b-compiled", identityBlob(t, "sales", "v1", "team-b", "retail"))
	svc := &Service{Store: store}

	models := svc.Models(governance.Identity{})
	if len(models) != 2 {
		t.Fatalf("Models: want both colliding entries listed, got %d", len(models))
	}
	for _, m := range models {
		if m.Namespace == "" || m.Resource == "" {
			t.Fatalf("Models: entry missing identity: %+v", m)
		}
	}

	_, err := svc.Resolve("sales", governance.Identity{})
	if err == nil {
		t.Fatal("Resolve(sales): expected ambiguity error")
	}
	for _, ref := range []string{"team-a/retail", "team-b/retail"} {
		if !strings.Contains(err.Error(), ref) {
			t.Fatalf("ambiguity error should name %s, got: %v", ref, err)
		}
	}
}

func TestResolveUnknownModel(t *testing.T) {
	store := NewStore()
	_ = store.Put("sm-a-compiled", identityBlob(t, "sales", "v1", "team-a", "retail"))
	svc := &Service{Store: store}
	if _, err := svc.Resolve("inventory", governance.Identity{}); err == nil {
		t.Fatal("Resolve(inventory): expected unknown-model error")
	}
	if m, err := svc.Resolve("sales", governance.Identity{}); err != nil || m.Name != "sales" {
		t.Fatalf("Resolve(sales): want ok, got %v err=%v", m, err)
	}
}

func TestResolveUnnamedAmbiguousSingleName(t *testing.T) {
	store := NewStore()
	_ = store.Put("sm-a-compiled", identityBlob(t, "sales", "v1", "team-a", "retail"))
	_ = store.Put("sm-b-compiled", identityBlob(t, "sales", "v2", "team-b", "retail"))
	svc := &Service{Store: store}

	_, err := svc.Resolve("", governance.Identity{})
	if err == nil {
		t.Fatal("Resolve(\"\"): expected ambiguity error")
	}
	for _, want := range []string{
		`model name "sales" is published by more than one resource`,
		"team-a/retail",
		"team-b/retail",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Resolve(\"\") error should contain %q, got: %v", want, err)
		}
	}
}

// governedModel is a two-metric, two-field model where the analyst role may
// see one metric and is denied one field. It is the smallest shape that shows
// discovery and enforcement agreeing.
func governedModel() *planner.CompiledModel {
	return &planner.CompiledModel{
		Name: "retail", Version: "v1",
		MetricOrder: []string{"revenue", "payroll_cost"},
		Metrics: map[string]*planner.CompiledMetric{
			"revenue":      {Name: "revenue", Raw: "SUM(s.amount)"},
			"payroll_cost": {Name: "payroll_cost", Raw: "SUM(e.salary)"},
		},
		DatasetOrder: []string{"store"},
		Datasets: map[string]*planner.CompiledDataset{
			"store": {
				Name: "store", FieldOrder: []string{"s_state", "manager_ssn"},
				Fields: map[string]*planner.CompiledField{
					"s_state":     {Name: "s_state"},
					"manager_ssn": {Name: "manager_ssn"},
				},
			},
		},
		Governance: &v1alpha1.GovernanceSpec{
			DefaultRole: "analyst",
			Roles: []v1alpha1.RolePolicy{
				{Name: "analyst", AllowMetrics: []string{"revenue"}, DenyFields: []string{"store.manager_ssn"}},
				{Name: "admin", AllowMetrics: []string{"*"}},
			},
		},
	}
}

// Discovery must hide what a query would refuse. Before this, listing returned
// every metric and every column regardless of role, so a caller learned the
// name of a metric and a PII column it could never read.
func TestDiscoveryHidesWhatTheRoleCannotQuery(t *testing.T) {
	svc := &Service{Store: NewStore()}
	m := governedModel()

	metrics, err := svc.ListMetrics(m, governance.Single("analyst"))
	if err != nil {
		t.Fatalf("ListMetrics: %v", err)
	}
	if len(metrics) != 1 || metrics[0].Name != "revenue" {
		t.Fatalf("analyst should see only revenue, got %+v", metrics)
	}

	dims, err := svc.ListDimensions(m, governance.Single("analyst"))
	if err != nil {
		t.Fatalf("ListDimensions: %v", err)
	}
	for _, d := range dims {
		if d.Name == "store.manager_ssn" {
			t.Fatalf("denied field disclosed in listing: %+v", dims)
		}
	}

	// The admin role sees everything, proving the filter is role-driven and not
	// a blanket suppression.
	adminMetrics, err := svc.ListMetrics(m, governance.Single("admin"))
	if err != nil {
		t.Fatalf("ListMetrics(admin): %v", err)
	}
	if len(adminMetrics) != 2 {
		t.Fatalf("admin should see both metrics, got %+v", adminMetrics)
	}
}

// Listing and enforcement read the same policy, so anything discovery shows
// must actually be queryable, and anything it hides must actually be refused.
func TestDiscoveryAgreesWithEnforcement(t *testing.T) {
	svc := &Service{Store: NewStore()}
	m := governedModel()
	id := governance.Single("analyst")

	metrics, err := svc.ListMetrics(m, id)
	if err != nil {
		t.Fatalf("ListMetrics: %v", err)
	}
	for _, mi := range metrics {
		if _, err := governance.Authorize(m.Governance, id, []string{mi.Name}, nil); err != nil {
			t.Fatalf("listed metric %q is refused by Authorize: %v", mi.Name, err)
		}
	}
	if _, err := governance.Authorize(m.Governance, id, []string{"payroll_cost"}, nil); err == nil {
		t.Fatal("hidden metric payroll_cost was authorized")
	}
}

// The raw SQL expression is the definition itself, so it stays out of listings
// unless explicitly turned on.
func TestExpressionsAreSuppressedByDefault(t *testing.T) {
	m := governedModel()
	id := governance.Single("admin")

	off, err := (&Service{Store: NewStore()}).ListMetrics(m, id)
	if err != nil {
		t.Fatalf("ListMetrics: %v", err)
	}
	for _, mi := range off {
		if mi.Expression != "" {
			t.Fatalf("expression leaked with ExposeExpressions off: %+v", mi)
		}
	}

	on, err := (&Service{Store: NewStore(), ExposeExpressions: true}).ListMetrics(m, id)
	if err != nil {
		t.Fatalf("ListMetrics: %v", err)
	}
	if on[0].Expression == "" {
		t.Fatal("expression missing with ExposeExpressions on")
	}
}

// A role with no policy in a model cannot use it at all, so the model is left
// out of the listing rather than shown and then refused.
func TestModelsOmitsModelsTheRoleCannotUse(t *testing.T) {
	svc := &Service{Store: NewStore()}
	m := governedModel()
	if _, err := governance.Visible(m.Governance, governance.Single("stranger")); err == nil {
		t.Fatal("unknown role should not resolve to a policy")
	}
	if _, err := svc.ListMetrics(m, governance.Single("stranger")); err == nil {
		t.Fatal("unknown role should be refused by ListMetrics")
	}
}

func governedBlob(t *testing.T, name string) []byte {
	t.Helper()
	b, err := json.Marshal(planner.CompiledModel{
		Name: name, Version: "v1", Namespace: "ns", Resource: name,
		Governance: &v1alpha1.GovernanceSpec{
			DefaultRole: "analyst",
			Roles:       []v1alpha1.RolePolicy{{Name: "analyst", AllowMetrics: []string{"*"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Resolve must not disclose model names a role cannot access. Before this, the
// "model name required" error listed every published model, so an authenticated
// caller with a zero-grant role could enumerate names, contradicting Models.
func TestResolveUnnamedHidesModelsFromUnauthorizedRole(t *testing.T) {
	store := NewStore()
	_ = store.Put("sm-a-compiled", governedBlob(t, "alpha"))
	_ = store.Put("sm-b-compiled", governedBlob(t, "beta"))
	svc := &Service{Store: store}

	_, err := svc.Resolve("", governance.Single("stranger"))
	if err == nil {
		t.Fatal("Resolve(\"\") for an unauthorized role: expected an error")
	}
	for _, name := range []string{"alpha", "beta"} {
		if strings.Contains(err.Error(), name) {
			t.Fatalf("error leaked model name %q to an unauthorized role: %v", name, err)
		}
	}

	_, err = svc.Resolve("", governance.Single("analyst"))
	if err == nil {
		t.Fatal("Resolve(\"\") for an authorized role: expected a model-name-required error")
	}
	for _, name := range []string{"alpha", "beta"} {
		if !strings.Contains(err.Error(), name) {
			t.Fatalf("authorized role should see model name %q, got: %v", name, err)
		}
	}
}
