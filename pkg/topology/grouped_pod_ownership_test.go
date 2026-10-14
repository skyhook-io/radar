package topology

import (
	"encoding/json"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"strings"
	"testing"
)

func TestGroupedOwnershipSnapshotStaysPrivateAndFollowsFilteredGroups(t *testing.T) {
	topo := ownershipFixture(t)
	if len(topo.groupedPods) != 6 {
		t.Fatalf("private replicas=%d want6", len(topo.groupedPods))
	}
	for _, n := range topo.Nodes {
		if n.Kind == KindPod && strings.HasPrefix(n.Name, "web-abc-") {
			t.Fatalf("cached graph expanded: %s", n.ID)
		}
	}
	raw, err := json.Marshal(topo)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "groupedPods") || strings.Contains(string(raw), `"pod-0"`) {
		t.Fatalf("private ownership serialized")
	}
	hidden := *topo
	hidden.Nodes = nil
	hidden.Edges = nil
	idx := IndexByResource(&hidden)
	if len(idx.groupedPods) != 0 {
		t.Fatal("filtered display groups retain private members")
	}
	for _, p := range topo.groupedPods {
		rel := GetRelationshipsWithObject("Pod", p.namespace, p.name, nil, &hidden, nil, nil, idx)
		if rel != nil {
			t.Fatalf("hidden replica linked: %+v", rel)
		}
	}
}

func TestGroupedPodManagedByWithAndWithoutSharedIndex(t *testing.T) {
	topo := ownershipFixture(t)
	for _, idx := range []*RelationshipsIndex{nil, IndexByResource(topo)} {
		refs := SynthesizeManagedBy(nil, "Pod", "team", "web-abc-0", topo, nil, idx)
		if len(refs) != 1 || refs[0].Kind != "Deployment" || refs[0].Name != "web" {
			t.Fatalf("direct grouped owner walk: %+v", refs)
		}
		rel := GetRelationshipsWithObject("Pod", "team", "web-abc-0", nil, topo, nil, nil, idx)
		if rel == nil || len(rel.ManagedBy) != 1 || rel.ManagedBy[0].Name != "web" {
			t.Fatalf("grouped relationships: %+v", rel)
		}
	}
	app := Node{ID: "application/team/release", Kind: KindApplication, Name: "release", Data: map[string]any{"namespace": "team", "apiVersion": "argoproj.io/v1alpha1"}}
	topo.Nodes = append(topo.Nodes, app)
	topo.Edges = append(topo.Edges, Edge{Source: app.ID, Target: "deployment/team/web", Type: EdgeManages})
	for _, idx := range []*RelationshipsIndex{nil, IndexByResource(topo)} {
		refs := SynthesizeManagedBy(nil, "Pod", "team", "web-abc-0", topo, nil, idx)
		if len(refs) != 1 || refs[0].Kind != "Application" || refs[0].Name != "release" {
			t.Fatalf("grouped inventory walk: %+v", refs)
		}
	}
}

func TestGroupedPodWithUnobservedSecondOwnerIsPossible(t *testing.T) {
	topo := ownershipFixture(t)
	id := topo.groupedPods[0].id
	topo.groupedPods[0].owners = append(append([]metav1.OwnerReference(nil), topo.groupedPods[0].owners...), ownerRef("unwatched.example.io/v1", "HiddenOwner", "unseen", "unseen-uid", false))
	index := IndexByResource(topo)
	for _, idx := range []*RelationshipsIndex{nil, index} {
		preview := GetCascadeDeletePreviewWithIndex(ResourceRef{Kind: "Deployment", Namespace: "team", Name: "web"}, topo, nil, idx)
		possible := false
		for _, ref := range preview.PossibleDependents {
			if "pod/"+ref.Namespace+"/"+ref.Name == id {
				possible = true
			}
		}
		if !possible {
			t.Fatalf("unobserved co-owner treated as certain: %+v", preview)
		}
		for _, ref := range preview.Dependents {
			if "pod/"+ref.Namespace+"/"+ref.Name == id {
				t.Fatalf("possible replica also certain: %+v", preview)
			}
		}
	}
}

func TestGroupedOwnershipFollowsNamespaceFilteredClone(t *testing.T) {
	topo := ownershipFixture(t)
	visible := *topo
	visible.Nodes = append([]Node(nil), topo.Nodes...)
	visible.Edges = append([]Edge(nil), topo.Edges...)
	// Auth-mode relationship queries rebuild their index over this whole-value clone.
	rel := GetRelationshipsWithObject("Pod", "team", "web-abc-0", nil, &visible, nil, nil, nil)
	if rel == nil || rel.Owner == nil || len(rel.ManagedBy) != 1 {
		t.Fatalf("authorized clone lost ownership: %+v", rel)
	}
	parent := GetRelationshipsWithObject("ReplicaSet", "team", "web-abc", nil, &visible, nil, nil, nil)
	if parent == nil || len(parent.Children) != 6 {
		t.Fatalf("authorized clone lost children: %+v", parent)
	}
	preview := GetCascadeDeletePreview(ResourceRef{Kind: "Deployment", Namespace: "team", Name: "web"}, &visible, nil)
	if len(preview.Dependents) != 7 {
		t.Fatalf("authorized clone lost delete dependents: %+v", preview)
	}
	denied := map[string]bool{}
	for _, n := range visible.Nodes {
		if n.Data["namespace"] == "team" {
			denied[n.ID] = true
		}
	}
	visible.StripNodeIDs(denied)
	rel = GetRelationshipsWithObject("Pod", "team", "web-abc-0", nil, &visible, nil, nil, nil)
	if rel != nil {
		t.Fatalf("denied namespace resurrected grouped member: %+v", rel)
	}
	preview = GetCascadeDeletePreview(ResourceRef{Kind: "Deployment", Namespace: "team", Name: "web"}, &visible, nil)
	if preview.RootResolved || len(preview.Dependents) != 0 {
		t.Fatalf("denied namespace exposed preview: %+v", preview)
	}
	if len(topo.Nodes) == len(visible.Nodes) {
		t.Fatal("shared topology mutated or filtering ineffective")
	}
}
