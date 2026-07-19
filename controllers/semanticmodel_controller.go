// Package controllers reconciles SemanticModel resources: validate the Ossie
// document, bind and drift-check physical schema through StarRocks, compile
// deterministically, publish the artifact ConfigMap, and materialize
// governed views. Reconciliation is level-triggered and idempotent, so it
// behaves under ArgoCD.
package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	semanticv1alpha1 "github.com/KubedAI/ossie-semantic-operator/api/v1alpha1"
	"github.com/KubedAI/ossie-semantic-operator/internal/emitter"
	"github.com/KubedAI/ossie-semantic-operator/internal/ossie"
	"github.com/KubedAI/ossie-semantic-operator/internal/planner"
	"github.com/KubedAI/ossie-semantic-operator/internal/planner/expr"
	"github.com/KubedAI/ossie-semantic-operator/internal/serving/views"
	"github.com/KubedAI/ossie-semantic-operator/internal/starrocks"
)

const finalizer = "semantic.ossie.io/views-cleanup"

// StarRocksClient is the slice of the StarRocks client the controller needs;
// mocked in the smoke test.
type StarRocksClient interface {
	DescribeTable(ctx context.Context, catalog, database, table string) ([]starrocks.Column, error)
	Exec(ctx context.Context, sql string) error
	Ping(ctx context.Context) error
}

// SemanticModelReconciler reconciles SemanticModel objects.
type SemanticModelReconciler struct {
	client.Client
	StarRocks StarRocksClient
	Dialect   emitter.Dialect
	// ViewDatabase is the default database for governed views (Helm value).
	ViewDatabase string
	// ResyncPeriod is the drift-detection cadence. Default 5m.
	ResyncPeriod time.Duration
}

// +kubebuilder:rbac:groups=semantic.ossie.io,resources=semanticmodels,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=semantic.ossie.io,resources=semanticmodels/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=semantic.ossie.io,resources=semanticmodels/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete

