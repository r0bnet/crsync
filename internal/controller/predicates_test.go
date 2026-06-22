package controller

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
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

func newEvent(apiVersion, kind string) *eventsv1.Event {
	return &eventsv1.Event{
		Regarding: corev1.ObjectReference{
			APIVersion: apiVersion,
			Kind:       kind,
		},
	}
}

func TestEventPredicate_createMatchingGVK(t *testing.T) {
	watched := map[schema.GroupVersionKind]struct{}{
		{Group: "apps", Version: "v1", Kind: "Deployment"}: {},
	}
	ev := newEvent("apps/v1", "Deployment")

	p := EventPredicate(watched)
	if !p.Create(event.CreateEvent{Object: ev}) {
		t.Error("expected matching event to pass through on create")
	}
}

func TestEventPredicate_createNonMatchingGVK(t *testing.T) {
	watched := map[schema.GroupVersionKind]struct{}{
		{Group: "apps", Version: "v1", Kind: "Deployment"}: {},
	}
	ev := newEvent("v1", "ConfigMap")

	p := EventPredicate(watched)
	if p.Create(event.CreateEvent{Object: ev}) {
		t.Error("expected non-matching event to be filtered out on create")
	}
}

func TestEventPredicate_updateMatchingGVK(t *testing.T) {
	watched := map[schema.GroupVersionKind]struct{}{
		{Group: "apps", Version: "v1", Kind: "Deployment"}: {},
	}
	ev := newEvent("apps/v1", "Deployment")

	p := EventPredicate(watched)
	if !p.Update(event.UpdateEvent{ObjectOld: ev, ObjectNew: ev}) {
		t.Error("expected matching event to pass through on update")
	}
}

func TestEventPredicate_deleteAlwaysFiltered(t *testing.T) {
	watched := map[schema.GroupVersionKind]struct{}{
		{Group: "apps", Version: "v1", Kind: "Deployment"}: {},
	}
	ev := newEvent("apps/v1", "Deployment")

	p := EventPredicate(watched)
	if p.Delete(event.DeleteEvent{Object: ev}) {
		t.Error("expected delete events to always be filtered out")
	}
}

func TestEventPredicate_genericMatchingGVK(t *testing.T) {
	watched := map[schema.GroupVersionKind]struct{}{
		{Group: "apps", Version: "v1", Kind: "Deployment"}: {},
	}
	ev := newEvent("apps/v1", "Deployment")

	p := EventPredicate(watched)
	if !p.Generic(event.GenericEvent{Object: ev}) {
		t.Error("expected matching event to pass through on generic")
	}
}

func TestEventPredicate_nonEventObject(t *testing.T) {
	watched := map[schema.GroupVersionKind]struct{}{
		{Group: "apps", Version: "v1", Kind: "Deployment"}: {},
	}
	obj := unstructuredObj(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "a"},
	})

	p := EventPredicate(watched)
	if p.Create(event.CreateEvent{Object: obj}) {
		t.Error("expected non-Event object to be filtered out")
	}
}

func newSecret(name string, secretType corev1.SecretType) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Type:       secretType,
	}
}

func TestSecretTypePredicate_emptyAllowsAll(t *testing.T) {
	p := SecretTypePredicate(nil)
	s := newSecret("a", corev1.SecretTypeOpaque)

	if !p.Create(event.CreateEvent{Object: s}) {
		t.Error("expected empty allowedTypes to allow all secrets on create")
	}
	if !p.Update(event.UpdateEvent{ObjectOld: s, ObjectNew: s}) {
		t.Error("expected empty allowedTypes to allow all secrets on update")
	}
	if !p.Delete(event.DeleteEvent{Object: s}) {
		t.Error("expected empty allowedTypes to allow all secrets on delete")
	}
}

func TestSecretTypePredicate_matchingType(t *testing.T) {
	p := SecretTypePredicate([]corev1.SecretType{corev1.SecretTypeOpaque, corev1.SecretTypeTLS})
	s := newSecret("a", corev1.SecretTypeTLS)

	if !p.Create(event.CreateEvent{Object: s}) {
		t.Error("expected matching type to pass on create")
	}
	if !p.Update(event.UpdateEvent{ObjectOld: s, ObjectNew: s}) {
		t.Error("expected matching type to pass on update")
	}
	if !p.Delete(event.DeleteEvent{Object: s}) {
		t.Error("expected matching type to pass on delete")
	}
}

func TestSecretTypePredicate_nonMatchingType(t *testing.T) {
	p := SecretTypePredicate([]corev1.SecretType{corev1.SecretTypeTLS})
	s := newSecret("a", corev1.SecretTypeOpaque)

	if p.Create(event.CreateEvent{Object: s}) {
		t.Error("expected non-matching type to be filtered on create")
	}
	if p.Update(event.UpdateEvent{ObjectOld: s, ObjectNew: s}) {
		t.Error("expected non-matching type to be filtered on update")
	}
	if p.Delete(event.DeleteEvent{Object: s}) {
		t.Error("expected non-matching type to be filtered on delete")
	}
}

func TestSecretTypePredicate_nonSecretObject(t *testing.T) {
	p := SecretTypePredicate([]corev1.SecretType{corev1.SecretTypeOpaque})
	obj := unstructuredObj(map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "a"},
	})

	if p.Create(event.CreateEvent{Object: obj}) {
		t.Error("expected non-Secret object to be filtered out")
	}
}

func secretWithAnnotations(annotations map[string]string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        "test-secret",
			Annotations: annotations,
		},
		Type: corev1.SecretTypeOpaque,
	}
}

func TestRequireAnnotation_withAnnotation(t *testing.T) {
	p := RequireAnnotation("example.com/required")
	s := secretWithAnnotations(map[string]string{
		"example.com/required": "some-value",
	})

	if !p.Create(event.CreateEvent{Object: s}) {
		t.Error("expected object with annotation to pass on create")
	}
	if !p.Update(event.UpdateEvent{ObjectOld: s, ObjectNew: s}) {
		t.Error("expected object with annotation to pass on update")
	}
	if !p.Delete(event.DeleteEvent{Object: s}) {
		t.Error("expected object with annotation to pass on delete")
	}
}

func TestRequireAnnotation_missingAnnotation(t *testing.T) {
	p := RequireAnnotation("example.com/required")
	s := secretWithAnnotations(nil)

	if p.Create(event.CreateEvent{Object: s}) {
		t.Error("expected object without annotation to be filtered on create")
	}
	if p.Update(event.UpdateEvent{ObjectOld: s, ObjectNew: s}) {
		t.Error("expected object without annotation to be filtered on update")
	}
	if p.Delete(event.DeleteEvent{Object: s}) {
		t.Error("expected object without annotation to be filtered on delete")
	}
}

func TestRequireAnnotation_emptyAnnotationValue(t *testing.T) {
	p := RequireAnnotation("example.com/required")
	s := secretWithAnnotations(map[string]string{
		"example.com/required": "",
	})

	if p.Create(event.CreateEvent{Object: s}) {
		t.Error("expected object with empty annotation value to be filtered")
	}
}

func TestRequireAnnotation_unrelatedAnnotation(t *testing.T) {
	p := RequireAnnotation("example.com/required")
	s := secretWithAnnotations(map[string]string{
		"example.com/other": "value",
	})

	if p.Create(event.CreateEvent{Object: s}) {
		t.Error("expected object with unrelated annotation to be filtered")
	}
}
