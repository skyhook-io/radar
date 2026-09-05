package ai

import (
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/investigationrefs"
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

func bindEvidence(events []RunEvent, request evidenceReferenceRequest, scope string) *RootCauseEvidence {
	issued := make(investigationrefs.Records)
	for _, event := range events {
		if step := event.Event.Step; step != nil && step.EvidenceRef != "" {
			issued[step.EvidenceRef] = step.Result
		}
	}
	return bindEvidenceWithIssued(events, request, scope, issued)
}

func bindEvidenceWithIssued(
	events []RunEvent,
	request evidenceReferenceRequest,
	scope string,
	issued investigationrefs.Records,
) *RootCauseEvidence {
	run := &Run{events: events}
	diagnosis := Diagnosis{
		RootCause:       "The image tag is invalid.",
		evidenceRequest: request,
		evidenceScope:   scope,
		issuedEvidence:  issued,
	}
	run.mu.Lock()
	run.bindRootCauseEvidenceLocked(&diagnosis)
	run.mu.Unlock()
	return diagnosis.RootCauseEvidence
}

func TestBindRootCauseEvidenceRequiresExactPrivateTransportIssuance(t *testing.T) {
	scope := strings.Repeat("a", 26)
	ref := "ev_" + scope + "_" + strings.Repeat("b", 26)
	events := []RunEvent{{Event: StreamEvent{Type: "turn"}}, evidenceStep(ref, nil)}
	request := evidenceReferenceRequest{present: true, refs: []string{ref}}

	tests := []struct {
		name   string
		issued investigationrefs.Records
		want   EvidenceLinkStatus
	}{
		{name: "exact issued payload", issued: investigationrefs.Records{ref: `{"kind":"Pod"}`}, want: EvidenceLinked},
		{name: "invented fresh ref", issued: investigationrefs.Records{}, want: EvidenceInvalid},
		{name: "payload substitution", issued: investigationrefs.Records{ref: `{"kind":"Deployment"}`}, want: EvidenceInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := bindEvidenceWithIssued(events, request, scope, test.issued)
			if got == nil || got.Status != test.want {
				t.Fatalf("evidence = %+v, want %s", got, test.want)
			}
			if test.want != EvidenceLinked && len(got.Refs) != 0 {
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
		evidenceReferenceRequest{present: true, refs: []string{second, first}},
		scope,
	)
	if got == nil || got.Status != EvidenceLinked {
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
		request evidenceReferenceRequest
		status  EvidenceLinkStatus
	}{
		{name: "omitted", status: EvidenceMissing},
		{name: "empty", request: evidenceReferenceRequest{present: true}, status: EvidenceMissing},
		{name: "parser rejected", request: evidenceReferenceRequest{present: true, invalid: true}, status: EvidenceInvalid},
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
			got := bindEvidence(test.events, evidenceReferenceRequest{present: true, refs: test.refs}, scope)
			if got == nil || got.Status != EvidenceInvalid || len(got.Refs) != 0 {
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
		RootCause:       "bad tag",
		evidenceRequest: evidenceReferenceRequest{present: true, refs: []string{currentRef}},
		evidenceScope:   scope,
		issuedEvidence: investigationrefs.Records{
			currentRef: `{"kind":"Pod"}`,
		},
	}
	run.mu.Lock()
	run.bindRootCauseEvidenceLocked(&diagnosis)
	run.mu.Unlock()
	if diagnosis.RootCauseEvidence == nil || diagnosis.RootCauseEvidence.Status != EvidenceLinked {
		t.Fatalf("same host id across turns should bind by current ref: %+v", diagnosis.RootCauseEvidence)
	}
	if diagnosis.issuedEvidence != nil || diagnosis.evidenceScope != "" || diagnosis.evidenceRequest.present {
		t.Fatalf("private binding material survived promotion: %+v", diagnosis)
	}

	diagnosis = Diagnosis{
		RootCause:       "bad tag",
		evidenceRequest: evidenceReferenceRequest{present: true, refs: []string{rejectedRef}},
		evidenceScope:   scope,
		issuedEvidence: investigationrefs.Records{
			currentRef: `{"kind":"Pod"}`,
		},
	}
	run.mu.Lock()
	run.bindRootCauseEvidenceLocked(&diagnosis)
	run.mu.Unlock()
	if diagnosis.RootCauseEvidence.Status != EvidenceInvalid {
		t.Fatalf("rejected callback became evidence: %+v", diagnosis.RootCauseEvidence)
	}
}

func TestBindRootCauseEvidenceOmittedWithoutRootCause(t *testing.T) {
	diagnosis := Diagnosis{
		Healthy:           true,
		RootCauseEvidence: &RootCauseEvidence{Status: EvidenceLinked, Refs: []string{"should-clear"}},
	}
	run := &Run{}
	run.mu.Lock()
	run.bindRootCauseEvidenceLocked(&diagnosis)
	run.mu.Unlock()
	if diagnosis.RootCauseEvidence != nil {
		t.Fatalf("healthy result retained root-cause links: %+v", diagnosis.RootCauseEvidence)
	}
}
