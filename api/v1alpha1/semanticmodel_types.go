package v1alpha1

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Condition types set on SemanticModel status.
const (
	ConditionValidated     = "Validated"
	ConditionCompiled      = "Compiled"
	ConditionPublished     = "Published"
	ConditionDriftDetected = "DriftDetected"
	ConditionViewsReady    = "ViewsReady"
)

// Labels and keys used on published ConfigMaps.
const (
	LabelModel        = "semantic.ossie.io/model"
	LabelVersion      = "semantic.ossie.io/version"
	CompiledModelKey  = "compiled-model.json"
	ManagedByValue    = "semantic-operator"
	AnnotationOwnedViews = "semantic.ossie.io/owned-views"
)

// SemanticModelSpec defines the desired state of SemanticModel.
// The ossie block is an Apache Ossie semantic_model entry, structurally
// unmodified so it round-trips as Ossie YAML. governance and views are
// operator extensions kept outside the Ossie document.
type SemanticModelSpec struct {
	// Connection pins the Ossie model's dataset sources to a StarRocks external catalog.
	Connection ConnectionSpec `json:"connection"`

	// Ossie is the semantic model definition, following the Ossie core spec.
	Ossie OssieModel `json:"ossie"`

	// Governance defines compile-time row/column/metric access policies.
	// +optional
	Governance *GovernanceSpec `json:"governance,omitempty"`

	// Views are governed metric views the operator creates in StarRocks for BI tools.
	// +optional
	Views []ViewSpec `json:"views,omitempty"`

	// CatalogSync refreshes dataset field lists from the lake catalog (e.g. Glue).
	// +optional
	CatalogSync *CatalogSyncSpec `json:"catalogSync,omitempty"`
}

// ConnectionSpec resolves bare dataset sources to physical tables.
type ConnectionSpec struct {
	// Catalog is the StarRocks external catalog name (e.g. "iceberg").
	Catalog string `json:"catalog"`
	// Database is the database/schema within the catalog (e.g. "osi_demo").
	Database string `json:"database"`
	// ViewDatabase is the StarRocks default-catalog database where governed
	// views are created. Defaults to "semantic_views".
	// +optional
	ViewDatabase string `json:"viewDatabase,omitempty"`
}

// OssieModel mirrors one entry of the Ossie `semantic_model` list.
type OssieModel struct {
	Name        string `json:"name"`
	// +optional
	Description string `json:"description,omitempty"`
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	// +optional
	AIContext *apiextensionsv1.JSON `json:"ai_context,omitempty"`
	Datasets  []Dataset             `json:"datasets"`
	// +optional
	Relationships []Relationship `json:"relationships,omitempty"`
	// +optional
	Metrics []Metric `json:"metrics,omitempty"`
	// +optional
	CustomExtensions []CustomExtension `json:"custom_extensions,omitempty"`
}

// Dataset is an Ossie logical dataset bound to a physical table.
type Dataset struct {
	Name string `json:"name"`
	// Source is the physical binding: bare table name (resolved against
	// spec.connection) or fully qualified catalog.database.table.
	Source string `json:"source"`
	// +optional
	PrimaryKey []string `json:"primary_key,omitempty"`
	// +optional
	UniqueKeys [][]string `json:"unique_keys,omitempty"`
	// +optional
	Description string `json:"description,omitempty"`
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	// +optional
	AIContext *apiextensionsv1.JSON `json:"ai_context,omitempty"`
	// +optional
	Fields []Field `json:"fields,omitempty"`
	// +optional
	CustomExtensions []CustomExtension `json:"custom_extensions,omitempty"`
}

// Field is a row-level attribute: a column reference or scalar expression.
type Field struct {
	Name       string     `json:"name"`
	Expression Expression `json:"expression"`
	// +optional
	Dimension *Dimension `json:"dimension,omitempty"`
	// +optional
	Label string `json:"label,omitempty"`
	// +optional
	Description string `json:"description,omitempty"`
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	// +optional
	AIContext *apiextensionsv1.JSON `json:"ai_context,omitempty"`
	// +optional
	CustomExtensions []CustomExtension `json:"custom_extensions,omitempty"`
}

// Expression holds per-dialect scalar or aggregate SQL expressions.
type Expression struct {
	Dialects []DialectExpression `json:"dialects"`
}

// DialectExpression is one dialect's rendering of an expression.
type DialectExpression struct {
	// +kubebuilder:validation:Enum=ANSI_SQL;SNOWFLAKE;MDX;TABLEAU;DATABRICKS;MAQL;STARROCKS
	Dialect    string `json:"dialect"`
	Expression string `json:"expression"`
}

// Dimension marks a field's dimensional metadata.
type Dimension struct {
	// +optional
	IsTime bool `json:"is_time,omitempty"`
}

// Relationship is an edge in the join graph: many side (from) to one side (to).
type Relationship struct {
	Name        string   `json:"name"`
	From        string   `json:"from"`
	To          string   `json:"to"`
	FromColumns []string `json:"from_columns"`
	ToColumns   []string `json:"to_columns"`
	// JoinType is an operator extension: INNER (default) or LEFT.
	// +kubebuilder:validation:Enum=INNER;LEFT
	// +optional
	JoinType string `json:"join_type,omitempty"`
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	// +optional
	AIContext *apiextensionsv1.JSON `json:"ai_context,omitempty"`
	// +optional
	CustomExtensions []CustomExtension `json:"custom_extensions,omitempty"`
}

