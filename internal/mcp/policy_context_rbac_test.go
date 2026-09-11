package mcp

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/policyreports"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

// Policy findings attached to an agent's view of a resource are the same data
// the UI withholds per report family. An agent that can ask the question a
// second way must not receive what the first way held back — otherwise the
// answer depends on which surface was used, and the gate is decoration.

func policyReport(group, namespace, policy, pod, result string) *unstructured.Unstructured {
	kind, version := "PolicyReport", "v1alpha2"
	if group == "openreports.io" {
		kind, version = "Report", "v1alpha1"
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": group + "/" + version,
		"kind":       kind,
		"metadata":   map[string]any{"name": "rep-" + group, "namespace": namespace},
		"scope":      map[string]any{"apiVersion": "v1", "kind": "Pod", "name": pod},
		"results": []any{
			map[string]any{"policy": policy, "rule": "check", "result": result, "source": "kyverno"},
		},
	}}
}

func withIndex(t *testing.T, reports ...*unstructured.Unstructured) {
	t.Helper()
	prev := k8s.SetTestPolicyReportIndex(policyreports.BuildIndex(reports))
	t.Cleanup(func() { k8s.SetTestPolicyReportIndex(prev) })
}

// findings the adapter would hand the agent, gated as tools.go gates it.
func mcpFindings(t *testing.T, user, namespace, pod string, seed func(p *pkgauth.UserPermissions)) ([]resourcecontext.KyvernoFinding, resourcecontext.OmittedReason, bool) {
	t.Helper()
	ctx := withClusterAdmin(t, user)
	seed(getPermCache().Get(user, nil))
	families := k8s.ReadablePolicyReportFamilies(namespace, func(group, resource, ns string) bool {
		return canReadInNamespace(ctx, group, resource, ns, "list")
	})
	a := mcpPolicyReportLookupAdapter{
		idx:      k8s.GetPolicyReportIndex(),
		status:   k8s.GetPolicyReportStatus(),
		families: families,
	}
	reason, omitted := a.Unavailable()
	return a.FindingsFor("", "Pod", namespace, pod), reason, omitted
}

func TestMCPPolicyContext_WithholdsTheFamilyTheCallerCannotRead(t *testing.T) {
	withIndex(t,
		policyReport("wgpolicyk8s.io", "shop", "require-limits", "web", "fail"),
		policyReport("openreports.io", "shop", "require-probes", "web", "fail"),
	)

	got, _, _ := mcpFindings(t, "half", "shop", "web", func(p *pkgauth.UserPermissions) {
		p.SetCanI("list", "wgpolicyk8s.io", "policyreports", "shop", true)
		p.SetCanI("list", "openreports.io", "reports", "shop", false)
	})

	if len(got) != 1 {
		t.Fatalf("findings = %d, want 1 (only the readable family): %+v", len(got), got)
	}
	if got[0].Policy != "require-limits" {
		t.Errorf("policy = %q, want require-limits — the unreadable family leaked", got[0].Policy)
	}
}

// The dangerous shape: no findings AND no explanation reads as a resource that
// violates nothing, which is the opposite of what happened.
func TestMCPPolicyContext_SaysDeniedRatherThanClean(t *testing.T) {
	withIndex(t, policyReport("wgpolicyk8s.io", "shop", "require-limits", "web", "fail"))

	got, reason, omitted := mcpFindings(t, "none", "shop", "web", func(p *pkgauth.UserPermissions) {
		p.SetCanI("list", "wgpolicyk8s.io", "policyreports", "shop", false)
		p.SetCanI("list", "openreports.io", "reports", "shop", false)
	})

	if len(got) != 0 {
		t.Fatalf("findings = %d, want 0 for a caller entitled to neither family", len(got))
	}
	if !omitted || reason != resourcecontext.OmittedRBACDenied {
		t.Errorf("omitted=%v reason=%q, want true/%q — silence here reads as compliant",
			omitted, reason, resourcecontext.OmittedRBACDenied)
	}
}

func TestMCPPolicyContext_KeepsEverythingForAFullyAuthorizedCaller(t *testing.T) {
	withIndex(t,
		policyReport("wgpolicyk8s.io", "shop", "require-limits", "web", "fail"),
		policyReport("openreports.io", "shop", "require-probes", "web", "fail"),
	)

	got, _, omitted := mcpFindings(t, "full", "shop", "web", func(p *pkgauth.UserPermissions) {
		p.SetCanI("list", "wgpolicyk8s.io", "policyreports", "shop", true)
		p.SetCanI("list", "openreports.io", "reports", "shop", true)
	})

	if len(got) != 2 {
		t.Fatalf("findings = %d, want 2: %+v", len(got), got)
	}
	if omitted {
		t.Error("nothing was withheld, so nothing should be reported as omitted")
	}
}
