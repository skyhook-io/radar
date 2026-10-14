package topology

import (
	"fmt"
	"sort"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
)

func ownerRef(apiVersion, kind, name string, uid types.UID, controller bool) metav1.OwnerReference {
	return metav1.OwnerReference{APIVersion: apiVersion, Kind: kind, Name: name, UID: uid, Controller: &controller}
}

// ownershipFixture builds the relationship-cache topology for a cluster with
// real owner chains next to management links that carry no owner reference.
func ownershipFixture(t *testing.T) *Topology {
	t.Helper()
	meta := func(name string, uid types.UID, owners ...metav1.OwnerReference) metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: name, Namespace: "team", UID: uid, OwnerReferences: owners}
	}
	labels := map[string]string{"app": "web"}
	var pods []*corev1.Pod
	for i := 0; i < 6; i++ {
		pods = append(pods, &corev1.Pod{ObjectMeta: meta(fmt.Sprintf("web-abc-%d", i), types.UID(fmt.Sprintf("pod-%d", i)), ownerRef("apps/v1", "ReplicaSet", "web-abc", "rs", true))})
		pods[i].Labels = labels
	}
	pods = append(pods, &corev1.Pod{ObjectMeta: meta("nightly-1-pod", "job-pod", ownerRef("batch/v1", "Job", "nightly-1", "job", true))})
	provider := &mockProvider{
		deployments: []*appsv1.Deployment{{ObjectMeta: meta("web", "deploy")}},
		replicaSets: []*appsv1.ReplicaSet{{ObjectMeta: meta("web-abc", "rs", ownerRef("apps/v1", "Deployment", "web", "deploy", true))}},
		cronJobs: []*batchv1.CronJob{
			{ObjectMeta: meta("nightly", "cron")},
			{ObjectMeta: meta("audit", "audit")},
		},
		// The Job is held by two CronJobs; only the controller is "nightly".
		jobs: []*batchv1.Job{{ObjectMeta: meta("nightly-1", "job",
			ownerRef("batch/v1", "CronJob", "nightly", "cron", true),
			ownerRef("batch/v1", "CronJob", "audit", "audit", false))}},
		pods: pods,
	}

	classGVR := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gatewayclasses"}
	gatewayGVR := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"}
	class := genericIdentityObject(classGVR, "GatewayClass", "", "istio")
	class.SetUID("class")
	gateway := genericIdentityObject(gatewayGVR, "Gateway", "team", "public")
	gateway.SetUID("gateway")
	gateway.Object["spec"] = map[string]any{"gatewayClassName": "istio"}
	dynamic := &genericIdentityDynamic{
		watched:   []schema.GroupVersionResource{classGVR, gatewayGVR},
		kinds:     map[schema.GroupVersionResource]string{classGVR: "GatewayClass", gatewayGVR: "Gateway"},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{classGVR: {class}, gatewayGVR: {gateway}},
		listCalls: map[schema.GroupVersionResource]int{},
	}

	opts := DefaultBuildOptions()
	opts.ViewMode = ViewModeResources
	opts.IncludeReplicaSets = true
	opts.ForRelationshipCache = true
	topo, err := NewBuilder(provider).WithDynamic(dynamic).Build(opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return topo
}

func refNames(refs []ResourceRef) []string {
	var names []string
	for _, ref := range refs {
		names = append(names, ref.Kind+"/"+ref.Name)
	}
	sort.Strings(names)
	return names
}

