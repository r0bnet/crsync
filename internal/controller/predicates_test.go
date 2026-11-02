package controller

import (
	"testing"

	eventsv1 "k8s.io/api/events/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/event"
)

func unstructuredObj(fields map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: fields}
}

func TestIgnoreStatusChange_statusOnlyUpdate(t *testing.T) {
	oldObj := unstructuredObj(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "a", "namespace": "ns", "resourceVersion": "1"},
		"spec":       map[string]any{"key": "value"},
	})
	newObj := unstructuredObj(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "a", "namespace": "ns", "resourceVersion": "2"},
		"spec":       map[string]any{"key": "value"},
		"status":     map[string]any{"phase": "Ready"},
	})

	p := IgnoreStatusChange()
	if p.Update(event.UpdateEvent{ObjectOld: oldObj, ObjectNew: newObj}) {
		t.Error("expected status-only update to be filtered out")
	}
}

func TestIgnoreStatusChange_specChange(t *testing.T) {
	oldObj := unstructuredObj(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "a", "namespace": "ns", "resourceVersion": "1"},
		"spec":       map[string]any{"key": "old"},
	})
	newObj := unstructuredObj(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "a", "namespace": "ns", "resourceVersion": "2"},
		"spec":       map[string]any{"key": "new"},
	})

	p := IgnoreStatusChange()
	if !p.Update(event.UpdateEvent{ObjectOld: oldObj, ObjectNew: newObj}) {
		t.Error("expected spec change to pass through")
	}
}

func TestIgnoreStatusChange_labelChange(t *testing.T) {
	oldObj := unstructuredObj(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "a", "namespace": "ns", "resourceVersion": "1", "labels": map[string]any{"app": "old"}},
	})
	newObj := unstructuredObj(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "a", "namespace": "ns", "resourceVersion": "2", "labels": map[string]any{"app": "new"}},
	})

	p := IgnoreStatusChange()
	if !p.Update(event.UpdateEvent{ObjectOld: oldObj, ObjectNew: newObj}) {
		t.Error("expected label change to pass through")
	}
}

func TestIgnoreStatusChange_specAndStatusChange(t *testing.T) {
	oldObj := unstructuredObj(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "a", "namespace": "ns", "resourceVersion": "1"},
		"spec":       map[string]any{"key": "old"},
		"status":     map[string]any{"phase": "Pending"},
	})
	newObj := unstructuredObj(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "a", "namespace": "ns", "resourceVersion": "2"},
		"spec":       map[string]any{"key": "new"},
		"status":     map[string]any{"phase": "Ready"},
	})

	p := IgnoreStatusChange()
	if !p.Update(event.UpdateEvent{ObjectOld: oldObj, ObjectNew: newObj}) {
		t.Error("expected spec+status change to pass through")
	}
}

func TestIgnoreStatusChange_noChange(t *testing.T) {
	oldObj := unstructuredObj(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "a", "namespace": "ns", "resourceVersion": "1"},
		"spec":       map[string]any{"key": "value"},
	})
	newObj := unstructuredObj(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "a", "namespace": "ns", "resourceVersion": "2"},
		"spec":       map[string]any{"key": "value"},
	})

	p := IgnoreStatusChange()
	if p.Update(event.UpdateEvent{ObjectOld: oldObj, ObjectNew: newObj}) {
		t.Error("expected no-change (only resourceVersion) to be filtered out")
	}
}

func TestIgnoreStatusChange_createPassesThrough(t *testing.T) {
	obj := unstructuredObj(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "a", "namespace": "ns"},
	})

	p := IgnoreStatusChange()
	if !p.Create(event.CreateEvent{Object: obj}) {
		t.Error("expected create events to pass through")
	}
}

func TestIgnoreStatusChange_deletePassesThrough(t *testing.T) {
	obj := unstructuredObj(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "a", "namespace": "ns"},
	})

	p := IgnoreStatusChange()
	if !p.Delete(event.DeleteEvent{Object: obj}) {
		t.Error("expected delete events to pass through")
	}
}

func TestOnlyStatusChange_statusChanged(t *testing.T) {
	oldObj := unstructuredObj(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "a", "resourceVersion": "1"},
		"status":     map[string]any{"phase": "Pending"},
	})
	newObj := unstructuredObj(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "a", "resourceVersion": "2"},
		"status":     map[string]any{"phase": "Ready"},
	})

	p := OnlyStatusChange()
	if !p.Update(event.UpdateEvent{ObjectOld: oldObj, ObjectNew: newObj}) {
		t.Error("expected status change to pass through")
	}
}

