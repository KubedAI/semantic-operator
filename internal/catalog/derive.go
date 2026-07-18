package catalog

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	"github.com/KubedAI/ossie-semantic-operator/api/v1alpha1"
)

// DeriveDatasets turns physical tables into Ossie dataset stubs: source
// binding, one field per column (ANSI_SQL identity expression), is_time on
// date/timestamp columns. Humans keep metrics, relationships, and synonyms;
// this output is regenerated as the physical schema evolves.
func DeriveDatasets(tables []Table) []v1alpha1.Dataset {
	sorted := append([]Table{}, tables...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	var out []v1alpha1.Dataset
	for _, t := range sorted {
		d := v1alpha1.Dataset{Name: t.Name, Source: t.Name}
		for _, c := range t.Columns {
			f := v1alpha1.Field{
				Name: c.Name,
				Expression: v1alpha1.Expression{Dialects: []v1alpha1.DialectExpression{
					{Dialect: "ANSI_SQL", Expression: c.Name},
				}},
				Description: c.Comment,
			}
			if isTimeType(c.Type) {
				f.Dimension = &v1alpha1.Dimension{IsTime: true}
			}
			d.Fields = append(d.Fields, f)
		}
		out = append(out, d)
	}
	return out
}

// SyncFields refreshes an existing dataset's field list from the physical
// table: new columns are appended as identity fields, existing fields
// (including computed ones) are left untouched, and nothing is removed
// (removal is drift, surfaced by the controller, decided by a human).
// Returns the number of fields added.
func SyncFields(d *v1alpha1.Dataset, t Table) int {
	have := map[string]bool{}
	for _, f := range d.Fields {
		have[f.Name] = true
	}
	added := 0
	for _, c := range t.Columns {
		if have[c.Name] {
			continue
		}
		f := v1alpha1.Field{
			Name: c.Name,
			Expression: v1alpha1.Expression{Dialects: []v1alpha1.DialectExpression{
				{Dialect: "ANSI_SQL", Expression: c.Name},
			}},
			Description: c.Comment,
		}
		if isTimeType(c.Type) {
			f.Dimension = &v1alpha1.Dimension{IsTime: true}
		}
		d.Fields = append(d.Fields, f)
		added++
	}
	return added
}

// CandidateRelationship is an inferred join edge, produced for human review.
type CandidateRelationship struct {
	v1alpha1.Relationship
	Reason string
}

// InferRelationships proposes join edges. Glue rarely records foreign keys,
// so inference is convention-based: a column on table A that exactly matches
// a column name on table B where it looks like B's surrogate key (both named
// *_sk/*_id with the same suffix semantics, as in TPC-DS ss_item_sk ->
// i_item_sk) or an exact name match. Candidates are suggestions only; the
// caller emits them commented out for review.
func InferRelationships(tables []Table) []CandidateRelationship {
	sorted := append([]Table{}, tables...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	// Index: suffix (e.g. "item_sk") -> tables owning a column with that suffix
	// that plausibly is their key.
	type keyCol struct{ table, column string }
	keyIndex := map[string][]keyCol{}
	for _, t := range sorted {
		for _, c := range t.Columns {
			if suf, ok := keySuffix(c.Name); ok {
				keyIndex[suf] = append(keyIndex[suf], keyCol{t.Name, c.Name})
			}
		}
	}

	var out []CandidateRelationship
	for _, t := range sorted {
		for _, c := range t.Columns {
			suf, ok := keySuffix(c.Name)
			if !ok {
				continue
			}
			for _, target := range keyIndex[suf] {
				if target.table == t.Name || target.column == c.Name && target.table == t.Name {
					continue
				}
				// Heuristic: the "to" side owns the key when its column is
				// prefixed like the table's own initials (i_item_sk on item),
				// i.e. the suffix minus prefix matches the table name.
				if !ownsKey(target.table, target.column, suf) {
					continue
				}
				if t.Name == target.table {
					continue
				}
				out = append(out, CandidateRelationship{
					Relationship: v1alpha1.Relationship{
						Name:        fmt.Sprintf("%s_to_%s", t.Name, target.table),
						From:        t.Name,
						To:          target.table,
						FromColumns: []string{c.Name},
						ToColumns:   []string{target.column},
					},
					Reason: fmt.Sprintf("%s.%s and %s.%s share key suffix %q", t.Name, c.Name, target.table, target.column, suf),
				})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return dedupeCandidates(out)
}

func dedupeCandidates(in []CandidateRelationship) []CandidateRelationship {
	seen := map[string]bool{}
	var out []CandidateRelationship
	for _, c := range in {
		k := c.From + "|" + c.To + "|" + strings.Join(c.FromColumns, ",")
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, c)
	}
	return out
}

// keySuffix extracts the shared join-key suffix from a TPC-DS style column:
// ss_item_sk -> "item_sk", i_item_sk -> "item_sk", d_date_sk -> "date_sk".
// Only *_sk and *_id columns participate.
func keySuffix(col string) (string, bool) {
	if !strings.HasSuffix(col, "_sk") && !strings.HasSuffix(col, "_id") {
		return "", false
	}
	parts := strings.SplitN(col, "_", 2)
	if len(parts) != 2 || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

// ownsKey reports whether table.column looks like the table's own surrogate
// key for the suffix: the suffix body names the table (item_sk on item,
// date_sk on date_dim).
func ownsKey(table, column, suffix string) bool {
	body := strings.TrimSuffix(strings.TrimSuffix(suffix, "_sk"), "_id")
	return table == body || strings.HasPrefix(table, body+"_") || strings.HasPrefix(body, strings.TrimSuffix(table, "_dim"))
}

func isTimeType(t string) bool {
	lt := strings.ToLower(t)
	return strings.HasPrefix(lt, "date") || strings.HasPrefix(lt, "timestamp") || strings.HasPrefix(lt, "datetime")
}

// AIContextJSON builds an ai_context JSON value from synonyms, for generated
// stubs where only a synonyms list applies.
func AIContextJSON(synonyms ...string) *apiextensionsv1.JSON {
	b, _ := json.Marshal(map[string]any{"synonyms": synonyms})
	return &apiextensionsv1.JSON{Raw: b}
}
