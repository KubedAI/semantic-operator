package planner

import (
	"fmt"
	"strings"

	"github.com/vara-bonthu/osi-semantic-operator/api/v1alpha1"
	"github.com/vara-bonthu/osi-semantic-operator/internal/emitter"
	"github.com/vara-bonthu/osi-semantic-operator/internal/planner/expr"
)

// builder holds per-request state for SQL construction. All methods are
// deterministic: iteration is over spec-ordered slices only.
type builder struct {
	cm         *CompiledModel
	d          emitter.Dialect
	req        Request
	dims       []dimSpec
	rowFilters []v1alpha1.RowFilter
}

// joinTree picks the root dataset and the pruned, ordered list of joins that
// connect every required dataset. Roots are tried in a stable order: first
// the required datasets themselves (a query touching only `store` must not
// detour through the fact table), then all datasets in spec order.
func (b *builder) joinTree(required map[string]bool) (string, []CompiledRelationship, error) {
	if len(required) == 0 {
		return "", nil, fmt.Errorf("internal: empty dataset requirement")
	}
	var candidates []string
	for _, ds := range b.cm.DatasetOrder {
		if required[ds] {
			candidates = append(candidates, ds)
		}
	}
	candidates = append(candidates, b.cm.DatasetOrder...)

	for _, root := range candidates {
		if joins, ok := b.reach(root, required); ok {
			return root, joins, nil
		}
	}
	names := make([]string, 0, len(required))
	for _, ds := range b.cm.DatasetOrder {
		if required[ds] {
			names = append(names, ds)
		}
	}
	return "", nil, fmt.Errorf("no join path connects datasets [%s]; check osi.relationships", strings.Join(names, ", "))
}

// reach runs BFS from root over relationship edges (from -> to, spec order),
// then prunes to the edges on a path from root to a required dataset.
func (b *builder) reach(root string, required map[string]bool) ([]CompiledRelationship, bool) {
	parent := map[string]*CompiledRelationship{} // discovered node -> edge used
	visited := map[string]bool{root: true}
	queue := []string{root}
	var discovery []*CompiledRelationship
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for i := range b.cm.Relationships {
			rel := &b.cm.Relationships[i]
			if rel.From != cur || visited[rel.To] {
				continue
			}
			visited[rel.To] = true
			parent[rel.To] = rel
			discovery = append(discovery, rel)
			queue = append(queue, rel.To)
		}
	}
	needed := map[*CompiledRelationship]bool{}
	for ds := range required {
		if !visited[ds] {
			return nil, false
		}
		for cur := ds; cur != root; {
			rel := parent[cur]
			needed[rel] = true
			cur = rel.From
		}
	}
	var joins []CompiledRelationship
	for _, rel := range discovery {
		if needed[rel] {
			joins = append(joins, *rel)
		}
	}
	return joins, true
}

// alias returns the quoted table alias for a dataset (its logical name).
func (b *builder) alias(dataset string) string { return b.d.QuoteIdent(dataset) }

// renderFieldRef renders a dataset.field as SQL against the dataset alias.
// Compound field expressions are parenthesized.
func (b *builder) renderFieldRef(ref expr.FieldRef) (string, error) {
	ds := b.cm.Datasets[ref.Dataset]
	if ds == nil {
		return "", fmt.Errorf("unknown dataset %q", ref.Dataset)
	}
	cf := ds.Fields[ref.Field]
	if cf == nil {
		return "", fmt.Errorf("unknown field %q on dataset %q", ref.Field, ref.Dataset)
	}
	out := expr.QualifyBareColumns(cf.Expr, b.alias(ref.Dataset))
	if strings.ContainsAny(out, " (") {
		out = "(" + out + ")"
	}
	return out, nil
}

func (b *builder) renderDim(dm dimSpec) (string, error) {
	s, err := b.renderFieldRef(dm.Ref)
	if err != nil {
		return "", err
	}
	if dm.Grained {
		s = b.d.DateTrunc(b.req.TimeGrain, s)
	}
	return s, nil
}

// renderTerm renders one aggregation term: FUNC([DISTINCT] scalar) with
// dataset.field references rewritten to qualified physical expressions.
func (b *builder) renderTerm(t expr.AggTerm) (string, error) {
	var rerr error
	scalar := expr.RewriteRefs(t.Scalar, func(r expr.FieldRef) string {
		s, err := b.renderFieldRef(r)
		if err != nil && rerr == nil {
			rerr = err
		}
		return s
	})
	if rerr != nil {
		return "", rerr
	}
	if t.Distinct {
		return t.Func + "(DISTINCT " + scalar + ")", nil
	}
	return t.Func + "(" + scalar + ")", nil
}

