package prometheus

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/prom"
	"github.com/skyhook-io/radar/pkg/rightsizing"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func sizingTunnelTestContext(ctx context.Context) context.Context {
	var tunnel context.Context
	cloud.AuthenticatedTunnelHandler(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		tunnel = r.Context()
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil))
	return tunnel
}

func sizingIdentityPod() corev1.Pod {
	return corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "agent-a", UID: "aaaaaaaa-1111-2222-3333-444444444444", Labels: map[string]string{"app": "agent"}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "agent", Image: "agent:1", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")}}}}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}, ContainerStatuses: []corev1.ContainerStatus{{Name: "agent", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}},
	}
}

func TestSizingPodIdentityChecksAdmittedContainersAndRestartEvidence(t *testing.T) {
	base := sizingIdentityPod()
	for _, tc := range []struct {
		name            string
		edit            func(*corev1.Pod)
		eligible, known bool
	}{
		{"running", func(*corev1.Pod) {}, true, true},
		{"admission changed request", func(p *corev1.Pod) {
			p.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("200m")
		}, false, false},
		{"previous image", func(p *corev1.Pod) { p.Spec.Containers[0].Image = "agent:0" }, false, false},
		{"missing UID", func(p *corev1.Pod) { p.UID = "" }, false, false},
		{"not ready", func(p *corev1.Pod) { p.Status.Conditions = nil }, false, false},
		{"terminating", func(p *corev1.Pod) { now := metav1.Now(); p.DeletionTimestamp = &now }, false, false},
		{"status not reported", func(p *corev1.Pod) { p.Status.ContainerStatuses = nil }, true, false},
		{"container restarted", func(p *corev1.Pod) { p.Status.ContainerStatuses[0].RestartCount = 1 }, true, false},
		{"single clean restart", func(p *corev1.Pod) {
			p.Status.ContainerStatuses[0].RestartCount = 1
			p.Status.ContainerStatuses[0].LastTerminationState.Terminated = &corev1.ContainerStateTerminated{Reason: "Completed"}
		}, true, true},
		{"earlier termination lost", func(p *corev1.Pod) {
			p.Status.ContainerStatuses[0].RestartCount = 2
			p.Status.ContainerStatuses[0].LastTerminationState.Terminated = &corev1.ContainerStateTerminated{Reason: "Completed"}
		}, true, false},
		{"termination retained", func(p *corev1.Pod) {
			p.Status.ContainerStatuses[0].LastTerminationState.Terminated = &corev1.ContainerStateTerminated{Reason: "OOMKilled"}
		}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := scanWorkload{replicas: 1, workload: rightsizingWorkload{podSpec: &base.Spec}}
			p := base.DeepCopy()
			tc.edit(p)
			w.workload.observeSizingPodIdentity(context.Background(), nil, p)
			if hasCurrentPodSizingCohort(w) != tc.eligible || (w.workload.podRestartEvidence["agent"].knownPods == 1) != tc.known {
				t.Fatalf("identities=%v, incomplete=%v, restart evidence=%v", w.workload.podIdentities, w.workload.podIdentityIncomplete, w.workload.podRestartEvidence)
			}
		})
	}
}

