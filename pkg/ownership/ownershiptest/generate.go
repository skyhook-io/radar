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
// custom kinds Radar watches on demand (one named like a built-in of another
// scope), one kind that can't be watched, Events, and the GitOps managers
// whose status lists their members.
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
	{GroupKind: resourceid.GroupKind{Group: "example.io", Kind: "Node"}, Version: "v1", Resource: "nodes", Namespaced: true, Watchable: true},
	{GroupKind: ownership.ArgoApplication, Version: "v1alpha1", Resource: "applications", Namespaced: true, Watchable: true},
	{GroupKind: ownership.FluxHelmRelease, Version: "v2", Resource: "helmreleases", Namespaced: true, Watchable: true},
	{GroupKind: ownership.FluxKustomization, Version: "v1", Resource: "kustomizations", Namespaced: true, Watchable: true},
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
// naming an owner. Its GitOps managers hold every membership state, in every
// form their status takes.
func Fixture() World {
	b := newBuilder()
	web := b.add("apps", "Deployment", "alpha", "web")
	rs := b.add("apps", "ReplicaSet", "alpha", "web-1", web.OwnerRef(true))
	podA := b.add("", "Pod", "alpha", "web-1-a", rs.OwnerRef(true))
	b.deleting(podA, "foregroundDeletion")
	cfg := b.add("", "ConfigMap", "alpha", "cfg")
	b.add("", "Pod", "alpha", "web-1-b", rs.OwnerRef(true), cfg.OwnerRef(false))
	stray := b.add("", "Pod", "beta", "stray", rs.OwnerRef(true))
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
	pv1 := b.add("", "PersistentVolume", "", "pv1")
	b.add("example.io", "Summary", "alpha", "s1", w1.OwnerRef(false))

	// Argo CD lists a namespaced member in its destination, one elsewhere,
	// cluster-scoped ones, one already gone, one of a kind discovery doesn't
	// serve, a built-in and a custom kind twice each, with and without their
	// namespace, and a namespaced custom kind named like a cluster-scoped
	// built-in.
	gone := resourceid.Ref{Group: "apps", Kind: "Deployment", Namespace: "alpha", Name: "gone"}
	unknownKind := resourceid.Ref{Group: "example.io", Kind: "Unserved", Namespace: "alpha", Name: "x"}
	shared := resourceid.Ref{Kind: "ConfigMap", Namespace: "alpha", Name: "shared"}
	g1 := resourceid.Ref{Group: "example.io", Kind: "Gadget", Namespace: "beta", Name: "g1"}
	customNode := b.add("example.io", "Node", "alpha", "n1")
	cw1 := resourceid.Ref{Group: "example.io", Kind: "ClusterWidget", Name: "cw1"}
	app := ownership.ArgoApplication
	b.manager(app, "argocd", "web", ownership.MembersRecorded, 0, "alpha", web.Ref, cfg.Ref, n1.Ref, w1.Ref, gone, unknownKind, g1, n1.Ref, cfg.Ref, customNode.Ref, w1.Ref)
	b.manager(app, "argocd", "by-name", ownership.MembersRecorded, 1, "beta", g1, pv1.Ref, web.Ref, stray.Ref)
	b.manager(app, "argocd", "no-destination-namespace", ownership.MembersRecorded, 0, "", n1.Ref, cfg.Ref)
	b.manager(app, "argocd", "empty-list", ownership.MembersRecorded, 0, "alpha")
	b.manager(app, "argocd", "empty", ownership.MembersRecorded, 1, "alpha")
	b.manager(app, "argocd", "elsewhere", ownership.MembersRemote, 0, "alpha", web.Ref)
	b.manager(app, "argocd", "elsewhere-by-name", ownership.MembersRemote, 1, "alpha", cfg.Ref)
	b.manager(app, "argocd", "new", ownership.MembersUnreadable, 0, "alpha")
	b.manager(app, "argocd", "malformed", ownership.MembersUnreadable, 1, "alpha", web.Ref)
	b.manager(app, "argocd", "nowhere", ownership.MembersUnreadable, 2, "alpha", cfg.Ref)
	// Flux lists cluster-scoped members with and without a namespace the
	// kind ignores, and shares members with Argo CD.
	ks, hr := ownership.FluxKustomization, ownership.FluxHelmRelease
	b.manager(ks, "flux-system", "apps", ownership.MembersRecorded, 0, "", w1.Ref, web.Ref, shared, n1.Ref, pv1.Ref, cw1, unknownKind)
	b.manager(ks, "flux-system", "empty", ownership.MembersRecorded, 1, "")
	b.manager(ks, "flux-system", "remote", ownership.MembersRemote, 0, "", cfg.Ref)
	b.manager(ks, "flux-system", "bad-id", ownership.MembersUnreadable, 1, "", web.Ref)
	b.manager(hr, "flux-system", "chart", ownership.MembersRecorded, 0, "", cfg.Ref, web.Ref)
	b.manager(hr, "flux-system", "legacy", ownership.MembersUnreadable, 0, "")
	b.manager(hr, "flux-system", "no-id", ownership.MembersUnreadable, 2, "", cfg.Ref)
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
	membership := func() ownership.MembershipState {
		return []ownership.MembershipState{ownership.MembersRecorded, ownership.MembersRecorded, ownership.MembersRecorded, ownership.MembersRemote, ownership.MembersUnreadable}[r.IntN(5)]
	}
	// members picks up to three objects so far for a manager to list, and
	// sometimes one that is gone or of a kind discovery doesn't serve.
	members := func(name string) []resourceid.Ref {
		var out []resourceid.Ref
		for range r.IntN(4) {
			if len(b.objects) > 0 {
				out = append(out, b.objects[r.IntN(len(b.objects))].Ref)
			}
		}
		switch r.IntN(6) {
		case 0:
			out = append(out, resourceid.Ref{Group: "apps", Kind: "Deployment", Namespace: ns(), Name: name + "-gone"})
		case 1:
			out = append(out, resourceid.Ref{Group: "example.io", Kind: "Unserved", Namespace: ns(), Name: name + "-x"})
		}
		return out
	}
	for i := range 4 + r.IntN(6) {
		name := fmt.Sprintf("m%d", i)
		switch r.IntN(10) {
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
		case 8: // an Argo CD Application
			destination := ns()
			if r.IntN(4) == 0 {
				destination = ""
			}
			b.manager(ownership.ArgoApplication, ns(), name+"-app", membership(), uint8(r.IntN(3)), destination, members(name)...)
		case 9: // a Flux Kustomization or HelmRelease
			kind := pickKind(ownership.FluxKustomization, ownership.FluxHelmRelease)
			b.manager(kind, ns(), name+"-flux", membership(), uint8(r.IntN(3)), "", members(name)...)
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

// manager adds a GitOps manager whose status says state of members.
func (b *builder) manager(gk resourceid.GroupKind, namespace, name string, state ownership.MembershipState, form uint8, destination string, members ...resourceid.Ref) Object {
	b.add(gk.Group, gk.Kind, namespace, name)
	o := &b.objects[len(b.objects)-1]
	o.Membership, o.Form, o.Destination, o.Members = state, form, destination, members
	return *o
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
