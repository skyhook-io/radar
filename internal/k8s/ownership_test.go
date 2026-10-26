package k8s

import (
	"errors"
	"fmt"
	goruntime "runtime"
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/cachesource"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
)

// scriptedSource opens sessions over the ownership fixture whose ValidateCaches
// returns the next scripted error.
type scriptedSource struct {
	t        *testing.T
	begin    error
	validate []error
	begun    int
	calls    []string
}

type scriptedSession struct {
	ownership.ReadSession
	validate error
	calls    *[]string
}

func (s scriptedSession) ValidateCaches() error {
	*s.calls = append(*s.calls, "validate")
	return s.validate
}

func (s *scriptedSource) Begin() (ownership.ReadSession, error) {
	if s.begin != nil {
		return nil, s.begin
	}
	inner, err := ownershiptest.Scan(ownershiptest.Fixture()).Begin()
	if err != nil {
		s.t.Fatal(err)
	}
	var v error
	if s.begun < len(s.validate) {
		v = s.validate[s.begun]
	}
	s.begun++
	return scriptedSession{ReadSession: inner, validate: v, calls: &s.calls}, nil
}

func TestAnswerOwnershipRefusesReplacedCaches(t *testing.T) {
	pod := resourceid.Ref{Kind: "Pod", Namespace: "alpha", Name: "web-1-a"}
	var fetched ownership.Observation
	for _, o := range ownershiptest.Fixture().Objects {
		if o.Ref == pod {
			fetched = ownership.Observation{Ref: o.Ref, UID: o.UID, OwnerReferences: o.OwnerReferences}
		}
	}
	unrestricted := func(*ownership.Catalog) ownership.Access { return ownership.Unrestricted() }
	for _, tc := range []struct {
		name        string
		source      *scriptedSource
		begun       int
		unavailable readreason.Code
	}{
		{"valid at once", &scriptedSource{}, 1, ""},
		{"caches replaced", &scriptedSource{validate: []error{ownership.ErrCachesReplaced}}, 1, readreason.ConnectionChanged},
		{"no caches", &scriptedSource{begin: errors.New("not connected")}, 0, readreason.ClusterConnecting},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.source.t = t
			rel := &topology.Relationships{}
			present := func(rel *topology.Relationships, _ *ownership.View, _ ownership.ReadSession) {
				tc.source.calls = append(tc.source.calls, "present")
				rel.OlderRevisions = append(rel.OlderRevisions, topology.ResourceRef{Group: "apps", Kind: "ReplicaSet", Namespace: "alpha", Name: "old"})
			}
			answerOwnership(tc.source, topology.ApplyOwnership, present, rel, fetched, unrestricted)
			if want := slices.Repeat([]string{"present", "validate"}, tc.begun); !slices.Equal(tc.source.calls, want) {
				t.Errorf("calls %v, want %v: each answer is presented before it is validated", tc.source.calls, want)
			}
			if wantInactive := map[bool]int{true: 1, false: 0}[tc.unavailable == ""]; len(rel.OlderRevisions) != wantInactive {
				t.Errorf("inactive children %v; a refused answer keeps none", rel.OlderRevisions)
			}
			if tc.source.begun != tc.begun {
				t.Errorf("began %d sessions, want %d", tc.source.begun, tc.begun)
			}
			if rel.Ownership == nil || rel.Ownership.UnavailableReason != string(tc.unavailable) {
				t.Fatalf("ownership = %+v, want unavailable %q", rel.Ownership, tc.unavailable)
			}
			if answered := rel.Owner != nil; answered != (tc.unavailable == "") {
				t.Fatalf("owner = %+v; an answer only when the session held", rel.Owner)
			}
		})
	}
}

// historyFixture is a Deployment at revision 3 with ReplicaSets and Pods in a
// real typed cache, and a source reading it with discovery serving them.
func historyFixture(t *testing.T, objects ...runtime.Object) (*fake.Clientset, *k8score.ResourceCache, ownership.Source, *appsv1.Deployment) {
	t.Helper()
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web", UID: "uid-web", Annotations: map[string]string{historyRevision: "3"}}}
	client := fake.NewClientset(append([]runtime.Object{deployment}, objects...)...)
	typed, err := k8score.NewResourceCache(k8score.CacheConfig{Client: client, ResourceTypes: map[string]bool{k8score.Pods: true, k8score.ReplicaSets: true, k8score.Deployments: true}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(typed.Stop)
	discovery, err := k8score.NewResourceDiscovery(fake.NewClientset().Discovery())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []k8score.APIResource{
		{Name: "pods", Kind: "Pod", Version: "v1"},
		{Name: "deployments", Kind: "Deployment", Group: "apps", Version: "v1"},
		{Name: "replicasets", Kind: "ReplicaSet", Group: "apps", Version: "v1"},
	} {
		r.Namespaced, r.Verbs = true, metav1.Verbs{"delete", "get", "list", "watch"}
		discovery.AddAPIResource(r)
	}
	source := cachesource.New(func() cachesource.Caches { return cachesource.Caches{Typed: typed, Discovery: discovery} })
	return client, typed, source, deployment
}

const historyRevision = "deployment.kubernetes.io/revision"

func historyReplicaSet(name, revision string, desired, running int32) *appsv1.ReplicaSet {
	controller := true
	return &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: name, UID: types.UID("uid-" + name), Annotations: map[string]string{historyRevision: revision},
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", UID: "uid-web", Controller: &controller}}},
		Spec:   appsv1.ReplicaSetSpec{Replicas: &desired},
		Status: appsv1.ReplicaSetStatus{Replicas: running},
	}
}

func historyPod(name, replicaSet string) *corev1.Pod {
	controller := true
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: name, UID: types.UID("uid-" + name), OwnerReferences: []metav1.OwnerReference{
		{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: replicaSet, UID: types.UID("uid-" + replicaSet), Controller: &controller},
	}}}
}

