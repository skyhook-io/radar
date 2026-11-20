package k8s

import (
	appsv1 "k8s.io/api/apps/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/cachesource"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
)

// ownershipSource reads ownership from the caches current when a session
// begins. A session pins them, so a context switch that replaces them fails
// its ValidateCaches instead of mixing two clusters' answers.
var ownershipSource = cachesource.New(func() cachesource.Caches {
	var c cachesource.Caches
	if rc := GetResourceCache(); rc != nil {
		c.Typed = rc.ResourceCache
	}
	if dc := GetDynamicResourceCache(); dc != nil {
		c.Dynamic = dc.DynamicResourceCache
	}
	if rd := GetResourceDiscovery(); rd != nil {
		c.Discovery = rd.ResourceDiscovery
	}
	return c
})

// BeginOwnershipRead binds a request's captured cache to an ownership session.
// The caller must validate the session before publishing its derived answer.
func BeginOwnershipRead(cache *ResourceCache) (ownership.ReadSession, error) {
	session, err := ownershipSource.Begin()
	if err != nil {
		return nil, err
	}
	typed, ok := cachesource.TypedCache(session)
	if cache == nil || !ok || typed != cache.ResourceCache {
		return nil, ownership.ErrCachesReplaced
	}
	return session, nil
}

// AnswerOwnership answers rel's ownership for fetched, the object the caller
// fetched, from the caches, for the caller access describes once the
// session's catalog is known. owner is the fetched object itself: when it is a
// Deployment or an Argo Rollout, the ReplicaSets that only keep its rollback
// history move to OlderRevisions, read from the answer's own caches so that
// a context switch refuses them with it. Caches replaced or not ready leave
// the answer unavailable.
func AnswerOwnership(rel *topology.Relationships, fetched ownership.Observation, owner metav1.Object, access func(*ownership.Catalog) ownership.Access) {
	answerOwnership(ownershipSource, topology.ApplyOwnership, splitHistory(fetched.Ref, owner, nil), rel, fetched, access)
}

// splitHistory moves the ReplicaSets among rel's Children that only keep
// subject's rollback history to OlderRevisions, reading them and the Pods
// still naming them through the typed cache the session reads.
func splitHistory(subject resourceid.Ref, owner metav1.Object, access ownership.Access) func(*topology.Relationships, *ownership.View, ownership.ReadSession) {
	return func(rel *topology.Relationships, _ *ownership.View, s ownership.ReadSession) {
		typed, ok := cachesource.TypedCache(s)
		if !ok || owner == nil {
			return
		}
		topology.SplitOlderRevisions(rel, subject.GroupKind(), owner.GetAnnotations(), func(namespace, name string) (*appsv1.ReplicaSet, bool, bool) {
			if access != nil && access.CanRead(resourceid.GroupKind{Kind: "Pod"}, namespace) != ownership.Allowed {
				return nil, false, false
			}
			obj, found, ok := typed.ReadableObject(k8score.ReplicaSets, namespace, name)
			rs, isReplicaSet := obj.(*appsv1.ReplicaSet)
			if !ok || !found || !isReplicaSet {
				return nil, false, false
			}
			pods, ok := typed.ReadableDependents(k8score.Pods, []string{namespace}, string(rs.UID))
			if !ok {
				return nil, false, false
			}
			return rs, len(pods) > 0, true
		})
	}
}

// AnswerOwner is AnswerOwnership for rel's Owner and Manager, which is all a
// resource context reads. It searches for no children and builds no member
// list: the manager answer reads the recorder index along the controller
// chain and, for a claim its lists don't confirm, that manager's list state.
func AnswerOwner(rel *topology.Relationships, fetched ownership.Observation, access func(*ownership.Catalog) ownership.Access) {
	answerOwnership(ownershipSource, topology.ApplyOwner, nil, rel, fetched, access)
}

