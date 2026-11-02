package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
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
	// AnnotationWriteBack marks a hub object for write-back when no owner chain leads to a hub copy.
	AnnotationWriteBack = "crsync.r0bnet.github.io/write-back"
	// LabelWriteBackCopy marks objects crsync wrote back; crsync never touches objects without it.
	LabelWriteBackCopy = "crsync.r0bnet.github.io/write-back-copy"
	// ReasonWriteBackConflict is recorded when an object crsync doesn't manage blocks a copy.
	ReasonWriteBackConflict = "WriteBackConflict"
	// ReasonWriteBackTypeMissing is recorded when the workload cluster doesn't serve the type.
	ReasonWriteBackTypeMissing = "WriteBackTypeMissing"

	writeBackTypeRetry = 5 * time.Minute
)

// writeBackReconciler copies hub objects of one GVK that belong to a hub copy (by owner
// chain or AnnotationWriteBack) into the source namespace on the workload cluster.
type writeBackReconciler struct {
	hubClient client.Client
	// ownerReader reads owners with plain GETs, so RBAC only needs "get" on them.
	ownerReader client.Reader
	clusters    ClusterProvider
	gvk         schema.GroupVersionKind
	recorder    events.EventRecorder
}

// writeBackTarget is where a hub object is copied to. source is nil for annotated objects.
type writeBackTarget struct {
	cluster   string
	namespace string
	source    *unstructured.Unstructured
}

// NewWriteBackReconciler returns a writeBackReconciler for the given GVK.
func NewWriteBackReconciler(hubClient client.Client, ownerReader client.Reader, clusters ClusterProvider, gvk schema.GroupVersionKind, recorder events.EventRecorder) *writeBackReconciler {
	return &writeBackReconciler{hubClient: hubClient, ownerReader: ownerReader, clusters: clusters, gvk: gvk, recorder: recorder}
}

// SetupWithManager watches the GVK on the hub, and crsync's copies on workload clusters
// so that edited, deleted or orphaned copies get repaired.
func (r *writeBackReconciler) SetupWithManager(mgr mcmanager.Manager, maxConcurrentReconciles int) error {
	hubObj := &unstructured.Unstructured{}
	hubObj.SetGroupVersionKind(r.gvk)
	spokeObj := &unstructured.Unstructured{}
	spokeObj.SetGroupVersionKind(r.gvk)

	return mcbuilder.ControllerManagedBy(mgr).
		Named(fmt.Sprintf("crsync-writeback-%s-%s-%s", r.gvk.Group, r.gvk.Version, r.gvk.Kind)).
		For(hubObj, mcbuilder.WithEngageWithLocalCluster(true), mcbuilder.WithEngageWithProviderClusters(false)).
		Watches(spokeObj, writeBackCopyToHubRequest, mcbuilder.WithPredicates(predicate.NewPredicateFuncs(isWriteBackCopy))).
		WithOptions(mccontroller.Options{
			MaxConcurrentReconciles: maxConcurrentReconciles,
		}).
		Complete(r)
}

func isWriteBackCopy(obj client.Object) bool {
	return obj.GetLabels()[LabelWriteBackCopy] == "true"
}

// writeBackCopyToHubRequest maps a copy on a workload cluster to its hub object.
func writeBackCopyToHubRequest(clusterName multicluster.ClusterName, _ cluster.Cluster) handler.TypedEventHandler[client.Object, mcreconcile.Request] {
	return handler.TypedEnqueueRequestsFromMapFunc(
		func(ctx context.Context, obj client.Object) []mcreconcile.Request {
			hubNS, err := hubNamespace(string(clusterName), obj.GetNamespace())
			if err != nil {
				log.FromContext(ctx).Error(err, "dropping write-back copy event")
				return nil
			}
			return []mcreconcile.Request{{
				Request: reconcile.Request{NamespacedName: types.NamespacedName{Namespace: hubNS, Name: obj.GetName()}},
			}}
		},
	)
}

