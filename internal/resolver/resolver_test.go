package resolver

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	fakediscovery "k8s.io/client-go/discovery/fake"
	fakeclientset "k8s.io/client-go/kubernetes/fake"

	"github.com/r0bnet/crsync/cmd/config"
)

func newFakeDiscovery(resources []*metav1.APIResourceList) *fakediscovery.FakeDiscovery {
	cs := fakeclientset.NewSimpleClientset()
	fd := cs.Discovery().(*fakediscovery.FakeDiscovery)
	fd.Resources = resources
	return fd
}

func TestGVKResolver_Resolve(t *testing.T) {
	apiResources := []*metav1.APIResourceList{
		{
			GroupVersion: "stable.example.com/v1",
			APIResources: []metav1.APIResource{
				{Kind: "CronTab", Name: "crontabs"},
			},
		},
		{
			GroupVersion: "stable.example.com/v1beta1",
			APIResources: []metav1.APIResource{
				{Kind: "CronTab", Name: "crontabs"},
				{Kind: "Widget", Name: "widgets"},
			},
		},
		{
			GroupVersion: "apps/v1",
			APIResources: []metav1.APIResource{
				{Kind: "Deployment", Name: "deployments"},
			},
		},
	}

	tests := []struct {
		name      string
		resources []config.ResourceGVK
		wantGVKs  []schema.GroupVersionKind
		wantErr   bool
	}{
		{
			name: "all versions explicit",
			resources: []config.ResourceGVK{
				{Group: "apps", Version: "v1", Kind: "Deployment"},
			},
			wantGVKs: []schema.GroupVersionKind{
				{Group: "apps", Version: "v1", Kind: "Deployment"},
			},
		},
		{
			name: "version omitted picks only the preferred version",
			resources: []config.ResourceGVK{
				{Group: "stable.example.com", Kind: "CronTab"},
			},
			wantGVKs: []schema.GroupVersionKind{
				{Group: "stable.example.com", Version: "v1", Kind: "CronTab"},
			},
		},
		{
			name: "mixed explicit and discovered, falling back when the preferred version lacks the kind",
			resources: []config.ResourceGVK{
				{Group: "apps", Version: "v1", Kind: "Deployment"},
				{Group: "stable.example.com", Kind: "Widget"},
			},
			wantGVKs: []schema.GroupVersionKind{
				{Group: "apps", Version: "v1", Kind: "Deployment"},
				{Group: "stable.example.com", Version: "v1beta1", Kind: "Widget"},
			},
		},
		{
			name: "version omitted but kind not found",
			resources: []config.ResourceGVK{
				{Group: "stable.example.com", Kind: "NonExistent"},
			},
			wantErr: true,
		},
		{
			name:      "empty resources",
			resources: nil,
			wantGVKs:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			disc := newFakeDiscovery(apiResources)
			got, err := NewGVKResolver(disc).Resolve(tt.resources)

			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(got) != len(tt.wantGVKs) {
				t.Fatalf("got %d GVKs, want %d\ngot:  %v\nwant: %v", len(got), len(tt.wantGVKs), got, tt.wantGVKs)
			}
			for i, gvk := range got {
				if gvk != tt.wantGVKs[i] {
					t.Errorf("GVK[%d] = %v, want %v", i, gvk, tt.wantGVKs[i])
				}
			}
		})
	}
}
