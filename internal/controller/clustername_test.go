package controller

import (
	"strings"
	"testing"

	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

func TestClusterLabelValue_roundTrip(t *testing.T) {
	tests := []struct {
		name    string
		cluster string
		label   string
	}{
		{name: "cluster profile", cluster: "fleet/spoke1", label: "fleet.spoke1"},
		{name: "dotted profile name", cluster: "fleet/a.b", label: "fleet.a.b"},
		{name: "kind cluster", cluster: "crsyncspoke1", label: "crsyncspoke1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := clusterLabelValue(tt.cluster)
			if got != tt.label {
				t.Fatalf("clusterLabelValue(%q) = %q, want %q", tt.cluster, got, tt.label)
			}
			back := ClusterNameFromLabel(got)
			if back != multicluster.ClusterName(tt.cluster) {
				t.Fatalf("ClusterNameFromLabel(%q) = %q, want %q", got, back, tt.cluster)
			}
		})
	}
}

func TestHubNamespace(t *testing.T) {
	tests := []struct {
		name      string
		cluster   string
		sourceNS  string
		want      string
		wantError bool
	}{
		{name: "cluster profile", cluster: "fleet/spoke1", sourceNS: "default", want: "fleet-spoke1--default"},
		{name: "kind cluster", cluster: "crsyncspoke1", sourceNS: "default", want: "crsyncspoke1--default"},
		{name: "too long", cluster: "fleet/" + strings.Repeat("a", 50), sourceNS: "default", wantError: true},
		{name: "dotted profile name", cluster: "fleet/a.b", sourceNS: "default", wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := hubNamespace(tt.cluster, tt.sourceNS)
			if tt.wantError {
				if err == nil {
					t.Fatalf("expected error, got namespace %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("hubNamespace(%q, %q) = %q, want %q", tt.cluster, tt.sourceNS, got, tt.want)
			}
		})
	}
}
