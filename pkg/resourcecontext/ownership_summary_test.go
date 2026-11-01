package resourcecontext

import (
	"context"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"testing"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
)

func TestBuildDeniedOwnershipSubjectReportsRBAC(t *testing.T) {
	pod := &corev1.Pod{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}, ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "shop"}}
	rc := Build(context.Background(), pod, Options{Ownership: func(rel *topology.Relationships, _ ownership.Observation) {
		topology.OwnershipUnavailable(rel, string(readreason.UserForbidden))
	}})
	for _, note := range rc.Omitted {
		if note.Field == "managedBy" {
			if note.Reason != OmittedRBACDenied {
				t.Fatalf("reason %s", note.Reason)
			}
			return
		}
	}
	t.Fatal("missing managedBy denial")
}

func TestManagerSummaryEligibility(t *testing.T) {
	lead := resourceid.Ref{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "web"}
	present := ownership.Subject{Access: ownership.Allowed, Read: ownership.ObjectRead{State: ownership.Present}}
	for _, state := range []ownership.ManagerState{ownership.ManagerAgree, ownership.ManagerListed, ownership.ManagerShared, ownership.ManagerDisagree, ownership.ManagerUnknown} {
		t.Run(string(state), func(t *testing.T) {
			a := ownership.ManagerAnswer{Subject: present, State: state, Lead: &lead}
			want := state == ownership.ManagerAgree || state == ownership.ManagerListed || state == ownership.ManagerShared
			if got := ManagerSummary(a); (got != nil) != want {
				t.Fatalf("pointer %+v, want present=%v", got, want)
			}
			a.Lead = nil
			if got := ManagerSummary(a); got != nil {
				t.Fatalf("no lead: %+v", got)
			}
		})
	}
	for _, check := range []ownership.ClaimCheck{ownership.ClaimRecorded, ownership.ClaimOmitted, ownership.ClaimUnreadable, ownership.ClaimUnchecked, ownership.ClaimMissing, ownership.ClaimUnresolved, ownership.ClaimWithheld, ownership.ClaimRemote} {
		t.Run(string(check), func(t *testing.T) {
			claim := ownership.CheckedClaim{Check: check}
			claim.Manager = lead
			a := ownership.ManagerAnswer{Subject: present, State: ownership.ManagerClaimed, Lead: &lead, Direct: ownership.ManagerEvidence{Claims: []ownership.CheckedClaim{claim}}}
			want := check == ownership.ClaimRecorded || check == ownership.ClaimOmitted || check == ownership.ClaimUnreadable
			if got := ManagerSummary(a); (got != nil) != want {
				t.Fatalf("pointer %+v, want present=%v", got, want)
			}
			a.Direct.Claims[0].Weak = true
			if got := ManagerSummary(a); got != nil {
				t.Fatalf("inherited claim: %+v", got)
			}
			a.Direct.Claims[0].Weak = false
			a.Direct.Claims[0].Copied = true
			if got := ManagerSummary(a); got != nil {
				t.Fatalf("copied claim: %+v", got)
			}
		})
	}
	lead.Group, lead.Kind = "", "HelmRelease"
	claim := ownership.CheckedClaim{Check: ownership.ClaimNotRecording}
	claim.Manager = lead
	a := ownership.ManagerAnswer{Subject: present, State: ownership.ManagerClaimed, Lead: &lead, Direct: ownership.ManagerEvidence{Claims: []ownership.CheckedClaim{claim}}}
	if got := ManagerSummary(a); got == nil || got.Source != "helm" {
		t.Fatalf("Helm lead %+v", got)
	}
	lead.Namespace = ""
	if got := ManagerSummary(a); got != nil {
		t.Fatalf("unresolved namespace: %+v", got)
	}
}

func TestControllerSummaryKeepsRoleAndCoverage(t *testing.T) {
	ref := topology.ResourceRef{Group: "argoproj.io", Kind: "Workflow", Namespace: "jobs", Name: "run"}
	if got := ControllerSummary(ref); got.Source != "native" || got.Group != ref.Group {
		t.Fatalf("controller misclassified: %+v", got)
	}
	for _, tc := range []struct {
		chain *topology.ControllerChain
		want  OmittedReason
	}{
		{nil, OmittedUnavailable}, {&topology.ControllerChain{}, ""},
		{&topology.ControllerChain{Stop: "unknown"}, OmittedUnavailable},
		{&topology.ControllerChain{Stop: "withheld"}, OmittedRBACDenied},
	} {
		if got := ControllerOmission(tc.chain); got != tc.want {
			t.Fatalf("omission %q, want %q", got, tc.want)
		}
	}
	rc := BuildSummary(nil, SummaryOptions{Omitted: []OmittedField{{Field: "managedBy", Reason: OmittedUnavailable}}})
	if rc == nil || len(rc.Omitted) != 1 {
		t.Fatalf("missing coverage without a pointer: %+v", rc)
	}
}

func TestControllerOmissionKeepsKnownCollectionReason(t *testing.T) {
	for _, reason := range []readreason.Code{readreason.KindSyncPending, readreason.RadarKindNotCollected} {
		chain := topology.ChainOf(ownership.ControllerChainAnswer{
			Subject:             ownership.Subject{Access: ownership.Allowed, Read: ownership.ObjectRead{State: ownership.Present}},
			UnfollowedReference: &ownership.OwnerResolution{State: ownership.OwnerUnknown, Gap: ownership.Gap{Reason: reason}},
		})
		if got := ControllerOmission(chain); got != OmittedCacheCold {
			t.Fatalf("%s: %s", reason, got)
		}
	}
}
