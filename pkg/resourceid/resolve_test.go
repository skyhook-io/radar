package resourceid

import (
	"reflect"
	"testing"
)

type fakeCatalog struct {
	groups   map[string][]string
	complete bool
}

func (c fakeCatalog) GroupsForKind(kind string) []string { return c.groups[kind] }
func (c fakeCatalog) Complete() bool                     { return c.complete }

// gpuCluster serves the collisions Radar's GPU demo fixtures carry: a batch
// Job next to a Volcano Job, and PodGroups from both Volcano and KAI.
var gpuCluster = fakeCatalog{complete: true, groups: map[string][]string{
	"Job":      {"batch", "batch.volcano.sh"},
	"PodGroup": {"scheduling.volcano.sh", "scheduling.run.ai"},
	"Queue":    {"scheduling.volcano.sh"},
	"Pod":      {""},
}}

func TestReferenceConstructors(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ref   Reference
		group string
		src   GroupSource
	}{
		{"core apiVersion is the core group", ReferenceFromAPIVersion("v1", "Pod", "ns", "p"), "", GroupObserved},
		{"empty apiVersion is missing, not core", ReferenceFromAPIVersion("", "Pod", "ns", "p"), "", GroupMissing},
		{"CRD apiVersion", ReferenceFromAPIVersion("batch.volcano.sh/v1alpha1", "Job", "ml", "train"), "batch.volcano.sh", GroupObserved},
		{"Flux spells core as core", ObservedReference("core", "Service", "ns", "s"), "", GroupObserved},
		{"omitted field takes its API default", DefaultedReference("", false, "gateway.networking.k8s.io", "Gateway", "ns", "gw"), "gateway.networking.k8s.io", GroupDefaulted},
		{"explicitly empty field names the core group", DefaultedReference("", true, "gateway.networking.k8s.io", "Service", "ns", "api"), "", GroupObserved},
		{"present field beats the default", DefaultedReference("networking.istio.io", true, "gateway.networking.k8s.io", "Gateway", "ns", "gw"), "networking.istio.io", GroupObserved},
		{"typed informer object", RestoredReference("Deployment", "ns", "web"), "apps", GroupRestored},
		{"only built-ins can be restored", RestoredReference("Rollout", "ns", "web"), "", GroupMissing},
		{"user-typed kind", UnqualifiedReference("Job", "ml", "train"), "", GroupMissing},
	} {
		if tc.ref.Group != tc.group || tc.ref.GroupSource != tc.src {
			t.Errorf("%s: got group %q (%s), want %q (%s)", tc.name, tc.ref.Group, tc.ref.GroupSource, tc.group, tc.src)
		}
	}
}

func TestOwnerReferenceKeepsIncarnation(t *testing.T) {
	ref := OwnerReference("batch/v1", "Job", "train", "uid-1", "ml")
	res := ResolveCurrent(ref, nil)
	if !res.OK() || res.Ref != NewRef("batch", "Job", "ml", "train") || res.UID != "uid-1" {
		t.Fatalf("owner reference resolved to %+v", res)
	}
}

