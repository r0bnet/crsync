package controller

import (
	"context"
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

// fakeClusterProvider implements ClusterProvider by mapping cluster names to
// pre-built fake clusters. It records every requested cluster name.
type fakeClusterProvider struct {
	clusters  map[string]cluster.Cluster
	requested []string
}

func (f *fakeClusterProvider) GetCluster(_ context.Context, name multicluster.ClusterName) (cluster.Cluster, error) {
	f.requested = append(f.requested, string(name))
	cl, ok := f.clusters[string(name)]
	if !ok {
		return nil, fmt.Errorf("cluster %q not found", name)
	}
	return cl, nil
}

// fakeCluster wraps a client.Client to satisfy cluster.Cluster.
type fakeCluster struct {
	cluster.Cluster
	client client.Client
}

func (fc *fakeCluster) GetClient() client.Client { return fc.client }

// GetAPIReader returns the same client: fake clients have no cache.
func (fc *fakeCluster) GetAPIReader() client.Reader { return fc.client }

var testGVK = schema.GroupVersionKind{Group: "test.crsync.r0bnet.github.io", Version: "v1", Kind: "TestResource"}

func newSpokeResource(namespace, name string, spec map[string]any, labels map[string]string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "test.crsync.r0bnet.github.io/v1",
			"kind":       "TestResource",
			"metadata": map[string]any{
				"name":      name,
				"namespace": namespace,
			},
		},
	}
	if spec != nil {
		obj.Object["spec"] = spec
	}
	if labels != nil {
		obj.SetLabels(labels)
	}
	return obj
}

func spokeClient(objects ...client.Object) client.Client {
	return fake.NewClientBuilder().WithScheme(testScheme).WithObjects(objects...).Build()
}

func hubClientWith(objects ...client.Object) client.Client {
	return fake.NewClientBuilder().WithScheme(testScheme).WithObjects(objects...).Build()
}