// Reconcile writes a hub object back to the workload cluster, or deletes the copy once
// the hub object is gone.
func (r *writeBackReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("gvk", r.gvk.String(), "hubObject", req.NamespacedName)
	ctx = log.IntoContext(ctx, logger)

	hubObj := &unstructured.Unstructured{}
	hubObj.SetGroupVersionKind(r.gvk)
	err := r.hubClient.Get(ctx, req.NamespacedName, hubObj)
	if apierrors.IsNotFound(err) {
		if err := r.deleteCopy(ctx, req.Namespace, req.Name); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("get hub object: %w", err)
	}

	target, err := r.resolveTarget(ctx, hubObj)
	if err != nil {
		return ctrl.Result{}, err
	}
	if target == nil {
		return ctrl.Result{}, nil
	}

	spokeCluster, err := r.clusters.GetCluster(ctx, clusterNameFromLabel(target.cluster))
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("get spoke cluster %s: %w", target.cluster, err)
	}
	spokeClient := spokeCluster.GetClient()

	_, err = spokeClient.RESTMapper().RESTMapping(r.gvk.GroupKind(), r.gvk.Version)
	if meta.IsNoMatchError(err) {
		r.warn(ctx, spokeClient, hubObj, target, ReasonWriteBackTypeMissing,
			fmt.Sprintf("%s is not served by the workload cluster, %s can't be copied", r.gvk.String(), hubObj.GetName()))
		return ctrl.Result{RequeueAfter: writeBackTypeRetry}, nil
	}
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("check %s on spoke: %w", r.gvk.String(), err)
	}

	// The spoke cache only holds crsync's copies, so check for foreign objects uncached.
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(r.gvk)
	err = spokeCluster.GetAPIReader().Get(ctx, client.ObjectKey{Namespace: target.namespace, Name: hubObj.GetName()}, existing)
	if err == nil && !isWriteBackCopy(existing) {
		r.warn(ctx, spokeClient, hubObj, target, ReasonWriteBackConflict,
			fmt.Sprintf("%s %s already exists and isn't managed by crsync, so it is left unchanged", r.gvk.Kind, hubObj.GetName()))
		return ctrl.Result{}, nil
	}
	if err != nil && !apierrors.IsNotFound(err) {
		return ctrl.Result{}, fmt.Errorf("get existing copy: %w", err)
	}

	copied := writeBackCopy(hubObj, target)
	err = spokeClient.Apply(ctx, client.ApplyConfigurationFromUnstructured(copied), client.FieldOwner(FieldManager), client.ForceOwnership)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("apply copy to spoke: %w", err)
	}
	logger.V(1).Info("wrote back to spoke", "cluster", target.cluster, "namespace", target.namespace)
	return ctrl.Result{}, nil
}

// resolveTarget returns where hubObj is copied to, or nil if it isn't selected. An owner
// chain to a hub copy wins over AnnotationWriteBack, because it also identifies the source.
func (r *writeBackReconciler) resolveTarget(ctx context.Context, hubObj *unstructured.Unstructured) (*writeBackTarget, error) {
	hubCopy, hops := findHubCopy(ctx, r.ownerReader, hubObj.GetNamespace(), r.gvk, hubObj.GetName())
	// hops == 0 means hubObj is a hub copy itself, which is synced, not written back.
	if hubCopy != nil && hops > 0 {
		return r.ownedTarget(ctx, hubCopy)
	}
	if hubObj.GetAnnotations()[AnnotationWriteBack] != "true" {
		return nil, nil
	}

	clusterLabel, sourceNamespace, err := r.hubNamespaceSource(ctx, hubObj.GetNamespace())
	if err != nil || clusterLabel == "" {
		return nil, err
	}
	return &writeBackTarget{cluster: clusterLabel, namespace: sourceNamespace}, nil
}

// ownedTarget targets the source of hubCopy; it returns nil while the source doesn't exist.
func (r *writeBackReconciler) ownedTarget(ctx context.Context, hubCopy *unstructured.Unstructured) (*writeBackTarget, error) {
	labels := hubCopy.GetLabels()
	spokeCluster, err := r.clusters.GetCluster(ctx, clusterNameFromLabel(labels[LabelSourceCluster]))
	if err != nil {
		return nil, fmt.Errorf("get spoke cluster %s: %w", labels[LabelSourceCluster], err)
	}

	source := &unstructured.Unstructured{}
	source.SetGroupVersionKind(hubCopy.GroupVersionKind())
	err = spokeCluster.GetClient().Get(ctx, client.ObjectKey{Namespace: labels[LabelSourceNamespace], Name: labels[LabelSourceName]}, source)
	if apierrors.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get source object on spoke: %w", err)
	}
	return &writeBackTarget{cluster: labels[LabelSourceCluster], namespace: source.GetNamespace(), source: source}, nil
}