// Metric is a model-level aggregate measure.
type Metric struct {
	Name       string     `json:"name"`
	Expression Expression `json:"expression"`
	// +optional
	Description string `json:"description,omitempty"`
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Schemaless
	// +optional
	AIContext *apiextensionsv1.JSON `json:"ai_context,omitempty"`
	// +optional
	CustomExtensions []CustomExtension `json:"custom_extensions,omitempty"`
}

// CustomExtension carries vendor-specific attributes as a JSON string.
type CustomExtension struct {
	VendorName string `json:"vendor_name"`
	Data       string `json:"data"`
}

// GovernanceSpec defines compile-time access policies keyed by role.
type GovernanceSpec struct {
	// DefaultRole applies when an adapter passes no identity.
	// +optional
	DefaultRole string `json:"defaultRole,omitempty"`
	Roles       []RolePolicy `json:"roles"`
}

// RolePolicy is the policy for one role.
type RolePolicy struct {
	Name string `json:"name"`
	// AllowMetrics is a list of metric-name globs (e.g. "*", "total_*").
	// Empty means no metrics are allowed for this role.
	// +optional
	AllowMetrics []string `json:"allowMetrics,omitempty"`
	// DenyFields lists dataset.field references this role may never read,
	// as a dimension, filter, or inside a metric expression.
	// +optional
	DenyFields []string `json:"denyFields,omitempty"`
	// RowFilters are predicates conjoined into WHERE at compile time.
	// +optional
	RowFilters []RowFilter `json:"rowFilters,omitempty"`
}

// RowFilter is a row-level predicate over one dataset.
type RowFilter struct {
	Dataset string `json:"dataset"`
	// Predicate is a SQL boolean expression over the dataset's physical
	// columns, e.g. "s_state = 'TX'".
	Predicate string `json:"predicate"`
}

// ViewSpec declares a governed metric view materialized in StarRocks.
type ViewSpec struct {
	Name    string   `json:"name"`
	Metrics []string `json:"metrics"`
	// +optional
	Dimensions []string `json:"dimensions,omitempty"`
	// +optional
	Filters []ViewFilter `json:"filters,omitempty"`
	// TimeGrain is one of day, week, month, quarter, year.
	// +kubebuilder:validation:Enum=day;week;month;quarter;year
	// +optional
	TimeGrain string `json:"timeGrain,omitempty"`
	// Role the view is compiled under. Defaults to governance.defaultRole.
	// +optional
	Role string `json:"role,omitempty"`
}

// ViewFilter is a static filter on a governed view.
type ViewFilter struct {
	Field string `json:"field"`
	// +kubebuilder:validation:Enum==;!=;<;<=;>;>=;IN;NOT IN;LIKE;BETWEEN
	Op string `json:"op"`
	// +optional
	Value string `json:"value,omitempty"`
	// +optional
	Values []string `json:"values,omitempty"`
	// ValueType forces literal rendering: auto (default), string, or number.
	// +kubebuilder:validation:Enum=auto;string;number
	// +optional
	ValueType string `json:"valueType,omitempty"`
}

// CatalogSyncSpec enables field-list refresh from the lake catalog.
type CatalogSyncSpec struct {
	Enabled bool `json:"enabled"`
	// +kubebuilder:validation:Enum=glue
	// +kubebuilder:default=glue
	// +optional
	Source string `json:"source,omitempty"`
	// Database in the catalog; defaults to spec.connection.database.
	// +optional
	Database string `json:"database,omitempty"`
}

// DatasetBinding reports the physical resolution of one dataset.
type DatasetBinding struct {
	Dataset string `json:"dataset"`
	Table   string `json:"table"`
	// +optional
	Drift string `json:"drift,omitempty"`
}

// SemanticModelStatus defines the observed state of SemanticModel.
type SemanticModelStatus struct {
	// ModelVersion is a content hash of the spec; it names the published artifact.
	// +optional
	ModelVersion string `json:"modelVersion,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	PublishedConfigMap string `json:"publishedConfigMap,omitempty"`
	// +optional
	Bindings []DatasetBinding `json:"bindings,omitempty"`
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=sm
// +kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.status.modelVersion`
// +kubebuilder:printcolumn:name="Validated",type=string,JSONPath=`.status.conditions[?(@.type=="Validated")].status`
// +kubebuilder:printcolumn:name="Published",type=string,JSONPath=`.status.conditions[?(@.type=="Published")].status`
// +kubebuilder:printcolumn:name="Drift",type=string,JSONPath=`.status.conditions[?(@.type=="DriftDetected")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// SemanticModel is the Schema for the semanticmodels API.
type SemanticModel struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   SemanticModelSpec   `json:"spec,omitempty"`
	Status SemanticModelStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// SemanticModelList contains a list of SemanticModel.
type SemanticModelList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SemanticModel `json:"items"`
}

func init() {
	SchemeBuilder.Register(&SemanticModel{}, &SemanticModelList{})
}
