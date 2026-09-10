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
)

const (
	maxDiagnosisEvidenceRefs = 3
	// Case caps are constants to revisit after real runs.
	maxDiagnosisEvidenceItems  = 8
	maxDiagnosisRuledOut       = 5
	maxDiagnosisClaimChars     = 200
	maxDiagnosisSubjectChars   = 253
	maxDiagnosisHypothesisRune = 200
)

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
			// Normalize verdict precedence so the object can't be self-contradictory:
			// a concrete finding (root cause / remediation) wins over both flags; an
			// explicit "couldn't tell" wins over healthy ("absence of evidence is not
			// health"). So at most one of {finding, inconclusive, healthy} holds.
			if d.RootCause != "" || len(d.Remediation) > 0 {
				d.Healthy = false
				d.Inconclusive = false
			} else if d.Inconclusive {
				d.Healthy = false
			}
			// Keep the index only when it points at a real remediation step.
			if parsed.RecommendedIndex != nil && *parsed.RecommendedIndex >= 1 &&
				*parsed.RecommendedIndex <= len(parsed.Remediation) {
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
	if len(evidenceRaw) > 0 && json.Unmarshal(evidenceRaw, &rawItems) == nil {
		if len(rawItems) > maxDiagnosisEvidenceItems {
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
	if json.Unmarshal(raw, &parsed) != nil || !evidenceRefRe.MatchString(parsed.Ref) {
		return caseItemRequest{}
	}
	role := EvidenceRole(strings.ToLower(strings.TrimSpace(parsed.Role)))
	if _, known := evidenceRoles[role]; !known {
		return caseItemRequest{}
	}
	claim := strings.TrimSpace(parsed.Claim)
	if utf8.RuneCountInString(claim) > maxDiagnosisClaimChars {
		return caseItemRequest{}
	}
	item := caseItemRequest{valid: true, ref: parsed.Ref, role: role, claim: claim}
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