// hubNamespaceSource returns the source cluster label and namespace of a crsync hub
// namespace, or empty strings if it isn't one or is being deleted.
func (r *writeBackReconciler) hubNamespaceSource(ctx context.Context, name string) (string, string, error) {
	ns := &corev1.Namespace{}
	err := r.hubClient.Get(ctx, client.ObjectKey{Name: name}, ns)
	if apierrors.IsNotFound(err) {
		return "", "", nil
	}
	if err != nil {
		return "", "", fmt.Errorf("get hub namespace %s: %w", name, err)
	}
	if !ns.DeletionTimestamp.IsZero() {
		return "", "", nil
	}
	return ns.Labels[LabelSourceCluster], ns.Labels[LabelSourceNamespace], nil
}

// deleteCopy deletes crsync's copy of a hub object that is gone. Objects crsync doesn't
// manage are left alone.
func (r *writeBackReconciler) deleteCopy(ctx context.Context, hubNamespaceName, name string) error {
	clusterLabel, sourceNamespace, err := r.hubNamespaceSource(ctx, hubNamespaceName)
	if err != nil || clusterLabel == "" || sourceNamespace == "" {
		return err
	}

	spokeCluster, err := r.clusters.GetCluster(ctx, clusterNameFromLabel(clusterLabel))
	if err != nil {
		return fmt.Errorf("get spoke cluster %s: %w", clusterLabel, err)
	}

	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(r.gvk)
	err = spokeCluster.GetAPIReader().Get(ctx, client.ObjectKey{Namespace: sourceNamespace, Name: name}, existing)
	if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get copy on spoke: %w", err)
	}
	if !isWriteBackCopy(existing) {
		return nil
	}

	err = spokeCluster.GetClient().Delete(ctx, existing, client.Preconditions{UID: ptr.To(existing.GetUID())})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete copy on spoke: %w", err)
	}
	log.FromContext(ctx).V(1).Info("deleted write-back copy", "cluster", clusterLabel, "namespace", sourceNamespace)
	return nil
}

// warn tells the user on the workload cluster when there is a source object; annotated
// objects have none, so the Warning goes on the hub object instead.
func (r *writeBackReconciler) warn(ctx context.Context, spokeClient client.Client, hubObj *unstructured.Unstructured, target *writeBackTarget, reason, note string) {
	if target.source == nil {
		r.recorder.Eventf(hubObj, nil, corev1.EventTypeWarning, reason, "WriteBack", note)
		return
	}

	ev := &eventsv1.Event{
		ObjectMeta:          metav1.ObjectMeta{UID: types.UID(string(hubObj.GetUID()) + "/" + reason)},
		EventTime:           metav1.NowMicro(),
		ReportingController: FieldManager,
		ReportingInstance:   FieldManager,
		Action:              "WriteBack",
		Reason:              reason,
		Type:                corev1.EventTypeWarning,
		Note:                note,
	}
	err := spokeClient.Apply(ctx, replayedEvent(ev, target.source, 0), client.FieldOwner(FieldManager), client.ForceOwnership)
	if err != nil {
		log.FromContext(ctx).Error(err, "failed to record warning on source", "reason", reason)
	}
}

// writeBackCopy builds the copy: every top-level field except metadata and status, plus
// crsync's label and an ownerReference to the source when there is one.
func writeBackCopy(hubObj *unstructured.Unstructured, target *writeBackTarget) *unstructured.Unstructured {
	out := &unstructured.Unstructured{Object: map[string]any{}}
	for k, v := range hubObj.Object {
		if k == "metadata" || k == "status" {
			continue
		}
		out.Object[k] = runtime.DeepCopyJSONValue(v)
	}
	out.SetName(hubObj.GetName())
	out.SetNamespace(target.namespace)
	out.SetLabels(map[string]string{LabelWriteBackCopy: "true"})
	if target.source != nil {
		out.SetOwnerReferences([]metav1.OwnerReference{{
			APIVersion: target.source.GetAPIVersion(),
			Kind:       target.source.GetKind(),
			Name:       target.source.GetName(),
			UID:        target.source.GetUID(),
		}})
	}
	return out
}
