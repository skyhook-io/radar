package context

import (
	"strings"
	"testing"

	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

func TestPolicyTargetBudgetWarning(t *testing.T) {
	for _, tc := range []struct {
		name string
		rc   *resourcecontext.ResourceContext
		want bool
	}{
		{name: "no context"},
		{name: "complete", rc: &resourcecontext.ResourceContext{}},
		{name: "permission omission only", rc: &resourcecontext.ResourceContext{Omitted: []resourcecontext.OmittedField{{Field: "protects", Reason: resourcecontext.OmittedRBACDenied}}}},
		{name: "another field budget", rc: &resourcecontext.ResourceContext{Omitted: []resourcecontext.OmittedField{{Field: "referencedBy", Reason: resourcecontext.OmittedBudgetExceeded}}}},
		{name: "policy targets truncated", rc: &resourcecontext.ResourceContext{Omitted: []resourcecontext.OmittedField{{Field: "protects", Reason: resourcecontext.OmittedBudgetExceeded}}}, want: true},
		{name: "denied and unexamined targets", rc: &resourcecontext.ResourceContext{Omitted: []resourcecontext.OmittedField{{Field: "protects", Reason: resourcecontext.OmittedRBACDenied}, {Field: "protects", Reason: resourcecontext.OmittedBudgetExceeded}}}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			warning := PolicyTargetBudgetWarning(tc.rc)
			if (warning != "") != tc.want {
				t.Fatalf("warning=%q, want present=%v", warning, tc.want)
			}
			if !tc.want {
				return
			}
			for _, boundary := range []string{"incomplete", "reference or permission-check budget", "full visible target list", "list_resources", "get_resource", "resourceContext.selectedBy", "resourceContext.stagedPolicies", "networkPolicyMatchesIncomplete", "RBAC omissions", "does not produce an exhaustive target list"} {
				if !strings.Contains(warning, boundary) {
					t.Errorf("missing continuation or completeness boundary %q: %s", boundary, warning)
				}
			}
		})
	}
}
