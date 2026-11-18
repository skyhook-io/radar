package server

import (
	"encoding/json"
	"fmt"

	"github.com/skyhook-io/radar/internal/k8s"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"os"
	"reflect"
	"testing"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
	"slices"
)

type applicationRootGolden struct {
	Case string `json:"case"`
	Root string `json:"root"`
	Key  string `json:"key"`
}

func applicationTopologyFixture(t *testing.T, topo *topology.Topology) *applicationInventory {
	t.Helper()
	var objects []ownershiptest.Object
	byID := map[string]int{}
	batch := &appRelationBatch{fields: map[string]map[resourceid.Ref][]topology.ResourceRef{}}
	for _, node := range topo.Nodes {
		ns, _ := node.Data["namespace"].(string)
		apiVersion, _ := node.Data["apiVersion"].(string)
		kind := topology.KubernetesKindForNode(&node)
		group := resourceid.GroupFromAPIVersion(apiVersion)
		if apiVersion == "" {
			group = resourceid.GroupForBuiltinKind(kind)
		}
		byID[node.ID] = len(objects)
		objects = append(objects, appTestObject(group, kind, ns, node.Name))
	}
	for _, edge := range topo.Edges {
		source, target := &objects[byID[edge.Source]], &objects[byID[edge.Target]]
		switch edge.Type {
		case topology.EdgeManages:
			if isGitOpsManagerKind(topology.NodeKind(source.Ref.Kind), source.Ref.Group) {
				source.Membership = ownership.MembersRecorded
				source.Members = append(source.Members, target.Ref)
			} else {
				target.OwnerReferences = append(target.OwnerReferences, source.OwnerRef(true))
			}
		case topology.EdgeExposes, topology.EdgeRoutesTo:
			switch source.Ref.Kind {
			case "Service":
				batch.add("Services", target.Ref, source.Ref)
			case "Ingress":
				batch.add("Ingresses", target.Ref, source.Ref)
			default:
				if isAppRouteKind(source.Ref.Kind) {
					batch.add("Routes", target.Ref, source.Ref)
				}
				if isAppRouteKind(target.Ref.Kind) {
					batch.add("Routes", source.Ref, target.Ref)
				}
			}
		}
	}
	batch.linkEntrypoints()
	session := appManagerSession(t, objects...)
	return &applicationInventory{batch: batch, session: session.(relations.Session), owners: ownership.NewView(session, ownership.Unrestricted())}
}

func applicationRootRow(g *applicationInventory, ref resourceid.Ref) (string, string) {
	root, kind, group, identity := g.rootOf(ref.Group, ref.Kind, ref.Namespace, ref.Name)
	in := rawInput(ref.Kind, ref.Namespace, ref.Name, "v1", "healthy")
	in.rootKey, in.rootKind, in.rootGroup, in.rootIdentity = root, kind, group, identity
	return root, groupApplications([]appWorkloadInput{in})[0].Key
}

func TestApplicationsStructuralPrecedenceGolden(t *testing.T) {
	workload := appTestObject("apps", "Deployment", "prod", "api")
	argo := appTestObject("argoproj.io", "Application", "argocd", "shop")
	other := appTestObject("argoproj.io", "Application", "argocd", "other")
	release := appTestObject("helm.toolkit.fluxcd.io", "HelmRelease", "flux-system", "shop")
	parent := appTestObject("argoproj.io", "Application", "argocd", "platform")
	operator := appTestObject("platform.example.io", "Platform", "prod", "database")
	var got []applicationRootGolden
	for _, tc := range []struct {
		name    string
		objects []ownershiptest.Object
		links   [][2]int
	}{
		// Shared managers of one kind resolve to the sorted-first recorder, whatever order they are read in.
		{"shared managers", []ownershiptest.Object{workload, argo, other}, [][2]int{{2, 0}, {1, 0}}},
		{"disagreeing manager and claim", []ownershiptest.Object{workload, argo, other}, [][2]int{{1, 0}}},
		{"labels without inventory", []ownershiptest.Object{workload, release}, [][2]int{{1, 0}}},
		{"app of apps", []ownershiptest.Object{workload, argo, parent}, [][2]int{{1, 0}, {2, 1}}},
		{"operator controller", []ownershiptest.Object{workload, operator, argo}, [][2]int{{1, 0}, {2, 0}}},
		{"operator noncontroller owner", []ownershiptest.Object{workload, operator}, [][2]int{{1, 0}}},
	} {
		for _, link := range tc.links {
			source, target := &tc.objects[link[0]], &tc.objects[link[1]]
			if isGitOpsManagerKind(topology.NodeKind(source.Ref.Kind), source.Ref.Group) {
				source.Membership = ownership.MembersRecorded
				source.Members = append(source.Members, target.Ref)
			} else {
				target.OwnerReferences = append(target.OwnerReferences, source.OwnerRef(tc.name == "operator controller"))
			}
		}
		if tc.name == "labels without inventory" {
			tc.objects[1].Membership, tc.objects[1].Members = ownership.MembersUnreadable, nil
			tc.objects[0].Labels = map[string]string{"helm.toolkit.fluxcd.io/name": "shop", "helm.toolkit.fluxcd.io/namespace": "flux-system"}
		}
		if tc.name == "disagreeing manager and claim" {
			tc.objects[0].Labels = map[string]string{"argocd.argoproj.io/instance": "other"}
		}
		session := appManagerSession(t, tc.objects...)
		inventory := &applicationInventory{session: session.(relations.Session), owners: ownership.NewView(session, ownership.Unrestricted())}
		root, key := applicationRootRow(inventory, workload.Ref)
		got = append(got, applicationRootGolden{Case: tc.name, Root: root, Key: key})
	}
	data, err := os.ReadFile("testdata/applications-structural-roots.json")
	if err != nil {
		t.Fatal(err)
	}
	var want []applicationRootGolden
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("grouping keys changed\ngot: %+v\nwant: %+v", got, want)
	}
}

