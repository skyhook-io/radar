package ai

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	jsonBlockRe     = regexp.MustCompile("(?s)```json\\s*(\\{.*?\\})\\s*```")
	evidenceScopeRe = regexp.MustCompile(`^[a-z2-7]{26,128}$`)
	evidenceRefRe   = regexp.MustCompile(`^ev_[a-z2-7]{26,128}_[a-z2-7]{26,128}$`)
	// The ref reaches the model inside a marker wrapper, so a model that
	// copies the whole marker instead of its payload is quoting Radar
	// correctly. Unwrapping is transcription, not interpretation.
	// Anchored, and one marker only: a field holding two markers, or a marker
	// inside prose, is the agent meaning something this cannot read, and
	// picking the first would be choosing an interpretation rather than
	// removing a wrapper.
	wrappedEvidenceRefRe = regexp.MustCompile(`^\[\[radar:evidence-ref=(ev_[a-z2-7]{26,128}_[a-z2-7]{26,128})\]\]$`)
)

const (
	maxDiagnosisEvidenceRefs = 3
	// Case caps are constants to revisit after real runs.
	maxDiagnosisEvidenceItems  = 8
	maxDiagnosisRuledOut       = 5
	maxDiagnosisClaimChars     = 200
	maxDiagnosisSubjectChars   = 253
	maxDiagnosisHypothesisRune = 200
	maxDiagnosisSummaryRune    = 240
	maxDiagnosisUnresolved     = 3
	maxDiagnosisUnresolvedRune = 200
	maxDiagnosisSteps          = 6
	maxDiagnosisPreconditRune  = 200
)

var diagnosisCertainties = map[DiagnosisCertainty]struct{}{
	CertaintyEstablished: {}, CertaintyLikely: {}, CertaintySuspected: {},
}

var diagnosisStepKinds = map[DiagnosisStepKind]struct{}{
	StepMitigate: {}, StepVerify: {}, StepInvestigate: {},
}

// evidenceRoles is one half of a Go↔TS contract: the DiagnosisEvidenceRole
// union in web/src/api/diagnose.ts and EVIDENCE_ROLES in
// web/src/components/diagnose/investigationCase.ts must list exactly these
// roles. A role accepted here but absent there is parsed and then dropped by
// the frontend; change both sides together.
var evidenceRoles = map[EvidenceRole]struct{}{
	EvidenceRoleCause:    {},
	EvidenceRoleSymptom:  {},
	EvidenceRoleContext:  {},
	EvidenceRoleBenign:   {},
	EvidenceRoleDemoted:  {},
	EvidenceRoleRulesOut: {},
}

