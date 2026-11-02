package controller

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	mcbuilder "sigs.k8s.io/multicluster-runtime/pkg/builder"
	mccontroller "sigs.k8s.io/multicluster-runtime/pkg/controller"
	mcmanager "sigs.k8s.io/multicluster-runtime/pkg/manager"
	"sigs.k8s.io/multicluster-runtime/pkg/multicluster"
	mcreconcile "sigs.k8s.io/multicluster-runtime/pkg/reconcile"
)

// FinalizerSpokeCopyCleanup is added to hub Secrets to ensure the spoke copy is
// removed before the hub Secret is garbage-collected by Kubernetes.
const FinalizerSpokeCopyCleanup = "crsync.r0bnet.github.io/spoke-copy-cleanup"

// errSkipSecret is returned by resolveTarget when the namespace does not meet
// the conditions for secret syncing. Callers should treat this as a no-op reconcile.
var errSkipSecret = errors.New("secret does not match sync conditions")

// secretReconciler reconciles Secrets from the hub cluster.
type secretReconciler struct {
	client   client.Client
	clusters ClusterProvider
	// namespaceLabel is the label key that a namespace must carry for its
	// Secrets to be processed. Only the presence of the key is checked; the
	// value is ignored. When empty, the namespace check is skipped.
	namespaceLabel string
}

type secretReconcilerOption func(*secretReconciler)

// WithNamespaceLabel restricts reconciliation to Secrets whose namespace
// carries the given label key (value is not checked).
func WithNamespaceLabel(key string) secretReconcilerOption {
	return func(r *secretReconciler) {
		r.namespaceLabel = key
	}
}

// NewSecretReconciler returns a secretReconciler.
func NewSecretReconciler(c client.Client, clusters ClusterProvider, opts ...secretReconcilerOption) *secretReconciler {
	r := &secretReconciler{client: c, clusters: clusters}
	for _, o := range opts {
		o(r)
	}
	return r
}

// SetupWithManager registers the secret reconciler with the multicluster manager.
func (r *secretReconciler) SetupWithManager(mgr mcmanager.Manager, allowedSecretTypes []corev1.SecretType, maxConcurrentReconciles int) error {
	return mcbuilder.ControllerManagedBy(mgr).
		Named("crsync-secrets").
		For(&corev1.Secret{}, mcbuilder.WithEngageWithLocalCluster(true), mcbuilder.WithEngageWithProviderClusters(false)).
		// Watch spoke Secrets to trigger hub reconciliation for updates and deletes and reconcile them if necessary.
		// TODO: Should we do it this way since we're now watching all spoke Secrets? Alternatively, we could do a manual requeue in the secret reconciler after 10min or so
		Watches(
			&corev1.Secret{},
			spokeSecretToHubRequest,
			mcbuilder.WithPredicates(predicate.Funcs{
				CreateFunc:  func(event.CreateEvent) bool { return false },
				UpdateFunc:  func(event.UpdateEvent) bool { return true },
				DeleteFunc:  func(event.DeleteEvent) bool { return true },
				GenericFunc: func(event.GenericEvent) bool { return false },
			}),
		).
		WithEventFilter(SecretTypePredicate(allowedSecretTypes)).
		WithOptions(mccontroller.Options{
			MaxConcurrentReconciles: maxConcurrentReconciles,
		}).
		Complete(r)
}

// spokeSecretToHubRequest maps a Secret event from a spoke cluster back to the
// corresponding hub Secret. The multicluster source infrastructure calls this
// factory once per engaged spoke, passing the spoke's cluster name. The returned
// handler translates each spoke Secret event into a hub reconcile request by
// reconstructing the hub namespace (pattern: <cluster>--<namespace>).
// ClusterName is left empty so the request targets the hub.
func spokeSecretToHubRequest(clusterName multicluster.ClusterName, _ cluster.Cluster) handler.TypedEventHandler[client.Object, mcreconcile.Request] {
	return handler.TypedEnqueueRequestsFromMapFunc(
		func(ctx context.Context, obj client.Object) []mcreconcile.Request {
			hubNS, err := hubNamespace(string(clusterName), obj.GetNamespace())
			if err != nil {
				log.FromContext(ctx).Error(err, "dropping spoke secret event")
				return nil
			}
			return []mcreconcile.Request{{
				Request: reconcile.Request{
					NamespacedName: types.NamespacedName{
						Namespace: hubNS,
						Name:      obj.GetName(),
					},
				},
			}}
		},
	)
}