func TestOwnershipFollowsOwnerReferencesOnly(t *testing.T) {
	topo := ownershipFixture(t)
	idx := IndexByResource(topo)

	// The graph still draws the CronJob → Pod shortcut; it is never the owner.
	pod := GetRelationshipsWithObject("Pod", "team", "nightly-1-pod", nil, topo, nil, nil, idx)
	if pod == nil || pod.Owner == nil || pod.Owner.Kind != "Job" || pod.Owner.Name != "nightly-1" {
		t.Fatalf("Pod owner = %+v, want Job nightly-1", pod)
	}
	if len(pod.Managers) != 0 {
		t.Errorf("shortcut edge surfaced as a manager: %v", refNames(pod.Managers))
	}
	if top := SynthesizeManagedBy(nil, "Pod", "team", "nightly-1-pod", topo, nil, idx); len(top) != 1 || top[0].Name != "nightly" {
		t.Errorf("ManagedBy = %+v, want controller chain to CronJob nightly", top)
	}
	for _, edge := range topo.Edges {
		if edge.Target == "pod/team/nightly-1-pod" && edge.SkipIfKindVisible != "" && edge.Source != "cronjob/team/nightly" {
			t.Errorf("Pod shortcut drawn from %s, want the Job's controller CronJob", edge.Source)
		}
	}
	cron := GetRelationshipsWithObject("CronJob", "team", "nightly", nil, topo, nil, nil, idx)
	if got := refNames(cron.Children); len(got) != 1 || got[0] != "Job/nightly-1" {
		t.Errorf("CronJob children = %v, want only its Job", got)
	}

	gateway := GetRelationshipsWithObject("Gateway", "team", "public", nil, topo, nil, nil, idx)
	if gateway == nil || gateway.Owner != nil || len(gateway.Managers) != 0 {
		t.Fatalf("Gateway relationships = %+v, want no owner or manager: gatewayClassName is a reference", gateway)
	}
	if got := refNames(gateway.Dependencies); len(got) != 1 || got[0] != "GatewayClass/istio" {
		t.Errorf("Gateway dependencies = %v", got)
	}
	if top := SynthesizeManagedBy(nil, "Gateway", "team", "public", topo, nil, idx); len(top) != 0 {
		t.Errorf("Gateway ManagedBy = %+v, want none: a class is not a manager by ownership", top)
	}
	class := GetRelationshipsWithObject("GatewayClass", "", "istio", nil, topo, nil, nil, idx)
	if class == nil || len(class.Children) != 0 || len(class.Manages) != 0 {
		t.Fatalf("GatewayClass relationships = %+v, want no children or managed resources", class)
	}
	if got := refNames(class.Dependents); len(got) != 1 || got[0] != "Gateway/public" {
		t.Errorf("GatewayClass dependents = %v", got)
	}
}

func TestRelationshipCacheKeepsEveryPodRelated(t *testing.T) {
	topo := ownershipFixture(t)
	idx := IndexByResource(topo)
	for i := 0; i < 6; i++ {
		rel := GetRelationshipsWithObject("Pod", "team", fmt.Sprintf("web-abc-%d", i), nil, topo, nil, nil, idx)
		if rel == nil || rel.Owner == nil || rel.Owner.Name != "web-abc" || rel.Deployment == nil || rel.Deployment.Name != "web" {
			t.Fatalf("replica %d of six: %+v", i, rel)
		}
	}
}

func TestCascadePreviewMatchesGarbageCollection(t *testing.T) {
	topo := ownershipFixture(t)
	preview := func(kind, namespace, name, group string) []string {
		p := GetCascadeDeletePreview(ResourceRef{Kind: kind, Namespace: namespace, Name: name, Group: group}, topo, nil)
		if !p.RootResolved {
			t.Fatalf("%s/%s unresolved", kind, name)
		}
		return refNames(p.Dependents)
	}

	got := preview("Deployment", "team", "web", "")
	if len(got) != 7 || got[6] != "ReplicaSet/web-abc" {
		t.Errorf("Deployment dependents = %v, want the ReplicaSet and all six Pods", got)
	}
	// The Job survives: CronJob audit still owns it, so its Pod does too.
	if got := preview("CronJob", "team", "nightly", ""); len(got) != 0 {
		t.Errorf("CronJob nightly dependents = %v, want none while audit still owns the Job", got)
	}
	// A GatewayClass is referenced by Gateways, not their owner.
	if got := preview("GatewayClass", "", "istio", ""); len(got) != 0 {
		t.Errorf("GatewayClass dependents = %v, want none", got)
	}
}