// Reconcile drives a SemanticModel through Validated -> Compiled ->
// Published -> ViewsReady. Drift blocks publication of the new version; the
// previously published artifact keeps serving.
func (r *SemanticModelReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)
	var sm semanticv1alpha1.SemanticModel
	if err := r.Get(ctx, req.NamespacedName, &sm); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !sm.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, &sm)
	}
	if !controllerutil.ContainsFinalizer(&sm, finalizer) {
		controllerutil.AddFinalizer(&sm, finalizer)
		if err := r.Update(ctx, &sm); err != nil {
			return ctrl.Result{}, err
		}
	}

	version := planner.SpecVersion(&sm.Spec)
	sm.Status.ModelVersion = version
	sm.Status.ObservedGeneration = sm.Generation

	// 1. Validate the Ossie document and planner subset. Pure, no I/O.
	if err := ossie.ValidateSpec(&sm.Spec); err != nil {
		r.setCondition(&sm, semanticv1alpha1.ConditionValidated, metav1.ConditionFalse, "ValidationFailed", truncate(err.Error()))
		log.Error(err, "validation failed")
		return ctrl.Result{}, r.Status().Update(ctx, &sm)
	}
	r.setCondition(&sm, semanticv1alpha1.ConditionValidated, metav1.ConditionTrue, "OK", "Ossie document and planner subset checks passed")

	// 2. Compile. Pure; failures here are spec bugs, not infrastructure.
	compiled, err := planner.Compile(&sm.Spec, sm.Namespace, sm.Name)
	if err != nil {
		r.setCondition(&sm, semanticv1alpha1.ConditionCompiled, metav1.ConditionFalse, "CompileFailed", truncate(err.Error()))
		return ctrl.Result{}, r.Status().Update(ctx, &sm)
	}

	// 3. Bind and drift-check against live StarRocks. Connectivity failures
	// requeue with backoff; a missing table or column is drift.
	drift, bindings, err := r.bind(ctx, compiled)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("starrocks unreachable during bind: %w", err)
	}
	sm.Status.Bindings = bindings
	if len(drift) > 0 {
		r.setCondition(&sm, semanticv1alpha1.ConditionDriftDetected, metav1.ConditionTrue, "SchemaDrift", truncate(strings.Join(drift, "; ")))
		log.Info("schema drift detected, not publishing", "drift", drift)
		return ctrl.Result{RequeueAfter: r.resync()}, r.Status().Update(ctx, &sm)
	}
	r.setCondition(&sm, semanticv1alpha1.ConditionDriftDetected, metav1.ConditionFalse, "NoDrift", "physical schema matches bindings")
	r.setCondition(&sm, semanticv1alpha1.ConditionCompiled, metav1.ConditionTrue, "OK", "compiled model version "+version)

	// 4. Publish the compiled artifact.
	cmName, err := r.publish(ctx, &sm, compiled)
	if err != nil {
		r.setCondition(&sm, semanticv1alpha1.ConditionPublished, metav1.ConditionFalse, "PublishFailed", truncate(err.Error()))
		_ = r.Status().Update(ctx, &sm)
		return ctrl.Result{}, err
	}
	sm.Status.PublishedConfigMap = cmName
	r.setCondition(&sm, semanticv1alpha1.ConditionPublished, metav1.ConditionTrue, "OK", "published "+cmName)

	// 5. Governed views.
	if err := r.reconcileViews(ctx, &sm, compiled); err != nil {
		r.setCondition(&sm, semanticv1alpha1.ConditionViewsReady, metav1.ConditionFalse, "ViewsFailed", truncate(err.Error()))
		_ = r.Status().Update(ctx, &sm)
		return ctrl.Result{}, err
	}
	r.setCondition(&sm, semanticv1alpha1.ConditionViewsReady, metav1.ConditionTrue, "OK", fmt.Sprintf("%d views published", len(sm.Spec.Views)))

	if err := r.Status().Update(ctx, &sm); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: r.resync()}, nil
}

// bind introspects every dataset table and cross-checks relationship columns
// and identity-expression fields. It fills physical column types into the
// compiled artifact. Returns drift messages (not errors) per dataset.
func (r *SemanticModelReconciler) bind(ctx context.Context, compiled *planner.CompiledModel) ([]string, []semanticv1alpha1.DatasetBinding, error) {
	var drift []string
	var bindings []semanticv1alpha1.DatasetBinding
	tableCols := map[string]map[string]string{} // dataset -> column -> type

	for _, name := range compiled.DatasetOrder {
		ds := compiled.Datasets[name]
		fq := ds.Catalog + "." + ds.Database + "." + ds.Table
		b := semanticv1alpha1.DatasetBinding{Dataset: name, Table: fq}
		cols, err := r.StarRocks.DescribeTable(ctx, ds.Catalog, ds.Database, ds.Table)
		if err != nil {
			if perr := r.StarRocks.Ping(ctx); perr != nil {
				return nil, nil, perr // infrastructure, not drift
			}
			b.Drift = fmt.Sprintf("table not resolvable: %v", err)
			drift = append(drift, name+": "+b.Drift)
			bindings = append(bindings, b)
			continue
		}
		colTypes := map[string]string{}
		for _, c := range cols {
			colTypes[c.Name] = c.Type
		}
		tableCols[name] = colTypes

		var missing []string
		for _, fname := range ds.FieldOrder {
			f := ds.Fields[fname]
			if isBareIdent(f.Expr) {
				if t, ok := colTypes[f.Expr]; ok {
					f.Type = t
				} else {
					missing = append(missing, fname+" (column "+f.Expr+")")
				}
			}
		}
		if len(missing) > 0 {
			b.Drift = "missing columns: " + strings.Join(missing, ", ")
			drift = append(drift, name+": "+b.Drift)
		}
		bindings = append(bindings, b)
	}

	for _, rel := range compiled.Relationships {
		for _, c := range rel.FromColumns {
			if cols := tableCols[rel.From]; cols != nil {
				if _, ok := cols[c]; !ok {
					drift = append(drift, fmt.Sprintf("relationship %s: column %s missing on %s", rel.Name, c, rel.From))
				}
			}
		}
		for _, c := range rel.ToColumns {
			if cols := tableCols[rel.To]; cols != nil {
				if _, ok := cols[c]; !ok {
					drift = append(drift, fmt.Sprintf("relationship %s: column %s missing on %s", rel.Name, c, rel.To))
				}
			}
		}
	}

	// Governance row filters reference physical columns of their dataset. The
	// grammar was validated earlier; here we cross-check the referenced columns
	// against the DESC output so a typo (s_stat = 'TX') surfaces at reconcile,
	// not at query time.
	if compiled.Governance != nil {
		for _, role := range compiled.Governance.Roles {
			for _, rf := range role.RowFilters {
				cols, err := expr.ParsePredicate(rf.Predicate)
				if err != nil {
					continue // grammar errors are the validator's job
				}
				physical := tableCols[rf.Dataset]
				if physical == nil {
					continue // dataset itself already drifted or unknown
				}
				for _, c := range cols {
					if _, ok := physical[c]; !ok {
						drift = append(drift, fmt.Sprintf("governance role %s: rowFilter on %s references missing column %s", role.Name, rf.Dataset, c))
					}
				}
			}
		}
	}
	return drift, bindings, nil
}

