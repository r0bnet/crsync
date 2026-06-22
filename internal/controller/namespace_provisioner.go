package controller

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

// namespaceDeprovisioner cleans up hub namespaces when a spoke cluster is
// disengaged. Hub namespaces are created on-demand by the spoke reconciler;
// only the cleanup path is handled here.
// It implements mcmanager.Runnable (Start + Engage).
type namespaceDeprovisioner struct {
	hubClient client.Client
}

// NewNamespaceDeprovisioner returns a namespaceDeprovisioner that uses hubClient
// to delete namespaces on the hub cluster when spoke clusters get disengaged.
func NewNamespaceDeprovisioner(hubClient client.Client) *namespaceDeprovisioner {
	return &namespaceDeprovisioner{hubClient: hubClient}
}

// Start blocks until ctx is cancelled. Required by manager.Runnable.
func (n *namespaceDeprovisioner) Start(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

// Engage is called when a new spoke cluster is discovered. It spawns a
// goroutine that deletes all hub namespaces belonging to the cluster when
// the cluster context is cancelled.
func (n *namespaceDeprovisioner) Engage(ctx context.Context, clusterName multicluster.ClusterName, _ cluster.Cluster) error {
	logger := log.FromContext(ctx).WithValues("cluster", clusterName)
	logger.Info("engaged cluster, watching for disengage")

	go func() {
		<-ctx.Done()
		logger.Info("cluster disengaged, cleaning up namespaces")

		// Use a background context because the cluster context is already cancelled.
		bgCtx := context.Background()
		nsList := &corev1.NamespaceList{}
		if err := n.hubClient.List(bgCtx, nsList, client.MatchingLabels{
			LabelSourceCluster: string(clusterName),
		}); err != nil {
			logger.Error(err, "failed to list namespaces for cleanup")
			return
		}

		for i := range nsList.Items {
			if err := n.hubClient.Delete(bgCtx, &nsList.Items[i]); err != nil && !apierrors.IsNotFound(err) {
				logger.Error(err, "failed to delete namespace on cluster disengage", "namespace", nsList.Items[i].Name)
			}
		}
	}()

	return nil
}