func TestCascadePreviewHoldsBackDependentsWithUnseenOwners(t *testing.T) {
	meta := func(name string, uid types.UID, owners ...metav1.OwnerReference) metav1.ObjectMeta {
		return metav1.ObjectMeta{Name: name, Namespace: "team", UID: uid, OwnerReferences: owners}
	}
	rs := ownerRef("apps/v1", "ReplicaSet", "web-abc", "rs", true)
	provider := &mockProvider{
		deployments: []*appsv1.Deployment{{ObjectMeta: meta("web", "deploy")}},
		replicaSets: []*appsv1.ReplicaSet{{ObjectMeta: meta("web-abc", "rs", ownerRef("apps/v1", "Deployment", "web", "deploy", true))}},
		cronJobs:    []*batchv1.CronJob{{ObjectMeta: meta("audit", "audit-now")}},
		pods: []*corev1.Pod{
			{ObjectMeta: meta("only-rs", "p1", rs)},
			// A second owner Radar doesn't observe may be live.
			{ObjectMeta: meta("shared", "p2", rs, ownerRef("example.io/v1", "Holder", "keeper", "keeper", false))},
			// A second owner whose UID names a deleted incarnation doesn't count.
			{ObjectMeta: meta("stale", "p3", rs, ownerRef("batch/v1", "CronJob", "audit", "audit-old", false))},
		},
	}
	opts := DefaultBuildOptions()
	opts.ViewMode = ViewModeResources
	opts.IncludeReplicaSets = true
	opts.ForRelationshipCache = true
	topo, err := NewBuilder(provider).Build(opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	p := GetCascadeDeletePreview(ResourceRef{Kind: "Deployment", Namespace: "team", Name: "web"}, topo, nil)
	if got := refNames(p.Dependents); fmt.Sprint(got) != "[Pod/only-rs Pod/stale ReplicaSet/web-abc]" {
		t.Errorf("certain dependents = %v", got)
	}
	if got := refNames(p.PossibleDependents); fmt.Sprint(got) != "[Pod/shared]" {
		t.Errorf("possible dependents = %v, want the Pod with an unseen owner", got)
	}
}

func TestStorageBindingsAreNotOwnership(t *testing.T) {
	provider := &mockProvider{
		pvcs: []*corev1.PersistentVolumeClaim{{ObjectMeta: metav1.ObjectMeta{Name: "data", Namespace: "team"}, Spec: corev1.PersistentVolumeClaimSpec{VolumeName: "pv-1"}}},
		pvs: []*corev1.PersistentVolume{{ObjectMeta: metav1.ObjectMeta{Name: "pv-1"}, Spec: corev1.PersistentVolumeSpec{
			StorageClassName: "fast",
			ClaimRef:         &corev1.ObjectReference{Namespace: "team", Name: "data"},
		}}},
	}
	topo := &Topology{Nodes: []Node{
		{ID: "persistentvolumeclaim/team/data", Kind: KindPVC, Name: "data", Data: map[string]any{"namespace": "team"}},
		{ID: "persistentvolume//pv-1", Kind: KindPV, Name: "pv-1", Data: map[string]any{}},
		{ID: "storageclass//fast", Kind: KindStorageClass, Name: "fast", Data: map[string]any{}},
	}}
	claim := GetRelationshipsWithObject("PersistentVolumeClaim", "team", "data", nil, topo, provider, nil, nil)
	if claim == nil || len(claim.Children) != 0 || len(claim.StorageRefs) != 1 || claim.StorageRefs[0].Name != "pv-1" {
		t.Errorf("claim relationships = %+v, want its bound volume as storage, not a child", claim)
	}
	class := GetRelationshipsWithObject("StorageClass", "", "fast", nil, topo, provider, nil, nil)
	if class == nil || len(class.Children) != 0 || len(class.Consumers) != 1 || class.Consumers[0].Name != "pv-1" {
		t.Errorf("class relationships = %+v, want its volumes under Used By, not children", class)
	}
}

func TestWatchedAnalysisRunKeepsItsRolloutOwnership(t *testing.T) {
	rolloutGVR := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}
	runGVR := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "analysisruns"}
	rollout := genericIdentityObject(rolloutGVR, "Rollout", "team", "web")
	rollout.SetUID("rollout")
	rollout.Object["spec"] = map[string]any{"strategy": map[string]any{"canary": map[string]any{}}}
	rollout.Object["status"] = map[string]any{"canary": map[string]any{"currentStepAnalysisRunStatus": map[string]any{"name": "web-run", "status": "Running"}}}
	run := genericIdentityObject(runGVR, "AnalysisRun", "team", "web-run", ownerRef("argoproj.io/v1alpha1", "Rollout", "web", "rollout", true))
	run.SetUID("run")

	for _, watched := range []bool{true, false} {
		gvrs := []schema.GroupVersionResource{rolloutGVR}
		if watched {
			gvrs = append(gvrs, runGVR)
		}
		dynamic := &genericIdentityDynamic{
			watched:   gvrs,
			kinds:     map[schema.GroupVersionResource]string{rolloutGVR: "Rollout", runGVR: "AnalysisRun"},
			resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{rolloutGVR: {rollout}, runGVR: {run}},
			listCalls: map[schema.GroupVersionResource]int{},
		}
		opts := DefaultBuildOptions()
		opts.ForRelationshipCache = true
		topo, err := NewBuilder(&mockProvider{}).WithDynamic(dynamic).Build(opts)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		rel := GetRelationshipsWithObject("Rollout", "team", "web", nil, topo, nil, nil, IndexByResource(topo))
		children, manages := refNames(rel.Children), refNames(rel.Manages)
		if watched && (fmt.Sprint(children) != "[AnalysisRun/web-run]" || len(manages) != 0) {
			t.Errorf("watched run: children %v, manages %v; want the owned run as a child", children, manages)
		}
		// Without a watch Radar can't see the run's owner reference.
		if !watched && (len(children) != 0 || fmt.Sprint(manages) != "[AnalysisRun/web-run]") {
			t.Errorf("unwatched run: children %v, manages %v", children, manages)
		}
	}
}

