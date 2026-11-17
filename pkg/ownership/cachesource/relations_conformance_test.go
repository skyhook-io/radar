package cachesource

import (
	"cmp"
	"fmt"
	"math/rand/v2"
	"reflect"
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// declaringKinds are the World's kinds whose pod spec or template declares.
var declaringKinds = []resourceid.GroupKind{
	{Group: "apps", Kind: "Deployment"}, ownershiptest.ArgoRollout, {Group: "batch", Kind: "Job"},
	{Group: "apps", Kind: "ReplicaSet"}, {Kind: "Pod"},
}

// consumerKinds are every kind ConfigConsumers asks for, in its order; the
// ones the World doesn't serve read as empty.
var consumerKinds = []resourceid.GroupKind{
	{Group: "apps", Kind: "Deployment"}, ownershiptest.ArgoRollout, {Group: "apps", Kind: "StatefulSet"},
	{Group: "apps", Kind: "DaemonSet"}, {Group: "batch", Kind: "CronJob"}, {Group: "batch", Kind: "Job"},
	{Group: "apps", Kind: "ReplicaSet"}, {Group: "networking.k8s.io", Kind: "Ingress"}, {Kind: "Pod"},
}

// declaredPath is where DeclaringSpec writes a declaration of kind.
var declaredPath = map[string]string{
	"ConfigMap": "volumes[].configMap.name", "Secret": "volumes[].secret.secretName",
	"PersistentVolumeClaim": "volumes[].persistentVolumeClaim.claimName", "ServiceAccount": "serviceAccountName",
}

// withDeclarations adds Rollouts to w and gives its declaring objects
// references to ConfigMaps in their namespace, present or not. ConfigMaps
// are the only declarable kind a generated World serves; the others are
// literal cases.
func withDeclarations(seed uint64, w ownershiptest.World) (ownershiptest.World, []resourceid.Ref) {
	r := rand.New(rand.NewPCG(seed, seed^0x5bd1e995))
	namespaces := []string{"alpha", "beta", "gamma"}
	for _, ns := range namespaces {
		for i := range r.IntN(3) {
			name := fmt.Sprintf("rollout-%d", i)
			w.Objects = append(w.Objects, ownershiptest.Object{
				Ref:        resourceid.Ref{Group: ownershiptest.ArgoRollout.Group, Kind: ownershiptest.ArgoRollout.Kind, Namespace: ns, Name: name},
				APIVersion: "argoproj.io/v1alpha1", UID: types.UID("uid-" + ns + "-" + name), ResourceVersion: "1",
			})
		}
	}
	var targets []resourceid.Ref
	for _, o := range w.Objects {
		if o.Ref.Kind == "ConfigMap" && o.Ref.Group == "" {
			targets = append(targets, o.Ref)
		}
	}
	for _, ns := range namespaces {
		targets = append(targets, resourceid.Ref{Kind: "ConfigMap", Namespace: ns, Name: "missing"})
	}
	for i := range w.Objects {
		o := &w.Objects[i]
		if !slices.Contains(declaringKinds, o.Ref.GroupKind()) || r.IntN(4) == 0 {
			continue
		}
		var local []resourceid.Ref
		for _, t := range targets {
			if t.Namespace == o.Ref.Namespace {
				local = append(local, t)
			}
		}
		for range 1 + r.IntN(3) {
			if pick := local[r.IntN(len(local))]; !slices.Contains(o.Declares, pick) {
				o.Declares = append(o.Declares, pick)
			}
		}
	}
	return w, targets
}

// expected derives the answers from the World itself: its declarations,
// the caller's access and what Scan says Radar can read, sharing no relation
// rule with pkg/relations.
type expected struct {
	w      ownershiptest.World
	scan   relations.Session
	access ownership.Access
}

// may applies the caller's access the way an answer must: only Allowed
// reads; Denied narrows; a check that couldn't be made is a gap.
func (e expected) may(gk resourceid.GroupKind, namespace string, c *relations.Coverage) bool {
	switch e.access.CanRead(gk, namespace) {
	case ownership.Allowed:
		return true
	case ownership.Undecided:
		addGap(c, ownership.Gap{Kind: gk, Namespace: namespace, Reason: readreason.UserPermissionCheckFailed})
	default:
		c.Narrowed = true
	}
	return false
}

func (e expected) object(ref resourceid.Ref) ownershiptest.Object {
	if i := slices.IndexFunc(e.w.Objects, func(o ownershiptest.Object) bool { return o.Ref == ref }); i >= 0 {
		return e.w.Objects[i]
	}
	return ownershiptest.Object{}
}

func addGap(c *relations.Coverage, g ownership.Gap) {
	if !slices.Contains(c.NotChecked, g) {
		c.NotChecked = append(c.NotChecked, g)
	}
}

// observed reads subject as an answer about it starts: true when Present,
// otherwise the coverage the answer stops with.
func (e expected) observed(subject resourceid.Ref, c *relations.Coverage) bool {
	if !e.may(subject.GroupKind(), subject.Namespace, c) {
		return false
	}
	switch read := e.scan.Object(subject); read.State {
	case ownership.Present:
		return true
	case ownership.Unavailable:
		c.NotChecked = []ownership.Gap{read.Gap}
	}
	return false
}

func (e expected) consumers(subject resourceid.Ref) relations.ReferrersAnswer {
	var a relations.ReferrersAnswer
	if !e.observed(subject, &a.Coverage) {
		return a
	}
	for _, gk := range consumerKinds {
		if gk.Kind == "Ingress" && subject.Kind != "Secret" {
			continue
		}
		if !e.may(gk, subject.Namespace, &a.Coverage) {
			continue
		}
		read := e.scan.Indexed(gk, cache.NamespaceIndex, []string{subject.Namespace}, ownership.InNamespace(subject.Namespace))
		for _, g := range read.Gaps {
			addGap(&a.Coverage, g)
		}
		for _, found := range read.Objects {
			if slices.Contains(e.object(found.Ref).Declares, subject) {
				a.Referrers = append(a.Referrers, relations.Referrer{Ref: found.Ref, Paths: []string{declaredPath[subject.Kind]}})
			}
		}
	}
	slices.SortStableFunc(a.Referrers, func(x, y relations.Referrer) int {
		return cmp.Or(cmp.Compare(slices.Index(consumerKinds, x.Ref.GroupKind()), slices.Index(consumerKinds, y.Ref.GroupKind())), cmp.Compare(x.Ref.Name, y.Ref.Name))
	})
	return a
}

func (e expected) references(subject resourceid.Ref) relations.ReferencesAnswer {
	var a relations.ReferencesAnswer
	if !slices.Contains(declaringKinds, subject.GroupKind()) || !e.observed(subject, &a.Coverage) {
		return a
	}
	targets := slices.Clone(e.object(subject).Declares)
	slices.SortFunc(targets, func(x, y resourceid.Ref) int { return cmp.Or(cmp.Compare(x.Kind, y.Kind), cmp.Compare(x.Name, y.Name)) })
	for _, t := range targets {
		d := relations.Declared{Ref: t, Paths: []string{declaredPath[t.Kind]}}
		switch decision := e.access.CanRead(t.GroupKind(), t.Namespace); {
		case decision == ownership.Undecided:
			d.State = relations.TargetUnread
			d.Gap = ownership.Gap{Kind: t.GroupKind(), Namespace: t.Namespace, Reason: readreason.UserPermissionCheckFailed}
			addGap(&a.Coverage, d.Gap)
		case decision != ownership.Allowed:
			d.State = relations.TargetWithheld
			a.Coverage.Narrowed = true
		default:
			switch read := e.scan.Object(t); read.State {
			case ownership.Present:
				d.State = relations.TargetPresent
			case ownership.Absent:
				d.State = relations.TargetAbsent
			default:
				d.State, d.Gap = relations.TargetUnread, read.Gap
				addGap(&a.Coverage, read.Gap)
			}
		}
		a.Declared = append(a.Declared, d)
	}
	return a
}

func indexedRefs(r relations.IndexedRead) []resourceid.Ref {
	refs := make([]resourceid.Ref, 0, len(r.Objects))
	for _, o := range r.Objects {
		refs = append(refs, o.Ref)
	}
	return refs
}

// exercised counts the answers a conformance run compared that said
// something, so the generated worlds can't pass by answering nothing.
type exercised struct{ consumers, rollouts, gaps, narrowed int }

// conformRelations checks the cache source session got's index reads
// against Scan of w, and the relations answers over both sessions against
// answers derived from w itself, for every target and object of w.
func conformRelations(t *testing.T, w ownershiptest.World, got relations.Session, targets []resourceid.Ref, seen *exercised) {
	t.Helper()
	scanned, err := ownershiptest.Scan(w).Begin()
	if err != nil {
		t.Fatal(err)
	}
	scan := scanned.(relations.Session)
	for _, gk := range consumerKinds {
		for _, ns := range []string{"alpha", "beta", "gamma"} {
			a := scan.Indexed(gk, cache.NamespaceIndex, []string{ns}, ownership.InNamespace(ns))
			b := got.Indexed(gk, cache.NamespaceIndex, []string{ns}, ownership.InNamespace(ns))
			if !slices.Equal(indexedRefs(a), indexedRefs(b)) || !slices.Equal(a.Gaps, b.Gaps) {
				t.Errorf("Indexed(%v, %s): scan %v %v, cache %v %v", gk, ns, indexedRefs(a), a.Gaps, indexedRefs(b), b.Gaps)
			}
		}
	}
	for _, access := range []ownership.Access{ownership.Unrestricted(), alphaOnly{}, betaUndecided{}} {
		want := expected{w: w, scan: scan, access: access}
		for _, session := range []struct {
			name string
			s    relations.Session
		}{{"scan", scan}, {"cache", got}} {
			v := relations.NewView(session.s, access)
			for _, target := range targets {
				answer := v.Referrers(relations.ConfigReferences, target, nil)
				if e := want.consumers(target); !reflect.DeepEqual(answer, e) {
					t.Errorf("%s %T ConfigConsumers(%v):\ngot  %+v\nwant %+v", session.name, access, target, answer, e)
				}
				if session.name != "cache" {
					continue
				}
				if len(answer.Referrers) > 0 {
					seen.consumers++
				}
				if slices.ContainsFunc(answer.Referrers, func(c relations.Referrer) bool { return c.Ref.GroupKind() == ownershiptest.ArgoRollout }) {
					seen.rollouts++
				}
				if len(answer.Coverage.NotChecked) > 0 {
					seen.gaps++
				}
				if answer.Coverage.Narrowed {
					seen.narrowed++
				}
			}
			for _, o := range w.Objects {
				if answer, e := v.References(relations.ConfigReferences, o.Ref, nil), want.references(o.Ref); !reflect.DeepEqual(answer, e) {
					t.Errorf("%s %T ConfigReferences(%v):\ngot  %+v\nwant %+v", session.name, access, o.Ref, answer, e)
				}
			}
		}
	}
}

func TestRelationsConformOnGeneratedWorlds(t *testing.T) {
	seeds := 24
	if testing.Short() {
		seeds = 8
	}
	var seen exercised
	for seed := range uint64(seeds) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			w, targets := withDeclarations(seed, ownershiptest.Generate(seed))
			conformRelations(t, w, begin(t, render(t, w).source()).(relations.Session), targets, &seen)
		})
	}
	if seen.consumers == 0 || seen.rollouts == 0 || seen.gaps == 0 || seen.narrowed == 0 {
		t.Fatalf("the generated worlds didn't exercise every answer: %+v", seen)
	}
}

