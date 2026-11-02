package controller

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mccontroller "sigs.k8s.io/multicluster-runtime/pkg/controller"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

const (
	// LabelSourceCluster identifies the originating cluster of a synced resource.
	LabelSourceCluster = "crsync.r0bnet.github.io/source-cluster"
	// LabelSourceNamespace identifies the originating namespace of a synced resource.
	LabelSourceNamespace = "crsync.r0bnet.github.io/source-namespace"
	// LabelSourceName identifies the originating name of a synced resource.
	LabelSourceName = "crsync.r0bnet.github.io/source-name"
	// FinalizerHubCopyCleanup is added to spoke resources to ensure the hub copy is
	// removed before the source object is garbage-collected by Kubernetes.
	FinalizerHubCopyCleanup = "crsync.r0bnet.github.io/hub-copy-cleanup"
	// FieldManager is the server-side apply field manager crsync writes hub copies with.
	FieldManager = "crsync"
	// ReasonFieldConflict is the Event reason when a hub controller changed a field the spoke also sets.
	ReasonFieldConflict = "FieldConflict"
)

// errHubCopyPending is returned by reconcileDeletion when the hub copy still exists
// and the reconcile loop should requeue to wait for external finalizers to clear.
var errHubCopyPending = errors.New("hub copy not yet deleted")

// crsyncFromSpokeReconciler reconciles a single GVK across multiple clusters.
type crsyncFromSpokeReconciler struct {
	hubClient client.Client
	clusters  ClusterProvider
	gvk       schema.GroupVersionKind
	recorder  events.EventRecorder
}

// NewCRSyncFromSpokeReconciler returns a crsyncFromSpokeReconciler for the given GVK.
// The recorder emits Events on hub copies, e.g. for field conflicts.
func NewCRSyncFromSpokeReconciler(hubClient client.Client, clusters ClusterProvider, gvk schema.GroupVersionKind, recorder events.EventRecorder) *crsyncFromSpokeReconciler {
	return &crsyncFromSpokeReconciler{hubClient: hubClient, clusters: clusters, gvk: gvk, recorder: recorder}
}

// SetupWithManager registers the spoke reconciler with the multicluster manager.
func (r *crsyncFromSpokeReconciler) SetupWithManager(mgr mcmanager.Manager, maxConcurrentReconciles int) error {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(r.gvk)

	hubObj := &unstructured.Unstructured{}
	hubObj.SetGroupVersionKind(r.gvk)

	return mcbuilder.ControllerManagedBy(mgr).
		Named(fmt.Sprintf("crsync-spoke-%s-%s-%s", r.gvk.Group, r.gvk.Version, r.gvk.Kind)).
		For(obj, mcbuilder.WithPredicates(IgnoreStatusChange())). // default is to engage only with provider clusters
		// A hub copy deleted out of band, e.g. with its namespace, is recreated from its source.
		Watches(hubObj, hubCopyDeleteToSpokeRequest,
			mcbuilder.WithEngageWithLocalCluster(true),
			mcbuilder.WithEngageWithProviderClusters(false),
			mcbuilder.WithPredicates(predicate.Funcs{
				CreateFunc:  func(event.CreateEvent) bool { return false },
				UpdateFunc:  func(event.UpdateEvent) bool { return false },
				DeleteFunc:  func(event.DeleteEvent) bool { return true },
				GenericFunc: func(event.GenericEvent) bool { return false },
			})).
		WithOptions(mccontroller.Options{
			MaxConcurrentReconciles: maxConcurrentReconciles,
		}).
		Complete(r)
}

// hubCopyDeleteToSpokeRequest maps a hub copy back to its source on the spoke.
func hubCopyDeleteToSpokeRequest(_ multicluster.ClusterName, _ cluster.Cluster) handler.TypedEventHandler[client.Object, mcreconcile.Request] {
	return handler.TypedEnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []mcreconcile.Request {
		return hubCopyToSpokeRequests(obj)
	})
}

