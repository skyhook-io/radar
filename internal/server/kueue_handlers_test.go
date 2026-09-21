package server

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/internal/auth"
)

func admissionWorkload(root *unstructured.Unstructured, name string) *unstructured.Unstructured {
	w := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kueue.x-k8s.io/v1beta2", "kind": "Workload",
		"spec": map[string]any{"queueName": "training"},
		"status": map[string]any{"conditions": []any{map[string]any{
			"type": "QuotaReserved", "status": "False", "reason": "Pending", "message": "insufficient quota", "observedGeneration": int64(2),
		}}},
	}}
	w.SetNamespace(root.GetNamespace())
	w.SetName(name)
	w.SetUID(types.UID(name + "-uid"))
	w.SetGeneration(3)
	w.SetOwnerReferences([]metav1.OwnerReference{*metav1.NewControllerRef(root, root.GroupVersionKind())})
	return w
}

func TestKueueAdmissionExactOwner(t *testing.T) {
	root := testJobSet("ml", "training", "root-uid")
	for _, tc := range []struct {
		name   string
		mutate func(*unstructured.Unstructured)
	}{
		{"wrong UID", func(w *unstructured.Unstructured) {
			refs := w.GetOwnerReferences()
			refs[0].UID = "old-uid"
			w.SetOwnerReferences(refs)
		}},
		{"wrong name", func(w *unstructured.Unstructured) {
			refs := w.GetOwnerReferences()
			refs[0].Name = "other"
			w.SetOwnerReferences(refs)
		}},
		{"wrong version", func(w *unstructured.Unstructured) {
			refs := w.GetOwnerReferences()
			refs[0].APIVersion = "jobset.x-k8s.io/v1"
			w.SetOwnerReferences(refs)
		}},
		{"non-controller", func(w *unstructured.Unstructured) {
			refs := w.GetOwnerReferences()
			no := false
			refs[0].Controller = &no
			w.SetOwnerReferences(refs)
		}},
		{"wrong namespace", func(w *unstructured.Unstructured) { w.SetNamespace("other") }},
		{"foreign Workload", func(w *unstructured.Unstructured) { w.SetAPIVersion("example.io/v1beta2") }},
		{"label only", func(w *unstructured.Unstructured) {
			w.SetOwnerReferences(nil)
			w.SetLabels(map[string]string{"kueue.x-k8s.io/job-uid": "root-uid"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wrong := admissionWorkload(root, "wrong")
			tc.mutate(wrong)
			got := kueueAdmissionForJobSet(context.Background(), root, []*unstructured.Unstructured{wrong, admissionWorkload(root, "right"), nil}, nil)
			if got.Total != 1 || got.Workloads[0].Name != "right" {
				t.Fatalf("wrong association: %+v", got)
			}
		})
	}
}

func TestKueueAdmissionBoundedDeterministicRecords(t *testing.T) {
	root := testJobSet("ml", "training", "root-uid")
	var items []*unstructured.Unstructured
	for i := 11; i >= 0; i-- {
		w := admissionWorkload(root, fmt.Sprintf("slice-%02d", i))
		w.SetCreationTimestamp(metav1.NewTime(time.Unix(int64(i/2), 0)))
		items = append(items, w)
	}
	got := kueueAdmissionForJobSet(context.Background(), root, items, nil)
	if got.Total != 12 || !got.Truncated || len(got.Workloads) != 8 || got.Workloads[0].Name != "slice-10" || got.Workloads[1].Name != "slice-11" {
		t.Fatalf("bad bounded ordering: %+v", got)
	}
	observation := got.Workloads[0].Scheduling.Observations[0]
	if observation.Decision != "unsatisfied" || observation.PrimaryCondition.ObservedGeneration != 2 || observation.SubjectGeneration != 3 {
		t.Fatalf("lost evidence: %+v", observation)
	}
}

type admissionAccess map[string]bool

func (a admissionAccess) CanRead(_ context.Context, _, kind, _ string) bool { return a[kind] }

func TestKueueAdmissionPermissionFilteredRefsAndUnsupportedEvidence(t *testing.T) {
	root := testJobSet("ml", "training", "root-uid")
	w := admissionWorkload(root, "workload")
	got := kueueAdmissionForJobSet(context.Background(), root, []*unstructured.Unstructured{w}, admissionAccess{"Workload": true})
	entry := got.Workloads[0]
	queue := entry.Scheduling.Observations[0].Queues[0]
	if queue.Name != "training" || queue.Ref != nil || !entry.LinksLimited || entry.Ref == nil {
		t.Fatalf("bad ref filtering: %+v %+v", entry, queue)
	}
	got = kueueAdmissionForJobSet(context.Background(), root, []*unstructured.Unstructured{w}, admissionAccess{})
	if entry := got.Workloads[0]; entry.Ref != nil || entry.Scheduling != nil || entry.Projection != "forbidden" || entry.Name != "workload" {
		t.Fatalf("list-only should retain identity but withhold get detail: %+v", entry)
	}
	w.SetAPIVersion("kueue.x-k8s.io/v1beta1")
	got = kueueAdmissionForJobSet(context.Background(), root, []*unstructured.Unstructured{w}, nil)
	if got.Total != 1 || got.Workloads[0].Projection != "unsupported" {
		t.Fatalf("unsupported version is not absence: %+v", got)
	}
}

func TestProxyAuth_KueueAdmissionRejectsUnsupportedAndUnreadableRoot(t *testing.T) {
	env := newAuthTestServer(t)
	permissions := &auth.UserPermissions{AllowedNamespaces: []string{"default"}}
	permissions.SetCanI("get", "jobset.x-k8s.io", "jobsets", "default", false)
	env.srv.permCache.Set("alice", nil, permissions)
	for _, tc := range []struct {
		path   string
		status int
	}{
		{"/api/kueue/admission/jobs/default/training?group=batch", http.StatusBadRequest},
		{"/api/kueue/admission/jobsets/default/training?group=foreign.io", http.StatusBadRequest},
		{"/api/kueue/admission/jobsets/default/training?group=jobset.x-k8s.io", http.StatusForbidden},
		{"/api/kueue/admission/jobsets/private/training?group=jobset.x-k8s.io", http.StatusForbidden},
	} {
		resp := env.authGet(t, tc.path, "alice", "")
		resp.Body.Close()
		if resp.StatusCode != tc.status {
			t.Errorf("%s: got %d want %d", tc.path, resp.StatusCode, tc.status)
		}
	}
}
