package controller

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mccontroller "sigs.k8s.io/multicluster-runtime/pkg/controller"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

// crsyncFromHubReconciler reconciles a single GVK across multiple clusters.
type crsyncFromHubReconciler struct {
	hubClient client.Client
	clusters  ClusterProvider
	gvk       schema.GroupVersionKind
}

// NewCRSyncFromHubReconciler returns a crsyncFromHubReconciler for the given GVK.
func NewCRSyncFromHubReconciler(hubClient client.Client, clusters ClusterProvider, gvk schema.GroupVersionKind) *crsyncFromHubReconciler {
	return &crsyncFromHubReconciler{hubClient: hubClient, clusters: clusters, gvk: gvk}
}

// SetupWithManager registers the hub reconciler with the multicluster manager.
func (r *crsyncFromHubReconciler) SetupWithManager(mgr mcmanager.Manager, maxConcurrentReconciles int) error {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(r.gvk)

	return mcbuilder.ControllerManagedBy(mgr).
		Named(fmt.Sprintf("crsync-hub-%s-%s-%s", r.gvk.Group, r.gvk.Version, r.gvk.Kind)).
		For(obj, mcbuilder.WithEngageWithLocalCluster(true), mcbuilder.WithEngageWithProviderClusters(false)).
		WithEventFilter(OnlyStatusChange()).
		WithOptions(mccontroller.Options{
			MaxConcurrentReconciles: maxConcurrentReconciles,
		}).
		Complete(r)
}

// Reconcile handles a reconcile request from the hub cluster. It copies the
// status of the hub copy back to the originating resource on the spoke cluster.
//
// The hub copy lives in a namespace named <cluster>--<namespace>. Its labels
// carry the source cluster (LabelSourceCluster), namespace (LabelSourceNamespace)
// and name (LabelSourceName) that identify the resource on the spoke.
// Requests for a spoke that isn't engaged yet are retried.
func (r *crsyncFromHubReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	return retryNotEngaged(ctx, req, r.reconcile)
}

func (r *crsyncFromHubReconciler) reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues(
		"gvk", r.gvk.String(),
		"source", "hub",
		"namespacedName", req.NamespacedName,
	)
	ctx = log.IntoContext(ctx, logger)

	logger.Info("reconciling custom resource from hub")

	hubCopy := &unstructured.Unstructured{}
	hubCopy.SetGroupVersionKind(r.gvk)
	if err := r.hubClient.Get(ctx, req.NamespacedName, hubCopy); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get hub copy: %w", err)
	}

	labels := hubCopy.GetLabels()
	clusterName := labels[LabelSourceCluster]
	sourceNamespace := labels[LabelSourceNamespace]
	sourceName := labels[LabelSourceName]
	if clusterName == "" || sourceNamespace == "" || sourceName == "" {
		logger.Info("hub copy missing source labels, skipping status sync")
		return ctrl.Result{}, nil
	}

	logger = logger.WithValues("cluster", clusterName)
	ctx = log.IntoContext(ctx, logger)

	spokeCluster, err := r.clusters.GetCluster(ctx, ClusterNameFromLabel(clusterName))
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("get spoke cluster %s: %w", clusterName, err)
	}
	spokeClient := spokeCluster.GetClient()

	sourceObj := &unstructured.Unstructured{}
	sourceObj.SetGroupVersionKind(r.gvk)
	if err := spokeClient.Get(ctx, client.ObjectKey{Namespace: sourceNamespace, Name: sourceName}, sourceObj); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get source resource from spoke: %w", err)
	}

	// Copy the status from the hub copy onto the spoke source object and update
	// the status subresource. Types without one answer NotFound and are skipped.
	// Keep crsync's own Accepted condition: the hub copy knows nothing about it.
	accepted := acceptedCondition(sourceObj)
	sourceObj.Object["status"] = hubCopy.Object["status"]
	if accepted != nil {
		setAcceptedCondition(sourceObj, accepted)
	}
	err = spokeClient.Status().Update(ctx, sourceObj)
	if apierrors.IsNotFound(err) {
		logger.Info("source resource gone or its type has no status subresource, skipping status sync")
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("update status on spoke: %w", err)
	}

	logger.Info("status synced to spoke", "sourceNamespace", sourceNamespace, "sourceName", sourceName)
	return ctrl.Result{}, nil
}
