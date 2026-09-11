package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/policyreports"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Per-user authorization on /api/policy/policies/{policy}.
//
// The gate this exercises is invisible without auth: canRead returns true for
// every tuple when there is no authenticated user, so a local Radar — and every
// manual check against one — runs the endpoint with authorization switched off
// entirely. These tests are the only place the withholding logic executes with
// a real user, which is how a customer runs it.
//
// What makes it worth testing at all: the response merges findings from two
// report families that are separate RBAC resources, under two different names
// depending on the subject's scope. Getting the resource name wrong authorizes
// the wrong question, and the failure is silent in the direction that matters —
// results appear that the caller was never entitled to.

// report builds a PolicyReport document in the given family. `group` selects
// the family ("wgpolicyk8s.io" or "openreports.io"); an empty namespace makes
// it the cluster-scoped kind, which is what a finding about a cluster-scoped
// subject is read from.
func report(group, namespace, policy string, subject map[string]any, result string) *unstructured.Unstructured {
	kind, version := "PolicyReport", "v1alpha2"
	if group == "openreports.io" {
		kind, version = "Report", "v1alpha1"
	}
	if namespace == "" {
		kind = "Cluster" + kind
	}
	meta := map[string]any{"name": "rep-" + group + "-" + policy}
	if namespace != "" {
		meta["namespace"] = namespace
	}
	obj := map[string]any{
		"apiVersion": group + "/" + version,
		"kind":       kind,
		"metadata":   meta,
		"scope":      subject,
		"results": []any{
			map[string]any{"policy": policy, "rule": "check", "result": result, "source": "kyverno"},
		},
	}
	u := &unstructured.Unstructured{Object: obj}
	if namespace != "" {
		u.SetNamespace(namespace)
	}
	return u
}

func podSubject(name string) map[string]any {
	return map[string]any{"apiVersion": "v1", "kind": "Pod", "name": name}
}

// publishIndex installs an index for the duration of the test.
func publishIndex(t *testing.T, reports ...*unstructured.Unstructured) {
	t.Helper()
	prev := k8s.SetTestPolicyReportIndex(policyreports.BuildIndex(reports))
	t.Cleanup(func() { k8s.SetTestPolicyReportIndex(prev) })
}

// allow seeds the SAR cache so canRead answers from it rather than reaching for
// an apiserver that no test has.
func allow(perms *auth.UserPermissions, group, resource, namespace string, ok bool) {
	perms.SetCanI("list", group, resource, namespace, ok)
}

