package planner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/KubedAI/ossie-semantic-operator/api/v1alpha1"
	"github.com/KubedAI/ossie-semantic-operator/internal/planner/expr"
)

// CompiledModel is the frozen, JSON-serializable artifact the operator
// publishes and the server plans against. Compilation is pure: the same
// spec always produces the same artifact.
type CompiledModel struct {
	Name       string                      `json:"name"`
	Version    string                      `json:"version"`
	Namespace  string                      `json:"namespace,omitempty"`
	Resource   string                      `json:"resource,omitempty"` // CR name
	Connection v1alpha1.ConnectionSpec     `json:"connection"`
	Datasets   map[string]*CompiledDataset `json:"datasets"`
	// DatasetOrder preserves spec order for deterministic iteration.
	DatasetOrder  []string                  `json:"datasetOrder"`
	Relationships []CompiledRelationship    `json:"relationships"`
	Metrics       map[string]*CompiledMetric `json:"metrics"`
	MetricOrder   []string                  `json:"metricOrder"`
	Governance    *v1alpha1.GovernanceSpec  `json:"governance,omitempty"`
	Description   string                    `json:"description,omitempty"`
	AIContext     v1alpha1.AIContext        `json:"aiContext,omitempty"`
}

// CompiledDataset is a dataset with its physical binding resolved.
type CompiledDataset struct {
	Name        string                    `json:"name"`
	Catalog     string                    `json:"catalog"`
	Database    string                    `json:"database"`
	Table       string                    `json:"table"`
	PrimaryKey  []string                  `json:"primaryKey,omitempty"`
	Description string                    `json:"description,omitempty"`
	AIContext   v1alpha1.AIContext        `json:"aiContext,omitempty"`
	Fields      map[string]*CompiledField `json:"fields"`
	FieldOrder  []string                  `json:"fieldOrder"`
}

// CompiledField is a field with its dialect expression selected.
type CompiledField struct {
	Name        string             `json:"name"`
	Expr        string             `json:"expr"` // scalar over the dataset's physical columns
	IsTime      bool               `json:"isTime,omitempty"`
	Type        string             `json:"type,omitempty"` // physical type, filled by the bind step when known
	Description string             `json:"description,omitempty"`
	AIContext   v1alpha1.AIContext `json:"aiContext,omitempty"`
}

// CompiledRelationship is a join-graph edge (many side -> one side).
type CompiledRelationship struct {
	Name        string   `json:"name"`
	From        string   `json:"from"`
	To          string   `json:"to"`
	FromColumns []string `json:"fromColumns"`
	ToColumns   []string `json:"toColumns"`
	JoinType    string   `json:"joinType"` // INNER or LEFT
}

// CompiledMetric is a metric with its expression parsed.
type CompiledMetric struct {
	Name        string             `json:"name"`
	Raw         string             `json:"raw"`
	Expr        expr.MetricExpr    `json:"expr"`
	Description string             `json:"description,omitempty"`
	AIContext   v1alpha1.AIContext `json:"aiContext,omitempty"`
}

// SpecVersion computes the content-addressed model version: a short sha256
// of the canonical JSON encoding of the spec.
func SpecVersion(spec *v1alpha1.SemanticModelSpec) string {
	b, _ := json.Marshal(spec)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])[:12]
}

// Compile freezes a validated spec into a CompiledModel. It assumes
// ossie.ValidateSpec passed; errors here indicate planner-subset violations.
func Compile(spec *v1alpha1.SemanticModelSpec, namespace, resource string) (*CompiledModel, error) {
	cm := &CompiledModel{
		Name:        spec.Ossie.Name,
		Version:     SpecVersion(spec),
		Namespace:   namespace,
		Resource:    resource,
		Connection:  spec.Connection,
		Datasets:    map[string]*CompiledDataset{},
		Metrics:     map[string]*CompiledMetric{},
		Governance:  spec.Governance,
		Description: spec.Ossie.Description,
		AIContext:   v1alpha1.DecodeAIContext(spec.Ossie.AIContext),
	}
	for i := range spec.Ossie.Datasets {
		d := &spec.Ossie.Datasets[i]
		cat, db, table, err := ResolveSource(d.Source, spec.Connection)
		if err != nil {
			return nil, fmt.Errorf("dataset %q: %w", d.Name, err)
		}
		cd := &CompiledDataset{
			Name:        d.Name,
			Catalog:     cat,
			Database:    db,
			Table:       table,
			PrimaryKey:  d.PrimaryKey,
			Description: d.Description,
			AIContext:   v1alpha1.DecodeAIContext(d.AIContext),
			Fields:      map[string]*CompiledField{},
		}
		for j := range d.Fields {
			f := &d.Fields[j]
			body, ok := f.Expression.Select()
			if !ok {
				return nil, fmt.Errorf("dataset %q field %q: no usable dialect expression", d.Name, f.Name)
			}
			cf := &CompiledField{
				Name:        f.Name,
				Expr:        body,
				Description: f.Description,
				AIContext:   v1alpha1.DecodeAIContext(f.AIContext),
			}
			if f.Dimension != nil {
				cf.IsTime = f.Dimension.IsTime
			}
			cd.Fields[f.Name] = cf
			cd.FieldOrder = append(cd.FieldOrder, f.Name)
		}
		cm.Datasets[d.Name] = cd
		cm.DatasetOrder = append(cm.DatasetOrder, d.Name)
	}
	for i := range spec.Ossie.Relationships {
		r := &spec.Ossie.Relationships[i]
		jt := r.JoinType
		if jt == "" {
			jt = "INNER"
		}
		cm.Relationships = append(cm.Relationships, CompiledRelationship{
			Name: r.Name, From: r.From, To: r.To,
			FromColumns: r.FromColumns, ToColumns: r.ToColumns, JoinType: jt,
		})
	}
	for i := range spec.Ossie.Metrics {
		m := &spec.Ossie.Metrics[i]
		body, ok := m.Expression.Select()
		if !ok {
			return nil, fmt.Errorf("metric %q: no usable dialect expression", m.Name)
		}
		parsed, err := expr.Parse(body)
		if err != nil {
			return nil, fmt.Errorf("metric %q: %w", m.Name, err)
		}
		cm.Metrics[m.Name] = &CompiledMetric{
			Name: m.Name, Raw: body, Expr: parsed,
			Description: m.Description,
			AIContext:   v1alpha1.DecodeAIContext(m.AIContext),
		}
		cm.MetricOrder = append(cm.MetricOrder, m.Name)
	}
	return cm, nil
}

// ResolveSource resolves an Ossie dataset source against the CR connection.
// Accepted forms: "table", "database.table", "catalog.database.table".
func ResolveSource(source string, conn v1alpha1.ConnectionSpec) (catalog, database, table string, err error) {
	parts := strings.Split(source, ".")
	switch len(parts) {
	case 1:
		return conn.Catalog, conn.Database, parts[0], nil
	case 2:
		return conn.Catalog, parts[0], parts[1], nil
	case 3:
		return parts[0], parts[1], parts[2], nil
	default:
		return "", "", "", fmt.Errorf("source %q: expected table, database.table, or catalog.database.table", source)
	}
}
