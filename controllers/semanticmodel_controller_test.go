package controllers

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	semanticv1alpha1 "github.com/KubedAI/semantic-operator/api/v1alpha1"
	sr "github.com/KubedAI/semantic-operator/internal/dbclient"
	"github.com/KubedAI/semantic-operator/internal/emitter"
	_ "github.com/KubedAI/semantic-operator/internal/emitter/starrocks"
)

// fakeStarRocks serves canned DESC output and records DDL.
type fakeStarRocks struct {
	tables map[string][]sr.Column // "catalog.db.table" -> columns
	ddl    []string
}

func (f *fakeStarRocks) DescribeTable(_ context.Context, cat, db, tbl string) ([]sr.Column, error) {
	cols, ok := f.tables[cat+"."+db+"."+tbl]
	if !ok {
		return nil, &notFoundErr{}
	}
	return cols, nil
}

type notFoundErr struct{}

func (*notFoundErr) Error() string { return "Unknown table" }

func (f *fakeStarRocks) Exec(_ context.Context, sql string) error {
	f.ddl = append(f.ddl, sql)
	return nil
}

func (f *fakeStarRocks) Ping(context.Context) error { return nil }

func testScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := semanticv1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	return s
}

func demoCR() *semanticv1alpha1.SemanticModel {
	e := func(x string) semanticv1alpha1.Expression {
		return semanticv1alpha1.Expression{Dialects: []semanticv1alpha1.DialectExpression{{Dialect: "ANSI_SQL", Expression: x}}}
	}
	return &semanticv1alpha1.SemanticModel{
		ObjectMeta: metav1.ObjectMeta{Name: "retail", Namespace: "default"},
		Spec: semanticv1alpha1.SemanticModelSpec{
			Connection: semanticv1alpha1.ConnectionSpec{Catalog: "iceberg", Database: "osi_demo"},
			Ossie: semanticv1alpha1.OssieModel{
				Name: "retail_model",
				Datasets: []semanticv1alpha1.Dataset{
					{Name: "store_sales", Source: "store_sales", PrimaryKey: []string{"ss_item_sk", "ss_ticket_number"}, Fields: []semanticv1alpha1.Field{
						{Name: "ss_item_sk", Expression: e("ss_item_sk")},
						{Name: "ss_ext_sales_price", Expression: e("ss_ext_sales_price")},
					}},
					{Name: "item", Source: "item", PrimaryKey: []string{"i_item_sk"}, Fields: []semanticv1alpha1.Field{
						{Name: "i_item_sk", Expression: e("i_item_sk")},
						{Name: "i_category", Expression: e("i_category")},
					}},
				},
				Relationships: []semanticv1alpha1.Relationship{
					{Name: "sales_to_item", From: "store_sales", To: "item",
						FromColumns: []string{"ss_item_sk"}, ToColumns: []string{"i_item_sk"}},
				},
				Metrics: []semanticv1alpha1.Metric{
					{Name: "total_sales", Expression: e("SUM(store_sales.ss_ext_sales_price)")},
				},
			},
			Views: []semanticv1alpha1.ViewSpec{
				{Name: "sales_by_category", Metrics: []string{"total_sales"}, Dimensions: []string{"item.i_category"}},
			},
		},
	}
}

func healthyTables() map[string][]sr.Column {
	return map[string][]sr.Column{
		"iceberg.osi_demo.store_sales": {
			{Name: "ss_item_sk", Type: "bigint"},
			{Name: "ss_ticket_number", Type: "bigint"},
			{Name: "ss_ext_sales_price", Type: "decimal(7,2)"},
		},
		"iceberg.osi_demo.item": {
			{Name: "i_item_sk", Type: "bigint"},
			{Name: "i_category", Type: "varchar(50)"},
		},
	}
}

func reconcileOnce(t *testing.T, r *SemanticModelReconciler, name types.NamespacedName) {
	t.Helper()
	// First pass may stop after adding the finalizer; run twice.
	for i := 0; i < 2; i++ {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: name}); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
	}
}