// TestRelationsConformThroughChanges keeps the answers right as objects are
// created, deleted and change what they declare.
func TestRelationsConformThroughChanges(t *testing.T) {
	histories := 8
	if testing.Short() {
		histories = 3
	}
	var seen exercised
	for seed := range uint64(histories) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			w, targets := withDeclarations(seed, ownershiptest.Generate(seed))
			d := &driver{t: t, r: render(t, w), w: w}
			r := rand.New(rand.NewPCG(seed, 11))
			redeclared := 0
			for n := range 10 {
				change := "redeclare"
				var declaring []ownershiptest.Object
				for _, o := range d.w.Objects {
					if slices.Contains(declaringKinds, o.Ref.GroupKind()) {
						declaring = append(declaring, o)
					}
				}
				if n%2 == 0 || len(declaring) == 0 {
					if change = d.step(r, n); change == "skip" {
						continue
					}
				} else {
					o := declaring[r.IntN(len(declaring))]
					var local []resourceid.Ref
					for _, target := range targets {
						if target.Namespace == o.Ref.Namespace && r.IntN(2) == 0 {
							local = append(local, target)
						}
					}
					d.update(o.Ref, func(o *ownershiptest.Object) { o.Declares = local })
					redeclared++
				}
				d.fence()
				conformRelations(t, d.w, begin(t, d.r.source()).(relations.Session), targets, &seen)
				if t.Failed() {
					t.Fatalf("after step %d (%s)", n, change)
				}
			}
			if redeclared == 0 {
				t.Fatal("no step changed what an object declares")
			}
		})
	}
	if seen.consumers == 0 {
		t.Fatalf("the histories never found a consumer: %+v", seen)
	}
}

