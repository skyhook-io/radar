package topology

import (
	"fmt"
	"sort"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// stableCanaryCluster has one app split into stable and canary pods, with a
// Service selecting each track. The pods share app=web, so they group
// together even though no Service selects all of them.
func stableCanaryCluster(stable, canary int) *mockProvider {
	pod := func(name, track string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "team", Labels: map[string]string{"app": "web", "track": track}},
			Status:     corev1.PodStatus{Phase: corev1.PodRunning},
		}
	}
	var pods []*corev1.Pod
	for i := 0; i < stable; i++ {
		pods = append(pods, pod(fmt.Sprintf("stable-%d", i), "stable"))
	}
	for i := 0; i < canary; i++ {
		pods = append(pods, pod(fmt.Sprintf("canary-%d", i), "canary"))
	}
	service := func(track string) *corev1.Service {
		return &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: track, Namespace: "team"},
			Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "web", "track": track}},
		}
	}
	return &mockProvider{pods: pods, services: []*corev1.Service{service("stable"), service("canary")}}
}

func buildTraffic(t *testing.T, provider *mockProvider, mutate func(*BuildOptions)) *Topology {
	t.Helper()
	opts := DefaultBuildOptions()
	opts.ViewMode = ViewModeTraffic
	if mutate != nil {
		mutate(&opts)
	}
	topo, err := NewBuilder(provider).Build(opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return topo
}

func TestTrafficConnectsEachPodOnlyToServicesSelectingIt(t *testing.T) {
	topo := buildTraffic(t, stableCanaryCluster(1, 1), nil)
	var got []string
	for _, edge := range topo.Edges {
		if edge.Type == EdgeRoutesTo && nodeByID(topo.Nodes, edge.Target) != nil && nodeByID(topo.Nodes, edge.Target).Kind == KindPod {
			got = append(got, edge.Source+" -> "+edge.Target)
		}
	}
	sort.Strings(got)
	want := []string{"service/team/canary -> pod/team/canary-0", "service/team/stable -> pod/team/stable-0"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("service → pod edges = %v, want %v", got, want)
	}
}

func TestTrafficPodGroupSaysWhichPodsEachServiceSelects(t *testing.T) {
	topo := buildTraffic(t, stableCanaryCluster(4, 3), nil)
	var group *Node
	for i := range topo.Nodes {
		if topo.Nodes[i].Kind == KindPodGroup {
			group = &topo.Nodes[i]
		}
	}
	if group == nil {
		t.Fatal("seven pods should collapse into a PodGroup")
	}
	labels := map[string]string{}
	for _, edge := range topo.Edges {
		if edge.Target == group.ID {
			labels[edge.Source] = edge.Label
		}
	}
	if labels["service/team/stable"] != "4 of 7 pods" || labels["service/team/canary"] != "3 of 7 pods" {
		t.Errorf("group edge labels = %v", labels)
	}
	for _, pd := range group.Data["pods"].([]map[string]any) {
		want := "service/team/stable"
		if pd["name"].(string)[:6] == "canary" {
			want = "service/team/canary"
		}
		if ids, _ := pd["ownerIds"].([]string); len(ids) != 1 || ids[0] != want {
			t.Errorf("pod %v expands to %v, want [%s]", pd["name"], pd["ownerIds"], want)
		}
	}
}

func TestTrafficSummaryCountsOnlySelectedPods(t *testing.T) {
	topo := buildTraffic(t, stableCanaryCluster(4, 3), func(opts *BuildOptions) { opts.SummaryMode = true })
	for _, tc := range []struct {
		id    string
		total int
	}{{"service/team/stable", 4}, {"service/team/canary", 3}} {
		svc := nodeByID(topo.Nodes, tc.id)
		if svc == nil {
			t.Fatalf("%s missing", tc.id)
		}
		summary, _ := svc.Data["podSummary"].(map[string]any)
		if summary["total"] != tc.total {
			t.Errorf("%s podSummary = %v, want total %d", tc.id, summary, tc.total)
		}
	}
}
