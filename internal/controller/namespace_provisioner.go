package controller

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	clusterinventoryv1alpha1 "sigs.k8s.io/cluster-inventory-api/apis/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
)

// ClusterGoneFunc reports whether a disengaged cluster was removed for good, as opposed
// to a restart, leader change or re-engage with new credentials.
type ClusterGoneFunc func(ctx context.Context, clusterName multicluster.ClusterName) (bool, error)

// ClusterProfileGone treats a cluster as gone once its ClusterProfile is deleted or being deleted.
func ClusterProfileGone(hubReader client.Reader) ClusterGoneFunc {
	return func(ctx context.Context, clusterName multicluster.ClusterName) (bool, error) {
		namespace, name, ok := strings.Cut(string(clusterName), "/")
		if !ok {
			return false, fmt.Errorf("cluster name %q is not <namespace>/<name>", clusterName)
		}

		profile := &clusterinventoryv1alpha1.ClusterProfile{}
		err := hubReader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, profile)
		if apierrors.IsNotFound(err) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		return profile.DeletionTimestamp != nil, nil
	}
}

// namespaceDeprovisioner cleans up hub namespaces when a spoke cluster is removed.
// Hub namespaces are created on-demand by the spoke reconciler; only the cleanup path is handled here.
// It implements mcmanager.Runnable (Start + Engage).
type namespaceDeprovisioner struct {
	hubClient   client.Client
	clusterGone ClusterGoneFunc
}

// NewNamespaceDeprovisioner returns a namespaceDeprovisioner that deletes a cluster's hub
// namespaces once clusterGone confirms the cluster was removed.
func NewNamespaceDeprovisioner(hubClient client.Client, clusterGone ClusterGoneFunc) *namespaceDeprovisioner {
	return &namespaceDeprovisioner{hubClient: hubClient, clusterGone: clusterGone}
}

// Start blocks until ctx is cancelled. Required by manager.Runnable.
func (n *namespaceDeprovisioner) Start(ctx context.Context) error {
	<-ctx.Done()
	return nil
}

// Engage is called when a new spoke cluster is discovered. Once the cluster context is
// cancelled, it cleans up the cluster's hub namespaces if the cluster is really gone.
func (n *namespaceDeprovisioner) Engage(ctx context.Context, clusterName multicluster.ClusterName, _ cluster.Cluster) error {
	logger := log.FromContext(ctx).WithValues("cluster", clusterName)
	logger.Info("engaged cluster, watching for disengage")

	go func() {
		<-ctx.Done()
		// Use a background context because the cluster context is already cancelled.
		if err := n.cleanup(context.Background(), clusterName); err != nil {
			logger.Error(err, "failed to clean up namespaces of disengaged cluster")
		}
	}()

	return nil
}

// cleanup deletes all hub namespaces of clusterName, unless the cluster is still registered.
// Restarts and re-engages also cancel the cluster context, and must not delete hub copies.
func (n *namespaceDeprovisioner) cleanup(ctx context.Context, clusterName multicluster.ClusterName) error {
	logger := log.FromContext(ctx).WithValues("cluster", clusterName)

	gone, err := n.clusterGone(ctx, clusterName)
	if err != nil {
		return fmt.Errorf("check whether cluster is gone: %w", err)
	}
	if !gone {
		logger.Info("cluster disengaged but still registered, keeping its namespaces")
		return nil
	}

	logger.Info("cluster removed, cleaning up namespaces")
	nsList := &corev1.NamespaceList{}
	if err := n.hubClient.List(ctx, nsList, client.MatchingLabels{
		LabelSourceCluster: clusterLabelValue(string(clusterName)),
	}); err != nil {
		return fmt.Errorf("list namespaces: %w", err)
	}

	for i := range nsList.Items {
		if err := n.hubClient.Delete(ctx, &nsList.Items[i]); err != nil && !apierrors.IsNotFound(err) {
			logger.Error(err, "failed to delete namespace of removed cluster", "namespace", nsList.Items[i].Name)
		}
	}
	return nil
}
