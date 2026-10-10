package topology

import (
	"fmt"
	"sort"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
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
	if gateway == nil || gateway.Owner != nil {
		t.Fatalf("Gateway owner = %+v, want none: gatewayClassName is not an owner reference", gateway)
	}
	if got := refNames(gateway.Managers); len(got) != 1 || got[0] != "GatewayClass/istio" {
		t.Errorf("Gateway managers = %v", got)
	}
	if top := SynthesizeManagedBy(nil, "Gateway", "team", "public", topo, nil, idx); len(top) != 0 {
		t.Errorf("Gateway ManagedBy = %+v, want none: a class is not a manager by ownership", top)
	}
	class := GetRelationshipsWithObject("GatewayClass", "", "istio", nil, topo, nil, nil, idx)
	if class == nil || len(class.Children) != 0 {
		t.Fatalf("GatewayClass children = %+v, want none", class)
	}
	if got := refNames(class.Manages); len(got) != 1 || got[0] != "Gateway/public" {
		t.Errorf("GatewayClass manages = %v", got)
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
