package topology

import (
	"fmt"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	k8score "github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/karpenter"
)

type karpenterDynamicProvider struct {
	exact              map[string]schema.GroupVersionResource
	resources          map[schema.GroupVersionResource][]*unstructured.Unstructured
	kinds              map[schema.GroupVersionResource]string
	watched            []schema.GroupVersionResource
	listCalls          map[schema.GroupVersionResource]int
	listNamespaceCalls map[schema.GroupVersionResource]int
}

func (p *karpenterDynamicProvider) List(gvr schema.GroupVersionResource, _ string) ([]*unstructured.Unstructured, error) {
	p.listCalls[gvr]++
	return p.resources[gvr], nil
}

func (p *karpenterDynamicProvider) ListNamespaces(gvr schema.GroupVersionResource, _ []string) ([]*unstructured.Unstructured, error) {
	p.listNamespaceCalls[gvr]++
	return p.resources[gvr], nil
}

func (p *karpenterDynamicProvider) Get(gvr schema.GroupVersionResource, _, name string) (*unstructured.Unstructured, error) {
	for _, resource := range p.resources[gvr] {
		if resource.GetName() == name {
			return resource, nil
		}
	}
	return nil, fmt.Errorf("not found")
}

func (p *karpenterDynamicProvider) GetWatchedResources() []schema.GroupVersionResource {
	return p.watched
}

func (p *karpenterDynamicProvider) GetDiscoveryStatus() k8score.CRDDiscoveryStatus {
	return k8score.CRDDiscoveryComplete
}

func (p *karpenterDynamicProvider) GetGVR(string) (schema.GroupVersionResource, bool) {
	return schema.GroupVersionResource{}, false
}

func (p *karpenterDynamicProvider) GetGVRWithGroup(kind, group string) (schema.GroupVersionResource, bool) {
	want := group + "/" + kind
	for key, gvr := range p.exact {
		if strings.EqualFold(key, want) {
			return gvr, true
		}
	}
	return schema.GroupVersionResource{}, false
}

func (p *karpenterDynamicProvider) GetKindForGVR(gvr schema.GroupVersionResource) string {
	return p.kinds[gvr]
}

func (p *karpenterDynamicProvider) IsCRD(string) bool {
	return true
}
func (p *karpenterDynamicProvider) IsCRDGVR(schema.GroupVersionResource) bool { return true }

