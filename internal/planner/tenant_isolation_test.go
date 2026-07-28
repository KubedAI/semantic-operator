package planner

import (
	"strings"
	"testing"

	"github.com/KubedAI/semantic-operator/api/v1alpha1"
	"github.com/KubedAI/semantic-operator/internal/governance"
)

// tenantModel is the shared model with a role whose row filter interpolates a
// claim, which is the shape that made the plan cache unsafe.
func tenantModel(t *testing.T) *CompiledModel {
	t.Helper()
	cm := compiled(t)
	cm.Governance = &v1alpha1.GovernanceSpec{
		Roles: []v1alpha1.RolePolicy{
			{
				Name: "analyst", AllowMetrics: []string{"*"},
				RowFilters: []v1alpha1.RowFilter{
					{Dataset: "store", Predicate: "s_state = {{claim.tenant_id}}"},
				},
			},
		},
	}
	return cm
}

func tenant(id string) governance.Identity {
	return governance.Identity{Roles: []string{"analyst"}, Claims: map[string]string{"tenant_id": id}}
}

// Two callers holding the same role and different tenant claims must compile
// to different SQL and, critically, to different request hashes. The hash is
// the plan cache key, so an equal hash means the second caller executes SQL
// built for the first, against the first caller's rows.
func TestTenantsDoNotShareAPlanCacheKey(t *testing.T) {
	cm := tenantModel(t)
	req := Request{Metrics: []string{"total_sales"}}

	acme, err := Build(cm, testDialect(t), req, tenant("acme"))
	if err != nil {
		t.Fatal(err)
	}
	globex, err := Build(cm, testDialect(t), req, tenant("globex"))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(acme.SQL, "'acme'") || !strings.Contains(globex.SQL, "'globex'") {
		t.Fatalf("claim was not interpolated:\n%s\n---\n%s", acme.SQL, globex.SQL)
	}
	if acme.SQL == globex.SQL {
		t.Fatal("two tenants compiled to identical SQL")
	}
	if acme.RequestHash == globex.RequestHash {
		t.Fatalf("two tenants share a plan cache key %q, so one can read the other's rows",
			acme.RequestHash)
	}
	// The hash must not carry the tenant value in clear, since it is logged
	// and used as a cache key.
	if strings.Contains(acme.RequestHash, "acme") {
		t.Fatalf("claim value leaked into the request hash: %q", acme.RequestHash)
	}
}

// The same caller must still be stable, or nothing would ever cache.
func TestSameTenantIsDeterministic(t *testing.T) {
	cm := tenantModel(t)
	req := Request{Metrics: []string{"total_sales"}}
	a, err := Build(cm, testDialect(t), req, tenant("acme"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Build(cm, testDialect(t), req, tenant("acme"))
	if err != nil {
		t.Fatal(err)
	}
	if a.SQL != b.SQL || a.RequestHash != b.RequestHash {
		t.Fatal("the same identity must compile to the same plan and hash")
	}
}

// A caller missing the claim the filter needs is refused rather than served a
// filter that silently matches the wrong rows.
func TestMissingTenantClaimIsRefused(t *testing.T) {
	cm := tenantModel(t)
	_, err := Build(cm, testDialect(t), Request{Metrics: []string{"total_sales"}},
		governance.Single("analyst"))
	if err == nil {
		t.Fatal("a row filter needing an absent claim must not compile")
	}
}

// Tenant isolation rests on this value being unique, so its width is asserted
// rather than left to whatever a future edit truncates it to.
func TestRequestHashIsWideEnoughToRelyOn(t *testing.T) {
	cm := tenantModel(t)
	p, err := Build(cm, testDialect(t), Request{Metrics: []string{"total_sales"}}, tenant("acme"))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.RequestHash) != RequestHashLen {
		t.Fatalf("request hash is %d hex chars (%d bits), want %d (%d bits)",
			len(p.RequestHash), len(p.RequestHash)*4, RequestHashLen, RequestHashLen*4)
	}
}
