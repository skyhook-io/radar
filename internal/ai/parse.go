package ai

import (
	"encoding/json"
	"regexp"
	"strings"
)

var jsonBlockRe = regexp.MustCompile("(?s)```json\\s*(\\{.*?\\})\\s*```")

// diagnosisFromText assembles the Diagnosis from the CLI's final text. The
// prompt asks for a trailing fenced json block {root_cause, remediation,
// confidence}; we parse the last one. Absent that, the whole text is the report
// and its first paragraph the root cause.
func diagnosisFromText(text string) Diagnosis {
	text = strings.TrimSpace(text)
	d := Diagnosis{Report: text}
	if m := jsonBlockRe.FindAllStringSubmatch(text, -1); len(m) > 0 {
		var parsed struct {
			Healthy          *bool    `json:"healthy"`
			RootCause        string   `json:"root_cause"`
			Remediation      []string `json:"remediation"`
			RecommendedIndex *int     `json:"recommended_index"`
			Confidence       *float64 `json:"confidence"`
		}
		if json.Unmarshal([]byte(m[len(m)-1][1]), &parsed) == nil {
			if parsed.Healthy != nil {
				d.Healthy = *parsed.Healthy
			}
			d.RootCause = parsed.RootCause
			d.Remediation = parsed.Remediation
			d.Confidence = parsed.Confidence
			d.Report = strings.TrimSpace(jsonBlockRe.ReplaceAllString(text, ""))
			// Keep the index only when it points at a real remediation step.
			if parsed.RecommendedIndex != nil && *parsed.RecommendedIndex >= 1 &&
				*parsed.RecommendedIndex <= len(parsed.Remediation) {
				d.RecommendedIndex = parsed.RecommendedIndex
			}
		}
	}
	// Deliberately NOT fabricating a RootCause from free text: a reply with no
	// structured root_cause (e.g. "the resource looks healthy", or a clarifying
	// question) must not render under the alarming "ROOT CAUSE" anchor. The UI
	// shows such replies as a neutral analysis (Report carries the full text).
	return d
}