// Reconcile handles a Secret on the hub. It reads the target cluster and
// namespace from the labels of the Secret's hub namespace, then creates or
// updates a copy of the Secret on the spoke cluster.
// When the hub Secret has been deleted, the corresponding spoke copy is removed.
//
// If a namespace label is configured, only Secrets whose namespace carries
// that label key are processed.
func (r *secretReconciler) Reconcile(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx).WithValues(
		"cluster", req.ClusterName,
		"secret", req.NamespacedName,
	)
	ctx = log.IntoContext(ctx, logger)

	secret := &corev1.Secret{}
	if err := r.client.Get(ctx, req.NamespacedName, secret); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("get secret: %w", err)
		}
		// Secret already gone (e.g. force-deleted or finalizer was never added).
		// Best-effort delete of the spoke copy.
		return r.reconcileOrphanedDelete(ctx, req)
	}

	clusterName, targetNamespace, err := r.resolveTarget(ctx, req.Namespace)
	if err != nil {
		if errors.Is(err, errSkipSecret) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	logger = logger.WithValues("targetCluster", clusterName, "targetNamespace", targetNamespace)
	ctx = log.IntoContext(ctx, logger)

	spokeCluster, err := r.clusters.GetCluster(ctx, clusterNameFromLabel(clusterName))
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("get spoke cluster %s: %w", clusterName, err)
	}

	// Handle deletion: remove the spoke copy and then release the finalizer.
	if !secret.DeletionTimestamp.IsZero() {
		if err := r.deleteSpokeSecret(ctx, spokeCluster.GetClient(), targetNamespace, req.Name); err != nil {
			return ctrl.Result{}, err
		}
		if controllerutil.RemoveFinalizer(secret, FinalizerSpokeCopyCleanup) {
			if err := r.client.Update(ctx, secret); err != nil {
				return ctrl.Result{}, fmt.Errorf("remove finalizer: %w", err)
			}
		}
		logger.Info("spoke secret deleted")
		return ctrl.Result{}, nil
	}

	// Ensure the finalizer is present so that deletion is blocked until the spoke
	// copy has been cleaned up.
	if controllerutil.AddFinalizer(secret, FinalizerSpokeCopyCleanup) {
		if err := r.client.Update(ctx, secret); err != nil {
			return ctrl.Result{}, fmt.Errorf("add finalizer: %w", err)
		}
		return ctrl.Result{}, nil
	}

	if err := r.upsertSpokeSecret(ctx, spokeCluster.GetClient(), secret, targetNamespace); err != nil {
		return ctrl.Result{}, err
	}

	logger.Info("secret synced to spoke")
	return ctrl.Result{}, nil
}

// resolveTarget reads the hub namespace and returns the target cluster name and
// namespace from its labels. It returns errSkipSecret when the namespace does
// not meet the conditions for syncing:
// - Namespace not found
// - Namespace missing source labels
// - Namespace missing required label (when namespaceLabel is configured)
func (r *secretReconciler) resolveTarget(ctx context.Context, namespaceName string) (string, string, error) {
	logger := log.FromContext(ctx)

	ns := &corev1.Namespace{}
	if err := r.client.Get(ctx, client.ObjectKey{Name: namespaceName}, ns); err != nil {
		if apierrors.IsNotFound(err) {
			logger.V(1).Info("skipping secret: namespace not found")
			return "", "", errSkipSecret
		}
		return "", "", fmt.Errorf("get namespace %s: %w", namespaceName, err)
	}

	if r.namespaceLabel != "" {
		if _, ok := ns.Labels[r.namespaceLabel]; !ok {
			logger.V(1).Info("skipping secret: namespace does not carry required label",
				"namespaceLabel", r.namespaceLabel)
			return "", "", errSkipSecret
		}
	}

	clusterName := ns.Labels[LabelSourceCluster]
	targetNamespace := ns.Labels[LabelSourceNamespace]
	if clusterName == "" || targetNamespace == "" {
		logger.V(1).Info("skipping secret: namespace missing source labels")
		return "", "", errSkipSecret
	}

	return clusterName, targetNamespace, nil
}

// reconcileOrphanedDelete handles the case where the hub secret is already gone
// (e.g. force-deleted or the finalizer was never added). It performs a best-effort
// delete of the spoke copy.
func (r *secretReconciler) reconcileOrphanedDelete(ctx context.Context, req mcreconcile.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	clusterName, targetNamespace, err := r.resolveTarget(ctx, req.Namespace)
	if err != nil {
		if errors.Is(err, errSkipSecret) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	spokeCluster, err := r.clusters.GetCluster(ctx, clusterNameFromLabel(clusterName))
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("get spoke cluster %s: %w", clusterName, err)
	}

	if err := r.deleteSpokeSecret(ctx, spokeCluster.GetClient(), targetNamespace, req.Name); err != nil {
		return ctrl.Result{}, err
	}
	logger.Info("spoke secret deleted (orphaned)")
	return ctrl.Result{}, nil
}

// upsertSpokeSecret creates or updates the secret on the spoke cluster in the
// given target namespace, preserving the source name.
func (r *secretReconciler) upsertSpokeSecret(ctx context.Context, spokeClient client.Client, source *corev1.Secret, targetNamespace string) error {
	target := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      source.Name,
			Namespace: targetNamespace,
		},
	}
	_, err := controllerutil.CreateOrUpdate(ctx, spokeClient, target, func() error {
		// TODO: should we allow to change the type of the secret in the spoke? This could make our implementation more independent of Crossplane
		// since the owner of the spoke cluster would not know whether we're using Crossplane or anything else on the hub.
		// Actually the user should not even need to care about the hub implementation at all.
		target.Type = source.Type
		target.Data = source.Data
		target.StringData = source.StringData
		target.Labels = source.Labels
		target.Annotations = source.Annotations
		return nil
	})
	if err != nil {
		return fmt.Errorf("upsert secret %s/%s on spoke: %w", targetNamespace, source.Name, err)
	}
	return nil
}

// deleteSpokeSecret removes the secret from the spoke cluster. It is a no-op
// if the secret does not exist.
func (r *secretReconciler) deleteSpokeSecret(ctx context.Context, spokeClient client.Client, targetNamespace, name string) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: targetNamespace,
		},
	}
	if err := spokeClient.Delete(ctx, secret); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete secret %s/%s on spoke: %w", targetNamespace, name, err)
	}
	return nil
}
