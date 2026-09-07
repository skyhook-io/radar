package topology

import (
	"fmt"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func rolloutGVR() schema.GroupVersionResource {
	return schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}
}

func findEdge(topo *Topology, source, target string) *Edge {
	for i := range topo.Edges {
		if topo.Edges[i].Source == source && topo.Edges[i].Target == target {
			return &topo.Edges[i]
		}
	}
	return nil
}

// TestRolloutTrafficRole_BlueGreenNotMislabeledAsCanary pins the real-world
// bug: stableRS/currentPodHash are generic status fields the Rollout
// controller populates for EVERY Rollout, not just canary ones — a
// blueGreen Rollout's ReplicaSets/Pods have real values there too. Checking
// them before activeSelector/previewSelector would classify a blueGreen
// revision as stable/canary instead of active/preview.
func TestRolloutTrafficRole_BlueGreenNotMislabeledAsCanary(t *testing.T) {
	info := rolloutTrafficInfo{
		// A real blueGreen Rollout: the controller still tracks these two
		// generic fields even though the Rollout uses the blueGreen strategy.
		stableRS:        "hash-old",
		currentPodHash:  "hash-new",
		activeSelector:  "hash-old",
		previewSelector: "hash-new",
	}
	if got := rolloutTrafficRole("hash-old", info); got != "active" {
		t.Errorf("role for the active revision = %q, want %q", got, "active")
	}
	if got := rolloutTrafficRole("hash-new", info); got != "preview" {
		t.Errorf("role for the preview revision = %q, want %q", got, "preview")
	}
}

func TestRolloutTrafficRole_CanaryUnaffectedByEmptyBlueGreenFields(t *testing.T) {
	// A canary Rollout never populates status.blueGreen, so both fields are
	// empty strings — checking them first must not accidentally match.
	info := rolloutTrafficInfo{
		stableRS:        "hash-stable",
		currentPodHash:  "hash-canary",
		activeSelector:  "",
		previewSelector: "",
	}
	if got := rolloutTrafficRole("hash-stable", info); got != "stable" {
		t.Errorf("role for the stable revision = %q, want %q", got, "stable")
	}
	if got := rolloutTrafficRole("hash-canary", info); got != "canary" {
		t.Errorf("role for the canary revision = %q, want %q", got, "canary")
	}
}

func TestBuildResourcesTopology_CanaryServiceEdgesCarryWeightAndRole(t *testing.T) {
	rollout := karpenterTopologyObject("argoproj.io/v1alpha1", "Rollout", "web", "web-uid", map[string]any{
		"spec": map[string]any{
			"replicas": int64(3),
			"strategy": map[string]any{
				"canary": map[string]any{
					"canaryService": "web-canary",
					"stableService": "web-stable",
				},
			},
		},
		"status": map[string]any{
			"canary": map[string]any{
				"weights": map[string]any{
					"canary": map[string]any{"weight": int64(20)},
					"stable": map[string]any{"weight": int64(80)},
				},
			},
			"currentPodHash": "abc123",
			"stableRS":       "def456",
		},
	})
	rollout.SetNamespace("prod")

	dynamic := &rolloutDynamicProvider{gvr: rolloutGVR(), rollouts: []*unstructured.Unstructured{rollout}}
	provider := &mockProvider{
		services: []*corev1.Service{
			{ObjectMeta: metav1.ObjectMeta{Name: "web-canary", Namespace: "prod"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "web-stable", Namespace: "prod"}},
		},
	}

	topo, err := NewBuilder(provider).WithDynamic(dynamic).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	rolloutID := "rollout/prod/web"

	canaryEdge := findEdge(topo, "service/prod/web-canary", rolloutID)
	if canaryEdge == nil {
		t.Fatalf("no edge from canary service to rollout; edges=%+v", topo.Edges)
	}
	if canaryEdge.Label != "Canary · 20%" {
		t.Errorf("canary edge label = %q, want %q", canaryEdge.Label, "Canary · 20%")
	}

	stableEdge := findEdge(topo, "service/prod/web-stable", rolloutID)
	if stableEdge == nil {
		t.Fatalf("no edge from stable service to rollout; edges=%+v", topo.Edges)
	}
	if stableEdge.Label != "Stable · 80%" {
		t.Errorf("stable edge label = %q, want %q", stableEdge.Label, "Stable · 80%")
	}

	canarySvcNode := findNode(topo, "service/prod/web-canary")
	if canarySvcNode == nil || canarySvcNode.Data["trafficRole"] != "canary" {
		t.Errorf("canary service node trafficRole = %v, want %q", canarySvcNode.Data["trafficRole"], "canary")
	}
	stableSvcNode := findNode(topo, "service/prod/web-stable")
	if stableSvcNode == nil || stableSvcNode.Data["trafficRole"] != "stable" {
		t.Errorf("stable service node trafficRole = %v, want %q", stableSvcNode.Data["trafficRole"], "stable")
	}

	rolloutNode := findNode(topo, rolloutID)
	if rolloutNode == nil {
		t.Fatalf("missing rollout node")
	}
	if rolloutNode.Data["canaryWeight"] != int64(20) {
		t.Errorf("rollout node canaryWeight = %v, want 20", rolloutNode.Data["canaryWeight"])
	}
	if rolloutNode.Data["stableWeight"] != int64(80) {
		t.Errorf("rollout node stableWeight = %v, want 80", rolloutNode.Data["stableWeight"])
	}
}

