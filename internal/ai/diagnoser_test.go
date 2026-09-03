package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/investigationrefs"
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

func testEvidenceRef(scope, nonce byte) string {
	return "ev_" + strings.Repeat(string(scope), 26) + "_" + strings.Repeat(string(nonce), 26)
}

func TestDiagnosisFromText_ParsesEvidenceRequestPrivatelyAndStrictly(t *testing.T) {
	first := testEvidenceRef('a', 'b')
	second := testEvidenceRef('c', 'd')
	valid := "```json\n" +
		`{"root_cause":"bad tag","root_cause_evidence_refs":["` + first + `","` + second + `"]}` +
		"\n```"
	diagnosis := diagnosisFromText(valid)
	if diagnosis.RootCauseEvidence != nil {
		t.Fatal("untrusted model refs must not enter the public diagnosis before run binding")
	}
	if !diagnosis.evidenceRequest.present || diagnosis.evidenceRequest.invalid {
		t.Fatalf("valid request state = %+v", diagnosis.evidenceRequest)
	}
	if len(diagnosis.evidenceRequest.refs) != 2 || diagnosis.evidenceRequest.refs[0] != first || diagnosis.evidenceRequest.refs[1] != second {
		t.Fatalf("evidence refs = %v", diagnosis.evidenceRequest.refs)
	}

	invalidFields := []string{
		`null`,
		`{"ref":"` + first + `"}`,
		`["not-a-ref"]`,
		`["` + first + `","` + first + `"]`,
		`["` + first + `","` + second + `","` + testEvidenceRef('e', 'f') + `","` + testEvidenceRef('g', 'h') + `"]`,
	}
	for _, field := range invalidFields {
		text := "```json\n" + `{"root_cause":"still preserved","root_cause_evidence_refs":` + field + `}` + "\n```"
		got := diagnosisFromText(text)
		if got.RootCause != "still preserved" {
			t.Fatalf("malformed evidence discarded root cause for %s", field)
		}
		if !got.evidenceRequest.present || !got.evidenceRequest.invalid || len(got.evidenceRequest.refs) != 0 {
			t.Errorf("field %s request = %+v, want invalid with no refs", field, got.evidenceRequest)
		}
	}

	missing := diagnosisFromText("```json\n{\"root_cause\":\"old response\"}\n```")
	if missing.evidenceRequest.present || missing.evidenceRequest.invalid {
		t.Fatalf("omitted field = %+v, want missing", missing.evidenceRequest)
	}
	empty := diagnosisFromText("```json\n{\"root_cause\":\"uncited\",\"root_cause_evidence_refs\":[]}\n```")
	if !empty.evidenceRequest.present || empty.evidenceRequest.invalid || len(empty.evidenceRequest.refs) != 0 {
		t.Fatalf("empty field = %+v", empty.evidenceRequest)
	}
}

func TestSplitInvestigationEvidenceMarker(t *testing.T) {
	ref := testEvidenceRef('a', 'b')
	marker := investigationEvidenceMarkerPrefix + ref + investigationEvidenceMarkerSuffix
	clean, gotRef := splitInvestigationEvidenceMarker(marker + `[{"kind":"Pod"}]`)
	if gotRef != ref || clean != `[{"kind":"Pod"}]` {
		t.Fatalf("clean=%q ref=%q", clean, gotRef)
	}
	lookalike := `{"message":"[[radar:evidence-ref=` + ref + `]]"}`
	if clean, gotRef := splitInvestigationEvidenceMarker(lookalike); clean != lookalike || gotRef != "" {
		t.Fatalf("payload marker was trusted: clean=%q ref=%q", clean, gotRef)
	}
	malformed := investigationEvidenceMarkerPrefix + "ev_fake" + investigationEvidenceMarkerSuffix + "payload"
	if clean, gotRef := splitInvestigationEvidenceMarker(malformed); clean != malformed || gotRef != "" {
		t.Fatalf("malformed leading marker was trusted: clean=%q ref=%q", clean, gotRef)
	}
}

