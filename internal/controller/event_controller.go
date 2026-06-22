package controller

import (
	"context"

	eventsv1 "k8s.io/api/events/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

// eventReconciler reconciles events.k8s.io/v1 Events from the hub cluster,
// filtered to only those whose Regarding object matches a watched GVK.
type eventReconciler struct {
	watchedGVKs map[schema.GroupVersionKind]struct{}
}

// NewEventReconciler returns an eventReconciler watching the given GVKs.
func NewEventReconciler(watchedGVKs map[schema.GroupVersionKind]struct{}) *eventReconciler {
	return &eventReconciler{watchedGVKs: watchedGVKs}
}

// SetupWithManager registers the event reconciler with the multicluster manager.
func (r *eventReconciler) SetupWithManager(mgr mcmanager.Manager) error {
	return mcbuilder.ControllerManagedBy(mgr).
		Named("crsync-events").
		For(&eventsv1.Event{}, mcbuilder.WithEngageWithLocalCluster(true), mcbuilder.WithEngageWithProviderClusters(false)).
		WithEventFilter(EventPredicate(r.watchedGVKs)).
		Complete(r)
}

// Reconcile handles an Event. Only events originating from the hub cluster
// are processed; spoke-cluster events are skipped immediately.
func (r *eventReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues(
		"cluster", req.ClusterName,
		"event", req.NamespacedName,
	)
	logger.Info("reconciling hub event")

	// TODO: implement event reconciliation logic
	return ctrl.Result{}, nil
}