func TestCascadePreviewTreatsDeclaredStubOwnersAsUnresolved(t *testing.T) {
	ctrl := true
	topo := &Topology{
		Nodes: []Node{
			{ID: "replicaset/team/web-abc", Kind: KindReplicaSet, Name: "web-abc", uid: "rs", observed: true, Data: map[string]any{"namespace": "team", "apiVersion": "apps/v1"}},
			// A Secret only some declaration named; Radar hasn't observed it.
			{ID: "secret/team/tls", Kind: KindSecret, Name: "tls", Data: map[string]any{"namespace": "team"}},
			{ID: "pod/team/web-abc-1", Kind: KindPod, Name: "web-abc-1", uid: "pod", observed: true, Data: map[string]any{"namespace": "team"}, ownerReferences: []metav1.OwnerReference{
				{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-abc", UID: "rs", Controller: &ctrl},
				{APIVersion: "v1", Kind: "Secret", Name: "tls", UID: "secret"},
			}},
		},
		Edges: []Edge{{ID: "rs-pod", Source: "replicaset/team/web-abc", Target: "pod/team/web-abc-1", Type: EdgeManages, OwnerController: &ctrl}},
	}
	p := GetCascadeDeletePreview(ResourceRef{Kind: "ReplicaSet", Namespace: "team", Name: "web-abc", Group: ""}, topo, nil)
	if len(p.Dependents) != 0 || fmt.Sprint(refNames(p.PossibleDependents)) != "[Pod/web-abc-1]" {
		t.Errorf("dependents %v, possible %v; an unobserved owner must leave the Pod possible", refNames(p.Dependents), refNames(p.PossibleDependents))
	}
}

// servedButUnwatched resolves AnalysisRuns through discovery without watching
// them, and records any Get, which in the real cache starts a watch.
type servedButUnwatched struct {
	*genericIdentityDynamic
	run       schema.GroupVersionResource
	startedOn []string
}

func (s *servedButUnwatched) GetGVRWithGroup(kind, group string) (schema.GroupVersionResource, bool) {
	if kind == "AnalysisRun" && group == s.run.Group {
		return s.run, true
	}
	return s.genericIdentityDynamic.GetGVRWithGroup(kind, group)
}

func (s *servedButUnwatched) Get(gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	if gvr == s.run {
		s.startedOn = append(s.startedOn, namespace+"/"+name)
	}
	return s.genericIdentityDynamic.Get(gvr, namespace, name)
}

func TestTopologyBuildNeverStartsAnAnalysisRunWatch(t *testing.T) {
	rolloutGVR := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}
	runGVR := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "analysisruns"}
	rollout := genericIdentityObject(rolloutGVR, "Rollout", "team", "web")
	rollout.Object["spec"] = map[string]any{"strategy": map[string]any{"canary": map[string]any{}}}
	rollout.Object["status"] = map[string]any{"canary": map[string]any{"currentStepAnalysisRunStatus": map[string]any{"name": "web-run", "status": "Running"}}}
	dynamic := &servedButUnwatched{
		genericIdentityDynamic: &genericIdentityDynamic{
			watched:   []schema.GroupVersionResource{rolloutGVR},
			kinds:     map[schema.GroupVersionResource]string{rolloutGVR: "Rollout", runGVR: "AnalysisRun"},
			resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{rolloutGVR: {rollout}},
			listCalls: map[schema.GroupVersionResource]int{},
		},
		run: runGVR,
	}
	if _, err := NewBuilder(&mockProvider{}).WithDynamic(dynamic).Build(DefaultBuildOptions()); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(dynamic.startedOn) != 0 {
		t.Errorf("topology build read unwatched AnalysisRuns through Get: %v", dynamic.startedOn)
	}
}