// publish writes the compiled artifact to an owned, labeled ConfigMap. The
// content-addressed version makes this idempotent.
func (r *SemanticModelReconciler) publish(ctx context.Context, sm *semanticv1alpha1.SemanticModel, compiled *planner.CompiledModel) (string, error) {
	blob, err := json.Marshal(compiled)
	if err != nil {
		return "", err
	}
	name := "sm-" + sm.Name + "-compiled"
	var cm corev1.ConfigMap
	err = r.Get(ctx, types.NamespacedName{Namespace: sm.Namespace, Name: name}, &cm)
	switch {
	case apierrors.IsNotFound(err):
		cm = corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: sm.Namespace},
		}
		r.decorate(&cm, sm, compiled.Version, blob)
		if err := controllerutil.SetControllerReference(sm, &cm, r.Scheme()); err != nil {
			return "", err
		}
		return name, r.Create(ctx, &cm)
	case err != nil:
		return "", err
	default:
		// Rehome the artifact when the ConfigMap already exists but is still owned
		// by a prior SemanticModel object (for example after an API-group migration
		// or delete/recreate of the same logical resource name).
		for i := len(cm.OwnerReferences) - 1; i >= 0; i-- {
			ref := cm.OwnerReferences[i]
			if ref.Kind == "SemanticModel" && (ref.APIVersion == semanticv1alpha1.GroupVersion.String() || ref.APIVersion == "semantic.osi.io/v1alpha1") {
				cm.OwnerReferences = append(cm.OwnerReferences[:i], cm.OwnerReferences[i+1:]...)
			}
		}
		if err := controllerutil.SetControllerReference(sm, &cm, r.Scheme()); err != nil {
			return "", err
		}
		if cm.Labels[semanticv1alpha1.LabelVersion] == compiled.Version &&
			cm.Data[semanticv1alpha1.CompiledModelKey] == string(blob) {
			return name, nil // already current
		}
		r.decorate(&cm, sm, compiled.Version, blob)
		return name, r.Update(ctx, &cm)
	}
}

func (r *SemanticModelReconciler) decorate(cm *corev1.ConfigMap, sm *semanticv1alpha1.SemanticModel, version string, blob []byte) {
	if cm.Labels == nil {
		cm.Labels = map[string]string{}
	}
	cm.Labels["app.kubernetes.io/managed-by"] = semanticv1alpha1.ManagedByValue
	cm.Labels[semanticv1alpha1.LabelModel] = sm.Spec.Ossie.Name
	cm.Labels[semanticv1alpha1.LabelVersion] = version
	cm.Data = map[string]string{semanticv1alpha1.CompiledModelKey: string(blob)}
}

