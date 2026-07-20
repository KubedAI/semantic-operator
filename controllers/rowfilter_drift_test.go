package controllers

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	semanticv1alpha1 "github.com/KubedAI/semantic-operator/api/v1alpha1"
	"github.com/KubedAI/semantic-operator/internal/emitter"
)

// A governance row filter that references a physical column missing from the
// live table must surface as drift at reconcile, not as a query-time error.
func TestReconcileFlagsRowFilterColumnDrift(t *testing.T) {
	scheme := testScheme(t)
	cr := demoCR()
	cr.Spec.Governance = &semanticv1alpha1.GovernanceSpec{
		DefaultRole: "analyst",
		Roles: []semanticv1alpha1.RolePolicy{
			{Name: "analyst", AllowMetrics: []string{"*"},
				// typo: physical column is ss_ext_sales_price
				RowFilters: []semanticv1alpha1.RowFilter{{Dataset: "store_sales", Predicate: "ss_ext_sales_pric > 0"}}},
		},
	}
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
	if !meta.IsStatusConditionTrue(got.Status.Conditions, semanticv1alpha1.ConditionDriftDetected) {
		t.Fatalf("expected DriftDetected=True for row-filter column typo: %+v", got.Status.Conditions)
	}
	c := meta.FindStatusCondition(got.Status.Conditions, semanticv1alpha1.ConditionDriftDetected)
	if !strings.Contains(c.Message, "ss_ext_sales_pric") {
		t.Fatalf("drift message should name the missing column, got: %s", c.Message)
	}
}

// A row filter on an existing column must not produce drift.
func TestReconcileRowFilterOnExistingColumnIsClean(t *testing.T) {
	scheme := testScheme(t)
	cr := demoCR()
	cr.Spec.Governance = &semanticv1alpha1.GovernanceSpec{
		DefaultRole: "analyst",
		Roles: []semanticv1alpha1.RolePolicy{
			{Name: "analyst", AllowMetrics: []string{"*"},
				RowFilters: []semanticv1alpha1.RowFilter{{Dataset: "store_sales", Predicate: "ss_ext_sales_price > 0"}}},
		},
	}
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
	if meta.IsStatusConditionTrue(got.Status.Conditions, semanticv1alpha1.ConditionDriftDetected) {
		t.Fatalf("unexpected drift for valid row filter: %+v", got.Status.Conditions)
	}
	if !meta.IsStatusConditionTrue(got.Status.Conditions, semanticv1alpha1.ConditionPublished) {
		t.Fatalf("expected Published=True: %+v", got.Status.Conditions)
	}
}
