package relations

import (
	"fmt"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/configrefs"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// countingAccess wraps an access and counts what it is asked, by kind.
type countingAccess struct {
	ownership.Access
	asked map[string]int
}

func (a *countingAccess) CanRead(gk resourceid.GroupKind, namespace string) ownership.Decision {
	a.asked[gk.Kind]++
	return a.Access.CanRead(gk, namespace)
}

func counting(access ownership.Access) *countingAccess {
	return &countingAccess{Access: access, asked: map[string]int{}}
}

// In the subject's namespace an answer checks a kind only for objects that
// relate, and says it withheld something exactly when a related one is hidden.
func TestAnswersWithholdOnlyRelatedObjects(t *testing.T) {
	web := labelled(deployment("web", corev1.PodSpec{}), map[string]string{"app": "web"})
	related := typed(&corev1.Service{ObjectMeta: meta("web"), Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "web"}}}, "", "v1", "Service")
	unrelated := typed(&corev1.Service{ObjectMeta: meta("other"), Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "other"}}}, "", "v1", "Service")

	for _, tc := range []struct {
		name        string
		objects     []runtime.Object
		wantChecked bool
		wantNarrow  bool
	}{
		{"no Service", []runtime.Object{web}, false, false},
		{"an unrelated Service", []runtime.Object{web, unrelated}, false, false},
		{"a related Service", []runtime.Object{web, unrelated, related}, true, true},
	} {
		access := counting(denies{"Service"})
		got := NewView(allNamespaces{cluster: cluster{objects: tc.objects}}, access).SelectedBy(ServiceSelectors, refOf(web), nil)
		if checked := access.asked["Service"] > 0; checked != tc.wantChecked || got.Coverage.Narrowed != tc.wantNarrow || len(got.Matches) != 0 {
			t.Errorf("%s: checked=%v narrowed=%v matches=%v; want checked=%v narrowed=%v and nothing listed", tc.name, checked, got.Coverage.Narrowed, got.Matches, tc.wantChecked, tc.wantNarrow)
		}
	}

	got := NewView(allNamespaces{cluster: cluster{objects: []runtime.Object{web, unrelated, related}}}, ownership.Unrestricted()).SelectedBy(ServiceSelectors, refOf(web), nil)
	if len(got.Matches) != 1 || got.Matches[0] != refOf(related) || got.Coverage.Narrowed {
		t.Errorf("a caller who may read Services got %+v", got)
	}
}

// Across namespaces the same holds where the caller may not read: a related
// object there narrows the answer, an unrelated one doesn't.
func TestAnswersAcrossNamespacesWithholdOnlyRelatedObjects(t *testing.T) {
	svc := labelled(typed(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "web"}}, "", "v1", "Service"), map[string]string{"metrics": "on"})
	monitor := func(name, label string) runtime.Object {
		return inNamespace(object("monitoring.coreos.com/v1", "ServiceMonitor", name, map[string]any{
			"selector":          map[string]any{"matchLabels": map[string]any{"metrics": label}},
			"namespaceSelector": map[string]any{"any": true},
		}), "monitoring")
	}
	restricted := namespaceAccess{namespace: "apps"}

	for _, tc := range []struct {
		name       string
		objects    []runtime.Object
		wantNarrow bool
	}{
		{"no monitor", []runtime.Object{svc}, false},
		{"an unrelated monitor elsewhere", []runtime.Object{svc, monitor("other", "off")}, false},
		{"a related monitor elsewhere", []runtime.Object{svc, monitor("web", "on")}, true},
	} {
		got := NewView(allNamespaces{cluster: cluster{objects: tc.objects}}, restricted).SelectedBy(Monitors, refOf(svc), nil)
		if got.Coverage.Narrowed != tc.wantNarrow || len(got.Matches) != 0 {
			t.Errorf("%s: narrowed=%v matches=%v; want narrowed=%v and nothing listed", tc.name, got.Coverage.Narrowed, got.Matches, tc.wantNarrow)
		}
	}
}

// undecidedElsewhere may read namespace, and can't check any other.
type undecidedElsewhere struct{ namespaceAccess }

func (u undecidedElsewhere) CanRead(gk resourceid.GroupKind, namespace string) ownership.Decision {
	if d := u.namespaceAccess.CanRead(gk, namespace); d != ownership.Denied {
		return d
	}
	return ownership.Undecided
}

