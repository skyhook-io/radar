package k8s

import (
	goctx "context"
	"errors"
	"fmt"
	"reflect"
	goruntime "runtime"
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/cachesource"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
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

// Before discovery answers, as while Radar starts, a resource context says
// every field a relationship answer fills is unavailable, rather than empty,
// and still names the Node a Pod's own spec names.
func TestResourceContextWithoutARelationshipAnswer(t *testing.T) {
	_, typed, _, deployment := historyFixture(t)
	previous := ownershipSource
	ownershipSource = cachesource.New(func() cachesource.Caches { return cachesource.Caches{Typed: typed} })
	t.Cleanup(func() { ownershipSource = previous })
	access := func(*ownership.Catalog) ownership.Access { return ownership.Unrestricted() }
	context := func(obj runtime.Object) *resourcecontext.ResourceContext {
		rel, _, extras := AnswerResourceContextRelationships(&ResourceCache{ResourceCache: typed}, obj, access, access)
		if rel != nil || extras == nil {
			t.Fatalf("no answer for %T: %+v %+v", obj, rel, extras)
		}
		return resourcecontext.Build(goctx.Background(), obj, resourcecontext.Options{Relationships: rel, RelationshipExtras: extras})
	}
	obj := deployment.DeepCopy()
	obj.APIVersion, obj.Kind = "apps/v1", "Deployment"
	rc := context(obj)
	for _, field := range []string{"exposes", "exposedBy", "selectedBy", "scaledBy", "monitors", "stagedPolicies", "uses"} {
		if !slices.Contains(rc.Omitted, resourcecontext.OmittedField{Field: field, Reason: resourcecontext.OmittedUnavailable}) {
			t.Errorf("%s not marked unavailable: %+v", field, rc.Omitted)
		}
	}
	if slices.ContainsFunc(rc.Omitted, func(o resourcecontext.OmittedField) bool {
		return o.Field == "referencedBy" || o.Field == "runsOn" || o.Field == "backends" || o.Field == "dependents"
	}) {
		t.Errorf("a Deployment's context marked a field it doesn't have: %+v", rc.Omitted)
	}
	pod := &corev1.Pod{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}, ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web-a"}, Spec: corev1.PodSpec{NodeName: "worker"}}
	rc = context(pod)
	if rc.RunsOn == nil || rc.RunsOn.Name != "worker" || slices.ContainsFunc(rc.Omitted, func(o resourcecontext.OmittedField) bool { return o.Field == "runsOn" }) {
		t.Fatalf("Pod's Node = %+v, omitted %+v", rc.RunsOn, rc.Omitted)
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
	without, with := bytes(nil), bytes(splitHistory(subject, rs, nil))
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
	answerOwnership(source, topology.ApplyOwnership, splitHistory(subject, deployment, nil), rel, ownership.Observe(subject, deployment), func(*ownership.Catalog) ownership.Access { return ownership.Unrestricted() })
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
	splitHistory(subject, deployment, nil)(rel, ownership.NewView(scripted, ownership.Unrestricted()), scripted)
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

// The rows of one list read their chains in one session, as the caller, and
// a session whose caches were replaced answers nothing more.
func TestAgentOwnershipReadRowsInOneSession(t *testing.T) {
	_, _, source, _ := historyFixture(t, historyReplicaSet("web-3", "3", 2, 2), historyPod("web-3-a", "web-3"), historyPod("web-3-b", "web-3"))
	unrestricted := func(*ownership.Catalog) ownership.Access { return ownership.Unrestricted() }
	counted := &countingSource{Source: source}
	chains := &AgentOwnership{source: counted, access: unrestricted}
	for _, name := range []string{"web-3-a", "web-3-b"} {
		top := chains.Of(historyPod(name, "web-3"), podRef(name)).Top()
		if top == nil || *top != (topology.ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "web"}) {
			t.Fatalf("%s: top %v, want the Deployment", name, top)
		}
	}
	if counted.begun != 1 {
		t.Errorf("began %d sessions for two rows, want 1", counted.begun)
	}

	denied := &AgentOwnership{source: source, access: func(*ownership.Catalog) ownership.Access {
		return liveManagerAccess{Access: ownership.Unrestricted(), denied: resourceid.GroupKind{Group: "apps", Kind: "Deployment"}}
	}}
	if c := denied.Of(historyPod("web-3-a", "web-3"), podRef("web-3-a")); c == nil || c.Top() == nil || c.Top().Kind != "ReplicaSet" || c.Stop != "withheld" {
		t.Errorf("a Deployment the caller may not read: %+v, want the ReplicaSet, withheld", c)
	}

	// A row read from the API, not the caches, still walks up; here its owner
	// is gone, so there is nothing above it.
	if c := (&AgentOwnership{source: source, access: unrestricted}).Of(historyPod("web-9-a", "web-9"), podRef("web-9-a")); c == nil || c.Top() != nil {
		t.Errorf("a row whose owner is gone: %+v, want no top", c)
	}

	replaced := &AgentOwnership{source: &scriptedSource{t: t, validate: []error{ownership.ErrCachesReplaced}}, access: unrestricted}
	pod := historyPod("web-1-a", "web-1")
	pod.Namespace = "alpha"
	if c := replaced.Of(pod, resourceid.Ref{Kind: "Pod", Namespace: "alpha", Name: "web-1-a"}); c != nil {
		t.Errorf("replaced caches answered %+v", c)
	}
	if !replaced.failed {
		t.Error("replaced caches did not invalidate the chain reader")
	}
}

func podRef(name string) resourceid.Ref {
	return resourceid.Ref{Kind: "Pod", Namespace: "shop", Name: name}
}

type countingSource struct {
	ownership.Source
	begun int
}

func (s *countingSource) Begin() (ownership.ReadSession, error) {
	s.begun++
	return s.Source.Begin()
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

func TestRelationshipReadBindsCapturedCache(t *testing.T) {
	_, typed, source, _ := historyFixture(t)
	previous := ownershipSource
	ownershipSource = source.(*cachesource.Source)
	t.Cleanup(func() { ownershipSource = previous })
	captured := &ResourceCache{ResourceCache: typed}
	session, err := BeginOwnershipRead(captured)
	if err != nil {
		t.Fatal(err)
	}
	caches, ok := cachesource.SessionCaches(session)
	if !ok || caches.Typed != typed {
		t.Fatalf("session caches = %+v, %v", caches, ok)
	}
	for _, wrong := range []*ResourceCache{nil, {ResourceCache: &k8score.ResourceCache{}}} {
		if _, err := BeginOwnershipRead(wrong); !errors.Is(err, ownership.ErrCachesReplaced) {
			t.Fatalf("wrong cache: %v", err)
		}
		answer, _, _ := AnswerRelationships(wrong, nil, func(*ownership.Catalog) ownership.Access { t.Fatal("access after cache mismatch"); return nil }, nil)
		if answer != nil {
			t.Fatalf("answer after cache mismatch: %+v", answer)
		}
	}
}

func TestRelationshipCaptureIncludesManagerStatus(t *testing.T) {
	_, _, source, _ := historyFixture(t)
	session, err := source.Begin()
	if err != nil {
		t.Fatal(err)
	}
	manager := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"namespace": "gitops", "name": "web", "uid": "application"},
		"status": map[string]any{
			"resources": []any{map[string]any{"group": "apps", "kind": "Deployment", "namespace": "shop", "name": "web"}},
			"sync":      map[string]any{"comparedTo": map[string]any{"destination": map[string]any{"server": "https://kubernetes.default.svc", "namespace": "shop"}}},
		},
	}}
	ref := resourceid.NewRef("argoproj.io", "Application", "gitops", "web")
	captured, ok := relations.CaptureSubject(session.(relations.Session), ref, manager)
	if !ok {
		t.Fatal("capture failed")
	}
	members := captured.Members(ref)
	if members.Manager.State != ownership.Present || members.Membership != ownership.MembersRecorded || len(members.Members) != 1 || members.Members[0].Name != "web" {
		t.Fatalf("captured status not used: %+v", members)
	}
	// The cache can't read the Application, so the capture must not hide that.
	if read := captured.Object(ref); read.State == ownership.Present {
		t.Fatalf("a subject the cache can't read reads as present: %+v", read)
	}
}

