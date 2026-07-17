package ai

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestDiagnosisFromText_ParsesJSONBlock(t *testing.T) {
	text := "The pod crashloops.\n\n```json\n" +
		`{"root_cause": "bad image tag", "remediation": ["roll back"], "confidence": 0.9}` +
		"\n```"
	d := diagnosisFromText(text)
	if d.RootCause != "bad image tag" {
		t.Errorf("root cause = %q", d.RootCause)
	}
	if len(d.Remediation) != 1 || d.Remediation[0] != "roll back" {
		t.Errorf("remediation = %v", d.Remediation)
	}
	if d.Confidence == nil || *d.Confidence != 0.9 {
		t.Errorf("confidence = %v", d.Confidence)
	}
	if strings.Contains(d.Report, "```json") {
		t.Errorf("report still has the json block: %q", d.Report)
	}
}

func TestDiagnosisFromText_RecommendedIndex(t *testing.T) {
	valid := "x\n\n```json\n" +
		`{"root_cause":"r","remediation":["a","b"],"recommended_index":2}` + "\n```"
	if d := diagnosisFromText(valid); d.RecommendedIndex == nil || *d.RecommendedIndex != 2 {
		t.Errorf("recommended_index = %v, want 2", d.RecommendedIndex)
	}
	// Out of range (and the 0 = "no safe fix" sentinel) must be dropped, so the UI
	// never points Apply at a non-existent step.
	for _, bad := range []string{"0", "3", "-1"} {
		text := "x\n\n```json\n" +
			`{"root_cause":"r","remediation":["a","b"],"recommended_index":` + bad + "}\n```"
		if d := diagnosisFromText(text); d.RecommendedIndex != nil {
			t.Errorf("recommended_index %s should be dropped, got %v", bad, *d.RecommendedIndex)
		}
	}
}

func TestDiagnosisFromText_ParsesHealthyAllClear(t *testing.T) {
	text := "The deployment is healthy.\n\n```json\n" +
		`{"healthy":true,"root_cause":"","remediation":[],"recommended_index":0,"confidence":0.8}` +
		"\n```"
	d := diagnosisFromText(text)
	if !d.Healthy {
		t.Fatal("healthy = false, want true")
	}
	if d.RootCause != "" {
		t.Errorf("root cause = %q, want empty", d.RootCause)
	}
	if len(d.Remediation) != 0 {
		t.Errorf("remediation = %v, want empty", d.Remediation)
	}
	if d.RecommendedIndex != nil {
		t.Errorf("recommended_index should be dropped for all-clear, got %v", *d.RecommendedIndex)
	}
}

// Verdict precedence must never produce a self-contradictory object: a concrete
// finding clears both flags; inconclusive clears healthy ("absence of evidence is
// not health"); at most one of {finding, inconclusive, healthy} survives.
func TestDiagnosisFromText_VerdictPrecedence(t *testing.T) {
	block := func(j string) string { return "prose\n\n```json\n" + j + "\n```" }

	// healthy + a real root cause → the finding wins; healthy cleared.
	d := diagnosisFromText(block(`{"healthy":true,"root_cause":"bad image","remediation":["fix it"],"recommended_index":1}`))
	if d.Healthy {
		t.Error("healthy must be cleared when a root cause is present")
	}
	if d.Inconclusive {
		t.Error("inconclusive must be cleared when a root cause is present")
	}
	if d.RootCause != "bad image" {
		t.Errorf("root cause = %q, want %q", d.RootCause, "bad image")
	}

	// healthy + inconclusive → inconclusive wins (never a false all-clear).
	d = diagnosisFromText(block(`{"healthy":true,"inconclusive":true,"root_cause":"","remediation":[]}`))
	if d.Healthy {
		t.Error("healthy must be cleared when inconclusive is set")
	}
	if !d.Inconclusive {
		t.Error("inconclusive should hold")
	}

	// inconclusive + recommended_reason carried only with a valid index (none here).
	d = diagnosisFromText(block(`{"inconclusive":true,"root_cause":"","remediation":[],"recommended_index":0,"recommended_reason":"x"}`))
	if d.RecommendedReason != "" {
		t.Errorf("recommended_reason must be empty without a valid index, got %q", d.RecommendedReason)
	}
}

