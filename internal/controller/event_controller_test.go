package controller

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
)

const (
	eventTestHubNS     = "spoke1--default"
	eventTestSourceUID = types.UID("source-uid")
)

var crsyncNSLabels = map[string]string{LabelSourceCluster: "spoke1", LabelSourceNamespace: "default"}

// eventTestHubCopy returns a hub copy of default/my-db from spoke1.
func eventTestHubCopy() *unstructured.Unstructured {
	hubCopy := newSpokeResource(eventTestHubNS, "my-db", map[string]any{"size": "small"}, map[string]string{
		LabelSourceCluster:   "spoke1",
		LabelSourceNamespace: "default",
		LabelSourceName:      "my-db",
	})
	hubCopy.SetUID("hub-copy-uid")
	return hubCopy
}

// eventTestComposed returns a ConfigMap standing in for a composed resource, controlled by ownerName.
func eventTestComposed(ownerName string) *corev1.ConfigMap {
	return &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-db-x7k2p",
			Namespace: eventTestHubNS,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: testGVK.GroupVersion().String(),
				Kind:       testGVK.Kind,
				Name:       ownerName,
				UID:        "hub-copy-uid",
				Controller: ptr.To(true),
			}},
		},
	}
}

func eventTestHubEvent(regarding corev1.ObjectReference) *eventsv1.Event {
	return &eventsv1.Event{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "hub-event",
			Namespace: eventTestHubNS,
			UID:       "hub-event-uid",
		},
		EventTime:           metav1.NewMicroTime(time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)),
		Regarding:           regarding,
		Reason:              "CannotCreateExternalResource",
		Note:                "quota exceeded",
		Type:                corev1.EventTypeWarning,
		Action:              "Create",
		ReportingController: "managed/object.kubernetes.m.crossplane.io",
		ReportingInstance:   "provider-kubernetes-abc",
	}
}

func composedRef() corev1.ObjectReference {
	return corev1.ObjectReference{APIVersion: "v1", Kind: "ConfigMap", Namespace: eventTestHubNS, Name: "my-db-x7k2p"}
}

func hubCopyRef() corev1.ObjectReference {
	return corev1.ObjectReference{APIVersion: testGVK.GroupVersion().String(), Kind: testGVK.Kind, Namespace: eventTestHubNS, Name: "my-db"}
}

// newEventTestSetup builds hub and spoke clients plus an event reconciler for spoke1.
func newEventTestSetup(hubNSLabels map[string]string, hubObjects ...client.Object) (*eventReconciler, client.Client, *fakeClusterProvider) {
	source := newSpokeResource("default", "my-db", map[string]any{"size": "small"}, nil)
	source.SetUID(eventTestSourceUID)
	sc := spokeClient(source)
	hc := hubClientWith(append([]client.Object{newNamespace(eventTestHubNS, hubNSLabels)}, hubObjects...)...)
	clusters := &fakeClusterProvider{
		clusters: map[string]cluster.Cluster{"spoke1": &fakeCluster{client: sc}},
	}
	return NewEventReconciler(hc, hc, clusters), sc, clusters
}

func reconcileEvent(t *testing.T, r *eventReconciler) {
	t.Helper()
	_, err := r.Reconcile(context.Background(), newRequest("", eventTestHubNS, "hub-event"))
	if err != nil {
		t.Fatalf("unexpected reconcile error: %v", err)
	}
}

func spokeEvents(t *testing.T, sc client.Client) []eventsv1.Event {
	t.Helper()
	list := &eventsv1.EventList{}
	err := sc.List(context.Background(), list, client.InNamespace("default"))
	if err != nil {
		t.Fatalf("list spoke events: %v", err)
	}
	return list.Items
}

