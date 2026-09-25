package server

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/skyhook-io/radar/internal/k8s"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"net/http"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/pkg/k8score"
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
			got := kueueAdmissionForRoot(context.Background(), root, []*unstructured.Unstructured{wrong, admissionWorkload(root, "right"), nil}, nil)
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
	got := kueueAdmissionForRoot(context.Background(), root, items, nil)
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
	got := kueueAdmissionForRoot(context.Background(), root, []*unstructured.Unstructured{w}, admissionAccess{"Workload": true})
	entry := got.Workloads[0]
	queue := entry.Scheduling.Observations[0].Queues[0]
	if queue.Name != "training" || queue.Ref != nil || !entry.LinksLimited || entry.Ref == nil {
		t.Fatalf("bad ref filtering: %+v %+v", entry, queue)
	}
	got = kueueAdmissionForRoot(context.Background(), root, []*unstructured.Unstructured{w}, admissionAccess{})
	if entry := got.Workloads[0]; entry.Ref != nil || entry.Scheduling != nil || entry.LinksLimited || entry.Projection != "forbidden" || entry.Name != "workload" {
		t.Fatalf("list-only should retain identity but withhold get detail: %+v", entry)
	}
	w.SetAPIVersion("kueue.x-k8s.io/v1beta1")
	got = kueueAdmissionForRoot(context.Background(), root, []*unstructured.Unstructured{w}, nil)
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
		{"/api/kueue/admission/jobs/default/training?group=batch", http.StatusForbidden},
		{"/api/kueue/admission/jobs/default/training?group=batch.volcano.sh", http.StatusBadRequest},
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

func TestKueueAdmissionRecreatedRoot(t *testing.T) {
	oldRoot := testJobSet("ml", "training", "old-uid")
	newRoot := testJobSet("ml", "training", "new-uid")
	got := kueueAdmissionForRoot(context.Background(), newRoot, []*unstructured.Unstructured{admissionWorkload(oldRoot, "old-workload")}, nil)
	if got.UID != "new-uid" || got.Total != 0 || len(got.Workloads) != 0 {
		t.Fatalf("recreated root received old admission evidence: %+v", got)
	}
}

func provisioningRequest(root *unstructured.Unstructured, name string) *unstructured.Unstructured {
	p := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "autoscaling.x-k8s.io/v1", "kind": "ProvisioningRequest",
		"spec":   map[string]any{"provisioningClassName": "test.invalid", "parameters": map[string]any{"omit": "large provider data"}},
		"status": map[string]any{"conditions": []any{map[string]any{"type": "Failed", "status": "True", "message": "capacity unavailable"}}},
	}}
	p.SetName(name)
	p.SetNamespace(root.GetNamespace())
	p.SetUID(types.UID(name))
	p.SetGeneration(1)
	p.SetOwnerReferences([]metav1.OwnerReference{*metav1.NewControllerRef(root, root.GroupVersionKind())})
	return p
}