func TestBuildResourcesTopology_BlueGreenServiceEdgesHaveNoWeight(t *testing.T) {
	rollout := karpenterTopologyObject("argoproj.io/v1alpha1", "Rollout", "web", "web-uid", map[string]any{
		"spec": map[string]any{
			"replicas": int64(2),
			"strategy": map[string]any{
				"blueGreen": map[string]any{
					"activeService":  "web-active",
					"previewService": "web-preview",
				},
			},
		},
		"status": map[string]any{
			"blueGreen": map[string]any{
				"activeSelector":  "hash-active",
				"previewSelector": "hash-preview",
			},
		},
	})
	rollout.SetNamespace("prod")

	dynamic := &rolloutDynamicProvider{gvr: rolloutGVR(), rollouts: []*unstructured.Unstructured{rollout}}
	provider := &mockProvider{
		services: []*corev1.Service{
			{ObjectMeta: metav1.ObjectMeta{Name: "web-active", Namespace: "prod"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "web-preview", Namespace: "prod"}},
		},
	}

	topo, err := NewBuilder(provider).WithDynamic(dynamic).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	rolloutID := "rollout/prod/web"

	activeEdge := findEdge(topo, "service/prod/web-active", rolloutID)
	if activeEdge == nil || activeEdge.Label != "Active" {
		t.Fatalf("active edge = %+v, want label %q", activeEdge, "Active")
	}
	previewEdge := findEdge(topo, "service/prod/web-preview", rolloutID)
	if previewEdge == nil || previewEdge.Label != "Preview" {
		t.Fatalf("preview edge = %+v, want label %q", previewEdge, "Preview")
	}
}

