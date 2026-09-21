package ai

import (
	"reflect"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/investigationrefs"

	"github.com/skyhook-io/radar/pkg/investigation"
)

func evidenceStep(ref string, patch func(*StepInfo)) RunEvent {
	step := &StepInfo{
		ID:            "call-1",
		Tool:          "get_resource",
		Status:        "done",
		Result:        `{"kind":"Pod"}`,
		EvidenceRef:   ref,
		RadarEvidence: true,
		IsError:       boolPointer(false),
	}
	if patch != nil {
		patch(step)
	}
	return RunEvent{Event: StreamEvent{Type: "step", Step: step}}
}

// rootCauseCitations builds the citation request the parser would produce
// for a verdict citing refs; a nil list omits the field, so the request is
// "missing" rather than empty.
func rootCauseCitations(refs []string) investigation.Citations {
	field := ""
	if refs != nil {
		quoted := make([]string, len(refs))
		for i, ref := range refs {
			quoted[i] = `"` + ref + `"`
		}
		field = `,"root_cause_evidence_refs":[` + strings.Join(quoted, ",") + `]`
	}
	return investigation.Parse("```json\n{\"root_cause\":\"x\"" + field + "}\n```").Citations
}

func bindEvidence(events []RunEvent, citations investigation.Citations, scope string) *investigation.RootCauseEvidence {
	issued := make(investigationrefs.Records)
	for _, event := range events {
		if step := event.Event.Step; step != nil && step.EvidenceRef != "" {
			issued[step.EvidenceRef] = step.Result
		}
	}
	return bindEvidenceWithIssued(events, citations, scope, issued)
}

func bindEvidenceWithIssued(
	events []RunEvent,
	citations investigation.Citations,
	scope string,
	issued investigationrefs.Records,
) *investigation.RootCauseEvidence {
	run := &Run{events: events}
	diagnosis := Diagnosis{
		Verdict:        investigation.Verdict{RootCause: "The image tag is invalid."},
		citations:      citations,
		evidenceScope:  scope,
		issuedEvidence: issued,
	}
	run.mu.Lock()
	run.bindRootCauseEvidenceLocked(&diagnosis)
	run.mu.Unlock()
	return diagnosis.RootCauseEvidence
}

func TestBindRootCauseEvidenceLinksUpgradeReadinessAlongsideResource(t *testing.T) {
	scope := strings.Repeat("a", 26)
	resourceRef := "ev_" + scope + "_" + strings.Repeat("b", 26)
	upgradeRef := "ev_" + scope + "_" + strings.Repeat("c", 26)
	events := []RunEvent{
		{Event: StreamEvent{Type: "turn"}},
		evidenceStep(resourceRef, nil),
		evidenceStep(upgradeRef, func(step *StepInfo) {
			step.ID = "upgrade-call"
			step.Tool = "get_cluster_upgrade_readiness"
			step.Result = `{"verdict":"blocked"}`
		}),
	}
	got := bindEvidence(events, rootCauseCitations([]string{resourceRef, upgradeRef}), scope)
	if got == nil || got.Status != investigation.Linked || len(got.Refs) != 2 {
		t.Fatalf("registered read tool must not invalidate the citation set: %+v", got)
	}
}

func TestBindRootCauseEvidenceRequiresExactPrivateTransportIssuance(t *testing.T) {
	scope := strings.Repeat("a", 26)
	ref := "ev_" + scope + "_" + strings.Repeat("b", 26)
	events := []RunEvent{{Event: StreamEvent{Type: "turn"}}, evidenceStep(ref, nil)}
	request := rootCauseCitations([]string{ref})

	tests := []struct {
		name   string
		issued investigationrefs.Records
		want   investigation.LinkStatus
	}{
		{name: "exact issued payload", issued: investigationrefs.Records{ref: `{"kind":"Pod"}`}, want: investigation.Linked},
		{name: "invented fresh ref", issued: investigationrefs.Records{}, want: investigation.Invalid},
		{name: "payload substitution", issued: investigationrefs.Records{ref: `{"kind":"Deployment"}`}, want: investigation.Invalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := bindEvidenceWithIssued(events, request, scope, test.issued)
			if got == nil || got.Status != test.want {
				t.Fatalf("evidence = %+v, want %s", got, test.want)
			}
			if test.want != investigation.Linked && len(got.Refs) != 0 {
				t.Fatalf("invalid evidence promoted refs: %v", got.Refs)
			}
		})
	}
}

