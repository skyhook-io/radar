package ownershiptest

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// Kind is one kind a generated World can hold.
type Kind struct {
	resourceid.GroupKind
	Version    string
	Resource   string
	Namespaced bool
	// Typed: Radar holds the kind in its typed cache, whose collection is
	// fixed when it starts; other kinds are watched on demand.
	Typed bool
	// Watchable: served with list and watch (and delete), so Radar can
	// collect it and the garbage collector acts on it.
	Watchable bool
}

func (k Kind) APIVersion() string {
	return resourceid.APIVersion(k.Group, k.Version)
}

// Kinds are the kinds generated Worlds use: built-ins the typed cache holds,
// custom kinds Radar watches on demand, one kind that can't be watched, and
// Events.
var Kinds = []Kind{
	{GroupKind: resourceid.GroupKind{Kind: "Pod"}, Version: "v1", Resource: "pods", Namespaced: true, Typed: true, Watchable: true},
	{GroupKind: resourceid.GroupKind{Group: "apps", Kind: "ReplicaSet"}, Version: "v1", Resource: "replicasets", Namespaced: true, Typed: true, Watchable: true},
	{GroupKind: resourceid.GroupKind{Group: "apps", Kind: "Deployment"}, Version: "v1", Resource: "deployments", Namespaced: true, Typed: true, Watchable: true},
	{GroupKind: resourceid.GroupKind{Kind: "ConfigMap"}, Version: "v1", Resource: "configmaps", Namespaced: true, Typed: true, Watchable: true},
	{GroupKind: resourceid.GroupKind{Group: "batch", Kind: "Job"}, Version: "v1", Resource: "jobs", Namespaced: true, Typed: true, Watchable: true},
	{GroupKind: resourceid.GroupKind{Kind: "Node"}, Version: "v1", Resource: "nodes", Typed: true, Watchable: true},
	{GroupKind: resourceid.GroupKind{Kind: "PersistentVolume"}, Version: "v1", Resource: "persistentvolumes", Typed: true, Watchable: true},
	{GroupKind: resourceid.GroupKind{Kind: "Event"}, Version: "v1", Resource: "events", Namespaced: true, Typed: true, Watchable: true},
	{GroupKind: resourceid.GroupKind{Group: "example.io", Kind: "Widget"}, Version: "v1", Resource: "widgets", Namespaced: true, Watchable: true},
	{GroupKind: resourceid.GroupKind{Group: "example.io", Kind: "Gadget"}, Version: "v1", Resource: "gadgets", Namespaced: true, Watchable: true},
	{GroupKind: resourceid.GroupKind{Group: "example.io", Kind: "ClusterWidget"}, Version: "v1", Resource: "clusterwidgets", Watchable: true},
	{GroupKind: resourceid.GroupKind{Group: "example.io", Kind: "Summary"}, Version: "v1", Resource: "summaries", Namespaced: true},
}

// KindOf returns the generated kind gk.
func KindOf(gk resourceid.GroupKind) (Kind, bool) {
	i := slices.IndexFunc(Kinds, func(k Kind) bool { return k.GroupKind == gk })
	if i < 0 {
		return Kind{}, false
	}
	return Kinds[i], true
}

// Resources is what discovery serves for Kinds, plus a second served version
// of Widget, so owner references can name a version Radar doesn't watch.
func Resources() []ownership.ServedResource {
	gc := []string{"create", "delete", "get", "list", "patch", "update", "watch"}
	var out []ownership.ServedResource
	for _, k := range Kinds {
		verbs := gc
		if !k.Watchable {
			verbs = []string{"get", "list"}
		}
		out = append(out, ownership.ServedResource{Group: k.Group, Version: k.Version, Kind: k.Kind, Resource: k.Resource, Namespaced: k.Namespaced, Verbs: verbs})
	}
	return append(out, ownership.ServedResource{Group: "example.io", Version: "v1beta1", Kind: "Widget", Resource: "widgets", Namespaced: true, Verbs: gc})
}