// Across namespaces, a related object whose access check couldn't be made
// leaves a gap naming no namespace, rather than reading as withheld.
func TestAnAcrossCheckThatCouldntBeMadeIsAGapNotADenial(t *testing.T) {
	svc := labelled(typed(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "web"}}, "", "v1", "Service"), map[string]string{"metrics": "on"})
	monitor := inNamespace(object("monitoring.coreos.com/v1", "ServiceMonitor", "web", map[string]any{
		"selector":          map[string]any{"matchLabels": map[string]any{"metrics": "on"}},
		"namespaceSelector": map[string]any{"any": true},
	}), "monitoring")
	got := NewView(allNamespaces{cluster: cluster{objects: []runtime.Object{svc, monitor}}}, undecidedElsewhere{namespaceAccess{namespace: "apps"}}).SelectedBy(Monitors, refOf(svc), nil)
	want := ownership.Gap{Kind: refOf(monitor).GroupKind(), Reason: readreason.UserPermissionCheckFailed}
	if got.Coverage.Narrowed || len(got.Matches) != 0 || len(got.Coverage.NotChecked) != 1 || got.Coverage.NotChecked[0] != want {
		t.Errorf("got %+v; want no matches, not narrowed, gap %v", got, want)
	}
}

// A kind Radar hasn't read yet where the caller may not read could hide a
// related object, so the answer says it may have withheld one.
func TestAnswersAcrossNamespacesNarrowOnUnreadableGaps(t *testing.T) {
	svc := labelled(typed(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "web"}}, "", "v1", "Service"), map[string]string{"metrics": "on"})
	monitors := resourceid.GroupKind{Group: "monitoring.coreos.com", Kind: "ServiceMonitor"}
	cold := allNamespaces{cluster: cluster{objects: []runtime.Object{svc}}, gaps: []ownership.Gap{{Kind: monitors, Namespace: "monitoring", Reason: readreason.KindSyncPending}}}
	got := NewView(cold, namespaceAccess{namespace: "apps"}).SelectedBy(Monitors, refOf(svc), nil)
	if !got.Coverage.Narrowed || len(got.Coverage.NotChecked) != 0 {
		t.Errorf("an unread kind where the caller may not read: %+v", got.Coverage)
	}
}

// A hidden object that fails to decode says nothing about itself: only an
// object the caller may read records a read error.
func TestHiddenObjectsDecodeErrorsStayHidden(t *testing.T) {
	sealed := object("bitnami.com/v1alpha1", "SealedSecret", "creds", nil)
	hidden := object("argoproj.io/v1alpha1", "Rollout", "private", map[string]any{"template": map[string]any{"spec": "not-an-object"}})
	session := allNamespaces{cluster: cluster{objects: []runtime.Object{sealed, hidden}}}
	if got := NewView(session, denies{"Rollout"}).Composed(SealedSecretConsumers, refOf(sealed), nil); len(got.Coverage.NotChecked) != 0 {
		t.Fatalf("a hidden Rollout's decode error reached the answer: %+v", got)
	}
	if got := NewView(session, ownership.Unrestricted()).Composed(SealedSecretConsumers, refOf(sealed), nil); len(got.Coverage.NotChecked) == 0 {
		t.Fatalf("a readable Rollout's decode error was lost: %+v", got)
	}
}

// primaryMiddlewareDenied may not read traefik.io Middlewares, the group a
// reference resolves to first.
type primaryMiddlewareDenied struct{}

func (primaryMiddlewareDenied) CanRead(gk resourceid.GroupKind, _ string) ownership.Decision {
	if gk.Group == "traefik.io" && gk.Kind == "Middleware" {
		return ownership.Denied
	}
	return ownership.Allowed
}
func (primaryMiddlewareDenied) Namespaces() ([]string, bool) { return nil, true }

// A reference whose resolution stopped at a candidate the caller may not read
// leaves the answer withheld, not complete and empty.
func TestResolutionStoppedByAWithheldCandidateNarrows(t *testing.T) {
	route := object("traefik.io/v1alpha1", "IngressRoute", "route", map[string]any{"routes": []any{map[string]any{"middlewares": []any{map[string]any{"name": "config"}}}}})
	legacy := object("traefik.containo.us/v1alpha1", "Middleware", "config", nil)
	session := allNamespaces{cluster: cluster{objects: []runtime.Object{route, legacy}}}
	got := NewView(session, primaryMiddlewareDenied{}).Referrers(TraefikRouteConfig, refOf(legacy), nil)
	if len(got.Referrers) != 0 || !got.Coverage.Narrowed {
		t.Fatalf("resolution stopped by a withheld candidate: %+v", got)
	}
}

