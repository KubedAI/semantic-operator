// Package governance enforces row, column, and metric policies at plan
// compile time. The planner calls Authorize before building any SQL; a
// violation is a compile error (ErrUnauthorized), never a filtered result.
package governance

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/gobwas/glob"

	"github.com/KubedAI/semantic-operator/api/v1alpha1"
	"github.com/KubedAI/semantic-operator/internal/planner/expr"
)

// ErrUnauthorized marks policy violations. Adapters map it to 403.
var ErrUnauthorized = errors.New("unauthorized")

// Identity is who is asking. Adapters pass it explicitly; the planner never
// infers it.
//
// Roles is a set, because a token normally carries group membership rather
// than one label. Roles are evaluated one at a time and never pooled, which is
// spelled out on Visible.
type Identity struct {
	Principal string            `json:"principal,omitempty"`
	Groups    []string          `json:"groups,omitempty"`
	Roles     []string          `json:"roles,omitempty"`
	Claims    map[string]string `json:"claims,omitempty"`
}

// Single builds an identity holding one application role, which is what
// header mode and the view materializer produce when no principal is needed.
func Single(role string) Identity {
	if role == "" {
		return Identity{}
	}
	return Identity{Roles: []string{role}}
}

// PolicyRoles returns the deduplicated union used by built-in governance.
// External providers receive Groups and Roles separately and can preserve
// their distinct directory and application semantics.
func (id Identity) PolicyRoles() []string {
	seen := make(map[string]struct{}, len(id.Groups)+len(id.Roles))
	roles := make([]string, 0, len(id.Groups)+len(id.Roles))
	for _, values := range [][]string{id.Groups, id.Roles} {
		for _, value := range values {
			if value == "" {
				continue
			}
			if _, exists := seen[value]; exists {
				continue
			}
			seen[value] = struct{}{}
			roles = append(roles, value)
		}
	}
	return roles
}

// Decision is the successful outcome of authorization.
type Decision struct {
	// Roles are the policy names that matched, sorted.
	Roles []string
	// RoleKey is a deterministic rendering of Roles, used for provenance in
	// the response. It is not sufficient as a cache key on its own, because a
	// row filter may also depend on a claim. Use IdentityKey for caching.
	RoleKey string
	// RowFilterGroups holds one group per role that authorized the whole
	// request. Filters within a group are conjoined and the groups are
	// disjoined. Empty means no row restriction.
	RowFilterGroups [][]v1alpha1.RowFilter
}

// Visibility is what one identity is permitted to see in a model.
//
// Discovery filters through it so a caller is never shown the name of a metric
// or column that a query would refuse. Authorize is built on the same value,
// so listing and enforcement read one policy through one code path.
//
// Roles do not pool their permissions. Each role is evaluated whole, and a
// request must be authorized by some single role on its own. Pooling looks
// natural, because Kubernetes RBAC is additive, but it is unsound here: a
// policy carries restrictions as well as grants, and unioning restrictions
// weakens them. A role that grants nothing would otherwise cancel another
// role's denyFields and row filters just by being present.
type Visibility struct {
	roles []string
	// unrestricted is set when the model carries no governance spec at all.
	unrestricted bool
	policies     []*v1alpha1.RolePolicy
	// globs is parallel to policies. Kept per policy rather than merged,
	// because merging is what allows one role to complete another's
	// half-granted permission.
	globs [][]glob.Glob
}