func historyChild(name string) topology.ResourceRef {
	return topology.ResourceRef{Group: "apps", Kind: "ReplicaSet", Namespace: "shop", Name: name}
}

// Before discovery answers, as while Radar starts, no kind can be searched or
// resolved: the answer is unavailable, not a list of children with every kind
// unread.
func TestAnswerOwnershipWaitsForDiscovery(t *testing.T) {
	_, typed, _, deployment := historyFixture(t, historyReplicaSet("web-3", "3", 1, 1))
	source := cachesource.New(func() cachesource.Caches { return cachesource.Caches{Typed: typed} })
	subject := resourceid.Ref{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "web"}
	unrestricted := func(*ownership.Catalog) ownership.Access { return ownership.Unrestricted() }
	for name, apply := range map[string]func(*topology.Relationships, *ownership.View, ownership.Observation){"full": topology.ApplyOwnership, "owner only": topology.ApplyOwner} {
		rel := &topology.Relationships{}
		answerOwnership(source, apply, nil, rel, ownership.Observe(subject, deployment), unrestricted)
		if rel.Ownership == nil || rel.Ownership.UnavailableReason != string(readreason.ClusterConnecting) || len(rel.Children) != 0 {
			t.Errorf("%s: ownership %+v, children %v; want unavailable while discovery hasn't answered", name, rel.Ownership, rel.Children)
		}
	}
}

// Only a Deployment or Rollout keeps rollback history, so the split gathers
// nothing for another owner: a ReplicaSet with hundreds of Pods, like a
// DaemonSet with thousands, costs what it costs without the split.
func TestSplitHistoryCostsNothingForAnOwnerWithoutHistory(t *testing.T) {
	const pods = 400
	objects := []runtime.Object{historyReplicaSet("web-big", "3", pods, pods)}
	for i := range pods {
		objects = append(objects, historyPod(fmt.Sprintf("web-big-%03d", i), "web-big"))
	}
	_, _, source, _ := historyFixture(t, objects...)
	rs := objects[0].(*appsv1.ReplicaSet)
	subject := resourceid.Ref{Group: "apps", Kind: "ReplicaSet", Namespace: "shop", Name: "web-big"}
	fetched := ownership.Observe(subject, rs)
	unrestricted := func(*ownership.Catalog) ownership.Access { return ownership.Unrestricted() }
	bytes := func(present func(*topology.Relationships, *ownership.View, ownership.ReadSession)) uint64 {
		const answers = 5
		var before, after goruntime.MemStats
		goruntime.ReadMemStats(&before)
		for range answers {
			rel := &topology.Relationships{}
			answerOwnership(source, topology.ApplyOwnership, present, rel, fetched, unrestricted)
			if len(rel.Children) != pods {
				t.Fatalf("%d children, want %d", len(rel.Children), pods)
			}
		}
		goruntime.ReadMemStats(&after)
		return (after.TotalAlloc - before.TotalAlloc) / answers
	}
	without, with := bytes(nil), bytes(splitHistory(subject, rs))
	if with > without+without/20 {
		t.Fatalf("the split adds %d bytes to an answer of %d; a ReplicaSet keeps no history", with-without, without)
	}
}