func TestBuildResourcesTopology_RolloutOwnedReplicaSetsShowOnlyWhenLive(t *testing.T) {
	rollout := karpenterTopologyObject("argoproj.io/v1alpha1", "Rollout", "web", "web-uid", map[string]any{
		"spec": map[string]any{
			"replicas": int64(2),
			"strategy": map[string]any{"canary": map[string]any{}},
		},
		"status": map[string]any{
			"currentPodHash": "newhash",
			"stableRS":       "newhash",
		},
	})
	rollout.SetNamespace("prod")

	dynamic := &rolloutDynamicProvider{gvr: rolloutGVR(), rollouts: []*unstructured.Unstructured{rollout}}

	liveReplicas := int32(2)
	deadReplicas := int32(0)
	provider := &mockProvider{
		replicaSets: []*appsv1.ReplicaSet{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "web-newhash", Namespace: "prod",
					Labels:          map[string]string{rolloutPodTemplateHashLabel: "newhash"},
					OwnerReferences: []metav1.OwnerReference{{Kind: "Rollout", Name: "web"}},
				},
				Spec: appsv1.ReplicaSetSpec{Replicas: &liveReplicas},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "web-oldhash", Namespace: "prod",
					Labels:          map[string]string{rolloutPodTemplateHashLabel: "oldhash"},
					OwnerReferences: []metav1.OwnerReference{{Kind: "Rollout", Name: "web"}},
				},
				Spec: appsv1.ReplicaSetSpec{Replicas: &deadReplicas},
			},
		},
	}

	opts := DefaultBuildOptions()
	if opts.IncludeReplicaSets {
		t.Fatal("test assumes IncludeReplicaSets defaults to false")
	}

	topo, err := NewBuilder(provider).WithDynamic(dynamic).Build(opts)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	liveNode := findNode(topo, "replicaset/prod/web-newhash")
	if liveNode == nil {
		t.Fatalf("live rollout-owned ReplicaSet should be visible without IncludeReplicaSets; nodes=%+v", topo.Nodes)
	}
	if liveNode.Data["trafficRole"] != "stable" {
		t.Errorf("live ReplicaSet trafficRole = %v, want %q", liveNode.Data["trafficRole"], "stable")
	}

	if findNode(topo, "replicaset/prod/web-oldhash") != nil {
		t.Fatalf("scaled-to-zero ReplicaSet should stay hidden")
	}
}

