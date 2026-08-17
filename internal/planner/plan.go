package planner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/KubedAI/semantic-operator/api/v1alpha1"
	"github.com/KubedAI/semantic-operator/internal/emitter"
	"github.com/KubedAI/semantic-operator/internal/governance"
	"github.com/KubedAI/semantic-operator/internal/planner/expr"
)

// Request is a semantic query: metrics by dimensions, filtered, ordered, at a grain.
type Request struct {
	Model      string   `json:"model"`
	Metrics    []string `json:"metrics"`
	Dimensions []string `json:"dimensions,omitempty"`
	Filters    []Filter `json:"filters,omitempty"`
	// TimeGrain is one of day, week, month, quarter, year. It truncates the
	// first requested time dimension (dimension.is_time).
	TimeGrain string          `json:"timeGrain,omitempty"`
	OrderBy   []OrderByClause `json:"orderBy,omitempty"`
	Limit     int             `json:"limit,omitempty"`
}

// OrderByClause orders by one requested output field. Field is a certified
// metric name or dataset.field dimension, never a SQL expression.
type OrderByClause struct {
	Field     string `json:"field"`
	Direction string `json:"direction"` // asc or desc
}

// Filter restricts rows before aggregation.
type Filter struct {
	Field  string `json:"field"` // dataset.field
	Op     string `json:"op"`    // = != < <= > >= IN NOT IN LIKE BETWEEN
	Value  any    `json:"value,omitempty"`
	Values []any  `json:"values,omitempty"` // for IN / NOT IN / BETWEEN
}

// Plan is the compiled output: exactly one SQL statement plus provenance.
type Plan struct {
	SQL                      string   `json:"sql"`
	Model                    string   `json:"model"`
	ModelVersion             string   `json:"modelVersion"`
	RequestHash              string   `json:"requestHash"`
	Role                     string   `json:"role,omitempty"`
	AuthorizationFingerprint string   `json:"authorizationFingerprint,omitempty"`
	Columns                  []string `json:"columns"`
}

var validGrains = map[string]bool{"day": true, "week": true, "month": true, "quarter": true, "year": true}

// RequestHashLen is the hex width of a request hash, 128 bits.
//
// The hash is the plan cache key, so two identities colliding here means one
// caller is served SQL compiled for another. 128 bits keeps that out of reach,
// including for someone deliberately searching for a collision, and costs 16
// more characters in a log line.
const RequestHashLen = 32

// RequestHash identifies a compiled plan. The same request, identity, and
// model version always produce the same hash, which is what makes a plan
// cacheable.
//
// identityKey must come from governance.IdentityKey. It covers the role set
// and a digest of every claim, because a row filter may interpolate a claim
// into the SQL, and two callers whose SQL differs must never share a key.
func RequestHash(req Request, identityKey string) string {
	b, _ := json.Marshal(struct {
		Request
		Identity string `json:"identity"`
	}{req, identityKey})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])[:RequestHashLen]
}

// dimSpec is a resolved requested dimension.
type dimSpec struct {
	Ref     expr.FieldRef
	Field   *CompiledField
	Grained bool // TimeGrain applies to this dimension
}

// orderSpec is a validated final-output ordinal and fixed SQL direction.
type orderSpec struct {
	Ordinal   int
	Direction string
}