// A Deployment's history moves out of Children only for ReplicaSets the cache
// holds with no Pod naming them, read through the session's own cache.
func TestSplitHistoryReadsTheSessionsCache(t *testing.T) {
	_, _, source, deployment := historyFixture(t,
		historyReplicaSet("web-3", "3", 1, 1), historyPod("web-3-a", "web-3"),
		historyReplicaSet("web-2", "2", 0, 0),
		// Scaled down with a Pod still shutting down, on a cluster that
		// doesn't report terminatingReplicas.
		historyReplicaSet("web-1", "1", 0, 0), historyPod("web-1-a", "web-1"),
	)
	subject := resourceid.Ref{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "web"}
	rel := &topology.Relationships{}
	answerOwnership(source, topology.ApplyOwnership, splitHistory(subject, deployment), rel, ownership.Observe(subject, deployment), func(*ownership.Catalog) ownership.Access { return ownership.Unrestricted() })
	if want := []topology.ResourceRef{historyChild("web-1"), historyChild("web-3")}; !slices.Equal(rel.Children, want) {
		t.Errorf("children %v, want %v: the live revision and one still holding a Pod", rel.Children, want)
	}
	if want := []topology.ResourceRef{historyChild("web-2")}; !slices.Equal(rel.OlderRevisions, want) {
		t.Errorf("inactive children %v, want %v", rel.OlderRevisions, want)
	}

	scripted, err := ownershiptest.Scan(ownershiptest.Fixture()).Begin()
	if err != nil {
		t.Fatal(err)
	}
	rel = &topology.Relationships{Children: []topology.ResourceRef{historyChild("web-2")}}
	splitHistory(subject, deployment)(rel, ownership.NewView(scripted, ownership.Unrestricted()), scripted)
	if len(rel.OlderRevisions) != 0 {
		t.Errorf("a session over other caches moved %v; only the typed cache it reads can say", rel.OlderRevisions)
	}
}

func TestAnswerLiveManagerUsesLiveMetadataAndValidatesCaches(t *testing.T) {
	_, _, source, deployment := historyFixture(t)
	live := deployment.DeepCopy()
	live.Labels = map[string]string{"kustomize.toolkit.fluxcd.io/name": "live", "kustomize.toolkit.fluxcd.io/namespace": "flux-system"}
	subject := resourceid.Ref{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "web"}
	fetched := ownership.Observe(subject, live)
	for _, tc := range []struct {
		name string
		uid  types.UID
		lead bool
	}{
		{"same object", deployment.UID, true},
		{"replaced object", "different-uid", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fetched.UID = tc.uid
			rel := &topology.Relationships{}
			answerOwnership(liveOwnershipSource{Source: source, fetched: fetched}, topology.ApplyOwner, nil, rel, fetched, func(*ownership.Catalog) ownership.Access { return ownership.Unrestricted() })
			if !tc.lead {
				if rel.Ownership.Manager != nil || rel.Ownership.UnavailableReason == "" {
					t.Fatalf("replaced object answered %+v", rel.Ownership)
				}
				return
			}
			answer := rel.Ownership.Manager
			if answer == nil || answer.Lead == nil || answer.Lead.Name != "live" || len(answer.Direct.Claims) != 1 {
				t.Fatalf("manager = %+v; want the live metadata, not the cached object without labels", answer)
			}
		})
	}

	scripted := &scriptedSource{t: t, validate: []error{ownership.ErrCachesReplaced}}
	rel := &topology.Relationships{}
	answerOwnership(liveOwnershipSource{Source: scripted, fetched: fetched}, topology.ApplyOwner, nil, rel, fetched, func(*ownership.Catalog) ownership.Access { return ownership.Unrestricted() })
	if rel.Ownership.Manager != nil || rel.Ownership.UnavailableReason != string(readreason.ConnectionChanged) {
		t.Fatalf("cache switch kept manager %+v", rel.Ownership)
	}
}

type liveManagerAccess struct {
	ownership.Access
	denied resourceid.GroupKind
}

func (a liveManagerAccess) CanRead(kind resourceid.GroupKind, namespace string) ownership.Decision {
	if kind == a.denied {
		return ownership.Denied
	}
	return a.Access.CanRead(kind, namespace)
}

func TestAnswerLiveManagerPreservesCallerAccess(t *testing.T) {
	_, _, source, deployment := historyFixture(t)
	live := deployment.DeepCopy()
	live.Labels = map[string]string{"kustomize.toolkit.fluxcd.io/name": "live", "kustomize.toolkit.fluxcd.io/namespace": "flux-system"}
	subject := resourceid.Ref{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "web"}
	fetched := ownership.Observe(subject, live)
	for _, denied := range []resourceid.GroupKind{subject.GroupKind(), ownership.FluxKustomization} {
		rel := &topology.Relationships{}
		answerOwnership(liveOwnershipSource{Source: source, fetched: fetched}, topology.ApplyOwner, nil, rel, fetched, func(*ownership.Catalog) ownership.Access {
			return liveManagerAccess{Access: ownership.Unrestricted(), denied: denied}
		})
		if denied == subject.GroupKind() {
			if rel.Ownership.Manager != nil || rel.Ownership.UnavailableReason != string(readreason.UserForbidden) {
				t.Fatalf("live subject bypassed access: %+v", rel.Ownership)
			}
		} else {
			answer := rel.Ownership.Manager
			if answer == nil || !answer.Narrowed || len(answer.Direct.Claims) != 1 || answer.Direct.Claims[0].Check != "withheld" || len(answer.Direct.Recorders) != 0 {
				t.Fatalf("manager access = %+v", answer)
			}
		}
	}
}
