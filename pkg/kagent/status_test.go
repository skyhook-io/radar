package kagent

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type vectorFinding struct {
	Severity     Severity `json:"severity"`
	Reason       string   `json:"reason"`
	Message      string   `json:"message"`
	Action       string   `json:"action"`
	KagentReason string   `json:"kagentReason"`
	Fingerprint  string   `json:"fingerprint"`
}

type vector struct {
	Name   string                    `json:"name"`
	Now    time.Time                 `json:"now"`
	Object unstructured.Unstructured `json:"object"`
	Want   struct {
		Reads    bool            `json:"reads"`
		State    State           `json:"state"`
		Tone     Tone            `json:"tone"`
		Text     string          `json:"text"`
		Summary  string          `json:"summary"`
		Egress   string          `json:"egress"`
		Findings []vectorFinding `json:"findings"`
	} `json:"want"`
}

// testdata/status.json is shared with k8s-ui's resource-utils-kagent.test.ts,
// which checks the same readings; the findings are Go's alone.
func TestStatusVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/status.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []vector
	if err := json.Unmarshal(raw, &vectors); err != nil {
		t.Fatal(err)
	}
	if len(vectors) == 0 {
		t.Fatal("no vectors")
	}
	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			r, ok := Read(&v.Object, v.Now)
			if ok != v.Want.Reads {
				t.Fatalf("reads = %v, want %v", ok, v.Want.Reads)
			}
			if !ok {
				return
			}
			if r.State != v.Want.State || r.Tone != v.Want.Tone || r.Text != v.Want.Text || r.Summary != v.Want.Summary {
				t.Errorf("got %s/%s %q %q, want %s/%s %q %q", r.State, r.Tone, r.Text, r.Summary, v.Want.State, v.Want.Tone, v.Want.Text, v.Want.Summary)
			}
			egress := ""
			if r.Egress != nil {
				egress = r.Egress.Summary
			}
			if egress != v.Want.Egress {
				t.Errorf("egress = %q, want %q", egress, v.Want.Egress)
			}
			got := r.Findings()
			if len(got) != len(v.Want.Findings) {
				t.Fatalf("findings = %+v, want %+v", got, v.Want.Findings)
			}
			for i, f := range got {
				w := v.Want.Findings[i]
				if f.Severity != w.Severity || f.Reason != w.Reason || f.Message != w.Message || f.Action != w.Action || f.KagentReason != w.KagentReason || f.Fingerprint != w.Fingerprint {
					t.Errorf("finding %d = %+v, want %+v", i, f, w)
				}
			}
		})
	}
}

// Kubernetes keeps Ready's transition time across a reason change, so a
// pending build's time is not when preparation began: however old, it is no
// Issue. A retrying build is one at once.
func TestPreparingClaimsNoDuration(t *testing.T) {
	agent := func(reason string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "api.kagent.dev/v1alpha3", "kind": "Agent",
			"metadata": map[string]any{"name": "a", "namespace": "agents", "generation": int64(1)},
			"status": map[string]any{"observedGeneration": int64(1), "desiredRevision": "abc", "conditions": []any{
				map[string]any{"type": "Ready", "status": "False", "reason": reason, "observedGeneration": int64(1), "lastTransitionTime": "2026-11-01T00:00:00Z"},
			}},
		}}
	}
	now := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	if r := ReadAgent(agent("ActorTemplatePending"), now); len(r.Findings()) != 0 || r.Tone != ToneDegraded {
		t.Fatalf("pending: %+v, want no finding", r.Findings())
	}
	if f := ReadAgent(agent("ActorTemplateRetrying"), now).Findings(); len(f) != 1 || f[0].Reason != ReasonPreparationRetrying || f[0].Severity != SeverityWarning {
		t.Fatalf("retrying: %+v, want one warning", f)
	}
}

func TestReferenceStep(t *testing.T) {
	for _, tc := range []struct{ message, want string }{
		{`resolve AgentTemplate "agents/base": not found`, "Create AgentTemplate agents/base or point the agent at an existing one."},
		{`resolve RemoteMCPServer "github": not found`, "Create RemoteMCPServer github or point the agent at an existing one."},
		{`resolve systemPromptFrom: ConfigMap "prompts" not found`, "Create ConfigMap prompts or point the agent at an existing one."},
		{`resolve model "agents/x": unsupported kind Foo`, "Fix the reference kagent names, or create the object it expects."},
	} {
		if got := referenceStep(tc.message, "agent"); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.message, got, tc.want)
		}
	}
}

func TestReportsStatus(t *testing.T) {
	for _, kind := range []string{KindAgentTemplate, KindHarness, KindEgressPolicy} {
		if ReportsStatus(kind) {
			t.Errorf("%s reports status", kind)
		}
	}
	for _, kind := range []string{KindAgent, KindSandboxTemplate, KindModelConfig, KindModelProviderConfig, KindRemoteMCPServer} {
		if !ReportsStatus(kind) {
			t.Errorf("%s does not report status", kind)
		}
	}
}