// reconcileViews publishes declared views and drops ones the operator
// created earlier that are no longer declared, tracked via annotation.
func (r *SemanticModelReconciler) reconcileViews(ctx context.Context, sm *semanticv1alpha1.SemanticModel, compiled *planner.CompiledModel) error {
	db := sm.Spec.Connection.ViewDatabase
	if db == "" {
		db = r.ViewDatabase
	}
	defaultRole := ""
	if sm.Spec.Governance != nil {
		defaultRole = sm.Spec.Governance.DefaultRole
	}
	created, err := views.Publish(ctx, r.StarRocks, compiled, r.Dialect, sm.Spec.Views, db, defaultRole)
	if err != nil {
		return err
	}

	var previous []string
	if raw := sm.Annotations[semanticv1alpha1.AnnotationOwnedViews]; raw != "" {
		_ = json.Unmarshal([]byte(raw), &previous)
	}
	current := map[string]bool{}
	for _, v := range created {
		current[v] = true
	}
	var stale []string
	for _, v := range previous {
		if !current[v] {
			stale = append(stale, v)
		}
	}
	if len(stale) > 0 {
		if err := views.Drop(ctx, r.StarRocks, r.Dialect, db, stale); err != nil {
			return err
		}
	}
	blob, _ := json.Marshal(created)
	if sm.Annotations == nil {
		sm.Annotations = map[string]string{}
	}
	if sm.Annotations[semanticv1alpha1.AnnotationOwnedViews] != string(blob) {
		sm.Annotations[semanticv1alpha1.AnnotationOwnedViews] = string(blob)
		if err := r.Update(ctx, sm); err != nil {
			return err
		}
	}
	return nil
}

// finalize drops operator-created views, then releases the finalizer. The
// owned ConfigMap is garbage-collected via the owner reference.
func (r *SemanticModelReconciler) finalize(ctx context.Context, sm *semanticv1alpha1.SemanticModel) (ctrl.Result, error) {
	if controllerutil.ContainsFinalizer(sm, finalizer) {
		var owned []string
		if raw := sm.Annotations[semanticv1alpha1.AnnotationOwnedViews]; raw != "" {
			_ = json.Unmarshal([]byte(raw), &owned)
		}
		db := sm.Spec.Connection.ViewDatabase
		if db == "" {
			db = r.ViewDatabase
		}
		if len(owned) > 0 {
			if err := views.Drop(ctx, r.StarRocks, r.Dialect, db, owned); err != nil {
				logf.FromContext(ctx).Error(err, "dropping views during finalize; continuing")
			}
		}
		controllerutil.RemoveFinalizer(sm, finalizer)
		if err := r.Update(ctx, sm); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

func (r *SemanticModelReconciler) setCondition(sm *semanticv1alpha1.SemanticModel, t string, s metav1.ConditionStatus, reason, msg string) {
	meta.SetStatusCondition(&sm.Status.Conditions, metav1.Condition{
		Type: t, Status: s, Reason: reason, Message: msg,
		ObservedGeneration: sm.Generation,
	})
}

func (r *SemanticModelReconciler) resync() time.Duration {
	if r.ResyncPeriod > 0 {
		return r.ResyncPeriod
	}
	return 5 * time.Minute
}

func isBareIdent(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func truncate(s string) string {
	const max = 1024
	if len(s) <= max {
		return s
	}
	return s[:max] + " ..."
}

// SetupWithManager registers the controller.
func (r *SemanticModelReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&semanticv1alpha1.SemanticModel{}).
		Owns(&corev1.ConfigMap{}).
		Complete(r)
}