// A dependent shared by two owners that both go with the root is deleted,
// whichever owner the walk reaches first.
func TestCascadePreviewRevisitsSharedDependents(t *testing.T) {
	ctrl, owner := true, false
	n := func(id, kind, name string, uid types.UID, owners ...metav1.OwnerReference) Node {
		return Node{ID: id, Kind: NodeKind(kind), Name: name, uid: uid, observed: true, ownerReferences: owners, Data: map[string]any{"namespace": "team", "apiVersion": "example.io/v1"}}
	}
	ref := func(kind, name string, uid types.UID, controller bool) metav1.OwnerReference {
		return metav1.OwnerReference{APIVersion: "example.io/v1", Kind: kind, Name: name, UID: uid, Controller: &controller}
	}
	edge := func(src, dst string, controller *bool) Edge {
		return Edge{ID: src + "-" + dst, Source: src, Target: dst, Type: EdgeManages, OwnerController: controller}
	}
	for _, order := range [][2]string{{"a", "b"}, {"b", "a"}} {
		topo := &Topology{
			Nodes: []Node{
				n("root/team/root", "Root", "root", "root"),
				n("a/team/a", "A", "a", "a", ref("Root", "root", "root", true)),
				n("b/team/b", "B", "b", "b", ref("Root", "root", "root", true)),
				n("x/team/x", "X", "x", "x", ref("A", "a", "a", true), ref("B", "b", "b", false)),
			},
		}
		// Edge order decides which owner's walk reaches the dependent first.
		first, second := order[0]+"/team/"+order[0], order[1]+"/team/"+order[1]
		topo.Edges = []Edge{
			edge("root/team/root", first, &ctrl),
			edge(first, "x/team/x", &ctrl),
			edge("root/team/root", second, &ctrl),
			edge(second, "x/team/x", &owner),
		}
		p := GetCascadeDeletePreview(ResourceRef{Kind: "Root", Namespace: "team", Name: "root", Group: ""}, topo, nil)
		if got := refNames(p.Dependents); fmt.Sprint(got) != "[A/a B/b X/x]" {
			t.Errorf("order %v: dependents %v (possible %v)", order, got, refNames(p.PossibleDependents))
		}
	}
}

func TestCascadePreviewRevisitsNestedSharedDependents(t *testing.T) {
	for _, sharedFirst := range []bool{true, false} {
		for _, cachedIndex := range []bool{false, true} {
			t.Run(fmt.Sprintf("shared-first=%t/cached-index=%t", sharedFirst, cachedIndex), func(t *testing.T) {
				node := func(name string, owners ...metav1.OwnerReference) Node {
					return Node{ID: "configmap/team/" + name, Kind: KindConfigMap, Name: name, uid: types.UID(name), observed: true, ownerReferences: owners, Data: map[string]any{"namespace": "team", "apiVersion": "v1"}}
				}
				ref := func(name string) metav1.OwnerReference {
					return ownerRef("v1", "ConfigMap", name, types.UID(name), false)
				}
				topo := &Topology{Nodes: []Node{
					node("root"), node("a", ref("root")), node("b", ref("a")),
					node("c", ref("a"), ref("b")), node("leaf", ref("c")),
				}}
				topo.Edges = addObservedOwnerEdges(topo.Nodes, nil)
				// Make the shared child wait for B in one order; in the other,
				// B is already marked when A first examines the shared child.
				var childrenOfA []int
				for i, edge := range topo.Edges {
					if edge.Source == "configmap/team/a" {
						childrenOfA = append(childrenOfA, i)
					}
				}
				if len(childrenOfA) != 2 {
					t.Fatalf("expected both observed children of A, got %v", childrenOfA)
				}
				i, j := childrenOfA[0], childrenOfA[1]
				if (topo.Edges[i].Target == "configmap/team/c") != sharedFirst {
					topo.Edges[i], topo.Edges[j] = topo.Edges[j], topo.Edges[i]
				}
				var index *RelationshipsIndex
				if cachedIndex {
					index = IndexByResource(topo)
				}
				preview := GetCascadeDeletePreviewWithIndex(ResourceRef{Kind: "ConfigMap", Namespace: "team", Name: "root"}, topo, nil, index)
				if got := fmt.Sprint(refNames(preview.Dependents)); got != "[ConfigMap/a ConfigMap/b ConfigMap/c ConfigMap/leaf]" {
					t.Fatalf("certain dependents: %s; possible: %v", got, refNames(preview.PossibleDependents))
				}
				if len(preview.PossibleDependents) != 0 {
					t.Fatalf("all owners are observed and deleted: %+v", preview.PossibleDependents)
				}
			})
		}
	}
}

