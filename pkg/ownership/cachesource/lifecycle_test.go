package cachesource

import (
	"context"
	"fmt"
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// driver changes a rendered World and the World alike, so after each change
// the source can be checked against Scan of the World.
type driver struct {
	t   *testing.T
	r   *rendered
	w   ownershiptest.World
	rev int
}

func (d *driver) resource(o ownershiptest.Object) (ownershiptest.Kind, schema.GroupVersionResource) {
	k, _ := ownershiptest.KindOf(o.Ref.GroupKind())
	return k, schema.GroupVersionResource{Group: k.Group, Version: k.Version, Resource: k.Resource}
}

func (d *driver) nextVersion() string {
	d.rev++
	return fmt.Sprint(1000 + d.rev)
}

// apply makes the API server change o: through the typed fake's tracker for
// typed kinds, so it keeps the World's resource version, and through the
// dynamic client otherwise.
func (d *driver) apply(verb string, o ownershiptest.Object) {
	d.t.Helper()
	ctx := context.Background()
	obj, typed := apiObject(d.t, o)
	_, gvr := d.resource(o)
	var err error
	if typed {
		tracker := d.r.typed.Tracker()
		switch verb {
		case "create":
			err = tracker.Create(gvr, obj, o.Ref.Namespace)
		case "update":
			err = tracker.Update(gvr, obj, o.Ref.Namespace)
		case "delete":
			err = tracker.Delete(gvr, o.Ref.Namespace, o.Ref.Name)
		}
	} else {
		client := d.r.dynamic.Resource(gvr).Namespace(o.Ref.Namespace)
		u := obj.(*unstructured.Unstructured)
		switch verb {
		case "create":
			_, err = client.Create(ctx, u, metav1.CreateOptions{})
		case "update":
			_, err = client.Update(ctx, u, metav1.UpdateOptions{})
		case "delete":
			err = client.Delete(ctx, o.Ref.Name, metav1.DeleteOptions{})
		}
	}
	if err != nil {
		d.t.Fatalf("%s %v: %v", verb, o.Ref, err)
	}
}

func (d *driver) index(ref resourceid.Ref) int {
	return slices.IndexFunc(d.w.Objects, func(o ownershiptest.Object) bool { return o.Ref == ref })
}

func (d *driver) create(o ownershiptest.Object) {
	o.ResourceVersion = d.nextVersion()
	d.apply("create", o)
	d.w.Objects = append(d.w.Objects, o)
}

func (d *driver) update(ref resourceid.Ref, change func(*ownershiptest.Object)) {
	i := d.index(ref)
	o := d.w.Objects[i]
	o.Owners = slices.Clone(o.Owners)
	change(&o)
	o.ResourceVersion = d.nextVersion()
	d.apply("update", o)
	d.w.Objects[i] = o
}

func (d *driver) delete(ref resourceid.Ref) {
	i := d.index(ref)
	d.apply("delete", d.w.Objects[i])
	d.w.Objects = slices.Delete(d.w.Objects, i, i+1)
}

// fence waits until every store Radar has loaded holds exactly the World's
// objects of its kind and namespaces, by UID and resource version.
func (d *driver) fence() {
	d.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		stale := d.staleKind()
		if stale == "" {
			return
		}
		if time.Now().After(deadline) {
			d.t.Fatalf("fence: %s never caught up", stale)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (d *driver) staleKind() string {
	for _, k := range ownershiptest.Kinds {
		c := d.w.Collection[k.GroupKind]
		if !k.Watchable || c.State != ownershiptest.Collected {
			continue
		}
		want := map[string]string{}
		for _, o := range d.w.Objects {
			if o.Ref.GroupKind() == k.GroupKind && (c.Namespaces == nil || !k.Namespaced || slices.Contains(c.Namespaces, o.Ref.Namespace)) {
				want[o.Ref.Namespace+"/"+o.Ref.Name] = string(o.UID) + "@" + o.ResourceVersion
			}
		}
		got := map[string]string{}
		var items []any
		if k.Typed {
			objs, ok := d.r.caches.Typed.ReadableObjects(k.Resource, nil)
			if !ok {
				return k.GroupKind.String() + " (not loaded)"
			}
			for _, o := range objs {
				items = append(items, o)
			}
		} else {
			// Every watch the kind is collected through must have loaded,
			// so an empty store isn't mistaken for a missing one.
			gvr := schema.GroupVersionResource{Group: k.Group, Version: k.Version, Resource: k.Resource}
			scopes := []string{""}
			if k.Namespaced && c.Namespaces != nil {
				scopes = c.Namespaces
			}
			for _, ns := range scopes {
				if _, synced := d.r.caches.Dynamic.ReadServer(gvr, ns); !synced {
					return k.GroupKind.String() + " (not loaded in " + ns + ")"
				}
			}
			objs, _ := d.r.caches.Dynamic.ListWatchedReadOnly(gvr)
			for _, o := range objs {
				items = append(items, o)
			}
		}
		for _, item := range items {
			m, err := apimeta.Accessor(item)
			if err != nil {
				continue
			}
			got[m.GetNamespace()+"/"+m.GetName()] = string(m.GetUID()) + "@" + m.GetResourceVersion()
		}
		if fmt.Sprint(want) != fmt.Sprint(got) {
			return k.GroupKind.String()
		}
	}
	return ""
}

// step makes one random change: reparent, orphan, delete, recreate under the
// same name with a new UID, start deleting, or create a dependent.
func (d *driver) step(r *rand.Rand, n int) string {
	pick := func() ownershiptest.Object { return d.w.Objects[r.IntN(len(d.w.Objects))] }
	switch r.IntN(6) {
	case 0:
		o, owner := pick(), pick()
		d.update(o.Ref, func(o *ownershiptest.Object) { o.Owners = append(o.Owners, owner.OwnerRef(r.IntN(2) == 0)) })
		return fmt.Sprintf("%v gains owner %v", o.Ref, owner.Ref)
	case 1:
		o := pick()
		d.update(o.Ref, func(o *ownershiptest.Object) { o.Owners = nil })
		return fmt.Sprintf("%v loses its owners", o.Ref)
	case 2:
		if len(d.w.Objects) < 4 {
			return "skip"
		}
		o := pick()
		d.delete(o.Ref)
		return fmt.Sprintf("%v deleted", o.Ref)
	case 3:
		o := pick()
		d.delete(o.Ref)
		o.UID = types.UID(fmt.Sprintf("%s-again-%d", o.UID, n))
		d.create(o)
		return fmt.Sprintf("%v recreated as %s", o.Ref, o.UID)
	case 4:
		o := pick()
		at := metav1.NewTime(time.Date(2026, 10, 17, 11, 0, n, 0, time.UTC))
		d.update(o.Ref, func(o *ownershiptest.Object) {
			o.DeletionTimestamp = &at
			o.Finalizers = append(slices.Clone(o.Finalizers), "example.io/hold")
		})
		return fmt.Sprintf("%v starts deleting", o.Ref)
	default:
		owner := pick()
		ns := owner.Ref.Namespace
		if ns == "" {
			ns = "alpha"
		}
		kind := []resourceid.GroupKind{{Kind: "Pod"}, {Group: "example.io", Kind: "Gadget"}}[r.IntN(2)]
		k, _ := ownershiptest.KindOf(kind)
		o := ownershiptest.Object{
			Ref:        resourceid.Ref{Group: kind.Group, Kind: kind.Kind, Namespace: ns, Name: fmt.Sprintf("new-%d", n)},
			APIVersion: k.APIVersion(), UID: types.UID(fmt.Sprintf("uid-new-%d", n)),
			Owners: []metav1.OwnerReference{owner.OwnerRef(true)},
		}
		d.create(o)
		return fmt.Sprintf("%v created under %v", o.Ref, owner.Ref)
	}
}

func TestSourceConformsThroughChanges(t *testing.T) {
	histories := 12
	if testing.Short() {
		histories = 3
	}
	for seed := range uint64(histories) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			w := ownershiptest.Generate(seed)
			d := &driver{t: t, r: render(t, w), w: w}
			changed := 0
			r := rand.New(rand.NewPCG(seed, 7))
			for n := range 10 {
				change := d.step(r, n)
				if change == "skip" {
					continue
				}
				d.fence()
				want, _ := ownershiptest.Scan(d.w).Begin()
				got := begin(t, d.r.source())
				if diffs := ownershiptest.Conform(t, want, got, ownershiptest.QueriesFor(d.w)); diffs > 0 {
					t.Fatalf("after step %d (%s): %d differences", n, change, diffs)
				}
				if err := got.Validate(); err != nil {
					t.Fatalf("after step %d (%s): Validate: %v", n, change, err)
				}
				changed++
			}
			if changed < 5 {
				t.Fatalf("only %d of 10 steps changed the World", changed)
			}
		})
	}
}

// The fence must not mistake a missing store for an empty one: a kind the
// World collects, with no objects, is caught up only once a store serving it
// has loaded.
func TestFenceWaitsForAnEmptyStoreToLoad(t *testing.T) {
	f := newFixture(t, k8score.CacheConfig{}, nil)
	w := ownershiptest.World{Collection: map[resourceid.GroupKind]ownershiptest.Collection{}}
	for _, k := range ownershiptest.Kinds {
		w.Collection[k.GroupKind] = ownershiptest.Collection{State: ownershiptest.NotCollected}
	}
	w.Collection[widgetKind] = ownershiptest.Collection{OnDemand: true}
	d := &driver{t: t, r: &rendered{caches: f.caches}, w: w}
	if _, ok := f.caches.Dynamic.ReadServer(widgets, ""); ok {
		t.Fatal("scenario: no store may serve Widgets yet")
	}
	if stale := d.staleKind(); stale == "" {
		t.Fatal("the fence passed with no store serving Widgets")
	}
}