func hubCopyToSpokeRequests(obj client.Object) []mcreconcile.Request {
	labels := obj.GetLabels()
	if labels[LabelSourceCluster] == "" || labels[LabelSourceNamespace] == "" || labels[LabelSourceName] == "" {
		return nil
	}
	return []mcreconcile.Request{{
		ClusterName: ClusterNameFromLabel(labels[LabelSourceCluster]),
		Request: reconcile.Request{
			NamespacedName: types.NamespacedName{Namespace: labels[LabelSourceNamespace], Name: labels[LabelSourceName]},
		},
	}}
}

// Reconcile handles a reconcile request from any engaged cluster.
func (r *crsyncFromSpokeReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	clusterName := string(req.ClusterName)
	hubNS, err := hubNamespace(clusterName, req.Namespace)
	if err != nil {
		return ctrl.Result{}, reconcile.TerminalError(err)
	}

	logger := log.FromContext(ctx).WithValues(
		"cluster", clusterName,
		"gvk", r.gvk.String(),
		"source", "spoke",
		"namespacedName", req.NamespacedName,
		"hubNamespace", hubNS,
	)
	ctx = log.IntoContext(ctx, logger)

	logger.Info("reconciling resource from spoke cluster")

	// Obtain the spoke cluster client.
	spokeCluster, err := r.clusters.GetCluster(ctx, multicluster.ClusterName(clusterName))
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("get spoke cluster %s: %w", clusterName, err)
	}
	spokeClient := spokeCluster.GetClient()

	sourceObj := &unstructured.Unstructured{}
	sourceObj.SetGroupVersionKind(r.gvk)
	if err := spokeClient.Get(ctx, req.NamespacedName, sourceObj); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, r.deleteHubCopy(ctx, hubNS, req.Name)
		}
		return ctrl.Result{}, fmt.Errorf("get source resource from spoke: %w", err)
	}

	// Ensure the finalizer is present to guard against premature deletion of the source.
	if !controllerutil.ContainsFinalizer(sourceObj, FinalizerHubCopyCleanup) {
		controllerutil.AddFinalizer(sourceObj, FinalizerHubCopyCleanup)
		if err := spokeClient.Update(ctx, sourceObj); err != nil {
			return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
		}
		return ctrl.Result{}, nil
	}

	// Handle deletion: trigger removal of the hub copy and wait until it is fully gone
	// before releasing the spoke finalizer.
	if !sourceObj.GetDeletionTimestamp().IsZero() {
		if err := r.reconcileDeletion(ctx, spokeClient, sourceObj, hubNS); err != nil {
			if errors.Is(err, errHubCopyPending) {
				return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
			}
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}

	// Ensure the hub namespace exists, creating it on-demand with source labels.
	if err := r.ensureHubNamespace(ctx, hubNS, clusterName, req.Namespace); err != nil {
		return ctrl.Result{}, err
	}

	if err = r.upsertHubCopy(ctx, sourceObj, clusterName, hubNS, req.Name); err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("hub copy upserted successfully")
	return ctrl.Result{}, nil
}

// reconcileDeletion triggers removal of the hub copy and waits until it is fully gone
// before releasing the spoke finalizer. External finalizers on the hub copy mean
// deletion may not be immediate; it returns errHubCopyPending when the caller should requeue.
func (r *crsyncFromSpokeReconciler) reconcileDeletion(ctx context.Context, spokeClient client.Client, sourceObj *unstructured.Unstructured, hubNS string) error {
	if err := r.deleteHubCopy(ctx, hubNS, sourceObj.GetName()); err != nil {
		return fmt.Errorf("delete resource copy on hub: %w", err)
	}

	hubCopy := &unstructured.Unstructured{}
	hubCopy.SetGroupVersionKind(r.gvk)
	if err := r.hubClient.Get(ctx, client.ObjectKey{Namespace: hubNS, Name: sourceObj.GetName()}, hubCopy); err == nil {
		return errHubCopyPending
	} else if !apierrors.IsNotFound(err) {
		return fmt.Errorf("check hub copy removal: %w", err)
	}

	// Hub copy is gone — safe to release the spoke finalizer.
	controllerutil.RemoveFinalizer(sourceObj, FinalizerHubCopyCleanup)
	if err := spokeClient.Update(ctx, sourceObj); err != nil {
		return fmt.Errorf("remove finalizer: %w", err)
	}
	return nil
}

