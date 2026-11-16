package tree

import (
	"context"
	"testing"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/topology"
)

func TestHelmProjectionRetainsLabelledNonWorkloadKinds(t *testing.T) {
	release := ownedObject("helm.toolkit.fluxcd.io", "HelmRelease", "flux-system", "podinfo", nil, false)
	release.Membership = ownership.MembersUnreadable
	config := ownedObject("", "ConfigMap", "demo", "settings", nil, false)
	widget := ownedObject("example.io", "Widget", "demo", "settings", nil, false)
	config.Labels, widget.Labels = podinfoOrigin, podinfoOrigin
	hr := helmRelease("flux-system", "podinfo")
	hr.SetUID(release.UID)
	topo := &topology.Topology{Nodes: []topology.Node{
		topoNode("ConfigMap", "demo", "settings", podinfoOrigin),
		{ID: "widget/demo/settings", Kind: "Widget", Name: "settings", Data: map[string]any{"namespace": "demo", "apiVersion": "example.io/v1", "labels": podinfoOrigin}},
	}}
	session := ownershipSession(t, release, config, widget)
	tree, _, err := NewBuilder(podinfoGetter(hr, liveObject(config, config.Labels, nil), liveObject(widget, widget.Labels, nil)), topo).
		WithOwnership(session).Build(context.Background(), "HelmRelease", "flux-system", "podinfo", "helm.toolkit.fluxcd.io")
	if err != nil {
		t.Fatal(err)
	}
	for _, obj := range []struct{ group, kind string }{{"", "ConfigMap"}, {"example.io", "Widget"}} {
		assertNodeRole(t, tree, ResourceRef{Group: obj.group, Kind: obj.kind, Namespace: "demo", Name: "settings"}, RoleDeclared)
	}
}
