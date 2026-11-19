package relations

import (
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

type observedCluster struct{ cluster }

func (c observedCluster) Borrow(ref resourceid.Ref) (runtime.Object, ownership.ObjectRead) {
	obj, read := c.cluster.Borrow(ref)
	if read.State == ownership.Present {
		m, _ := apimeta.Accessor(obj)
		read.Object = ownership.Observe(ref, m)
	}
	return obj, read
}

func TestCaptureSubject(t *testing.T) {
	cached := deployment("web", mounts("old")).(*appsv1.Deployment)
	cached.UID, cached.ResourceVersion = "same", "1"
	cached.Spec.Template.Labels = map[string]string{"app": "old"}
	fetched := cached.DeepCopy()
	fetched.ResourceVersion = "2"
	fetched.Spec.Template.Spec = mounts("new")
	fetched.Spec.Template.Labels = map[string]string{"app": "new"}
	subject := refOf(fetched)
	c := observedCluster{cluster{objects: []runtime.Object{cached, configMap("old"), configMap("new")}}}
	s, ok := CaptureSubject(c, subject, fetched)
	if !ok {
		t.Fatal("same UID rejected")
	}
	got, _ := s.Borrow(subject)
	if got != fetched || s.Object(subject).Object.ResourceVersion != "2" {
		t.Fatal("capture not used for both reads")
	}
	v := NewView(s, ownership.Unrestricted())
	answer := v.References(ConfigReferences, subject, nil)
	if len(answer.Declared) != 1 || answer.Declared[0].Ref.Name != "new" {
		t.Fatalf("references = %+v", answer)
	}
	held, ok := v.heldSubject(subject, nil, &Coverage{})
	if !ok || held != fetched {
		t.Fatal("selectors do not read capture")
	}
	fetched.UID = "replacement"
	if _, ok := CaptureSubject(c, subject, fetched); ok {
		t.Fatal("different UID accepted")
	}
}

func TestCaptureKeepsAMissingSubjectsGap(t *testing.T) {
	cm := configMap("settings")
	subject := refOf(cm)
	pods := []runtime.Object{pod("web-1", mounts("settings"))}
	for name, c := range map[string]cluster{
		"absent":      {objects: pods},
		"unavailable": {objects: pods, unread: map[resourceid.GroupKind]readreason.Code{subject.GroupKind(): readreason.RadarForbidden}},
	} {
		s, ok := CaptureSubject(c, subject, cm)
		if !ok {
			t.Fatalf("%s: capture refused", name)
		}
		if s.Object(subject).State == ownership.Present {
			t.Fatalf("%s: a subject the cache lacks reads as present", name)
		}
		got := NewView(s, ownership.Unrestricted()).Referrers(ConfigReferences, subject, cm)
		if got.Coverage.Complete() {
			t.Fatalf("%s: consumers of a subject the cache lacks read as complete: %+v", name, got)
		}
	}
}
