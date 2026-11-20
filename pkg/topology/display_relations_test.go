package topology

import (
	"slices"
	"testing"

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