func TestRelationshipSubjectReplacementAndFinalValidation(t *testing.T) {
	_, typed, source, deployment := historyFixture(t)
	session, err := source.Begin()
	if err != nil {
		t.Fatal(err)
	}
	current, _ := cachesource.SessionCaches(session)
	previous := ownershipSource
	ownershipSource = cachesource.New(func() cachesource.Caches { return current })
	t.Cleanup(func() { ownershipSource = previous })
	captured := &ResourceCache{ResourceCache: typed}
	obj := deployment.DeepCopy()
	obj.APIVersion = "apps/v1"
	obj.Kind = "Deployment"
	obj.UID = "new-incarnation"
	access := func(*ownership.Catalog) ownership.Access { return ownership.Unrestricted() }
	rel, coverage, _ := AnswerRelationships(captured, obj, access, access)
	if rel == nil || coverage["subject"].SubjectState != "replaced" || rel.Owner != nil || len(rel.Children)+len(rel.Managers)+len(rel.ConfigRefs) > 0 {
		t.Fatalf("replaced subject: %+v, %+v", rel, coverage)
	}
	obj.UID = deployment.UID
	obj.ResourceVersion = "new-version"
	rel, coverage, _ = AnswerRelationships(captured, obj, access, access)
	if rel == nil || coverage["subject"].SubjectState != "" {
		t.Fatalf("same UID refused: %+v, %+v", rel, coverage)
	}
	switched := func(*ownership.Catalog) ownership.Access {
		current.Typed = &k8score.ResourceCache{}
		return ownership.Unrestricted()
	}
	rel, coverage, _ = AnswerRelationships(captured, obj, switched, access)
	if rel != nil || !slices.Contains(coverage["answer"].NotChecked, ownership.Gap{Reason: readreason.ConnectionChanged}) {
		t.Fatalf("published after cache replacement: %+v, %+v", rel, coverage)
	}
}

