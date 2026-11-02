package controller

import (
	"context"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// maxOwnerHops bounds the ownerReference walk from an object to its hub copy.
const maxOwnerHops = 5

// findHubCopy walks controller ownerReferences from the given object until it finds an
// object carrying crsync's source labels. It returns nil when an object can't be read,
// has no controller, or the walk exceeds maxOwnerHops.
func findHubCopy(ctx context.Context, reader client.Reader, namespace string, gvk schema.GroupVersionKind, name string) (*unstructured.Unstructured, int) {
	logger := log.FromContext(ctx)

	for hops := 0; hops <= maxOwnerHops; hops++ {
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(gvk)
		// ownerReferences are namespace-local, so the whole chain lives in one namespace.
		err := reader.Get(ctx, client.ObjectKey{Namespace: namespace, Name: name}, obj)
		if err != nil {
			logger.V(1).Info("owner chain not readable", "gvk", gvk.String(), "name", name, "error", err.Error())
			return nil, 0
		}

		labels := obj.GetLabels()
		if labels[LabelSourceCluster] != "" && labels[LabelSourceNamespace] != "" && labels[LabelSourceName] != "" {
			return obj, hops
		}

		owner := metav1.GetControllerOfNoCopy(obj)
		if owner == nil {
			logger.V(1).Info("no controller owner", "gvk", gvk.String(), "name", name)
			return nil, 0
		}
		gvk = schema.FromAPIVersionAndKind(owner.APIVersion, owner.Kind)
		name = owner.Name
	}
	logger.V(1).Info("owner chain too long", "maxOwnerHops", maxOwnerHops)
	return nil, 0
}