// betaUndecided can't check anything in beta, and may read everything else.
type betaUndecided struct{}

func (betaUndecided) CanRead(_ resourceid.GroupKind, namespace string) ownership.Decision {
	if namespace == "beta" {
		return ownership.Undecided
	}
	return ownership.Allowed
}
func (betaUndecided) Namespaces() ([]string, bool) { return nil, true }

func TestIndexedReadByMissingIndexIsIncomplete(t *testing.T) {
	f := newFixture(t, k8score.CacheConfig{}, nil)
	f.watch(t, widgets)
	read, err := f.source().Begin()
	if err != nil {
		t.Fatal(err)
	}
	s := read.(relations.Session)
	for _, gk := range []resourceid.GroupKind{{Kind: "Pod"}, {Group: "example.io", Kind: "Widget"}} {
		got := s.Indexed(gk, "missing", []string{"key"}, ownership.InNamespace("alpha"))
		if len(got.Objects) != 0 || len(got.Gaps) == 0 {
			t.Errorf("%v: %+v; want a gap, never a complete empty answer", gk, got)
		}
	}
}

// A kind discovery doesn't serve reads as empty when discovery covered its
// group, even with another group failing, and as a gap when its own group
// failed; the scan oracle agrees.
func TestIndexedJudgesAnUnservedKindByItsGroup(t *testing.T) {
	w := ownershiptest.Fixture()
	w.FailedGroups = []string{"metrics.k8s.io"}
	got := begin(t, render(t, w).source()).(relations.Session)
	scanned, err := ownershiptest.Scan(w).Begin()
	if err != nil {
		t.Fatal(err)
	}
	scan := scanned.(relations.Session)
	for _, tc := range []struct {
		gk      resourceid.GroupKind
		wantGap bool
	}{
		{resourceid.GroupKind{Group: "keda.sh", Kind: "ScaledObject"}, false},
		{resourceid.GroupKind{Group: "metrics.k8s.io", Kind: "PodMetrics"}, true},
	} {
		for name, s := range map[string]relations.Session{"cache": got, "scan": scan} {
			read := s.Indexed(tc.gk, "", nil, ownership.InNamespace("alpha"))
			if hasGap := len(read.Gaps) > 0; hasGap != tc.wantGap || len(read.Objects) != 0 {
				t.Errorf("%s: Indexed(%v) = %v; want gap %v and no objects", name, tc.gk, read, tc.wantGap)
			}
		}
	}
}
