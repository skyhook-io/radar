package topology

import (
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
