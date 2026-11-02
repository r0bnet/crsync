package main

import (
	"context"
	"testing"

	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	clusterinventoryv1alpha1 "sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"github.com/r0bnet/crsync/internal/controller"
)

const testHubNamespace = "fleet-spoke1--team-a"

func testHandler(t *testing.T, sourceLabels map[string]string) *handler {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := clusterinventoryv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}

	hub := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name: testHubNamespace,
			Labels: map[string]string{
				controller.LabelSourceCluster:   "fleet.spoke1",
				controller.LabelSourceNamespace: "team-a",
			},
		}},
		&clusterinventoryv1alpha1.ClusterProfile{ObjectMeta: metav1.ObjectMeta{Namespace: "fleet", Name: "spoke1"}},
	).Build()
	spoke := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a", Labels: sourceLabels}},
	).Build()

	return &handler{
		hub: hub,
		spokeReader: func(profile *clusterinventoryv1alpha1.ClusterProfile) (client.Reader, error) {
			if profile.Namespace != "fleet" || profile.Name != "spoke1" {
				t.Fatalf("got ClusterProfile %s/%s, want fleet/spoke1", profile.Namespace, profile.Name)
			}
			return spoke, nil
		},
	}
}

func queueRequest() admission.Request {
	return admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
		Namespace: testHubNamespace,
		Object: runtime.RawExtension{
			Raw: []byte(`{"apiVersion":"messaging.example.io/v1alpha1","kind":"Queue","metadata":{"name":"orders"},"spec":{}}`),
		},
	}}
}

func TestHandleSetsCostCenterFromSourceNamespace(t *testing.T) {
	h := testHandler(t, map[string]string{costCenterLabel: "cc-42"})

	resp := h.Handle(context.Background(), queueRequest())
	if !resp.Allowed {
		t.Fatalf("request denied: %v", resp.Result)
	}
	if len(resp.Patches) != 1 {
		t.Fatalf("got patches %v, want one that sets spec.costCenter", resp.Patches)
	}
	if p := resp.Patches[0]; p.Path != "/spec/costCenter" || p.Value != "cc-42" {
		t.Errorf("got patch %s %s=%v, want add /spec/costCenter=cc-42", p.Operation, p.Path, p.Value)
	}
}

func TestHandleLeavesObjectWithoutLabel(t *testing.T) {
	h := testHandler(t, nil)

	resp := h.Handle(context.Background(), queueRequest())
	if !resp.Allowed || len(resp.Patches) != 0 {
		t.Errorf("got allowed=%v patches=%v, want the object admitted unchanged", resp.Allowed, resp.Patches)
	}
}
