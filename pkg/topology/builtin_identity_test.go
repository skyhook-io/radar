package topology

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuiltInNeighborhoodAPIGroups(t *testing.T) {
	// Typed informer objects need not retain TypeMeta. The builder knows
	// their API version from the typed provider, not from object metadata.
	meta := metav1.ObjectMeta{Namespace: "default", Name: "example"}
	provider := &mockProvider{
		deployments:     []*appsv1.Deployment{{ObjectMeta: meta}},
		daemonSets:      []*appsv1.DaemonSet{{ObjectMeta: meta}},
		statefulSets:    []*appsv1.StatefulSet{{ObjectMeta: meta}},
		replicaSets:     []*appsv1.ReplicaSet{{ObjectMeta: meta}},
		jobs:            []*batchv1.Job{{ObjectMeta: meta}},
		cronJobs:        []*batchv1.CronJob{{ObjectMeta: meta}},
		ingresses:       []*networkingv1.Ingress{{ObjectMeta: meta}},
		networkPolicies: []*networkingv1.NetworkPolicy{{ObjectMeta: meta}},
		hpas:            []*autoscalingv2.HorizontalPodAutoscaler{{ObjectMeta: meta}},
		pdbs:            []*policyv1.PodDisruptionBudget{{ObjectMeta: meta}},
	}
	cases := []struct {
		kind       NodeKind
		group      string
		apiVersion string
	}{
		{KindDeployment, "apps", "apps/v1"},
		{KindDaemonSet, "apps", "apps/v1"},
		{KindStatefulSet, "apps", "apps/v1"},
		{KindReplicaSet, "apps", "apps/v1"},
		{KindJob, "batch", "batch/v1"},
		{KindCronJob, "batch", "batch/v1"},
		{KindIngress, "networking.k8s.io", "networking.k8s.io/v1"},
		{KindNetworkPolicy, "networking.k8s.io", "networking.k8s.io/v1"},
		{KindHPA, "autoscaling", "autoscaling/v2"},
		{KindPDB, "policy", "policy/v1"},
	}
	for _, mode := range []ViewMode{ViewModeResources, ViewModeTraffic} {
		t.Run(string(mode), func(t *testing.T) {
			opts := relationshipCacheOptions()
			opts.ViewMode = mode
			graph, err := NewBuilder(provider).Build(opts)
			if err != nil {
				t.Fatal(err)
			}
			for _, tc := range cases {
				if mode == ViewModeTraffic && tc.kind != KindIngress {
					continue
				}
				t.Run(string(tc.kind), func(t *testing.T) {
					ref := ResourceRef{Kind: string(tc.kind), Namespace: meta.Namespace, Name: meta.Name}
					for _, group := range []string{"", tc.group, "wrong.example"} {
						ref.Group = group
						sub := BuildNeighborhoodWithIndex(graph, ref, NeighborhoodOptions{}, IndexByResource(graph), nil)
						if group == "wrong.example" {
							if len(sub.Nodes) != 0 {
								t.Fatalf("wrong group resolved: %+v", sub.Nodes)
							}
							continue
						}
						if len(sub.Nodes) == 0 || sub.Nodes[0].Kind != tc.kind {
							t.Fatalf("group %q did not resolve %s", group, tc.kind)
						}
						if got := sub.Nodes[0].Data["apiVersion"]; got != tc.apiVersion {
							t.Errorf("group %q: apiVersion = %v, want %s", group, got, tc.apiVersion)
						}
					}
				})
			}
		})
	}
}