// renderInlineMetric renders a metric that is safe in a single pass:
// aggregation or ratio with NULLIF-protected denominator.
func (b *builder) renderInlineMetric(m *CompiledMetric) (string, error) {
	num, err := b.renderTerm(m.Expr.Numerator)
	if err != nil {
		return "", err
	}
	if m.Expr.Denominator == nil {
		return num, nil
	}
	den, err := b.renderTerm(*m.Expr.Denominator)
	if err != nil {
		return "", err
	}
	return num + " / NULLIF(" + den + ", 0)", nil
}

func (b *builder) renderFilter(f Filter) (string, error) {
	ref, _, err := resolveFieldRef(b.cm, f.Field)
	if err != nil {
		return "", fmt.Errorf("filter %q: %w", f.Field, err)
	}
	lhs, err := b.renderFieldRef(ref)
	if err != nil {
		return "", err
	}
	lit := func(v any) (string, error) { return b.d.Literal(v) }
	switch f.Op {
	case "=", "!=", "<", "<=", ">", ">=", "LIKE":
		v, err := lit(f.Value)
		if err != nil {
			return "", fmt.Errorf("filter %q: %w", f.Field, err)
		}
		return lhs + " " + f.Op + " " + v, nil
	case "IN", "NOT IN":
		if len(f.Values) == 0 {
			return "", fmt.Errorf("filter %q: %s requires values", f.Field, f.Op)
		}
		parts := make([]string, len(f.Values))
		for i, v := range f.Values {
			s, err := lit(v)
			if err != nil {
				return "", fmt.Errorf("filter %q: %w", f.Field, err)
			}
			parts[i] = s
		}
		return lhs + " " + f.Op + " (" + strings.Join(parts, ", ") + ")", nil
	case "BETWEEN":
		if len(f.Values) != 2 {
			return "", fmt.Errorf("filter %q: BETWEEN requires exactly two values", f.Field)
		}
		lo, err := lit(f.Values[0])
		if err != nil {
			return "", err
		}
		hi, err := lit(f.Values[1])
		if err != nil {
			return "", err
		}
		return lhs + " BETWEEN " + lo + " AND " + hi, nil
	default:
		return "", fmt.Errorf("filter %q: unsupported operator %q", f.Field, f.Op)
	}
}

// whereClauses renders user filters (request order) then governance row
// filters (policy order), each parenthesized. Row filters referencing
// datasets outside the query's join tree are a planning error.
func (b *builder) whereClauses(filters []Filter, inTree map[string]bool) ([]string, error) {
	var out []string
	for _, f := range filters {
		s, err := b.renderFilter(f)
		if err != nil {
			return nil, err
		}
		out = append(out, "("+s+")")
	}
	for _, rf := range b.rowFilters {
		if !inTree[rf.Dataset] {
			return nil, fmt.Errorf("internal: row filter dataset %q not in join tree", rf.Dataset)
		}
		out = append(out, "("+expr.QualifyBareColumns(rf.Predicate, b.alias(rf.Dataset))+")")
	}
	return out, nil
}

// fromClause renders FROM root plus the ordered joins.
func (b *builder) fromClause(root string, joins []CompiledRelationship) (string, map[string]bool) {
	inTree := map[string]bool{root: true}
	rootDS := b.cm.Datasets[root]
	var sb strings.Builder
	sb.WriteString("FROM " + b.d.QualifyTable(rootDS.Catalog, rootDS.Database, rootDS.Table) + " AS " + b.alias(root))
	for _, j := range joins {
		inTree[j.To] = true
		to := b.cm.Datasets[j.To]
		var conds []string
		for i := range j.FromColumns {
			conds = append(conds,
				b.alias(j.From)+"."+b.d.QuoteIdent(j.FromColumns[i])+" = "+
					b.alias(j.To)+"."+b.d.QuoteIdent(j.ToColumns[i]))
		}
		sb.WriteString("\n" + j.JoinType + " JOIN " + b.d.QualifyTable(to.Catalog, to.Database, to.Table) +
			" AS " + b.alias(j.To) + " ON " + strings.Join(conds, " AND "))
	}
	return sb.String(), inTree
}

// selectItem is one output column of a (sub)query.
type selectItem struct {
	Expr  string
	Alias string // already-safe identifier, quoted at render
}

