package governance

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/KubedAI/semantic-operator/api/v1alpha1"
	"github.com/KubedAI/semantic-operator/internal/planner/expr"
)

func spec() *v1alpha1.GovernanceSpec {
	return &v1alpha1.GovernanceSpec{
		DefaultRole: "analyst",
		Roles: []v1alpha1.RolePolicy{
			{
				Name: "analyst", AllowMetrics: []string{"revenue"},
				DenyFields: []string{"store.manager_ssn"},
				RowFilters: []v1alpha1.RowFilter{{Dataset: "store", Predicate: "tenant_id = 'acme'"}},
			},
			{
				// Also allows revenue, so two roles can authorize the same
				// request and their row filters must be disjoined.
				Name: "regional", AllowMetrics: []string{"revenue"},
				DenyFields: []string{"store.manager_ssn"},
				RowFilters: []v1alpha1.RowFilter{{Dataset: "store", Predicate: "s_state = 'NY'"}},
			},
			{
				// Permits the sensitive field but cannot query revenue, so it
				// must not be able to complete analyst's denied request.
				Name: "finance", AllowMetrics: []string{"payroll_cost"},
				RowFilters: []v1alpha1.RowFilter{{Dataset: "store", Predicate: "dept = 'fin'"}},
			},
			// Grants nothing at all. Present in many real token group lists.
			{Name: "metadata_viewer"},
			{Name: "admin", AllowMetrics: []string{"*"}},
		},
	}
}

func refs(t *testing.T, names ...string) []expr.FieldRef {
	t.Helper()
	out := make([]expr.FieldRef, 0, len(names))
	for _, n := range names {
		ds, f, _ := strings.Cut(n, ".")
		out = append(out, expr.FieldRef{Dataset: ds, Field: f})
	}
	return out
}