func TestReconcileHappyPath(t *testing.T) {
	scheme := testScheme(t)
	cr := demoCR()
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cr).
		WithStatusSubresource(&semanticv1alpha1.SemanticModel{}).Build()
	srx := &fakeStarRocks{tables: healthyTables()}
	d, _ := emitter.Get("starrocks")
	r := &SemanticModelReconciler{Client: cl, DB: srx, Dialect: d}

	name := types.NamespacedName{Name: "retail", Namespace: "default"}
	reconcileOnce(t, r, name)

	var got semanticv1alpha1.SemanticModel
	if err := cl.Get(context.Background(), name, &got); err != nil {
		t.Fatal(err)
	}
	for _, cond := range []string{
		semanticv1alpha1.ConditionValidated,
		semanticv1alpha1.ConditionCompiled,
		semanticv1alpha1.ConditionPublished,
		semanticv1alpha1.ConditionViewsReady,
	} {
		if !meta.IsStatusConditionTrue(got.Status.Conditions, cond) {
			t.Errorf("condition %s not true: %+v", cond, got.Status.Conditions)
		}
	}
	if meta.IsStatusConditionTrue(got.Status.Conditions, semanticv1alpha1.ConditionDriftDetected) {
		t.Error("unexpected drift")
	}
	if got.Status.ModelVersion == "" || got.Status.PublishedConfigMap == "" {
		t.Errorf("status incomplete: %+v", got.Status)
	}

	var cm corev1.ConfigMap
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "sm-retail-compiled", Namespace: "default"}, &cm); err != nil {
		t.Fatalf("compiled ConfigMap missing: %v", err)
	}
	if !strings.Contains(cm.Data[semanticv1alpha1.CompiledModelKey], `"total_sales"`) {
		t.Error("artifact does not contain the metric")
	}

	// Governed view DDL executed against StarRocks.
	joined := strings.Join(srx.ddl, "\n---\n")
	if !strings.Contains(joined, "CREATE OR REPLACE VIEW `semantic_views`.`sales_by_category`") {
		t.Errorf("view DDL missing:\n%s", joined)
	}
}

func TestReconcileFlagsDriftAndBlocksPublish(t *testing.T) {
	scheme := testScheme(t)
	cr := demoCR()
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cr).
		WithStatusSubresource(&semanticv1alpha1.SemanticModel{}).Build()
	tables := healthyTables()
	delete(tables, "iceberg.osi_demo.item") // simulate dropped table
	srx := &fakeStarRocks{tables: tables}
	d, _ := emitter.Get("starrocks")
	r := &SemanticModelReconciler{Client: cl, DB: srx, Dialect: d}

	name := types.NamespacedName{Name: "retail", Namespace: "default"}
	reconcileOnce(t, r, name)

	var got semanticv1alpha1.SemanticModel
	if err := cl.Get(context.Background(), name, &got); err != nil {
		t.Fatal(err)
	}
	if !meta.IsStatusConditionTrue(got.Status.Conditions, semanticv1alpha1.ConditionDriftDetected) {
		t.Fatalf("expected DriftDetected=True: %+v", got.Status.Conditions)
	}
	var cm corev1.ConfigMap
	err := cl.Get(context.Background(), types.NamespacedName{Name: "sm-retail-compiled", Namespace: "default"}, &cm)
	if err == nil {
		t.Fatal("drift must block publication of the artifact")
	}
}

func TestReconcileInvalidSpecSetsValidatedFalse(t *testing.T) {
	scheme := testScheme(t)
	cr := demoCR()
	cr.Spec.Ossie.Metrics[0].Expression.Dialects[0].Expression = "SUM(bare_column)"
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cr).
		WithStatusSubresource(&semanticv1alpha1.SemanticModel{}).Build()
	d, _ := emitter.Get("starrocks")
	r := &SemanticModelReconciler{Client: cl, DB: &fakeStarRocks{tables: healthyTables()}, Dialect: d}

	name := types.NamespacedName{Name: "retail", Namespace: "default"}
	reconcileOnce(t, r, name)

	var got semanticv1alpha1.SemanticModel
	if err := cl.Get(context.Background(), name, &got); err != nil {
		t.Fatal(err)
	}
	c := meta.FindStatusCondition(got.Status.Conditions, semanticv1alpha1.ConditionValidated)
	if c == nil || c.Status != metav1.ConditionFalse {
		t.Fatalf("expected Validated=False, got %+v", c)
	}
}