func TestOnlyStatusChange_specOnlyChange(t *testing.T) {
	oldObj := unstructuredObj(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "a", "resourceVersion": "1"},
		"spec":       map[string]any{"key": "old"},
		"status":     map[string]any{"phase": "Ready"},
	})
	newObj := unstructuredObj(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "a", "resourceVersion": "2"},
		"spec":       map[string]any{"key": "new"},
		"status":     map[string]any{"phase": "Ready"},
	})

	p := OnlyStatusChange()
	if p.Update(event.UpdateEvent{ObjectOld: oldObj, ObjectNew: newObj}) {
		t.Error("expected spec-only change to be filtered out")
	}
}

func TestOnlyStatusChange_specAndStatusChange(t *testing.T) {
	oldObj := unstructuredObj(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "a", "resourceVersion": "1"},
		"spec":       map[string]any{"key": "old"},
		"status":     map[string]any{"phase": "Pending"},
	})
	newObj := unstructuredObj(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "a", "resourceVersion": "2"},
		"spec":       map[string]any{"key": "new"},
		"status":     map[string]any{"phase": "Ready"},
	})

	p := OnlyStatusChange()
	if !p.Update(event.UpdateEvent{ObjectOld: oldObj, ObjectNew: newObj}) {
		t.Error("expected status change (even with spec change) to pass through")
	}
}

func TestOnlyStatusChange_noChange(t *testing.T) {
	oldObj := unstructuredObj(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "a", "resourceVersion": "1"},
		"status":     map[string]any{"phase": "Ready"},
	})
	newObj := unstructuredObj(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "a", "resourceVersion": "2"},
		"status":     map[string]any{"phase": "Ready"},
	})

	p := OnlyStatusChange()
	if p.Update(event.UpdateEvent{ObjectOld: oldObj, ObjectNew: newObj}) {
		t.Error("expected no-change to be filtered out")
	}
}

func TestOnlyStatusChange_statusAddedWhereNone(t *testing.T) {
	oldObj := unstructuredObj(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "a", "resourceVersion": "1"},
	})
	newObj := unstructuredObj(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "a", "resourceVersion": "2"},
		"status":     map[string]any{"phase": "Ready"},
	})

	p := OnlyStatusChange()
	if !p.Update(event.UpdateEvent{ObjectOld: oldObj, ObjectNew: newObj}) {
		t.Error("expected status appearing for the first time to pass through")
	}
}

func TestOnlyStatusChange_createPassesThrough(t *testing.T) {
	obj := unstructuredObj(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "a"},
	})

	p := OnlyStatusChange()
	if !p.Create(event.CreateEvent{Object: obj}) {
		t.Error("expected create events to pass through")
	}
}

func TestOnlyStatusChange_deletePassesThrough(t *testing.T) {
	obj := unstructuredObj(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "a"},
	})

	p := OnlyStatusChange()
	if !p.Delete(event.DeleteEvent{Object: obj}) {
		t.Error("expected delete events to pass through")
	}
}

func newEvent(reportingController string) *eventsv1.Event {
	return &eventsv1.Event{ReportingController: reportingController}
}

func TestEventPredicate_createOtherController(t *testing.T) {
	p := EventPredicate(FieldManager)
	if !p.Create(event.CreateEvent{Object: newEvent("managed/object.kubernetes.m.crossplane.io")}) {
		t.Error("expected events of other controllers to pass through on create")
	}
}

func TestEventPredicate_createOwnEventFiltered(t *testing.T) {
	p := EventPredicate(FieldManager)
	if p.Create(event.CreateEvent{Object: newEvent(FieldManager)}) {
		t.Error("expected crsync's own events to be filtered out on create")
	}
}

func TestEventPredicate_updateOwnEventFiltered(t *testing.T) {
	ev := newEvent(FieldManager)
	p := EventPredicate(FieldManager)
	if p.Update(event.UpdateEvent{ObjectOld: ev, ObjectNew: ev}) {
		t.Error("expected crsync's own events to be filtered out on update")
	}
}

func TestEventPredicate_updateOtherController(t *testing.T) {
	ev := newEvent("other")
	p := EventPredicate(FieldManager)
	if !p.Update(event.UpdateEvent{ObjectOld: ev, ObjectNew: ev}) {
		t.Error("expected events of other controllers to pass through on update")
	}
}

func TestEventPredicate_deleteAlwaysFiltered(t *testing.T) {
	p := EventPredicate(FieldManager)
	if p.Delete(event.DeleteEvent{Object: newEvent("other")}) {
		t.Error("expected delete events to always be filtered out")
	}
}

func TestEventPredicate_nonEventObject(t *testing.T) {
	obj := unstructuredObj(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "a"},
	})

	p := EventPredicate(FieldManager)
	if p.Create(event.CreateEvent{Object: obj}) {
		t.Error("expected non-Event object to be filtered out")
	}
}