// A gap Radar's setup causes names no object, so it reaches a caller who may
// not read the kind without a check; any other gap still needs the kind.
func TestSetupGapsInTheSubjectsNamespaceNeedNoKindCheck(t *testing.T) {
	web := labelled(deployment("web", corev1.PodSpec{}), map[string]string{"app": "web"})
	for _, tc := range []struct {
		reason      readreason.Code
		wantGap     bool
		wantChecked bool
	}{
		{readreason.RadarNamespaceNotCollected, true, false},
		{readreason.ReadError, false, true},
	} {
		access := counting(denies{"Service"})
		c := allNamespaces{cluster: cluster{objects: []runtime.Object{web}}, gaps: []ownership.Gap{{Kind: kindService, Namespace: refOf(web).Namespace, Reason: tc.reason}}}
		got := NewView(c, access).SelectedBy(ServiceSelectors, refOf(web), nil)
		if hasGap := len(got.Coverage.NotChecked) == 1; hasGap != tc.wantGap || (access.asked["Service"] > 0) != tc.wantChecked {
			t.Errorf("%s: gaps=%v checked=%v; want gap=%v checked=%v", tc.reason, got.Coverage.NotChecked, access.asked["Service"] > 0, tc.wantGap, tc.wantChecked)
		}
	}
}

// partlyCollected holds ServiceMonitors only in the namespaces it names: an
// all-namespace read reports the rest as one gap naming no namespace.
type partlyCollected struct {
	cluster
	collected []string
}

func (p partlyCollected) Catalog() *ownership.Catalog {
	return ownership.NewCatalog([]ownership.ServedResource{{Group: "monitoring.coreos.com", Version: "v1", Kind: "ServiceMonitor", Resource: "servicemonitors", Namespaced: true, Verbs: []string{"list", "watch"}}}, true)
}

func (p partlyCollected) Indexed(gk resourceid.GroupKind, index string, keys []string, scope ownership.SearchScope) IndexedRead {
	if gk.Kind != "ServiceMonitor" {
		return p.cluster.Indexed(gk, index, keys, scope)
	}
	if !scope.AllNamespaces {
		var r IndexedRead
		for _, ns := range scope.Namespaces {
			if !slices.Contains(p.collected, ns) {
				r.Gaps = append(r.Gaps, ownership.Gap{Kind: gk, Namespace: ns, Reason: readreason.RadarNamespaceNotCollected})
			}
		}
		return r
	}
	return IndexedRead{Gaps: []ownership.Gap{{Kind: gk, Reason: readreason.RadarNamespaceNotCollected}}}
}

// When Radar collects a kind in only some namespaces, an across answer for a
// caller whose namespaces are all collected is complete and may have withheld;
// a caller namespace Radar doesn't collect is still a gap.
func TestAnswersAcrossNamespacesKeepOnlyReadableUncollectedGaps(t *testing.T) {
	svc := labelled(typed(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "web"}}, "", "v1", "Service"), map[string]string{"metrics": "on"})
	monitors := resourceid.GroupKind{Group: "monitoring.coreos.com", Kind: "ServiceMonitor"}
	got := NewView(partlyCollected{cluster: cluster{objects: []runtime.Object{svc}}, collected: []string{"apps", "monitoring"}}, namespaceAccess{namespace: "apps"}).SelectedBy(Monitors, refOf(svc), svc)
	if !got.Coverage.Complete() || !got.Coverage.Narrowed {
		t.Errorf("caller's namespaces collected: %+v; want complete and narrowed", got.Coverage)
	}
	got = NewView(partlyCollected{cluster: cluster{objects: []runtime.Object{svc}}, collected: []string{"monitoring"}}, namespaceAccess{namespace: "apps"}).SelectedBy(Monitors, refOf(svc), svc)
	want := ownership.Gap{Kind: monitors, Namespace: "apps", Reason: readreason.RadarNamespaceNotCollected}
	if len(got.Coverage.NotChecked) != 1 || got.Coverage.NotChecked[0] != want {
		t.Errorf("caller's namespace uncollected: %+v; want gap %v", got.Coverage, want)
	}
}

// syncingThenReady reports ServiceMonitors as syncing on its first read and
// ready, holding a matching one, on every later read.
type syncingThenReady struct {
	cluster
	reads *int
}

func (p syncingThenReady) Catalog() *ownership.Catalog { return partlyCollected{}.Catalog() }

func (p syncingThenReady) Indexed(gk resourceid.GroupKind, index string, keys []string, scope ownership.SearchScope) IndexedRead {
	if gk.Kind != "ServiceMonitor" {
		return p.cluster.Indexed(gk, index, keys, scope)
	}
	*p.reads++
	if *p.reads == 1 {
		return IndexedRead{Gaps: []ownership.Gap{{Kind: gk, Reason: readreason.KindSyncPending}}}
	}
	return p.cluster.Indexed(gk, index, keys, scope)
}

