package controller

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

func hubSecretClient(objects ...client.Object) client.Client {
	return fake.NewClientBuilder().WithScheme(testScheme).WithObjects(objects...).Build()
}

func newHubSecret(namespace, name string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{"key": []byte("value")},
	}
}

func newSecretRequest(namespace, name string) mcreconcile.Request {
	return mcreconcile.Request{
		Request: reconcile.Request{
			NamespacedName: client.ObjectKey{Namespace: namespace, Name: name},
		},
	}
}

func TestSecretReconcile_copiesSecretToSpoke(t *testing.T) {
	ctx := context.Background()
	ns := newNamespace("spoke1--default", map[string]string{
		LabelSourceCluster:   "spoke1",
		LabelSourceNamespace: "default",
	})
	secret := newHubSecret("spoke1--default", "my-secret")
	hc := hubSecretClient(ns, secret)
	sc := spokeClient()

	r := NewSecretReconciler(hc, &fakeClusterProvider{
		clusters: map[string]cluster.Cluster{"spoke1": &fakeCluster{client: sc}},
	}, WithNamespaceLabel(LabelSourceCluster))

	req := newSecretRequest("spoke1--default", "my-secret")

	// First reconcile: adds the finalizer and returns early.
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("first reconcile: unexpected error: %v", err)
	}

	// Second reconcile: syncs the secret to the spoke.
	result, err := r.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("second reconcile: unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("unexpected requeue: %v", result)
	}

	got := &corev1.Secret{}
	if err := sc.Get(ctx, client.ObjectKey{Namespace: "default", Name: "my-secret"}, got); err != nil {
		t.Fatalf("expected secret on spoke: %v", err)
	}
	if string(got.Data["key"]) != "value" {
		t.Errorf("expected data key=value, got %v", got.Data)
	}
}

func TestSecretReconcile_addsFinalizerOnFirstReconcile(t *testing.T) {
	ctx := context.Background()
	ns := newNamespace("spoke1--default", map[string]string{
		LabelSourceCluster:   "spoke1",
		LabelSourceNamespace: "default",
	})
	secret := newHubSecret("spoke1--default", "my-secret")
	hc := hubSecretClient(ns, secret)
	sc := spokeClient()

	r := NewSecretReconciler(hc, &fakeClusterProvider{
		clusters: map[string]cluster.Cluster{"spoke1": &fakeCluster{client: sc}},
	})

	if _, err := r.Reconcile(ctx, newSecretRequest("spoke1--default", "my-secret")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the finalizer was added to the hub secret.
	got := &corev1.Secret{}
	if err := hc.Get(ctx, client.ObjectKey{Namespace: "spoke1--default", Name: "my-secret"}, got); err != nil {
		t.Fatalf("get hub secret: %v", err)
	}
	if !controllerutil.ContainsFinalizer(got, FinalizerSpokeCopyCleanup) {
		t.Errorf("expected finalizer %s, got %v", FinalizerSpokeCopyCleanup, got.Finalizers)
	}

	// Spoke secret should not exist yet — first reconcile only adds the finalizer.
	spokeSecret := &corev1.Secret{}
	if err := sc.Get(ctx, client.ObjectKey{Namespace: "default", Name: "my-secret"}, spokeSecret); err == nil {
		t.Error("expected spoke secret to not exist after first reconcile")
	}
}

func TestSecretReconcile_updatesExistingSecretOnSpoke(t *testing.T) {
	ctx := context.Background()
	ns := newNamespace("spoke1--default", map[string]string{
		LabelSourceCluster:   "spoke1",
		LabelSourceNamespace: "default",
	})
	secret := newHubSecret("spoke1--default", "my-secret")
	secret.Data = map[string][]byte{"key": []byte("updated")}
	controllerutil.AddFinalizer(secret, FinalizerSpokeCopyCleanup)
	hc := hubSecretClient(ns, secret)

	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "my-secret", Namespace: "default"},
		Data:       map[string][]byte{"key": []byte("old")},
	}
	sc := spokeClient(existing)

	r := NewSecretReconciler(hc, &fakeClusterProvider{
		clusters: map[string]cluster.Cluster{"spoke1": &fakeCluster{client: sc}},
	})
	if _, err := r.Reconcile(ctx, newSecretRequest("spoke1--default", "my-secret")); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &corev1.Secret{}
	if err := sc.Get(ctx, client.ObjectKey{Namespace: "default", Name: "my-secret"}, got); err != nil {
		t.Fatalf("expected secret on spoke: %v", err)
	}
	if string(got.Data["key"]) != "updated" {
		t.Errorf("expected data key=updated, got %v", got.Data)
	}
}

func TestSecretReconcile_deletesSecretFromSpokeViaFinalizer(t *testing.T) {
	ctx := context.Background()
	ns := newNamespace("spoke1--default", map[string]string{
		LabelSourceCluster:   "spoke1",
		LabelSourceNamespace: "default",
	})
	// Hub secret is marked for deletion — it still exists because of our finalizer.
	secret := newHubSecret("spoke1--default", "my-secret")
	now := metav1.Now()
	secret.DeletionTimestamp = &now
	secret.Finalizers = []string{FinalizerSpokeCopyCleanup}
	hc := hubSecretClient(ns, secret)

	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "my-secret", Namespace: "default"},
		Data:       map[string][]byte{"key": []byte("value")},
	}
	sc := spokeClient(existing)

	r := NewSecretReconciler(hc, &fakeClusterProvider{
		clusters: map[string]cluster.Cluster{"spoke1": &fakeCluster{client: sc}},
	})
	result, err := r.Reconcile(ctx, newSecretRequest("spoke1--default", "my-secret"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("unexpected requeue: %v", result)
	}

	// Verify the spoke secret was deleted.
	got := &corev1.Secret{}
	if err := sc.Get(ctx, client.ObjectKey{Namespace: "default", Name: "my-secret"}, got); err == nil {
		t.Fatal("expected spoke secret to be deleted")
	}

	// After finalizer removal the fake client garbage-collects the hub secret
	// (DeletionTimestamp was set and no finalizers remain).
	hubGot := &corev1.Secret{}
	err = hc.Get(ctx, client.ObjectKey{Namespace: "spoke1--default", Name: "my-secret"}, hubGot)
	if err == nil && controllerutil.ContainsFinalizer(hubGot, FinalizerSpokeCopyCleanup) {
		t.Errorf("expected finalizer to be removed, got %v", hubGot.Finalizers)
	}
}