func TestKueueProvisioningOwnershipAndSummary(t *testing.T) {
	root := admissionWorkload(testJobSet("ml", "job", "job-uid"), "workload")
	for _, tc := range []struct {
		name   string
		mutate func(*unstructured.Unstructured)
	}{
		{"previous UID", func(p *unstructured.Unstructured) {
			refs := p.GetOwnerReferences()
			refs[0].UID = "old"
			p.SetOwnerReferences(refs)
		}},
		{"same name other group", func(p *unstructured.Unstructured) {
			refs := p.GetOwnerReferences()
			refs[0].APIVersion = "foreign.io/v1"
			p.SetOwnerReferences(refs)
		}},
		{"wrong kind", func(p *unstructured.Unstructured) {
			refs := p.GetOwnerReferences()
			refs[0].Kind = "Job"
			p.SetOwnerReferences(refs)
		}},
		{"wrong name", func(p *unstructured.Unstructured) {
			refs := p.GetOwnerReferences()
			refs[0].Name = "different"
			p.SetOwnerReferences(refs)
		}},
		{"not controller", func(p *unstructured.Unstructured) {
			refs := p.GetOwnerReferences()
			no := false
			refs[0].Controller = &no
			p.SetOwnerReferences(refs)
		}},
		{"other namespace", func(p *unstructured.Unstructured) { p.SetNamespace("other") }},
		{"other resource group", func(p *unstructured.Unstructured) { p.SetAPIVersion("foreign.io/v1") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			wrong := provisioningRequest(root, "wrong")
			tc.mutate(wrong)
			right := provisioningRequest(root, "right")
			refs := right.GetOwnerReferences()
			refs[0].APIVersion = "kueue.x-k8s.io/v1beta1"
			right.SetOwnerReferences(refs)
			got := provisioningForWorkload(root, []*unstructured.Unstructured{nil, wrong, right})
			if got.Total != 1 || len(got.Requests) != 1 || got.Requests[0].GetName() != "right" {
				t.Fatalf("bad association: %+v", got)
			}
			if _, found, _ := unstructured.NestedMap(got.Requests[0].Object, "spec", "parameters"); found {
				t.Fatal("summary leaked provider parameters")
			}
			if got.UID != string(root.GetUID()) || !got.Installed {
				t.Fatalf("bad root provenance: %+v", got)
			}
		})
	}
}

func TestKueueProvisioningBoundedAndCleanedUp(t *testing.T) {
	root := admissionWorkload(testJobSet("ml", "job", "uid"), "workload")
	var items []*unstructured.Unstructured
	for i := 0; i < 25; i++ {
		p := provisioningRequest(root, fmt.Sprintf("p-%02d", i))
		p.SetCreationTimestamp(metav1.NewTime(time.Unix(int64(i), 0)))
		items = append(items, p)
	}
	got := provisioningForWorkload(root, items)
	if got.Total != 25 || !got.Truncated || len(got.Requests) != 20 || got.Requests[0].GetName() != "p-24" {
		t.Fatalf("bad bounded result: %+v", got)
	}
	empty := provisioningForWorkload(root, nil)
	if empty.Requests == nil || empty.Total != 0 || !empty.Installed || empty.Truncated {
		t.Fatalf("bad cleaned-up result: %+v", empty)
	}
	root.SetUID("")
	if got := provisioningForWorkload(root, items); got.Total != 0 {
		t.Fatal("matched without root UID")
	}
}

func TestProxyAuth_KueueProvisioningPermissions(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		root, list, installed bool
		namespace             string
		status                int
	}{
		{"root denied", false, true, true, "default", 403},
		{"list denied", true, false, true, "default", 403},
		{"namespace denied", true, true, true, "private", 403},
		{"API absent without grant", true, false, false, "default", 200},
		{"authorized lookup", true, true, true, "default", 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := admissionWorkload(testJobSet("default", "job", "uid"), "training")
			workloadGVR := schema.GroupVersionResource{Group: kueueGroup, Version: "v1beta2", Resource: "workloads"}
			requestGVR := schema.GroupVersionResource{Group: provisioningGroup, Version: "v1", Resource: "provisioningrequests"}
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{workloadGVR: "WorkloadList", requestGVR: "ProvisioningRequestList"}, root)
			resources := []k8s.APIResource{{Group: kueueGroup, Version: "v1beta2", Name: "workloads", Kind: "Workload", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}}}
			if tc.installed {
				resources = append(resources, k8s.APIResource{Group: provisioningGroup, Version: "v1", Name: "provisioningrequests", Kind: "ProvisioningRequest", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}})
			}
			if err := k8s.InitTestDynamicResourceCache(dyn, resources); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(k8s.ResetTestDynamicState)
			cache := k8s.GetDynamicResourceCache()
			discovered := make(chan struct{})
			cache.OnCRDDiscoveryComplete(func() { close(discovered) })
			cache.DiscoverAllCRDs()
			select {
			case <-discovered:
			case <-time.After(5 * time.Second):
				t.Fatal("discovery did not complete")
			}
			if err := cache.EnsureWatching(workloadGVR); err != nil {
				t.Fatal(err)
			}
			if !cache.WaitForSync(workloadGVR, 5*time.Second) {
				t.Fatal("Workload cache did not sync")
			}
			env := newAuthTestServer(t)
			permissions := &auth.UserPermissions{AllowedNamespaces: []string{"default"}}
			permissions.SetCanI("get", kueueGroup, "workloads", tc.namespace, tc.root)
			permissions.SetCanI("list", provisioningGroup, "provisioningrequests", tc.namespace, tc.list)
			env.srv.permCache.Set("alice", nil, permissions)
			response := env.authGet(t, "/api/kueue/provisioning/"+tc.namespace+"/training", "alice", "")
			defer response.Body.Close()
			if response.StatusCode != tc.status {
				t.Fatalf("got %d want %d", response.StatusCode, tc.status)
			}
			if tc.status == 200 {
				var got KueueProvisioningResponse
				if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
					t.Fatal(err)
				}
				if got.Installed != tc.installed || got.UID != string(root.GetUID()) || got.Requests == nil {
					t.Fatalf("bad observation: %+v", got)
				}
			}
		})
	}
}

