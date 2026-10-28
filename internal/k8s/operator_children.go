package k8s

import (
	"sort"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// OperatorChildren separates a bounded general child preview from runtime
// workload selection. Unwatched unrelated CRDs do not make apps workloads
// unknown, and hundreds of ConfigMaps cannot displace a runtime bundle.
type OperatorChildren struct {
	Context   *resourcecontext.ChildrenContext
	Workloads []resourcecontext.ContextRef // first four: diagnose shows three
	// WorkloadsComplete means all three apps workload inventories were readable
	// and checked. Workloads itself remains a bounded sample.
	WorkloadsComplete bool
	// Unavailable is a readreason code for a discarded observation; consumers
	// project it as the generic omitted reason unavailable. Empty means usable.
	Unavailable string
}

// OperatorChildrenForObject returns nil for unsupported kinds, or a nonnil
// unavailable observation when current ownership could not be established.
// access must be nonnil and authorize reads within the session catalog.
func OperatorChildrenForObject(obj runtime.Object, access func(*ownership.Catalog) ownership.Access) *OperatorChildren {
	if obj == nil {
		return nil
	}
	gk := obj.GetObjectKind().GroupVersionKind().GroupKind()
	if !(gk.Group == "opentelemetry.io" && gk.Kind == "OpenTelemetryCollector" || gk.Group == "flagger.app" && gk.Kind == "Canary") {
		return nil
	}
	fetched, ok := OwnershipObservation(obj)
	if !ok || fetched.UID == "" {
		return &OperatorChildren{Unavailable: string(readreason.ReadError)}
	}
	return operatorChildrenFromSource(ownershipSource, fetched, access)
}

func operatorChildrenFromSource(source ownership.Source, fetched ownership.Observation, access func(*ownership.Catalog) ownership.Access) *OperatorChildren {
	out := &OperatorChildren{WorkloadsComplete: true}
	session, err := source.Begin()
	if err != nil {
		out.Unavailable = string(readreason.ClusterConnecting)
		return out
	}
	callerAccess := access(session.Catalog())
	v := ownership.NewView(session, callerAccess)
	v.Seed(fetched)
	a := v.Children(fetched.Ref)
	if !a.Subject.Present() || a.Subject.Read.Object.UID != fetched.UID {
		out.Unavailable = string(readreason.ReadError)
		return out
	}
	for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet"} {
		if callerAccess.CanRead(resourceid.GroupKind{Group: "apps", Kind: kind}, fetched.Ref.Namespace) != ownership.Allowed {
			out.WorkloadsComplete = false
		}
		served, known := session.Catalog().Kind(resourceid.GroupKind{Group: "apps", Kind: kind})
		if !known || !served.HasGCVerbs {
			out.WorkloadsComplete = false
		}
	}
	for _, gap := range a.Gaps {
		if gap.Kind.Kind == "" || runtimeWorkload(gap.Kind) {
			out.WorkloadsComplete = false
		}
	}
	children := a.Children // View owns this slice; objects' metadata stays borrowed.
	sort.Slice(children, func(i, j int) bool {
		a, b := children[i].Ref, children[j].Ref
		aw, bw := runtimeWorkload(a.GroupKind()), runtimeWorkload(b.GroupKind())
		if aw != bw {
			return aw
		}
		return a.Group+"/"+a.Kind+"/"+a.Namespace+"/"+a.Name < b.Group+"/"+b.Kind+"/"+b.Namespace+"/"+b.Name
	})
	out.Context = &resourcecontext.ChildrenContext{Items: []resourcecontext.ContextRef{}, Complete: a.Complete(), Narrowed: a.Narrowed}
	for _, child := range children {
		r := child.Ref
		ref := resourcecontext.ContextRef{Group: r.Group, Kind: r.Kind, Namespace: r.Namespace, Name: r.Name}
		controlled := false
		for _, owner := range child.OwnerReferences {
			if owner.Controller != nil && *owner.Controller && owner.UID == fetched.UID && owner.Name == fetched.Ref.Name && owner.Kind == fetched.Ref.Kind && schema.FromAPIVersionAndKind(owner.APIVersion, owner.Kind).Group == fetched.Ref.Group {
				controlled = true
				break
			}
		}
		if controlled && runtimeWorkload(r.GroupKind()) && len(out.Workloads) < 4 {
			out.Workloads = append(out.Workloads, ref)
		}
		if len(out.Context.Items) < 25 {
			out.Context.Items = append(out.Context.Items, ref)
		} else {
			out.Context.Truncated = true
			out.Context.Complete = false
		}
	}
	if session.ValidateCaches() != nil {
		return &OperatorChildren{Unavailable: string(readreason.ConnectionChanged)}
	}
	return out
}

func runtimeWorkload(kind resourceid.GroupKind) bool {
	return kind.Group == "apps" && (kind.Kind == "Deployment" || kind.Kind == "StatefulSet" || kind.Kind == "DaemonSet")
}
