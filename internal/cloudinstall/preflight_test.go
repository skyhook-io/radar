package cloudinstall

import (
	"errors"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestPreflightCause(t *testing.T) {
	cases := []struct {
		name string
		fill func(r *PreflightResult)
		want BlockCause
	}{
		{"nothing blocking", func(r *PreflightResult) {}, ""},
		{"only denials", func(r *PreflightResult) {
			r.blockDenied("create ClusterRole \"radar\": forbidden")
			r.blockDenied("create Deployment \"radar\": forbidden")
		}, BlockCausePermissions},
		{"a refusal outranks denials", func(r *PreflightResult) {
			r.blockDenied("create ClusterRole \"radar\": forbidden")
			r.blockRefused("create Deployment \"radar\": an object already exists but is not owned by the current Helm release")
		}, BlockCauseCluster},
		{"a refusal outranks unverifiable", func(r *PreflightResult) {
			r.blockUnverifiable("inspect rendered chart Secrets: hidden")
			r.blockRefused("map target Helm manifest to this cluster: no matches for kind")
		}, BlockCauseCluster},
		{"only unverifiable", func(r *PreflightResult) {
			r.blockUnverifiable("inspect rendered chart Secrets: hidden")
		}, BlockCauseVerification},
		{"denial outranks unverifiable", func(r *PreflightResult) {
			r.blockUnverifiable("inspect rendered chart Secrets: hidden")
			r.blockDenied("create Secret \"radar\": forbidden")
		}, BlockCausePermissions},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var r PreflightResult
			tc.fill(&r)
			if got := r.Cause(); got != tc.want {
				t.Fatalf("Cause() = %q, want %q (blocking=%v denied=%v unverifiable=%v)", got, tc.want, r.Blocking, r.Denied, r.Unverifiable)
			}
			if len(r.Denied)+len(r.Unverifiable) > len(r.Blocking) {
				t.Fatalf("subsets larger than Blocking")
			}
		})
	}
}

func TestDryRunForbiddenIsDeniedOnlyForAuthorizerPhrasing(t *testing.T) {
	var r PreflightResult
	authz := apierrors.NewForbidden(schema.GroupResource{Resource: "namespaces"}, "radar",
		errors.New(`User "dev" cannot create resource "namespaces" in API group "" at the cluster scope`))
	if err := recordMutationError(&r, "t", "create Namespace \"radar\"", authz); err != nil {
		t.Fatal(err)
	}
	admission := apierrors.NewForbidden(schema.GroupResource{Resource: "namespaces"}, "radar",
		errors.New(`admission webhook "policy.example" denied the request: namespaces must carry a team label`))
	if err := recordMutationError(&r, "t", "create Namespace \"radar\"", admission); err != nil {
		t.Fatal(err)
	}
	if len(r.Blocking) != 2 || len(r.Denied) != 1 {
		t.Fatalf("blocking=%v denied=%v", r.Blocking, r.Denied)
	}
	// One refusal among the two makes the whole result a cluster block:
	// more permission would not clear the webhook's answer.
	if got := r.Cause(); got != BlockCauseCluster {
		t.Fatalf("Cause() = %q, want cluster", got)
	}
}

func TestDryRunForbiddenRBACEscalationIsADenial(t *testing.T) {
	for _, tc := range []struct{ name, resource, msg string }{
		{"clusterrole", "clusterroles", `user "dev" (groups=["system:authenticated"]) is attempting to grant RBAC permissions not currently held: {APIGroups:[""], Resources:["secrets"], Verbs:["get"]}`},
		{"clusterrolebinding", "clusterrolebindings", `user "dev" (groups=["system:authenticated"]) is attempting to grant RBAC permissions not currently held: {APIGroups:[""], Resources:["pods"], Verbs:["list"]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var r PreflightResult
			err := apierrors.NewForbidden(schema.GroupResource{Group: "rbac.authorization.k8s.io", Resource: tc.resource}, "radar", errors.New(tc.msg))
			if err := recordMutationError(&r, "t", "create "+tc.resource, err); err != nil {
				t.Fatal(err)
			}
			if len(r.Denied) != 1 || r.Cause() != BlockCausePermissions {
				t.Fatalf("escalation refusal should be a permission denial: denied=%v cause=%q", r.Denied, r.Cause())
			}
		})
	}
}