// AnswerLiveManager reads the live target's metadata while ancestors and
// recorded membership retain their cache provenance. The view still checks
// access and refuses an object replaced since the live read; unavailable is
// then the reason code, and the answer nil.
func AnswerLiveManager(fetched ownership.Observation, access func(*ownership.Catalog) ownership.Access) (answer *topology.ManagerAnswer, unavailable string) {
	rel := &topology.Relationships{}
	answerOwnership(liveOwnershipSource{Source: ownershipSource, fetched: fetched}, topology.ApplyOwner, nil, rel, fetched, access)
	switch {
	case rel.Ownership == nil:
		return nil, "unavailable"
	case rel.Ownership.UnavailableReason != "":
		return nil, rel.Ownership.UnavailableReason
	}
	return rel.Ownership.Manager, ""
}

type liveOwnershipSource struct {
	ownership.Source
	fetched ownership.Observation
}

func (s liveOwnershipSource) Begin() (ownership.ReadSession, error) {
	session, err := s.Source.Begin()
	if err != nil {
		return nil, err
	}
	return liveOwnershipSession{ReadSession: session, fetched: s.fetched}, nil
}

type liveOwnershipSession struct {
	ownership.ReadSession
	fetched ownership.Observation
}

func (s liveOwnershipSession) Object(ref resourceid.Ref) ownership.ObjectRead {
	read := s.ReadSession.Object(ref)
	if ref == s.fetched.Ref && (read.State == ownership.Present && read.Object.UID == s.fetched.UID || read.State == ownership.Absent || read.State == ownership.Unavailable) {
		return ownership.ObjectRead{State: ownership.Present, Object: s.fetched}
	}
	return read
}

// answerOwnership applies the view's answer to rel, then present (when set)
// with the view and its session, before validating: a refused answer takes
// what present read with it.
func answerOwnership(source ownership.Source, apply func(*topology.Relationships, *ownership.View, ownership.Observation), present func(*topology.Relationships, *ownership.View, ownership.ReadSession), rel *topology.Relationships, fetched ownership.Observation, access func(*ownership.Catalog) ownership.Access) {
	session, err := source.Begin()
	if err != nil {
		topology.OwnershipUnavailable(rel, string(readreason.ClusterConnecting))
		return
	}
	if len(session.Catalog().DependentKinds()) == 0 {
		// Discovery hasn't answered yet, as while Radar starts, so no kind
		// could be searched or resolved.
		topology.OwnershipUnavailable(rel, string(readreason.ClusterConnecting))
		return
	}
	v := ownership.NewView(session, access(session.Catalog()))
	apply(rel, v, fetched)
	if present != nil {
		present(rel, v, session)
	}
	if session.ValidateCaches() != nil {
		// A cluster switch mid-answer: it may mix two clusters.
		topology.OwnershipUnavailable(rel, string(readreason.ConnectionChanged))
	}
}

// AnswerRelationships is a fetched object's Related Resources answered from
// the caches in one session: the relation families projected as the
// relationship graph's edges are (topology.RelationsAnswer), then ownership,
// with one ownership view whose searches both share. When the caches aren't
// ready or are replaced mid-answer, the relationships are nil and the extras
// say why (topology.UnansweredRelationships).
func AnswerRelationships(cache *ResourceCache, obj runtime.Object, access, traverse func(*ownership.Catalog) ownership.Access) (*topology.Relationships, map[string]relations.Coverage, *topology.RelationshipExtras) {
	return answerRelationships(cache, obj, access, traverse, false)
}

// AnswerResourceContextRelationships shares the relation session and access
// policy with drawer answers, but only asks ownership for owner/manager facts.
// obj must be what the host read for the caller through its resource read
// gate. The relation families read obj's kind in its namespace as that gate
// did, so a caller who reads obj by namespace access, without a get grant on
// its kind, still gets its relationships; ownership reads obj as access does.
func AnswerResourceContextRelationships(cache *ResourceCache, obj runtime.Object, access, traverse func(*ownership.Catalog) ownership.Access) (*topology.Relationships, map[string]relations.Coverage, *topology.RelationshipExtras) {
	return answerRelationships(cache, obj, access, traverse, true)
}

