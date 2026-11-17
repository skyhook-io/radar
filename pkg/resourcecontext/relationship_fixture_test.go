package resourcecontext

import (
	"context"
	"fmt"
	"reflect"
	"slices"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// Existing projection fixtures supply graph edges (topo) and provider objects.
// Adapt those inputs at the host boundary; Build itself only consumes the
// answers.
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
	session := &contextFixtureSession{subject: subject, obj: obj, provider: opts.Provider}
	access := fixtureAccess{ctx: ctx, checker: opts.AccessChecker}
	view := relations.NewView(session, access).WithLimit(10000)
	extras := &topology.RelationshipExtras{Subject: subject, Fields: map[string]relations.Coverage{}, ScalerManagers: map[resourceid.Ref]resourceid.Ref{}}
	declarations := view.References(relations.ConfigReferences, subject, obj)
	for _, d := range declarations.Declared {
		if d.State != relations.TargetWithheld {
			extras.Declarations = append(extras.Declarations, d)
		}
	}
	for _, field := range []string{"ConfigRefs", "StorageRefs", "ServiceAccount"} {
		extras.Fields[field] = declarations.Coverage
	}
	consumers := view.Referrers(relations.ConfigReferences, subject, obj)
	extras.Consumers, extras.Fields["Consumers"] = consumers.Referrers, consumers.Coverage
	if opts.Provider != nil && ident.Kind == "StorageClass" && ident.Group == "storage.k8s.io" {
		for _, kind := range []string{"PersistentVolume", "PersistentVolumeClaim"} {
			for _, gap := range session.assess(resourceid.GroupKind{Kind: kind}, nil) {
				c := extras.Fields["Dependents"]
				c.NotChecked = append(c.NotChecked, gap)
				extras.Fields["Dependents"] = c
			}
		}
	}
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
	if subject.GroupKind() == (resourceid.GroupKind{Kind: "Node"}) {
		if opts.Relationships != nil && opts.Relationships.PodPlacementObserved {
			if opts.Relationships.PodPlacementUnavailable {
				extras.Fields["Consumers"] = relations.Coverage{NotChecked: []ownership.Gap{{Reason: readreason.ReadError}}}
			}
			for _, pod := range opts.Relationships.Pods {
				extras.Consumers = append(extras.Consumers, relations.Referrer{Ref: resourceid.NewRef(pod.Group, pod.Kind, pod.Namespace, pod.Name), Paths: []string{"spec.nodeName"}})
			}
		} else if opts.Provider != nil {
			placement := view.Referrers(relations.PodPlacement, subject, obj)
			extras.Consumers, extras.Fields["Consumers"] = placement.Referrers, placement.Coverage
			if len(placement.Coverage.NotChecked) > 0 {
				extras.Fields["Consumers"] = placement.Coverage
			}
		} else {
			extras = nil
		}
	}
	if opts.Relationships != nil {
		for _, scaler := range opts.Relationships.Scalers {
			if opts.Provider == nil || scaler.Kind != "HorizontalPodAutoscaler" {
				continue
			}
			hpas, _ := opts.Provider.HorizontalPodAutoscalers()
			for _, hpa := range hpas {
				if hpa.Name != scaler.Name || hpa.Namespace != scaler.Namespace {
					continue
				}
				name := hpa.Labels["scaledobject.keda.sh/name"]
				for _, owner := range hpa.OwnerReferences {
					if owner.Kind == "ScaledObject" && resourceid.GroupFromAPIVersion(owner.APIVersion) == "keda.sh" {
						name = owner.Name
						break
					}
				}
				if name != "" {
					r := resourceid.NewRef("keda.sh", "ScaledObject", hpa.Namespace, name)
					if access.CanRead(r.GroupKind(), r.Namespace) == ownership.Allowed {
						extras.ScalerManagers[resourceid.NewRef(scaler.Group, scaler.Kind, scaler.Namespace, scaler.Name)] = r
					} else {
						extras.Fields["ScalerManagers"] = relations.Coverage{Narrowed: true}
					}
				}
			}
		}
	}
	opts.RelationshipExtras = extras
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

type contextFixtureSession struct {
	ownership.ReadSession
	subject  resourceid.Ref
	obj      runtime.Object
	provider topology.ResourceProvider
}

func (*contextFixtureSession) Catalog() *ownership.Catalog { return ownership.NewCatalog(nil, true) }
func (s *contextFixtureSession) Borrow(r resourceid.Ref) (runtime.Object, ownership.ObjectRead) {
	if r == s.subject {
		m, _ := apimeta.Accessor(s.obj)
		return s.obj, ownership.ObjectRead{State: ownership.Present, Object: ownership.Observe(r, m)}
	}
	return nil, ownership.ObjectRead{State: ownership.Absent}
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
	builtin, _ := resourceid.BuiltinForKind(gk.Kind)
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
	if s.provider == nil {
		return out
	}
	var objects []runtime.Object
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