type historyPodAccess struct {
	ownership.Access
	decision ownership.Decision
}

func (a historyPodAccess) CanRead(gk resourceid.GroupKind, namespace string) ownership.Decision {
	if gk == (resourceid.GroupKind{Kind: "Pod"}) && namespace == "shop" {
		return a.decision
	}
	return a.Access.CanRead(gk, namespace)
}

func TestRelationshipRevisionSplitRequiresReadablePods(t *testing.T) {
	for _, decision := range []ownership.Decision{ownership.Denied, ownership.Undecided, ownership.Allowed} {
		for _, withPod := range []bool{false, true} {
			t.Run(fmt.Sprintf("%v/pod=%v", decision, withPod), func(t *testing.T) {
				objects := []runtime.Object{historyReplicaSet("web-1", "1", 0, 0)}
				if withPod {
					objects = append(objects, historyPod("web-1-a", "web-1"))
				}
				_, typed, source, deployment := historyFixture(t, objects...)
				previous := ownershipSource
				ownershipSource = source.(*cachesource.Source)
				t.Cleanup(func() { ownershipSource = previous })
				obj := deployment.DeepCopy()
				obj.APIVersion, obj.Kind = "apps/v1", "Deployment"
				access := func(*ownership.Catalog) ownership.Access {
					return historyPodAccess{Access: ownership.Unrestricted(), decision: decision}
				}
				traverse := func(*ownership.Catalog) ownership.Access { return ownership.Unrestricted() }
				rel, _, _ := AnswerRelationships(&ResourceCache{ResourceCache: typed}, obj, access, traverse)
				if rel == nil {
					t.Fatal("answer unavailable")
				}
				older := decision == ownership.Allowed && !withPod
				if older {
					if len(rel.Children) != 0 || !slices.Equal(rel.OlderRevisions, []topology.ResourceRef{historyChild("web-1")}) {
						t.Fatalf("readable empty Pods: children %v, older %v", rel.Children, rel.OlderRevisions)
					}
				} else if !slices.Equal(rel.Children, []topology.ResourceRef{historyChild("web-1")}) || len(rel.OlderRevisions) != 0 {
					t.Fatalf("children %v, older %v; want unsplit ReplicaSet", rel.Children, rel.OlderRevisions)
				}
			})
		}
	}
}

// flippingAccess allows the subject's kind on its first check only, and
// decides later checks as then, as a permission cache entry expiring between
// two checks would.
type flippingAccess struct {
	ownership.Access
	kind   resourceid.GroupKind
	checks *int
	then   ownership.Decision
}