// Build compiles a semantic request into one SQL statement. It is a pure
// function of (model, dialect, request, identity): no I/O, no randomness,
// map iteration only through order slices.
func Build(cm *CompiledModel, d emitter.Dialect, req Request, id governance.Identity) (*Plan, error) {
	if len(req.Metrics) == 0 {
		return nil, fmt.Errorf("request must name at least one metric")
	}
	if req.Model != "" && req.Model != cm.Name {
		return nil, fmt.Errorf("request model %q does not match compiled model %q", req.Model, cm.Name)
	}

	// Resolve metrics in request order.
	var metrics []*CompiledMetric
	for _, name := range req.Metrics {
		m, ok := cm.Metrics[name]
		if !ok {
			return nil, fmt.Errorf("unknown metric %q; available: %s", name, strings.Join(cm.MetricOrder, ", "))
		}
		metrics = append(metrics, m)
	}

	// Resolve dimensions.
	var dims []dimSpec
	for _, ds := range req.Dimensions {
		ref, cf, err := resolveFieldRef(cm, ds)
		if err != nil {
			return nil, fmt.Errorf("dimension %q: %w", ds, err)
		}
		dims = append(dims, dimSpec{Ref: ref, Field: cf})
	}

	// Time grain applies to the first time dimension.
	if req.TimeGrain != "" {
		if !validGrains[req.TimeGrain] {
			return nil, fmt.Errorf("invalid timeGrain %q: use day, week, month, quarter, or year", req.TimeGrain)
		}
		applied := false
		for i := range dims {
			if dims[i].Field.IsTime {
				dims[i].Grained = true
				applied = true
				break
			}
		}
		if !applied {
			return nil, fmt.Errorf("timeGrain %q requires a time dimension (dimension.is_time) among the requested dimensions", req.TimeGrain)
		}
	}

	order, err := resolveOrderBy(req)
	if err != nil {
		return nil, err
	}

	// Resolve filters.
	var filterRefs []expr.FieldRef
	for i := range req.Filters {
		ref, _, err := resolveFieldRef(cm, req.Filters[i].Field)
		if err != nil {
			return nil, fmt.Errorf("filter %q: %w", req.Filters[i].Field, err)
		}
		filterRefs = append(filterRefs, ref)
	}

	// Governance gate: dimensions, filters, and every field inside every
	// requested metric expression. Fails before any SQL exists.
	var allRefs []expr.FieldRef
	for _, dm := range dims {
		allRefs = append(allRefs, dm.Ref)
	}
	allRefs = append(allRefs, filterRefs...)
	for _, m := range metrics {
		allRefs = append(allRefs, m.Expr.Numerator.Refs...)
		if m.Expr.Denominator != nil {
			allRefs = append(allRefs, m.Expr.Denominator.Refs...)
		}
	}
	decision, err := governance.Authorize(cm.Governance, id, req.Metrics, allRefs)
	if err != nil {
		return nil, err
	}
	// Expand claim placeholders now, while the dialect is in hand, so a
	// predicate reaching the builder is already final and safely quoted.
	groups := make([][]v1alpha1.RowFilter, 0, len(decision.RowFilterGroups))
	for _, g := range decision.RowFilterGroups {
		expanded := make([]v1alpha1.RowFilter, 0, len(g))
		for _, rf := range g {
			if cm.Datasets[rf.Dataset] == nil {
				return nil, fmt.Errorf("row filter references unknown dataset %q", rf.Dataset)
			}
			pred, err := governance.ExpandClaims(rf.Predicate, id.Claims, d.Literal)
			if err != nil {
				return nil, err
			}
			expanded = append(expanded, v1alpha1.RowFilter{Dataset: rf.Dataset, Predicate: pred})
		}
		groups = append(groups, expanded)
	}

	b := &builder{cm: cm, d: d, req: req, dims: dims, rowFilterGroups: groups}

	// Base requirement set: dimensions, filters, row filters.
	baseRequired := map[string]bool{}
	for _, dm := range dims {
		baseRequired[dm.Ref.Dataset] = true
	}
	for _, r := range filterRefs {
		baseRequired[r.Dataset] = true
	}
	for _, g := range groups {
		for _, rf := range g {
			baseRequired[rf.Dataset] = true
		}
	}

	// Partition metrics into inline (single-pass) and split (ratio needing
	// fan-out-safe two-sided aggregation).
	var inline, split []*CompiledMetric
	for _, m := range metrics {
		if m.Expr.Denominator == nil {
			inline = append(inline, m)
			for _, r := range m.Expr.Numerator.Refs {
				baseRequired[r.Dataset] = true
			}
			continue
		}
		// Ratio: try inline. Compute the root the combined query would use
		// and check both sides are fan-out safe against it.
		tentative := cloneSet(baseRequired)
		for _, ds := range m.Expr.Datasets() {
			tentative[ds] = true
		}
		root, _, rerr := b.joinTree(tentative)
		if rerr == nil && termSafe(m.Expr.Numerator, root) && termSafe(*m.Expr.Denominator, root) {
			inline = append(inline, m)
			for k := range tentative {
				baseRequired[k] = true
			}
			continue
		}
		split = append(split, m)
	}

	role := decision.RoleKey
	hash := RequestHash(req, governance.IdentityKey(cm.Governance, id))
	comment := fmt.Sprintf("/* semantic-layer model=%s version=%s request=%s */", cm.Name, cm.Version, hash)

	var columns []string
	for _, dm := range dims {
		columns = append(columns, dm.Ref.String())
	}
	columns = append(columns, req.Metrics...)

	var sql string
	if len(split) == 0 {
		sql, err = b.flatQuery(baseRequired, inline, req.Filters, true)
		if err != nil {
			return nil, err
		}
		sql = finishQuery(sql, len(dims), order, req.Limit)
	} else {
		sql, err = b.compositeQuery(baseRequired, inline, split[0], split[1:], req.Filters)
		if err != nil {
			return nil, err
		}
		sql = finishQuery(sql, len(dims), order, req.Limit)
	}

	return &Plan{
		SQL:          comment + "\n" + sql,
		Model:        cm.Name,
		ModelVersion: cm.Version,
		RequestHash:  hash,
		Role:         role,
		Columns:      columns,
	}, nil
}

