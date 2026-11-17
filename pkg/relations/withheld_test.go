package relations

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

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
