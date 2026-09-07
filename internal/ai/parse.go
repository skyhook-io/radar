package ai

import (
	"encoding/json"
	"regexp"
	"strings"
)

var (
	jsonBlockRe     = regexp.MustCompile("(?s)```json\\s*(\\{.*?\\})\\s*```")
	evidenceScopeRe = regexp.MustCompile(`^[a-z2-7]{26,128}$`)
	evidenceRefRe   = regexp.MustCompile(`^ev_[a-z2-7]{26,128}_[a-z2-7]{26,128}$`)
)

const maxDiagnosisEvidenceRefs = 3

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
