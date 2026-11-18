package issues

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func readyFalse(name, reason string, since time.Duration) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": name, "namespace": "agents"},
		"status": map[string]any{"conditions": []any{map[string]any{
			"type": "Ready", "status": "False", "reason": reason, "message": reason + " message",
			"lastTransitionTime": time.Now().Add(-since).UTC().Format(time.RFC3339),
		}}},
	}}
}

func withCondition(u *unstructured.Unstructured, condType, status, reason string, since time.Duration) *unstructured.Unstructured {
	conds := u.Object["status"].(map[string]any)["conditions"].([]any)
	u.Object["status"].(map[string]any)["conditions"] = append(conds, map[string]any{
		"type": condType, "status": status, "reason": reason,
		"lastTransitionTime": time.Now().Add(-since).UTC().Format(time.RFC3339),
	})
	return u
}

func composeOne(gvr schema.GroupVersionResource, kind string, objects ...*unstructured.Unstructured) map[string]Issue {
	for _, u := range objects {
		u.SetAPIVersion(gvr.GroupVersion().String())
		u.SetKind(kind)
	}
	p := &fakeProvider{
		dynamic:    map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: objects},
		kinds:      map[schema.GroupVersionResource]string{gvr: kind},
		namespaced: map[schema.GroupVersionResource]bool{gvr: true},
	}
	byName := map[string]Issue{}
	for _, issue := range Compose(p, Filters{Limit: NoLimit}) {
		byName[issue.Name] = issue
	}
	return byName
}

func TestAgentSandboxReadyReasons(t *testing.T) {
	sandboxes := schema.GroupVersionResource{Group: agentSandboxGroup, Version: "v1beta1", Resource: "sandboxes"}
	got := composeOne(sandboxes, "Sandbox",
		withCondition(readyFalse("suspended", "SandboxSuspended", 72*time.Hour), "Suspended", "True", "PodTerminated", 72*time.Hour),
		withCondition(readyFalse("stuck-suspending", "SandboxSuspended", 2*time.Hour), "Suspended", "False", "PodTerminating", 2*time.Hour),
		withCondition(readyFalse("foreign-pod", "SandboxSuspended", time.Minute), "Suspended", "False", "PodNotOwned", time.Minute),
		// Ready kept its day-old transition time through the resume.
		withCondition(readyFalse("just-resumed", "DependenciesNotReady", 24*time.Hour), "Suspended", "False", "NotSuspended", 2*time.Minute),
		readyFalse("expired", "SandboxExpired", 72*time.Hour),
		readyFalse("finished", "PodSucceeded", 72*time.Hour),
		readyFalse("provisioning", "DependenciesNotReady", 5*time.Minute),
		readyFalse("stuck", "DependenciesNotReady", 2*time.Hour),
		readyFalse("failed", "PodFailed", time.Minute),
		readyFalse("erroring", "ReconcilerError", time.Minute),
		readyFalse("invalid", "InvalidConfiguration", time.Minute),
	)
	// A suspension's start is unknown, and a Pod that never terminates is reported
	// as a stuck deletion instead.
	for _, quiet := range []string{"suspended", "stuck-suspending", "expired", "finished", "provisioning", "just-resumed"} {
		if issue, ok := got[quiet]; ok {
			t.Errorf("%s: unexpected issue %q", quiet, issue.Reason)
		}
	}
	// A terminal failure is critical (agentsandbox.Failed, which the workspace
	// shares); a stall, a retried controller error and a refused suspension
	// stay warnings.
	for loud, severity := range map[string]Severity{"stuck": SeverityWarning, "failed": SeverityCritical, "erroring": SeverityWarning, "invalid": SeverityCritical, "foreign-pod": SeverityWarning} {
		issue, ok := got[loud]
		if !ok {
			t.Errorf("%s: expected an issue, got %v", loud, got)
			continue
		}
		if issue.Severity != severity {
			t.Errorf("%s: severity %s, want %s", loud, issue.Severity, severity)
		}
	}
	// A refused suspension is reported at once, under the Suspended condition's reason.
	// Suspended was False before the refusal too, so its time is not the onset.
	if foreign := got["foreign-pod"]; foreign.Reason != "PodNotOwned" || !strings.HasPrefix(foreign.Message, "Suspension is blocked: ") || !strings.Contains(foreign.Message, `The conflicting Pod is "foreign-pod".`) || !foreign.OnsetUnknown {
		t.Errorf("foreign-pod = %+v", foreign)
	}
}

