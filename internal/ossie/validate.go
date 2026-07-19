// Package ossie validates SemanticModel specs against the Ossie structure and
// the planner's supported subset. Validation is pure: no I/O, so the same
// spec always validates the same way (physical drift is checked separately
// by the controller against live StarRocks).
package ossie

import (
	"errors"
	"fmt"
	"strings"

	"github.com/KubedAI/semantic-operator/api/v1alpha1"
	"github.com/KubedAI/semantic-operator/internal/planner/expr"
)

// ValidateSpec checks the whole spec. It returns an aggregate error listing
// every problem found, or nil.
func ValidateSpec(spec *v1alpha1.SemanticModelSpec) error {
	var errs []error
	add := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	m := &spec.Ossie
	if m.Name == "" {
		add("ossie.name is required")
	}
	if spec.Connection.Catalog == "" || spec.Connection.Database == "" {
		add("connection.catalog and connection.database are required")
	}
	if len(m.Datasets) == 0 {
		add("ossie.datasets must not be empty")
	}

	datasets := map[string]*v1alpha1.Dataset{}
	for i := range m.Datasets {
		d := &m.Datasets[i]
		if d.Name == "" {
			add("dataset[%d]: name is required", i)
			continue
		}
		if datasets[d.Name] != nil {
			add("dataset %q: duplicate name", d.Name)
			continue
		}
		datasets[d.Name] = d
		if d.Source == "" {
			add("dataset %q: source is required", d.Name)
		}
		fields := map[string]bool{}
		for j := range d.Fields {
			f := &d.Fields[j]
			if f.Name == "" {
				add("dataset %q field[%d]: name is required", d.Name, j)
				continue
			}
			if fields[f.Name] {
				add("dataset %q field %q: duplicate name", d.Name, f.Name)
			}
			fields[f.Name] = true
			if _, ok := f.Expression.Select(); !ok {
				add("dataset %q field %q: no ANSI_SQL or STARROCKS dialect expression", d.Name, f.Name)
			}
		}
	}

	// Relationships: referential integrity and acyclic join graph.
	relNames := map[string]bool{}
	edges := map[string][]string{} // from -> to
	for i := range m.Relationships {
		r := &m.Relationships[i]
		if r.Name == "" {
			add("relationship[%d]: name is required", i)
		} else if relNames[r.Name] {
			add("relationship %q: duplicate name", r.Name)
		}
		relNames[r.Name] = true
		if datasets[r.From] == nil {
			add("relationship %q: from dataset %q does not exist", r.Name, r.From)
		}
		if datasets[r.To] == nil {
			add("relationship %q: to dataset %q does not exist", r.Name, r.To)
		}
		if len(r.FromColumns) == 0 || len(r.FromColumns) != len(r.ToColumns) {
			add("relationship %q: from_columns and to_columns must be non-empty and the same length", r.Name)
		}
		edges[r.From] = append(edges[r.From], r.To)
	}
	if cyc := findCycle(edges); cyc != "" {
		add("relationships form a cycle through %s; the join graph must be a tree (star/snowflake)", cyc)
	}

	// Metrics: parse under the bounded grammar, resolve refs.
	metricNames := map[string]bool{}
	for i := range m.Metrics {
		mt := &m.Metrics[i]
		if mt.Name == "" {
			add("metric[%d]: name is required", i)
			continue
		}
		if metricNames[mt.Name] {
			add("metric %q: duplicate name", mt.Name)
		}
		metricNames[mt.Name] = true
		body, ok := mt.Expression.Select()
		if !ok {
			add("metric %q: no ANSI_SQL or STARROCKS dialect expression", mt.Name)
			continue
		}
		parsed, err := expr.Parse(body)
		if err != nil {
			add("metric %q: %v", mt.Name, err)
			continue
		}
		for _, ref := range collectRefs(parsed) {
			ds := datasets[ref.Dataset]
			if ds == nil {
				add("metric %q: references unknown dataset %q", mt.Name, ref.Dataset)
				continue
			}
			if len(ds.Fields) > 0 && ds.FindField(ref.Field) == nil {
				add("metric %q: references unknown field %q on dataset %q", mt.Name, ref.Field, ref.Dataset)
			}
		}
		// A non-fan-out-safe ratio side aggregating a non-root dataset needs
		// that dataset's primary key to deduplicate; require it up front.
		if parsed.Denominator != nil && !parsed.Denominator.Distinct {
			for _, ds := range termDatasets(*parsed.Denominator) {
				if d := datasets[ds]; d != nil && len(d.PrimaryKey) == 0 {
					add("metric %q: ratio denominator aggregates dataset %q which has no primary_key (needed for fan-out-safe planning)", mt.Name, ds)
				}
			}
		}
	}

	// Governance.
	if g := spec.Governance; g != nil {
		roleNames := map[string]bool{}
		for i := range g.Roles {
			r := &g.Roles[i]
			if r.Name == "" {
				add("governance.roles[%d]: name is required", i)
				continue
			}
			if roleNames[r.Name] {
				add("governance role %q: duplicate name", r.Name)
			}
			roleNames[r.Name] = true
			for _, df := range r.DenyFields {
				ds, _, ok := splitRef(df)
				if !ok || datasets[ds] == nil {
					add("governance role %q: denyFields entry %q must be <existing dataset>.<field>", r.Name, df)
				}
			}
			for _, rf := range r.RowFilters {
				if datasets[rf.Dataset] == nil {
					add("governance role %q: rowFilter dataset %q does not exist", r.Name, rf.Dataset)
				}
				if strings.TrimSpace(rf.Predicate) == "" {
					add("governance role %q: rowFilter on %q has empty predicate", r.Name, rf.Dataset)
				} else if _, perr := expr.ParsePredicate(rf.Predicate); perr != nil {
					add("governance role %q: rowFilter on %q: %v", r.Name, rf.Dataset, perr)
				}
			}
		}
		if g.DefaultRole != "" && !roleNames[g.DefaultRole] {
			add("governance.defaultRole %q is not a defined role", g.DefaultRole)
		}
	}

	// Views.
	viewNames := map[string]bool{}
	for i := range spec.Views {
		v := &spec.Views[i]
		if v.Name == "" {
			add("views[%d]: name is required", i)
			continue
		}
		if viewNames[v.Name] {
			add("view %q: duplicate name", v.Name)
		}
		viewNames[v.Name] = true
		if len(v.Metrics) == 0 {
			add("view %q: at least one metric is required", v.Name)
		}
		for _, mn := range v.Metrics {
			if !metricNames[mn] {
				add("view %q: metric %q does not exist", v.Name, mn)
			}
		}
		for _, dim := range v.Dimensions {
			ds, f, ok := splitRef(dim)
			if !ok || datasets[ds] == nil {
				add("view %q: dimension %q must be <existing dataset>.<field>", v.Name, dim)
				continue
			}
			if d := datasets[ds]; len(d.Fields) > 0 && d.FindField(f) == nil {
				add("view %q: dimension %q not found on dataset %q", v.Name, f, ds)
			}
		}
		if v.Role != "" && spec.Governance.Role(v.Role) == nil {
			add("view %q: role %q is not defined in governance", v.Name, v.Role)
		}
	}

	// Join-type overrides: each must name an existing relationship, at most
	// once. A duplicate would otherwise be resolved last-write-wins at compile
	// time, letting a typo or merge artifact silently change join semantics.
	joinSeen := map[string]bool{}
	for i := range spec.Joins {
		j := &spec.Joins[i]
		if !relNames[j.Relationship] {
			add("joins[%d]: relationship %q does not exist", i, j.Relationship)
		}
		if joinSeen[j.Relationship] {
			add("joins[%d]: duplicate override for relationship %q", i, j.Relationship)
		}
		joinSeen[j.Relationship] = true
		if j.Type != "INNER" && j.Type != "LEFT" {
			add("joins[%d]: type %q must be INNER or LEFT", i, j.Type)
		}
	}

	return errors.Join(errs...)
}

