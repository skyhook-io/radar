package resourcecontext

import (
	"testing"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
)

// What a workload's spec names is listed in uses, and a target the caller may
// not read is marked, denied or unverified, instead of left out.
func TestUsesMarksUnreadableDeclaredTargets(t *testing.T) {
	ref := func(kind, name string) resourceid.Ref { return resourceid.Ref{Kind: kind, Namespace: "shop", Name: name} }
	extras := &topology.RelationshipExtras{
		Subject: resourceid.Ref{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "web"},
		Declarations: []relations.Declared{
			{Ref: ref("ConfigMap", "web-config"), State: relations.TargetPresent},
			{Ref: ref("Secret", "web-tls"), State: relations.TargetWithheld},
			{Ref: ref("Secret", "web-db"), State: relations.TargetUnread, Gap: ownership.Gap{Kind: resourceid.GroupKind{Kind: "Secret"}, Namespace: "shop", Reason: readreason.UserPermissionCheckFailed}},
		},
	}
	uses := buildUses(extras)
	if uses == nil || len(uses.ConfigMaps) != 1 || uses.ConfigMaps[0].Access != "" {
		t.Fatalf("configMaps = %+v", uses)
	}
	want := []ContextRef{{Kind: "Secret", Namespace: "shop", Name: "web-tls", Access: "denied"}, {Kind: "Secret", Namespace: "shop", Name: "web-db", Access: "unverified"}}
	if len(uses.Secrets) != 2 || uses.Secrets[0] != want[0] || uses.Secrets[1] != want[1] {
		t.Fatalf("secrets = %+v, want %+v", uses.Secrets, want)
	}
}