// A role that grants nothing must not strip another role's row filter. Pooling
// permissions across roles used to let an empty role do exactly that, which
// removed tenant isolation from every query.
func TestEmptyRoleCannotRemoveRowFilters(t *testing.T) {
	solo, err := Authorize(spec(), Identity{Roles: []string{"analyst"}}, []string{"revenue"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(solo.RowFilterGroups) != 1 {
		t.Fatalf("analyst alone should carry its row filter, got %d groups", len(solo.RowFilterGroups))
	}
	both, err := Authorize(spec(), Identity{Roles: []string{"analyst", "metadata_viewer"}}, []string{"revenue"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(both.RowFilterGroups) != 1 {
		t.Fatalf("an empty role removed the row filter: %d groups", len(both.RowFilterGroups))
	}
}

// The same escalation through denyFields. A role that denies nothing must not
// cancel a deny another role declared.
func TestEmptyRoleCannotRemoveDenyFields(t *testing.T) {
	_, err := Authorize(spec(), Identity{Roles: []string{"analyst", "metadata_viewer"}},
		[]string{"revenue"}, refs(t, "store.manager_ssn"))
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("an empty role cancelled a denyFields entry, got %v", err)
	}
}

// Roles are not combined. finance may query payroll_cost but is not allowed to
// lend that permission to a request analyst could not make on its own.
func TestRolesCannotJointlyAuthorizeWhatNoneAllowsAlone(t *testing.T) {
	// analyst has the metric but denies the field. finance permits the field
	// but cannot query the metric. Neither covers the request, so pooling them
	// would be the only way to allow it.
	_, err := Authorize(spec(), Identity{Roles: []string{"analyst", "finance"}},
		[]string{"revenue"}, refs(t, "store.manager_ssn"))
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("roles were pooled to authorize a request neither allows alone, got %v", err)
	}
	// The same caller may still make a request finance covers on its own.
	if _, err := Authorize(spec(), Identity{Roles: []string{"analyst", "finance"}},
		[]string{"payroll_cost"}, refs(t, "store.manager_ssn")); err != nil {
		t.Fatalf("finance alone covers this request: %v", err)
	}
}

// Only roles that authorize the whole request contribute row filters, and when
// several do the predicates are disjoined.
func TestRowFiltersComeOnlyFromAuthorizingRoles(t *testing.T) {
	// Both analyst and regional allow revenue, so both contribute.
	d, err := Authorize(spec(), Identity{Roles: []string{"analyst", "regional"}}, []string{"revenue"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.RowFilterGroups) != 2 {
		t.Fatalf("both roles authorize revenue, want 2 groups, got %d", len(d.RowFilterGroups))
	}
	// Only finance allows payroll_cost, so analyst's filter must not appear.
	d, err = Authorize(spec(), Identity{Roles: []string{"analyst", "finance"}}, []string{"payroll_cost"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.RowFilterGroups) != 1 || d.RoleKey != "finance" {
		t.Fatalf("only the authorizing role may contribute filters, got %d groups for %q",
			len(d.RowFilterGroups), d.RoleKey)
	}
}

// A role that authorizes the request and carries no row filters really can see
// every row, so the disjunction is unrestricted. Safe only because that role
// covered the whole request by itself.
func TestAuthorizingRoleWithoutFiltersIsUnrestricted(t *testing.T) {
	d, err := Authorize(spec(), Identity{Roles: []string{"analyst", "admin"}}, []string{"revenue"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.RowFilterGroups) != 0 {
		t.Fatalf("admin authorizes and is unfiltered, want no row filtering, got %d", len(d.RowFilterGroups))
	}
}

// A group list is written for an organization, not for one model.
func TestUnknownRolesIgnoredButAllUnknownDenied(t *testing.T) {
	if _, err := Visible(spec(), Identity{Roles: []string{"analyst", "sre"}}); err != nil {
		t.Fatalf("unknown roles alongside a known one should be ignored: %v", err)
	}
	if _, err := Visible(spec(), Identity{Roles: []string{"sre", "oncall"}}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("an identity matching no policy must be denied, got %v", err)
	}
	// A role that exists but grants nothing is equally unusable.
	if _, err := Visible(spec(), Identity{Roles: []string{"metadata_viewer"}}); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("a role granting no metrics must not authenticate a caller, got %v", err)
	}
}

func TestRoleKeyIsOrderIndependent(t *testing.T) {
	a, _ := Visible(spec(), Identity{Roles: []string{"regional", "analyst"}})
	b, _ := Visible(spec(), Identity{Roles: []string{"analyst", "regional"}})
	if a.RoleKey() != b.RoleKey() {
		t.Fatalf("role key depends on order: %q vs %q", a.RoleKey(), b.RoleKey())
	}
}

func TestDefaultRoleAppliesWhenIdentityNamesNone(t *testing.T) {
	v, err := Visible(spec(), Identity{})
	if err != nil {
		t.Fatal(err)
	}
	if v.RoleKey() != "analyst" {
		t.Fatalf("want the default role, got %q", v.RoleKey())
	}
}

// Two callers holding the same role with different tenant claims compile to
// different SQL, so they must not share a cache key. Keying on roles alone let
// the second caller read a plan built for the first.
func TestIdentityKeySeparatesCallersByClaims(t *testing.T) {
	g := spec()
	acme := Identity{Roles: []string{"analyst"}, Claims: map[string]string{"tenant_id": "acme"}}
	globex := Identity{Roles: []string{"analyst"}, Claims: map[string]string{"tenant_id": "globex"}}

	ka, kb := IdentityKey(g, acme), IdentityKey(g, globex)
	if ka == kb {
		t.Fatalf("two tenants share a cache key: %q", ka)
	}
	if IdentityKey(g, acme) != ka {
		t.Fatal("identity key is not stable across calls")
	}
	// Claim values must never travel in the key, which reaches caches and logs.
	if strings.Contains(ka, "acme") || strings.Contains(kb, "globex") {
		t.Fatalf("raw claim value leaked into the key: %q %q", ka, kb)
	}
	// A caller with no claims still keys on roles alone.
	if got := IdentityKey(g, Identity{Roles: []string{"analyst"}}); got != "analyst" {
		t.Fatalf("claimless identity key = %q", got)
	}
}

// Distinct claim maps must not collide through naive concatenation.
func TestIdentityKeyResistsClaimAmbiguity(t *testing.T) {
	g := spec()
	a := Identity{Roles: []string{"analyst"}, Claims: map[string]string{"a": "bc"}}
	b := Identity{Roles: []string{"analyst"}, Claims: map[string]string{"ab": "c"}}
	if IdentityKey(g, a) == IdentityKey(g, b) {
		t.Fatal("claim maps with the same concatenation must not share a key")
	}
	// Map iteration order must not matter.
	m1 := Identity{Roles: []string{"analyst"}, Claims: map[string]string{"x": "1", "y": "2"}}
	m2 := Identity{Roles: []string{"analyst"}, Claims: map[string]string{"y": "2", "x": "1"}}
	if IdentityKey(g, m1) != IdentityKey(g, m2) {
		t.Fatal("identity key depends on map ordering")
	}
}

// A predicate carrying a claim placeholder has to survive publication.
func TestValidatablePredicateAcceptsPlaceholders(t *testing.T) {
	got, err := ValidatablePredicate("tenant_id = {{claim.tenant_id}}")
	if err != nil {
		t.Fatal(err)
	}
	if _, perr := expr.ParsePredicate(got); perr != nil {
		t.Fatalf("substituted predicate must parse, got %v for %q", perr, got)
	}
	plain, err := ValidatablePredicate("s_state = 'TX'")
	if err != nil || plain != "s_state = 'TX'" {
		t.Fatalf("plain predicate changed: %q %v", plain, err)
	}
}

// Stray brace syntax would otherwise reach the engine inside a WHERE clause.
func TestValidatablePredicateRejectsMalformedTemplates(t *testing.T) {
	for _, bad := range []string{
		"tenant_id = {{claim tenant_id}}",
		"tenant_id = {{ claim.tenant_id",
		"tenant_id = }}",
		"tenant_id = {{tenant_id}}",
	} {
		if _, err := ValidatablePredicate(bad); err == nil {
			t.Fatalf("malformed template accepted: %q", bad)
		}
	}
}

func TestExpandClaimsEscapesValues(t *testing.T) {
	lit := func(v any) (string, error) { return "'" + strings.ReplaceAll(fmt.Sprint(v), "'", "''") + "'", nil }
	got, err := ExpandClaims("region = {{claim.region}}", map[string]string{"region": "x' OR 1=1 --"}, lit)
	if err != nil {
		t.Fatal(err)
	}
	if got != "region = 'x'' OR 1=1 --'" {
		t.Fatalf("claim value was not escaped: %q", got)
	}
}

// A missing claim must fail the request. An empty substitution leaves a filter
// that silently matches the wrong rows.
func TestExpandClaimsFailsClosedOnMissingClaim(t *testing.T) {
	lit := func(v any) (string, error) { return fmt.Sprintf("'%v'", v), nil }
	if _, err := ExpandClaims("region = {{claim.region}}", map[string]string{}, lit); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized for a missing claim, got %v", err)
	}
}

// The claim digest is the other half of the plan cache identity, so it must
// not be truncated either.
func TestIdentityKeyDigestIsFullWidth(t *testing.T) {
	k := IdentityKey(spec(), Identity{Roles: []string{"analyst"}, Claims: map[string]string{"tenant_id": "acme"}})
	_, digest, ok := strings.Cut(k, "|")
	if !ok {
		t.Fatalf("expected a digest in %q", k)
	}
	if len(digest) != 64 {
		t.Fatalf("claim digest is %d hex chars (%d bits), want 64 (256 bits)", len(digest), len(digest)*4)
	}
}