func TestAgentSandboxClaimReadyReasons(t *testing.T) {
	claims := schema.GroupVersionResource{Group: agentSandboxExtensionsGroup, Version: "v1beta1", Resource: "sandboxclaims"}
	got := composeOne(claims, "SandboxClaim",
		readyFalse("expired", "ClaimExpired", 72*time.Hour),
		readyFalse("binding", "AdoptionConflict", time.Minute),
		readyFalse("no-pool", "WarmPoolNotFound", time.Minute),
		readyFalse("mirrors-sandbox", "SandboxSuspended", 72*time.Hour),
		readyFalse("cold-start", "SandboxNotReady", time.Minute),
		// The claim's Sandbox reports a stuck provision under its own name.
		readyFalse("mirrors-provisioning", "DependenciesNotReady", 2*time.Hour),
		// A failure forwarded from the claim's Sandbox stays on the claim too.
		readyFalse("mirrors-failure", "PodFailed", time.Minute),
	)
	if len(got) != 2 {
		t.Fatalf("issues = %v, want no-pool and mirrors-failure", got)
	}
	// The claim's own reason is fixable, so a warning; the forwarded failure is
	// terminal, so critical, as on the Sandbox.
	if got["no-pool"].Severity != SeverityWarning || got["mirrors-failure"].Severity != SeverityCritical {
		t.Errorf("no-pool %s, mirrors-failure %s", got["no-pool"].Severity, got["mirrors-failure"].Severity)
	}
}

// Another project's Sandbox kind keeps the generic reading of Ready=False.
func TestForeignSandboxKeepsGenericConditions(t *testing.T) {
	foreign := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "sandboxes"}
	if got := composeOne(foreign, "Sandbox", readyFalse("other", "SandboxSuspended", time.Hour)); len(got) != 1 {
		t.Fatalf("issues = %v, want the generic warning", got)
	}
}

func workerPool(name string, generation, observedGeneration, desired, ready int64, status, reason string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": name, "namespace": "agents", "generation": generation},
		"spec":     map[string]any{"replicas": desired},
		"status": map[string]any{
			"replicas":      ready,
			"readyReplicas": ready,
			"sandboxClasses": []any{map[string]any{
				"name": "gvisor",
				"conditions": []any{map[string]any{
					"type": "Resolved", "status": status, "reason": reason, "message": "SandboxConfig \"gvisor-v2\" not found",
					"observedGeneration": observedGeneration, "lastTransitionTime": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339),
				}},
			}},
		},
	}}
}

func TestWorkerPoolResolvedIssues(t *testing.T) {
	pools := schema.GroupVersionResource{Group: substrateGroup, Version: "v1", Resource: "workerpools"}
	got := composeOne(pools, "WorkerPool",
		workerPool("no-workers", 2, 2, 3, 0, "False", "SandboxConfigNotFound"),
		workerPool("still-serving", 2, 2, 3, 3, "False", "SandboxConfigNotFound"),
		workerPool("stale", 3, 2, 3, 0, "False", "SandboxConfigNotFound"),
		workerPool("healthy", 2, 2, 3, 3, "True", "Resolved"),
	)
	if _, ok := got["healthy"]; ok {
		t.Fatal("a resolved pool reported an issue")
	}
	noWorkers := got["no-workers"]
	if noWorkers.Severity != SeverityCritical || noWorkers.Reason != "SandboxConfigNotFound" || !strings.HasPrefix(noWorkers.Message, "Sandbox class gvisor: ") {
		t.Errorf("no-workers = %+v", noWorkers)
	}
	if got["still-serving"].Severity != SeverityWarning {
		t.Errorf("still-serving severity = %v, want warning", got["still-serving"].Severity)
	}
	stale := got["stale"]
	if stale.Severity != SeverityWarning || !strings.Contains(stale.Message, "Reported for generation 2; the pool is now at generation 3.") {
		t.Errorf("stale = %+v", stale)
	}
}

func TestWorkerPoolDeploymentNeverCreated(t *testing.T) {
	pools := schema.GroupVersionResource{Group: substrateGroup, Version: "v1", Resource: "workerpools"}
	withSelector := workerPool("provisioned", 2, 2, 3, 0, "True", "Resolved")
	withSelector.Object["status"].(map[string]any)["selector"] = "ate.dev/workerpool=provisioned"
	fresh := workerPool("just-resolved", 2, 2, 3, 0, "True", "Resolved")
	fresh.Object["status"].(map[string]any)["sandboxClasses"].([]any)[0].(map[string]any)["conditions"].([]any)[0].(map[string]any)["lastTransitionTime"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	got := composeOne(pools, "WorkerPool",
		workerPool("rejected", 2, 2, 3, 0, "True", "Resolved"),
		withSelector,
		fresh,
		workerPool("scaled-to-zero", 2, 2, 0, 0, "True", "Resolved"),
		workerPool("stale-resolution", 3, 2, 3, 0, "True", "Resolved"),
	)
	rejected, ok := got["rejected"]
	if !ok || rejected.Reason != "DeploymentNotCreated" || rejected.Severity != SeverityCritical {
		t.Fatalf("rejected = %+v, want a critical DeploymentNotCreated", rejected)
	}
	if !regexp.MustCompile(`every sandbox class resolved \d+[smhd] ago\.`).MatchString(rejected.Message) {
		t.Errorf("message = %q, want the time since resolution as a short age", rejected.Message)
	}
	for _, quiet := range []string{"provisioned", "just-resolved", "scaled-to-zero", "stale-resolution"} {
		if issue, ok := got[quiet]; ok {
			t.Errorf("%s: unexpected issue %+v", quiet, issue)
		}
	}
}
