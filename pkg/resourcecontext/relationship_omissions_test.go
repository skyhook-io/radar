package resourcecontext

import (
	"slices"
	"strings"
	"testing"

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
	if !slices.Contains(pod, OmittedField{Field: "uses", Reason: OmittedUnavailable}) || !slices.Contains(pod, OmittedField{Field: "exposedBy", Reason: OmittedUnavailable}) {
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