func TestBuildResourcesTopology_PodTrafficRoleForCanaryAndStable(t *testing.T) {
	rollout := karpenterTopologyObject("argoproj.io/v1alpha1", "Rollout", "web", "web-uid", map[string]any{
		"spec": map[string]any{
			"replicas": int64(2),
			"strategy": map[string]any{"canary": map[string]any{}},
		},
		"status": map[string]any{
			"currentPodHash": "canaryhash",
			"stableRS":       "stablehash",
			"canary": map[string]any{
				"weights": map[string]any{
					"canary": map[string]any{"weight": int64(30)},
					"stable": map[string]any{"weight": int64(70)},
				},
			},
		},
	})
	rollout.SetNamespace("prod")

	dynamic := &rolloutDynamicProvider{gvr: rolloutGVR(), rollouts: []*unstructured.Unstructured{rollout}}

	replicas := int32(1)
	provider := &mockProvider{
		replicaSets: []*appsv1.ReplicaSet{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "web-canaryhash", Namespace: "prod",
					Labels:          map[string]string{rolloutPodTemplateHashLabel: "canaryhash"},
					OwnerReferences: []metav1.OwnerReference{{Kind: "Rollout", Name: "web"}},
				},
				Spec: appsv1.ReplicaSetSpec{Replicas: &replicas},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "web-stablehash", Namespace: "prod",
					Labels:          map[string]string{rolloutPodTemplateHashLabel: "stablehash"},
					OwnerReferences: []metav1.OwnerReference{{Kind: "Rollout", Name: "web"}},
				},
				Spec: appsv1.ReplicaSetSpec{Replicas: &replicas},
			},
		},
		pods: []*corev1.Pod{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "web-canaryhash-abc", Namespace: "prod",
					Labels:          map[string]string{rolloutPodTemplateHashLabel: "canaryhash"},
					OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "web-canaryhash"}},
				},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "web-stablehash-abc", Namespace: "prod",
					Labels:          map[string]string{rolloutPodTemplateHashLabel: "stablehash"},
					OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "web-stablehash"}},
				},
			},
		},
	}

	topo, err := NewBuilder(provider).WithDynamic(dynamic).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	canaryPod := findNode(topo, "pod/prod/web-canaryhash-abc")
	if canaryPod == nil || canaryPod.Data["trafficRole"] != "canary" {
		t.Errorf("canary pod trafficRole = %v, want %q", canaryPod.Data["trafficRole"], "canary")
	}
	stablePod := findNode(topo, "pod/prod/web-stablehash-abc")
	if stablePod == nil || stablePod.Data["trafficRole"] != "stable" {
		t.Errorf("stable pod trafficRole = %v, want %q", stablePod.Data["trafficRole"], "stable")
	}

	// Pods connect straight to their (now-visible) ReplicaSet, not via the
	// Deployment/Rollout shortcut, once the owning ReplicaSet is live.
	podEdge := findEdge(topo, "replicaset/prod/web-canaryhash", "pod/prod/web-canaryhash-abc")
	if podEdge == nil {
		t.Fatalf("expected Pod->ReplicaSet edge for the live canary ReplicaSet; edges=%+v", topo.Edges)
	}

	// The same "Canary · 20%" style label rides every hop of the traffic
	// path, not just the Service->Rollout edge - Rollout->ReplicaSet and
	// ReplicaSet->Pod carry it too, so a weight is visible however far down
	// the graph the user is looking (the actual ask this test guards against
	// regressing: a blinking-but-unlabeled edge told the user nothing about
	// which revision was carrying which share of traffic).
	if podEdge.Label != "Canary · 30%" {
		t.Errorf("canary Pod edge label = %q, want %q", podEdge.Label, "Canary · 30%")
	}
	stablePodEdge := findEdge(topo, "replicaset/prod/web-stablehash", "pod/prod/web-stablehash-abc")
	if stablePodEdge == nil || stablePodEdge.Label != "Stable · 70%" {
		t.Fatalf("stable Pod edge = %+v, want label %q", stablePodEdge, "Stable · 70%")
	}

	rolloutID := "rollout/prod/web"
	rsEdge := findEdge(topo, rolloutID, "replicaset/prod/web-canaryhash")
	if rsEdge == nil || rsEdge.Label != "Canary · 30%" {
		t.Fatalf("Rollout->ReplicaSet canary edge = %+v, want label %q", rsEdge, "Canary · 30%")
	}
	stableRsEdge := findEdge(topo, rolloutID, "replicaset/prod/web-stablehash")
	if stableRsEdge == nil || stableRsEdge.Label != "Stable · 70%" {
		t.Fatalf("Rollout->ReplicaSet stable edge = %+v, want label %q", stableRsEdge, "Stable · 70%")
	}
}

