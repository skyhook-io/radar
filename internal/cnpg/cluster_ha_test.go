package cnpg

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	metadatafake "k8s.io/client-go/metadata/fake"

	integration "github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

func TestCNPGQuorumArithmetic(t *testing.T) {
	ready := func(name string, ok bool) *corev1.Pod {
		st := corev1.ConditionFalse
		if ok {
			st = corev1.ConditionTrue
		}
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name}, Status: corev1.PodStatus{Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: st}}}}
	}
	pods := []*corev1.Pod{ready("a", true), ready("b", true), ready("c", false)}

	q := CNPGHAQuorum{Status: &CNPGHAQuorumStatus{StandbyNames: []string{"b", "c"}, StandbyNumber: 2, Primary: "a"}}
	cnpgQuorumArithmetic(&q, pods, true)
	if *q.R != 1 || !*q.Holds {
		t.Errorf("R=1 W=2 N=2 should hold: %+v", q)
	}

	reset := CNPGHAQuorum{Status: &CNPGHAQuorumStatus{StandbyNames: []string{}}}
	cnpgQuorumArithmetic(&reset, pods, true)
	if reset.N != nil || reset.Holds != nil {
		t.Errorf("a reset object records no configuration; nothing may be computed: %+v", reset)
	}

	unknownPods := CNPGHAQuorum{Status: &CNPGHAQuorumStatus{StandbyNames: []string{"b"}, StandbyNumber: 1}}
	cnpgQuorumArithmetic(&unknownPods, nil, false)
	if unknownPods.N == nil || unknownPods.R != nil || unknownPods.Holds != nil {
		t.Errorf("without Pods R and the verdict are unknown: %+v", unknownPods)
	}
}

func TestCNPGUncachedReasonSaysWhatIsUnknownAndWhy(t *testing.T) {
	if got := cnpgUncachedReason("Zones", "Nodes", "", k8score.KindReadAssessment{Readiness: k8score.KindUnavailable}); got != "Zones unknown: Radar is not collecting Nodes" {
		t.Errorf("uncached = %q", got)
	}
	if got := cnpgUncachedReason("Instance Jobs", "Jobs", "pg", k8score.KindReadAssessment{Readiness: k8score.KindReady, Requested: []string{"pg"}, Uncovered: []string{"pg"}}); got != "Instance Jobs unknown: Radar watches Jobs only in the namespaces it chose when it connected, and pg is not one of them" {
		t.Errorf("out of scope = %q", got)
	}
	if got := cnpgUncachedReason("Instance Jobs", "Jobs", "pg", k8score.KindReadAssessment{Readiness: k8score.KindPending, Requested: []string{"pg"}, Read: []string{}, Loading: []string{"pg"}}); got != "Instance Jobs unknown: Radar is still loading Jobs" {
		t.Errorf("syncing = %q", got)
	}
	// pg's own informer has synced while another namespace of the kind loads.
	if got := cnpgUncachedReason("Instance Jobs", "Jobs", "pg", k8score.KindReadAssessment{Readiness: k8score.KindFailed, Requested: []string{"pg"}, Read: []string{}, Failed: []string{"pg"}}); got != "Instance Jobs unknown: Radar failed to load Jobs" {
		t.Fatalf("failed reason = %q", got)
	}
	if got := cnpgUncachedReason("Instance Jobs", "Jobs", "pg", k8score.KindReadAssessment{Readiness: k8score.KindPending, Requested: []string{"pg"}, Read: []string{"pg"}, Complete: true}); got != "" {
		t.Errorf("a synced namespace reads while another loads = %q", got)
	}
	if got := cnpgUncachedReason("Zones", "Nodes", "", k8score.KindReadAssessment{Readiness: k8score.KindReady, AllNamespaces: true, Complete: true}); got != "" {
		t.Errorf("cached = %q", got)
	}
}

func TestCNPGHAJobSchedulerEvidence(t *testing.T) {
	job := cnpgJob("db", "analytics-1-initdb", "job-uid", clusterRef("analytics", "cluster-uid"))
	job.Status.Active = 1
	pod := cnpgJobPod("db", "analytics-1-initdb-abc", "analytics", jobRef(job.Name, string(job.UID)))
	got := cnpgHAJobOf(job, pod)
	if got.Phase != "pending" || !strings.Contains(got.Reason, "Pod cannot be scheduled: Unschedulable: 0/2 nodes") {
		t.Fatalf("job = %+v", got)
	}
	if got := cnpgHAJobOf(job); got.Phase != "active" {
		t.Fatalf("without Pod access: %+v", got)
	}
	pod.OwnerReferences[0].UID = "previous-job"
	if got := cnpgHAJobOf(job, pod); got.Phase != "active" {
		t.Fatalf("stale Job Pod adopted: %+v", got)
	}
	pod.OwnerReferences[0] = jobRef(job.Name, string(job.UID))
	pod.Status.Phase = corev1.PodRunning
	if got := cnpgHAJobOf(job, pod); got.Phase != "active" {
		t.Fatalf("active Job: %+v", got)
	}
	pending := pod.DeepCopy()
	pending.Status.Phase = corev1.PodPending
	for _, pods := range [][]*corev1.Pod{{pending, pod}, {pod, pending}} {
		if got := cnpgHAJobOf(job, pods...); got.Phase != "active" {
			t.Fatalf("Job with a running Pod: %+v", got)
		}
	}
}