func TestEventReconcile_composedResourceEventReplayedToSource(t *testing.T) {
	r, sc, _ := newEventTestSetup(crsyncNSLabels, eventTestHubCopy(), eventTestComposed("my-db"), eventTestHubEvent(composedRef()))

	reconcileEvent(t, r)

	events := spokeEvents(t, sc)
	if len(events) != 1 {
		t.Fatalf("expected one replayed event on the spoke, got %d", len(events))
	}
	ev := events[0]
	if ev.Regarding.Name != "my-db" || ev.Regarding.UID != eventTestSourceUID || ev.Regarding.Kind != testGVK.Kind {
		t.Errorf("expected the event to regard the spoke source my-db (%s), got %+v", eventTestSourceUID, ev.Regarding)
	}
	if ev.Note != "ConfigMap: quota exceeded" {
		t.Errorf("expected note prefixed with the composed kind only, got %q", ev.Note)
	}
	if strings.Contains(ev.Note, "my-db-x7k2p") {
		t.Errorf("note leaks the composed resource's hub name: %q", ev.Note)
	}
	if ev.ReportingController != "managed/object.kubernetes.m.crossplane.io" || ev.Type != corev1.EventTypeWarning || ev.Reason != "CannotCreateExternalResource" {
		t.Errorf("expected controller, type and reason of the hub event, got %q %q %q", ev.ReportingController, ev.Type, ev.Reason)
	}
}

func TestEventReconcile_hubCopyEventReplayedWithoutPrefix(t *testing.T) {
	r, sc, _ := newEventTestSetup(crsyncNSLabels, eventTestHubCopy(), eventTestHubEvent(hubCopyRef()))

	reconcileEvent(t, r)

	events := spokeEvents(t, sc)
	if len(events) != 1 {
		t.Fatalf("expected one replayed event on the spoke, got %d", len(events))
	}
	if events[0].Note != "quota exceeded" {
		t.Errorf("expected the note unchanged for an event on the hub copy itself, got %q", events[0].Note)
	}
}

func TestEventReconcile_unreadableOwnerSkipsEvent(t *testing.T) {
	// The composed resource's controller doesn't exist (e.g. not readable), so the chain breaks.
	r, sc, clusters := newEventTestSetup(crsyncNSLabels, eventTestHubCopy(), eventTestComposed("gone"), eventTestHubEvent(composedRef()))

	reconcileEvent(t, r)

	if events := spokeEvents(t, sc); len(events) != 0 {
		t.Fatalf("expected no replayed event, got %d", len(events))
	}
	if len(clusters.requested) != 0 {
		t.Fatalf("expected no spoke lookup for a broken owner chain, got %v", clusters.requested)
	}
}

func TestEventReconcile_namespaceNotManagedByCrsyncSkipped(t *testing.T) {
	r, sc, clusters := newEventTestSetup(nil, eventTestHubCopy(), eventTestHubEvent(hubCopyRef()))

	reconcileEvent(t, r)

	if events := spokeEvents(t, sc); len(events) != 0 {
		t.Fatalf("expected no replayed event outside crsync namespaces, got %d", len(events))
	}
	if len(clusters.requested) != 0 {
		t.Fatalf("expected no spoke lookup outside crsync namespaces, got %v", clusters.requested)
	}
}

func TestEventReconcile_replayIsIdempotentAndTracksSeries(t *testing.T) {
	hubEvent := eventTestHubEvent(composedRef())
	r, sc, _ := newEventTestSetup(crsyncNSLabels, eventTestHubCopy(), eventTestComposed("my-db"), hubEvent)

	reconcileEvent(t, r)

	hubEvent.Series = &eventsv1.EventSeries{
		Count:            3,
		LastObservedTime: metav1.NewMicroTime(time.Date(2026, 11, 2, 10, 5, 0, 0, time.UTC)),
	}
	err := r.hubClient.Update(context.Background(), hubEvent)
	if err != nil {
		t.Fatalf("update hub event series: %v", err)
	}
	reconcileEvent(t, r)

	events := spokeEvents(t, sc)
	if len(events) != 1 {
		t.Fatalf("expected the replay to update one spoke event, got %d", len(events))
	}
	if events[0].Series == nil || events[0].Series.Count != 3 {
		t.Errorf("expected the spoke event series count 3, got %+v", events[0].Series)
	}
}