// flatQuery renders a single-pass SELECT: dims + inline metric aggregates
// over one join tree, grouped by the dimension ordinals.
func (b *builder) flatQuery(required map[string]bool, metrics []*CompiledMetric, filters []Filter, publicAliases bool) (string, error) {
	var items []selectItem
	for i, dm := range b.dims {
		e, err := b.renderDim(dm)
		if err != nil {
			return "", err
		}
		alias := fmt.Sprintf("d%d", i)
		if publicAliases {
			alias = dm.Ref.String()
		}
		items = append(items, selectItem{Expr: e, Alias: alias})
	}
	for _, m := range metrics {
		e, err := b.renderInlineMetric(m)
		if err != nil {
			return "", err
		}
		alias := "m_" + m.Name
		if publicAliases {
			alias = m.Name
		}
		items = append(items, selectItem{Expr: e, Alias: alias})
	}
	root, joins, err := b.joinTree(required)
	if err != nil {
		return "", err
	}
	from, inTree := b.fromClause(root, joins)
	where, err := b.whereClauses(filters, inTree)
	if err != nil {
		return "", err
	}
	return renderSelect(b.d, items, from, where, len(b.dims)), nil
}

// sideQuery renders one side of a split ratio metric as a subquery with
// columns d0..dn, val. When the aggregated dataset sits on the one side of
// a join (fan-out), the rows are deduplicated on that dataset's primary key
// before aggregating.
func (b *builder) sideQuery(t expr.AggTerm, filters []Filter) (string, error) {
	required := map[string]bool{}
	for _, dm := range b.dims {
		required[dm.Ref.Dataset] = true
	}
	for _, f := range filters {
		ref, _, err := resolveFieldRef(b.cm, f.Field)
		if err != nil {
			return "", err
		}
		required[ref.Dataset] = true
	}
	for _, rf := range b.rowFilters {
		required[rf.Dataset] = true
	}
	for _, r := range t.Refs {
		required[r.Dataset] = true
	}

	root, joins, err := b.joinTree(required)
	if err != nil {
		return "", err
	}
	from, inTree := b.fromClause(root, joins)
	where, err := b.whereClauses(filters, inTree)
	if err != nil {
		return "", err
	}

	measureDatasets := termDatasetsOf(t)
	safe := (t.Func == "COUNT" && t.Distinct) || (len(measureDatasets) == 1 && measureDatasets[0] == root)
	if safe {
		var items []selectItem
		for i, dm := range b.dims {
			e, derr := b.renderDim(dm)
			if derr != nil {
				return "", derr
			}
			items = append(items, selectItem{Expr: e, Alias: fmt.Sprintf("d%d", i)})
		}
		agg, aerr := b.renderTerm(t)
		if aerr != nil {
			return "", aerr
		}
		items = append(items, selectItem{Expr: agg, Alias: "val"})
		return renderSelect(b.d, items, from, where, len(b.dims)), nil
	}

	// Fan-out: deduplicate on the measure dataset's primary key, then aggregate.
	if len(measureDatasets) != 1 {
		return "", fmt.Errorf("ratio side %s(%s) references multiple datasets and is not fan-out safe; not supported", t.Func, t.Scalar)
	}
	mds := b.cm.Datasets[measureDatasets[0]]
	if len(mds.PrimaryKey) == 0 {
		return "", fmt.Errorf("ratio side aggregates dataset %q across a fan-out join but it declares no primary_key", mds.Name)
	}
	var inner []selectItem
	for i, pk := range mds.PrimaryKey {
		inner = append(inner, selectItem{
			Expr:  b.alias(mds.Name) + "." + b.d.QuoteIdent(pk),
			Alias: fmt.Sprintf("pk%d", i),
		})
	}
	for i, dm := range b.dims {
		e, derr := b.renderDim(dm)
		if derr != nil {
			return "", derr
		}
		inner = append(inner, selectItem{Expr: e, Alias: fmt.Sprintf("d%d", i)})
	}
	var rerr error
	scalar := expr.RewriteRefs(t.Scalar, func(r expr.FieldRef) string {
		s, e := b.renderFieldRef(r)
		if e != nil && rerr == nil {
			rerr = e
		}
		return s
	})
	if rerr != nil {
		return "", rerr
	}
	inner = append(inner, selectItem{Expr: scalar, Alias: "mval"})
	innerSQL := renderSelectDistinct(b.d, inner, from, where)

	var outer []selectItem
	for i := range b.dims {
		outer = append(outer, selectItem{Expr: "t." + b.d.QuoteIdent(fmt.Sprintf("d%d", i)), Alias: fmt.Sprintf("d%d", i)})
	}
	outer = append(outer, selectItem{Expr: t.Func + "(t.`mval`)", Alias: "val"})
	return renderSelect(b.d, outer, "FROM (\n"+indent(innerSQL)+"\n) AS t", nil, len(b.dims)), nil
}