// A kind still syncing on the answer's read keeps its gap, however the cache
// reads a moment later.
func TestAnAcrossSyncingGapIsKeptAsRead(t *testing.T) {
	svc := labelled(typed(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "web"}}, "", "v1", "Service"), map[string]string{"metrics": "on"})
	monitor := inNamespace(object("monitoring.coreos.com/v1", "ServiceMonitor", "web", map[string]any{
		"selector":          map[string]any{"matchLabels": map[string]any{"metrics": "on"}},
		"namespaceSelector": map[string]any{"any": true},
	}), "apps")
	reads := 0
	got := NewView(syncingThenReady{cluster: cluster{objects: []runtime.Object{svc, monitor}}, reads: &reads}, namespaceAccess{namespace: "apps"}).SelectedBy(Monitors, refOf(svc), svc)
	if got.Coverage.Complete() {
		t.Errorf("a syncing kind read as complete: %+v", got)
	}
}

// collectedIn holds Secrets only in the namespaces it names, in stores with
// the registered indexes: an all-namespace read reports the rest as one gap
// naming no namespace.
type collectedIn struct {
	registeredStores
	collected []string
}

func (c collectedIn) Indexed(gk resourceid.GroupKind, index string, keys []string, scope ownership.SearchScope) IndexedRead {
	if gk != kindSecret {
		return c.registeredStores.Indexed(gk, index, keys, scope)
	}
	var gaps []ownership.Gap
	held := ownership.SearchScope{Namespaces: c.collected}
	if !scope.AllNamespaces {
		held.Namespaces = nil
		for _, ns := range scope.Namespaces {
			if slices.Contains(c.collected, ns) {
				held.Namespaces = append(held.Namespaces, ns)
			} else {
				gaps = append(gaps, ownership.Gap{Kind: gk, Namespace: ns, Reason: readreason.RadarNamespaceNotCollected})
			}
		}
	} else {
		gaps = append(gaps, ownership.Gap{Kind: gk, Reason: readreason.RadarNamespaceNotCollected})
	}
	r := c.registeredStores.Indexed(gk, index, keys, held)
	r.Gaps = append(gaps, r.Gaps...)
	return r
}

// readsRecorded records each read made of a Session and how many objects
// it returned.
type readsRecorded struct {
	Session
	reads *[]recordedRead
}

type recordedRead struct {
	index   string
	keys    []string
	scope   ownership.SearchScope
	objects int
}

func (r readsRecorded) Indexed(gk resourceid.GroupKind, index string, keys []string, scope ownership.SearchScope) IndexedRead {
	read := r.Session.Indexed(gk, index, keys, scope)
	*r.reads = append(*r.reads, recordedRead{index: index, keys: keys, scope: scope, objects: len(read.Objects)})
	return read
}

// Where Radar collects a kind in only some namespaces, an across answer reads
// where the caller's namespaces are collected for the gaps alone: it reads
// none of their objects, so the index still bounds what the answer reads.
func TestAnAcrossCoverageReadReadsNoObjects(t *testing.T) {
	secret := func(namespace, name, reflects string) runtime.Object {
		m := metav1.ObjectMeta{Namespace: namespace, Name: name}
		if reflects != "" {
			m.Annotations = map[string]string{configrefs.ReflectsAnnotation: reflects}
		}
		return typed(&corev1.Secret{ObjectMeta: m}, "", "v1", "Secret")
	}
	objects := []runtime.Object{secret("team", "registry", ""), secret("team", "copy", "team/registry"), secret("shop", "registry", "team/registry")}
	for i := range 100 {
		objects = append(objects, secret("team", fmt.Sprintf("unrelated-%d", i), ""))
	}
	var reads []recordedRead
	s := readsRecorded{Session: collectedIn{registeredStores: newRegisteredStores(t, objects), collected: []string{"shop", "team"}}, reads: &reads}
	got := NewView(s, namespaceAccess{namespace: "team"}).Referrers(Reflections, refOf(objects[0]), nil)
	if len(got.Referrers) != 1 || got.Referrers[0].Ref != refOf(objects[1]) || !got.Coverage.Complete() || !got.Coverage.Narrowed {
		t.Fatalf("got %+v; want team/copy, complete and narrowed", got)
	}
	read := 0
	for _, r := range reads {
		read += r.objects
		if !r.scope.AllNamespaces && (len(r.keys) != 0 || r.objects != 0) {
			t.Errorf("the coverage read of %v passed keys %q and read %d objects; want none", r.scope.Namespaces, r.keys, r.objects)
		}
	}
	if read != 2 {
		t.Errorf("the answer read %d objects; want only the 2 the index holds for the subject", read)
	}
}
