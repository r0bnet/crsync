package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
)

var secretGVK = corev1.SchemeGroupVersion.WithKind("Secret")

// writeBackHubSecret returns a hub Secret in the hub namespace of spoke1/default.
func writeBackHubSecret(owned bool, annotations map[string]string) *corev1.Secret {
	s := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "my-db-conn",
			Namespace:   eventTestHubNS,
			UID:         "hub-secret-uid",
			Labels:      map[string]string{"crossplane.io/composite": "my-db"},
			Annotations: annotations,
		},
		Type: "connection.crossplane.io/v1alpha1",
		Data: map[string][]byte{"password": []byte("s3cr3t")},
	}
	if owned {
		s.OwnerReferences = eventTestComposed("my-db").OwnerReferences
	}
	return s
}

type writeBackTestSetup struct {
	r        *writeBackReconciler
	spoke    client.Client
	recorder *events.FakeRecorder
}

// spokeMapper maps the kinds the tests use on spoke1; secretServed toggles Secret.
func spokeMapper(secretServed bool) meta.RESTMapper {
	mapper := meta.NewDefaultRESTMapper(nil)
	mapper.Add(testGVK, meta.RESTScopeNamespace)
	mapper.Add(eventsv1.SchemeGroupVersion.WithKind("Event"), meta.RESTScopeNamespace)
	if secretServed {
		mapper.Add(secretGVK, meta.RESTScopeNamespace)
	}
	return mapper
}

// newWriteBackTestSetup builds a hub with the hub copy of default/my-db plus hubObjects,
// and spoke1 holding the source object plus spokeObjects.
func newWriteBackTestSetup(t *testing.T, secretServed bool, hubObjects []client.Object, spokeObjects ...client.Object) writeBackTestSetup {
	t.Helper()
	source := newSpokeResource("default", "my-db", map[string]any{"size": "small"}, nil)
	source.SetUID(eventTestSourceUID)

	sc := fake.NewClientBuilder().
		WithScheme(testScheme).
		WithRESTMapper(spokeMapper(secretServed)).
		WithObjects(append(spokeObjects, source)...).
		Build()

	hubObjects = append(hubObjects, newNamespace(eventTestHubNS, crsyncNSLabels), eventTestHubCopy())
	hc := hubClientWith(hubObjects...)
	recorder := events.NewFakeRecorder(10)
	clusters := &fakeClusterProvider{clusters: map[string]cluster.Cluster{"spoke1": &fakeCluster{client: sc}}}
	return writeBackTestSetup{
		r:        NewWriteBackReconciler(hc, hc, clusters, secretGVK, recorder),
		spoke:    sc,
		recorder: recorder,
	}
}

func (s writeBackTestSetup) reconcile(t *testing.T) {
	t.Helper()
	_, err := s.r.Reconcile(context.Background(), newRequest("", eventTestHubNS, "my-db-conn"))
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
}

func (s writeBackTestSetup) spokeCopy(t *testing.T) (*corev1.Secret, bool) {
	t.Helper()
	got := &corev1.Secret{}
	err := s.spoke.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "my-db-conn"}, got)
	if apierrors.IsNotFound(err) {
		return nil, false
	}
	if err != nil {
		t.Fatalf("get spoke copy: %v", err)
	}
	return got, true
}

func TestWriteBack_copiesOwnedObject(t *testing.T) {
	s := newWriteBackTestSetup(t, true, []client.Object{eventTestComposed("my-db"), writeBackHubSecret(true, nil)})
	s.reconcile(t)

	got, ok := s.spokeCopy(t)
	if !ok {
		t.Fatal("expected the owned Secret to be written back")
	}
	if string(got.Data["password"]) != "s3cr3t" || got.Type != "connection.crossplane.io/v1alpha1" {
		t.Errorf("copy content = type %q data %v, want the hub Secret's type and data", got.Type, got.Data)
	}
	wantLabels := map[string]string{LabelWriteBackCopy: "true"}
	if len(got.Labels) != len(wantLabels) || got.Labels[LabelWriteBackCopy] != "true" {
		t.Errorf("labels = %v, want only %v (hub labels must not leak)", got.Labels, wantLabels)
	}
	if len(got.OwnerReferences) != 1 || got.OwnerReferences[0].UID != eventTestSourceUID {
		t.Errorf("ownerReferences = %v, want one pointing at the source object", got.OwnerReferences)
	}
}