func newNamespace(name string, labels map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

func newRequest(clusterName, namespace, name string) mcreconcile.Request {
	return mcreconcile.Request{
		ClusterName: multicluster.ClusterName(clusterName),
		Request: reconcile.Request{
			NamespacedName: client.ObjectKey{Namespace: namespace, Name: name},
		},
	}
}

func TestReconcile_create(t *testing.T) {
	sourceCM := newSpokeResource("default", "my-cm", map[string]any{"key": "value"}, map[string]string{"app": "test"})
	sc := spokeClient(sourceCM)
	hc := hubClientWith(newNamespace("spoke1--default", map[string]string{
		LabelSourceCluster:   "spoke1",
		LabelSourceNamespace: "default",
	}))

	ctx := context.Background()

	r := NewCRSyncFromSpokeReconciler(hc, &fakeClusterProvider{
		clusters: map[string]cluster.Cluster{"spoke1": &fakeCluster{client: sc}},
	}, testGVK, events.NewFakeRecorder(10))

	// First reconcile: adds the finalizer and returns early.
	if _, err := r.Reconcile(ctx, newRequest("spoke1", "default", "my-cm")); err != nil {
		t.Fatalf("unexpected error on first reconcile: %v", err)
	}

	// Second reconcile: creates the hub copy.
	result, err := r.Reconcile(ctx, newRequest("spoke1", "default", "my-cm"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("unexpected requeue: %v", result)
	}

	// Verify hub resource was created.
	hub := &unstructured.Unstructured{}
	hub.SetGroupVersionKind(testGVK)
	if err := hc.Get(ctx, client.ObjectKey{Namespace: "spoke1--default", Name: "my-cm"}, hub); err != nil {
		t.Fatalf("expected hub resource to exist: %v", err)
	}

	// Verify labels.
	labels := hub.GetLabels()
	if labels[LabelSourceCluster] != "spoke1" {
		t.Errorf("expected source-cluster label = spoke1, got %q", labels[LabelSourceCluster])
	}
	if labels[LabelSourceNamespace] != "default" {
		t.Errorf("expected source-namespace label = default, got %q", labels[LabelSourceNamespace])
	}
	if labels[LabelSourceName] != "my-cm" {
		t.Errorf("expected source-name label = my-cm, got %q", labels[LabelSourceName])
	}
	if labels["app"] != "test" {
		t.Errorf("expected app label = test, got %q", labels["app"])
	}

	// Verify spec was copied.
	spec, ok := hub.Object["spec"].(map[string]any)
	if !ok {
		t.Fatalf("expected spec field in hub resource")
	}
	if spec["key"] != "value" {
		t.Errorf("expected spec.key = value, got %v", spec["key"])
	}
}

// ConfigMaps keep their data outside spec, so every field but metadata and status must reach the hub.
func TestReconcile_configMapDataCopied(t *testing.T) {
	cmGVK := corev1.SchemeGroupVersion.WithKind("ConfigMap")
	source := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata": map[string]any{
			"name":      "settings",
			"namespace": "default",
		},
		"data":       map[string]any{"color": "orange"},
		"binaryData": map[string]any{"logo": "aGk="},
		"immutable":  true,
	}}
	sc := spokeClient(source)
	hc := hubClientWith(newNamespace("spoke1--default", map[string]string{
		LabelSourceCluster:   "spoke1",
		LabelSourceNamespace: "default",
	}))
	ctx := context.Background()

	r := NewCRSyncFromSpokeReconciler(hc, &fakeClusterProvider{
		clusters: map[string]cluster.Cluster{"spoke1": &fakeCluster{client: sc}},
	}, cmGVK, events.NewFakeRecorder(10))

	// The first reconcile adds the finalizer, the second creates the hub copy.
	for range 2 {
		_, err := r.Reconcile(ctx, newRequest("spoke1", "default", "settings"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	hub := &corev1.ConfigMap{}
	err := hc.Get(ctx, client.ObjectKey{Namespace: "spoke1--default", Name: "settings"}, hub)
	if err != nil {
		t.Fatalf("expected hub ConfigMap to exist: %v", err)
	}
	if hub.Data["color"] != "orange" {
		t.Errorf("expected data.color = orange, got %v", hub.Data)
	}
	if string(hub.BinaryData["logo"]) != "hi" {
		t.Errorf("expected binaryData.logo = hi, got %v", hub.BinaryData)
	}
	if hub.Immutable == nil || !*hub.Immutable {
		t.Errorf("expected immutable = true, got %v", hub.Immutable)
	}
}

func TestReconcile_update(t *testing.T) {
	sourceCM := newSpokeResource("default", "my-cm", map[string]any{"key": "updated"}, nil)
	sc := spokeClient(sourceCM)

	ctx := context.Background()

	// Pre-create the hub copy with old spec.
	existingHub := newSpokeResource("spoke1--default", "my-cm", map[string]any{"key": "old"}, map[string]string{
		LabelSourceCluster:   "spoke1",
		LabelSourceNamespace: "default",
		LabelSourceName:      "my-cm",
	})
	hc := hubClientWith(newNamespace("spoke1--default", map[string]string{
		LabelSourceCluster:   "spoke1",
		LabelSourceNamespace: "default",
	}), existingHub)

	r := NewCRSyncFromSpokeReconciler(hc, &fakeClusterProvider{
		clusters: map[string]cluster.Cluster{"spoke1": &fakeCluster{client: sc}},
	}, testGVK, events.NewFakeRecorder(10))

	// First reconcile: adds the finalizer and returns early.
	if _, err := r.Reconcile(ctx, newRequest("spoke1", "default", "my-cm")); err != nil {
		t.Fatalf("unexpected error on first reconcile: %v", err)
	}

	// Second reconcile: updates the hub copy.
	result, err := r.Reconcile(ctx, newRequest("spoke1", "default", "my-cm"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("unexpected requeue: %v", result)
	}

	hub := &unstructured.Unstructured{}
	hub.SetGroupVersionKind(testGVK)
	if err := hc.Get(ctx, client.ObjectKey{Namespace: "spoke1--default", Name: "my-cm"}, hub); err != nil {
		t.Fatalf("expected hub resource to exist: %v", err)
	}

	// Verify spec was updated.
	spec, ok := hub.Object["spec"].(map[string]any)
	if !ok {
		t.Fatalf("expected spec field in hub resource")
	}
	if spec["key"] != "updated" {
		t.Errorf("expected spec.key = updated, got %v", spec["key"])
	}
}

func TestReconcile_namespaceCreatedOnDemand(t *testing.T) {
	sourceCM := newSpokeResource("default", "my-cm", map[string]any{"key": "value"}, nil)
	sc := spokeClient(sourceCM)
	hc := hubClientWith() // no namespace pre-created

	ctx := context.Background()

	r := NewCRSyncFromSpokeReconciler(hc, &fakeClusterProvider{
		clusters: map[string]cluster.Cluster{"spoke1": &fakeCluster{client: sc}},
	}, testGVK, events.NewFakeRecorder(10))

	// First reconcile: adds the finalizer and returns early.
	if _, err := r.Reconcile(ctx, newRequest("spoke1", "default", "my-cm")); err != nil {
		t.Fatalf("unexpected error on first reconcile: %v", err)
	}

	// Second reconcile: creates the namespace on-demand and the hub copy.
	result, err := r.Reconcile(ctx, newRequest("spoke1", "default", "my-cm"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("unexpected requeue: %v", result)
	}

	// Verify the namespace was created with correct labels.
	ns := &corev1.Namespace{}
	if err := hc.Get(ctx, client.ObjectKey{Name: "spoke1--default"}, ns); err != nil {
		t.Fatalf("expected namespace spoke1--default to exist: %v", err)
	}
	if ns.Labels[LabelSourceCluster] != "spoke1" {
		t.Errorf("expected label %s = spoke1, got %q", LabelSourceCluster, ns.Labels[LabelSourceCluster])
	}
	if ns.Labels[LabelSourceNamespace] != "default" {
		t.Errorf("expected label %s = default, got %q", LabelSourceNamespace, ns.Labels[LabelSourceNamespace])
	}

	// Verify hub resource was created.
	hub := &unstructured.Unstructured{}
	hub.SetGroupVersionKind(testGVK)
	if err := hc.Get(ctx, client.ObjectKey{Namespace: "spoke1--default", Name: "my-cm"}, hub); err != nil {
		t.Fatalf("expected hub resource to exist: %v", err)
	}
}

func TestReconcile_namespaceLabelDriftCorrected(t *testing.T) {
	sourceCM := newSpokeResource("default", "my-cm", map[string]any{"key": "value"}, nil)
	sc := spokeClient(sourceCM)
	// Namespace exists but labels are missing (simulating manual removal or drift).
	hc := hubClientWith(newNamespace("spoke1--default", nil))

	ctx := context.Background()

	r := NewCRSyncFromSpokeReconciler(hc, &fakeClusterProvider{
		clusters: map[string]cluster.Cluster{"spoke1": &fakeCluster{client: sc}},
	}, testGVK, events.NewFakeRecorder(10))

	// First reconcile: adds the finalizer and returns early.
	if _, err := r.Reconcile(ctx, newRequest("spoke1", "default", "my-cm")); err != nil {
		t.Fatalf("unexpected error on first reconcile: %v", err)
	}

	// Second reconcile: corrects labels and creates the hub copy.
	if _, err := r.Reconcile(ctx, newRequest("spoke1", "default", "my-cm")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the namespace labels were corrected.
	ns := &corev1.Namespace{}
	if err := hc.Get(ctx, client.ObjectKey{Name: "spoke1--default"}, ns); err != nil {
		t.Fatalf("expected namespace spoke1--default to exist: %v", err)
	}
	if ns.Labels[LabelSourceCluster] != "spoke1" {
		t.Errorf("expected label %s = spoke1, got %q", LabelSourceCluster, ns.Labels[LabelSourceCluster])
	}
	if ns.Labels[LabelSourceNamespace] != "default" {
		t.Errorf("expected label %s = default, got %q", LabelSourceNamespace, ns.Labels[LabelSourceNamespace])
	}
}

func TestReconcile_sourceDeleted(t *testing.T) {
	sc := spokeClient() // no source resource

	ctx := context.Background()

	// Hub copy exists.
	existingHub := newSpokeResource("spoke1--default", "my-cm", nil, map[string]string{
		LabelSourceCluster:   "spoke1",
		LabelSourceNamespace: "default",
		LabelSourceName:      "my-cm",
	})
	hc := hubClientWith(newNamespace("spoke1--default", map[string]string{
		LabelSourceCluster:   "spoke1",
		LabelSourceNamespace: "default",
	}), existingHub)

	r := NewCRSyncFromSpokeReconciler(hc, &fakeClusterProvider{
		clusters: map[string]cluster.Cluster{"spoke1": &fakeCluster{client: sc}},
	}, testGVK, events.NewFakeRecorder(10))

	result, err := r.Reconcile(ctx, newRequest("spoke1", "default", "my-cm"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("unexpected requeue: %v", result)
	}

	// Verify hub copy was deleted.
	hub := &unstructured.Unstructured{}
	hub.SetGroupVersionKind(testGVK)
	err = hc.Get(ctx, client.ObjectKey{Namespace: "spoke1--default", Name: "my-cm"}, hub)
	if err == nil {
		t.Fatal("expected hub resource to be deleted")
	}
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected NotFound, got: %v", err)
	}
}

func TestReconcile_sourceDeletedNoHubCopy(t *testing.T) {
	sc := spokeClient() // no source
	hc := hubClientWith(newNamespace("spoke1--default", map[string]string{
		LabelSourceCluster:   "spoke1",
		LabelSourceNamespace: "default",
	})) // no hub copy either

	r := NewCRSyncFromSpokeReconciler(hc, &fakeClusterProvider{
		clusters: map[string]cluster.Cluster{"spoke1": &fakeCluster{client: sc}},
	}, testGVK, events.NewFakeRecorder(10))

	result, err := r.Reconcile(context.Background(), newRequest("spoke1", "default", "my-cm"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("unexpected requeue: %v", result)
	}
}

func TestReconcile_clusterProfileNameEncoded(t *testing.T) {
	sourceCM := newSpokeResource("default", "my-cm", map[string]any{"key": "value"}, nil)
	sc := spokeClient(sourceCM)
	hc := hubClientWith()
	ctx := context.Background()

	r := NewCRSyncFromSpokeReconciler(hc, &fakeClusterProvider{
		clusters: map[string]cluster.Cluster{"fleet/spoke1": &fakeCluster{client: sc}},
	}, testGVK, events.NewFakeRecorder(10))

	// First reconcile adds the finalizer, the second creates namespace and hub copy.
	for i := range 2 {
		_, err := r.Reconcile(ctx, newRequest("fleet/spoke1", "default", "my-cm"))
		if err != nil {
			t.Fatalf("unexpected error on reconcile %d: %v", i+1, err)
		}
	}

	ns := &corev1.Namespace{}
	err := hc.Get(ctx, client.ObjectKey{Name: "fleet-spoke1--default"}, ns)
	if err != nil {
		t.Fatalf("expected namespace fleet-spoke1--default: %v", err)
	}
	if ns.Labels[LabelSourceCluster] != "fleet.spoke1" {
		t.Errorf("expected namespace label %s = fleet.spoke1, got %q", LabelSourceCluster, ns.Labels[LabelSourceCluster])
	}

	hub := &unstructured.Unstructured{}
	hub.SetGroupVersionKind(testGVK)
	err = hc.Get(ctx, client.ObjectKey{Namespace: "fleet-spoke1--default", Name: "my-cm"}, hub)
	if err != nil {
		t.Fatalf("expected hub copy: %v", err)
	}
	if hub.GetLabels()[LabelSourceCluster] != "fleet.spoke1" {
		t.Errorf("expected hub copy label %s = fleet.spoke1, got %q", LabelSourceCluster, hub.GetLabels()[LabelSourceCluster])
	}
}

func TestReconcile_hubNamespaceOfOtherClusterRejected(t *testing.T) {
	// "a-b/c" and "a/b-c" both encode to the hub namespace "a-b-c--default".
	sourceCM := newSpokeResource("default", "my-cm", map[string]any{"key": "value"}, nil)
	sc := spokeClient(sourceCM)
	hc := hubClientWith(newNamespace("a-b-c--default", map[string]string{
		LabelSourceCluster:   "a-b.c",
		LabelSourceNamespace: "default",
	}))
	ctx := context.Background()

	r := NewCRSyncFromSpokeReconciler(hc, &fakeClusterProvider{
		clusters: map[string]cluster.Cluster{"a/b-c": &fakeCluster{client: sc}},
	}, testGVK, events.NewFakeRecorder(10))

	// First reconcile only adds the finalizer.
	_, err := r.Reconcile(ctx, newRequest("a/b-c", "default", "my-cm"))
	if err != nil {
		t.Fatalf("unexpected error on first reconcile: %v", err)
	}

	_, err = r.Reconcile(ctx, newRequest("a/b-c", "default", "my-cm"))
	if err == nil {
		t.Fatal("expected error when the hub namespace belongs to another cluster")
	}

	hub := &unstructured.Unstructured{}
	hub.SetGroupVersionKind(testGVK)
	err = hc.Get(ctx, client.ObjectKey{Namespace: "a-b-c--default", Name: "my-cm"}, hub)
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected no hub copy in the other cluster's namespace, got err=%v", err)
	}
}

func TestReconcile_hubOwnedSpecFieldSurvivesResync(t *testing.T) {
	sourceCR := newSpokeResource("default", "my-cr", map[string]any{"size": "small", "tier": "dev"}, nil)
	sc := spokeClient(sourceCR)
	hc := hubClientWith()
	ctx := context.Background()
	key := client.ObjectKey{Namespace: "spoke1--default", Name: "my-cr"}

	r := NewCRSyncFromSpokeReconciler(hc, &fakeClusterProvider{
		clusters: map[string]cluster.Cluster{"spoke1": &fakeCluster{client: sc}},
	}, testGVK, events.NewFakeRecorder(10))
	reconcile := func() {
		t.Helper()
		_, err := r.Reconcile(ctx, newRequest("spoke1", "default", "my-cr"))
		if err != nil {
			t.Fatalf("unexpected reconcile error: %v", err)
		}
	}
	reconcile() // adds the finalizer
	reconcile() // creates the hub copy

	// A hub controller (like Crossplane) sets a field the spoke never sends, using a plain update.
	hub := &unstructured.Unstructured{}
	hub.SetGroupVersionKind(testGVK)
	err := hc.Get(ctx, key, hub)
	if err != nil {
		t.Fatalf("get hub copy: %v", err)
	}
	err = unstructured.SetNestedField(hub.Object, "default-composition", "spec", "crossplane", "compositionRef", "name")
	if err != nil {
		t.Fatalf("set hub-owned field: %v", err)
	}
	err = hc.Update(ctx, hub, client.FieldOwner("crossplane"))
	if err != nil {
		t.Fatalf("update hub copy as hub controller: %v", err)
	}

	// The user changes one field and removes another on the spoke.
	src := &unstructured.Unstructured{}
	src.SetGroupVersionKind(testGVK)
	err = sc.Get(ctx, client.ObjectKey{Namespace: "default", Name: "my-cr"}, src)
	if err != nil {
		t.Fatalf("get spoke source: %v", err)
	}
	src.Object["spec"] = map[string]any{"size": "large"}
	err = sc.Update(ctx, src)
	if err != nil {
		t.Fatalf("update spoke source: %v", err)
	}

	reconcile()

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(testGVK)
	err = hc.Get(ctx, key, got)
	if err != nil {
		t.Fatalf("get hub copy after resync: %v", err)
	}
	ref, _, _ := unstructured.NestedString(got.Object, "spec", "crossplane", "compositionRef", "name")
	if ref != "default-composition" {
		t.Errorf("hub-owned spec.crossplane.compositionRef.name was lost, got %q", ref)
	}
	size, _, _ := unstructured.NestedString(got.Object, "spec", "size")
	if size != "large" {
		t.Errorf("expected spec.size = large from the spoke, got %q", size)
	}
	_, found, _ := unstructured.NestedFieldNoCopy(got.Object, "spec", "tier")
	if found {
		t.Error("expected spec.tier removed on the hub after it was removed on the spoke")
	}
}

func TestReconcile_fieldConflictRecordsEventAndSpokeWins(t *testing.T) {
	sourceCR := newSpokeResource("default", "my-cr", map[string]any{"size": "small"}, nil)
	sc := spokeClient(sourceCR)
	hc := hubClientWith()
	ctx := context.Background()
	key := client.ObjectKey{Namespace: "spoke1--default", Name: "my-cr"}
	recorder := events.NewFakeRecorder(10)

	r := NewCRSyncFromSpokeReconciler(hc, &fakeClusterProvider{
		clusters: map[string]cluster.Cluster{"spoke1": &fakeCluster{client: sc}},
	}, testGVK, recorder)
	reconcile := func() {
		t.Helper()
		_, err := r.Reconcile(ctx, newRequest("spoke1", "default", "my-cr"))
		if err != nil {
			t.Fatalf("unexpected reconcile error: %v", err)
		}
	}
	reconcile() // adds the finalizer
	reconcile() // creates the hub copy
	reconcile() // resync without conflict
	if len(recorder.Events) != 0 {
		t.Fatalf("expected no events without a conflict, got %q", <-recorder.Events)
	}

	// A hub controller changes a field the spoke also sets.
	hub := &unstructured.Unstructured{}
	hub.SetGroupVersionKind(testGVK)
	err := hc.Get(ctx, key, hub)
	if err != nil {
		t.Fatalf("get hub copy: %v", err)
	}
	err = unstructured.SetNestedField(hub.Object, "medium", "spec", "size")
	if err != nil {
		t.Fatalf("set spec.size: %v", err)
	}
	err = hc.Update(ctx, hub, client.FieldOwner("hub-normalizer"))
	if err != nil {
		t.Fatalf("update hub copy as hub controller: %v", err)
	}

	reconcile()

	if len(recorder.Events) != 1 {
		t.Fatalf("expected exactly one event for the conflict, got %d", len(recorder.Events))
	}
	event := <-recorder.Events
	for _, want := range []string{"Warning", ReasonFieldConflict, ".spec.size", "hub-normalizer"} {
		if !strings.Contains(event, want) {
			t.Errorf("event %q does not mention %q", event, want)
		}
	}

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(testGVK)
	err = hc.Get(ctx, key, got)
	if err != nil {
		t.Fatalf("get hub copy after resync: %v", err)
	}
	size, _, _ := unstructured.NestedString(got.Object, "spec", "size")
	if size != "small" {
		t.Errorf("expected the spoke value spec.size = small to win, got %q", size)
	}
}

func TestHubCopyToSpokeRequests(t *testing.T) {
	hubCopy := newSpokeResource("fleet-spoke1--default", "orders", nil, map[string]string{
		LabelSourceCluster:   "fleet.spoke1",
		LabelSourceNamespace: "default",
		LabelSourceName:      "orders",
	})
	got := hubCopyToSpokeRequests(hubCopy)
	want := newRequest("fleet/spoke1", "default", "orders")
	if len(got) != 1 || got[0] != want {
		t.Errorf("requests = %v, want [%v]", got, want)
	}

	if got := hubCopyToSpokeRequests(newSpokeResource("other", "orders", nil, nil)); len(got) != 0 {
		t.Errorf("requests for an object without source labels = %v, want none", got)
	}
}