func TestApplyPrompt_BindsConfirmedFix(t *testing.T) {
	fix := "Set `spec.replicas` to `3` on Deployment `x`"
	req := Request{Kind: "Deployment", Namespace: "prod", Name: "x", Fix: fix}
	p := applyPrompt(req)
	if !strings.Contains(p, fix) {
		t.Errorf("apply prompt should embed the confirmed fix; got %q", p)
	}
	if !strings.Contains(p, "Deployment prod/x") {
		t.Errorf("apply prompt should name the target resource; got %q", p)
	}
	if p := applyPrompt(Request{Kind: "Deployment", Name: "x"}); strings.Contains(p, "EXACTLY this fix") {
		t.Errorf("empty fix should use the fallback prompt; got %q", p)
	}
}

func TestTaskPrompt_HealthAwareOpening(t *testing.T) {
	healthy := taskPrompt(Request{
		Kind: "Deployment", Namespace: "prod", Name: "api",
		Health: &ResourceHealthSignal{Health: "healthy"},
	})
	for _, want := range []string{
		"Radar currently reports Deployment prod/api as healthy",
		"do not manufacture a problem",
		`"healthy": boolean`,
	} {
		if !strings.Contains(healthy, want) {
			t.Errorf("healthy prompt missing %q:\n%s", want, healthy)
		}
	}
	if strings.Contains(healthy, "Investigate the unhealthy") {
		t.Errorf("healthy prompt still uses unhealthy framing:\n%s", healthy)
	}

	broken := taskPrompt(Request{
		Kind: "Deployment", Namespace: "prod", Name: "api",
		Health: &ResourceHealthSignal{
			IssueCount: 2, HighestSeverity: "critical", TopReason: "CrashLoopBackOff",
		},
	})
	for _, want := range []string{
		"Radar currently flags 2 active issues on Deployment prod/api",
		"highest severity critical: CrashLoopBackOff",
		"Find the specific root cause",
	} {
		if !strings.Contains(broken, want) {
			t.Errorf("broken prompt missing %q:\n%s", want, broken)
		}
	}

	auditOnly := taskPrompt(Request{
		Kind: "Pod", Namespace: "prod", Name: "api-7",
		Health: &ResourceHealthSignal{
			Health: "healthy", AuditCount: 1, AuditSeverity: "warning", TopFinding: "missingResourceRequests",
		},
	})
	for _, want := range []string{
		"static posture finding",
		"not proof of a live outage",
		"Verify quickly",
	} {
		if !strings.Contains(auditOnly, want) {
			t.Errorf("audit-only prompt missing %q:\n%s", want, auditOnly)
		}
	}
}

func TestDiagnosisFromText_FreeTextIsReportNotRootCause(t *testing.T) {
	// A reply with no fenced JSON carries the prose in Report and leaves RootCause
	// empty — so the UI renders it neutrally, not under the "ROOT CAUSE" anchor.
	d := diagnosisFromText("The deployment looks healthy; nothing is wrong.")
	if d.Report == "" {
		t.Fatalf("expected free text in Report, got %q", d.Report)
	}
	if d.RootCause != "" {
		t.Errorf("free text must not become a RootCause, got %q", d.RootCause)
	}
}

// TestParseStream_FormatPin locks the claude stream-json schema we depend on,
// including the cost/turns fields on the terminal result event.
func TestParseStream_FormatPin(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"hmm"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"mcp__radar__diagnose","input":{"name":"x"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"crashloop"}]}}`,
		`{"type":"result","result":"bad tag.\n\n` + "```json\\n" + `{\"root_cause\":\"bad tag\"}` + "\\n```" + `","num_turns":2,"total_cost_usd":0.42}`,
	}, "\n")

	var running, done bool
	var thinking, doneResult string
	diag := parseStream(strings.NewReader(stream), func(ev StreamEvent) {
		switch ev.Type {
		case "thinking":
			thinking += ev.Token
		case "step":
			if ev.Step != nil && ev.Step.Status == "running" {
				running = true
				if ev.Step.Tool != "diagnose" {
					t.Errorf("tool prefix not stripped: %q", ev.Step.Tool)
				}
			}
			if ev.Step != nil && ev.Step.Status == "done" {
				done = true
				doneResult = ev.Step.Result
			}
		}
	})
	if !running || !done {
		t.Errorf("expected running+done steps; running=%v done=%v", running, done)
	}
	if thinking != "hmm" {
		t.Errorf("expected thinking event %q, got %q", "hmm", thinking)
	}
	if doneResult == "" {
		t.Errorf("expected tool result preview on done step")
	}
	if diag.RootCause != "bad tag" {
		t.Errorf("root cause not parsed: %q", diag.RootCause)
	}
	if diag.CostUSD == nil || *diag.CostUSD != 0.42 || diag.Turns != 2 {
		t.Errorf("usage not parsed: cost=%v turns=%d", diag.CostUSD, diag.Turns)
	}
}