// upsertHubCopy server-side applies the hub copy of the given spoke resource.
// Fields only hub controllers set (e.g. Crossplane's spec.crossplane.compositionRef)
// are left alone. If a hub controller changed a field the spoke also sets, a
// Warning Event names it and the spoke wins via a forced apply.
func (r *crsyncFromSpokeReconciler) upsertHubCopy(ctx context.Context, sourceObj *unstructured.Unstructured, clusterName, hubNS, name string) error {
	target := buildTarget(sourceObj, r.gvk, clusterName, hubNS, name)
	applyConfig := client.ApplyConfigurationFromUnstructured(target)
	err := r.hubClient.Apply(ctx, applyConfig, client.FieldOwner(FieldManager))
	if apierrors.IsConflict(err) {
		// The conflict message names the fields and the managers that changed them.
		r.recorder.Eventf(target, nil, corev1.EventTypeWarning, ReasonFieldConflict, "Apply",
			"spoke value overrides a hub-side change: %v", err)
		err = r.hubClient.Apply(ctx, applyConfig, client.FieldOwner(FieldManager), client.ForceOwnership)
	}
	if err != nil {
		return fmt.Errorf("apply hub resource: %w", err)
	}
	return nil
}

// deleteHubCopy removes the derived resource from the hub cluster namespace.
func (r *crsyncFromSpokeReconciler) deleteHubCopy(ctx context.Context, hubNS, name string) error {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(r.gvk)
	obj.SetNamespace(hubNS)
	obj.SetName(name)

	if err := r.hubClient.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete hub copy: %w", err)
	}
	return nil
}

// buildTarget constructs an Unstructured resource for the hub by copying data
// fields (see dataFields) from the source and setting derivation metadata.
func buildTarget(source *unstructured.Unstructured, gvk schema.GroupVersionKind, clusterName, hubNS, name string) *unstructured.Unstructured {
	target := &unstructured.Unstructured{Object: dataFields(source)}
	target.SetGroupVersionKind(gvk)
	target.SetNamespace(hubNS)
	target.SetName(name)

	// Merge source labels with derivation labels.
	labels := make(map[string]string)
	maps.Copy(labels, source.GetLabels())

	labels[LabelSourceCluster] = clusterLabelValue(clusterName)
	labels[LabelSourceNamespace] = source.GetNamespace()
	labels[LabelSourceName] = source.GetName()

	target.SetLabels(labels)

	// Carry over annotations from the source.
	if ann := source.GetAnnotations(); len(ann) > 0 {
		target.SetAnnotations(ann)
	}

	return target
}

// dataFields deep-copies every top-level field except metadata and status: spec for
// most CRDs, data and binaryData for a ConfigMap.
func dataFields(obj *unstructured.Unstructured) map[string]any {
	out := map[string]any{}
	for k, v := range obj.Object {
		if k == "metadata" || k == "status" {
			continue
		}
		out[k] = runtime.DeepCopyJSONValue(v)
	}
	return out
}

// ensureHubNamespace ensures the hub namespace exists and carries the correct
// source labels. If the namespace is missing it is created. If it already
// exists but the labels have drifted they are corrected via an update, unless
// it belongs to a different cluster (an encoding collision).
func (r *crsyncFromSpokeReconciler) ensureHubNamespace(ctx context.Context, hubNS, clusterName, sourceNamespace string) error {
	ns := &corev1.Namespace{
		ObjectMeta: metav1.ObjectMeta{Name: hubNS},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, r.hubClient, ns, func() error {
		if ns.Labels == nil {
			ns.Labels = make(map[string]string)
		}
		clusterLabel := clusterLabelValue(clusterName)
		if owner, ok := ns.Labels[LabelSourceCluster]; ok && owner != clusterLabel {
			return fmt.Errorf("hub namespace %s belongs to cluster %q", hubNS, owner)
		}
		ns.Labels[LabelSourceCluster] = clusterLabel
		ns.Labels[LabelSourceNamespace] = sourceNamespace
		return nil
	})
	if err != nil {
		return fmt.Errorf("ensure hub namespace %s: %w", hubNS, err)
	}
	return nil
}