func TestCompactNeighborhoodKeepsWorkloadContextAtHighReplicaCounts(t *testing.T) {
	labels := map[string]string{"app": "web"}
	provider := &mockProvider{
		deployments: []*appsv1.Deployment{{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "team", UID: "deployment"}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: labels}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: labels}}}}},
		replicaSets: []*appsv1.ReplicaSet{{ObjectMeta: metav1.ObjectMeta{Name: "web-abc", Namespace: "team", UID: "rs", OwnerReferences: []metav1.OwnerReference{ownerRef("apps/v1", "Deployment", "web", "deployment", true)}}}},
		services:    []*corev1.Service{{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "team"}, Spec: corev1.ServiceSpec{Selector: labels}}},
		pdbs:        []*policyv1.PodDisruptionBudget{{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "team"}, Spec: policyv1.PodDisruptionBudgetSpec{Selector: &metav1.LabelSelector{MatchLabels: labels}}}},
	}
	for i := range 30 {
		provider.pods = append(provider.pods, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("web-abc-%d", i), Namespace: "team", UID: types.UID(fmt.Sprintf("pod-%d", i)), Labels: labels, OwnerReferences: []metav1.OwnerReference{ownerRef("apps/v1", "ReplicaSet", "web-abc", "rs", true)}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}})
	}
	opts := DefaultBuildOptions()
	opts.IncludeReplicaSets = true
	opts.ForRelationshipCache = true
	compact, err := NewBuilder(provider).Build(opts)
	if err != nil {
		t.Fatal(err)
	}
	neighborhood := BuildNeighborhood(compact, ResourceRef{Kind: "Deployment", Group: "apps", Namespace: "team", Name: "web"}, NeighborhoodOptions{Hops: 2})
	if neighborhood.Truncated {
		t.Fatal("replica siblings consumed the neighborhood budget")
	}
	found := map[NodeKind]bool{}
	for _, node := range neighborhood.Nodes {
		found[node.Kind] = true
	}
	for _, kind := range []NodeKind{KindService, KindPDB, KindPodGroup} {
		if !found[kind] {
			t.Fatalf("compact neighborhood lost %s: %+v", kind, neighborhood.Nodes)
		}
	}
	individual, err := NewBuilder(provider).Build(opts)
	if err != nil {
		t.Fatal(err)
	}
	rel := GetRelationships("Pod", "team", "web-abc-29", individual, provider, nil)
	if rel == nil || rel.Owner == nil || rel.Owner.Name != "web-abc" || rel.Deployment == nil || rel.Deployment.Name != "web" {
		t.Fatalf("individual replica lost its relationships: %+v", rel)
	}
}