func TestSecretReconcile_deleteNoopWhenSpokeSecretMissing(t *testing.T) {
	ns := newNamespace("spoke1--default", map[string]string{
		LabelSourceCluster:   "spoke1",
		LabelSourceNamespace: "default",
	})
	// Hub secret is marked for deletion, spoke secret already gone.
	secret := newHubSecret("spoke1--default", "my-secret")
	now := metav1.Now()
	secret.DeletionTimestamp = &now
	secret.Finalizers = []string{FinalizerSpokeCopyCleanup}
	hc := hubSecretClient(ns, secret)
	sc := spokeClient() // no spoke secret

	r := NewSecretReconciler(hc, &fakeClusterProvider{
		clusters: map[string]cluster.Cluster{"spoke1": &fakeCluster{client: sc}},
	})
	result, err := r.Reconcile(context.Background(), newSecretRequest("spoke1--default", "my-secret"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("unexpected requeue: %v", result)
	}
}

func TestSecretReconcile_orphanedDeleteCleansUpSpoke(t *testing.T) {
	ctx := context.Background()
	ns := newNamespace("spoke1--default", map[string]string{
		LabelSourceCluster:   "spoke1",
		LabelSourceNamespace: "default",
	})
	hc := hubSecretClient(ns) // no hub secret — force-deleted

	existing := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "my-secret", Namespace: "default"},
		Data:       map[string][]byte{"key": []byte("value")},
	}
	sc := spokeClient(existing)

	r := NewSecretReconciler(hc, &fakeClusterProvider{
		clusters: map[string]cluster.Cluster{"spoke1": &fakeCluster{client: sc}},
	})
	result, err := r.Reconcile(ctx, newSecretRequest("spoke1--default", "my-secret"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("unexpected requeue: %v", result)
	}

	// Verify the spoke secret was deleted.
	got := &corev1.Secret{}
	if err := sc.Get(ctx, client.ObjectKey{Namespace: "default", Name: "my-secret"}, got); err == nil {
		t.Fatal("expected spoke secret to be deleted")
	}
}

func TestSecretReconcile_namespaceMissingSourceLabels(t *testing.T) {
	// Namespace exists but has no source labels — secret should be skipped.
	ns := newNamespace("some-ns", nil)
	secret := newHubSecret("some-ns", "my-secret")
	hc := hubSecretClient(ns, secret)

	r := NewSecretReconciler(hc, &fakeClusterProvider{})
	result, err := r.Reconcile(context.Background(), newSecretRequest("some-ns", "my-secret"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("unexpected requeue: %v", result)
	}
}

func TestSecretReconcile_namespaceWithoutLabel(t *testing.T) {
	// Namespace has source labels but is missing the required namespaceLabel key.
	ns := newNamespace("spoke1--default", map[string]string{
		LabelSourceNamespace: "default",
		// LabelSourceCluster intentionally absent — used as namespaceLabel
	})
	secret := newHubSecret("spoke1--default", "my-secret")
	hc := hubSecretClient(ns, secret)

	r := NewSecretReconciler(hc, &fakeClusterProvider{}, WithNamespaceLabel(LabelSourceCluster))
	result, err := r.Reconcile(context.Background(), newSecretRequest("spoke1--default", "my-secret"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("unexpected requeue: %v", result)
	}
}

func TestSecretReconcile_namespaceNotFound(t *testing.T) {
	secret := newHubSecret("gone-ns", "my-secret")
	hc := hubSecretClient(secret) // no namespace

	r := NewSecretReconciler(hc, &fakeClusterProvider{})
	result, err := r.Reconcile(context.Background(), newSecretRequest("gone-ns", "my-secret"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.RequeueAfter != 0 {
		t.Fatalf("unexpected requeue: %v", result)
	}
}

func TestSecretReconcile_withoutNamespaceLabelOption(t *testing.T) {
	ctx := context.Background()
	// When no namespaceLabel is configured, any namespace with source labels is synced.
	ns := newNamespace("spoke1--default", map[string]string{
		LabelSourceCluster:   "spoke1",
		LabelSourceNamespace: "default",
	})
	secret := newHubSecret("spoke1--default", "my-secret")
	hc := hubSecretClient(ns, secret)
	sc := spokeClient()

	r := NewSecretReconciler(hc, &fakeClusterProvider{
		clusters: map[string]cluster.Cluster{"spoke1": &fakeCluster{client: sc}},
	}) // no WithNamespaceLabel

	req := newSecretRequest("spoke1--default", "my-secret")

	// First reconcile: adds the finalizer.
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("first reconcile: unexpected error: %v", err)
	}

	// Second reconcile: syncs to spoke.
	if _, err := r.Reconcile(ctx, req); err != nil {
		t.Fatalf("second reconcile: unexpected error: %v", err)
	}

	got := &corev1.Secret{}
	if err := sc.Get(ctx, client.ObjectKey{Namespace: "default", Name: "my-secret"}, got); err != nil {
		t.Fatalf("expected secret on spoke: %v", err)
	}
}
