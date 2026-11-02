package controller

import (
	"context"
	"errors"
	"time"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

// clusterNotEngagedRetry covers hub requests that arrive before their workload cluster is engaged, e.g. at startup.
const clusterNotEngagedRetry = 5 * time.Second

// ClusterProvider returns a cluster.Cluster for the given cluster name.
// It is satisfied by the multicluster manager (mcmanager.Manager).
type ClusterProvider interface {
	GetCluster(ctx context.Context, clusterName multicluster.ClusterName) (cluster.Cluster, error)
}

// retryNotEngaged runs reconcile and requeues on ErrClusterNotFound. Hub-side controllers need
// this: the builder's ClusterNotFoundWrapper would otherwise drop the request for good.
func retryNotEngaged(ctx context.Context, req mcreconcile.Request, reconcile func(context.Context, mcreconcile.Request) (ctrl.Result, error)) (ctrl.Result, error) {
	result, err := reconcile(ctx, req)
	if errors.Is(err, multicluster.ErrClusterNotFound) {
		log.FromContext(ctx).V(1).Info("workload cluster not engaged yet, retrying", "request", req.NamespacedName)
		return ctrl.Result{RequeueAfter: clusterNotEngagedRetry}, nil
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	return result, nil
}
