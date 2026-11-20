package tree

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
)

var podinfoOrigin = map[string]string{fluxHelmNameLabel: "podinfo", fluxHelmNamespaceLabel: "flux-system"}

func liveObject(o ownershiptest.Object, labels, annotations map[string]string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{}
	u.SetAPIVersion(o.APIVersion)
	u.SetKind(o.Ref.Kind)
	u.SetNamespace(o.Ref.Namespace)
	u.SetName(o.Ref.Name)
	u.SetUID(o.UID)
	u.SetLabels(labels)
	u.SetAnnotations(annotations)
	u.SetOwnerReferences(o.OwnerReferences)
	return u
}

func podinfoGetter(hr *unstructured.Unstructured, objects ...*unstructured.Unstructured) *fakeDynamic {
	getter := &fakeDynamic{objects: map[string]*unstructured.Unstructured{
		refKey(ResourceRef{Group: "helm.toolkit.fluxcd.io", Kind: "HelmRelease", Namespace: "flux-system", Name: "podinfo"}): hr,
	}}
	for _, u := range objects {
		getter.objects[refKey(ResourceRef{Group: apiGroup(u), Kind: u.GetKind(), Namespace: u.GetNamespace(), Name: u.GetName()})] = u
	}
	return getter
}

func assertNoKind(t *testing.T, tree *ResourceTree, kind string) {
	t.Helper()
	for _, node := range tree.Nodes {
		if node.Ref.Kind == kind {
			t.Fatalf("%s shown in the release's tree: %+v", kind, node)
		}
	}
}

// A release that records no inventory keeps the objects Flux's origin labels
// name, wherever they live. A Helm hook stays outside the release, and an
// object a controller generated, which may carry labels its controller copied,
// shows under that controller rather than as a member beside it.
func TestHelmProjectionKeepsOnlyLabelledMembers(t *testing.T) {
	deploy := ownedObject("apps", "Deployment", "demo", "podinfo", nil, false)
	rs := ownedObject("apps", "ReplicaSet", "demo", "podinfo-abc", &deploy, true)
	pod := ownedObject("", "Pod", "demo", "podinfo-abc-1", &rs, true)
	hook := ownedObject("batch", "Job", "demo", "podinfo-migrate", nil, false)
	getter := podinfoGetter(helmRelease("flux-system", "podinfo"),
		liveObject(deploy, podinfoOrigin, nil),
		liveObject(pod, podinfoOrigin, nil),
		liveObject(hook, podinfoOrigin, map[string]string{"helm.sh/hook": "post-install"}),
	)
	topo := &topology.Topology{Nodes: []topology.Node{
		topoNode("Deployment", "demo", "podinfo", podinfoOrigin),
		topoNode("Pod", "demo", "podinfo-abc-1", podinfoOrigin),
		topoNode("Job", "demo", "podinfo-migrate", podinfoOrigin),
	}}
	got, _, err := NewBuilder(getter, topo).WithOwnership(ownershipSession(t, deploy, rs, pod, hook)).Build(context.Background(), "HelmRelease", "flux-system", "podinfo", "helm.toolkit.fluxcd.io")
	if err != nil {
		t.Fatal(err)
	}
	assertNodeRole(t, got, ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "demo", Name: "podinfo"}, RoleDeclared)
	assertNodeRole(t, got, ResourceRef{Kind: "Pod", Namespace: "demo", Name: "podinfo-abc-1"}, RoleGenerated)
	assertNoKind(t, got, "Job")
	if got.Summary.Declared != 1 {
		t.Fatalf("declared = %d, want only the Deployment: %+v", got.Summary.Declared, got.Nodes)
	}
}

// Helm keeps hooks out of a release's inventory, and the tree reads the
// inventory: a hook's origin labels, and the graph edge they could draw,
// don't add it.
func TestHelmInventoryLeavesOutLabelledHook(t *testing.T) {
	deploy := ownedObject("apps", "Deployment", "demo", "podinfo", nil, false)
	hook := ownedObject("batch", "Job", "demo", "podinfo-migrate", nil, false)
	hook.Labels, hook.Annotations = podinfoOrigin, map[string]string{"helm.sh/hook": "post-install"}
	release := ownedObject("helm.toolkit.fluxcd.io", "HelmRelease", "flux-system", "podinfo", nil, false)
	release.Membership, release.Members = ownership.MembersRecorded, []resourceid.Ref{deploy.Ref}
	hr := helmRelease("flux-system", "podinfo")
	if err := unstructured.SetNestedSlice(hr.Object, []any{map[string]any{"id": "demo_podinfo_apps_Deployment", "v": "v1"}}, "status", "inventory", "entries"); err != nil {
		t.Fatal(err)
	}
	topo := &topology.Topology{
		Nodes: []topology.Node{
			{ID: "hr", Kind: topology.KindHelmRelease, Name: "podinfo", Data: map[string]any{"namespace": "flux-system", "apiVersion": "helm.toolkit.fluxcd.io/v2"}},
			topoNode("Deployment", "demo", "podinfo", podinfoOrigin),
			topoNode("Job", "demo", "podinfo-migrate", podinfoOrigin),
		},
		Edges: []topology.Edge{{Source: "hr", Target: "Job/demo/podinfo-migrate", Type: topology.EdgeManages}},
	}
	got, _, err := NewBuilder(podinfoGetter(hr), topo).WithOwnership(ownershipSession(t, release, deploy, hook)).Build(context.Background(), "HelmRelease", "flux-system", "podinfo", "helm.toolkit.fluxcd.io")
	if err != nil {
		t.Fatal(err)
	}
	assertNodeRole(t, got, ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "demo", Name: "podinfo"}, RoleDeclared)
	assertNoKind(t, got, "Job")
}