func TestBuildKarpenterTopologyUsesReferencedNodeClassTypes(t *testing.T) {
	nodePoolGVR := schema.GroupVersionResource{Group: karpenter.Group, Version: "v1", Resource: "nodepools"}
	nodeClaimGVR := schema.GroupVersionResource{Group: karpenter.Group, Version: "v1", Resource: "nodeclaims"}
	eksNodeClassGVR := schema.GroupVersionResource{Group: "eks.amazonaws.com", Version: "v1", Resource: "nodeclasses"}
	customNodeClassGVR := schema.GroupVersionResource{Group: "infra.example.io", Version: "v1", Resource: "customnodeclasses"}
	ec2NodeClassGVR := schema.GroupVersionResource{Group: "karpenter.k8s.aws", Version: "v1beta1", Resource: "ec2nodeclasses"}

	eksPool := karpenterTopologyObject(karpenter.APIVersionV1, karpenter.NodePoolKind, "eks-pool", "eks-pool-uid", map[string]any{
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"nodeClassRef": map[string]any{"group": "eks.amazonaws.com", "kind": "NodeClass", "name": "shared"},
		}}},
		"status": karpenterReadyStatus(1),
	})
	customPool := karpenterTopologyObject(karpenter.APIVersionV1, karpenter.NodePoolKind, "custom-pool", "custom-pool-uid", map[string]any{
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"nodeClassRef": map[string]any{"group": "infra.example.io", "kind": "CustomNodeClass", "name": "shared"},
		}}},
		"status": karpenterReadyStatus(1),
	})
	legacyPool := karpenterTopologyObject(karpenter.APIVersionV1Beta1, karpenter.NodePoolKind, "legacy-pool", "legacy-pool-uid", map[string]any{
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"nodeClassRef": map[string]any{"apiVersion": "karpenter.k8s.aws/v1beta1", "kind": "EC2NodeClass", "name": "legacy"},
		}}},
		"status": karpenterReadyStatus(1),
	})

	matchingClaim := karpenterTopologyObject(karpenter.APIVersionV1, karpenter.NodeClaimKind, "claim-matching", "claim-matching-uid", map[string]any{
		"status": map[string]any{
			"nodeName":   "node-a",
			"conditions": karpenterReadyStatus(1)["conditions"],
		},
	})
	matchingClaim.SetLabels(map[string]string{karpenter.NodePoolLabelKey: "custom-pool"})
	matchingClaim.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion: karpenter.APIVersionV1,
		Kind:       karpenter.NodePoolKind,
		Name:       "eks-pool",
		UID:        types.UID("eks-pool-uid"),
	}})
	staleClaim := karpenterTopologyObject(karpenter.APIVersionV1, karpenter.NodeClaimKind, "claim-stale", "claim-stale-uid", nil)
	staleClaim.SetLabels(map[string]string{karpenter.NodePoolLabelKey: "custom-pool"})
	staleClaim.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion: karpenter.APIVersionV1,
		Kind:       karpenter.NodePoolKind,
		Name:       "eks-pool",
		UID:        types.UID("deleted-pool-uid"),
	}})
	labelClaim := karpenterTopologyObject(karpenter.APIVersionV1, karpenter.NodeClaimKind, "claim-label", "claim-label-uid", nil)
	labelClaim.SetLabels(map[string]string{karpenter.NodePoolLabelKey: "custom-pool"})

	eksNodeClass := karpenterTopologyObject("eks.amazonaws.com/v1", "NodeClass", "shared", "eks-class-uid", map[string]any{
		"status": karpenterReadyStatus(1),
	})
	customNodeClass := karpenterTopologyObject("infra.example.io/v1", "CustomNodeClass", "shared", "custom-class-uid", map[string]any{
		"status": karpenterReadyStatus(1),
	})
	customNodeClass.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion: karpenter.APIVersionV1,
		Kind:       karpenter.NodePoolKind,
		Name:       "custom-pool",
		UID:        types.UID("custom-pool-uid"),
	}})
	ec2NodeClass := karpenterTopologyObject("karpenter.k8s.aws/v1beta1", "EC2NodeClass", "legacy", "legacy-class-uid", map[string]any{
		"status": karpenterReadyStatus(1),
	})

	dynamic := &karpenterDynamicProvider{
		exact: map[string]schema.GroupVersionResource{
			karpenter.Group + "/" + karpenter.NodePoolKind:  nodePoolGVR,
			karpenter.Group + "/" + karpenter.NodeClaimKind: nodeClaimGVR,
			"eks.amazonaws.com/NodeClass":                   eksNodeClassGVR,
			"infra.example.io/CustomNodeClass":              customNodeClassGVR,
			"karpenter.k8s.aws/EC2NodeClass":                ec2NodeClassGVR,
		},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{
			nodePoolGVR:        {eksPool, customPool, legacyPool},
			nodeClaimGVR:       {matchingClaim, staleClaim, labelClaim},
			eksNodeClassGVR:    {eksNodeClass},
			customNodeClassGVR: {customNodeClass},
			ec2NodeClassGVR:    {ec2NodeClass},
		},
		kinds: map[schema.GroupVersionResource]string{
			nodePoolGVR:        karpenter.NodePoolKind,
			nodeClaimGVR:       karpenter.NodeClaimKind,
			eksNodeClassGVR:    "NodeClass",
			customNodeClassGVR: "CustomNodeClass",
			ec2NodeClassGVR:    "EC2NodeClass",
		},
		watched:            []schema.GroupVersionResource{customNodeClassGVR},
		listCalls:          make(map[schema.GroupVersionResource]int),
		listNamespaceCalls: make(map[schema.GroupVersionResource]int),
	}

	opts := DefaultBuildOptions()
	topo, err := NewBuilder(&mockProvider{nodes: []*corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}}}}).WithDynamic(dynamic).Build(opts)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	eksID := "nodeclass//shared/eks.amazonaws.com/nodeclass"
	customID := "nodeclass//shared/infra.example.io/customnodeclass"
	legacyID := "nodeclass//legacy/karpenter.k8s.aws/ec2nodeclass"
	for _, id := range []string{eksID, customID, legacyID} {
		if findNode(topo, id) == nil {
			t.Fatalf("missing referenced NodeClass node %q; nodes=%+v", id, topo.Nodes)
		}
	}
	if eksID == customID || countKind(topo, KindNodeClass) != 3 {
		t.Fatalf("NodeClass identities collided or generic CRD handling duplicated them: %+v", topo.Nodes)
	}
	customNode := findNode(topo, customID)
	customRef := parseNodeID(customID, dynamic)
	if customRef == nil || customRef.Kind != "CustomNodeClass" || customRef.Namespace != "" || customRef.Name != "shared" || customRef.Group != "infra.example.io" {
		t.Fatalf("enriched custom NodeClass ID parsed incorrectly: %+v", customRef)
	}
	enrichRef(customRef, dynamic)
	if customRef.Group != "infra.example.io" {
		t.Fatalf("enrichRef overwrote the group encoded by the NodeClass ID: %+v", customRef)
	}
	if customNode.Data["resource"] != "customnodeclasses" || customNode.Data["resourceKind"] != "CustomNodeClass" {
		t.Fatalf("custom NodeClass identity metadata = %#v", customNode.Data)
	}

	for source, target := range map[string]string{
		"nodepool//eks-pool":    eksID,
		"nodepool//custom-pool": customID,
		"nodepool//legacy-pool": legacyID,
	} {
		if !hasKarpenterTopologyEdge(topo, source, target, EdgeConfigures) {
			t.Fatalf("missing %s -> %s NodeClass edge; edges=%+v", source, target, topo.Edges)
		}
	}
	if !hasKarpenterTopologyEdge(topo, "nodepool//eks-pool", "nodeclaim//claim-matching", EdgeManages) {
		t.Fatalf("matching owner reference did not win over the label; edges=%+v", topo.Edges)
	}
	if hasKarpenterTopologyEdge(topo, "nodepool//custom-pool", "nodeclaim//claim-matching", EdgeManages) {
		t.Fatal("label overrode an authoritative NodePool owner reference")
	}
	if hasKarpenterTopologyEdge(topo, "nodepool//custom-pool", "nodeclaim//claim-stale", EdgeManages) {
		t.Fatal("stale owner UID fell back to the NodePool label")
	}
	if !hasKarpenterTopologyEdge(topo, "nodepool//custom-pool", "nodeclaim//claim-label", EdgeManages) {
		t.Fatal("label-only NodeClaim did not resolve to its NodePool")
	}
	if !hasKarpenterTopologyEdge(topo, "nodeclaim//claim-matching", "node//node-a", EdgeManages) {
		t.Fatal("NodeClaim status.nodeName did not link its Node")
	}

	for _, gvr := range []schema.GroupVersionResource{eksNodeClassGVR, customNodeClassGVR, ec2NodeClassGVR} {
		if dynamic.listCalls[gvr] != 1 {
			t.Fatalf("NodeClass %s listed %d times, want exactly once", gvr, dynamic.listCalls[gvr])
		}
	}
	if dynamic.listNamespaceCalls[nodePoolGVR] != 1 || dynamic.listNamespaceCalls[nodeClaimGVR] != 1 {
		t.Fatalf("Karpenter base resources were not group-qualified/listed once: pools=%d claims=%d",
			dynamic.listNamespaceCalls[nodePoolGVR], dynamic.listNamespaceCalls[nodeClaimGVR])
	}
}

func karpenterTopologyObject(apiVersion, kind, name, uid string, fields map[string]any) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{}}
	for key, value := range fields {
		obj.Object[key] = value
	}
	obj.SetAPIVersion(apiVersion)
	obj.SetKind(kind)
	obj.SetName(name)
	obj.SetUID(types.UID(uid))
	obj.SetGeneration(1)
	return obj
}

func karpenterReadyStatus(generation int64) map[string]any {
	return map[string]any{
		"conditions": []any{
			map[string]any{"type": "Ready", "status": "True", "observedGeneration": generation},
		},
	}
}

func hasKarpenterTopologyEdge(topo *Topology, source, target string, edgeType EdgeType) bool {
	for _, edge := range topo.Edges {
		if edge.Source == source && edge.Target == target && edge.Type == edgeType {
			return true
		}
	}
	return false
}