func TestInvestigationEvidenceValidatorRejectsSpoofedOrReplayedMarkers(t *testing.T) {
	scope := strings.Repeat("a", 26)
	refs := investigationrefs.NewRegistry()
	lease, err := refs.Begin(scope)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	ref, issued := refs.Issue(scope, `{"kind":"Pod"}`)
	if !issued {
		t.Fatal("could not issue fixture reference")
	}
	validator := investigationEvidenceValidator{
		registry: refs,
		scope:    scope,
		claimed:  make(map[string]struct{}),
	}
	event := func(ref, result string, prefilled bool) StreamEvent {
		return StreamEvent{Type: "step", Step: &StepInfo{
			ID: "call", Tool: "get_resource", Status: "done",
			EvidenceRef: ref, Result: result, RadarEvidence: prefilled,
		}}
	}

	spoofed := validator.validate(event("", `{"kind":"Pod"}`, true))
	if spoofed.Step.RadarEvidence {
		t.Fatal("adapter-authored provenance survived without a private reference")
	}
	substituted := validator.validate(event(ref, `{"kind":"Deployment"}`, true))
	if substituted.Step.RadarEvidence {
		t.Fatal("substituted payload received Radar provenance")
	}
	truncatedEvent := event(ref, `{"kind":"Pod"}`, true)
	truncatedEvent.Step.Truncated = true
	truncated := validator.validate(truncatedEvent)
	if truncated.Step.RadarEvidence {
		t.Fatal("truncated payload received Radar provenance")
	}
	legitimate := validator.validate(event(ref, `{"kind":"Pod"}`, false))
	if !legitimate.Step.RadarEvidence {
		t.Fatal("exact first private result did not receive Radar provenance")
	}
	replayed := validator.validate(event(ref, `{"kind":"Pod"}`, false))
	if replayed.Step.RadarEvidence {
		t.Fatal("a repeated marker was accepted by more than one step")
	}

	applyValidator := investigationEvidenceValidator{
		registry: refs,
		claimed:  make(map[string]struct{}),
	}
	applyEvent := applyValidator.validate(event(ref, `{"kind":"Pod"}`, true))
	if applyEvent.Step.RadarEvidence {
		t.Fatal("adapter-authored provenance survived on a write-enabled apply turn")
	}
}

type captureTurnAgent struct {
	spec       turnSpec
	refs       *investigationrefs.Registry
	issuedRef  string
	payload    string
	commandErr error
}