func TestReconcileRehomesCompiledConfigMapOwner(t *testing.T) {
	scheme := testScheme(t)
	cr := demoCR()

	// Seed an existing compiled artifact that is owned by a different object, as
	// can happen during an API-group migration or delete/recreate of the logical
	// resource name.
	existing := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "sm-retail-compiled",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "semantic.osi.io/v1alpha1",
				Kind:       "SemanticModel",
				Name:       "retail",
				UID:        types.UID("old-owner-uid"),
				Controller: ptrBool(true),
			}},
			Labels: map[string]string{
				semanticv1alpha1.LabelVersion: "stale-version",
			},
		},
		Data: map[string]string{
			semanticv1alpha1.CompiledModelKey: `{"stale":"artifact"}`,
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cr, existing).
		WithStatusSubresource(&semanticv1alpha1.SemanticModel{}).Build()
	srx := &fakeStarRocks{tables: healthyTables()}
	d, _ := emitter.Get("starrocks")
	r := &SemanticModelReconciler{Client: cl, DB: srx, Dialect: d}

	name := types.NamespacedName{Name: "retail", Namespace: "default"}
	reconcileOnce(t, r, name)

	var got corev1.ConfigMap
	if err := cl.Get(context.Background(), types.NamespacedName{Name: "sm-retail-compiled", Namespace: "default"}, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.OwnerReferences) != 1 {
		t.Fatalf("expected exactly one ownerRef after rehome, got %+v", got.OwnerReferences)
	}
	ref := got.OwnerReferences[0]
	if ref.APIVersion != semanticv1alpha1.GroupVersion.String() {
		t.Fatalf("ownerRef apiVersion = %q, want %q", ref.APIVersion, semanticv1alpha1.GroupVersion.String())
	}
	if ref.Kind != "SemanticModel" || ref.Name != "retail" {
		t.Fatalf("ownerRef = %+v, want current SemanticModel owner", ref)
	}
	if ref.UID != cr.UID {
		t.Fatalf("ownerRef UID = %q, want current object UID %q", ref.UID, cr.UID)
	}
	if !strings.Contains(got.Data[semanticv1alpha1.CompiledModelKey], `"total_sales"`) {
		t.Fatal("expected stale compiled artifact to be replaced")
	}
}

func TestReconcileRehomesOwnerEvenWhenContentCurrent(t *testing.T) {
	scheme := testScheme(t)
	cr := demoCR()
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cr).
		WithStatusSubresource(&semanticv1alpha1.SemanticModel{}).Build()
	d, _ := emitter.Get("starrocks")
	r := &SemanticModelReconciler{Client: cl, DB: &fakeStarRocks{tables: healthyTables()}, Dialect: d}

	name := types.NamespacedName{Name: "retail", Namespace: "default"}
	reconcileOnce(t, r, name)

	// Rewrite the published ConfigMap's owner to a legacy-group object with a
	// stale UID, leaving labels and data untouched. This is the delete/recreate
	// (or API-group migration) case where the artifact content is already
	// current but the owner must still be rehomed, or GC collects the artifact.
	cmName := types.NamespacedName{Name: "sm-retail-compiled", Namespace: "default"}
	var cm corev1.ConfigMap
	if err := cl.Get(context.Background(), cmName, &cm); err != nil {
		t.Fatal(err)
	}
	cm.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: "semantic.osi.io/v1alpha1",
		Kind:       "SemanticModel",
		Name:       "retail",
		UID:        types.UID("old-owner-uid"),
		Controller: ptrBool(true),
	}}
	if err := cl.Update(context.Background(), &cm); err != nil {
		t.Fatal(err)
	}

	reconcileOnce(t, r, name)

	var got corev1.ConfigMap
	if err := cl.Get(context.Background(), cmName, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.OwnerReferences) != 1 {
		t.Fatalf("expected exactly one ownerRef after rehome, got %+v", got.OwnerReferences)
	}
	ref := got.OwnerReferences[0]
	if ref.APIVersion != semanticv1alpha1.GroupVersion.String() || ref.UID == types.UID("old-owner-uid") {
		t.Fatalf("ownerRef not rehomed to the current object: %+v", ref)
	}
}

func ptrBool(v bool) *bool { return &v }
