package prometheus

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/rightsizing"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
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
		name           string
		edit           func(*corev1.Pod)
		eligible, zero bool
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
		{"termination retained", func(p *corev1.Pod) {
			p.Status.ContainerStatuses[0].LastTerminationState.Terminated = &corev1.ContainerStateTerminated{Reason: "OOMKilled"}
		}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := scanWorkload{replicas: 1, workload: rightsizingWorkload{podSpec: &base.Spec}}
			p := base.DeepCopy()
			tc.edit(p)
			w.workload.observeSizingPodIdentity(p)
			if hasCurrentPodSizingCohort(w) != tc.eligible || (w.workload.podZeroRestarts["agent"] == 1) != tc.zero {
				t.Fatalf("identities=%v, incomplete=%v, restart evidence=%v", w.workload.podIdentities, w.workload.podIdentityIncomplete, w.workload.podZeroRestarts)
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
	Initialize(nil, nil, "rightsizing-detail")
	t.Cleanup(func() { Reset(); Initialize(nil, nil, "") })
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
	workloads, _, _ := snapshotScanWorkloads(sizingTunnelTestContext(ctx), k8s.GetResourceCache(), map[string][]string{"DaemonSet": {"apps"}})
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