func TestBindRootCauseEvidenceLinksOnlyCurrentCompleteSuccessfulSteps(t *testing.T) {
	scope := strings.Repeat("a", 26)
	first := "ev_" + scope + "_" + strings.Repeat("b", 26)
	second := "ev_" + scope + "_" + strings.Repeat("c", 26)
	got := bindEvidence(
		[]RunEvent{
			{Event: StreamEvent{Type: "turn"}},
			{Event: StreamEvent{Type: "step", Step: &StepInfo{
				ID: "call-1", Tool: "mcp__radar__get_resource", Status: "running",
			}}},
			evidenceStep(first, func(step *StepInfo) { step.Tool = "" }),
			evidenceStep(second, func(step *StepInfo) { step.ID = "call-2" }),
		},
		rootCauseCitations([]string{second, first}),
		scope,
	)
	if got == nil || got.Status != investigation.Linked {
		t.Fatalf("evidence = %+v, want linked", got)
	}
	if len(got.Refs) != 2 || got.Refs[0] != second || got.Refs[1] != first {
		t.Fatalf("refs = %v, want model order preserved", got.Refs)
	}
}

func TestBindRootCauseEvidenceMissingAndInvalidRequests(t *testing.T) {
	scope := strings.Repeat("a", 26)
	for _, test := range []struct {
		name    string
		request investigation.Citations
		status  investigation.LinkStatus
	}{
		{name: "omitted", request: rootCauseCitations(nil), status: investigation.Missing},
		{name: "empty", request: rootCauseCitations([]string{}), status: investigation.Missing},
		{name: "parser rejected", request: investigation.Parse("```json\n{\"root_cause\":\"x\",\"root_cause_evidence_refs\":null}\n```").Citations, status: investigation.Invalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := bindEvidence([]RunEvent{{Event: StreamEvent{Type: "turn"}}}, test.request, scope)
			if got == nil || got.Status != test.status || len(got.Refs) != 0 {
				t.Fatalf("evidence = %+v, want %s with no refs", got, test.status)
			}
		})
	}
}

func TestBindRootCauseEvidenceRejectsUnverifiableRefsAsASet(t *testing.T) {
	scope := strings.Repeat("a", 26)
	validRef := "ev_" + scope + "_" + strings.Repeat("b", 26)
	otherScopeRef := "ev_" + strings.Repeat("c", 26) + "_" + strings.Repeat("d", 26)
	tests := []struct {
		name   string
		events []RunEvent
		refs   []string
	}{
		{name: "fabricated", events: []RunEvent{{Event: StreamEvent{Type: "turn"}}}, refs: []string{validRef}},
		{name: "wrong scope", events: []RunEvent{{Event: StreamEvent{Type: "turn"}}, evidenceStep(otherScopeRef, nil)}, refs: []string{otherScopeRef}},
		{
			name: "prior turn",
			events: []RunEvent{
				{Event: StreamEvent{Type: "turn"}}, evidenceStep(validRef, nil),
				{Event: StreamEvent{Type: "done"}}, {Event: StreamEvent{Type: "turn", Verify: true}},
			},
			refs: []string{validRef},
		},
		{name: "running", events: []RunEvent{{Event: StreamEvent{Type: "turn"}}, evidenceStep(validRef, func(step *StepInfo) { step.Status = "running" })}, refs: []string{validRef}},
		{name: "failed", events: []RunEvent{{Event: StreamEvent{Type: "turn"}}, evidenceStep(validRef, func(step *StepInfo) { step.IsError = boolPointer(true) })}, refs: []string{validRef}},
		{name: "unknown outcome", events: []RunEvent{{Event: StreamEvent{Type: "turn"}}, evidenceStep(validRef, func(step *StepInfo) { step.IsError = nil })}, refs: []string{validRef}},
		{name: "empty result", events: []RunEvent{{Event: StreamEvent{Type: "turn"}}, evidenceStep(validRef, func(step *StepInfo) { step.Result = " " })}, refs: []string{validRef}},
		{name: "truncated", events: []RunEvent{{Event: StreamEvent{Type: "turn"}}, evidenceStep(validRef, func(step *StepInfo) { step.Truncated = true })}, refs: []string{validRef}},
		{name: "unvalidated marker", events: []RunEvent{{Event: StreamEvent{Type: "turn"}}, evidenceStep(validRef, func(step *StepInfo) { step.RadarEvidence = false })}, refs: []string{validRef}},
		{name: "non-Radar tool", events: []RunEvent{{Event: StreamEvent{Type: "turn"}}, evidenceStep(validRef, func(step *StepInfo) { step.Tool = "mcp__grafana__query_prometheus" })}, refs: []string{validRef}},
		{name: "missing tool identity", events: []RunEvent{{Event: StreamEvent{Type: "turn"}}, evidenceStep(validRef, func(step *StepInfo) { step.Tool = "" })}, refs: []string{validRef}},
		{
			name: "conflicting correlated tool",
			events: []RunEvent{
				{Event: StreamEvent{Type: "turn"}},
				{Event: StreamEvent{Type: "step", Step: &StepInfo{ID: "call-1", Tool: "mcp__grafana__query_prometheus", Status: "running"}}},
				evidenceStep(validRef, nil),
			},
			refs: []string{validRef},
		},
		{name: "duplicate retained ref", events: []RunEvent{{Event: StreamEvent{Type: "turn"}}, evidenceStep(validRef, nil), evidenceStep(validRef, func(step *StepInfo) { step.ID = "call-2" })}, refs: []string{validRef}},
		{name: "one bad invalidates all", events: []RunEvent{{Event: StreamEvent{Type: "turn"}}, evidenceStep(validRef, nil)}, refs: []string{validRef, "ev_" + scope + "_" + strings.Repeat("e", 26)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := bindEvidence(test.events, rootCauseCitations(test.refs), scope)
			if got == nil || got.Status != investigation.Invalid || len(got.Refs) != 0 {
				t.Fatalf("evidence = %+v, want invalid with no promoted refs", got)
			}
		})
	}
}