// diagnosisFromText assembles the Diagnosis from the CLI's final text. The
// prompt asks for a trailing fenced json block {root_cause, remediation,
// confidence}; we parse the last one. Absent that, the whole text is the report
// and its first paragraph the root cause.
func diagnosisFromText(text string) Diagnosis {
	text = strings.TrimSpace(text)
	d := Diagnosis{Report: text}
	if m := jsonBlockRe.FindAllStringSubmatch(text, -1); len(m) > 0 {
		var parsed struct {
			Healthy           *bool           `json:"healthy"`
			Inconclusive      *bool           `json:"inconclusive"`
			RootCause         string          `json:"root_cause"`
			EvidenceRefs      json.RawMessage `json:"root_cause_evidence_refs"`
			Evidence          json.RawMessage `json:"evidence"`
			RuledOut          json.RawMessage `json:"ruled_out"`
			Remediation       []string        `json:"remediation"`
			RecommendedIndex  *int            `json:"recommended_index"`
			RecommendedReason string          `json:"recommended_reason"`
			Confidence        *float64        `json:"confidence"`
			Summary           string          `json:"summary"`
			Certainty         string          `json:"certainty"`
			Unresolved        []string        `json:"unresolved"`
			Steps             json.RawMessage `json:"steps"`
			RevisesAssessment *bool           `json:"revises_assessment"`
		}
		if json.Unmarshal([]byte(m[len(m)-1][1]), &parsed) == nil {
			if parsed.Healthy != nil {
				d.Healthy = *parsed.Healthy
			}
			if parsed.Inconclusive != nil {
				d.Inconclusive = *parsed.Inconclusive
			}
			d.RootCause = parsed.RootCause
			d.evidenceRequest = parseEvidenceReferenceRequest(parsed.EvidenceRefs)
			d.caseRequest = parseCaseRequest(parsed.Evidence, parsed.RuledOut)
			d.Remediation = parsed.Remediation
			d.Confidence = parsed.Confidence
			d.Report = strings.TrimSpace(jsonBlockRe.ReplaceAllString(text, ""))
			d.Summary = clampSummary(strings.TrimSpace(parsed.Summary), maxDiagnosisSummaryRune)
			if certainty := DiagnosisCertainty(strings.ToLower(strings.TrimSpace(parsed.Certainty))); certainty != "" {
				if _, known := diagnosisCertainties[certainty]; known {
					d.Certainty = certainty
				}
			}
			for _, item := range parsed.Unresolved {
				item = strings.TrimSpace(item)
				if item == "" || len(d.Unresolved) == maxDiagnosisUnresolved {
					continue
				}
				d.Unresolved = append(d.Unresolved, clampRunes(item, maxDiagnosisUnresolvedRune))
			}
			d.Steps = parseSteps(parsed.Steps)
			if len(d.Steps) > 0 {
				// One action list: the typed steps are authoritative and the
				// legacy remediation array is derived from them, so Apply,
				// recommended_index and the UI cannot disagree about a step.
				d.Remediation = make([]string, len(d.Steps))
				for i, step := range d.Steps {
					d.Remediation[i] = step.Text
				}
			}
			if parsed.RevisesAssessment != nil {
				d.RevisesAssessment = *parsed.RevisesAssessment
			}
			// Normalize verdict precedence so the object can't be self-contradictory.
			// A root cause wins over both flags, and an explicit "couldn't tell"
			// wins over healthy ("absence of evidence is not health"). A step
			// never establishes a cause: with typed steps, "cause unresolved; roll
			// back to restore service" is a legitimate verdict, so only the
			// legacy remediation-only shape keeps clearing inconclusive.
			if d.RootCause != "" || (len(d.Steps) == 0 && len(d.Remediation) > 0) {
				d.Healthy = false
				d.Inconclusive = false
			} else if d.Inconclusive {
				d.Healthy = false
			}
			// "Established" means nothing left would change the answer, which
			// is exactly what a non-empty unresolved list denies; the two
			// cannot both be true, and the caveats are the part the reader
			// must not lose.
			if d.Certainty == CertaintyEstablished && len(d.Unresolved) > 0 {
				d.Certainty = CertaintyLikely
			}
			// Keep the index only when it points at a real step, and with typed
			// steps only at one Apply may perform: a mitigation, and one whose
			// applicability is not itself in question — a step that is right
			// "only if" something unverified holds is not a one-click fix.
			if parsed.RecommendedIndex != nil && *parsed.RecommendedIndex >= 1 &&
				*parsed.RecommendedIndex <= len(d.Remediation) &&
				(len(d.Steps) == 0 ||
					(d.Steps[*parsed.RecommendedIndex-1].Kind == StepMitigate &&
						d.Steps[*parsed.RecommendedIndex-1].Precondition == "")) {
				d.RecommendedIndex = parsed.RecommendedIndex
				d.RecommendedReason = strings.TrimSpace(parsed.RecommendedReason)
			}
		}
	}
	// Deliberately NOT fabricating a RootCause from free text: a reply with no
	// structured root_cause (e.g. "the resource looks healthy", or a clarifying
	// question) must not render under the alarming "ROOT CAUSE" anchor. The UI
	// shows such replies as a neutral analysis (Report carries the full text).
	return d
}

