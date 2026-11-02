package controller

import (
	"maps"
	"reflect"

	eventsv1 "k8s.io/api/events/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

// EventPredicate returns a predicate that passes events.k8s.io/v1 Events on
// create and update, except those reported by ignoredController (crsync's own).
func EventPredicate(ignoredController string) predicate.Predicate {
	match := func(obj *eventsv1.Event) bool {
		return obj != nil && obj.ReportingController != ignoredController
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