func TestJobAdmissionOwnershipAndParent(t *testing.T) {
	root := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "batch/v1", "kind": "Job", "metadata": map[string]any{"namespace": "ml", "name": "train", "uid": "current"}}}
	parent := testJobSet("ml", "parent", "parent-uid")
	root.SetOwnerReferences([]metav1.OwnerReference{*metav1.NewControllerRef(parent, parent.GroupVersionKind())})
	for _, mutate := range []func(*unstructured.Unstructured){
		func(w *unstructured.Unstructured) {
			w.SetOwnerReferences(nil)
			w.SetLabels(map[string]string{"kueue.x-k8s.io/job-uid": "current"})
		},
		func(w *unstructured.Unstructured) {
			refs := w.GetOwnerReferences()
			refs[0].UID = "old"
			w.SetOwnerReferences(refs)
		},
		func(w *unstructured.Unstructured) {
			refs := w.GetOwnerReferences()
			refs[0].APIVersion = "batch.volcano.sh/v1alpha1"
			w.SetOwnerReferences(refs)
		},
		func(w *unstructured.Unstructured) {
			refs := w.GetOwnerReferences()
			refs[0].Name = "other"
			w.SetOwnerReferences(refs)
		},
		func(w *unstructured.Unstructured) {
			refs := w.GetOwnerReferences()
			refs[0].Controller = nil
			w.SetOwnerReferences(refs)
		},
		func(w *unstructured.Unstructured) { w.SetNamespace("other") },
	} {
		bad := admissionWorkload(root, "bad")
		mutate(bad)
		got := kueueAdmissionForRoot(context.Background(), root, []*unstructured.Unstructured{bad, admissionWorkload(root, "owned"), admissionWorkload(parent, "parent-owned")}, admissionAccess{"JobSet": true, "Workload": true})
		if got.Total != 1 || got.Workloads[0].Name != "owned" {
			t.Fatalf("bad association: %+v", got)
		}
	}
	yes := true
	root.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "CronJob", Name: "schedule", UID: "cron", Controller: &yes}})
	got := kueueAdmissionForRoot(context.Background(), root, []*unstructured.Unstructured{admissionWorkload(root, "cron-child")}, nil)
	if got.Total != 1 {
		t.Fatal("CronJob owner prevented direct admission")
	}
}