func TestBindRootCauseEvidenceIgnoresRejectedCallbackAndRepeatedHostIDs(t *testing.T) {
	scope := strings.Repeat("a", 26)
	oldRef := "ev_" + strings.Repeat("c", 26) + "_" + strings.Repeat("d", 26)
	currentRef := "ev_" + scope + "_" + strings.Repeat("b", 26)
	run := &Run{
		status:   "stopped",
		inFlight: true,
		subs:     map[int]chan RunEvent{},
		events: []RunEvent{
			{Event: StreamEvent{Type: "turn"}},
			evidenceStep(oldRef, func(step *StepInfo) { step.ID = "reused" }),
			{Event: StreamEvent{Type: "done"}},
			{Event: StreamEvent{Type: "turn"}},
			evidenceStep(currentRef, func(step *StepInfo) { step.ID = "reused" }),
		},
	}
	rejectedRef := "ev_" + scope + "_" + strings.Repeat("e", 26)
	if run.appendStreamEvent(evidenceStep(rejectedRef, nil).Event) {
		t.Fatal("stopped run admitted a late callback")
	}

	diagnosis := Diagnosis{
		Verdict:       investigation.Verdict{RootCause: "bad tag"},
		citations:     rootCauseCitations([]string{currentRef}),
		evidenceScope: scope,
		issuedEvidence: investigationrefs.Records{
			currentRef: `{"kind":"Pod"}`,
		},
	}
	run.mu.Lock()
	run.bindRootCauseEvidenceLocked(&diagnosis)
	run.mu.Unlock()
	if diagnosis.RootCauseEvidence == nil || diagnosis.RootCauseEvidence.Status != investigation.Linked {
		t.Fatalf("same host id across turns should bind by current ref: %+v", diagnosis.RootCauseEvidence)
	}
	if diagnosis.issuedEvidence != nil || diagnosis.evidenceScope != "" || !reflect.DeepEqual(diagnosis.citations, investigation.Citations{}) {
		t.Fatalf("private binding material survived promotion: %+v", diagnosis)
	}

	diagnosis = Diagnosis{
		Verdict:       investigation.Verdict{RootCause: "bad tag"},
		citations:     rootCauseCitations([]string{rejectedRef}),
		evidenceScope: scope,
		issuedEvidence: investigationrefs.Records{
			currentRef: `{"kind":"Pod"}`,
		},
	}
	run.mu.Lock()
	run.bindRootCauseEvidenceLocked(&diagnosis)
	run.mu.Unlock()
	if diagnosis.RootCauseEvidence.Status != investigation.Invalid {
		t.Fatalf("rejected callback became evidence: %+v", diagnosis.RootCauseEvidence)
	}
}

func TestBindRootCauseEvidenceOmittedWithoutRootCause(t *testing.T) {
	diagnosis := Diagnosis{
		Verdict: investigation.Verdict{
			Healthy:           true,
			RootCauseEvidence: &investigation.RootCauseEvidence{Status: investigation.Linked, Refs: []string{"should-clear"}},
		},
	}
	run := &Run{}
	run.mu.Lock()
	run.bindRootCauseEvidenceLocked(&diagnosis)
	run.mu.Unlock()
	if diagnosis.RootCauseEvidence != nil {
		t.Fatalf("healthy result retained root-cause links: %+v", diagnosis.RootCauseEvidence)
	}
}
