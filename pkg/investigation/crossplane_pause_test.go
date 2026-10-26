package investigation

import (
	"strings"
	"testing"

	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

func TestTaskPromptCrossplanePauseIndependentFromHealth(t *testing.T) {
	for _, pause := range []resourcecontext.CrossplanePauseState{resourcecontext.CrossplanePauseRequested, resourcecontext.CrossplanePauseAcknowledged} {
		t.Run(string(pause), func(t *testing.T) {
			h := &HealthSignal{CrossplanePause: pause, Health: "healthy"}
			target := Target{Kind: "Bucket", Group: "example.org", Namespace: "infra", Name: "archive"}
			prompt := TaskPrompt(target, h, Options{})
			for _, want := range []string{"availability as healthy", "crossplane.io/paused", "Verify quickly", "do not manufacture a problem"} {
				if !strings.Contains(prompt, want) {
					t.Fatalf("missing %q: %s", want, prompt)
				}
			}
			if strings.Contains(prompt, "currently reports Bucket infra/archive as healthy") || healthIndicatesProblem(h) {
				t.Fatal("pause escalated or unqualified current health")
			}
			if pause == resourcecontext.CrossplanePauseRequested {
				if !strings.Contains(prompt, "no Synced=False/ReconcilePaused acknowledgement is recorded") || strings.Contains(prompt, "while reconciliation is paused") {
					t.Fatal("request treated as completed pause")
				}
			} else if !strings.Contains(prompt, "corroborated by its recorded Synced=False/ReconcilePaused condition") || !strings.Contains(prompt, "not a fresh external check") {
				t.Fatal("ack source/freshness missing")
			}
			h.Health = "unhealthy"
			if !healthIndicatesProblem(h) {
				t.Fatal("independent unhealthy state hidden")
			}
			h.Health = "healthy"
			h.IssueCount = 1
			h.HighestSeverity = "warning"
			h.TopReason = "ReconcileError"
			h.Issues = []HealthLine{{Severity: "warning", Reason: "ReconcileError", Message: "access denied"}}
			h.AuditCount = 1
			h.AuditFindings = []HealthLine{{Severity: "high", Reason: "runAsRoot", Message: "container runs as root"}}
			prompt = TaskPrompt(target, h, Options{})
			for _, want := range []string{"1 active issue", "ReconcileError: access denied", "container runs as root", "Find the specific root cause", "crossplane.io/paused"} {
				if !strings.Contains(prompt, want) {
					t.Fatalf("independent evidence lost %q: %s", want, prompt)
				}
			}
			if !healthIndicatesProblem(h) {
				t.Fatal("independent issue hidden")
			}
		})
	}
}