// compositeQuery combines a base single-pass query with one or more split
// ratio metrics via CTEs joined null-safe on the dimension columns.
func (b *builder) compositeQuery(baseRequired map[string]bool, inline []*CompiledMetric, firstSplit *CompiledMetric, restSplit []*CompiledMetric, filters []Filter) (string, error) {
	splits := append([]*CompiledMetric{firstSplit}, restSplit...)

	type cte struct{ name, sql string }
	var ctes []cte
	hasBase := len(inline) > 0
	if hasBase {
		// Base still needs its own join tree even with only dims + filters.
		sql, err := b.flatQuery(baseRequired, inline, filters, false)
		if err != nil {
			return "", err
		}
		ctes = append(ctes, cte{"base", sql})
	}
	for _, m := range splits {
		num, err := b.sideQuery(m.Expr.Numerator, filters)
		if err != nil {
			return "", fmt.Errorf("metric %q numerator: %w", m.Name, err)
		}
		den, err := b.sideQuery(*m.Expr.Denominator, filters)
		if err != nil {
			return "", fmt.Errorf("metric %q denominator: %w", m.Name, err)
		}
		ctes = append(ctes, cte{"m_" + m.Name + "_num", num}, cte{"m_" + m.Name + "_den", den})
	}

	anchor := ctes[0].name
	var sb strings.Builder
	sb.WriteString("WITH ")
	for i, c := range ctes {
		if i > 0 {
			sb.WriteString(",\n")
		}
		sb.WriteString(b.d.QuoteIdent(c.name) + " AS (\n" + indent(c.sql) + "\n)")
	}
	sb.WriteString("\nSELECT ")

	var sel []string
	for i, dm := range b.dims {
		sel = append(sel, b.d.QuoteIdent(anchor)+"."+b.d.QuoteIdent(fmt.Sprintf("d%d", i))+" AS "+b.d.QuoteIdent(dm.Ref.String()))
	}
	// Metric output order follows the request, mixing inline and split.
	for _, name := range b.req.Metrics {
		if hasBase && containsMetric(inline, name) {
			sel = append(sel, b.d.QuoteIdent("base")+"."+b.d.QuoteIdent("m_"+name)+" AS "+b.d.QuoteIdent(name))
		} else {
			n := b.d.QuoteIdent("m_"+name+"_num") + ".`val`"
			d := b.d.QuoteIdent("m_"+name+"_den") + ".`val`"
			sel = append(sel, n+" / NULLIF("+d+", 0) AS "+b.d.QuoteIdent(name))
		}
	}
	sb.WriteString(strings.Join(sel, ",\n       "))
	sb.WriteString("\nFROM " + b.d.QuoteIdent(anchor))
	for _, c := range ctes[1:] {
		if len(b.dims) == 0 {
			sb.WriteString("\nCROSS JOIN " + b.d.QuoteIdent(c.name))
			continue
		}
		var conds []string
		for i := range b.dims {
			dcol := b.d.QuoteIdent(fmt.Sprintf("d%d", i))
			conds = append(conds, b.d.NullSafeEq(
				b.d.QuoteIdent(anchor)+"."+dcol,
				b.d.QuoteIdent(c.name)+"."+dcol,
			))
		}
		sb.WriteString("\nINNER JOIN " + b.d.QuoteIdent(c.name) + " ON " + strings.Join(conds, " AND "))
	}
	return sb.String(), nil
}

func containsMetric(ms []*CompiledMetric, name string) bool {
	for _, m := range ms {
		if m.Name == name {
			return true
		}
	}
	return false
}

func termDatasetsOf(t expr.AggTerm) []string {
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

func renderSelect(d emitter.Dialect, items []selectItem, from string, where []string, groupDims int) string {
	var sb strings.Builder
	sb.WriteString("SELECT ")
	parts := make([]string, len(items))
	for i, it := range items {
		parts[i] = it.Expr + " AS " + d.QuoteIdent(it.Alias)
	}
	sb.WriteString(strings.Join(parts, ",\n       "))
	sb.WriteString("\n" + from)
	if len(where) > 0 {
		sb.WriteString("\nWHERE " + strings.Join(where, "\n  AND "))
	}
	if groupDims > 0 && len(items) > groupDims {
		ords := make([]string, groupDims)
		for i := range ords {
			ords[i] = fmt.Sprint(i + 1)
		}
		sb.WriteString("\nGROUP BY " + strings.Join(ords, ", "))
	}
	return sb.String()
}

func renderSelectDistinct(d emitter.Dialect, items []selectItem, from string, where []string) string {
	var sb strings.Builder
	sb.WriteString("SELECT DISTINCT ")
	parts := make([]string, len(items))
	for i, it := range items {
		parts[i] = it.Expr + " AS " + d.QuoteIdent(it.Alias)
	}
	sb.WriteString(strings.Join(parts, ",\n       "))
	sb.WriteString("\n" + from)
	if len(where) > 0 {
		sb.WriteString("\nWHERE " + strings.Join(where, "\n  AND "))
	}
	return sb.String()
}

func indent(s string) string {
	return "  " + strings.ReplaceAll(s, "\n", "\n  ")
}
