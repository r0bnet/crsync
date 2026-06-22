package controller

import (
	"maps"
	"reflect"

	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
)

// IgnoreStatusChange returns a predicate that filters out update events where
// only the status (and resourceVersion) changed. Create, delete, and generic
// events always pass through.
func IgnoreStatusChange() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldU, oldOK := e.ObjectOld.(*unstructured.Unstructured)
			newU, newOK := e.ObjectNew.(*unstructured.Unstructured)
			if !oldOK || !newOK {
				return true
			}

			oldCopy := withoutStatusAndResourceVersion(oldU)
			newCopy := withoutStatusAndResourceVersion(newU)
			return !reflect.DeepEqual(oldCopy, newCopy)
		},
	}
}

// OnlyStatusChange returns a predicate that passes update events only when the
// status field changed. Non-status changes are filtered out. Create, delete,
// and generic events always pass through.
func OnlyStatusChange() predicate.Predicate {
	return predicate.Funcs{
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldU, oldOK := e.ObjectOld.(*unstructured.Unstructured)
			newU, newOK := e.ObjectNew.(*unstructured.Unstructured)
			if !oldOK || !newOK {
				return true
			}

			return !reflect.DeepEqual(oldU.Object["status"], newU.Object["status"])
		},
	}
}

// EventPredicate returns a predicate that filters events.k8s.io/v1 Events
// to only those whose Regarding object matches one of the watched GVKs.
func EventPredicate(watchedGVKs map[schema.GroupVersionKind]struct{}) predicate.Predicate {
	match := func(obj *eventsv1.Event) bool {
		if obj == nil {
			return false
		}
		ref := obj.Regarding
		gvk := schema.FromAPIVersionAndKind(ref.APIVersion, ref.Kind)
		_, ok := watchedGVKs[gvk]
		return ok
	}

	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			ev, ok := e.Object.(*eventsv1.Event)
			return ok && match(ev)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			ev, ok := e.ObjectNew.(*eventsv1.Event)
			return ok && match(ev)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return false // no need to reconcile deleted events
		},
		GenericFunc: func(e event.GenericEvent) bool {
			ev, ok := e.Object.(*eventsv1.Event)
			return ok && match(ev)
		},
	}
}

// withoutStatusAndResourceVersion returns a shallow copy of the object map
// with the "status" key removed and metadata.resourceVersion cleared so that
// status-only changes can be detected by comparing the remaining fields.
func withoutStatusAndResourceVersion(obj *unstructured.Unstructured) map[string]any {
	out := make(map[string]any, len(obj.Object))
	for k, v := range obj.Object {
		if k == "status" {
			continue
		}
		out[k] = v
	}

	// Clear resourceVersion which always changes on status updates.
	if meta, ok := out["metadata"].(map[string]any); ok {
		metaCopy := make(map[string]any, len(meta))
		maps.Copy(metaCopy, meta)
		delete(metaCopy, "resourceVersion")
		out["metadata"] = metaCopy
	}

	return out
}

// SecretTypePredicate returns a predicate that filters Secrets by their type.
// When allowedTypes is empty, all Secrets pass through. When non-empty, only
// Secrets whose Type matches one of the listed values are accepted.
func SecretTypePredicate(allowedTypes []corev1.SecretType) predicate.Predicate {
	if len(allowedTypes) == 0 {
		return predicate.Funcs{} // allow everything
	}

	allowed := make(map[corev1.SecretType]struct{}, len(allowedTypes))
	for _, t := range allowedTypes {
		allowed[t] = struct{}{}
	}

	match := func(obj *corev1.Secret) bool {
		_, ok := allowed[obj.Type]
		return ok
	}

	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			s, ok := e.Object.(*corev1.Secret)
			return ok && match(s)
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			s, ok := e.ObjectNew.(*corev1.Secret)
			return ok && match(s)
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			s, ok := e.Object.(*corev1.Secret)
			return ok && match(s)
		},
		GenericFunc: func(e event.GenericEvent) bool {
			s, ok := e.Object.(*corev1.Secret)
			return ok && match(s)
		},
	}
}

// RequireAnnotation returns a predicate that only accepts objects carrying the
// given annotation key with a non-empty value.
func RequireAnnotation(key string) predicate.Predicate {
	hasAnnotation := func(annotations map[string]string) bool {
		v, ok := annotations[key]
		return ok && v != ""
	}

	return predicate.Funcs{
		CreateFunc: func(e event.CreateEvent) bool {
			return hasAnnotation(e.Object.GetAnnotations())
		},
		UpdateFunc: func(e event.UpdateEvent) bool {
			return hasAnnotation(e.ObjectNew.GetAnnotations())
		},
		DeleteFunc: func(e event.DeleteEvent) bool {
			return hasAnnotation(e.Object.GetAnnotations())
		},
		GenericFunc: func(e event.GenericEvent) bool {
			return hasAnnotation(e.Object.GetAnnotations())
		},
	}
}
