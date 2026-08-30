package server

import (
	"net/http/httptest"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

// The policy findings attached to /api/ai/* are the same rows the UI withholds
// per report family. Two surfaces over one index must answer identically about
// who may see what — the per-resource and per-policy views already drifted once,
// and an agent surface that skips the gate is the same bug with a wider blast
// radius, since it is the surface a restricted user can drive directly.

// aiPolicyAdapter builds the lookup exactly as buildAIResourceContext does, for
// a request carrying `user`.
func aiPolicyAdapter(t *testing.T, env *authTestEnv, user, namespace string) policyReportLookupAdapter {
	t.Helper()
	r := httptest.NewRequest("GET", "/api/ai/resources/pods/"+namespace+"/web", nil)
	r = r.WithContext(auth.ContextWithUser(r.Context(), &auth.User{Username: user}))
	return policyReportLookupAdapter{
		idx:      k8s.GetPolicyReportIndex(),
		status:   k8s.GetPolicyReportStatus(),
		families: env.srv.readablePolicyReportFamilies(r, namespace),
	}
}

func TestAIResourceContext_WithholdsTheFamilyTheCallerCannotRead(t *testing.T) {
	env := newAuthTestServer(t)
	publishIndex(t,
		report("wgpolicyk8s.io", "app", "require-limits", podSubject("web"), "fail"),
		report("openreports.io", "app", "require-probes", podSubject("web"), "fail"),
	)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"app"}}
	allow(perms, "wgpolicyk8s.io", "policyreports", "app", true)
	allow(perms, "openreports.io", "reports", "app", false)
	env.srv.permCache.Set("half", nil, perms)

	got := aiPolicyAdapter(t, env, "half", "app").FindingsFor("", "Pod", "app", "web")
	if len(got) != 1 {
		t.Fatalf("findings = %d, want 1 (only the readable family): %+v", len(got), got)
	}
	if got[0].Policy != "require-limits" {
		t.Errorf("policy = %q, want require-limits — the unreadable family leaked", got[0].Policy)
	}
}

// No findings and no explanation is indistinguishable from a clean resource.
func TestAIResourceContext_SaysDeniedRatherThanClean(t *testing.T) {
	env := newAuthTestServer(t)
	publishIndex(t, report("wgpolicyk8s.io", "app", "require-limits", podSubject("web"), "fail"))
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"app"}}
	allow(perms, "wgpolicyk8s.io", "policyreports", "app", false)
	allow(perms, "openreports.io", "reports", "app", false)
	env.srv.permCache.Set("none", nil, perms)

	a := aiPolicyAdapter(t, env, "none", "app")
	if got := a.FindingsFor("", "Pod", "app", "web"); len(got) != 0 {
		t.Fatalf("findings = %d, want 0 for a caller entitled to neither family", len(got))
	}
	reason, omitted := a.Unavailable()
	if !omitted || reason != resourcecontext.OmittedRBACDenied {
		t.Errorf("omitted=%v reason=%q, want true/%q — silence reads as compliant",
			omitted, reason, resourcecontext.OmittedRBACDenied)
	}
}

// A cluster-scoped subject is authorized against the cluster-scoped report kind,
// which is a different grant. Reading the namespaced name for it asks a question
// about a resource the finding was never in.
func TestAIResourceContext_AuthorizesClusterScopedSubjectsAgainstTheClusterKind(t *testing.T) {
	env := newAuthTestServer(t)
	publishIndex(t, report("wgpolicyk8s.io", "", "require-labels",
		map[string]any{"apiVersion": "v1", "kind": "Namespace", "name": "shop"}, "fail"))
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"shop"}}
	// Granted on the namespaced kind only — which must NOT unlock the other.
	allow(perms, "wgpolicyk8s.io", "policyreports", "", true)
	allow(perms, "wgpolicyk8s.io", "clusterpolicyreports", "", false)
	allow(perms, "openreports.io", "clusterreports", "", false)
	env.srv.permCache.Set("cluster", nil, perms)

	a := aiPolicyAdapter(t, env, "cluster", "")
	if got := a.FindingsFor("", "Namespace", "", "shop"); len(got) != 0 {
		t.Fatalf("findings = %d, want 0 — authorized against the wrong resource name: %+v", len(got), got)
	}
}