// Visible resolves the caller's roles to policies and compiles them.
//
// Semantics:
//   - No governance spec: everything is visible, no row filters.
//   - Governance spec present: the effective policy roles are the union of
//     id.Groups and id.Roles, or defaultRole when the identity names none.
//   - Roles with no policy in this model are ignored, because a token's group
//     list is not written for one model. If none of them match, the caller is
//     denied outright.
//   - A policy with an empty allowMetrics grants nothing, so it is dropped
//     entirely. Keeping it would let a role that cannot run any query widen
//     the set of columns a caller may see.
func Visible(g *v1alpha1.GovernanceSpec, id Identity) (Visibility, error) {
	policyRoles := id.PolicyRoles()
	if g == nil {
		return Visibility{roles: policyRoles, unrestricted: true}, nil
	}

	wanted := policyRoles
	if len(wanted) == 0 && g.DefaultRole != "" {
		wanted = []string{g.DefaultRole}
	}

	v := Visibility{}
	seen := map[string]bool{}
	for _, name := range wanted {
		if name == "" || seen[name] {
			continue
		}
		policy := g.Role(name)
		if policy == nil || len(policy.AllowMetrics) == 0 {
			continue
		}
		seen[name] = true

		var globs []glob.Glob
		for _, pat := range policy.AllowMetrics {
			gl, err := glob.Compile(pat)
			if err != nil {
				return Visibility{}, fmt.Errorf("role %q: bad allowMetrics pattern %q: %v", name, pat, err)
			}
			globs = append(globs, gl)
		}
		v.roles = append(v.roles, name)
		v.policies = append(v.policies, policy)
		v.globs = append(v.globs, globs)
	}

	if len(v.policies) == 0 {
		return Visibility{}, fmt.Errorf("%w: no policy grants anything for role(s) %s",
			ErrUnauthorized, quoteList(wanted))
	}
	sortPolicies(&v)
	return v, nil
}

// sortPolicies orders roles by name and keeps the parallel slices aligned, so
// the cache key and any emitted SQL are independent of claim ordering.
func sortPolicies(v *Visibility) {
	idx := make([]int, len(v.roles))
	for i := range idx {
		idx[i] = i
	}
	sort.Slice(idx, func(a, b int) bool { return v.roles[idx[a]] < v.roles[idx[b]] })
	roles := make([]string, len(idx))
	policies := make([]*v1alpha1.RolePolicy, len(idx))
	globs := make([][]glob.Glob, len(idx))
	for i, j := range idx {
		roles[i], policies[i], globs[i] = v.roles[j], v.policies[j], v.globs[j]
	}
	v.roles, v.policies, v.globs = roles, policies, globs
}

// Roles are the matched policy names, sorted.
func (v Visibility) Roles() []string { return v.roles }

// RoleKey is the deterministic rendering of this role set.
func (v Visibility) RoleKey() string { return strings.Join(v.roles, ",") }

// permitsMetric reports whether policy i allows the metric.
func (v Visibility) permitsMetric(i int, name string) bool {
	for _, gl := range v.globs[i] {
		if gl.Match(name) {
			return true
		}
	}
	return false
}

// permitsField reports whether policy i may read the reference.
func (v Visibility) permitsField(i int, ref string) bool {
	for _, df := range v.policies[i].DenyFields {
		if df == ref {
			return false
		}
	}
	return true
}

// Metric reports whether some role may query the named metric. Discovery only.
// A listing says an item is reachable through at least one role, not that any
// combination of items is queryable together, which Authorize decides.
func (v Visibility) Metric(name string) bool {
	if v.unrestricted {
		return true
	}
	for i := range v.policies {
		if v.permitsMetric(i, name) {
			return true
		}
	}
	return false
}

// Field reports whether some role may read the "dataset.field" reference.
// Only roles that grant at least one metric are considered, because a role
// that can run no query must not enlarge the visible column set.
func (v Visibility) Field(ref string) bool {
	if v.unrestricted {
		return true
	}
	for i := range v.policies {
		if v.permitsField(i, ref) {
			return true
		}
	}
	return false
}

// authorizing returns the indexes of roles that permit the entire request on
// their own.
func (v Visibility) authorizing(metrics []string, fieldRefs []expr.FieldRef) []int {
	var out []int
	for i := range v.policies {
		ok := true
		for _, m := range metrics {
			if !v.permitsMetric(i, m) {
				ok = false
				break
			}
		}
		if ok {
			for _, ref := range fieldRefs {
				if !v.permitsField(i, ref.String()) {
					ok = false
					break
				}
			}
		}
		if ok {
			out = append(out, i)
		}
	}
	return out
}