func TestApplicationsNonControllerCronJobRootParity(t *testing.T) {
	withOwnershipDiscovery(t)
	addApplicationTypedDiscovery()
	owner := appTestObject("batch", "CronJob", "prod", "nightly")
	workload := appTestObject("batch", "Job", "prod", "run")
	workload.OwnerReferences = append(workload.OwnerReferences, owner.OwnerRef(false))
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "run", Namespace: "prod", UID: workload.UID, OwnerReferences: workload.OwnerReferences}}
	cronJob := &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: "prod", UID: owner.UID}}
	useTestResourceCache(t, fake.NewClientset(append(testNamespaces("prod"), job, cronJob)...))
	view := appManagerView(t, owner, workload)
	chain := view.ControllerChain(workload.Ref)
	if len(chain.Chain) != 1 || chain.Chain[0] != workload.Ref {
		t.Fatalf("controller chain = %+v", chain)
	}
	session, err := k8s.BeginOwnershipRead(k8s.GetResourceCache())
	if err != nil {
		t.Fatal(err)
	}
	candidate := &applicationInventory{session: session.(relations.Session), owners: ownership.NewView(session, ownership.Unrestricted())}
	root, key := applicationRootRow(candidate, workload.Ref)
	if root != "prod/CronJob/nightly" || key != "prod/CronJob/nightly" {
		t.Fatalf("existing grouping changed: root=%q key=%q", root, key)
	}
	if answer := view.Manager(workload.Ref); answer.State != ownership.ManagerNone {
		t.Fatalf("manager = %+v", answer)
	}

}

func TestApplicationsSharedManagerWinnerIsDeterministic(t *testing.T) {
	workload := appTestObject("apps", "Deployment", "prod", "api")
	shop := appTestObject("argoproj.io", "Application", "argocd", "shop")
	other := appTestObject("argoproj.io", "Application", "argocd", "other")
	for _, manager := range []*ownershiptest.Object{&shop, &other} {
		manager.Membership = ownership.MembersRecorded
		manager.Members = []resourceid.Ref{workload.Ref}
	}
	objects := []ownershiptest.Object{workload, shop, other}
	view := appManagerView(t, objects...)
	answer := view.Manager(workload.Ref)
	if answer.State != ownership.ManagerShared || answer.Lead != nil {
		t.Fatalf("manager answer = %+v, want shared without a lead", answer)
	}
	var recorders []resourceid.Ref
	for _, manager := range view.RecordingManagers(workload.Ref).Managers {
		recorders = append(recorders, manager.Ref)
	}
	if want := []resourceid.Ref{other.Ref, shop.Ref}; !reflect.DeepEqual(recorders, want) {
		t.Fatalf("recorders = %v, want %v", recorders, want)
	}
	for order := range 2 {
		session := appManagerSession(t, objects...)
		candidate := &applicationInventory{session: session.(relations.Session), owners: ownership.NewView(session, ownership.Unrestricted())}
		root, key := applicationRootRow(candidate, workload.Ref)
		if root != "argocd/Application/other" || key != "argocd/Application/other" {
			t.Fatalf("order=%d root=%q key=%q, want sorted-first other", order, root, key)
		}
		t.Logf("sorted-first winner with manager order %d: %q", order, key)
		slices.Reverse(objects)
	}
}