// parseSteps reads the typed next steps. A step with an unknown kind or no
// text is dropped rather than guessed at; the cap cuts, it does not reject.
func parseSteps(raw json.RawMessage) []DiagnosisStep {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var parsed []struct {
		Text         string `json:"text"`
		Kind         string `json:"kind"`
		Precondition string `json:"precondition"`
	}
	if json.Unmarshal(raw, &parsed) != nil {
		return nil
	}
	var steps []DiagnosisStep
	for _, item := range parsed {
		if len(steps) == maxDiagnosisSteps {
			break
		}
		text := strings.TrimSpace(item.Text)
		kind := DiagnosisStepKind(strings.ToLower(strings.TrimSpace(item.Kind)))
		if text == "" {
			continue
		}
		if _, known := diagnosisStepKinds[kind]; !known {
			continue
		}
		steps = append(steps, DiagnosisStep{
			Text:         text,
			Kind:         kind,
			Precondition: clampRunes(strings.TrimSpace(item.Precondition), maxDiagnosisPreconditRune),
		})
	}
	return steps
}

// clampSummary keeps a headline whole: an over-long summary is cut at the
// last sentence end inside the budget, then at the last word, never mid-word.
// The cut is visible as an ellipsis, so a reader knows there was more.
func clampSummary(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)[:limit]
	text := string(runes)
	if end := strings.LastIndexAny(text, ".!?"); end > limit/2 {
		return strings.TrimSpace(text[:end+1])
	}
	if space := strings.LastIndex(text, " "); space > limit/2 {
		return strings.TrimSpace(text[:space]) + "…"
	}
	return strings.TrimSpace(text) + "…"
}

// clampRunes cuts a free-text field at a rune budget. Unlike claims, these
// fields are headlines and caveats where a cut sentence still reads as one;
// the alternative, dropping the whole field, would hide the caveat entirely.
func clampRunes(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return strings.TrimSpace(string(runes[:limit]))
}

func parseEvidenceReferenceRequest(raw json.RawMessage) evidenceReferenceRequest {
	if len(raw) == 0 {
		return evidenceReferenceRequest{}
	}
	request := evidenceReferenceRequest{present: true}
	if string(raw) == "null" {
		request.invalid = true
		return request
	}
	var refs []string
	if json.Unmarshal(raw, &refs) != nil || len(refs) > maxDiagnosisEvidenceRefs {
		request.invalid = true
		return request
	}
	seen := make(map[string]struct{}, len(refs))
	for _, ref := range refs {
		if !evidenceRefRe.MatchString(ref) {
			request.invalid = true
			return request
		}
		if _, duplicate := seen[ref]; duplicate {
			request.invalid = true
			return request
		}
		seen[ref] = struct{}{}
	}
	request.refs = refs
	return request
}

// parseCaseRequest reads the agent's evidence items and ruled-out hypotheses.
// A malformed item is kept at its index as invalid so ruled_out indexes still
// point where the agent meant; it never invalidates its siblings or the legacy
// root_cause_evidence_refs. Over-cap arrays are cut, not rejected.
func parseCaseRequest(evidenceRaw, ruledOutRaw json.RawMessage) caseRequest {
	var request caseRequest
	var rawItems []json.RawMessage
	if len(evidenceRaw) > 0 && string(evidenceRaw) != "null" &&
		json.Unmarshal(evidenceRaw, &rawItems) != nil {
		// The agent sent something for evidence that is not a list of items.
		// There is no honest count of what it meant to say, so the loss is
		// reported as a fact rather than a number.
		request.malformed = true
	}
	if len(evidenceRaw) > 0 && json.Unmarshal(evidenceRaw, &rawItems) == nil {
		if len(rawItems) > maxDiagnosisEvidenceItems {
			// Items past the cap get no slot in Evidence at all, so their loss
			// is only visible if counted here.
			request.dropped = len(rawItems) - maxDiagnosisEvidenceItems
			rawItems = rawItems[:maxDiagnosisEvidenceItems]
		}
		for _, raw := range rawItems {
			request.items = append(request.items, parseCaseItem(raw))
		}
	}
	var rawRuledOut []json.RawMessage
	if len(ruledOutRaw) > 0 && json.Unmarshal(ruledOutRaw, &rawRuledOut) == nil {
		for _, raw := range rawRuledOut {
			if len(request.ruledOut) == maxDiagnosisRuledOut {
				break
			}
			if entry, ok := parseRuledOut(raw); ok {
				request.ruledOut = append(request.ruledOut, entry)
			}
		}
	}
	return request
}

