package resourcecontext

import (
	"context"
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
)

// What a workload's spec names is listed in uses, and a target the caller may
// not read is marked, denied or unverified, instead of left out.
func TestUsesMarksUnreadableDeclaredTargets(t *testing.T) {
	ref := func(kind, name string) resourceid.Ref {
		return resourceid.Ref{Kind: kind, Namespace: "shop", Name: name}
	}
	extras := &topology.RelationshipExtras{
		Subject: resourceid.Ref{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "web"},
		Declarations: []relations.Declared{
			{Ref: ref("ConfigMap", "web-config"), State: relations.TargetPresent},
			{Ref: ref("Secret", "web-tls"), State: relations.TargetWithheld},
			{Ref: ref("Secret", "web-db"), State: relations.TargetUnread, Gap: ownership.Gap{Kind: resourceid.GroupKind{Kind: "Secret"}, Namespace: "shop", Reason: readreason.UserPermissionCheckFailed}},
		},
	}
	uses := buildUses(resourceIdentity{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "web"}, extras, newOmittedTracker())
	if uses == nil || len(uses.ConfigMaps) != 1 || uses.ConfigMaps[0].Access != "" {
		t.Fatalf("configMaps = %+v", uses)
	}
	want := []ContextRef{{Kind: "Secret", Namespace: "shop", Name: "web-tls", Access: "denied"}, {Kind: "Secret", Namespace: "shop", Name: "web-db", Access: "unverified"}}
	if len(uses.Secrets) != 2 || uses.Secrets[0] != want[0] || uses.Secrets[1] != want[1] {
		t.Fatalf("secrets = %+v, want %+v", uses.Secrets, want)
	}
}

// Without a relationship answer (caches not ready, or replaced mid-answer)
// a workload's uses are reported unknown rather than left out.
func TestUsesUnansweredIsOmittedUnavailable(t *testing.T) {
	deployment := &appsv1.Deployment{TypeMeta: metav1.TypeMeta{APIVersion: "apps/v1", Kind: "Deployment"}, ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web"}}
	rc := Build(context.Background(), deployment, Options{})
	if rc.Uses != nil || !slices.Contains(rc.Omitted, OmittedField{Field: "uses", Reason: OmittedUnavailable}) {
		t.Fatalf("uses = %+v, omitted = %+v", rc.Uses, rc.Omitted)
	}
	configMap := &corev1.ConfigMap{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"}, ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web"}}
	if rc := Build(context.Background(), configMap, Options{}); slices.ContainsFunc(rc.Omitted, func(o OmittedField) bool { return o.Field == "uses" }) {
		t.Fatalf("a kind with no pod spec reported uses: %+v", rc.Omitted)
	}
}