// Authorize checks the requested metrics and every field reference (dimensions,
// filters, and fields inside metric expressions) against the caller's policies.
// A violation is a compile error, never a silently filtered result.
//
// Some single role must permit the whole request. Row filters then come only
// from roles that did, disjoined, so a caller sees the union of the rows those
// roles allow and nothing more.
func Authorize(g *v1alpha1.GovernanceSpec, id Identity, metrics []string, fieldRefs []expr.FieldRef) (Decision, error) {
	v, err := Visible(g, id)
	if err != nil {
		return Decision{}, err
	}
	if v.unrestricted {
		return Decision{Roles: v.roles, RoleKey: v.RoleKey()}, nil
	}

	granted := v.authorizing(metrics, fieldRefs)
	if len(granted) == 0 {
		return Decision{}, v.refusal(metrics, fieldRefs)
	}

	// A role that authorizes the request and carries no row filters really can
	// see every row, so the disjunction is unrestricted. This is only safe
	// because that role authorized the whole request on its own.
	var groups [][]v1alpha1.RowFilter
	for _, i := range granted {
		if len(v.policies[i].RowFilters) == 0 {
			groups = nil
			break
		}
		groups = append(groups, v.policies[i].RowFilters)
	}

	var names []string
	for _, i := range granted {
		names = append(names, v.roles[i])
	}
	return Decision{Roles: names, RoleKey: strings.Join(names, ","), RowFilterGroups: groups}, nil
}

// blocker returns the first metric or field that stops policy i authorizing the
// request, so the caller is told what to change rather than only that they were
// refused.
func (v Visibility) blocker(i int, metrics []string, fieldRefs []expr.FieldRef) string {
	for _, m := range metrics {
		if !v.permitsMetric(i, m) {
			return fmt.Sprintf("may not query metric %q", m)
		}
	}
	for _, ref := range fieldRefs {
		if !v.permitsField(i, ref.String()) {
			return fmt.Sprintf("may not read field %q", ref.String())
		}
	}
	return "does not permit this request"
}

// refusal explains why nothing authorized the request.
//
// With one role the answer is simply what that role forbids. With several it
// also has to say that roles are not pooled, because a caller looking at their
// combined permissions would otherwise expect this to work.
func (v Visibility) refusal(metrics []string, fieldRefs []expr.FieldRef) error {
	if len(v.policies) == 1 {
		return fmt.Errorf("%w: role %q %s", ErrUnauthorized, v.roles[0], v.blocker(0, metrics, fieldRefs))
	}
	reasons := make([]string, 0, len(v.policies))
	for i := range v.policies {
		reasons = append(reasons, fmt.Sprintf("%q %s", v.roles[i], v.blocker(i, metrics, fieldRefs)))
	}
	return fmt.Errorf("%w: no single role permits this request. %s. "+
		"Roles are not combined, so one role must allow every requested metric and field",
		ErrUnauthorized, strings.Join(reasons, ", "))
}

// claimRef matches a claim placeholder such as {{claim.region}} in a row
// filter predicate. Deliberately not a general template language: a predicate
// is SQL that reaches the engine, so the only substitution allowed is a single
// claim rendered as a literal.
var claimRef = regexp.MustCompile(`\{\{\s*claim\.([A-Za-z0-9_.-]+)\s*\}\}`)

// ExpandClaims substitutes claim placeholders in a row-filter predicate.
//
// The value is rendered through the dialect's literal function, so a claim is
// always a properly escaped SQL literal and can never inject syntax, even
// though claims already arrive from a signed token.
//
// A placeholder naming a claim the caller does not carry is an error. The
// alternative, substituting an empty string, turns a row filter into one that
// silently matches nothing or, worse, matches everything depending on the
// predicate. Failing closed is the only safe reading.
func ExpandClaims(predicate string, claims map[string]string, literal func(any) (string, error)) (string, error) {
	var bad error
	out := claimRef.ReplaceAllStringFunc(predicate, func(m string) string {
		name := claimRef.FindStringSubmatch(m)[1]
		v, ok := claims[name]
		if !ok {
			if bad == nil {
				bad = fmt.Errorf("%w: row filter needs claim %q, which the caller does not carry",
					ErrUnauthorized, name)
			}
			return m
		}
		lit, err := literal(v)
		if err != nil {
			if bad == nil {
				bad = fmt.Errorf("row filter claim %q: %v", name, err)
			}
			return m
		}
		return lit
	})
	if bad != nil {
		return "", bad
	}
	return out, nil
}

