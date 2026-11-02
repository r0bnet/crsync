package controller

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
)

func spokeClientWithStatus(objects ...client.Object) client.Client {
	return fake.NewClientBuilder().
		WithScheme(testScheme).
		WithObjects(objects...).
		WithStatusSubresource(objects...).
		Build()
}

func newHubCopy(namespace, derivedName string, status map[string]any, labels map[string]string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": testGVK.GroupVersion().String(),
			"kind":       testGVK.Kind,
			"metadata": map[string]any{
				"name":      derivedName,
				"namespace": namespace,
			},
		},
	}
	if status != nil {
		obj.Object["status"] = status
	}
	if labels != nil {
		obj.SetLabels(labels)
	}
	return obj
}

func TestHubReconcile_statusSync(t *testing.T) {
	sourceObj := newSpokeResource("default", "my-res", map[string]any{"key": "value"}, nil)
	hubCopy := newHubCopy("spoke1--default", "my-res",
		map[string]any{"phase": "Ready"},
		map[string]string{
			LabelSourceCluster:   "spoke1",
			LabelSourceNamespace: "default",
			LabelSourceName:      "my-res",
		},
	)

	sc := spokeClientWithStatus(sourceObj)
	hc := hubClientWith(hubCopy)
	ctx := context.Background()

	r := NewCRSyncFromHubReconciler(hc, &fakeClusterProvider{
		clusters: map[string]cluster.Cluster{"spoke1": &fakeCluster{client: sc}},
	}, testGVK)

	result, err := r.Reconcile(ctx, newRequest("", "spoke1--default", "my-res"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("unexpected requeue: %v", result)
	}

	// Verify status was synced to spoke source.
	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(testGVK)
	if err := sc.Get(ctx, client.ObjectKey{Namespace: "default", Name: "my-res"}, got); err != nil {
		t.Fatalf("get source: %v", err)
	}
	status, ok := got.Object["status"].(map[string]any)
	if !ok {
		t.Fatal("expected status field on spoke resource")
	}
	if status["phase"] != "Ready" {
		t.Errorf("expected status.phase = Ready, got %v", status["phase"])
	}
}

func TestHubReconcile_hubCopyNotFound(t *testing.T) {
	hc := hubClientWith() // no hub copy
	sc := spokeClient()
	ctx := context.Background()

	r := NewCRSyncFromHubReconciler(hc, &fakeClusterProvider{
		clusters: map[string]cluster.Cluster{"spoke1": &fakeCluster{client: sc}},
	}, testGVK)

	result, err := r.Reconcile(ctx, newRequest("", "spoke1--default", "my-res"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("unexpected requeue: %v", result)
	}
}

func TestHubReconcile_missingSourceLabels(t *testing.T) {
	// Hub copy exists but without source labels.
	hubCopy := newHubCopy("spoke1--default", "my-res", map[string]any{"phase": "Ready"}, nil)
	hc := hubClientWith(hubCopy)
	ctx := context.Background()

	r := NewCRSyncFromHubReconciler(hc, &fakeClusterProvider{
		clusters: map[string]cluster.Cluster{"spoke1": &fakeCluster{client: spokeClient()}},
	}, testGVK)

	result, err := r.Reconcile(ctx, newRequest("", "spoke1--default", "my-res"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("unexpected requeue: %v", result)
	}
}

func TestHubReconcile_sourceNotFoundOnSpoke(t *testing.T) {
	hubCopy := newHubCopy("spoke1--default", "my-res",
		map[string]any{"phase": "Ready"},
		map[string]string{
			LabelSourceCluster:   "spoke1",
			LabelSourceNamespace: "default",
			LabelSourceName:      "my-res",
		},
	)
	hc := hubClientWith(hubCopy)
	sc := spokeClient() // no source on spoke
	ctx := context.Background()

	r := NewCRSyncFromHubReconciler(hc, &fakeClusterProvider{
		clusters: map[string]cluster.Cluster{"spoke1": &fakeCluster{client: sc}},
	}, testGVK)

	result, err := r.Reconcile(ctx, newRequest("", "spoke1--default", "my-res"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("unexpected requeue: %v", result)
	}
}

func TestHubReconcile_spokeClusterNotFound(t *testing.T) {
	hubCopy := newHubCopy("unknown-cluster--default", "my-res",
		map[string]any{"phase": "Ready"},
		map[string]string{
			LabelSourceCluster:   "unknown-cluster",
			LabelSourceNamespace: "default",
			LabelSourceName:      "my-res",
		},
	)
	hc := hubClientWith(hubCopy)
	ctx := context.Background()

	r := NewCRSyncFromHubReconciler(hc, &fakeClusterProvider{
		clusters: map[string]cluster.Cluster{}, // no clusters
	}, testGVK)

	_, err := r.Reconcile(ctx, newRequest("", "unknown-cluster--default", "my-res"))
	if err == nil {
		t.Fatal("expected error when spoke cluster is not found")
	}
}