func TestInventoryManagerSurvivesVerifiedOwnerWalk(t *testing.T) {
	nodes := []Node{
		{ID: "application/team/app", Kind: KindApplication, Name: "app", Data: map[string]any{"namespace": "team", "apiVersion": "argoproj.io/v1alpha1"}},
		{ID: "deployment/team/web", Kind: KindDeployment, Name: "web", uid: "deployment", observed: true, Data: map[string]any{"namespace": "team"}},
		{ID: "replicaset/team/web-abc", Kind: KindReplicaSet, Name: "web-abc", uid: "rs", observed: true, ownerReferences: []metav1.OwnerReference{ownerRef("apps/v1", "Deployment", "web", "deployment", true)}, Data: map[string]any{"namespace": "team"}},
		{ID: "pod/team/web-abc-0", Kind: KindPod, Name: "web-abc-0", uid: "pod", observed: true, ownerReferences: []metav1.OwnerReference{ownerRef("apps/v1", "ReplicaSet", "web-abc", "rs", true)}, Data: map[string]any{"namespace": "team"}},
	}
	edges := addObservedOwnerEdges(nodes, []Edge{{Source: nodes[0].ID, Target: nodes[1].ID, Type: EdgeManages}})
	topo := &Topology{Nodes: nodes, Edges: edges}
	for _, index := range []*RelationshipsIndex{nil, IndexByResource(topo)} {
		refs := SynthesizeManagedBy(nil, "Pod", "team", "web-abc-0", topo, nil, index)
		if len(refs) != 1 || refs[0].Kind != "Application" || refs[0].Group != "argoproj.io" || refs[0].Name != "app" {
			t.Fatalf("inventory manager lost: %+v", refs)
		}
	}
	deployment := GetRelationships("Deployment", "team", "web", topo, nil, nil)
	if deployment.Owner != nil || len(deployment.Managers) != 1 {
		t.Fatalf("inventory became ownership: %+v", deployment)
	}
	preview := GetCascadeDeletePreview(ResourceRef{Kind: "Application", Group: "argoproj.io", Namespace: "team", Name: "app"}, topo, nil)
	if len(preview.Dependents) != 0 {
		t.Fatalf("inventory became garbage collection: %+v", preview)
	}
}

func TestNearestInventoryManagerSurvivesAnIntermediateOwner(t *testing.T) {
	nodes := []Node{
		{ID: "application/team/app", Kind: KindApplication, Name: "app", Data: map[string]any{"namespace": "team", "apiVersion": "argoproj.io/v1alpha1"}},
		{ID: "deployment/team/web", Kind: KindDeployment, Name: "web", uid: "deployment", observed: true, Data: map[string]any{"namespace": "team"}},
		{ID: "replicaset/team/web-abc", Kind: KindReplicaSet, Name: "web-abc", uid: "rs", observed: true, ownerReferences: []metav1.OwnerReference{ownerRef("apps/v1", "Deployment", "web", "deployment", true)}, Data: map[string]any{"namespace": "team"}},
		{ID: "pod/team/web-abc-0", Kind: KindPod, Name: "web-abc-0", uid: "pod", observed: true, ownerReferences: []metav1.OwnerReference{ownerRef("apps/v1", "ReplicaSet", "web-abc", "rs", true)}, Data: map[string]any{"namespace": "team"}},
	}
	topo := &Topology{Nodes: nodes, Edges: addObservedOwnerEdges(nodes, []Edge{{Source: nodes[0].ID, Target: nodes[2].ID, Type: EdgeManages}})}
	for _, index := range []*RelationshipsIndex{nil, IndexByResource(topo)} {
		refs := SynthesizeManagedBy(nil, "Pod", "team", "web-abc-0", topo, nil, index)
		if len(refs) != 1 || refs[0].Kind != "Application" || refs[0].Name != "app" {
			t.Fatalf("nearest manager lost: %+v", refs)
		}
	}
}

func TestGroupedCustomOwnerIsEnrolledWithoutBecomingGroupOwnership(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "parents.example.io", Version: "v1", Resource: "widgets"}
	parent := genericIdentityObject(gvr, "Widget", "team", "owner")
	parent.SetUID("owner-uid")
	dynamic := &genericIdentityDynamic{watched: []schema.GroupVersionResource{gvr}, kinds: map[schema.GroupVersionResource]string{gvr: "Widget"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {parent}}, listCalls: make(map[schema.GroupVersionResource]int)}
	provider := &mockProvider{}
	for i := range 7 {
		provider.pods = append(provider.pods, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("worker-%d", i), Namespace: "team", UID: types.UID(fmt.Sprintf("pod-%d", i)), Labels: map[string]string{"app": "workers"}, OwnerReferences: []metav1.OwnerReference{ownerRef("parents.example.io/v1", "Widget", "owner", "owner-uid", true)}}, Status: corev1.PodStatus{Phase: corev1.PodRunning}})
	}
	opts := DefaultBuildOptions()
	topo, err := NewBuilder(provider).WithDynamic(dynamic).Build(opts)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, edge := range topo.Edges {
		if edge.IsGeneratedOwner() {
			found = true
			if edge.OwnerController != nil {
				t.Fatalf("synthetic group became ownership: %+v", edge)
			}
		}
	}
	if !found {
		t.Fatalf("grouped custom ownership disappeared: %+v", topo)
	}
	rel := GetRelationships("Widget", "team", "owner", topo, provider, dynamic)
	if rel != nil && (len(rel.Children) != 0 || len(rel.Manages) != 0) {
		t.Fatalf("synthetic group became a resource reference: %+v", rel)
	}
	preview := GetCascadeDeletePreview(ResourceRef{Group: gvr.Group, Kind: "Widget", Namespace: "team", Name: "owner"}, topo, dynamic)
	if len(preview.Dependents) != 0 {
		t.Fatalf("synthetic group became garbage collection: %+v", preview)
	}
}