func (a flippingAccess) CanRead(gk resourceid.GroupKind, namespace string) ownership.Decision {
	if gk != a.kind {
		return a.Access.CanRead(gk, namespace)
	}
	*a.checks++
	if *a.checks == 1 {
		return ownership.Allowed
	}
	return a.then
}

// A subject the answer's own check refuses is answered with no relationships,
// and its ownership says why, as that check decided.
func TestRelationshipAnswerSurvivesAccessChangingMidAnswer(t *testing.T) {
	for _, tc := range []struct {
		contextOnly bool
		then        ownership.Decision
		reason      readreason.Code
	}{
		{false, ownership.Denied, readreason.UserForbidden},
		{true, ownership.Denied, readreason.UserForbidden},
		{false, ownership.Undecided, readreason.UserPermissionCheckFailed},
		{true, ownership.Undecided, readreason.UserPermissionCheckFailed},
	} {
		contextOnly := tc.contextOnly
		t.Run(fmt.Sprintf("context=%v/%v", contextOnly, tc.then), func(t *testing.T) {
			_, typed, source, deployment := historyFixture(t)
			previous := ownershipSource
			ownershipSource = source.(*cachesource.Source)
			t.Cleanup(func() { ownershipSource = previous })
			obj := deployment.DeepCopy()
			obj.APIVersion, obj.Kind = "apps/v1", "Deployment"
			checks := 0
			access := func(*ownership.Catalog) ownership.Access {
				return flippingAccess{Access: ownership.Unrestricted(), kind: resourceid.GroupKind{Group: "apps", Kind: "Deployment"}, checks: &checks, then: tc.then}
			}
			traverse := func(*ownership.Catalog) ownership.Access { return ownership.Unrestricted() }
			answer := AnswerRelationships
			if contextOnly {
				answer = AnswerResourceContextRelationships
			}
			rel, coverage, extras := answer(&ResourceCache{ResourceCache: typed}, obj, access, traverse)
			if checks < 2 {
				t.Fatalf("subject checked %d times; the test needs the answer's own check", checks)
			}
			if rel == nil || len(rel.Children)+len(rel.OlderRevisions) > 0 || rel.Owner != nil || extras == nil {
				t.Fatalf("refused subject answered: %+v %+v %+v", rel, coverage, extras)
			}
			if rel.Ownership == nil || rel.Ownership.UnavailableReason != string(tc.reason) {
				t.Fatalf("refused subject's ownership = %+v, want %s", rel.Ownership, tc.reason)
			}
			refused := extras.Fields["answer"]
			if tc.then == ownership.Denied && !refused.Narrowed || tc.then == ownership.Undecided && (len(refused.NotChecked) != 1 || refused.NotChecked[0].Reason != tc.reason) || !reflect.DeepEqual(coverage, extras.Coverage) {
				t.Fatalf("refused subject's fields = %+v, coverage %+v", extras.Fields, coverage)
			}
		})
	}
}

// A fetched Deployment the cache doesn't hold yet still answers what reads its
// ownership: its ReplicaSets' Pods, as well as its children.
func TestRelationshipAnswerSeedsAFetchedSubjectTheCacheLacks(t *testing.T) {
	rs := historyReplicaSet("fresh-1", "1", 1, 1)
	rs.OwnerReferences[0].Name, rs.OwnerReferences[0].UID = "fresh", "uid-fresh"
	_, typed, source, _ := historyFixture(t, rs, historyPod("fresh-1-a", "fresh-1"))
	previous := ownershipSource
	ownershipSource = source.(*cachesource.Source)
	t.Cleanup(func() { ownershipSource = previous })
	obj := &appsv1.Deployment{TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"}, ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "fresh", UID: "uid-fresh", Annotations: map[string]string{historyRevision: "1"}}}
	access := func(*ownership.Catalog) ownership.Access { return ownership.Unrestricted() }
	rel, _, _ := AnswerRelationships(&ResourceCache{ResourceCache: typed}, obj, access, access)
	if rel == nil {
		t.Fatal("answer unavailable")
	}
	if !slices.ContainsFunc(rel.Children, func(r topology.ResourceRef) bool { return r.Name == "fresh-1" }) {
		t.Fatalf("children: %+v", rel.Children)
	}
	if len(rel.Pods) != 1 || rel.Pods[0].Name != "fresh-1-a" {
		t.Fatalf("pods: %+v", rel.Pods)
	}
}
