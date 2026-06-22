package controller

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

// ClusterProvider returns a cluster.Cluster for the given cluster name.
// It is satisfied by the multicluster manager (mcmanager.Manager).
type ClusterProvider interface {
	GetCluster(ctx context.Context, clusterName multicluster.ClusterName) (cluster.Cluster, error)
}