func answerRelationships(cache *ResourceCache, obj runtime.Object, access, traverse func(*ownership.Catalog) ownership.Access, contextOnly bool) (*topology.Relationships, map[string]relations.Coverage, *topology.RelationshipExtras) {
	unanswered := func(reason readreason.Code) (*topology.Relationships, map[string]relations.Coverage, *topology.RelationshipExtras) {
		extras := topology.UnansweredRelationships(reason)
		return nil, extras.Coverage, extras
	}
	session, err := BeginOwnershipRead(cache)
	if err != nil || len(session.Catalog().DependentKinds()) == 0 {
		// Not connected, or discovery hasn't answered yet, as while Radar
		// starts.
		return unanswered(readreason.ClusterConnecting)
	}
	rs, ok := session.(relations.Session)
	if !ok {
		return unanswered(readreason.ReadError)
	}
	caches, ok := cachesource.SessionCaches(session)
	if !ok {
		return unanswered(readreason.ReadError)
	}
	provider := NewTopologyResourceProvider(&ResourceCache{ResourceCache: caches.Typed})
	var dp topology.DynamicProvider
	if caches.Dynamic != nil && caches.Discovery != nil {
		dp = NewTopologyDynamicProvider(&DynamicResourceCache{DynamicResourceCache: caches.Dynamic}, &ResourceDiscovery{ResourceDiscovery: caches.Discovery})
	}
	fetched, valid := OwnershipObservation(obj)
	if !valid {
		return unanswered(readreason.ReadError)
	}
	owner, _ := apimeta.Accessor(obj)
	acc := access(session.Catalog())
	families := acc
	if contextOnly {
		families = topology.ReadSubject(acc, fetched.Ref)
	}
	if decision := families.CanRead(fetched.Ref.GroupKind(), fetched.Ref.Namespace); decision != ownership.Allowed {
		if session.ValidateCaches() != nil {
			return unanswered(readreason.ConnectionChanged)
		}
		rel, extras := topology.RefusedRelationships(fetched.Ref, decision)
		return rel, extras.Coverage, extras
	}
	captured, current := relations.CaptureSubject(rs, fetched.Ref, obj)
	if !current {
		if session.ValidateCaches() != nil {
			return unanswered(readreason.ConnectionChanged)
		}
		rel := &topology.Relationships{}
		topology.OwnershipUnavailable(rel, string(readreason.ReadError))
		return rel, map[string]relations.Coverage{"subject": {SubjectState: "replaced"}}, &topology.RelationshipExtras{Fields: map[string]relations.Coverage{"subject": {SubjectState: "replaced"}}}
	}
	rs = captured
	ov := ownership.NewView(rs, acc)
	// Seeded before the answer, whose Deployment Pods and ManagedBy read
	// ownership, so a subject the cache doesn't hold yet still has them.
	ov.Seed(fetched)
	answer := topology.RelationsAnswerWithExtras
	if contextOnly {
		answer = topology.RelationsContextAnswer
	}
	rel, extras := answer(rs, families, traverse(session.Catalog()), ov, fetched.Ref, obj, provider, dp)
	if rel.Ownership != nil {
		// The answer checks the caller's access to obj again; a decision
		// that changed since the check above refuses it there, and says why.
		if session.ValidateCaches() != nil {
			return unanswered(readreason.ConnectionChanged)
		}
		return rel, extras.Coverage, extras
	}
	if contextOnly {
		topology.ApplyOwner(rel, ov, fetched)
		generators, incomplete := topology.RelationSpecGenerators(rs, acc, ov, fetched.Ref, obj)
		rel.Managers, rel.OtherManagersIncomplete = generators, incomplete
	} else {
		topology.ApplyOwnership(rel, ov, fetched)
		splitHistory(fetched.Ref, owner, acc)(rel, ov, session)
	}
	if session.ValidateCaches() != nil {
		return unanswered(readreason.ConnectionChanged)
	}
	return rel, extras.Coverage, extras
}

