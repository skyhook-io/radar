package ownership_test

import (
	"slices"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/api/meta"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/client-go/restmapper"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

func object(t *testing.T, w ownershiptest.World, group, kind, namespace, name string) ownershiptest.Object {
	t.Helper()
	ref := resourceid.Ref{Group: group, Kind: kind, Namespace: namespace, Name: name}
	for _, o := range w.Objects {
		if o.Ref == ref {
			return o
		}
	}
	t.Fatalf("no %v in the World", ref)
	return ownershiptest.Object{}
}

// resolve resolves the dependent's owner references in a scan of w.
func resolve(t *testing.T, w ownershiptest.World, dependent ownershiptest.Object) []ownership.OwnerResolution {
	t.Helper()
	s, _ := ownershiptest.Scan(w).Begin()
	read := s.Object(dependent.Ref)
	if read.State != ownership.Present {
		t.Fatalf("%v = %v", dependent.Ref, read.State)
	}
	var out []ownership.OwnerResolution
	for _, ref := range read.Object.OwnerReferences {
		out = append(out, ownership.ResolveOwner(s, read.Object, ref))
	}
	return out
}

// A state's name goes on the wire; a value no constant names reads as
// unknown rather than panicking.
func TestOwnerStateNames(t *testing.T) {
	for state, want := range map[ownership.OwnerState]string{
		ownership.OwnerUnknown: "unknown", ownership.OwnerFound: "found", ownership.OwnerGone: "gone",
		ownership.OwnerReplaced: "replaced", ownership.OwnerUnresolvable: "unresolvable", ownership.OwnerExcluded: "excluded",
		ownership.OwnerWithheld: "withheld", ownership.OwnerState(99): "unknown",
	} {
		if got := state.String(); got != want {
			t.Errorf("OwnerState(%d) = %q, want %q", state, got, want)
		}
	}
}

func TestMembershipStateNames(t *testing.T) {
	for state, want := range map[ownership.MembershipState]string{
		ownership.MembersUnreadable: "unreadable", ownership.MembersRecorded: "recorded", ownership.MembersRemote: "remote",
		ownership.MembershipState(99): "unreadable",
	} {
		if got := state.String(); got != want {
			t.Errorf("MembershipState(%d) = %q, want %q", state, got, want)
		}
	}
}

// Each case is how Kubernetes' garbage collector judges the reference.
func TestResolveOwnerJudgesReferencesAsTheCollectorDoes(t *testing.T) {
	w := ownershiptest.Fixture()
	ns := func(group, kind, namespace, name string) resourceid.Ref {
		return resourceid.Ref{Group: group, Kind: kind, Namespace: namespace, Name: name}
	}
	for _, tc := range []struct {
		name      string
		dependent ownershiptest.Object
		want      []ownership.OwnerState
		wantRefs  []resourceid.Ref
	}{
		{"controller chain", object(t, w, "", "Pod", "alpha", "web-1-a"), []ownership.OwnerState{ownership.OwnerFound}, []resourceid.Ref{ns("apps", "ReplicaSet", "alpha", "web-1")}},
		{"two owners", object(t, w, "", "Pod", "alpha", "web-1-b"), []ownership.OwnerState{ownership.OwnerFound, ownership.OwnerFound}, []resourceid.Ref{ns("apps", "ReplicaSet", "alpha", "web-1"), ns("", "ConfigMap", "alpha", "cfg")}},
		{"an owner in another namespace is looked up in the dependent's", object(t, w, "", "Pod", "beta", "stray"), []ownership.OwnerState{ownership.OwnerGone}, []resourceid.Ref{ns("apps", "ReplicaSet", "beta", "web-1")}},
		{"the right UID under the wrong kind and name", object(t, w, "", "Pod", "alpha", "wrong-coordinates"), []ownership.OwnerState{ownership.OwnerGone}, []resourceid.Ref{ns("apps", "Deployment", "alpha", "nope")}},
		{"a later incarnation", object(t, w, "", "Pod", "alpha", "replaced"), []ownership.OwnerState{ownership.OwnerReplaced}, []resourceid.Ref{ns("apps", "ReplicaSet", "alpha", "web-old")}},
		{"no such owner", object(t, w, "", "Pod", "alpha", "dangling"), []ownership.OwnerState{ownership.OwnerGone}, []resourceid.Ref{ns("apps", "ReplicaSet", "alpha", "gone")}},
		{"a cluster-scoped owner of a namespaced object", object(t, w, "example.io", "Gadget", "beta", "g1"), []ownership.OwnerState{ownership.OwnerFound}, []resourceid.Ref{ns("", "Node", "", "n1")}},
		{"a cluster-scoped owner of a cluster-scoped object", object(t, w, "example.io", "ClusterWidget", "", "cw1"), []ownership.OwnerState{ownership.OwnerFound}, []resourceid.Ref{ns("", "Node", "", "n1")}},
		{"a namespaced owner of a cluster-scoped object", object(t, w, "", "Node", "", "n2"), []ownership.OwnerState{ownership.OwnerUnresolvable}, []resourceid.Ref{{}}},
		{"an owner version discovery doesn't list", object(t, w, "batch", "Job", "alpha", "j1"), []ownership.OwnerState{ownership.OwnerUnknown}, []resourceid.Ref{{}}},
		{"an owner at another served version", object(t, w, "", "ConfigMap", "alpha", "versioned"), []ownership.OwnerState{ownership.OwnerFound}, []resourceid.Ref{ns("example.io", "Widget", "alpha", "w1")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := resolve(t, w, tc.dependent)
			if len(got) != len(tc.want) {
				t.Fatalf("%d resolutions, want %d", len(got), len(tc.want))
			}
			for i := range got {
				if got[i].State != tc.want[i] || got[i].Ref != tc.wantRefs[i] {
					t.Errorf("reference %d = %v at %v, want %v at %v", i, got[i].State, got[i].Ref, tc.want[i], tc.wantRefs[i])
				}
			}
		})
	}
}

func TestResolveOwnerRefusesMalformedReferences(t *testing.T) {
	w := ownershiptest.Fixture()
	s, _ := ownershiptest.Scan(w).Begin()
	pod := s.Object(resourceid.Ref{Kind: "Pod", Namespace: "alpha", Name: "web-1-a"}).Object
	for _, ref := range []metav1.OwnerReference{
		{APIVersion: "apps/v1/extra", Kind: "ReplicaSet", Name: "web-1", UID: "uid-alpha-web-1"},
		{Kind: "ReplicaSet", Name: "web-1", UID: "uid-alpha-web-1"},
		{APIVersion: "apps/v1", Name: "web-1", UID: "uid-alpha-web-1"},
		{APIVersion: "apps/v1", Kind: "ReplicaSet", UID: "uid-alpha-web-1"},
	} {
		if got := ownership.ResolveOwner(s, pod, ref); got.State != ownership.OwnerUnresolvable {
			t.Errorf("%+v = %v, want unresolvable", ref, got.State)
		}
	}
	if got := ownership.ResolveOwner(s, pod, metav1.OwnerReference{APIVersion: "v1", Kind: "Event", Name: "e1", UID: "uid-alpha-e1"}); got.State != ownership.OwnerExcluded {
		t.Errorf("an Event owner = %v, want excluded", got.State)
	}
}

// A kind discovery doesn't list may be installed since: the collector retries
// it, so the owner is unknown, not unresolvable, even with discovery whole.
func TestResolveOwnerOfAKindDiscoveryDoesntListIsUnknown(t *testing.T) {
	w := ownershiptest.Fixture()
	s, _ := ownershiptest.Scan(w).Begin()
	pod := s.Object(resourceid.Ref{Kind: "Pod", Namespace: "alpha", Name: "web-1-a"}).Object
	got := ownership.ResolveOwner(s, pod, metav1.OwnerReference{APIVersion: "apps/v1", Kind: "Sprocket", Name: "x", UID: "uid-x"})
	want := ownership.Gap{Kind: resourceid.GroupKind{Group: "apps", Kind: "Sprocket"}, Reason: readreason.KindUnknown}
	if got.State != ownership.OwnerUnknown || got.Gap != want {
		t.Fatalf("an owner kind discovery doesn't list = %v %+v, want unknown %+v", got.State, got.Gap, want)
	}
}

// Without every owner's store, or without discovery's whole answer, the
// resolution is unknown, never gone or unresolvable.
func TestResolveOwnerIsUnknownWithoutTheEvidence(t *testing.T) {
	w := ownershiptest.Fixture()
	w.Collection[resourceid.GroupKind{Group: "apps", Kind: "ReplicaSet"}] = ownershiptest.Collection{State: ownershiptest.Loading}
	got := resolve(t, w, object(t, w, "", "Pod", "alpha", "dangling"))
	if got[0].State != ownership.OwnerUnknown || got[0].Gap.Reason != readreason.KindSyncPending {
		t.Fatalf("owner in a loading kind = %v %+v, want unknown kind_sync_pending", got[0].State, got[0].Gap)
	}

	w = ownershiptest.Fixture()
	w.Partial = true
	if got := resolve(t, w, object(t, w, "batch", "Job", "alpha", "j1")); got[0].State != ownership.OwnerUnknown || got[0].Gap.Reason != readreason.ReadError {
		t.Fatalf("unlisted owner version with partial discovery = %v %+v, want unknown read_error", got[0].State, got[0].Gap)
	}
	s, _ := ownershiptest.Scan(w).Begin()
	pod := s.Object(resourceid.Ref{Kind: "Pod", Namespace: "alpha", Name: "web-1-a"}).Object
	if got := ownership.ResolveOwner(s, pod, metav1.OwnerReference{APIVersion: "apps/v1", Kind: "Sprocket", Name: "x", UID: "uid-x"}); got.State != ownership.OwnerUnknown {
		t.Fatalf("unlisted owner kind with partial discovery = %v, want unknown", got.State)
	}
}

// An Event owner is excluded, as an Event read is, even when partial
// discovery didn't list Events.
func TestResolveOwnerExcludesEventOwnersWhateverDiscoveryLists(t *testing.T) {
	w := ownershiptest.Fixture()
	w.Partial = true
	var resources []ownership.ServedResource
	for _, r := range w.Resources {
		if r.Kind != "Event" {
			resources = append(resources, r)
		}
	}
	w.Resources = resources
	s, _ := ownershiptest.Scan(w).Begin()
	pod := s.Object(resourceid.Ref{Kind: "Pod", Namespace: "alpha", Name: "web-1-a"}).Object
	for _, apiVersion := range []string{"v1", "events.k8s.io/v1"} {
		if got := ownership.ResolveOwner(s, pod, metav1.OwnerReference{APIVersion: apiVersion, Kind: "Event", Name: "e1", UID: "uid-alpha-e1"}); got.State != ownership.OwnerExcluded {
			t.Errorf("an Event owner at %s, Events unlisted = %v %+v, want excluded", apiVersion, got.State, got.Gap)
		}
	}
}

// collectorMapper is the discovery RESTMapper the garbage collector resolves
// owner references through, built from the same served resources.
func collectorMapper(resources []ownership.ServedResource) meta.RESTMapper {
	byGroup := map[string]*restmapper.APIGroupResources{}
	var groups []*restmapper.APIGroupResources
	for _, r := range resources {
		g := byGroup[r.Group]
		if g == nil {
			g = &restmapper.APIGroupResources{Group: metav1.APIGroup{Name: r.Group}, VersionedResources: map[string][]metav1.APIResource{}}
			byGroup[r.Group] = g
			groups = append(groups, g)
		}
		gv := schema.GroupVersion{Group: r.Group, Version: r.Version}.String()
		if !slices.ContainsFunc(g.Group.Versions, func(v metav1.GroupVersionForDiscovery) bool { return v.Version == r.Version }) {
			g.Group.Versions = append(g.Group.Versions, metav1.GroupVersionForDiscovery{GroupVersion: gv, Version: r.Version})
		}
		g.VersionedResources[r.Version] = append(g.VersionedResources[r.Version], metav1.APIResource{Name: r.Resource, Kind: r.Kind, Namespaced: r.Namespaced, Verbs: r.Verbs})
	}
	for _, g := range groups {
		g.Group.PreferredVersion = g.Group.Versions[0]
	}
	return restmapper.NewDiscoveryRESTMapper(groups)
}

// Every spelling of an owner reference the API server admits must resolve as
// the collector maps it, or be unknown where Radar can't tell: where its
// RESTMapper maps nothing, the collector retries, so the owner is unknown;
// where it maps a namespaced owner for a cluster-scoped object, the reference
// is unresolvable; where it maps a resource no API serves, as for the List
// kinds it makes up, Radar leaves the owner unknown; otherwise the owner is
// the object of the mapped kind at the reference's name, judged by UID,
// unless Radar can't read that kind.
func TestResolveOwnerMapsReferencesAsTheCollectorsRESTMapperDoes(t *testing.T) {
	w := ownershiptest.Fixture()
	s, _ := ownershiptest.Scan(w).Begin()
	mapper := collectorMapper(w.Resources)
	kindOf := map[schema.GroupResource]resourceid.GroupKind{}
	for _, r := range w.Resources {
		kindOf[schema.GroupResource{Group: r.Group, Resource: r.Resource}] = resourceid.GroupKind{Group: r.Group, Kind: r.Kind}
	}
	dependents := []ownership.Observation{
		{Ref: resourceid.Ref{Kind: "Pod", Namespace: "alpha", Name: "probe"}},
		{Ref: resourceid.Ref{Kind: "Node", Name: "probe"}},
	}
	checked := 0
	for _, r := range w.Resources {
		if ownership.IsEvent(resourceid.GroupKind{Group: r.Group, Kind: r.Kind}) {
			continue
		}
		var names []string
		uids := map[string]types.UID{}
		for _, o := range w.Objects {
			if o.Ref.Group == r.Group && o.Ref.Kind == r.Kind && !slices.Contains(names, o.Ref.Name) {
				names = append(names, o.Ref.Name)
				uids[o.Ref.Name] = o.UID
			}
		}
		names = append(names, "missing")
		spellings := []string{r.Kind, strings.ToLower(r.Kind), strings.ToUpper(r.Kind), r.Kind[:1] + strings.ToLower(r.Kind[1:]), r.Kind + "List", strings.ToLower(r.Kind) + "List", "List"}
		for _, version := range []string{r.Version, "v9", runtime.APIVersionInternal} {
			for _, kind := range spellings {
				for _, name := range names {
					for _, uid := range []types.UID{uids[name], "uid-other"} {
						if uid == "" {
							continue
						}
						ref := metav1.OwnerReference{APIVersion: resourceid.APIVersion(r.Group, version), Kind: kind, Name: name, UID: uid}
						if errs := apivalidation.ValidateOwnerReferences([]metav1.OwnerReference{ref}, field.NewPath("ownerReferences")); len(errs) > 0 {
							continue
						}
						for _, dependent := range dependents {
							checked++
							got := ownership.ResolveOwner(s, dependent, ref)
							want, wantRef, readable := collectorResolves(t, w, mapper, kindOf, dependent, ref)
							if got.State == ownership.OwnerUnknown && readable == false {
								continue
							}
							if got.State != want || (want != ownership.OwnerUnresolvable && got.Ref != wantRef) {
								t.Errorf("%s %s/%s uid %s, dependent %v: ResolveOwner = %v at %v; the collector maps it to %v at %v", ref.APIVersion, ref.Kind, ref.Name, ref.UID, dependent.Ref, got.State, got.Ref, want, wantRef)
							}
						}
					}
				}
			}
		}
	}
	if checked < 500 {
		t.Fatalf("only %d references checked", checked)
	}
}

// collectorResolves is what the collector's RESTMapper and a GET make of ref.
// readable is false when Radar can't read the mapped kind, so an unknown
// answer is honest. Radar also leaves unknown the List kinds the RESTMapper
// makes up, whose resources no API serves.
func collectorResolves(t *testing.T, w ownershiptest.World, mapper meta.RESTMapper, kindOf map[schema.GroupResource]resourceid.GroupKind, dependent ownership.Observation, ref metav1.OwnerReference) (ownership.OwnerState, resourceid.Ref, bool) {
	t.Helper()
	gv, err := schema.ParseGroupVersion(ref.APIVersion)
	if err != nil {
		return ownership.OwnerUnresolvable, resourceid.Ref{}, true
	}
	mapping, err := mapper.RESTMapping(schema.GroupKind{Group: gv.Group, Kind: ref.Kind}, gv.Version)
	if err != nil {
		// The collector retries a mapping error: the next discovery may serve
		// the kind.
		return ownership.OwnerUnknown, resourceid.Ref{}, true
	}
	gk, served := kindOf[mapping.Resource.GroupResource()]
	if !served {
		return ownership.OwnerGone, resourceid.Ref{}, false
	}
	namespaced := mapping.Scope.Name() == meta.RESTScopeNameNamespace
	if namespaced && dependent.Ref.Namespace == "" {
		return ownership.OwnerUnresolvable, resourceid.Ref{}, true
	}
	at := resourceid.Ref{Group: gv.Group, Kind: gk.Kind, Name: ref.Name}
	if namespaced {
		at.Namespace = dependent.Ref.Namespace
	}
	if c := w.Collection[gk]; c.State != ownershiptest.Collected {
		return ownership.OwnerUnknown, at, false
	}
	for _, o := range w.Objects {
		if o.Ref == at {
			if o.UID == ref.UID {
				return ownership.OwnerFound, at, true
			}
			return ownership.OwnerReplaced, at, true
		}
	}
	return ownership.OwnerGone, at, true
}

// References the collector's RESTMapper settles by the order it registered
// resources in, which Radar can't know, stay unknown rather than resolving to
// one candidate: a kind in lower case that two kinds differing only in case
// share, a served kind that is another's lower-case spelling, and the List
// kinds it makes up, one of which can name a served resource.
func TestResolveOwnerLeavesOrderDependentMappingsUnknown(t *testing.T) {
	gc := []string{"create", "delete", "get", "list", "patch", "update", "watch"}
	w := ownershiptest.Fixture()
	w.Resources = append(w.Resources,
		ownership.ServedResource{Group: "shapes.example.io", Version: "v1", Kind: "Thing", Resource: "things", Namespaced: true, Verbs: gc},
		ownership.ServedResource{Group: "shapes.example.io", Version: "v1", Kind: "THING", Resource: "allcapsthings", Namespaced: true, Verbs: gc},
		ownership.ServedResource{Group: "shapes.example.io", Version: "v1", Kind: "Gizmo", Resource: "gizmos", Namespaced: true, Verbs: gc},
		ownership.ServedResource{Group: "shapes.example.io", Version: "v1", Kind: "Gizmo", Resource: "gizmoalts", Namespaced: true, Verbs: gc},
		ownership.ServedResource{Group: "shapes.example.io", Version: "v1", Kind: "dial", Resource: "lowerdials", Namespaced: true, Verbs: gc},
		ownership.ServedResource{Group: "shapes.example.io", Version: "v1", Kind: "Dial", Resource: "dials", Namespaced: true, Verbs: gc},
		ownership.ServedResource{Group: "shapes.example.io", Version: "v1", Kind: "Other", Resource: "diallists", Namespaced: true, Verbs: gc},
	)
	s, err := ownershiptest.Scan(w).Begin()
	if err != nil {
		t.Fatal(err)
	}
	pod := ownership.Observation{Ref: resourceid.Ref{Kind: "Pod", Namespace: "alpha", Name: "probe"}}
	for _, kind := range []string{"thing", "dial", "GizmoList", "DialList", "List"} {
		got := ownership.ResolveOwner(s, pod, metav1.OwnerReference{APIVersion: "shapes.example.io/v1", Kind: kind, Name: "x", UID: "uid-x"})
		if got.State != ownership.OwnerUnknown || got.Gap.Reason == "" {
			t.Errorf("%s: %v %+v, want unknown with a reason", kind, got.State, got.Gap)
		}
	}
}