// TestReadTools_ExcludeWrites is the fail-closed guard: the read allowlist must
// never contain a Radar write tool.
func TestReadTools_ExcludeWrites(t *testing.T) {
	writes := map[string]bool{
		"apply_resource": true, "patch_resource": true, "manage_workload": true,
		"manage_cronjob": true, "manage_node": true, "manage_gitops": true,
	}
	for _, rt := range radarReadTools {
		if writes[rt] {
			t.Errorf("write tool %q must not be in the read allowlist", rt)
		}
	}
}

// TestDetectAgents_OnlyKnownNames ensures detection never reports a binary
// outside the fixed known list (we only ever exec literal known names).
func TestDetectAgents_OnlyKnownNames(t *testing.T) {
	known := map[string]bool{}
	for _, n := range knownAgents {
		known[n] = true
	}
	for _, a := range DetectAgents(context.Background(), false) {
		if !known[a.Name] {
			t.Errorf("detected unknown agent name %q (would mean we ran an unexpected binary)", a.Name)
		}
	}
}

// TestAgentExitError_Classifies pins the best-effort error taxonomy: common
// actionable failures get a plain-language lead; the rest get a generic line.
func TestAgentExitError_Classifies(t *testing.T) {
	cases := []struct{ detail, want string }{
		{"Error: Not logged in. Please run claude login", "isn't signed in"},
		{"invalid API key", "check its API credentials"},
		{"API error 429: rate limit exceeded", "rate-limited"},
		{"overloaded_error: server is overloaded", "rate-limited"},
		{"reached max turns", "step limit"},
		{"panic: nil pointer", "stopped unexpectedly"},
	}
	for _, c := range cases {
		if got := agentExitError("claude", "claude auth login", c.detail, "").Error(); !strings.Contains(got, c.want) {
			t.Errorf("detail %q → %q, want substring %q", c.detail, got, c.want)
		}
	}
	if got := agentExitError("claude", "claude auth login", "Not logged in", "incidental warning").Error(); !strings.Contains(got, "claude auth login") {
		t.Errorf("expected sign-in command in message, got %q", got)
	}
	got := agentExitError("claude", "claude auth login", "request failed: 401 unauthorized", "provider rejected the token").Error()
	if !strings.Contains(got, "stopped unexpectedly") || !strings.Contains(got, "401 unauthorized") || !strings.Contains(got, "provider rejected the token") {
		t.Errorf("ambiguous auth failure should preserve both details, got %q", got)
	}
}

// TestClaudeResultText covers the tool_result.content shapes: a plain JSON string
// (pinned in the format test), an MCP content array, multipart text, and a raw
// JSON object passed through.
func TestClaudeResultText(t *testing.T) {
	cases := []struct{ raw, want string }{
		{`"crashloop"`, "crashloop"},                                             // JSON string content
		{`[{"type":"text","text":"hello"}]`, "hello"},                            // single content block
		{`[{"type":"text","text":"a"},{"type":"text","text":"b"}]`, "ab"},        // multipart
		{`{"apiVersion":"v1","kind":"Pod"}`, `{"apiVersion":"v1","kind":"Pod"}`}, // object → raw
	}
	for _, c := range cases {
		if got := claudeResultText([]byte(c.raw)); got != c.want {
			t.Errorf("claudeResultText(%s) = %q, want %q", c.raw, got, c.want)
		}
	}
}

func TestCapPayload(t *testing.T) {
	if s, trunc := capPayload("short"); trunc || s != "short" {
		t.Errorf("short payload should not truncate, got %q trunc=%v", s, trunc)
	}
	big := strings.Repeat("x", maxToolPayload+500)
	s, trunc := capPayload(big)
	if !trunc {
		t.Error("oversized payload should be flagged truncated")
	}
	if len([]rune(s)) > maxToolPayload+2 {
		t.Errorf("truncated payload not capped: %d runes", len([]rune(s)))
	}
}