// termSafe reports whether an aggregation term can run in a single pass over
// a join tree rooted at root without fan-out distortion: COUNT DISTINCT is
// always safe; otherwise every referenced field must live on the root (the
// many side), which the join cannot duplicate.
func termSafe(t expr.AggTerm, root string) bool {
	if t.Func == "COUNT" && t.Distinct {
		return true
	}
	for _, r := range t.Refs {
		if r.Dataset != root {
			return false
		}
	}
	return true
}

func cloneSet(m map[string]bool) map[string]bool {
	out := make(map[string]bool, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

func resolveFieldRef(cm *CompiledModel, s string) (expr.FieldRef, *CompiledField, error) {
	i := strings.IndexByte(s, '.')
	if i <= 0 || i >= len(s)-1 {
		return expr.FieldRef{}, nil, fmt.Errorf("must be dataset.field")
	}
	ref := expr.FieldRef{Dataset: s[:i], Field: s[i+1:]}
	ds, ok := cm.Datasets[ref.Dataset]
	if !ok {
		return expr.FieldRef{}, nil, fmt.Errorf("unknown dataset %q", ref.Dataset)
	}
	cf, ok := ds.Fields[ref.Field]
	if !ok {
		return expr.FieldRef{}, nil, fmt.Errorf("unknown field %q on dataset %q", ref.Field, ref.Dataset)
	}
	return ref, cf, nil
}

func resolveOrderBy(req Request) ([]orderSpec, error) {
	positions := make(map[string][]int, len(req.Dimensions)+len(req.Metrics))
	for i, name := range req.Dimensions {
		positions[name] = append(positions[name], i+1)
	}
	for i, name := range req.Metrics {
		positions[name] = append(positions[name], len(req.Dimensions)+i+1)
	}

	seen := make(map[string]bool, len(req.OrderBy))
	order := make([]orderSpec, 0, len(req.OrderBy))
	for i, clause := range req.OrderBy {
		pos, ok := positions[clause.Field]
		if !ok {
			return nil, fmt.Errorf("orderBy[%d].field %q must reference a requested metric or dimension", i, clause.Field)
		}
		if len(pos) > 1 {
			return nil, fmt.Errorf("orderBy[%d].field %q is ambiguous because it is requested more than once", i, clause.Field)
		}
		if seen[clause.Field] {
			return nil, fmt.Errorf("orderBy[%d].field %q appears more than once", i, clause.Field)
		}

		var direction string
		switch clause.Direction {
		case "asc":
			direction = "ASC"
		case "desc":
			direction = "DESC"
		default:
			return nil, fmt.Errorf("orderBy[%d].direction %q is invalid: use asc or desc", i, clause.Direction)
		}
		seen[clause.Field] = true
		order = append(order, orderSpec{Ordinal: pos[0], Direction: direction})
	}
	return order, nil
}

func finishQuery(sql string, ndims int, order []orderSpec, limit int) string {
	if len(order) > 0 {
		parts := make([]string, len(order))
		for i, spec := range order {
			parts[i] = fmt.Sprintf("%d %s", spec.Ordinal, spec.Direction)
		}
		sql += "\nORDER BY " + strings.Join(parts, ", ")
	} else if ndims > 0 {
		ords := make([]string, ndims)
		for i := range ords {
			ords[i] = fmt.Sprint(i + 1)
		}
		sql += "\nORDER BY " + strings.Join(ords, ", ")
	}
	if limit > 0 {
		sql += fmt.Sprintf("\nLIMIT %d", limit)
	}
	return sql
}
