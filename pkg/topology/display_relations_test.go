package topology

import (
	"slices"
	"testing"

	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

func TestDrawnNodesFindNodesDrawnAfterTheirKindWasLookedUp(t *testing.T) {
	svc := func(name string) Node {
		return Node{ID: "service/shop/" + name, Kind: KindService, Name: name, Data: map[string]any{"namespace": "shop", "apiVersion": "v1"}}
	}
	nodes := []Node{svc("web")}
	d := newDrawnNodes()
	d.update(nodes)
	if id, ok := d.id(resourceid.NewRef("", "Service", "shop", "web")); !ok || id != "service/shop/web" {
		t.Fatalf("web = %q %v", id, ok)
	}
	nodes = append(nodes, svc("api"))
	d.update(nodes)
	if id, ok := d.id(resourceid.NewRef("", "Service", "shop", "api")); !ok || id != "service/shop/api" {
		t.Errorf("a Service drawn after Services were first looked up = %q %v", id, ok)
	}
	if _, ok := d.id(resourceid.NewRef("", "Service", "other", "api")); ok {
		t.Error("a Service in another namespace resolved")
	}
}

func TestDrawnNodesSurviveANodePrependedBetweenUpdates(t *testing.T) {
	nodes := []Node{
		{ID: "service/shop/web", Kind: KindService, Name: "web", Data: map[string]any{"namespace": "shop", "apiVersion": "v1"}},
		{ID: "deployment/shop/web", Kind: KindDeployment, Name: "web", Data: map[string]any{"namespace": "shop", "apiVersion": "apps/v1"}},
	}
	d := newDrawnNodes()
	d.update(nodes)
	// The traffic view prepends its Internet node, moving every node along.
	nodes = append([]Node{{ID: "internet", Kind: KindInternet, Name: "Internet"}}, nodes...)
	d.update(nodes)
	for _, r := range []resourceid.Ref{resourceid.NewRef("", "Service", "shop", "web"), resourceid.NewRef("apps", "Deployment", "shop", "web")} {
		if id, ok := d.id(r); !ok || id != nodes[slices.IndexFunc(nodes, func(n Node) bool { return n.Kind == NodeKind(r.Kind) })].ID {
			t.Errorf("%s = %q %v", r.Key(), id, ok)
		}
	}
}

func TestDrawFamiliesIndexesNodesAccumulatedDuringEmptyPass(t *testing.T) {
	d := newDrawnNodes()
	nodes := []Node{{ID: "gatewayclass//public", Kind: KindGatewayClass, Name: "public", Data: map[string]any{"apiVersion": "gateway.networking.k8s.io/v1"}}}
	edges := drawFamilies(nil, d, nodes, nil, relations.GatewayClasses)
	if d.indexed != 0 {
		t.Fatalf("empty pass indexed %d nodes", d.indexed)
	}
	gw := charUnstructured("gateway.networking.k8s.io/v1", "Gateway", "shop", "front", nil, map[string]any{"gatewayClassName": "public"}, nil)
	nodes = append(nodes, Node{ID: "gateway/shop/front", Kind: KindGateway, Name: "front", Data: map[string]any{"apiVersion": gw.GetAPIVersion(), "namespace": "shop"}})
	edges = drawFamilies(edges, d, nodes, []declaring{{ref: projectionRef(gw), obj: gw}}, relations.GatewayClasses)
	if len(edges) != 1 || edges[0].Source != nodes[0].ID || edges[0].Target != nodes[1].ID || edges[0].Type != EdgeManages {
		t.Errorf("class binding after empty pass = %+v", edges)
	}
}

func TestDrawnNodesForgetRemovedNodeOnShrink(t *testing.T) {
	d := newDrawnNodes()
	nodes := []Node{
		{ID: "service/shop/keep", Kind: KindService, Name: "keep"},
		{ID: "service/shop/gone", Kind: KindService, Name: "gone"},
	}
	r := resourceid.NewRef("", "Service", "shop", "gone")
	d.update(nodes)
	if id, ok := d.id(r); !ok || id != nodes[1].ID {
		t.Fatalf("before shrink: %q %v", id, ok)
	}
	d.update(nodes[:1])
	if id, ok := d.id(r); ok {
		t.Errorf("removed Service still resolves to %q", id)
	}
}