func TestProxyAuth_JobAdmissionEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		root, list, installed bool
		namespace, jobName    string
		status                int
	}{
		{"authorized", true, true, true, "default", "training", 200},
		{"root denied", false, true, true, "default", "training", 403},
		{"list denied", true, false, true, "default", "training", 403},
		{"namespace denied", true, true, true, "private", "training", 403},
		{"absent API", true, false, false, "default", "training", 200},
		{"missing Job", true, true, true, "default", "missing", 404},
		{"connection switched", true, true, true, "default", "training", 503},
		{"cache uncovered", true, true, true, "default", "training", 403},
		{"cache warming", true, true, true, "default", "training", 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "training", Namespace: "default", UID: "job-uid"}}
			useTestResourceCache(t, fake.NewClientset(job))
			root := &unstructured.Unstructured{}
			root.SetAPIVersion("batch/v1")
			root.SetKind("Job")
			root.SetName(job.Name)
			root.SetNamespace(job.Namespace)
			root.SetUID(job.UID)
			gvr := schema.GroupVersionResource{Group: kueueGroup, Version: "v1beta2", Resource: "workloads"}
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WorkloadList"}, admissionWorkload(root, "owned"))
			if tc.name == "connection switched" {
				for i := 0; i < 101; i++ {
					if err := dyn.Tracker().Add(admissionWorkload(root, fmt.Sprintf("other-%d", i))); err != nil {
						t.Fatal(err)
					}
				}
			}

			var resources []k8s.APIResource
			if tc.installed {
				resources = append(resources, k8s.APIResource{Group: kueueGroup, Version: "v1beta2", Name: "workloads", Kind: "Workload", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}})
			}
			if err := k8s.InitTestDynamicResourceCache(dyn, resources); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(k8s.ResetTestDynamicState)
			cache := k8s.GetDynamicResourceCache()
			discovered := make(chan struct{})
			cache.OnCRDDiscoveryComplete(func() { close(discovered) })
			cache.DiscoverAllCRDs()
			select {
			case <-discovered:
			case <-time.After(5 * time.Second):
				t.Fatal("discovery did not complete")
			}
			if tc.name == "connection switched" {
				dyn.PrependReactor("list", "workloads", func(k8stesting.Action) (bool, runtime.Object, error) {
					k8s.CancelOngoingOperations()
					return false, nil, nil
				})
			}

			if tc.name == "cache uncovered" {
				k8s.ResetResourceCache()
				if err := k8s.InitScopedTestResourceCache(fake.NewClientset(job), map[string]k8score.ResourceScope{k8score.Jobs: {Enabled: true, Namespace: "elsewhere"}}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.name == "cache warming" {
				k8s.ResetResourceCache()
				if err := k8s.InitTestPromotedSyncingCache(fake.NewClientset(job), 5*time.Second, 5*time.Second, map[string]time.Duration{"jobs": 3 * time.Second}); err != nil {
					t.Fatal(err)
				}
			}

			env := newAuthTestServer(t)
			permissions := &auth.UserPermissions{AllowedNamespaces: []string{"default"}}
			permissions.SetCanI("get", "batch", "jobs", tc.namespace, tc.root)
			permissions.SetCanI("list", kueueGroup, "workloads", tc.namespace, tc.list)
			permissions.SetCanI("get", kueueGroup, "workloads", tc.namespace, false)
			env.srv.permCache.Set("alice", nil, permissions)
			response := env.authGet(t, "/api/kueue/admission/jobs/"+tc.namespace+"/"+tc.jobName+"?group=batch", "alice", "")
			defer response.Body.Close()
			if response.StatusCode != tc.status {
				t.Fatalf("got %d want %d", response.StatusCode, tc.status)
			}
			if tc.status == 200 {
				var got KueueAdmissionResponse
				if err := json.NewDecoder(response.Body).Decode(&got); err != nil {
					t.Fatal(err)
				}
				if got.UID != "job-uid" || got.Installed != tc.installed || got.Workloads == nil {
					t.Fatalf("bad observation: %+v", got)
				}
				if tc.installed && (got.Total != 1 || got.Workloads[0].Name != "owned" || got.Workloads[0].Ref != nil) {
					t.Fatalf("bad ownership or unauthorized link: %+v", got)
				}
			}
		})
	}
}