func parseRuledOut(raw json.RawMessage) (DiagnosisRuledOut, bool) {
	var parsed struct {
		Hypothesis    string `json:"hypothesis"`
		EvidenceIndex *int   `json:"evidence_index"`
	}
	if json.Unmarshal(raw, &parsed) != nil {
		return DiagnosisRuledOut{}, false
	}
	hypothesis := strings.TrimSpace(parsed.Hypothesis)
	if hypothesis == "" || utf8.RuneCountInString(hypothesis) > maxDiagnosisHypothesisRune ||
		parsed.EvidenceIndex == nil || *parsed.EvidenceIndex < 0 {
		return DiagnosisRuledOut{}, false
	}
	return DiagnosisRuledOut{Hypothesis: hypothesis, EvidenceIndex: *parsed.EvidenceIndex}, true
}

func parseCaseItem(raw json.RawMessage) caseItemRequest {
	var parsed struct {
		Ref     string          `json:"ref"`
		Role    string          `json:"role"`
		Claim   string          `json:"claim"`
		Subject json.RawMessage `json:"subject"`
	}
	if json.Unmarshal(raw, &parsed) != nil {
		return caseItemRequest{}
	}
	ref := unwrapEvidenceRef(parsed.Ref)
	if !evidenceRefRe.MatchString(ref) {
		return caseItemRequest{}
	}
	role := EvidenceRole(strings.ToLower(strings.TrimSpace(parsed.Role)))
	if _, known := evidenceRoles[role]; !known {
		return caseItemRequest{}
	}
	claim := strings.TrimSpace(parsed.Claim)
	// The story carries the argument, so a claim is optional — except for the
	// roles that qualify or exclude something: an empty benign claim would
	// satisfy the frontend's "the agent explained this card" check while
	// saying nothing, quietly softening a warning banner, and a bare demoted
	// or rules_out label asserts without a reason. An over-long claim is
	// dropped rather than truncated — cutting a sentence mid-clause invents a
	// claim the agent did not make.
	claimRequired := role == EvidenceRoleBenign || role == EvidenceRoleDemoted || role == EvidenceRoleRulesOut
	if utf8.RuneCountInString(claim) > maxDiagnosisClaimChars {
		// The story places the item; losing the placement over a long note
		// would cost the reader the card. The note is dropped, the item kept
		// — unless the note is what the role asserts.
		if claimRequired {
			return caseItemRequest{}
		}
		claim = ""
	}
	if claim == "" && claimRequired {
		return caseItemRequest{}
	}
	item := caseItemRequest{valid: true, ref: ref, role: role, claim: claim}
	if len(parsed.Subject) == 0 || string(parsed.Subject) == "null" {
		return item
	}
	var subject DiagnosisEvidenceSubject
	if json.Unmarshal(parsed.Subject, &subject) != nil {
		return caseItemRequest{}
	}
	for _, field := range []string{
		subject.Kind, subject.Name, subject.Container, subject.Stream, subject.Observation,
	} {
		if utf8.RuneCountInString(field) > maxDiagnosisSubjectChars {
			return caseItemRequest{}
		}
	}
	for _, field := range []*string{subject.Group, subject.Namespace} {
		if field != nil && utf8.RuneCountInString(*field) > maxDiagnosisSubjectChars {
			return caseItemRequest{}
		}
	}
	if subject.Kind == "" || subject.Name == "" ||
		(subject.Stream != "" && subject.Stream != "current" && subject.Stream != "previous") {
		return caseItemRequest{}
	}
	item.subject = &subject
	return item
}

// unwrapEvidenceRef returns the ref inside a marker wrapper the model pasted
// whole, or the value unchanged when the field is not exactly one wrapper.
func unwrapEvidenceRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if m := wrappedEvidenceRefRe.FindStringSubmatch(ref); m != nil {
		return m[1]
	}
	return ref
}