func (*captureTurnAgent) Name() string      { return "claude" }
func (*captureTurnAgent) Path() string      { return "printf" }
func (*captureTurnAgent) SigninCmd() string { return "claude auth login" }
func (agent *captureTurnAgent) command(ctx context.Context, spec turnSpec) (*exec.Cmd, func(), error) {
	agent.spec = spec
	u, err := url.Parse(spec.mcpURL)
	if err != nil {
		return nil, func() {}, err
	}
	agent.payload = `{"kind":"Pod","status":"Running"}`
	issuedRef, issued := agent.refs.Issue(u.Query().Get("scope"), agent.payload)
	if !issued {
		return nil, func() {}, fmt.Errorf("test agent could not issue evidence")
	}
	agent.issuedRef = issuedRef
	if agent.commandErr != nil {
		return nil, func() {}, agent.commandErr
	}
	content, _ := json.Marshal(
		investigationEvidenceMarkerPrefix + issuedRef + investigationEvidenceMarkerSuffix + agent.payload,
	)
	stream := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"radar-read","name":"mcp__radar__get_resource","input":{"kind":"Pod","namespace":"shop","name":"api"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"radar-read","content":` + string(content) + `}]}}`,
		`{"type":"result","result":"` + "```json\\n{\\\"root_cause\\\":\\\"bad tag\\\"}\\n```" + `"}`,
	}, "\n")
	return exec.CommandContext(ctx, "printf", "%s\n", stream), func() {}, nil
}

func TestDiagnoseStreamClosesEvidenceScopeOnEarlyAgentFailure(t *testing.T) {
	scope := strings.Repeat("a", 26)
	refs := investigationrefs.NewRegistry()
	agent := &captureTurnAgent{refs: refs, commandErr: errors.New("fixture command failure")}
	diagnoser := &Diagnoser{
		agents:       map[string]Agent{"claude": agent},
		defName:      "claude",
		evidenceRefs: refs,
	}
	_, err := diagnoser.DiagnoseStream(context.Background(), Request{
		Kind: "Pod", Namespace: "shop", Name: "api", MCPPort: 9280,
		EvidenceScope: scope,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "fixture command failure") {
		t.Fatalf("error = %v, want fixture command failure", err)
	}
	if refs.Active(scope) {
		t.Fatal("turn scope remained active after early agent failure")
	}
	if _, issued := refs.Issue(scope, "late payload"); issued {
		t.Fatal("failed turn accepted late evidence issuance")
	}
}
func (*captureTurnAgent) parseStream(reader io.Reader, onEvent func(StreamEvent)) Diagnosis {
	return parseStream(reader, onEvent)
}

type adapterAuthoredEvidenceAgent struct {
	event StreamEvent
}

func (*adapterAuthoredEvidenceAgent) Name() string      { return "claude" }
func (*adapterAuthoredEvidenceAgent) Path() string      { return "printf" }
func (*adapterAuthoredEvidenceAgent) SigninCmd() string { return "claude auth login" }
func (*adapterAuthoredEvidenceAgent) command(ctx context.Context, _ turnSpec) (*exec.Cmd, func(), error) {
	return exec.CommandContext(ctx, "printf", ""), func() {}, nil
}
func (agent *adapterAuthoredEvidenceAgent) parseStream(_ io.Reader, onEvent func(StreamEvent)) Diagnosis {
	onEvent(agent.event)
	return Diagnosis{RootCause: "apply completed"}
}

func TestDiagnoseStreamClearsAdapterProvenanceOnApplyTurn(t *testing.T) {
	agent := &adapterAuthoredEvidenceAgent{event: StreamEvent{Type: "step", Step: &StepInfo{
		ID: "foreign-write", Tool: "patch_resource", Status: "done",
		Result: `{"patched":true}`, EvidenceRef: testEvidenceRef('a', 'b'), RadarEvidence: true,
	}}}
	diagnoser := &Diagnoser{
		agents:  map[string]Agent{"claude": agent},
		defName: "claude",
	}
	var delivered *StepInfo
	_, err := diagnoser.DiagnoseStream(context.Background(), Request{
		Kind: "Deployment", Namespace: "shop", Name: "api", MCPPort: 9280,
		Apply: true,
	}, func(event StreamEvent) {
		if event.Step != nil {
			delivered = event.Step
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if delivered == nil || delivered.RadarEvidence {
		t.Fatalf("apply event retained adapter-authored provenance: %+v", delivered)
	}
}

func TestDiagnoseStreamUsesPerTurnScopedInvestigationMount(t *testing.T) {
	refs := investigationrefs.NewRegistry()
	agent := &captureTurnAgent{refs: refs}
	diagnoser := &Diagnoser{
		agents:       map[string]Agent{"claude": agent},
		defName:      "claude",
		evidenceRefs: refs,
	}
	scope := strings.Repeat("a", 26)
	var evidenceStep *StepInfo
	diagnosis, err := diagnoser.DiagnoseStream(context.Background(), Request{
		Kind: "Pod", Namespace: "shop", Name: "api", MCPPort: 9280,
		MCPBasePath: "/radar", EvidenceScope: scope,
	}, func(event StreamEvent) {
		if event.Step != nil && event.Step.Status == "done" {
			evidenceStep = event.Step
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	wantURL := "http://localhost:9280/radar/mcp-investigation?scope=" + scope
	if agent.spec.mcpURL != wantURL {
		t.Fatalf("mcp URL = %q, want %q", agent.spec.mcpURL, wantURL)
	}
	if diagnosis.evidenceScope != scope {
		t.Fatalf("diagnosis scope = %q, want %q", diagnosis.evidenceScope, scope)
	}
	if payload := diagnosis.issuedEvidence[agent.issuedRef]; payload != agent.payload {
		t.Fatalf("issued payload = %q, want exact %q", payload, agent.payload)
	}
	if evidenceStep == nil || !evidenceStep.RadarEvidence ||
		evidenceStep.EvidenceRef != agent.issuedRef || evidenceStep.Result != agent.payload {
		t.Fatalf("validated evidence step = %+v", evidenceStep)
	}
	if refs.Active(scope) {
		t.Fatal("turn scope remained active after DiagnoseStream returned")
	}
	if _, issued := refs.Issue(scope, "late payload"); issued {
		t.Fatal("closed turn accepted late evidence issuance")
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

// Conclusion precedence must never produce a self-contradictory object: a concrete
// finding clears both flags; inconclusive clears healthy ("absence of evidence is
// not health"); at most one of {finding, inconclusive, healthy} survives.
func TestDiagnosisFromText_ConclusionPrecedence(t *testing.T) {
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

func TestApplyPrompt_BindsImmutableAPIGroup(t *testing.T) {
	grouped := applyPrompt(Request{Kind: "Rollout", Group: "argoproj.io", Namespace: "prod", Name: "checkout"})
	for _, want := range []string{
		`immutable target API group is "argoproj.io"`,
		`Pass group="argoproj.io" to patch_resource`,
		`manifest apiVersion must belong to "argoproj.io"`,
		"Never mutate a same-named resource from another API group",
	} {
		if !strings.Contains(grouped, want) {
			t.Errorf("group-qualified apply prompt missing %q:\n%s", want, grouped)
		}
	}

	core := applyPrompt(Request{Kind: "Pod", Namespace: "prod", Name: "checkout"})
	for _, want := range []string{"Kubernetes core API group", "omit group", "apiVersion must be v1"} {
		if !strings.Contains(core, want) {
			t.Errorf("core-group apply prompt missing %q:\n%s", want, core)
		}
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
		`"root_cause_evidence_refs": [string]`,
		"[[radar:evidence-ref=ev_...]]",
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
			Health: "healthy", AuditCount: 1, AuditSeverity: "high", TopFinding: "runAsRoot",
		},
	})
	for _, want := range []string{
		"static posture finding",
		"highest severity high",
		"not evidence of an active outage",
		"Verify quickly",
	} {
		if !strings.Contains(auditOnly, want) {
			t.Errorf("audit-only prompt missing %q:\n%s", want, auditOnly)
		}
	}

	coexisting := taskPrompt(Request{
		Kind: "Deployment", Namespace: "prod", Name: "api",
		Health: &ResourceHealthSignal{
			IssueCount: 1, HighestSeverity: "critical", TopReason: "CrashLoopBackOff",
			AuditCount: 1, AuditSeverity: "high", TopFinding: "runAsRoot",
		},
	})
	for _, want := range []string{
		"highest severity critical: CrashLoopBackOff",
		"static posture finding; highest severity high: runAsRoot",
		"not evidence of an active outage",
		"Find the specific root cause",
	} {
		if !strings.Contains(coexisting, want) {
			t.Errorf("coexisting issue/audit prompt missing %q:\n%s", want, coexisting)
		}
	}
	if strings.Contains(coexisting, "Verify quickly") {
		t.Errorf("coexisting issue/audit prompt used audit-only healthy framing:\n%s", coexisting)
	}

	if !strings.Contains(broken, "Start with Radar's `diagnose` tool for this workload") {
		t.Errorf("workload prompt should prefer semantic diagnose first:\n%s", broken)
	}
	nonWorkload := taskPrompt(Request{Kind: "ConfigMap", Namespace: "prod", Name: "api"})
	if strings.Contains(nonWorkload, "`diagnose` tool") {
		t.Errorf("unsupported resource prompt must not direct the agent to semantic diagnose:\n%s", nonWorkload)
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
	ref := testEvidenceRef('a', 'b')
	stream := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"hmm"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"mcp__radar__diagnose","input":{"name":"x"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"[[radar:evidence-ref=` + ref + `]]\ncrashloop"}]}}`,
		`{"type":"result","result":"bad tag.\n\n` + "```json\\n" + `{\"root_cause\":\"bad tag\"}` + "\\n```" + `","num_turns":2,"total_cost_usd":0.42}`,
	}, "\n")

	var running, done bool
	var doneIsError *bool
	var thinking, doneResult, doneEvidenceRef string
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
				doneEvidenceRef = ev.Step.EvidenceRef
				doneIsError = ev.Step.IsError
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
	if doneEvidenceRef != ref || strings.Contains(doneResult, "radar:evidence-ref") {
		t.Errorf("marker extraction result=%q ref=%q", doneResult, doneEvidenceRef)
	}
	if doneIsError == nil || *doneIsError {
		t.Errorf("Claude's omitted tool_result.is_error must be a confirmed success, got %v", doneIsError)
	}
	if diag.RootCause != "bad tag" {
		t.Errorf("root cause not parsed: %q", diag.RootCause)
	}
	if diag.CostUSD == nil || *diag.CostUSD != 0.42 || diag.Turns != 2 {
		t.Errorf("usage not parsed: cost=%v turns=%d", diag.CostUSD, diag.Turns)
	}
}