func coverageFor(t *testing.T, env *authTestEnv, user, policy string) PolicyCoverageResponse {
	t.Helper()
	resp := env.authGet(t, "/api/policy/policies/"+policy, user, "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out PolicyCoverageResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return out
}

// A caller authorized on one family must not receive the other family's
// findings. Both carry results for the same policy, and merging them is the
// endpoint's whole job — so the filter is the only thing standing between a
// half-authorized caller and results they cannot read at the source.
func TestPolicyCoverage_WithholdsTheFamilyTheCallerCannotRead(t *testing.T) {
	env := newAuthTestServer(t)
	publishIndex(t,
		report("wgpolicyk8s.io", "app", "require-limits", podSubject("readable"), "fail"),
		report("openreports.io", "app", "require-limits", podSubject("secret"), "fail"),
	)

	perms := &auth.UserPermissions{AllowedNamespaces: []string{"app"}}
	allow(perms, "wgpolicyk8s.io", "policyreports", "app", true)
	allow(perms, "openreports.io", "reports", "app", false)
	env.srv.permCache.Set("alice", nil, perms)

	got := coverageFor(t, env, "alice", "require-limits")

	if got.WithheldByFamily != 1 {
		t.Errorf("WithheldByFamily = %d, want 1", got.WithheldByFamily)
	}
	// Withheld outcomes are dropped from the counts too — they are not the
	// caller's to see, so a total that included them would describe data the
	// response deliberately does not carry.
	if got.Counts.Fail != 1 {
		t.Errorf("Counts.Fail = %d, want 1 (only the readable family)", got.Counts.Fail)
	}
	for _, rule := range got.Rules {
		for _, s := range rule.Subjects {
			if s.Name == "secret" {
				t.Errorf("subject from the unreadable family was returned: %+v", s)
			}
		}
	}
	if len(got.UnreadableFamilies) != 1 || got.UnreadableFamilies[0] != "openreports.io" {
		t.Errorf("UnreadableFamilies = %v, want [openreports.io]", got.UnreadableFamilies)
	}
}

// Neither family readable in the subject's namespace: nothing from it may be
// counted, listed, or implied. The namespace is reported as withheld so the
// smaller number is explained rather than passed off as the whole truth.
func TestPolicyCoverage_WithholdsANamespaceItCannotReadAtAll(t *testing.T) {
	env := newAuthTestServer(t)
	publishIndex(t,
		report("wgpolicyk8s.io", "app", "require-limits", podSubject("mine"), "pass"),
		report("wgpolicyk8s.io", "other", "require-limits", podSubject("theirs"), "fail"),
	)

	perms := &auth.UserPermissions{AllowedNamespaces: []string{"app"}}
	allow(perms, "wgpolicyk8s.io", "policyreports", "app", true)
	allow(perms, "openreports.io", "reports", "app", false)
	allow(perms, "wgpolicyk8s.io", "policyreports", "other", false)
	allow(perms, "openreports.io", "reports", "other", false)
	env.srv.permCache.Set("alice", nil, perms)

	got := coverageFor(t, env, "alice", "require-limits")

	if got.WithheldNamespaces != 1 {
		t.Errorf("WithheldNamespaces = %d, want 1", got.WithheldNamespaces)
	}
	if got.Counts.Fail != 0 {
		t.Errorf("Counts.Fail = %d, want 0 — the only failure is in a namespace this caller cannot read", got.Counts.Fail)
	}
	if got.Counts.Pass != 1 {
		t.Errorf("Counts.Pass = %d, want 1", got.Counts.Pass)
	}
	// Withheld once, reported once. The two gates are layered — a subject whose
	// namespace is unreadable would also fail the provenance filter behind it —
	// so an outcome that escapes the first and is caught by the second gets
	// counted twice, and the note tells the reader that one hidden result is
	// two, for two different reasons.
	if got.WithheldByFamily != 0 {
		t.Errorf("WithheldByFamily = %d, want 0 — this outcome is withheld by namespace, and counting it again under family double-reports it", got.WithheldByFamily)
	}
}

// A cluster-scoped subject's findings live in the cluster-scoped report kind,
// which is a different RBAC resource from the namespaced one. Authorizing the
// namespaced name asks a question whose answer says nothing about this data —
// in either direction. Here the caller may read cluster-scoped reports and is
// denied the namespaced ones, so only the resource-name split can produce the
// right answer.
func TestPolicyCoverage_AuthorizesClusterScopedSubjectsAgainstTheClusterScopedKind(t *testing.T) {
	env := newAuthTestServer(t)
	publishIndex(t,
		report("wgpolicyk8s.io", "", "require-limits",
			map[string]any{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole", "name": "admin"}, "fail"),
	)

	perms := &auth.UserPermissions{AllowedNamespaces: []string{"app"}}
	allow(perms, "wgpolicyk8s.io", "clusterpolicyreports", "", true)
	allow(perms, "openreports.io", "clusterreports", "", false)
	// Denied on the namespaced names, which must not be what this is gated on.
	allow(perms, "wgpolicyk8s.io", "policyreports", "", false)
	allow(perms, "openreports.io", "reports", "", false)
	env.srv.permCache.Set("alice", nil, perms)

	got := coverageFor(t, env, "alice", "require-limits")

	if got.Counts.Fail != 1 {
		t.Errorf("Counts.Fail = %d, want 1 — the caller can read cluster-scoped reports", got.Counts.Fail)
	}
	if got.WithheldClusterScoped {
		t.Error("WithheldClusterScoped is set for a caller who may read the cluster-scoped kind")
	}
}

// The mirror of the above: denied on the cluster-scoped kind, the finding must
// disappear even though the caller can read namespaced reports everywhere.
func TestPolicyCoverage_WithholdsClusterScopedSubjectsWhenDenied(t *testing.T) {
	env := newAuthTestServer(t)
	publishIndex(t,
		report("wgpolicyk8s.io", "", "require-limits",
			map[string]any{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "ClusterRole", "name": "admin"}, "fail"),
	)

	perms := &auth.UserPermissions{AllowedNamespaces: []string{"app"}}
	allow(perms, "wgpolicyk8s.io", "clusterpolicyreports", "", false)
	allow(perms, "openreports.io", "clusterreports", "", false)
	allow(perms, "wgpolicyk8s.io", "policyreports", "app", true)
	env.srv.permCache.Set("alice", nil, perms)

	got := coverageFor(t, env, "alice", "require-limits")

	if !got.WithheldClusterScoped {
		t.Error("WithheldClusterScoped not set for a caller denied the cluster-scoped kind")
	}
	if got.Counts.Fail != 0 {
		t.Errorf("Counts.Fail = %d, want 0 — the only finding is cluster-scoped and denied", got.Counts.Fail)
	}
}

// Authorization must not be inherited from the caller's own namespace. A
// ClusterPolicy spans namespaces, and asking the question once against "" would
// gate every finding on cluster-scoped permission — withholding results in the
// caller's own namespace that they are entitled to see.
func TestPolicyCoverage_AuthorizesPerSubjectNamespaceNotPerPolicy(t *testing.T) {
	env := newAuthTestServer(t)
	publishIndex(t,
		report("wgpolicyk8s.io", "app", "require-limits", podSubject("mine"), "fail"),
		report("wgpolicyk8s.io", "other", "require-limits", podSubject("theirs"), "fail"),
	)

	perms := &auth.UserPermissions{AllowedNamespaces: []string{"app"}}
	allow(perms, "wgpolicyk8s.io", "policyreports", "app", true)
	allow(perms, "openreports.io", "reports", "app", false)
	allow(perms, "wgpolicyk8s.io", "policyreports", "other", false)
	allow(perms, "openreports.io", "reports", "other", false)
	// Cluster-scoped denied, which must not remove the namespace they can read.
	allow(perms, "wgpolicyk8s.io", "clusterpolicyreports", "", false)
	allow(perms, "openreports.io", "clusterreports", "", false)
	env.srv.permCache.Set("alice", nil, perms)

	got := coverageFor(t, env, "alice", "require-limits")

	if got.Counts.Fail != 1 {
		t.Fatalf("Counts.Fail = %d, want 1 — the caller's own namespace must survive", got.Counts.Fail)
	}
	var names []string
	for _, rule := range got.Rules {
		for _, s := range rule.Subjects {
			names = append(names, s.Namespace+"/"+s.Name)
		}
	}
	if len(names) != 1 || names[0] != "app/mine" {
		t.Errorf("subjects = %v, want [app/mine]", names)
	}
}

// One resource, two rules. The outcome counts say two; the resource count says
// one. Anything worded "resources" reads the second — a headline built from the
// first invents a resource that does not exist, and the consequence sentence
// promises rejection for a resource that is not there.
func TestPolicyCoverage_CountsResourcesApartFromChecks(t *testing.T) {
	env := newAuthTestServer(t)
	// A second rule's outcome about the SAME subject, in the same family.
	second := report("wgpolicyk8s.io", "app", "two-rules", podSubject("only-one"), "pass")
	second.Object["metadata"] = map[string]any{"name": "rep-second", "namespace": "app"}
	second.Object["results"].([]any)[0].(map[string]any)["rule"] = "other-check"
	second.SetNamespace("app")
	publishIndex(t,
		report("wgpolicyk8s.io", "app", "two-rules", podSubject("only-one"), "fail"),
		second,
	)

	perms := &auth.UserPermissions{AllowedNamespaces: []string{"app"}}
	allow(perms, "wgpolicyk8s.io", "policyreports", "app", true)
	allow(perms, "openreports.io", "reports", "app", false)
	env.srv.permCache.Set("alice", nil, perms)

	got := coverageFor(t, env, "alice", "two-rules")

	if got.Examined != 2 {
		t.Errorf("Examined = %d, want 2 — two checks ran", got.Examined)
	}
	if got.Subjects != 1 {
		t.Errorf("Subjects = %d, want 1 — both checks are about the same resource", got.Subjects)
	}
	if got.SubjectsFailing != 1 {
		t.Errorf("SubjectsFailing = %d, want 1", got.SubjectsFailing)
	}
}

// A resource that fails nothing must not be counted among the failing ones,
// however many rules looked at it.
func TestPolicyCoverage_FailingResourceCountExcludesPassingOnes(t *testing.T) {
	env := newAuthTestServer(t)
	publishIndex(t,
		report("wgpolicyk8s.io", "app", "mixed", podSubject("bad"), "fail"),
		func() *unstructured.Unstructured {
			u := report("wgpolicyk8s.io", "app", "mixed", podSubject("good"), "pass")
			u.Object["metadata"] = map[string]any{"name": "rep-good", "namespace": "app"}
			u.SetNamespace("app")
			return u
		}(),
	)

	perms := &auth.UserPermissions{AllowedNamespaces: []string{"app"}}
	allow(perms, "wgpolicyk8s.io", "policyreports", "app", true)
	allow(perms, "openreports.io", "reports", "app", false)
	env.srv.permCache.Set("alice", nil, perms)

	got := coverageFor(t, env, "alice", "mixed")

	if got.Subjects != 2 {
		t.Errorf("Subjects = %d, want 2", got.Subjects)
	}
	if got.SubjectsFailing != 1 {
		t.Errorf("SubjectsFailing = %d, want 1 — only one of the two resources failed", got.SubjectsFailing)
	}
}

// "Failing" and "did not pass" are different sets. The count feeds a sentence
// that says the next update to these resources will be REJECTED, and a rule
// that errored or was skipped rejects nothing — it reached no verdict at all.
func TestPolicyCoverage_OnlyRealFailuresCountAsFailingResources(t *testing.T) {
	env := newAuthTestServer(t)
	reports := []*unstructured.Unstructured{}
	for i, result := range []string{"error", "skip", "warn"} {
		u := report("wgpolicyk8s.io", "app", "no-verdict", podSubject("subject-"+result), result)
		u.Object["metadata"] = map[string]any{"name": "rep-" + result, "namespace": "app"}
		u.SetNamespace("app")
		_ = i
		reports = append(reports, u)
	}
	publishIndex(t, reports...)

	perms := &auth.UserPermissions{AllowedNamespaces: []string{"app"}}
	allow(perms, "wgpolicyk8s.io", "policyreports", "app", true)
	allow(perms, "openreports.io", "reports", "app", false)
	env.srv.permCache.Set("alice", nil, perms)

	got := coverageFor(t, env, "alice", "no-verdict")

	if got.Subjects != 3 {
		t.Errorf("Subjects = %d, want 3", got.Subjects)
	}
	if got.SubjectsFailing != 0 {
		t.Errorf("SubjectsFailing = %d, want 0 — an error, a skip and a warning are not failures, and the consequence sentence promises rejection", got.SubjectsFailing)
	}
}