// TestBuildResourcesTopology_LargePodGroupWithMixedTrafficRoles pins the
// real-world bug: pods are grouped by shared app label regardless of which
// ReplicaSet owns them, so a Rollout with more than maxIndividualPods (5)
// pods split across canary and stable revisions — a completely normal
// mid-progression shape — collapses into ONE PodGroup node. Using only the
// group's first pod misrepresented the whole group with one arbitrary
// pod's role and connected it to only one of the two ReplicaSets actually
// present.
func TestBuildResourcesTopology_LargePodGroupWithMixedTrafficRoles(t *testing.T) {
	rollout := karpenterTopologyObject("argoproj.io/v1alpha1", "Rollout", "web", "web-uid", map[string]any{
		"spec": map[string]any{
			"replicas": int64(7),
			"strategy": map[string]any{"canary": map[string]any{}},
		},
		"status": map[string]any{
			"currentPodHash": "canaryhash",
			"stableRS":       "stablehash",
		},
	})
	rollout.SetNamespace("prod")

	dynamic := &rolloutDynamicProvider{gvr: rolloutGVR(), rollouts: []*unstructured.Unstructured{rollout}}

	canaryReplicas := int32(3)
	stableReplicas := int32(4)
	provider := &mockProvider{
		replicaSets: []*appsv1.ReplicaSet{
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "web-canaryhash", Namespace: "prod",
					Labels:          map[string]string{rolloutPodTemplateHashLabel: "canaryhash"},
					OwnerReferences: []metav1.OwnerReference{{Kind: "Rollout", Name: "web"}},
				},
				Spec: appsv1.ReplicaSetSpec{Replicas: &canaryReplicas},
			},
			{
				ObjectMeta: metav1.ObjectMeta{
					Name: "web-stablehash", Namespace: "prod",
					Labels:          map[string]string{rolloutPodTemplateHashLabel: "stablehash"},
					OwnerReferences: []metav1.OwnerReference{{Kind: "Rollout", Name: "web"}},
				},
				Spec: appsv1.ReplicaSetSpec{Replicas: &stableReplicas},
			},
		},
	}
	// 3 canary + 4 stable pods, all sharing the same app label (so they
	// group together) — 7 total, over the 5-pod individual-display threshold.
	for i := int32(0); i < canaryReplicas; i++ {
		provider.pods = append(provider.pods, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("web-canaryhash-%d", i), Namespace: "prod",
				Labels:          map[string]string{"app": "web", rolloutPodTemplateHashLabel: "canaryhash"},
				OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "web-canaryhash"}},
			},
		})
	}
	for i := int32(0); i < stableReplicas; i++ {
		provider.pods = append(provider.pods, &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: fmt.Sprintf("web-stablehash-%d", i), Namespace: "prod",
				Labels:          map[string]string{"app": "web", rolloutPodTemplateHashLabel: "stablehash"},
				OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "web-stablehash"}},
			},
		})
	}

	topo, err := NewBuilder(provider).WithDynamic(dynamic).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	podGroupID := "podgroup-prod-app-web"
	groupNode := findNode(topo, podGroupID)
	if groupNode == nil {
		t.Fatalf("expected a PodGroup node for the 7 grouped pods; nodes=%+v", topo.Nodes)
	}
	if role, ok := groupNode.Data["trafficRole"]; ok {
		t.Errorf("mixed-role group should not set a single trafficRole, got %v", role)
	}

	canaryEdge := findEdge(topo, "replicaset/prod/web-canaryhash", podGroupID)
	if canaryEdge == nil {
		t.Errorf("expected an edge from the canary ReplicaSet to the pod group; edges=%+v", topo.Edges)
	}
	stableEdge := findEdge(topo, "replicaset/prod/web-stablehash", podGroupID)
	if stableEdge == nil {
		t.Errorf("expected an edge from the stable ReplicaSet to the pod group; edges=%+v", topo.Edges)
	}

	// Each pod must carry ONLY its own owner's edge source, not every
	// distinct owner in the group — otherwise expanding the group on the
	// frontend draws a canary pod as owned by the stable ReplicaSet too
	// (and vice versa). See TopologyGraph.tsx's expandPodGroup.
	pods, ok := groupNode.Data["pods"].([]map[string]any)
	if !ok || len(pods) != 7 {
		t.Fatalf("expected 7 pod detail entries, got %#v", groupNode.Data["pods"])
	}
	for _, pd := range pods {
		name, _ := pd["name"].(string)
		ownerIDs, _ := pd["ownerIds"].([]string)
		switch {
		case strings.HasPrefix(name, "web-canaryhash-"):
			if len(ownerIDs) != 1 || ownerIDs[0] != "replicaset/prod/web-canaryhash" {
				t.Errorf("canary pod %s ownerIds = %v, want [replicaset/prod/web-canaryhash]", name, ownerIDs)
			}
		case strings.HasPrefix(name, "web-stablehash-"):
			if len(ownerIDs) != 1 || ownerIDs[0] != "replicaset/prod/web-stablehash" {
				t.Errorf("stable pod %s ownerIds = %v, want [replicaset/prod/web-stablehash]", name, ownerIDs)
			}
		default:
			t.Errorf("unexpected pod name %s", name)
		}
	}
}

