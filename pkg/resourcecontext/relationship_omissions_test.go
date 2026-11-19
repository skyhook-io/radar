package resourcecontext

import (
	"context"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
)

// Each context field is marked from the evidence it lists: uses from the
// subject's own declarations, never from what was read about their targets,
// and referencedBy from the references that name the subject. A field the
// subject's kind doesn't have is never marked.
func TestRelationshipOmissionsFollowEachFieldsEvidence(t *testing.T) {
	notCollected := relations.Coverage{NotChecked: []ownership.Gap{{Kind: resourceid.GroupKind{Kind: "Secret"}, Namespace: "shop", Reason: readreason.RadarKindNotCollected}}}
	omissions := func(kind, group string, fields map[string]relations.Coverage) []OmittedField {
		omitted := newOmittedTracker()
		addRelationshipOmissions(resourceIdentity{Group: group, Kind: kind, Namespace: "shop", Name: "x"}, &topology.RelationshipExtras{Fields: fields}, omitted)
		return omitted.items
	}
	targetSide := map[string]relations.Coverage{"ConfigRefs": notCollected, "StorageRefs": notCollected, "ServiceAccount": {Narrowed: true}, "Consumers": notCollected}
	if got := omissions("Deployment", "apps", targetSide); len(got) != 0 {
		t.Fatalf("what wasn't read about the targets marked context fields: %+v", got)
	}

	var uses []string
	for _, o := range omissions("Deployment", "apps", map[string]relations.Coverage{"Uses": notCollected}) {
		if o.Reason == OmittedUnavailable {
			uses = append(uses, o.Field)
		}
	}
	if want := []string{"uses.configMaps", "uses.secrets", "uses.pvcs", "uses.serviceAccount"}; !slices.Equal(uses, want) {
		t.Fatalf("declarations that couldn't be read marked %v, want %v", uses, want)
	}
	for _, kind := range [][2]string{{"Ingress", "networking.k8s.io"}, {"ServiceAccount", ""}} {
		if got := omissions(kind[0], kind[1], map[string]relations.Coverage{"Uses": notCollected}); len(got) != 0 {
			t.Fatalf("%s has no uses block but was marked: %+v", kind[0], got)
		}
	}

	if got := omissions("Deployment", "apps", map[string]relations.Coverage{"ReferencedBy": notCollected}); len(got) != 0 {
		t.Fatalf("a Deployment has no referencedBy but was marked: %+v", got)
	}
	if got := omissions("Secret", "", map[string]relations.Coverage{"ReferencedBy": notCollected}); !slices.Equal(got, []OmittedField{{Field: "referencedBy", Reason: OmittedUnavailable}}) {
		t.Fatalf("Secret referencedBy = %+v", got)
	}

	unanswered := map[string]relations.Coverage{"answer": {NotChecked: []ownership.Gap{{Reason: readreason.ClusterConnecting}}}}
	pod := omissions("Pod", "", unanswered)
	if !slices.Contains(pod, OmittedField{Field: "uses", Reason: OmittedUnavailable}) {
		t.Fatalf("a Pod without an answer = %+v", pod)
	}
	if slices.ContainsFunc(pod, func(o OmittedField) bool { return o.Field == "runsOn" || o.Field == "referencedBy" }) {
		t.Fatalf("a Pod without an answer marked a field its spec answers or it doesn't have: %+v", pod)
	}

	for _, o := range omissions("Ingress", "networking.k8s.io", map[string]relations.Coverage{"subject": {SubjectState: "replaced"}}) {
		if strings.HasPrefix(o.Field, "uses.") || o.Field == "referencedBy" || o.Field == "runsOn" {
			t.Fatalf("an Ingress's replaced subject marked %s", o.Field)
		}
	}
}

// A Service routed from VirtualServices Radar can't read is unknown on what
// exposes it, but has no uses to mark: the family's gap also covers the
// configuration a readable DestinationRule fills, which isn't uses.
func TestServiceWithUnreadableVirtualServicesMarksNoUses(t *testing.T) {
	ctx := context.Background()
	svc := &corev1.Service{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"}, ObjectMeta: metav1.ObjectMeta{Name: "reviews", Namespace: "bookinfo"}}
	rule := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "networking.istio.io/v1", "kind": "DestinationRule",
		"metadata": map[string]any{"name": "reviews", "namespace": "bookinfo"},
		"spec":     map[string]any{"host": "reviews"},
	}}
	virtualServices := resourceid.GroupKind{Group: "networking.istio.io", Kind: "VirtualService"}
	subject := resourceid.NewRef("", "Service", "bookinfo", "reviews")
	session := newContextFixtureSession(subject, svc, nil)
	session.objects = []runtime.Object{rule}
	session.gaps = map[resourceid.GroupKind]ownership.Gap{virtualServices: {Kind: virtualServices, Reason: readreason.RadarKindNotCollected}}
	access := fixtureAccess{ctx: ctx}
	rel, extras := topology.RelationsContextAnswer(session, access, access, ownership.NewView(session, access), subject, svc, nil, nil)
	if len(rel.ConfigRefs) != 1 || rel.ConfigRefs[0].Kind != "DestinationRule" || len(extras.Fields["ConfigRefs"].NotChecked) == 0 {
		t.Fatalf("fixture lost its shape: configRefs %+v, fields %+v", rel.ConfigRefs, extras.Fields)
	}
	rc := Build(ctx, svc, Options{Relationships: rel, RelationshipExtras: extras})
	if !slices.Contains(rc.Omitted, OmittedField{Field: "exposedBy", Reason: OmittedUnavailable}) {
		t.Fatalf("exposedBy not marked: %+v", rc.Omitted)
	}
	for _, o := range rc.Omitted {
		if strings.HasPrefix(o.Field, "uses") {
			t.Fatalf("a Service's context marked %s: %+v", o.Field, rc.Omitted)
		}
	}
}

// Without an answer, a context marks unavailable only the fields its kind can
// have: those some relation family, or the answer's own reads, fill for it.
func TestUnansweredContextMarksOnlyItsKindsFields(t *testing.T) {
	unanswered := map[string]relations.Coverage{"answer": {NotChecked: []ownership.Gap{{Reason: readreason.ClusterConnecting}}}}
	for _, tc := range []struct {
		kind, group string
		want        []string
	}{
		{"ConfigMap", "", []string{"referencedBy"}},
		{"Service", "", []string{"exposes", "exposedBy", "backends", "monitors"}},
		{"Deployment", "apps", []string{"exposes", "exposedBy", "selectedBy", "scaledBy", "monitors", "stagedPolicies", "uses"}},
		{"Node", "", []string{"referencedBy"}},
	} {
		omitted := newOmittedTracker()
		addRelationshipOmissions(resourceIdentity{Group: tc.group, Kind: tc.kind, Namespace: "shop", Name: "x"}, &topology.RelationshipExtras{Fields: unanswered}, omitted)
		var got []string
		for _, o := range omitted.items {
			if o.Reason != OmittedUnavailable {
				t.Fatalf("%s: %+v", tc.kind, o)
			}
			got = append(got, o.Field)
		}
		slices.Sort(got)
		if want := slices.Sorted(slices.Values(tc.want)); !slices.Equal(got, want) {
			t.Errorf("%s without an answer marked %v, want %v", tc.kind, got, want)
		}
	}
}
