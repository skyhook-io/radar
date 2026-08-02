package timeline

import (
	"strings"
	"testing"
)

// GetDiagnosis must derive its recommendations only from rows the caller may
// read: when `allow` denies a kind, neither the drop nor any tip computed from
// it may appear — otherwise the recommendation text leaks a filtered-out row.
func TestGetDiagnosis_RecommendationsRespectRBAC(t *testing.T) {
	m := GetMetrics()
	m.Reset()
	RecordDrop("Secret", "team-a", "db-creds", DropReasonNoisyFilter, "update")

	hasNoisyTip := func(recs []string) bool {
		for _, r := range recs {
			if strings.Contains(r, "noisy_filter") {
				return true
			}
		}
		return false
	}

	// nil allow (auth off): the drop and its tip are present.
	open := GetDiagnosis("Secret", "team-a", "db-creds", "", nil)
	if len(open.DropHistory) != 1 || !hasNoisyTip(open.Recommendations) {
		t.Fatalf("nil allow must surface the drop + its tip, got drops=%d recs=%v", len(open.DropHistory), open.Recommendations)
	}

	// allow denies Secret: no drop row AND no tip derived from it.
	denySecret := func(kind, apiVersion, namespace string) bool { return kind != "Secret" }
	gated := GetDiagnosis("Secret", "team-a", "db-creds", "", denySecret)
	if len(gated.DropHistory) != 0 {
		t.Fatalf("denied kind must be filtered from DropHistory, got %+v", gated.DropHistory)
	}
	if hasNoisyTip(gated.Recommendations) {
		t.Fatalf("recommendation leaked a filtered-out drop: %v", gated.Recommendations)
	}
}