// A "basic canary" Rollout - no trafficRouting plugin (Istio/SMI/ALB/NGINX)
// configured, traffic split purely by replica ratio - never populates
// status.canary.weights at all (confirmed live against a real Rollout on the
// demo cluster mid-progression). The step-definition fallback below is what
// makes weight visible on the edges for this — by far the more common —
// case; without it, a canary/stable edge shows a bare role with no percent
// the entire time the rollout progresses.
func TestBuildResourcesTopology_BasicCanaryWeightFallsBackToStepDefinition(t *testing.T) {
	rollout := karpenterTopologyObject("argoproj.io/v1alpha1", "Rollout", "web", "web-uid", map[string]any{
		"spec": map[string]any{
			"replicas": int64(4),
			"strategy": map[string]any{
				"canary": map[string]any{
					// No trafficRouting: block - matches the real demo fixture.
					"steps": []any{
						map[string]any{"setWeight": int64(20)},
						map[string]any{"analysis": map[string]any{}},
						map[string]any{"setWeight": int64(60)},
						map[string]any{"pause": map[string]any{}},
					},
				},
			},
		},
		"status": map[string]any{
			// currentStepIndex 1 = just past the first setWeight(20) step,
			// not yet at setWeight(60) - 20% is the controller's live target.
			// No status.canary.weights at all, the real-world shape.
			"currentStepIndex": int64(1),
			"currentPodHash":   "canaryhash",
			"stableRS":         "stablehash",
		},
	})
	rollout.SetNamespace("prod")

	dynamic := &rolloutDynamicProvider{gvr: rolloutGVR(), rollouts: []*unstructured.Unstructured{rollout}}
	provider := &mockProvider{
		services: []*corev1.Service{
			{ObjectMeta: metav1.ObjectMeta{Name: "web-canary", Namespace: "prod"}},
		},
	}
	// canaryService isn't set in this fixture (matching the demo, which has
	// no named split either) - use the Rollout node's own Data instead of an
	// edge, since that's reachable regardless of whether a Service exists.

	topo, err := NewBuilder(provider).WithDynamic(dynamic).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	rolloutNode := findNode(topo, "rollout/prod/web")
	if rolloutNode == nil {
		t.Fatalf("missing rollout node")
	}
	if rolloutNode.Data["canaryWeight"] != int64(20) {
		t.Errorf("rollout node canaryWeight = %v, want 20 (derived from the last setWeight step reached)", rolloutNode.Data["canaryWeight"])
	}
	if rolloutNode.Data["stableWeight"] != int64(80) {
		t.Errorf("rollout node stableWeight = %v, want 80", rolloutNode.Data["stableWeight"])
	}
}

// Once a canary Rollout fully promotes (currentStepIndex past the end of
// steps), there's no active split to report - canaryStepWeight must not
// keep returning the LAST step's weight as if it were still in effect.
func TestBuildResourcesTopology_BasicCanaryWeightOmittedOncePromoted(t *testing.T) {
	rollout := karpenterTopologyObject("argoproj.io/v1alpha1", "Rollout", "web", "web-uid", map[string]any{
		"spec": map[string]any{
			"replicas": int64(4),
			"strategy": map[string]any{
				"canary": map[string]any{
					"steps": []any{
						map[string]any{"setWeight": int64(50)},
						map[string]any{"pause": map[string]any{}},
					},
				},
			},
		},
		"status": map[string]any{
			// Past the end of a 2-step array - fully promoted.
			"currentStepIndex": int64(2),
			"currentPodHash":   "samehash",
			"stableRS":         "samehash",
		},
	})
	rollout.SetNamespace("prod")

	dynamic := &rolloutDynamicProvider{gvr: rolloutGVR(), rollouts: []*unstructured.Unstructured{rollout}}

	topo, err := NewBuilder(&mockProvider{}).WithDynamic(dynamic).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	rolloutNode := findNode(topo, "rollout/prod/web")
	if rolloutNode == nil {
		t.Fatalf("missing rollout node")
	}
	if _, ok := rolloutNode.Data["canaryWeight"]; ok {
		t.Errorf("rollout node canaryWeight = %v, want absent once fully promoted", rolloutNode.Data["canaryWeight"])
	}
}