func TestActualPodGroupCRDKeepsItsMetadataAncestors(t *testing.T) {
	widgets := schema.GroupVersionResource{Group: "parents.example.io", Version: "v1", Resource: "widgets"}
	groups := schema.GroupVersionResource{Group: "scheduling.example.io", Version: "v1", Resource: "podgroups"}
	parent := genericIdentityObject(widgets, "Widget", "team", "root")
	parent.SetUID("root-uid")
	group := genericIdentityObject(groups, "PodGroup", "team", "batch")
	group.SetUID("group-uid")
	group.SetOwnerReferences([]metav1.OwnerReference{ownerRef("parents.example.io/v1", "Widget", "root", "root-uid", true)})
	provider := &mockProvider{pods: []*corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "team", UID: "pod-uid", OwnerReferences: []metav1.OwnerReference{ownerRef("scheduling.example.io/v1", "PodGroup", "batch", "group-uid", true)}}}}}
	dynamic := &genericIdentityDynamic{watched: []schema.GroupVersionResource{widgets, groups}, kinds: map[schema.GroupVersionResource]string{widgets: "Widget", groups: "PodGroup"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{widgets: {parent}, groups: {group}}, listCalls: make(map[schema.GroupVersionResource]int)}
	topo, err := NewBuilder(provider).WithDynamic(dynamic).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(topo.Nodes) != 3 {
		t.Fatalf("actual PodGroup lost metadata ancestors: %+v", topo.Nodes)
	}
	rel := GetRelationshipsWithObject("Widget", "team", "root", parent, topo, provider, dynamic, nil)
	if rel == nil || len(rel.Children) != 1 || rel.Children[0].Kind != "PodGroup" || rel.Children[0].Group != groups.Group {
		t.Fatalf("actual PodGroup omitted from children: %+v", rel)
	}
	preview := GetCascadeDeletePreview(ResourceRef{Kind: "Widget", Group: widgets.Group, Namespace: "team", Name: "root"}, topo, dynamic)
	if len(preview.Dependents) != 2 {
		t.Fatalf("actual PodGroup omitted from garbage collection: %+v", preview)
	}

}

func TestGroupedMembersKeepOnlyCurrentIncarnationAndExplicitAggregation(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "parents.example.io", Version: "v1", Resource: "widgets"}
	parent := genericIdentityObject(gvr, "Widget", "team", "owner")
	parent.SetUID("current")
	dynamic := &genericIdentityDynamic{watched: []schema.GroupVersionResource{gvr}, kinds: map[schema.GroupVersionResource]string{gvr: "Widget"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {parent}}, listCalls: make(map[schema.GroupVersionResource]int)}
	provider := &mockProvider{}
	for i := 0; i < 7; i++ {
		uid := types.UID("stale")
		if i == 6 {
			uid = "current"
		}
		provider.pods = append(provider.pods, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("worker-%d", i), Namespace: "team", UID: types.UID(fmt.Sprintf("pod-%d", i)), Labels: map[string]string{"app": "workers"}, OwnerReferences: []metav1.OwnerReference{ownerRef("parents.example.io/v1", "Widget", "owner", uid, false)}}})
	}
	for _, allStale := range []bool{false, true} {
		if allStale {
			provider.pods[6].OwnerReferences[0].UID = "stale"
		}
		topo, err := NewBuilder(provider).WithDynamic(dynamic).Build(DefaultBuildOptions())
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, edge := range topo.Edges {
			if edge.OwnerAggregation != nil {
				found = true
				if !*edge.OwnerAggregation || edge.OwnerAggregationController == nil || *edge.OwnerAggregationController || edge.OwnerController != nil {
					t.Fatalf("misclassified aggregate: %+v", edge)
				}
			}
		}
		if found == allStale {
			t.Fatalf("matching incarnation found=%v allStale=%v", found, allStale)
		}
	}
}