func TestCurrentPodRightsizingDetailAndScanAgreeOnDaemonSetCohort(t *testing.T) {
	makeDS := func(name string, misscheduled int32, uid types.UID) (*appsv1.DaemonSet, *corev1.Pod) {
		p := sizingIdentityPod()
		p.Name = name + "-a"
		p.UID = uid
		p.OwnerReferences = []metav1.OwnerReference{scopeOwner("DaemonSet", name)}
		ds := &appsv1.DaemonSet{
			ObjectMeta: metav1.ObjectMeta{Namespace: p.Namespace, Name: name, UID: p.OwnerReferences[0].UID, Generation: 2},
			Spec:       appsv1.DaemonSetSpec{Selector: &metav1.LabelSelector{MatchLabels: p.Labels}, Template: corev1.PodTemplateSpec{Spec: p.Spec}},
			Status:     appsv1.DaemonSetStatus{ObservedGeneration: 2, DesiredNumberScheduled: 1, CurrentNumberScheduled: 1, UpdatedNumberScheduled: 1, NumberReady: 1, NumberMisscheduled: misscheduled},
		}
		return ds, &p
	}
	ds, p := makeDS("agent", 0, "aaaaaaaa-1111-2222-3333-444444444444")
	mixed, old := makeDS("mixed", 1, "bbbbbbbb-1111-2222-3333-444444444444")
	if err := k8s.InitTestResourceCache(fake.NewClientset(ds, p, mixed, old)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	Initialize(nil, nil, "rightsizing-detail", nil)
	t.Cleanup(func() { Reset(); Initialize(nil, nil, "", nil) })
	store, queries := bindingTestStore(t)
	connectBindingTestClient(t, store.URL)
	resolver := resolveRightsizingScope
	resolveRightsizingScope = func(context.Context, *Client, ViewScopeRequest, *k8s.ResourceCache) (ViewScope, error) {
		return ViewScope{State: ViewScopeUnlabeled}, nil
	}
	t.Cleanup(func() { resolveRightsizingScope = resolver })
	ctx := context.Background()
	resp, err := RightsizingForWorkload(sizingTunnelTestContext(ctx), "daemonset", ds.Namespace, ds.Name)
	if err != nil || resp.ClusterScope == nil || resp.ClusterScope.State != ViewScopeCurrentPodUIDs || resp.OwnerCoverage != rightsizing.OwnerCoverageCurrentPods || resp.SampleAvailable || !strings.Contains(resp.Reason, "UID") {
		t.Fatalf("detail=%+v err=%v", resp, err)
	}
	if len(queries()) != 3 {
		t.Fatalf("expected exactly the UID-bound range reads, got %v", queries())
	}
	for _, q := range queries() {
		if !strings.Contains(q, string(p.UID)) || strings.Contains(q, string(old.UID)) || strings.Contains(q, "kube_pod_owner") {
			t.Fatalf("incorrect cohort in query: %s", q)
		}
	}
	standalone, err := RightsizingForWorkload(ctx, "daemonset", ds.Namespace, ds.Name)
	if err != nil || standalone.ClusterScope == nil || standalone.ClusterScope.State != ViewScopeUnlabeled {
		t.Fatalf("standalone lost its existing history: %+v, %v", standalone, err)
	}
	for _, q := range queries()[3:] {
		if strings.Contains(q, "id=~") {
			t.Fatalf("standalone restricted to current UIDs: %s", q)
		}
	}
	workloads, _, _ := snapshotScanWorkloads(sizingTunnelTestContext(ctx), k8s.GetResourceCache(), map[string][]string{"DaemonSet": {"apps"}}, time.Now())
	if len(workloads) != 2 {
		t.Fatalf("scan snapshot: %+v", workloads)
	}
	for _, w := range workloads {
		detail, err := loadRightsizingWorkload(sizingTunnelTestContext(ctx), w.kind, w.namespace, w.name)
		if err != nil {
			t.Fatal(err)
		}
		expected := w.name == ds.Name
		single := scanWorkload{replicas: detail.replicas, workload: detail}
		if hasCurrentPodSizingCohort(w) != expected || hasCurrentPodSizingCohort(single) != expected {
			t.Errorf("wrong cohort eligibility on %s: scan=%v detail=%v", w.name, hasCurrentPodSizingCohort(w), hasCurrentPodSizingCohort(single))
		}
	}
}

func TestSizingRewrittenPodImagesUseControllerRevision(t *testing.T) {
	pod := sizingIdentityPod()
	template := *pod.Spec.DeepCopy()
	pod.Spec.Containers[0].Image = "mirror.internal/prod/agent@sha256:" + strings.Repeat("a", 64)
	rsOwner, ssOwner, dsOwner := scopeOwner("ReplicaSet", "wrk-123"), scopeOwner("StatefulSet", "db"), scopeOwner("DaemonSet", "collector")
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: pod.Namespace, Name: rsOwner.Name, UID: rsOwner.UID, Labels: map[string]string{appsv1.DefaultDeploymentUniqueLabelKey: "123"}}, Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{Spec: template}}}
	ss := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Namespace: pod.Namespace, Name: ssOwner.Name, UID: ssOwner.UID}, Spec: appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{Spec: template}}, Status: appsv1.StatefulSetStatus{UpdateRevision: "db-abc"}}
	cache := scopeTestCache(t, map[string]bool{k8score.ReplicaSets: true, k8score.StatefulSets: true}, rs, ss)
	spec, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&template)
	if err != nil {
		t.Fatal(err)
	}
	gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "controllerrevisions"}
	r := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apps/v1", "kind": "ControllerRevision", "metadata": map[string]any{"namespace": pod.Namespace, "name": "collector-456"}, "data": map[string]any{"spec": map[string]any{"template": map[string]any{"spec": spec}}}}}
	r.SetOwnerReferences([]metav1.OwnerReference{dsOwner})
	r.SetLabels(map[string]string{appsv1.ControllerRevisionHashLabelKey: "456"})
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "ControllerRevisionList"}, r)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: "apps", Version: "v1", Kind: "ControllerRevision", Name: "controllerrevisions", Namespaced: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	for _, tc := range []struct {
		owner        metav1.OwnerReference
		label, value string
	}{{rsOwner, appsv1.DefaultDeploymentUniqueLabelKey, "123"}, {ssOwner, appsv1.ControllerRevisionHashLabelKey, "db-abc"}, {dsOwner, appsv1.ControllerRevisionHashLabelKey, "456"}} {
		t.Run(tc.owner.Kind, func(t *testing.T) {
			for _, check := range []string{"current", "old hash", "recreated owner", "different spec", "admission changed requests"} {
				p := pod.DeepCopy()
				p.OwnerReferences = []metav1.OwnerReference{tc.owner}
				p.Labels[tc.label] = tc.value
				workload := rightsizingWorkload{podSpec: template.DeepCopy()}
				switch check {
				case "old hash":
					p.Labels[tc.label] = "previous"
				case "recreated owner":
					p.OwnerReferences[0].UID = "other"
				case "different spec":
					workload.podSpec.Containers[0].Command = []string{"other"}
				case "admission changed requests":
					p.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("200m")
				}
				workload.observeSizingPodIdentity(context.Background(), cache, p)
				if check == "current" && len(workload.podIdentities) == 1 {
					sibling := p.DeepCopy()
					sibling.Name = "another"
					sibling.UID = "bbbbbbbb-1111-2222-3333-444444444444"
					sibling.Spec.Containers[0].Image += "different"
					workload.observeSizingPodIdentity(context.Background(), cache, sibling)
					if !workload.podIdentityIncomplete {
						t.Error("mixed admitted images accepted")
					}
				}
				if got := len(workload.podIdentities) == 1; got != (check == "current") {
					t.Errorf("%s: eligible %t", check, got)
				}
			}
		})
	}
}

func TestUIDHistoryDeadlineHasItsOwnReason(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	result := currentPodRightsizingDetail(ctx, &fakeScanQuerier{}, currentSizingWorkload())
	if result.SampleAvailable || !strings.Contains(result.Reason, "timed out") {
		t.Fatalf("unexpected timeout response: %+v", result)
	}
	for _, r := range result.Rows {
		if r.Decision.Status == rightsizing.DecisionActionable {
			t.Fatalf("timeout produced a recommendation: %+v", r)
		}
	}
	result = currentPodRightsizingDetail(context.Background(), &fakeScanQuerier{rangeFn: func(string) (*prom.QueryResult, error) { return nil, context.DeadlineExceeded }}, currentSizingWorkload())
	if !strings.Contains(result.Reason, "timed out") || len(result.Warnings) != 3 || result.Warnings[0].Code != "cpu_query_timed_out" {
		t.Fatalf("upstream timeout: %+v", result)
	}
}