func collectRefs(m expr.MetricExpr) []expr.FieldRef {
	refs := append([]expr.FieldRef{}, m.Numerator.Refs...)
	if m.Denominator != nil {
		refs = append(refs, m.Denominator.Refs...)
	}
	return refs
}

func termDatasets(t expr.AggTerm) []string {
	seen := map[string]bool{}
	var out []string
	for _, r := range t.Refs {
		if !seen[r.Dataset] {
			seen[r.Dataset] = true
			out = append(out, r.Dataset)
		}
	}
	return out
}

func splitRef(s string) (dataset, field string, ok bool) {
	i := strings.IndexByte(s, '.')
	if i <= 0 || i >= len(s)-1 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

// findCycle returns a description of one cycle in the directed graph, or "".
func findCycle(edges map[string][]string) string {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := map[string]int{}
	var path []string
	var dfs func(n string) string
	dfs = func(n string) string {
		color[n] = gray
		path = append(path, n)
		for _, next := range edges[n] {
			switch color[next] {
			case gray:
				return strings.Join(append(path, next), " -> ")
			case white:
				if c := dfs(next); c != "" {
					return c
				}
			}
		}
		color[n] = black
		path = path[:len(path)-1]
		return ""
	}
	for n := range edges {
		if color[n] == white {
			if c := dfs(n); c != "" {
				return c
			}
		}
	}
	return ""
}
