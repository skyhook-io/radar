package insights

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/skyhook-io/radar/pkg/gitops"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// BuildFluxActivity lists the revisions Flux remembers in status.history and
// the root's notable events. Without history it keeps the condition rows, so
// an older Flux still shows something.
func BuildFluxActivity(root *unstructured.Unstructured, resolver Resolver) []HistoryItem {
	var rows []HistoryItem
	for _, e := range gitops.FluxKustomizationHistory(root) {
		row := HistoryItem{Kind: "revision", ID: e.Digest, Revision: e.Revision, FirstAt: e.FirstReconciled, DeployedAt: e.LastReconciled, Duration: e.Duration, Runs: e.Runs, Phase: "Applied"}
		if e.Status != "ReconciliationSucceeded" {
			row.Phase, row.Message = "Failed", e.Status
		}
		rows = append(rows, row)
	}
	for _, e := range gitops.FluxHelmHistory(root) {
		version := strconv.FormatInt(e.Version, 10)
		// Helm's firstDeployed is when the release was first installed, not
		// this revision, so it is left out.
		rows = append(rows, HistoryItem{Kind: "revision", ID: version, Revision: e.ChartVersion + " · release " + version, DeployedAt: e.LastDeployed, Phase: capitalize(e.Status)})
	}
	hasHistory := len(rows) > 0
	if !hasHistory {
		rows = buildHistory(root, gitops.ToolFluxCD)
		for i := range rows {
			rows[i].Kind = "condition"
		}
	}

	if resolver != nil {
		group := resourceid.GroupFromAPIVersion(root.GetAPIVersion())
		for _, ev := range resolver.RecentEvents(group, root.GetKind(), root.GetNamespace(), root.GetName()) {
			// Flux emits these every interval; status.history already covers them.
			if ev.Type != "Warning" && (ev.Reason == "ReconciliationSucceeded" || ev.Reason == "ArtifactUpToDate") {
				continue
			}
			rows = append(rows, HistoryItem{Kind: "event", ID: ev.Reason, Phase: ev.Type, Message: ev.Message, DeployedAt: ev.LastTimestamp, Runs: int64(ev.Count)})
		}
	}
	// Event times carry the server's local offset and history times are UTC,
	// so compare instants, not strings.
	sort.SliceStable(rows, func(i, j int) bool { return activityTime(rows[i].DeployedAt).After(activityTime(rows[j].DeployedAt)) })

	if !hasHistory {
		return rows
	}
	for _, c := range conditions(root) {
		if (c.typ == "Ready" && c.status == "False") || (c.typ == "Stalled" && c.status == "True") {
			failing := HistoryItem{Kind: "condition", ID: c.typ, Phase: fluxPhaseLabel(c.status, c.reason), Message: c.message, DeployedAt: c.lastTransitionTime}
			return append([]HistoryItem{failing}, rows...)
		}
	}
	return rows
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func activityTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}
