package catalog

import (
	_ "embed"
	"encoding/json"
	"io"
	"strings"
	"text/template"

	"github.com/KubedAI/semantic-operator/api/v1alpha1"
)

// ossieSpecVersion is the Apache Ossie core-spec version the generated
// spec.ossie block conforms to. Matches ossiectl's default osi-version stamp.
const ossieSpecVersion = "0.2.0.dev0"

//go:embed semanticmodel.yaml.tmpl
var semanticModelTemplateText string

var semanticModelTemplate = template.Must(
	template.New("semanticmodel.yaml.tmpl").Funcs(template.FuncMap{
		"yamlValue": yamlValue,
	}).Parse(semanticModelTemplateText),
)

// TemplateOptions parameterizes the SemanticModel scaffold emitted by
// RenderTemplate. All fields are physical/naming choices; nothing here is a
// business semantic.
type TemplateOptions struct {
	CRName    string // metadata.name
	Namespace string // metadata.namespace
	Catalog   string // spec.connection.catalog (StarRocks external catalog)
	Database  string // spec.connection.database
	Model     string // spec.ossie.name
}

type semanticModelTemplateData struct {
	APIVersion    string
	SpecVersion   string
	Options       TemplateOptions
	Datasets      []templateDataset
	Relationships []templateRelationship
}

type templateDataset struct {
	Name       string
	Source     string
	ExampleKey string
	Fields     []templateField
}

type templateField struct {
	Name        string
	Expression  string
	Description string
	IsTime      bool
}

type templateRelationship struct {
	Name        string
	From        string
	To          string
	FromColumns []string
	ToColumns   []string
	Reason      string
}

// RenderTemplate writes a SemanticModel YAML scaffold to w. The physical parts
// (connection, datasets, fields) are populated from the catalog tables; the
// business parts (metrics, relationships, governance, views, synonyms) are
// emitted as clearly-marked placeholders for a human to fill in. The output is
// intentionally valid out of the box (empty primary_key/relationships/metrics
// are legal) so `ossiectl validate` passes immediately and the file grows richer
// as placeholders are filled.
//
// The generated file mirrors the shape of a hand-authored SemanticModel so
// business users edit in place rather than learning the schema from scratch.
func RenderTemplate(w io.Writer, opts TemplateOptions, tables []Table) error {
	return semanticModelTemplate.Execute(w, prepareTemplateData(opts, tables))
}

func prepareTemplateData(opts TemplateOptions, tables []Table) semanticModelTemplateData {
	data := semanticModelTemplateData{
		APIVersion:  v1alpha1.GroupVersion.String(),
		SpecVersion: ossieSpecVersion,
		Options:     opts,
	}

	for _, dataset := range DeriveDatasets(tables) {
		templateDataset := templateDataset{
			Name:       dataset.Name,
			Source:     dataset.Source,
			ExampleKey: exampleKey(dataset),
		}
		for i := range dataset.Fields {
			field := &dataset.Fields[i]
			expression, ok := field.Expression.Select()
			if !ok || expression == "" {
				expression = field.Name
			}
			templateDataset.Fields = append(templateDataset.Fields, templateField{
				Name:        field.Name,
				Expression:  expression,
				Description: field.Description,
				IsTime:      field.Dimension != nil && field.Dimension.IsTime,
			})
		}
		data.Datasets = append(data.Datasets, templateDataset)
	}

	for _, relationship := range InferRelationships(tables) {
		data.Relationships = append(data.Relationships, templateRelationship{
			Name:        relationship.Name,
			From:        relationship.From,
			To:          relationship.To,
			FromColumns: relationship.FromColumns,
			ToColumns:   relationship.ToColumns,
			Reason:      relationship.Reason,
		})
	}

	return data
}

// exampleKey returns a plausible primary-key column name for the placeholder
// hint: the first field that looks like a surrogate/business key, else the
// first field, else a generic "id".
func exampleKey(d v1alpha1.Dataset) string {
	for i := range d.Fields {
		n := d.Fields[i].Name
		if strings.HasSuffix(n, "_sk") || strings.HasSuffix(n, "_id") {
			return n
		}
	}
	if len(d.Fields) > 0 {
		return d.Fields[0].Name
	}
	return "id"
}

// yamlValue returns JSON encoding, which is valid YAML flow syntax for the
// strings and string lists interpolated into the scaffold. Returning the error
// lets text/template stop rendering if a future unsupported value is supplied.
func yamlValue(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}
