package resourcecontext

import (
	"context"
	"fmt"
	"reflect"
	"slices"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// fixtureContext builds obj's context as a host does: the relationship
// extras come from the relation answer over a session holding obj and the
// provider's objects. A graph topo, or opts.Relationships, supplies the
// relationship lists of the tests that exercise Build's mapping of them.
func fixtureContext(ctx context.Context, obj runtime.Object, topo *topology.Topology, opts Options) *ResourceContext {
	if obj == nil {
		return Build(ctx, obj, opts)
	}
	ident, ok := identityOf(obj)
	if !ok {
		return Build(ctx, obj, opts)
	}
	obj = obj.DeepCopyObject()
	obj.GetObjectKind().SetGroupVersionKind(schema.GroupVersionKind{Group: ident.Group, Version: "v1", Kind: ident.Kind})
	subject := resourceid.NewRef(ident.Group, ident.Kind, ident.Namespace, ident.Name)
	session := newContextFixtureSession(subject, obj, opts.Provider)
	access := fixtureAccess{ctx: ctx, checker: opts.AccessChecker}
	_, opts.RelationshipExtras = topology.RelationsContextAnswer(session, access, access, ownership.NewView(session, access), subject, obj, opts.Provider, opts.DynamicProv)
	if topo != nil && opts.Relationships == nil {
		opts.Relationships = topology.GetRelationshipsWithObject(topology.KindForGVK(ident.Kind, ident.Group), ident.Namespace, ident.Name, obj, topo, opts.Provider, opts.DynamicProv, nil)
		if opts.Relationships == nil {
			opts.Relationships = &topology.Relationships{}
		}
	}
	if pod, ok := obj.(*corev1.Pod); ok && pod.Spec.NodeName != "" {
		if opts.Relationships == nil {
			opts.Relationships = &topology.Relationships{}
		}
		opts.Relationships.Node = &topology.ResourceRef{Kind: "Node", Name: pod.Spec.NodeName}
	}
	return Build(ctx, obj, opts)
}

type fixtureAccess struct {
	ctx     context.Context
	checker RefAccessChecker
}

func (a fixtureAccess) CanRead(gk resourceid.GroupKind, ns string) ownership.Decision {
	if a.checker != nil && !a.checker.CanRead(a.ctx, gk.Group, gk.Kind, ns) {
		return ownership.Denied
	}
	return ownership.Allowed
}
func (fixtureAccess) Namespaces() ([]string, bool) { return nil, true }

// contextFixtureSession holds the subject, the provider's objects and any
// further objects, and reads gaps for the kinds it is given; it holds no
// ownership managers or dependents.
type contextFixtureSession struct {
	ownership.ReadSession
	subject  resourceid.Ref
	obj      runtime.Object
	provider topology.ResourceProvider
	objects  []runtime.Object
	gaps     map[resourceid.GroupKind]ownership.Gap
}

func newContextFixtureSession(subject resourceid.Ref, obj runtime.Object, provider topology.ResourceProvider) *contextFixtureSession {
	empty, err := ownershiptest.Scan(ownershiptest.World{}).Begin()
	if err != nil {
		panic(err)
	}
	return &contextFixtureSession{ReadSession: empty, subject: subject, obj: obj, provider: provider}
}

func (*contextFixtureSession) Catalog() *ownership.Catalog { return ownership.NewCatalog(nil, true) }
func (s *contextFixtureSession) Borrow(r resourceid.Ref) (runtime.Object, ownership.ObjectRead) {
	obj := s.obj
	if r != s.subject {
		obj = nil
		read := s.Indexed(r.GroupKind(), "", nil, ownership.SearchScope{Namespaces: []string{r.Namespace}, IncludeClusterScoped: true})
		for _, o := range read.Objects {
			if o.Ref == r {
				obj = o.Object
			}
		}
	}
	if obj == nil {
		return nil, ownership.ObjectRead{State: ownership.Absent}
	}
	m, _ := apimeta.Accessor(obj)
	return obj, ownership.ObjectRead{State: ownership.Present, Object: ownership.Observe(r, m)}
}
func (s *contextFixtureSession) Object(r resourceid.Ref) ownership.ObjectRead {
	_, read := s.Borrow(r)
	return read
}
func (s *contextFixtureSession) assess(gk resourceid.GroupKind, namespaces []string) []ownership.Gap {
	coverage, ok := s.provider.(interface {
		AssessKindRead(string, []string) k8score.KindReadAssessment
	})
	if !ok {
		return nil
	}
	builtin, ok := resourceid.BuiltinForKind(gk.Kind)
	if !ok || builtin.GroupKind() != gk {
		return nil
	}
	a := coverage.AssessKindRead(builtin.Resource, namespaces)
	if a.Complete {
		return nil
	}
	reason := readreason.ReadError
	if a.TargetReadiness() == k8score.KindPending {
		reason = readreason.KindSyncPending
	}
	return []ownership.Gap{{Kind: gk, Reason: reason}}
}
func (s *contextFixtureSession) Indexed(gk resourceid.GroupKind, _ string, _ []string, scope ownership.SearchScope) relations.IndexedRead {
	out := relations.IndexedRead{Gaps: s.assess(gk, scope.Namespaces)}
	if gap, ok := s.gaps[gk]; ok {
		out.Gaps = append(out.Gaps, gap)
		return out
	}
	var objects []runtime.Object
	for _, o := range s.objects {
		if o.GetObjectKind().GroupVersionKind().GroupKind() == (schema.GroupKind{Group: gk.Group, Kind: gk.Kind}) {
			objects = append(objects, o)
		}
	}
	if s.provider == nil {
		return s.filed(out, gk, objects, scope)
	}
	add := func(o runtime.Object) {
		if o != nil && !reflect.ValueOf(o).IsNil() {
			objects = append(objects, o)
		}
	}
	var err error
	switch gk.Kind {
	case "Pod":
		list, e := s.provider.Pods()
		err = e
		for _, o := range list {
			add(o)
		}
	case "Deployment":
		list, e := s.provider.Deployments()
		err = e
		for _, o := range list {
			add(o)
		}
	case "StatefulSet":
		list, e := s.provider.StatefulSets()
		err = e
		for _, o := range list {
			add(o)
		}
	case "DaemonSet":
		list, e := s.provider.DaemonSets()
		err = e
		for _, o := range list {
			add(o)
		}
	case "ReplicaSet":
		list, e := s.provider.ReplicaSets()
		err = e
		for _, o := range list {
			add(o)
		}
	case "Job":
		list, e := s.provider.Jobs()
		err = e
		for _, o := range list {
			add(o)
		}
	case "CronJob":
		list, e := s.provider.CronJobs()
		err = e
		for _, o := range list {
			add(o)
		}
	case "Ingress":
		list, e := s.provider.Ingresses()
		err = e
		for _, o := range list {
			add(o)
		}
	case "HorizontalPodAutoscaler":
		list, e := s.provider.HorizontalPodAutoscalers()
		err = e
		for _, o := range list {
			add(o)
		}
	case "ServiceAccount":
		if p, ok := s.provider.(topology.ServiceAccountProvider); ok {
			list, e := p.ServiceAccounts()
			err = e
			for _, o := range list {
				add(o)
			}
		}
	}
	if err != nil {
		out.Gaps = append(out.Gaps, ownership.Gap{Kind: gk, Reason: readreason.ReadError})
		return out
	}
	return s.filed(out, gk, objects, scope)
}

// filed adds the objects of gk in scope to out, as the cache files them.
func (s *contextFixtureSession) filed(out relations.IndexedRead, gk resourceid.GroupKind, objects []runtime.Object, scope ownership.SearchScope) relations.IndexedRead {
	for _, o := range objects {
		m, e := apimeta.Accessor(o)
		if e != nil {
			panic(fmt.Sprint(e))
		}
		if !scope.AllNamespaces && m.GetNamespace() != "" && !slices.Contains(scope.Namespaces, m.GetNamespace()) {
			continue
		}
		r := resourceid.NewRef(gk.Group, gk.Kind, m.GetNamespace(), m.GetName())
		o = o.DeepCopyObject()
		o.GetObjectKind().SetGroupVersionKind(schema.GroupVersionKind{Group: gk.Group, Version: "v1", Kind: gk.Kind})
		out.Objects = append(out.Objects, relations.IndexedObject{Ref: r, Object: o})
	}
	return out
}