// When two Applications record a workload, the one its own tracking metadata
// names wins over the sorted-first one; a copied tracking ID names nothing.
func TestApplicationsSharedManagerPrefersTheClaimedOne(t *testing.T) {
	for _, tc := range []struct {
		name        string
		labels      map[string]string
		annotations map[string]string
		want        string
	}{
		{"tracking ID", nil, map[string]string{"argocd.argoproj.io/tracking-id": "shop:apps/Deployment:prod/api"}, "argocd/Application/shop"},
		{"instance label", map[string]string{"argocd.argoproj.io/instance": "shop"}, nil, "argocd/Application/shop"},
		{"copied tracking ID", nil, map[string]string{"argocd.argoproj.io/tracking-id": "shop:apps/Deployment:prod/other-api"}, "argocd/Application/other"},
		{"no claim", nil, nil, "argocd/Application/other"},
	} {
		workload := appTestObject("apps", "Deployment", "prod", "api")
		workload.Labels, workload.Annotations = tc.labels, tc.annotations
		shop := appTestObject("argoproj.io", "Application", "argocd", "shop")
		other := appTestObject("argoproj.io", "Application", "argocd", "other")
		for _, manager := range []*ownershiptest.Object{&shop, &other} {
			manager.Membership = ownership.MembersRecorded
			manager.Members = []resourceid.Ref{workload.Ref}
		}
		session := appManagerSession(t, workload, shop, other)
		candidate := &applicationInventory{session: session.(relations.Session), owners: ownership.NewView(session, ownership.Unrestricted())}
		if root, key := applicationRootRow(candidate, workload.Ref); root != tc.want || key != tc.want {
			t.Errorf("%s: root=%q key=%q, want %q", tc.name, root, key, tc.want)
		}
	}
}

func TestApplicationsRecordingManagerKindPrecedence(t *testing.T) {
	workload := appTestObject("apps", "Deployment", "prod", "api")
	argo := appTestObject("argoproj.io", "Application", "argocd", "z")
	kustomization := appTestObject("kustomize.toolkit.fluxcd.io", "Kustomization", "flux-system", "z")
	release := appTestObject("helm.toolkit.fluxcd.io", "HelmRelease", "flux-system", "z")
	other := appTestObject("helm.toolkit.fluxcd.io", "HelmRelease", "flux-system", "a")
	for _, m := range []*ownershiptest.Object{&argo, &kustomization, &release, &other} {
		m.Membership = ownership.MembersRecorded
		m.Members = []resourceid.Ref{workload.Ref}
	}
	for _, tc := range []struct {
		objects []ownershiptest.Object
		want    string
	}{
		{[]ownershiptest.Object{release, kustomization, argo, workload}, "argocd/Application/z"},
		{[]ownershiptest.Object{release, kustomization, workload}, "flux-system/Kustomization/z"},
		{[]ownershiptest.Object{release, other, workload}, "flux-system/HelmRelease/a"},
	} {
		session := appManagerSession(t, tc.objects...)
		inventory := &applicationInventory{session: session.(relations.Session), owners: ownership.NewView(session, ownership.Unrestricted())}
		root, key := applicationRootRow(inventory, workload.Ref)
		if root != tc.want || key != tc.want {
			t.Fatalf("root=%s key=%s want=%s", root, key, tc.want)
		}
	}
}

// A workload's root is the top of its owner chain however long the chain is,
// so workloads under one long non-controller chain group together.
func TestApplicationsLongNonControllerChainReachesTop(t *testing.T) {
	links := make([]ownershiptest.Object, 20)
	for i := range links {
		links[i] = appTestObject("example.io", "Link", "prod", fmt.Sprintf("link-%02d", i))
	}
	for i := 0; i+1 < len(links); i++ {
		links[i].OwnerReferences = append(links[i].OwnerReferences, links[i+1].OwnerRef(false))
	}
	workload := appTestObject("apps", "Deployment", "prod", "api")
	workload.OwnerReferences = append(workload.OwnerReferences, links[0].OwnerRef(false))
	session := appManagerSession(t, append(links, workload)...)
	got, _ := applicationStructuralRoot(session.(relations.Session), ownership.NewView(session, ownership.Unrestricted()), workload.Ref)
	if want := links[len(links)-1].Ref; got != want {
		t.Fatalf("root = %v, want the chain's top %v", got, want)
	}
}
