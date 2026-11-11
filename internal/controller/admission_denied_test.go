package controller

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/cluster"
)

var (
	webhookDenied = apierrors.NewForbidden(testGVK.GroupVersion().WithResource("testresources").GroupResource(), "my-cr",
		errors.New(`admission webhook "size.example.org" denied the request: size must be small`))
	policyDenied = apierrors.NewInvalid(testGVK.GroupKind(), "my-cr",
		field.ErrorList{field.Invalid(field.NewPath("spec", "size"), "huge", "size must be small")})
)

// rejectingHub is a hub client that fails every apply with err while err is set.
type rejectingHub struct {
	client.Client
	err     error
	applies int
}

func newRejectingHub(err error) *rejectingHub {
	h := &rejectingHub{err: err}
	h.Client = fake.NewClientBuilder().WithScheme(testScheme).WithInterceptorFuncs(interceptor.Funcs{
		Apply: func(ctx context.Context, c client.WithWatch, obj runtime.ApplyConfiguration, opts ...client.ApplyOption) error {
			h.applies++
			if h.err != nil {
				return h.err
			}
			return c.Apply(ctx, obj, opts...)
		},
	}).Build()
	return h
}

func spokeSource(t *testing.T, sc client.Client) *unstructured.Unstructured {
	t.Helper()
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(testGVK)
	err := sc.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "my-cr"}, obj)
	if err != nil {
		t.Fatalf("get spoke source: %v", err)
	}
	return obj
}

func TestReconcile_admissionDeniedReportedOnSpoke(t *testing.T) {
	for name, denied := range map[string]error{"webhook": webhookDenied, "policy": policyDenied} {
		t.Run(name, func(t *testing.T) {
			sc := spokeClientWithStatus(newSpokeResource("default", "my-cr", map[string]any{"size": "huge"}, nil))
			hc := newRejectingHub(denied)
			ctx := context.Background()
			r := NewCRSyncFromSpokeReconciler(hc, &fakeClusterProvider{
				clusters: map[string]cluster.Cluster{"spoke1": &fakeCluster{client: sc}},
			}, testGVK, events.NewFakeRecorder(10))

			_, err := r.Reconcile(ctx, newRequest("spoke1", "default", "my-cr")) // adds the finalizer
			if err != nil {
				t.Fatalf("unexpected error adding the finalizer: %v", err)
			}
			result, err := r.Reconcile(ctx, newRequest("spoke1", "default", "my-cr"))
			if err != nil {
				t.Fatalf("a rejection must not be returned as an error, got: %v", err)
			}
			if hc.applies == 0 {
				t.Fatal("precondition: the hub apply was never attempted")
			}
			if result.RequeueAfter != deniedRetryInterval {
				t.Errorf("expected a slow retry after %v, got %v", deniedRetryInterval, result.RequeueAfter)
			}

			evs := spokeEvents(t, sc)
			if len(evs) != 1 {
				t.Fatalf("expected one Event on the spoke, got %d", len(evs))
			}
			ev := evs[0]
			if ev.Reason != ReasonAdmissionDenied || ev.Type != corev1.EventTypeWarning || ev.Regarding.Name != "my-cr" {
				t.Errorf("unexpected Event: reason=%q type=%q regarding=%q", ev.Reason, ev.Type, ev.Regarding.Name)
			}
			if !strings.Contains(ev.Note, "size must be small") {
				t.Errorf("Event note %q does not carry the hub's message", ev.Note)
			}

			cond := acceptedCondition(spokeSource(t, sc))
			if cond == nil {
				t.Fatal("expected an Accepted condition on the spoke resource")
			}
			if cond["status"] != "False" || cond["reason"] != ReasonAdmissionDenied || cond["message"] != ev.Note {
				t.Errorf("unexpected Accepted condition: %v", cond)
			}

			// A retry with the same rejection doesn't repeat the Event.
			_, err = r.Reconcile(ctx, newRequest("spoke1", "default", "my-cr"))
			if err != nil {
				t.Fatalf("unexpected error on retry: %v", err)
			}
			if n := len(spokeEvents(t, sc)); n != 1 {
				t.Errorf("expected the retry to reuse the Event, got %d Events", n)
			}

			// Once the hub accepts the copy, the condition goes away.
			hc.err = nil
			result, err = r.Reconcile(ctx, newRequest("spoke1", "default", "my-cr"))
			if err != nil {
				t.Fatalf("unexpected error once accepted: %v", err)
			}
			if result.RequeueAfter != 0 {
				t.Errorf("unexpected requeue once accepted: %v", result)
			}
			if cond := acceptedCondition(spokeSource(t, sc)); cond != nil {
				t.Errorf("expected the Accepted condition to be removed, got %v", cond)
			}
		})
	}
}

func TestReconcile_admissionDeniedWithoutStatusSubresource(t *testing.T) {
	sc := spokeClient(newSpokeResource("default", "my-cr", map[string]any{"size": "huge"}, nil))
	ctx := context.Background()
	r := NewCRSyncFromSpokeReconciler(newRejectingHub(webhookDenied), &fakeClusterProvider{
		clusters: map[string]cluster.Cluster{"spoke1": &fakeCluster{client: sc}},
	}, testGVK, events.NewFakeRecorder(10))

	_, err := r.Reconcile(ctx, newRequest("spoke1", "default", "my-cr")) // adds the finalizer
	if err != nil {
		t.Fatalf("unexpected error adding the finalizer: %v", err)
	}
	_, err = r.Reconcile(ctx, newRequest("spoke1", "default", "my-cr"))
	if err != nil {
		t.Fatalf("a type without status must still get the Event, got error: %v", err)
	}
	if n := len(spokeEvents(t, sc)); n != 1 {
		t.Errorf("expected one Event on the spoke, got %d", n)
	}
}