// Fixture is one World with every ownership shape the rules distinguish:
// controller chains, several owners, cluster-scoped owners, a cluster-scoped
// dependent of a namespaced owner, a reference across namespaces, references
// with the right UID and the wrong kind or name, a replaced owner, a dangling
// owner, owner versions served and unserved, deletion in progress and an Event
// naming an owner.
func Fixture() World {
	b := newBuilder()
	web := b.add("apps", "Deployment", "alpha", "web")
	rs := b.add("apps", "ReplicaSet", "alpha", "web-1", web.OwnerRef(true))
	podA := b.add("", "Pod", "alpha", "web-1-a", rs.OwnerRef(true))
	b.deleting(podA, "foregroundDeletion")
	cfg := b.add("", "ConfigMap", "alpha", "cfg")
	b.add("", "Pod", "alpha", "web-1-b", rs.OwnerRef(true), cfg.OwnerRef(false))
	b.add("", "Pod", "beta", "stray", rs.OwnerRef(true))
	wrong := rs.OwnerRef(false)
	wrong.Kind, wrong.Name = "Deployment", "nope"
	b.add("", "Pod", "alpha", "wrong-coordinates", wrong)
	b.add("apps", "ReplicaSet", "alpha", "web-old")
	b.add("", "Pod", "alpha", "replaced", metav1.OwnerReference{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-old", UID: "uid-web-old-before"})
	b.add("", "Pod", "alpha", "dangling", metav1.OwnerReference{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "gone", UID: "uid-gone"})
	w1 := b.add("example.io", "Widget", "alpha", "w1", web.OwnerRef(false))
	w2 := b.add("example.io", "Widget", "alpha", "w2")
	b.add("", "ConfigMap", "alpha", "shared", w1.OwnerRef(false), w2.OwnerRef(false))
	n1 := b.add("", "Node", "", "n1")
	b.add("example.io", "Gadget", "beta", "g1", n1.OwnerRef(true))
	b.add("example.io", "ClusterWidget", "", "cw1", n1.OwnerRef(false))
	b.add("", "Node", "", "n2", w1.OwnerRef(false))
	unserved := w1.OwnerRef(false)
	unserved.APIVersion = "example.io/v9"
	b.add("batch", "Job", "alpha", "j1", unserved)
	otherVersion := w1.OwnerRef(false)
	otherVersion.APIVersion = "example.io/v1beta1"
	b.add("", "ConfigMap", "alpha", "versioned", otherVersion)
	b.add("", "Event", "alpha", "e1", podA.OwnerRef(false))
	b.add("", "PersistentVolume", "", "pv1")
	b.add("example.io", "Summary", "alpha", "s1", w1.OwnerRef(false))
	return b.world()
}

// Generate returns a random World of seed: small ownership motifs over two or
// three namespaces, each kind collected in a random way.
func Generate(seed uint64) World {
	r := rand.New(rand.NewPCG(seed, seed^0x9e3779b97f4a7c15))
	b := newBuilder()
	namespaces := []string{"alpha", "beta", "gamma"}[:2+r.IntN(2)]
	ns := func() string { return namespaces[r.IntN(len(namespaces))] }
	pick := func(list ...string) string { return list[r.IntN(len(list))] }
	pickKind := func(kinds ...resourceid.GroupKind) resourceid.GroupKind { return kinds[r.IntN(len(kinds))] }
	addKind := func(gk resourceid.GroupKind, namespace, name string, owners ...metav1.OwnerReference) Object {
		return b.add(gk.Group, gk.Kind, namespace, name, owners...)
	}
	configMap, gadget, pod := resourceid.GroupKind{Kind: "ConfigMap"}, resourceid.GroupKind{Group: "example.io", Kind: "Gadget"}, resourceid.GroupKind{Kind: "Pod"}
	node, pv := resourceid.GroupKind{Kind: "Node"}, resourceid.GroupKind{Kind: "PersistentVolume"}
	event, summary := resourceid.GroupKind{Kind: "Event"}, resourceid.GroupKind{Group: "example.io", Kind: "Summary"}
	var owners []Object
	for i := range 4 + r.IntN(6) {
		name := fmt.Sprintf("m%d", i)
		switch r.IntN(8) {
		case 0: // a controller chain
			space := ns()
			d := b.add("apps", "Deployment", space, name)
			rs := b.add("apps", "ReplicaSet", space, name+"-rs", d.OwnerRef(true))
			for j := range 1 + r.IntN(3) {
				b.add("", "Pod", space, fmt.Sprintf("%s-rs-%d", name, j), rs.OwnerRef(true))
			}
			owners = append(owners, d, rs)
		case 1: // several owners, some of another kind
			space := ns()
			a := b.add("example.io", "Widget", space, name+"-a")
			c := b.add("", "ConfigMap", space, name+"-c")
			addKind(pickKind(configMap, gadget), space, name+"-shared", a.OwnerRef(r.IntN(2) == 0), c.OwnerRef(false))
			owners = append(owners, a, c)
		case 2: // a cluster-scoped owner
			n := b.add("", "Node", "", name+"-node")
			addKind(pickKind(pod, gadget), ns(), name+"-on-node", n.OwnerRef(r.IntN(2) == 0))
			b.add("example.io", "ClusterWidget", "", name+"-cw", n.OwnerRef(false))
			owners = append(owners, n)
		case 3: // a cluster-scoped dependent of a namespaced owner
			w := b.add("example.io", "Widget", ns(), name+"-w")
			addKind(pickKind(node, pv), "", name+"-invalid", w.OwnerRef(false))
			owners = append(owners, w)
		case 4: // a reference into another namespace, or with the wrong coordinates
			if len(owners) == 0 {
				continue
			}
			o := owners[r.IntN(len(owners))]
			ref := o.OwnerRef(false)
			if r.IntN(2) == 0 {
				ref.Kind, ref.Name = pick("Deployment", "Widget", "Node"), pick("wrong", o.Ref.Name)
			}
			b.add("", "Pod", ns(), name+"-ref", ref)
		case 5: // a replaced or dangling owner
			space := ns()
			if r.IntN(2) == 0 {
				b.add("apps", "ReplicaSet", space, name+"-old")
			}
			b.add("", "Pod", space, name+"-orphan", metav1.OwnerReference{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: name + "-old", UID: types.UID("uid-before-" + name)})
		case 6: // deletion in progress, an unserved owner version
			space := ns()
			w := b.add("example.io", "Widget", space, name+"-w")
			ref := w.OwnerRef(true)
			if r.IntN(2) == 0 {
				ref.APIVersion = pick("example.io/v1beta1", "example.io/v9")
			}
			j := b.add("batch", "Job", space, name+"-job", ref)
			b.deleting(j, pick("foregroundDeletion", "orphan", "example.io/cleanup"))
			owners = append(owners, w)
		case 7: // kinds that are never dependents
			if len(owners) == 0 {
				continue
			}
			o := owners[r.IntN(len(owners))]
			space := o.Ref.Namespace
			if space == "" {
				space = ns()
			}
			addKind(pickKind(event, summary), space, name+"-other", o.OwnerRef(false))
		}
	}
	w := b.world()
	w.Partial = r.IntN(8) == 0
	for _, k := range Kinds {
		if !k.Watchable {
			continue
		}
		c := w.Collection[k.GroupKind]
		switch roll := r.IntN(20); {
		case roll < 2:
			c.State = Loading
		case roll < 4:
			c.State = NotCollected
		case roll < 5:
			c.State = Refused
		}
		if k.Namespaced && r.IntN(5) == 0 && c.State <= Loading {
			c.Namespaces = slices.Clone(namespaces[:1+r.IntN(len(namespaces)-1)])
		}
		w.Collection[k.GroupKind] = c
	}
	return w
}

// builder assembles a World with UIDs and resource versions derived from
// object names, so failures name objects readably.
type builder struct {
	objects []Object
}

func newBuilder() *builder {
	return &builder{}
}

func (b *builder) add(group, kind, namespace, name string, owners ...metav1.OwnerReference) Object {
	k, ok := KindOf(resourceid.GroupKind{Group: group, Kind: kind})
	if !ok {
		panic("ownershiptest: no generated kind " + kind)
	}
	if !k.Namespaced {
		namespace = ""
	}
	uid := "uid-" + name
	if namespace != "" {
		uid = "uid-" + namespace + "-" + name
	}
	o := Object{
		Ref:             resourceid.Ref{Group: group, Kind: kind, Namespace: namespace, Name: name},
		APIVersion:      k.APIVersion(),
		UID:             types.UID(uid),
		ResourceVersion: fmt.Sprint(len(b.objects) + 1),
		Owners:          owners,
	}
	b.objects = append(b.objects, o)
	return o
}

func (b *builder) deleting(o Object, finalizers ...string) {
	at := metav1.NewTime(time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC))
	for i := range b.objects {
		if b.objects[i].Ref == o.Ref {
			b.objects[i].DeletionTimestamp = &at
			b.objects[i].Finalizers = finalizers
		}
	}
}

// world returns the World built, every kind collected in full except those
// Radar can't watch.
func (b *builder) world() World {
	w := World{Resources: Resources(), Objects: b.objects, Collection: map[resourceid.GroupKind]Collection{}}
	for _, k := range Kinds {
		c := Collection{OnDemand: !k.Typed}
		if !k.Watchable {
			c.State = Unwatchable
		}
		w.Collection[k.GroupKind] = c
	}
	return w
}