// IdentityKey is the cache and provenance key for one caller against one
// model. It combines the resolved built-in policy role set with a digest of
// the complete authenticated identity.
//
// Principal, group, role, and claim values are hashed, never included in
// clear. The key travels into cache keys and logs, and identity data must not
// be scattered through either. Sorting and length prefixes make the digest
// independent of token claim order and unambiguous across field boundaries.
//
// Both the planner and the serving cache call this, so the hash they use
// cannot drift apart.
func IdentityKey(g *v1alpha1.GovernanceSpec, id Identity) string {
	roleKey := strings.Join(sortedUniqueCopy(id.PolicyRoles()), ",")
	if v, err := Visible(g, id); err == nil {
		roleKey = v.RoleKey()
	}
	return roleKey + "|" + IdentityDigest(id)
}

// IdentityDigest returns a deterministic full-width digest of all identity
// fields. Groups and roles are separate namespaces even when built-in
// governance treats their union as policy labels.
func IdentityDigest(id Identity) string {
	h := sha256.New()
	writeIdentityValue := func(kind, value string) {
		// hash.Hash never returns an error, so the write cannot fail.
		_, _ = fmt.Fprintf(h, "%d:%s=%d:%s;", len(kind), kind, len(value), value)
	}
	writeIdentityValue("principal", id.Principal)
	for _, group := range sortedUniqueCopy(id.Groups) {
		writeIdentityValue("group", group)
	}
	for _, role := range sortedUniqueCopy(id.Roles) {
		writeIdentityValue("role", role)
	}
	names := make([]string, 0, len(id.Claims))
	for name := range id.Claims {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		writeIdentityValue("claim-name", name)
		writeIdentityValue("claim-value", id.Claims[name])
	}
	return hex.EncodeToString(h.Sum(nil))
}

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func sortedUniqueCopy(in []string) []string {
	out := sortedCopy(in)
	if len(out) < 2 {
		return out
	}
	write := 1
	for read := 1; read < len(out); read++ {
		if out[read] == out[write-1] {
			continue
		}
		out[write] = out[read]
		write++
	}
	return out[:write]
}

// placeholderLiteral stands in for a claim while a predicate is being checked
// for syntax. A string literal, because ExpandClaims also substitutes a string
// literal at runtime, so validation exercises the same shape the engine sees.
const placeholderLiteral = "'x'"

// ValidatablePredicate returns the predicate with claim placeholders replaced
// by a harmless literal, ready for the predicate grammar.
//
// The grammar accepts SQL literals and nothing else, so a predicate written as
// "tenant_id = {{claim.tenant_id}}" fails to parse and the model is rejected
// at publish time. Validation and drift checking both go through here so a
// claim-based filter is checked rather than refused.
//
// Leftover brace syntax is an error. A typo such as {{claim tenant}} or a bare
// {{ would otherwise reach the engine verbatim inside a WHERE clause.
func ValidatablePredicate(pred string) (string, error) {
	out := claimRef.ReplaceAllString(pred, placeholderLiteral)
	if strings.Contains(out, "{{") || strings.Contains(out, "}}") {
		return "", fmt.Errorf("malformed template syntax in predicate %q, "+
			"the only form supported is {{claim.NAME}}", pred)
	}
	return out, nil
}

func quoteList(names []string) string {
	if len(names) == 0 {
		return `""`
	}
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = fmt.Sprintf("%q", n)
	}
	return strings.Join(q, ", ")
}
