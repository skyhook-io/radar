package relations

import (
	"cmp"
	"slices"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/cache"

	"github.com/skyhook-io/radar/pkg/configrefs"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// ConfigFamily names the config-reference family: the ConfigMaps, Secrets,
// PersistentVolumeClaims and ServiceAccounts Pods, pod templates and Ingress
// TLS declare.
const ConfigFamily = "configReferences"

// TargetState is what a declared reference found when Radar read its target.
type TargetState uint8

const (
	// TargetPresent: the target is in Radar's cache.
	TargetPresent TargetState = iota
	// TargetAbsent: the target's store had loaded in full and doesn't hold it.
	TargetAbsent
	// TargetUnread: Radar couldn't read the target's store; Gap says why.
	TargetUnread
	// TargetWithheld: the caller may not read the target.
	TargetWithheld
)

// Declared is one object a subject declares, every path that declares it,
// and what reading it found.
type Declared struct {
	Ref   resourceid.Ref
	Paths []string
	State TargetState
	// Gap is set when State is TargetUnread.
	Gap ownership.Gap
}

// ConfigReferencesAnswer is what a subject declares. Withheld targets are
// listed, so a host can say a reference exists without naming what the
// caller may not read; Coverage.Narrowed is set when there are any.
type ConfigReferencesAnswer struct {
	Declared []Declared
	Coverage Coverage
}

// ConfigConsumer is one object that declares the subject, with every path
// declaring it.
type ConfigConsumer struct {
	Ref   resourceid.Ref
	Paths []string
}

// ConfigConsumersAnswer is what declares a subject, as the caller may read it.
type ConfigConsumersAnswer struct {
	Consumers []ConfigConsumer
	Coverage  Coverage
}

// configTargetKinds are the kinds the family declares: all core and
// namespaced, so a declaration always names its declarer's namespace.
var configTargetKinds = map[string]bool{"ConfigMap": true, "Secret": true, "PersistentVolumeClaim": true, "ServiceAccount": true}

// IsConfigTarget reports whether gk is a kind the config-reference family
// declares.
func IsConfigTarget(gk resourceid.GroupKind) bool {
	return gk.Group == "" && configTargetKinds[gk.Kind]
}

// configConsumerKinds are the kinds that declare config references, in the
// order answers list them.
var configConsumerKinds = []resourceid.GroupKind{
	{Group: "apps", Kind: "Deployment"},
	{Group: configrefs.ArgoRolloutsGroup, Kind: "Rollout"},
	{Group: "apps", Kind: "StatefulSet"},
	{Group: "apps", Kind: "DaemonSet"},
	{Group: "batch", Kind: "CronJob"},
	{Group: "batch", Kind: "Job"},
	{Group: "apps", Kind: "ReplicaSet"},
	{Group: "networking.k8s.io", Kind: "Ingress"},
	{Group: "", Kind: "Pod"},
}

func consumerOrder(gk resourceid.GroupKind) int {
	return slices.Index(configConsumerKinds, gk)
}

// scaledToZero reports a ReplicaSet scaled explicitly to zero: an old
// revision, whose declarations no running workload uses.
func scaledToZero(obj runtime.Object) bool {
	rs, ok := obj.(*appsv1.ReplicaSet)
	return ok && rs.Spec.Replicas != nil && *rs.Spec.Replicas == 0
}

// ConfigReferences answers what subject declares. fetched is the subject as
// the host read it, or nil to read it from the cache. A caller who may not
// read the subject gets nothing, Narrowed.
func (v *View) ConfigReferences(subject resourceid.Ref, fetched runtime.Object) ConfigReferencesAnswer {
	var a ConfigReferencesAnswer
	if !v.mayRead(subject.GroupKind(), subject.Namespace) {
		a.Coverage.Narrowed = true
		return a
	}
	obj := fetched
	if obj == nil {
		borrowed, read := v.session.Borrow(subject)
		switch read.State {
		case ownership.Present:
			obj = borrowed
		case ownership.Unavailable:
			a.Coverage.NotChecked = []ownership.Gap{read.Gap}
			return a
		default:
			return a
		}
	}
	if scaledToZero(obj) {
		return a
	}
	refs, ok, err := configrefs.ObjectReferences(obj)
	if !ok {
		return a
	}
	if err != nil {
		a.Coverage.NotChecked = append(a.Coverage.NotChecked, ownership.Gap{Kind: subject.GroupKind(), Namespace: subject.Namespace, Reason: readreason.ReadError})
	}
	byTarget := map[resourceid.Ref]int{}
	for _, r := range refs {
		target := resourceid.Ref{Kind: r.Kind, Namespace: r.Namespace, Name: r.Name}
		i, seen := byTarget[target]
		if !seen {
			i = len(a.Declared)
			byTarget[target] = i
			a.Declared = append(a.Declared, Declared{Ref: target})
		}
		if !slices.Contains(a.Declared[i].Paths, r.Path) {
			a.Declared[i].Paths = append(a.Declared[i].Paths, r.Path)
		}
	}
	slices.SortFunc(a.Declared, func(x, y Declared) int {
		return cmp.Or(cmp.Compare(x.Ref.Kind, y.Ref.Kind), cmp.Compare(x.Ref.Name, y.Ref.Name))
	})
	for i := range a.Declared {
		d := &a.Declared[i]
		if !v.mayRead(d.Ref.GroupKind(), d.Ref.Namespace) {
			d.State = TargetWithheld
			a.Coverage.Narrowed = true
			continue
		}
		switch read := v.session.Object(d.Ref); read.State {
		case ownership.Present:
			d.State = TargetPresent
		case ownership.Absent:
			d.State = TargetAbsent
		default:
			d.State = TargetUnread
			d.Gap = read.Gap
			if !slices.Contains(a.Coverage.NotChecked, read.Gap) {
				a.Coverage.NotChecked = append(a.Coverage.NotChecked, read.Gap)
			}
		}
	}
	return a
}

// ConfigConsumers answers what declares subject, a ConfigMap, Secret,
// PersistentVolumeClaim or ServiceAccount: the Pods, pod-template workloads
// and, for a Secret, Ingresses in its namespace. It answers only for a
// subject Radar holds: a caller who may not read it gets nothing, Narrowed,
// and a subject Radar couldn't read gets its gap. ReplicaSets scaled to zero
// are left out: they are old revisions.
//
// A declaration always names its declarer's namespace, so the answer reads
// that namespace's objects of each declaring kind and evaluates what each
// declares, rather than keeping an index of every declaration: on a Pod
// store such an index cost about 1.4 KB a Pod and made a relist about five
// times as long.
func (v *View) ConfigConsumers(subject resourceid.Ref) ConfigConsumersAnswer {
	var a ConfigConsumersAnswer
	if !IsConfigTarget(subject.GroupKind()) || subject.Namespace == "" {
		return a
	}
	if !v.mayRead(subject.GroupKind(), subject.Namespace) {
		a.Coverage.Narrowed = true
		return a
	}
	switch read := v.session.Object(subject); read.State {
	case ownership.Present:
	case ownership.Unavailable:
		a.Coverage.NotChecked = []ownership.Gap{read.Gap}
		return a
	default:
		return a
	}
	want := configrefs.Ref{Kind: subject.Kind, Namespace: subject.Namespace, Name: subject.Name}
	scope := ownership.InNamespace(subject.Namespace)
	for _, gk := range configConsumerKinds {
		if gk.Kind == "Ingress" && subject.Kind != "Secret" {
			continue
		}
		if !v.mayRead(gk, subject.Namespace) {
			a.Coverage.Narrowed = true
			continue
		}
		read := v.session.Indexed(gk, cache.NamespaceIndex, []string{subject.Namespace}, scope)
		gaps := read.Gaps
		for _, o := range read.Objects {
			if scaledToZero(o.Object) {
				continue
			}
			var paths []string
			_, err := configrefs.VisitObjectReferences(o.Object, func(r configrefs.PodSpecReference) {
				if r.Ref == want && !slices.Contains(paths, r.Path) {
					paths = append(paths, r.Path)
				}
			})
			if err != nil {
				gaps = append(gaps, ownership.Gap{Kind: gk, Namespace: subject.Namespace, Reason: readreason.ReadError})
			}
			if len(paths) > 0 {
				a.Consumers = append(a.Consumers, ConfigConsumer{Ref: o.Ref, Paths: paths})
			}
		}
		for _, g := range gaps {
			if !slices.Contains(a.Coverage.NotChecked, g) {
				a.Coverage.NotChecked = append(a.Coverage.NotChecked, g)
			}
		}
	}
	slices.SortStableFunc(a.Consumers, func(x, y ConfigConsumer) int {
		return cmp.Or(cmp.Compare(consumerOrder(x.Ref.GroupKind()), consumerOrder(y.Ref.GroupKind())), cmp.Compare(x.Ref.Name, y.Ref.Name))
	})
	if v.limit > 0 && len(a.Consumers) > v.limit {
		a.Coverage.Truncated = true
		a.Coverage.Total = len(a.Consumers)
		a.Consumers = a.Consumers[:v.limit]
	}
	return a
}