func TestReconcile_otherApplyErrorsAreRetried(t *testing.T) {
	sc := spokeClientWithStatus(newSpokeResource("default", "my-cr", map[string]any{"size": "huge"}, nil))
	ctx := context.Background()
	r := NewCRSyncFromSpokeReconciler(newRejectingHub(apierrors.NewInternalError(errors.New("etcd down"))), &fakeClusterProvider{
		clusters: map[string]cluster.Cluster{"spoke1": &fakeCluster{client: sc}},
	}, testGVK, events.NewFakeRecorder(10))

	_, err := r.Reconcile(ctx, newRequest("spoke1", "default", "my-cr")) // adds the finalizer
	if err != nil {
		t.Fatalf("unexpected error adding the finalizer: %v", err)
	}
	_, err = r.Reconcile(ctx, newRequest("spoke1", "default", "my-cr"))
	if err == nil {
		t.Fatal("expected a transient hub error to be returned for a fast retry")
	}
	if n := len(spokeEvents(t, sc)); n != 0 {
		t.Errorf("expected no Event for a transient error, got %d", n)
	}
	if cond := acceptedCondition(spokeSource(t, sc)); cond != nil {
		t.Errorf("expected no Accepted condition for a transient error, got %v", cond)
	}
}

func TestSetAcceptedCondition(t *testing.T) {
	denied := func(msg string) map[string]any {
		return map[string]any{"type": ConditionAccepted, "status": "False", "reason": ReasonAdmissionDenied, "message": msg}
	}
	obj := newSpokeResource("default", "my-cr", nil, nil)
	obj.Object["status"] = map[string]any{"conditions": []any{
		map[string]any{"type": "Ready", "status": "True"},
	}}

	if !setAcceptedCondition(obj, denied("first")) {
		t.Fatal("expected adding the condition to change the object")
	}
	since := acceptedCondition(obj)["lastTransitionTime"]
	if since == nil {
		t.Fatal("expected lastTransitionTime to be set")
	}
	if setAcceptedCondition(obj, denied("first")) {
		t.Error("expected an identical condition to leave the object unchanged")
	}

	obj.Object["status"].(map[string]any)["conditions"].([]any)[1].(map[string]any)["lastTransitionTime"] = "2020-01-01T00:00:00Z"
	if !setAcceptedCondition(obj, denied("second")) {
		t.Fatal("expected a new message to change the object")
	}
	if got := acceptedCondition(obj)["lastTransitionTime"]; got != "2020-01-01T00:00:00Z" {
		t.Errorf("expected lastTransitionTime to stay while the status is unchanged, got %v", got)
	}

	if !setAcceptedCondition(obj, nil) {
		t.Fatal("expected removing the condition to change the object")
	}
	conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if len(conditions) != 1 || conditions[0].(map[string]any)["type"] != "Ready" {
		t.Errorf("expected only the type's own Ready condition to remain, got %v", conditions)
	}

	odd := newSpokeResource("default", "my-cr", nil, nil)
	odd.Object["status"] = map[string]any{"conditions": "not a list"}
	if setAcceptedCondition(odd, denied("first")) {
		t.Error("expected conditions that aren't a list to be left alone")
	}
}

func TestHubReconcile_keepsAcceptedCondition(t *testing.T) {
	sourceObj := newSpokeResource("default", "my-res", nil, nil)
	sourceObj.Object["status"] = map[string]any{"conditions": []any{
		map[string]any{"type": ConditionAccepted, "status": "False", "reason": ReasonAdmissionDenied, "message": "no", "lastTransitionTime": "2020-01-01T00:00:00Z"},
	}}
	hubCopy := newHubCopy("spoke1--default", "my-res",
		map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}},
		map[string]string{
			LabelSourceCluster:   "spoke1",
			LabelSourceNamespace: "default",
			LabelSourceName:      "my-res",
		},
	)
	sc := spokeClientWithStatus(sourceObj)
	r := NewCRSyncFromHubReconciler(hubClientWith(hubCopy), &fakeClusterProvider{
		clusters: map[string]cluster.Cluster{"spoke1": &fakeCluster{client: sc}},
	}, testGVK)

	_, err := r.Reconcile(context.Background(), newRequest("", "spoke1--default", "my-res"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := &unstructured.Unstructured{}
	got.SetGroupVersionKind(testGVK)
	err = sc.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "my-res"}, got)
	if err != nil {
		t.Fatalf("get source: %v", err)
	}
	conditions, _, _ := unstructured.NestedSlice(got.Object, "status", "conditions")
	if len(conditions) != 2 {
		t.Fatalf("expected the hub's Ready and crsync's Accepted condition, got %v", conditions)
	}
	cond := acceptedCondition(got)
	if cond == nil || cond["lastTransitionTime"] != "2020-01-01T00:00:00Z" {
		t.Errorf("expected the Accepted condition to survive unchanged, got %v", cond)
	}
}