func TestResolveCurrent(t *testing.T) {
	for _, tc := range []struct {
		name       string
		ref        Reference
		catalog    Catalog
		want       Resolution
		group      string
		candidates []string
		uncertain  bool
	}{
		{
			name: "recorded group wins even when another group serves the Kind",
			ref:  ReferenceFromAPIVersion("batch.volcano.sh/v1alpha1", "Job", "ml", "train"), catalog: gpuCluster,
			want: Resolved, group: "batch.volcano.sh",
		},
		{
			name: "recorded core group is not re-inferred",
			ref:  ObservedReference("", "Service", "ns", "s"), catalog: gpuCluster,
			want: Resolved, group: "",
		},
		{
			name: "missing group on a built-in Kind picks the built-in and reports the shadow",
			ref:  UnqualifiedReference("Job", "ml", "train"), catalog: gpuCluster,
			want: InferredBuiltin, group: "batch", candidates: []string{"batch.volcano.sh"},
		},
		{
			name: "built-in inference needs no catalog",
			ref:  UnqualifiedReference("Deployment", "ns", "web"),
			want: InferredBuiltin, group: "apps",
		},
		{
			name: "custom Kind served by one group",
			ref:  UnqualifiedReference("Queue", "", "gpu"), catalog: gpuCluster,
			want: InferredUnique, group: "scheduling.volcano.sh",
		},
		{
			name: "incomplete discovery makes a unique match uncertain",
			ref:  UnqualifiedReference("Queue", "", "gpu"), catalog: fakeCatalog{groups: gpuCluster.groups},
			want: InferredUnique, group: "scheduling.volcano.sh", uncertain: true,
		},
		{
			name: "custom Kind served by several groups refuses to choose",
			ref:  UnqualifiedReference("PodGroup", "ml", "train"), catalog: gpuCluster,
			want: Ambiguous, candidates: []string{"scheduling.run.ai", "scheduling.volcano.sh"},
		},
		{
			name: "custom Kind without a catalog",
			ref:  UnqualifiedReference("PodGroup", "ml", "train"),
			want: Unresolved,
		},
		{
			name: "Kind nobody serves",
			ref:  UnqualifiedReference("Widget", "ns", "w"), catalog: gpuCluster,
			want: Unresolved,
		},
		{
			name: "catalog spellings of core and duplicates are normalized",
			ref:  UnqualifiedReference("Thing", "ns", "t"), catalog: fakeCatalog{complete: true, groups: map[string][]string{"Thing": {"core", ""}}},
			want: InferredUnique, group: "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := ResolveCurrent(tc.ref, tc.catalog)
			if res.Resolution != tc.want || res.Ref.Group != tc.group || res.Uncertain != tc.uncertain {
				t.Fatalf("got %s group %q uncertain %v, want %s group %q uncertain %v", res.Resolution, res.Ref.Group, res.Uncertain, tc.want, tc.group, tc.uncertain)
			}
			if !reflect.DeepEqual(res.Candidates, tc.candidates) {
				t.Fatalf("candidates = %v, want %v", res.Candidates, tc.candidates)
			}
			if res.OK() != (tc.want == Resolved || tc.want == InferredBuiltin || tc.want == InferredUnique) {
				t.Fatalf("OK() = %v for %s", res.OK(), res.Resolution)
			}
		})
	}
}

func TestResolveHistoricalNeverInfers(t *testing.T) {
	if res := ResolveHistorical(UnqualifiedReference("Pod", "ns", "p")); res.Resolution != Unresolved || res.OK() {
		t.Fatalf("a versionless stored Pod event resolved to %+v", res)
	}
	if res := ResolveHistorical(ReferenceFromAPIVersion("v1", "Pod", "ns", "p")); res.Resolution != Resolved || res.Ref.Group != "" {
		t.Fatalf("a stored core Pod event resolved to %+v", res)
	}
	if res := ResolveHistorical(RestoredReference("Job", "ml", "train")); res.Resolution != Resolved || res.Ref.Group != "batch" {
		t.Fatalf("a stored typed-informer Job row resolved to %+v", res)
	}
}

// A partial reference's answer depends on when it is resolved. Installing a
// second CRD that serves the same Kind turns yesterday's unique match into an
// ambiguity, which is why stored observations must not be resolved against
// current discovery.
func TestPartialReferencesResolveDifferentlyOverTime(t *testing.T) {
	ref := UnqualifiedReference("PodGroup", "ml", "train")
	before := fakeCatalog{complete: true, groups: map[string][]string{"PodGroup": {"scheduling.volcano.sh"}}}
	after := gpuCluster
	if res := ResolveCurrent(ref, before); res.Resolution != InferredUnique {
		t.Fatalf("before the second CRD: %s", res.Resolution)
	}
	if res := ResolveCurrent(ref, after); res.Resolution != Ambiguous {
		t.Fatalf("after the second CRD: %s", res.Resolution)
	}
}

// Deleting and recreating an object keeps its identity and changes its
// incarnation; replacing it with a same-named object of another group changes
// its identity.
func TestIdentityAcrossRecreation(t *testing.T) {
	old := ResolveCurrent(OwnerReference("batch/v1", "Job", "train", "uid-old", "ml"), nil)
	recreated := ResolveCurrent(OwnerReference("batch/v1", "Job", "train", "uid-new", "ml"), nil)
	replaced := ResolveCurrent(OwnerReference("batch.volcano.sh/v1alpha1", "Job", "train", "uid-v", "ml"), nil)
	if old.Ref != recreated.Ref || old.UID == recreated.UID {
		t.Fatalf("recreation: refs %v vs %v, uids %q vs %q", old.Ref, recreated.Ref, old.UID, recreated.UID)
	}
	if old.Ref == replaced.Ref {
		t.Fatalf("a same-named object in another group shares identity: %v", old.Ref)
	}
}