func TestStepInfoIsErrorJSON(t *testing.T) {
	confirmedFalse, confirmedTrue := false, true
	cases := []struct {
		name      string
		isError   *bool
		wantField string
	}{
		{name: "confirmed success", isError: &confirmedFalse, wantField: `"isError":false`},
		{name: "confirmed failure", isError: &confirmedTrue, wantField: `"isError":true`},
		{name: "unknown", isError: nil, wantField: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := json.Marshal(StepInfo{ID: "t1", Status: "done", IsError: tc.isError})
			if err != nil {
				t.Fatal(err)
			}
			got := string(b)
			if tc.wantField == "" {
				if strings.Contains(got, `"isError"`) {
					t.Fatalf("unknown result must omit isError: %s", got)
				}
				return
			}
			if !strings.Contains(got, tc.wantField) {
				t.Fatalf("JSON = %s, want %s", got, tc.wantField)
			}
		})
	}
}

func TestClaudeToolResultErrorState(t *testing.T) {
	stream := strings.Join([]string{
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"ok","content":"ok"}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"bad","content":"denied","is_error":true}]}}`,
	}, "\n")

	got := map[string]*bool{}
	parseStream(strings.NewReader(stream), func(ev StreamEvent) {
		if ev.Step != nil {
			got[ev.Step.ID] = ev.Step.IsError
		}
	})
	if got["ok"] == nil || *got["ok"] {
		t.Errorf("omitted is_error = %v, want confirmed false", got["ok"])
	}
	if got["bad"] == nil || !*got["bad"] {
		t.Errorf("is_error:true = %v, want confirmed true", got["bad"])
	}
}

// TestReadTools_ExcludeWrites is the fail-closed guard: the read allowlist must
// never contain a Radar write tool.
func TestReadTools_ExcludeWrites(t *testing.T) {
	writes := map[string]bool{
		"apply_resource": true, "patch_resource": true, "manage_workload": true,
		"manage_rollout": true, "manage_cronjob": true, "manage_node": true, "manage_gitops": true,
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

func TestCapPayloadPreservesProducerBoundedDiagnoseEnvelope(t *testing.T) {
	payloadBytes, err := json.Marshal(map[string]any{
		"resource": map[string]any{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata":   map[string]any{"namespace": "shop", "name": "api-config"},
			"data":       map[string]string{"application.yaml": strings.Repeat("c", 16<<10)},
		},
		"resourceContext": map[string]any{
			"tier": "basic",
			"statusSummary": map[string]any{
				"conditions": []map[string]string{{"type": "Available", "status": "False", "message": strings.Repeat("m", 4<<10)}},
			},
		},
		"logsCurrent": []map[string]any{{
			"pod":       "api-abc",
			"container": "api",
			"logs": map[string]any{
				"lines":        []string{strings.Repeat("l", (32<<10)-1)},
				"totalLines":   1,
				"matchedLines": 1,
				"fallback":     false,
			},
		}},
		"events": []map[string]any{{
			"reason":  "BackOff",
			"message": strings.Repeat("e", 4<<10),
			"type":    "Warning",
		}},
	})
	if err != nil {
		t.Fatalf("marshal diagnose envelope: %v", err)
	}
	payload := string(payloadBytes)
	if len([]rune(payload)) <= 32<<10 {
		t.Fatalf("fixture must exceed the former transcript cap, got %d runes", len([]rune(payload)))
	}
	if len([]rune(payload)) > maxToolPayload {
		t.Fatalf("producer-bounded fixture exceeds transcript cap: %d > %d runes", len([]rune(payload)), maxToolPayload)
	}

	got, truncated := capPayload(payload)
	if truncated {
		t.Fatal("bounded diagnose evidence envelope was unexpectedly truncated")
	}
	if got != payload {
		t.Fatal("bounded diagnose evidence envelope changed")
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
// agent exit is forgiven only when a STRUCTURED conclusion parsed (the trailing
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
		refs := investigationrefs.NewRegistry()
		d, err := New(bin, refs)
		if err != nil {
			t.Fatal(err)
		}
		scope := strings.Repeat("a", 26)
		diagnosis, diagnoseErr := d.DiagnoseStream(context.Background(), Request{
			Kind: "Pod", Namespace: "ns", Name: "p", MCPPort: 1,
			EvidenceScope: scope,
		}, nil)
		if refs.Active(scope) {
			t.Fatal("turn scope leaked after DiagnoseStream exit")
		}
		return diagnosis, diagnoseErr
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
		t.Fatalf("nonzero exit with a complete structured conclusion should be forgiven, got %v", err)
	}
	if diag.RootCause != "bad tag" {
		t.Errorf("structured conclusion not preserved: %q", diag.RootCause)
	}
}