// TestParseStream_InterleavesNarration pins the Claude treatment: interim text
// (followed by more activity) becomes an interleaved narration ("thinking") event;
// the FINAL text (the report, equal to the result) is NOT emitted as narration —
// it surfaces via the result card.
func TestParseStream_InterleavesNarration(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Let me check the deployment."}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"mcp__radar__get_resource","input":{"name":"x"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"The root cause is the bad image."}]}}`,
		`{"type":"result","result":"The root cause is the bad image.","num_turns":1}`,
	}, "\n")

	var narrations []string
	var toolSeen bool
	parseStream(strings.NewReader(stream), func(ev StreamEvent) {
		switch ev.Type {
		case "thinking":
			narrations = append(narrations, ev.Token)
		case "step":
			if ev.Step != nil && ev.Step.Status == "running" {
				toolSeen = true
			}
		}
	})

	if len(narrations) != 1 || narrations[0] != "Let me check the deployment." {
		t.Errorf("expected the interim narration interleaved, got %v", narrations)
	}
	for _, n := range narrations {
		if strings.Contains(n, "root cause") {
			t.Errorf("the final report must not appear as narration, got %q", n)
		}
	}
	if !toolSeen {
		t.Error("expected the tool step")
	}
}

// TestDiagnoseStream_NonzeroExit pins the failure-honesty contract: a nonzero
// agent exit is forgiven only when a STRUCTURED verdict parsed (the trailing
// JSON block) — free-text alone means the process died mid-stream and must
// surface as an error, never as a calm "done".
func TestDiagnoseStream_ProcessAndStreamErrors(t *testing.T) {
	mkCLI := func(t *testing.T, resultLine, exitCode string) string {
		t.Helper()
		dir := t.TempDir()
		bin := dir + "/claude"
		// printf %s, not echo — sh's echo may expand \n escapes inside the JSON.
		script := "#!/bin/sh\nprintf '%s\\n' '" + resultLine + "'\nexit " + exitCode + "\n"
		if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
		return bin
	}
	run := func(t *testing.T, bin string) (Diagnosis, error) {
		t.Helper()
		d, err := New(bin)
		if err != nil {
			t.Fatal(err)
		}
		return d.DiagnoseStream(context.Background(), Request{
			Kind: "Pod", Namespace: "ns", Name: "p", MCPPort: 1,
		}, nil)
	}

	freeText := `{"type":"result","result":"got halfway through checking the pod","num_turns":1}`
	if _, err := run(t, mkCLI(t, freeText, "3")); err == nil {
		t.Error("nonzero exit with free-text-only output must return an error")
	}

	authErr := `{"type":"result","result":"Not logged in · Please run /login","is_error":true,"num_turns":1}`
	for _, exitCode := range []string{"0", "3"} {
		_, err := run(t, mkCLI(t, authErr, exitCode))
		if err == nil {
			t.Fatalf("is_error result with exit %s must return an error", exitCode)
		}
		if !strings.Contains(err.Error(), "isn't signed in") {
			t.Errorf("auth failure with exit %s should surface the sign-in hint, got: %v", exitCode, err)
		}
	}

	emptyMaxTurnsErr := `{"type":"result","subtype":"error_max_turns","is_error":true,"num_turns":1}`
	_, err := run(t, mkCLI(t, emptyMaxTurnsErr, "0"))
	if err == nil {
		t.Fatal("is_error result without result text must return an error")
	}
	if !strings.Contains(err.Error(), "step limit") || !strings.Contains(err.Error(), "error_max_turns") {
		t.Errorf("empty max-turns error should classify and preserve its subtype, got: %v", err)
	}

	structured := "{\"type\":\"result\",\"result\":\"```json\\n{\\\"root_cause\\\":\\\"bad tag\\\",\\\"remediation\\\":[\\\"fix it\\\"]}\\n```\",\"num_turns\":1}"
	diag, err := run(t, mkCLI(t, structured, "3"))
	if err != nil {
		t.Fatalf("nonzero exit with a complete structured verdict should be forgiven, got %v", err)
	}
	if diag.RootCause != "bad tag" {
		t.Errorf("structured verdict not preserved: %q", diag.RootCause)
	}
}
