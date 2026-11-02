package controller

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

// Provider cluster names may contain a "/" (ClusterProfile: "<namespace>/<name>"),
// which is invalid in label values and namespace names, so they get encoded.

// clusterLabelValue encodes a cluster name for the LabelSourceCluster label.
func clusterLabelValue(clusterName string) string {
	return strings.Replace(clusterName, "/", ".", 1)
}

// ClusterNameFromLabel reverses clusterLabelValue (exported for examples). The first "." is the
// separator because a ClusterProfile namespace can't contain one.
// shortcut: a dotted name without "/" (e.g. a kind cluster "a.b") decodes wrongly; fine for dev-only kind.
func ClusterNameFromLabel(value string) multicluster.ClusterName {
	return multicluster.ClusterName(strings.Replace(value, ".", "/", 1))
}

// hubNamespace returns the hub namespace (<cluster>--<namespace>) for a spoke
// cluster and source namespace. It also bounds the label value, which is shorter.
func hubNamespace(clusterName, sourceNamespace string) (string, error) {
	name := strings.Replace(clusterName, "/", "-", 1) + "--" + sourceNamespace
	if errs := validation.IsDNS1123Label(name); len(errs) > 0 {
		return "", fmt.Errorf("invalid hub namespace %q for cluster %q: %s", name, clusterName, strings.Join(errs, "; "))
	}
	return name, nil
}
