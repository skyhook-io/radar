package relations

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/ownership"
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
