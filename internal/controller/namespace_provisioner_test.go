package controller

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

var testScheme = runtime.NewScheme()

func init() {
	_ = corev1.AddToScheme(testScheme)
}

func newFakeClient() client.Client {
	return fake.NewClientBuilder().WithScheme(testScheme).Build()
}

func TestEngage_doesNotCreateNamespace(t *testing.T) {
	fc := newFakeClient()
	p := NewNamespaceDeprovisioner(fc)

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
	p := NewNamespaceDeprovisioner(fc)

	ctx := t.Context()

	if err := p.Engage(ctx, "spoke1", nil); err != nil {
		t.Fatalf("first Engage returned error: %v", err)
	}

	if err := p.Engage(ctx, "spoke1", nil); err != nil {
		t.Fatalf("second Engage returned error: %v", err)
	}
}

func TestEngage_deletesNamespacesByLabelOnCancel(t *testing.T) {
	fc := newFakeClient()
	p := NewNamespaceDeprovisioner(fc)

	ctx, cancel := context.WithCancel(context.Background())

	if err := p.Engage(ctx, "spoke1", nil); err != nil {
		t.Fatalf("Engage returned error: %v", err)
	}

	// Simulate the spoke controller having created namespaces with source-cluster labels.
	for _, nsName := range []string{"spoke1--default", "spoke1--kube-system"} {
		ns := &corev1.Namespace{}
		ns.Name = nsName
		ns.Labels = map[string]string{LabelSourceCluster: "spoke1"}
		if err := fc.Create(context.Background(), ns); err != nil {
			t.Fatalf("failed to pre-create namespace %s: %v", nsName, err)
		}
	}

	// Also create a namespace for a different cluster that should NOT be deleted.
	otherNS := &corev1.Namespace{}
	otherNS.Name = "spoke2--default"
	otherNS.Labels = map[string]string{LabelSourceCluster: "spoke2"}
	if err := fc.Create(context.Background(), otherNS); err != nil {
		t.Fatalf("failed to pre-create namespace spoke2--default: %v", err)
	}

	cancel()

	// Give the cleanup goroutine a moment to run.
	time.Sleep(100 * time.Millisecond)

	bgCtx := context.Background()

	// spoke1 namespaces should be deleted.
	for _, nsName := range []string{"spoke1--default", "spoke1--kube-system"} {
		ns := &corev1.Namespace{}
		err := fc.Get(bgCtx, client.ObjectKey{Name: nsName}, ns)
		if err == nil {
			t.Errorf("expected namespace %s to be deleted after cancel", nsName)
		} else if !apierrors.IsNotFound(err) {
			t.Errorf("expected NotFound for %s, got: %v", nsName, err)
		}
	}

	// spoke2 namespace should still exist.
	ns := &corev1.Namespace{}
	if err := fc.Get(bgCtx, client.ObjectKey{Name: "spoke2--default"}, ns); err != nil {
		t.Errorf("expected namespace spoke2--default to still exist: %v", err)
	}
}
