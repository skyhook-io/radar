package server

import (
	"encoding/json"

	"github.com/skyhook-io/radar/internal/k8s"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"os"
	"reflect"
	"testing"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
)

type applicationRootGolden struct {
	Case string `json:"case"`
	Root string `json:"root"`
	Key  string `json:"key"`
}

func applicationRootFixture(objects []ownershiptest.Object, links [][2]int) *appGraph {
	g := &appGraph{topo: &topology.Topology{}, byID: map[string]topology.Node{}, byResource: map[string]string{}}
	for _, obj := range objects {
		ref := obj.Ref
		n := topology.Node{ID: ref.Key(), Kind: topology.NodeKind(ref.Kind), Name: ref.Name, Data: map[string]any{"namespace": ref.Namespace, "apiVersion": obj.APIVersion}}
		g.topo.Nodes = append(g.topo.Nodes, n)
		g.byID[n.ID], g.byResource[ref.Key()] = n, n.ID
	}
	for _, link := range links {
		from, to := objects[link[0]].Ref.Key(), objects[link[1]].Ref.Key()
		g.topo.Edges = append(g.topo.Edges, topology.Edge{ID: from + "->" + to, Source: from, Target: to, Type: topology.EdgeManages})
	}
	g.idx = topology.IndexByResource(g.topo)
	return g
}

func applicationRootRow(g *appGraph, ref resourceid.Ref) (string, string) {
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
		{"shared managers", []ownershiptest.Object{workload, argo, other}, [][2]int{{1, 0}, {2, 0}}},
		{"disagreeing manager and claim", []ownershiptest.Object{workload, argo, other}, [][2]int{{1, 0}}},
		{"labels without inventory", []ownershiptest.Object{workload, release}, [][2]int{{1, 0}}},
		{"app of apps", []ownershiptest.Object{workload, argo, parent}, [][2]int{{1, 0}, {2, 1}}},
		{"operator controller", []ownershiptest.Object{workload, operator, argo}, [][2]int{{1, 0}, {2, 0}}},
		{"operator noncontroller owner", []ownershiptest.Object{workload, operator}, [][2]int{{1, 0}}},
	} {
		root, key := applicationRootRow(applicationRootFixture(tc.objects, tc.links), workload.Ref)
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

func TestApplicationsControllerOnlyCutoverBlocker(t *testing.T) {
	owner := appTestObject("batch", "CronJob", "prod", "nightly")
	workload := appTestObject("batch", "Job", "prod", "run")
	workload.OwnerReferences = append(workload.OwnerReferences, owner.OwnerRef(false))
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "run", Namespace: "prod", UID: workload.UID, OwnerReferences: workload.OwnerReferences}}
	cronJob := &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: "nightly", Namespace: "prod", UID: owner.UID}}
	useTestResourceCache(t, fake.NewClientset(append(testNamespaces("prod"), job, cronJob)...))
	graph := buildAppGraph(k8s.GetResourceCache(), []string{"prod"})
	view := appManagerView(t, owner, workload)
	chain := view.ControllerChain(workload.Ref)
	if len(chain.Chain) != 1 || chain.Chain[0] != workload.Ref {
		t.Fatalf("controller chain = %+v", chain)
	}
	root, key := applicationRootRow(graph, workload.Ref)
	if root != "prod/CronJob/nightly" || key != "prod/CronJob/nightly" {
		t.Fatalf("existing grouping changed: root=%q key=%q", root, key)
	}
	if answer := view.Manager(workload.Ref); answer.State != ownership.ManagerNone {
		t.Fatalf("manager = %+v", answer)
	}
	t.Logf("cutover blocked: existing key %q, ControllerChain top with no manager would key %q", key, "prod/Job/run")
}

func TestApplicationsSharedManagerGraphOrderHasNoOwnershipEquivalent(t *testing.T) {
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
	for _, tc := range []struct {
		links [][2]int
		want  string
	}{
		{[][2]int{{1, 0}, {2, 0}}, "argocd/Application/shop"},
		{[][2]int{{2, 0}, {1, 0}}, "argocd/Application/other"},
	} {
		root, key := applicationRootRow(applicationRootFixture(objects, tc.links), workload.Ref)
		if root != tc.want || key != tc.want {
			t.Fatalf("links=%v: root=%q key=%q, want %q", tc.links, root, key, tc.want)
		}
		t.Logf("identical ownership evidence, incoming edges %v: root and key %q", tc.links, key)
	}
}
