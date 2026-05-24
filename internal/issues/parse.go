package issues

import (
	"fmt"
	"strings"
)

// ParseSources parses a comma-separated `source=` list into the typed
// Source slice. Shared between the REST handler (/api/issues) and the
// MCP issues tool — both accept the same source vocabulary and reject
// the same removed values, so keeping the parser in one place avoids
// the two surfaces drifting on what's recognized.
//
// "audit" is explicitly rejected with a redirect message: audit findings
// are static config posture (a separate axis from live operational
// state) and live behind /api/audit / MCP get_cluster_audit. Combining
// them inside the issues source filter is the conflation that drove the
// B7 bench failure.
func ParseSources(v string) ([]Source, error) {
	if v == "" {
		return nil, nil
	}
	parts := strings.Split(v, ",")
	out := make([]Source, 0, len(parts))
	for _, p := range parts {
		switch strings.ToLower(strings.TrimSpace(p)) {
		case "":
			continue
		case "problem":
			out = append(out, SourceProblem)
		case "missing_ref":
			out = append(out, SourceMissingRef)
		case "event":
			out = append(out, SourceEvent)
		case "condition":
			out = append(out, SourceCondition)
		case "kyverno":
			out = append(out, SourceKyverno)
		case "audit":
			return nil, fmt.Errorf("source=audit was removed — use GET /api/audit (or MCP get_cluster_audit) for static best-practice findings; issues now covers live operational state only")
		default:
			return nil, fmt.Errorf("unknown source %q (want: problem, missing_ref, event, condition, kyverno)", p)
		}
	}
	return out, nil
}
