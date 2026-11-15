package relations

import (
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

func calicoPolicy(group, kind, namespace, name string, spec map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": group + "/v3", "kind": kind, "metadata": map[string]any{"name": name}, "spec": spec}}
	u.SetNamespace(namespace)
	return u
}

func TestCalicoPoliciesSelectAsCalicoDoes(t *testing.T) {
	web := labelled(deployment("web", mounts()), map[string]string{"app": "web"})
	api := labelled(deployment("api", mounts()), map[string]string{"app": "api"})
	np := calicoPolicy("projectcalico.org", "NetworkPolicy", "shop", "web", map[string]any{"selector": "app == 'web'"})
	checkSelects(t, CalicoPolicies, []runtime.Object{web, api, np}, np, web)

	// Calico's endpoint labels: every workload's pods carry their namespace.
	byNamespace := calicoPolicy("projectcalico.org", "NetworkPolicy", "shop", "ns", map[string]any{"selector": "projectcalico.org/namespace == 'shop'"})
	checkSelects(t, CalicoPolicies, []runtime.Object{web, api, byNamespace}, byNamespace, api, web)

	// A policy both groups serve is listed once.
	mirror := calicoPolicy("crd.projectcalico.org", "NetworkPolicy", "shop", "web", map[string]any{"selector": "app == 'web'"})
	got := NewView(allNamespaces{cluster: cluster{objects: []runtime.Object{web, np, mirror}}}, ownership.Unrestricted()).SelectedBy(CalicoPolicies, refOf(web), nil)
	if !slices.Equal(got.Matches, []resourceid.Ref{refOf(np)}) {
		t.Errorf("the workload's Calico policies are %v; want the policy once", got.Matches)
	}
}

func TestAGlobalCalicoPolicyReadsNamespaceLabelsOnlyWhenItSelectsOnThem(t *testing.T) {
	web := labelled(deployment("web", mounts()), map[string]string{"app": "web"})
	ns := typed(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop", Labels: map[string]string{"team": "retail"}}}, "", "v1", "Namespace")
	global := calicoPolicy("projectcalico.org", "GlobalNetworkPolicy", "", "retail", map[string]any{"selector": "all()", "namespaceSelector": "team == 'retail'"})
	checkSelects(t, CalicoPolicies, []runtime.Object{web, ns, global}, global, web)

	plain := calicoPolicy("projectcalico.org", "GlobalNetworkPolicy", "", "all", map[string]any{"selector": "app == 'web'"})
	unread := allNamespaces{cluster: cluster{objects: []runtime.Object{web, ns, global, plain}, unread: map[resourceid.GroupKind]readreason.Code{{Kind: "Namespace"}: readreason.KindSyncPending}}}
	if got := NewView(unread, ownership.Unrestricted()).Selected(CalicoPolicies, refOf(global), nil); len(got.Matches) != 0 || got.Coverage.Complete() {
		t.Errorf("with Namespaces unread the global policy selects %+v; want none, incomplete", got)
	}
	if got := NewView(unread, ownership.Unrestricted()).Selected(CalicoPolicies, refOf(plain), nil); !slices.Equal(got.Matches, []resourceid.Ref{refOf(web)}) || !got.Coverage.Complete() {
		t.Errorf("a global policy without a namespaceSelector needs no Namespace: %+v", got)
	}
}

func TestStagedCalicoPoliciesPreviewProtectionOnly(t *testing.T) {
	web := labelled(deployment("web", mounts()), map[string]string{"app": "web"})
	staged := calicoPolicy("projectcalico.org", "StagedNetworkPolicy", "shop", "next", map[string]any{"selector": "app == 'web'", "stagedAction": "Set"})
	checkSelects(t, CalicoStagedPolicies, []runtime.Object{web, staged}, staged, web)
	deletion := calicoPolicy("projectcalico.org", "StagedNetworkPolicy", "shop", "gone", map[string]any{"stagedAction": "Delete"})
	checkSelects(t, CalicoStagedPolicies, []runtime.Object{web, deletion}, deletion)

	kubernetes := calicoPolicy("projectcalico.org", "StagedKubernetesNetworkPolicy", "shop", "k8s", map[string]any{"podSelector": map[string]any{"matchLabels": map[string]any{"app": "web"}}})
	checkSelects(t, CalicoStagedPolicies, []runtime.Object{web, kubernetes}, kubernetes, web)
	if CalicoPolicies.Covers(resourceid.GroupKind{Group: "projectcalico.org", Kind: "StagedNetworkPolicy"}) {
		t.Error("enforced Calico policies cover a staged kind")
	}
}

func TestACalicoPolicyThatRulesAWorkloadOutNeedsNoNamespace(t *testing.T) {
	api := labelled(deployment("api", mounts()), map[string]string{"app": "api"})
	global := calicoPolicy("projectcalico.org", "GlobalNetworkPolicy", "", "retail", map[string]any{"selector": "app == 'web'", "namespaceSelector": "team == 'retail'"})
	c := allNamespaces{cluster: cluster{objects: []runtime.Object{api, global}, unread: map[resourceid.GroupKind]readreason.Code{{Kind: "Namespace"}: readreason.KindSyncPending}}}
	if got := NewView(c, ownership.Unrestricted()).SelectedBy(CalicoPolicies, refOf(api), nil); len(got.Matches) != 0 || !got.Coverage.Complete() {
		t.Errorf("a policy whose selector rules the workload out answered %+v; want none, complete", got)
	}
}

type calicoReads struct {
	allNamespaces
	t                       *testing.T
	localReads, globalReads int
}

func (s *calicoReads) Indexed(gk resourceid.GroupKind, index string, keys []string, scope ownership.SearchScope) IndexedRead {
	if slices.Contains(calicoGroups, gk.Group) {
		if strings.Contains(gk.Kind, "Global") {
			s.globalReads++
		} else {
			s.localReads++
			if index != cache.NamespaceIndex || !slices.Equal(keys, []string{"shop"}) || scope.AllNamespaces {
				s.t.Errorf("namespaced policy read widely: %s %q %+v", index, keys, scope)
			}
		}
	}
	return s.allNamespaces.Indexed(gk, index, keys, scope)
}

func TestCalicoReverseReadsNamespacedPoliciesOnlyInSubjectNamespace(t *testing.T) {
	web := labelled(deployment("web", mounts()), map[string]string{"app": "web"})
	for _, family := range []*SelectorFamily{CalicoPolicies, CalicoStagedPolicies} {
		s := &calicoReads{allNamespaces: allNamespaces{cluster: cluster{objects: []runtime.Object{web}}}, t: t}
		NewView(s, ownership.Unrestricted()).SelectedBy(family, refOf(web), nil)
		if s.localReads == 0 || s.globalReads == 0 {
			t.Fatalf("missing policy scope: local %d global %d", s.localReads, s.globalReads)
		}
	}
}

func TestCalicoMatcherMemoTracksUIDAndResourceVersion(t *testing.T) {
	p := calicoPolicy("projectcalico.org", "NetworkPolicy", "shop", "memo", map[string]any{"selector": "app == 'web'"})
	p.SetUID("memo-uid")
	p.SetResourceVersion("1")
	first := compiledCalicoMatcher(p)
	if first != compiledCalicoMatcher(p.DeepCopy()) {
		t.Fatal("same UID/version was recompiled")
	}
	p.SetResourceVersion("2")
	if first == compiledCalicoMatcher(p) {
		t.Fatal("new version reused old matcher")
	}
	p.SetResourceVersion("1")
	p.SetUID("new-memo-uid")
	if first == compiledCalicoMatcher(p) {
		t.Fatal("new UID reused old matcher")
	}
	p.SetUID("")
	if compiledCalicoMatcher(p) == compiledCalicoMatcher(p) {
		t.Fatal("identity-free policy was memoized")
	}
}
