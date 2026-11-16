package relations

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// countingAccess denies the kinds it names and counts what it is asked.
type countingAccess struct {
	deny  string
	asked map[string]int
}

func (a *countingAccess) CanRead(gk resourceid.GroupKind, _ string) ownership.Decision {
	a.asked[gk.Kind]++
	if gk.Kind == a.deny {
		return ownership.Denied
	}
	return ownership.Allowed
}
func (*countingAccess) Namespaces() ([]string, bool) { return nil, true }

func TestDecidingFoundSkipsTheCheckWhenNothingIsThere(t *testing.T) {
	web := labelled(deployment("web", corev1.PodSpec{}), map[string]string{"app": "web"})
	session := allNamespaces{cluster: cluster{objects: []runtime.Object{web}}}

	eager := &countingAccess{asked: map[string]int{}}
	NewView(session, eager).SelectedBy(ServiceSelectors, refOf(web), nil)
	if eager.asked["Service"] != 1 {
		t.Fatalf("a View decides Services up front: asked %v", eager.asked)
	}

	found := &countingAccess{asked: map[string]int{}}
	got := NewView(session, found).DecidingFound().SelectedBy(ServiceSelectors, refOf(web), nil)
	if found.asked["Service"] != 0 || len(got.Matches) != 0 {
		t.Fatalf("no Service in the namespace should cost no check: asked %v, matches %v", found.asked, got.Matches)
	}
}

func TestDecidingFoundStillWithholdsWhatTheCallerMayNotRead(t *testing.T) {
	web := labelled(deployment("web", corev1.PodSpec{}), map[string]string{"app": "web"})
	svc := typed(&corev1.Service{ObjectMeta: meta("web"), Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "web"}}}, "", "v1", "Service")
	session := allNamespaces{cluster: cluster{objects: []runtime.Object{web, svc}}}

	denied := &countingAccess{deny: "Service", asked: map[string]int{}}
	got := NewView(session, denied).DecidingFound().SelectedBy(ServiceSelectors, refOf(web), nil)
	if denied.asked["Service"] != 1 || len(got.Matches) != 0 || !got.Coverage.Narrowed {
		t.Fatalf("a found Service must be checked and withheld: asked %v, got %+v", denied.asked, got)
	}

	allowed := &countingAccess{asked: map[string]int{}}
	got = NewView(session, allowed).DecidingFound().SelectedBy(ServiceSelectors, refOf(web), nil)
	if len(got.Matches) != 1 || got.Matches[0] != refOf(svc) {
		t.Fatalf("a readable Service should be answered: %+v", got)
	}
}
