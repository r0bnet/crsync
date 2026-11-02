package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	corev1ac "k8s.io/client-go/applyconfigurations/core/v1"
	eventsv1ac "k8s.io/client-go/applyconfigurations/events/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

const (
	// maxEventNoteLength is the events.k8s.io/v1 limit for Event.note.
	maxEventNoteLength = 1024
)

// eventReconciler replays hub Events onto the source objects in spoke clusters.
// Events on composed resources reach the source by walking controller ownerReferences
// up to the hub copy; an Event whose chain can't be read is skipped.
type eventReconciler struct {
	hubClient client.Client
	// ownerReader reads owners with plain GETs, so RBAC only needs "get" on them.
	ownerReader client.Reader
	clusters    ClusterProvider
}

// NewEventReconciler returns an eventReconciler that replays hub Events to spokes.
func NewEventReconciler(hubClient client.Client, ownerReader client.Reader, clusters ClusterProvider) *eventReconciler {
	return &eventReconciler{hubClient: hubClient, ownerReader: ownerReader, clusters: clusters}
}

// SetupWithManager registers the event reconciler with the multicluster manager.
func (r *eventReconciler) SetupWithManager(mgr mcmanager.Manager) error {
	return mcbuilder.ControllerManagedBy(mgr).
		Named("crsync-events").
		For(&eventsv1.Event{}, mcbuilder.WithEngageWithLocalCluster(true), mcbuilder.WithEngageWithProviderClusters(false)).
		WithEventFilter(EventPredicate(FieldManager)).
		Complete(r)
}

// Reconcile replays a hub Event onto the spoke object its hub copy was synced from.
// Requests for a spoke that isn't engaged yet are retried.
func (r *eventReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	return retryNotEngaged(ctx, req, r.reconcile)
}

func (r *eventReconciler) reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues("event", req.NamespacedName)
	ctx = log.IntoContext(ctx, logger)

	ev := &eventsv1.Event{}
	err := r.hubClient.Get(ctx, req.NamespacedName, ev)
	if apierrors.IsNotFound(err) {
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("get event: %w", err)
	}

	ns := &corev1.Namespace{}
	err = r.hubClient.Get(ctx, client.ObjectKey{Name: ev.Namespace}, ns)
	if apierrors.IsNotFound(err) {
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("get event namespace: %w", err)
	}
	if _, ok := ns.Labels[LabelSourceCluster]; !ok {
		return ctrl.Result{}, nil
	}

	regarding := schema.FromAPIVersionAndKind(ev.Regarding.APIVersion, ev.Regarding.Kind)
	hubCopy, hops := findHubCopy(ctx, r.ownerReader, ev.Namespace, regarding, ev.Regarding.Name)
	if hubCopy == nil {
		return ctrl.Result{}, nil
	}

	labels := hubCopy.GetLabels()
	spokeCluster, err := r.clusters.GetCluster(ctx, ClusterNameFromLabel(labels[LabelSourceCluster]))
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("get spoke cluster %s: %w", labels[LabelSourceCluster], err)
	}
	spokeClient := spokeCluster.GetClient()

	source := &unstructured.Unstructured{}
	source.SetGroupVersionKind(hubCopy.GroupVersionKind())
	err = spokeClient.Get(ctx, client.ObjectKey{Namespace: labels[LabelSourceNamespace], Name: labels[LabelSourceName]}, source)
	if apierrors.IsNotFound(err) {
		return ctrl.Result{}, nil
	}
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("get source object on spoke: %w", err)
	}

	err = spokeClient.Apply(ctx, replayedEvent(ev, source, hops), client.FieldOwner(FieldManager), client.ForceOwnership)
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("replay event to spoke: %w", err)
	}
	logger.V(1).Info("replayed event to spoke", "source", client.ObjectKeyFromObject(source))
	return ctrl.Result{}, nil
}

// replayedEvent builds the spoke Event for a hub Event. Its name derives from the
// hub Event's UID, so repeated replays update the same spoke Event.
func replayedEvent(ev *eventsv1.Event, source *unstructured.Unstructured, hops int) *eventsv1ac.EventApplyConfiguration {
	sum := sha256.Sum256([]byte(ev.UID))
	name := fmt.Sprintf("%s.%s", source.GetName(), hex.EncodeToString(sum[:])[:16])

	note := ev.Note
	if hops > 0 {
		// Name only the kind of the composed resource; its name is a hub internal.
		note = ev.Regarding.Kind + ": " + note
	}
	if len(note) > maxEventNoteLength {
		note = note[:maxEventNoteLength]
	}

	controller := ev.ReportingController
	if controller == "" {
		controller = ev.DeprecatedSource.Component
	}
	if controller == "" {
		controller = FieldManager
	}
	instance := ev.ReportingInstance
	if instance == "" {
		instance = FieldManager
	}
	action := ev.Action
	if action == "" {
		action = ev.Reason
	}

	// Events created through the core/v1 API have no eventTime; the first timestamp is stable across updates.
	eventTime := ev.EventTime
	if eventTime.IsZero() {
		eventTime = metav1.NewMicroTime(ev.DeprecatedFirstTimestamp.Time)
	}

	ac := eventsv1ac.Event(name, source.GetNamespace()).
		WithEventTime(eventTime).
		WithReportingController(controller).
		WithReportingInstance(instance).
		WithAction(action).
		WithReason(ev.Reason).
		WithType(ev.Type).
		WithNote(note).
		WithRegarding(corev1ac.ObjectReference().
			WithAPIVersion(source.GetAPIVersion()).
			WithKind(source.GetKind()).
			WithNamespace(source.GetNamespace()).
			WithName(source.GetName()).
			WithUID(source.GetUID()))

	switch {
	case ev.Series != nil:
		ac.WithSeries(eventsv1ac.EventSeries().
			WithCount(ev.Series.Count).
			WithLastObservedTime(ev.Series.LastObservedTime))
	case ev.DeprecatedCount > 1:
		ac.WithSeries(eventsv1ac.EventSeries().
			WithCount(ev.DeprecatedCount).
			WithLastObservedTime(metav1.NewMicroTime(ev.DeprecatedLastTimestamp.Time)))
	}
	return ac
}