func TestAIResourceContext_KeepsEverythingForAFullyAuthorizedCaller(t *testing.T) {
	env := newAuthTestServer(t)
	publishIndex(t,
		report("wgpolicyk8s.io", "app", "require-limits", podSubject("web"), "fail"),
		report("openreports.io", "app", "require-probes", podSubject("web"), "fail"),
	)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"app"}}
	allow(perms, "wgpolicyk8s.io", "policyreports", "app", true)
	allow(perms, "openreports.io", "reports", "app", true)
	env.srv.permCache.Set("full", nil, perms)

	a := aiPolicyAdapter(t, env, "full", "app")
	if got := a.FindingsFor("", "Pod", "app", "web"); len(got) != 2 {
		t.Fatalf("findings = %d, want 2: %+v", len(got), got)
	}
	if _, omitted := a.Unavailable(); omitted {
		t.Error("nothing was withheld, so nothing should be reported as omitted")
	}
}

// The half-authorized case that Unavailable cannot see: the lookup is serving
// normally, so it reports no problem, yet THIS resource's only findings are in
// the family the caller may not read. Without the per-subject signal the agent
// receives an empty policy summary and no reason for it — which is the shape of
// a resource that violates nothing.
func TestAIResourceContext_SaysSoWhenThisResourcesFindingsWereWithheld(t *testing.T) {
	env := newAuthTestServer(t)
	publishIndex(t, report("openreports.io", "app", "require-probes", podSubject("web"), "fail"))
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"app"}}
	allow(perms, "wgpolicyk8s.io", "policyreports", "app", true)
	allow(perms, "openreports.io", "reports", "app", false)
	env.srv.permCache.Set("half", nil, perms)

	a := aiPolicyAdapter(t, env, "half", "app")
	if got := a.FindingsFor("", "Pod", "app", "web"); len(got) != 0 {
		t.Fatalf("findings = %d, want 0 — the unreadable family leaked", len(got))
	}
	// Serving normally overall: the whole-lookup signal has nothing to report.
	if _, unavailable := a.Unavailable(); unavailable {
		t.Fatal("lookup reports itself unavailable; this test is no longer covering the gap it was written for")
	}
	if !a.WithheldFor("", "Pod", "app", "web") {
		t.Error("withheld=false, but this resource's only finding was unreadable — the agent is told nothing was found")
	}
}

// The opposite case must stay quiet, or every clean resource on a cluster where
// the caller lacks one family carries a false alarm.
func TestAIResourceContext_ReportsNoWithholdingForAGenuinelyCleanResource(t *testing.T) {
	env := newAuthTestServer(t)
	publishIndex(t, report("openreports.io", "app", "require-probes", podSubject("other"), "fail"))
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"app"}}
	allow(perms, "wgpolicyk8s.io", "policyreports", "app", true)
	allow(perms, "openreports.io", "reports", "app", false)
	env.srv.permCache.Set("half", nil, perms)

	if aiPolicyAdapter(t, env, "half", "app").WithheldFor("", "Pod", "app", "web") {
		t.Error("nothing about this resource was withheld; a note here is a false alarm on every clean resource")
	}
}

// A cluster with no policy engine has nothing to withhold, and the family SARs
// fail there for exactly that reason — the report kinds do not exist. Claiming
// a denial would stamp a note on every resource of every non-Kyverno cluster,
// which is the noise the status mapping already refuses to make.
func TestAIResourceContext_SaysNothingWhenThereIsNoPolicyEngine(t *testing.T) {
	a := policyReportLookupAdapter{
		status:   k8s.PolicyReportStatus{Status: k8s.KyvernoStatusNotInstalled, ReasonCode: k8s.ReasonNotInstalled},
		families: map[string]bool{},
	}
	if reason, unavailable := a.Unavailable(); unavailable {
		t.Errorf("omitted=%v reason=%q on a cluster without Kyverno, want silence", unavailable, reason)
	}
}

// The same empty family set IS a denial once the index is live, because then
// there are findings behind it.
func TestAIResourceContext_DeniesOnlyWhenThereAreFindingsToWithhold(t *testing.T) {
	a := policyReportLookupAdapter{
		status:   k8s.PolicyReportStatus{Status: k8s.KyvernoStatusReady},
		families: map[string]bool{},
	}
	reason, unavailable := a.Unavailable()
	if !unavailable || reason != resourcecontext.OmittedRBACDenied {
		t.Errorf("omitted=%v reason=%q, want true/%q", unavailable, reason, resourcecontext.OmittedRBACDenied)
	}
}