func TestWriteBack_copiesAnnotatedObjectWithoutOwner(t *testing.T) {
	s := newWriteBackTestSetup(t, true, []client.Object{writeBackHubSecret(false, map[string]string{AnnotationWriteBack: "true"})})
	s.reconcile(t)

	got, ok := s.spokeCopy(t)
	if !ok {
		t.Fatal("expected the annotated Secret to be written back")
	}
	if len(got.OwnerReferences) != 0 {
		t.Errorf("ownerReferences = %v, want none: an annotated object has no known source", got.OwnerReferences)
	}
	if len(got.Annotations) != 0 {
		t.Errorf("annotations = %v, want none copied", got.Annotations)
	}
}

func TestWriteBack_skipsUnselectedObjects(t *testing.T) {
	for name, annotations := range map[string]map[string]string{
		"no annotation":       nil,
		"annotation not true": {AnnotationWriteBack: "false"},
	} {
		t.Run(name, func(t *testing.T) {
			s := newWriteBackTestSetup(t, true, []client.Object{writeBackHubSecret(false, annotations)})
			s.reconcile(t)
			if _, ok := s.spokeCopy(t); ok {
				t.Error("expected no copy for an object without owner chain or write-back annotation")
			}
		})
	}
}

func TestWriteBack_refusesUnmanagedObject(t *testing.T) {
	userSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "my-db-conn", Namespace: "default"},
		Data:       map[string][]byte{"mine": []byte("keep")},
	}
	s := newWriteBackTestSetup(t, true, []client.Object{eventTestComposed("my-db"), writeBackHubSecret(true, nil)}, userSecret)
	s.reconcile(t)

	got, ok := s.spokeCopy(t)
	if !ok || string(got.Data["mine"]) != "keep" || got.Data["password"] != nil {
		t.Errorf("user Secret was changed: %v", got)
	}

	list := &eventsv1.EventList{}
	if err := s.spoke.List(context.Background(), list, client.InNamespace("default")); err != nil {
		t.Fatalf("list spoke events: %v", err)
	}
	if len(list.Items) != 1 || list.Items[0].Reason != ReasonWriteBackConflict || list.Items[0].Regarding.UID != eventTestSourceUID {
		t.Errorf("spoke events = %v, want one %s Warning on the source", list.Items, ReasonWriteBackConflict)
	}
}

func TestWriteBack_warnsAndRetriesWhenTypeMissing(t *testing.T) {
	s := newWriteBackTestSetup(t, false, []client.Object{writeBackHubSecret(false, map[string]string{AnnotationWriteBack: "true"})})

	result, err := s.r.Reconcile(context.Background(), newRequest("", eventTestHubNS, "my-db-conn"))
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.RequeueAfter != writeBackTypeRetry {
		t.Errorf("RequeueAfter = %v, want %v so a CRD installed later gets picked up", result.RequeueAfter, writeBackTypeRetry)
	}
	select {
	case ev := <-s.recorder.Events:
		if want := corev1.EventTypeWarning + " " + ReasonWriteBackTypeMissing; len(ev) < len(want) || ev[:len(want)] != want {
			t.Errorf("hub event = %q, want prefix %q", ev, want)
		}
	default:
		t.Error("expected a WriteBackTypeMissing Warning on the hub object")
	}
}

func TestWriteBack_deletesManagedCopyWhenHubObjectIsGone(t *testing.T) {
	managed := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name:      "my-db-conn",
		Namespace: "default",
		Labels:    map[string]string{LabelWriteBackCopy: "true"},
	}}
	s := newWriteBackTestSetup(t, true, nil, managed)
	s.reconcile(t)
	if _, ok := s.spokeCopy(t); ok {
		t.Error("expected the managed copy to be deleted once its hub object is gone")
	}
}

func TestWriteBack_keepsUnmanagedObjectWhenHubObjectIsGone(t *testing.T) {
	userSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "my-db-conn", Namespace: "default"}}
	s := newWriteBackTestSetup(t, true, nil, userSecret)
	s.reconcile(t)
	if _, ok := s.spokeCopy(t); !ok {
		t.Error("deleted a Secret crsync doesn't manage")
	}
}

func TestIsWriteBackCopy(t *testing.T) {
	if !isWriteBackCopy(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{LabelWriteBackCopy: "true"}}}) {
		t.Error("isWriteBackCopy rejected a labelled copy")
	}
	if isWriteBackCopy(&corev1.Secret{}) {
		t.Error("isWriteBackCopy accepted an unlabelled object")
	}
}
