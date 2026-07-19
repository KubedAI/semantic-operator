package planner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/KubedAI/semantic-operator/internal/emitter"
	"github.com/KubedAI/semantic-operator/internal/governance"
	"github.com/KubedAI/semantic-operator/internal/planner/expr"
)

// Request is a semantic query: metrics by dimensions, filtered, at a grain.
type Request struct {
	Model      string   `json:"model"`
	Metrics    []string `json:"metrics"`
	Dimensions []string `json:"dimensions,omitempty"`
	Filters    []Filter `json:"filters,omitempty"`
	// TimeGrain is one of day, week, month, quarter, year. It truncates the
	// first requested time dimension (dimension.is_time).
	TimeGrain string `json:"timeGrain,omitempty"`
	Limit     int    `json:"limit,omitempty"`
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
	SQL          string   `json:"sql"`
	Model        string   `json:"model"`
	ModelVersion string   `json:"modelVersion"`
	RequestHash  string   `json:"requestHash"`
	Role         string   `json:"role,omitempty"`
	Columns      []string `json:"columns"`
}

var validGrains = map[string]bool{"day": true, "week": true, "month": true, "quarter": true, "year": true}

// RequestHash canonically hashes a request plus the effective role. It is
// the cache-key component: same request + same role + same model version
// means the same plan.
func RequestHash(req Request, role string) string {
	b, _ := json.Marshal(struct {
		Request
		Role string `json:"role"`
	}{req, role})
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])[:16]
}

// dimSpec is a resolved requested dimension.
type dimSpec struct {
	Ref     expr.FieldRef
	Field   *CompiledField
	Grained bool // TimeGrain applies to this dimension
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
	for _, rf := range decision.RowFilters {
		if cm.Datasets[rf.Dataset] == nil {
			return nil, fmt.Errorf("row filter references unknown dataset %q", rf.Dataset)
		}
	}

	b := &builder{cm: cm, d: d, req: req, dims: dims, rowFilters: decision.RowFilters}

	// Base requirement set: dimensions, filters, row filters.
	baseRequired := map[string]bool{}
	for _, dm := range dims {
		baseRequired[dm.Ref.Dataset] = true
	}
	for _, r := range filterRefs {
		baseRequired[r.Dataset] = true
	}
	for _, rf := range decision.RowFilters {
		baseRequired[rf.Dataset] = true
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

	role := decision.Role
	hash := RequestHash(req, role)
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
		sql = finishQuery(sql, len(dims), req.Limit)
	} else {
		sql, err = b.compositeQuery(baseRequired, inline, split[0], split[1:], req.Filters)
		if err != nil {
			return nil, err
		}
		sql = finishQuery(sql, len(dims), req.Limit)
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

func finishQuery(sql string, ndims, limit int) string {
	if ndims > 0 {
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