func TestCNPGHAPrimaryBudgetProtection(t *testing.T) {
	for _, tc := range []struct {
		name       string
		instances  int
		mutate     func(*k8s.CNPGPDBTestFixture)
		protection string
		reason     string
	}{
		{"healthy HA", 2, nil, "expected", ""},
		{"single instance", 1, nil, "singleInstance", ""},
		{"primary node cordoned", 2, func(f *k8s.CNPGPDBTestFixture) { f.Nodes["node-1"].Spec.Unschedulable = true }, "blocking", "the primary's node is cordoned, draining or not ready"},
		{"standby left on a cordoned node", 2, func(f *k8s.CNPGPDBTestFixture) { f.Nodes["node-2"].Spec.Unschedulable = true }, "blocking", "until that node is uncordoned"},
		{"a Node missing from the cache leaves it unknown", 2, func(f *k8s.CNPGPDBTestFixture) { delete(f.Nodes, "node-2") }, "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(k8s.ResetTestState)
			t.Cleanup(k8s.ResetTestDynamicState)
			f := k8s.NewCNPGPDBTestFixture(tc.instances)
			if tc.mutate != nil {
				tc.mutate(f)
			}
			if err := f.Install(""); err != nil {
				t.Fatal(err)
			}
			resp := CNPGClusterHAResponse{Pods: integration.ReadSource{State: cnpgHAStateOK}, PDBs: activityReader().hAPDBs(context.Background(), k8s.GetResourceCache(), f.Cluster)}
			cnpgHAPrimaryProtection(&resp, k8s.GetResourceCache(), f.Cluster, f.Pods)
			got := resp
			var primary *CNPGHAPDB
			for i := range got.PDBs.Items {
				if got.PDBs.Items[i].Role == "primary" {
					primary = &got.PDBs.Items[i]
				}
			}
			if primary == nil {
				t.Fatalf("primary budget missing: %+v", got.PDBs)
			}
			if primary.Protection != tc.protection || !strings.Contains(primary.ProtectionReason, tc.reason) || (tc.reason == "") != (primary.ProtectionReason == "") {
				t.Fatalf("protection = %q (%q), want %q (%q)", primary.Protection, primary.ProtectionReason, tc.protection, tc.reason)
			}
		})
	}
}

// Through ClusterHA: the reading needs the same list nodes Issues checks,
// not only the get nodes the zones need.
func TestCNPGClusterHAPrimaryProtectionNeedsListNodes(t *testing.T) {
	for _, tc := range []struct {
		name              string
		denyList, denyGet bool
	}{{"list allowed", false, false}, {"list denied", true, false}, {"list allowed, get denied", false, true}} {
		denyList, denyGet := tc.denyList, tc.denyGet
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(k8s.ResetTestState)
			t.Cleanup(k8s.ResetTestDynamicState)
			f := k8s.NewCNPGPDBTestFixture(2)
			if err := f.Install(""); err != nil {
				t.Fatal(err)
			}
			reader := &Reader{
				Access: Access{CanRead: func(_ context.Context, _, resource, _, verb string) bool {
					return !(resource == "nodes" && ((denyList && verb == "list") || (denyGet && verb == "get")))
				}},
				Observations: Observations{
					OperatorScope: func(context.Context) []string { return nil },
					TypedScope: func(context.Context, *k8s.ResourceCache, []string, string, string) (integration.KindAccess, []string) {
						return integration.KindAccess{State: integration.KindCoverageUncached}, nil
					},
				},
			}
			clients := HAClients{Typed: fake.NewClientset(), Dynamic: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), Metadata: metadatafake.NewSimpleMetadataClient(metadatafake.NewTestScheme())}
			got := reader.ClusterHA(context.Background(), clients, k8s.GetResourceCache(), f.Cluster, time.Now())
			if (got.Nodes.State == cnpgHAStateOK) == denyGet {
				t.Fatalf("zones follow get nodes: %+v", got.Nodes)
			}
			want := "expected"
			if denyList {
				want = ""
			}
			for _, p := range got.PDBs.Items {
				if p.Role == "primary" && p.Protection == want {
					return
				}
			}
			t.Fatalf("primary protection want %q: %+v", want, got.PDBs.Items)
		})
	}
}