// OwnershipObservation is what ownership reads from a fetched object, whose
// type meta must be set.
func OwnershipObservation(obj any) (ownership.Observation, bool) {
	o, ok := obj.(runtime.Object)
	if !ok {
		return ownership.Observation{}, false
	}
	m, err := apimeta.Accessor(o)
	gvk := o.GetObjectKind().GroupVersionKind()
	if err != nil || gvk.Kind == "" {
		return ownership.Observation{}, false
	}
	ref := resourceid.Ref{Group: gvk.Group, Kind: gvk.Kind, Namespace: m.GetNamespace(), Name: m.GetName()}
	return ownership.Observe(ref, m), true
}

// AgentOwnership reads controllers and management for one caller in one session,
// so the rows of one list share their owners' reads. Not safe for concurrent
// use.
type AgentOwnership struct {
	source ownership.Source
	access func(*ownership.Catalog) ownership.Access
	view   *ownership.View
	acc    ownership.Access
	// session is nil until the first read; failed once it couldn't begin or
	// its caches were replaced, after which nothing is answered.
	session ownership.ReadSession
	failed  bool
	active  resourceid.Ref
}

// NewAgentOwnership reads for the caller access describes once the
// session's catalog is known.
func NewAgentOwnership(access func(*ownership.Catalog) ownership.Access) *AgentOwnership {
	return &AgentOwnership{source: ownershipSource, access: access}
}

// Of is the controller chain of obj, read from the caches as ref, as the
// caller reads it; nil when it can't be answered. obj is never modified: the
// caches share it. A successful read activates ref for OtherManagers and Manager
// until the next Of call.
func (c *AgentOwnership) Of(obj metav1.Object, ref resourceid.Ref) *topology.ControllerChain {
	c.active = resourceid.Ref{}
	if obj == nil || ref.Kind == "" || c.failed {
		return nil
	}
	fetched := ownership.Observe(ref, obj)
	if c.session == nil {
		session, err := c.source.Begin()
		if err != nil || len(session.Catalog().DependentKinds()) == 0 {
			c.failed = true
			return nil
		}
		c.session, c.acc = session, c.access(session.Catalog())
		c.view = ownership.NewView(session, c.acc)
	}
	c.view.Seed(fetched)
	chain := c.view.ControllerChain(fetched.Ref)
	if c.session.ValidateCaches() != nil {
		c.failed = true
		return nil
	}
	if chain.Subject.Access == ownership.Denied {
		return &topology.ControllerChain{Stop: "withheld"}
	}
	if chain.Subject.Read.State == ownership.Excluded {
		return &topology.ControllerChain{}
	}
	if !chain.Subject.Present() || chain.Subject.Read.Object.UID != fetched.UID {
		return nil
	}
	c.active = ref
	return topology.ChainOf(chain)
}

// OtherManagers reads residual spec relations for the resource most recently
// passed to Of, in Of's session, so a page's rows share one catalog, access and
// memoized reads. The bool reports unchecked sources, never denied ones.
func (c *AgentOwnership) OtherManagers(obj runtime.Object, ref resourceid.Ref) ([]topology.ResourceRef, bool) {
	if c.view == nil || c.failed || c.active != ref {
		return nil, true
	}
	rs, ok := c.session.(relations.Session)
	if !ok {
		return nil, true
	}
	captured, ok := relations.CaptureSubject(rs, ref, obj)
	if !ok {
		return nil, true
	}
	out, incomplete := topology.RelationSpecGenerators(captured, c.acc, ownership.NewView(captured, c.acc), ref, obj)
	if c.session.ValidateCaches() != nil {
		return nil, true
	}
	return out, incomplete
}

// Manager reads GitOps/Helm evidence for the resource most recently passed to Of.
// A changed connection or inactive subject returns a zero answer (State is "",
// not ManagerNone).
func (c *AgentOwnership) Manager(ref resourceid.Ref) ownership.ManagerAnswer {
	if c.view == nil || c.failed || c.active != ref {
		return ownership.ManagerAnswer{}
	}
	out := c.view.Manager(ref)
	if c.session.ValidateCaches() != nil {
		c.failed = true
		return ownership.ManagerAnswer{}
	}
	return out
}
