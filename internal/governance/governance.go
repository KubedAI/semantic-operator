// Package governance enforces row, column, and metric policies at plan
// compile time. The planner calls Authorize before building any SQL; a
// violation is a compile error (ErrUnauthorized), never a filtered result.
package governance

import (
	"errors"
	"fmt"

	"github.com/gobwas/glob"

	"github.com/KubedAI/semantic-operator/api/v1alpha1"
	"github.com/KubedAI/semantic-operator/internal/planner/expr"
)

// ErrUnauthorized marks policy violations. Adapters map it to 403.
var ErrUnauthorized = errors.New("unauthorized")

// Identity is who is asking. Adapters pass it explicitly; the planner never
// infers it.
type Identity struct {
	Role   string            `json:"role,omitempty"`
	Claims map[string]string `json:"claims,omitempty"`
}

// Decision is the successful outcome of authorization: the row filters to
// conjoin into the plan's WHERE clause.
type Decision struct {
	Role       string
	RowFilters []v1alpha1.RowFilter
}

// Authorize checks the requested metrics and every field reference (dimensions,
// filters, and fields inside metric expressions) against the role's policy.
//
// Semantics:
//   - No governance spec: everything is allowed, no row filters.
//   - Governance spec present: the effective role is id.Role or defaultRole.
//     An unknown or empty effective role is denied outright.
//   - allowMetrics is a glob allowlist; a metric outside it is denied.
//   - denyFields deny reads through any path, including inside metric
//     expressions.
func Authorize(g *v1alpha1.GovernanceSpec, id Identity, metrics []string, fieldRefs []expr.FieldRef) (Decision, error) {
	if g == nil {
		return Decision{Role: id.Role}, nil
	}
	role := id.Role
	if role == "" {
		role = g.DefaultRole
	}
	policy := g.Role(role)
	if policy == nil {
		return Decision{}, fmt.Errorf("%w: no policy for role %q", ErrUnauthorized, role)
	}

	var metricGlobs []glob.Glob
	for _, pat := range policy.AllowMetrics {
		gl, err := glob.Compile(pat)
		if err != nil {
			return Decision{}, fmt.Errorf("role %q: bad allowMetrics pattern %q: %v", role, pat, err)
		}
		metricGlobs = append(metricGlobs, gl)
	}
	for _, m := range metrics {
		allowed := false
		for _, gl := range metricGlobs {
			if gl.Match(m) {
				allowed = true
				break
			}
		}
		if !allowed {
			return Decision{}, fmt.Errorf("%w: role %q may not query metric %q", ErrUnauthorized, role, m)
		}
	}

	denied := map[string]bool{}
	for _, df := range policy.DenyFields {
		denied[df] = true
	}
	for _, ref := range fieldRefs {
		if denied[ref.String()] {
			return Decision{}, fmt.Errorf("%w: role %q may not read field %q", ErrUnauthorized, role, ref.String())
		}
	}

	return Decision{Role: role, RowFilters: policy.RowFilters}, nil
}
