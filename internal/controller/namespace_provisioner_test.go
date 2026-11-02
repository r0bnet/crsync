package controller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clusterinventoryv1alpha1 "sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

var testScheme = runtime.NewScheme()

func init() {
	_ = corev1.AddToScheme(testScheme)
	_ = eventsv1.AddToScheme(testScheme)
	_ = clusterinventoryv1alpha1.AddToScheme(testScheme)
}

func newFakeClient() client.Client {
	return fake.NewClientBuilder().WithScheme(testScheme).Build()
}

func TestEngage_doesNotCreateNamespace(t *testing.T) {
	fc := newFakeClient()
	p := NewNamespaceDeprovisioner(fc, ClusterProfileGone(fc))

	ctx := t.Context()

	if err := p.Engage(ctx, "spoke1", nil); err != nil {
		t.Fatalf("Engage returned error: %v", err)
	}

	ns := &corev1.Namespace{}
	err := fc.Get(ctx, client.ObjectKey{Name: "spoke1"}, ns)
	if err == nil {
		t.Fatal("expected namespace not to be created by Engage")
	}
	if !apierrors.IsNotFound(err) {
		t.Fatalf("expected NotFound, got: %v", err)
	}
}

func TestEngage_idempotent(t *testing.T) {
	fc := newFakeClient()
	p := NewNamespaceDeprovisioner(fc, ClusterProfileGone(fc))

	ctx := t.Context()

	if err := p.Engage(ctx, "spoke1", nil); err != nil {
		t.Fatalf("first Engage returned error: %v", err)
	}

	if err := p.Engage(ctx, "spoke1", nil); err != nil {
		t.Fatalf("second Engage returned error: %v", err)
	}
}

// createHubNamespaces creates labelled hub namespaces for fleet/spoke1 and fleet/spoke2.
func createHubNamespaces(t *testing.T, fc client.Client) {
	t.Helper()
	for name, cluster := range map[string]string{
		"fleet-spoke1--default":     "fleet/spoke1",
		"fleet-spoke1--kube-system": "fleet/spoke1",
		"fleet-spoke2--default":     "fleet/spoke2",
	} {
		ns := &corev1.Namespace{}
		ns.Name = name
		ns.Labels = map[string]string{LabelSourceCluster: clusterLabelValue(cluster)}
		if err := fc.Create(context.Background(), ns); err != nil {
			t.Fatalf("failed to pre-create namespace %s: %v", name, err)
		}
	}
}

func namespaceExists(t *testing.T, fc client.Client, name string) bool {
	t.Helper()
	err := fc.Get(context.Background(), client.ObjectKey{Name: name}, &corev1.Namespace{})
	if apierrors.IsNotFound(err) {
		return false
	}
	if err != nil {
		t.Fatalf("get namespace %s: %v", name, err)
	}
	return true
}

func TestEngage_deletesNamespacesWhenProfileRemoved(t *testing.T) {
	fc := newFakeClient()
	p := NewNamespaceDeprovisioner(fc, ClusterProfileGone(fc))
	createHubNamespaces(t, fc)

	ctx, cancel := context.WithCancel(context.Background())
	if err := p.Engage(ctx, "fleet/spoke1", nil); err != nil {
		t.Fatalf("Engage returned error: %v", err)
	}
	cancel()

	deadline := time.Now().Add(2 * time.Second)
	for namespaceExists(t, fc, "fleet-spoke1--default") || namespaceExists(t, fc, "fleet-spoke1--kube-system") {
		if time.Now().After(deadline) {
			t.Fatal("expected fleet/spoke1 namespaces to be deleted after its ClusterProfile is gone")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !namespaceExists(t, fc, "fleet-spoke2--default") {
		t.Error("expected namespace of fleet/spoke2 to survive")
	}
}

// A restart, leader change or kubeconfig change cancels the cluster context too.
func TestCleanup_keepsNamespacesWhileProfileExists(t *testing.T) {
	fc := newFakeClient()
	p := NewNamespaceDeprovisioner(fc, ClusterProfileGone(fc))
	createHubNamespaces(t, fc)

	profile := &clusterinventoryv1alpha1.ClusterProfile{}
	profile.Namespace = "fleet"
	profile.Name = "spoke1"
	if err := fc.Create(context.Background(), profile); err != nil {
		t.Fatalf("create ClusterProfile: %v", err)
	}

	if err := p.cleanup(context.Background(), "fleet/spoke1"); err != nil {
		t.Fatalf("cleanup returned error: %v", err)
	}
	for _, name := range []string{"fleet-spoke1--default", "fleet-spoke1--kube-system"} {
		if !namespaceExists(t, fc, name) {
			t.Errorf("namespace %s was deleted although its ClusterProfile still exists", name)
		}
	}
}

func TestClusterProfileGone(t *testing.T) {
	deleting := &clusterinventoryv1alpha1.ClusterProfile{}
	deleting.Namespace = "fleet"
	deleting.Name = "deleting"
	deleting.Finalizers = []string{"test/keep"}
	deleting.DeletionTimestamp = &metav1.Time{Time: time.Now()}

	present := &clusterinventoryv1alpha1.ClusterProfile{}
	present.Namespace = "fleet"
	present.Name = "present"

	fc := fake.NewClientBuilder().WithScheme(testScheme).WithObjects(deleting, present).Build()
	gone := ClusterProfileGone(fc)

	for _, tc := range []struct {
		cluster string
		want    bool
		wantErr bool
	}{
		{cluster: "fleet/present", want: false},
		{cluster: "fleet/deleting", want: true},
		{cluster: "fleet/missing", want: true},
		{cluster: "no-namespace", wantErr: true},
	} {
		got, err := gone(context.Background(), multicluster.ClusterName(tc.cluster))
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v, wantErr %v", tc.cluster, err, tc.wantErr)
		}
		if got != tc.want {
			t.Errorf("%s: gone = %v, want %v", tc.cluster, got, tc.want)
		}
	}
}
