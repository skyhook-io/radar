package helm

import (
	"sort"
	"strconv"
	"strings"

	"github.com/skyhook-io/radar/pkg/helmhistory"
)

const (
	HelmOperationInsightActive    = "active"
	HelmOperationInsightRecovered = "recovered"

	maxRelatedOperationResources = 4
)

func buildOperationInsight(detail *HelmReleaseDetail) *HelmOperationInsight {
	if detail == nil || detail.LastOperation == nil {
		return nil
	}
	if detail.ManagedByFluxHelmRelease != "" {
		return nil
	}

	operation := *detail.LastOperation
	insight := &HelmOperationInsight{
		State:            operationInsightState(operation),
		SuggestedCompare: suggestedOperationCompare(detail.History, operation),
	}

	if insight.State == HelmOperationInsightActive {
		resources := operationResourceSignals(detail.Resources)
		if len(resources) > 0 {
			insight.SignalCount = len(resources)
			primary := resources[0]
			insight.PrimaryResource = &primary
			if len(resources) > 1 {
				limit := len(resources)
				if limit > maxRelatedOperationResources+1 {
					limit = maxRelatedOperationResources + 1
				}
				insight.RelatedResources = resources[1:limit]
			}
		}
	}

	if insight.PrimaryResource == nil && insight.SuggestedCompare == nil {
		return nil
	}
	return insight
}

func operationInsightState(operation HelmOperation) string {
	switch operation.Status {
	case helmhistory.StatusFailed, helmhistory.StatusStuck:
		return HelmOperationInsightActive
	default:
		return HelmOperationInsightRecovered
	}
}

func suggestedOperationCompare(history []HelmRevision, operation HelmOperation) *HelmSuggestedCompare {
	switch operation.Kind {
	case helmhistory.KindUpgradeRolledBack:
		return validSuggestedCompare(
			operation.TargetRevision,
			operation.FailedRevision,
			"Compare the restored revision with the failed upgrade revision.",
		)
	case helmhistory.KindRollback:
		previous := previousCompletedRevision(history, operation.Revision)
		return validSuggestedCompare(
			previous,
			operation.Revision,
			"Compare the previous completed revision with the rollback revision.",
		)
	case helmhistory.KindUpgradeFailed:
		revision := operation.FailedRevision
		if revision == 0 {
			revision = operation.Revision
		}
		previous := previousCompletedRevision(history, revision)
		return validSuggestedCompare(
			previous,
			revision,
			"Compare the previous completed revision with the failed upgrade revision.",
		)
	case helmhistory.KindPending:
		if strings.EqualFold(operation.PendingStatus, "pending-install") {
			return nil
		}
		previous := previousCompletedRevision(history, operation.Revision)
		return validSuggestedCompare(
			previous,
			operation.Revision,
			"Compare the previous completed revision with the pending Helm revision.",
		)
	default:
		return nil
	}
}

func validSuggestedCompare(revision1, revision2 int, reason string) *HelmSuggestedCompare {
	if revision1 <= 0 || revision2 <= 0 || revision1 == revision2 {
		return nil
	}
	return &HelmSuggestedCompare{
		Revision1: revision1,
		Revision2: revision2,
		Reason:    reason,
	}
}

func previousCompletedRevision(history []HelmRevision, beforeRevision int) int {
	best := 0
	for _, revision := range history {
		if revision.Revision >= beforeRevision || !isCompletedHistoryStatus(revision.Status) {
			continue
		}
		if revision.Revision > best {
			best = revision.Revision
		}
	}
	return best
}

func isCompletedHistoryStatus(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "deployed", "superseded":
		return true
	default:
		return false
	}
}

type scoredOperationResource struct {
	resource OwnedResource
	score    int
}

func operationResourceSignals(resources []OwnedResource) []OwnedResource {
	scored := make([]scoredOperationResource, 0, len(resources))
	for _, resource := range resources {
		score := operationResourceScore(resource)
		if score <= 0 {
			continue
		}
		scored = append(scored, scoredOperationResource{resource: resource, score: score})
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		if scored[i].resource.Kind != scored[j].resource.Kind {
			return scored[i].resource.Kind < scored[j].resource.Kind
		}
		if scored[i].resource.Namespace != scored[j].resource.Namespace {
			return scored[i].resource.Namespace < scored[j].resource.Namespace
		}
		return scored[i].resource.Name < scored[j].resource.Name
	})

	out := make([]OwnedResource, 0, len(scored))
	for _, item := range scored {
		out = append(out, item.resource)
	}
	return out
}

func operationResourceScore(resource OwnedResource) int {
	severity := operationResourceSeverity(resource)
	if severity <= 0 {
		return 0
	}
	return severity*100 + operationResourceKindTier(resource.Kind)
}

func operationResourceSeverity(resource OwnedResource) int {
	if strings.TrimSpace(resource.Issue) != "" {
		if isNetworkEdgeResourceKind(resource.Kind) {
			return 650
		}
		return 1000
	}
	status := strings.ToLower(strings.TrimSpace(resource.Status))
	switch status {
	case "failed", "error", "crashloopbackoff", "imagepullbackoff", "errimagepull", "evicted":
		return 900
	}
	if readyMismatch(resource.Ready) {
		return 700
	}
	switch status {
	case "pending", "progressing", "containercreating", "creating":
		return 600
	case "terminating":
		return 500
	}
	return 0
}

func operationResourceKindTier(kind string) int {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "deployment", "statefulset", "daemonset", "replicaset":
		return 4
	case "pod":
		return 3
	case "job":
		return 3
	case "persistentvolumeclaim", "persistentvolume":
		return 2
	case "service", "ingress":
		return 1
	default:
		return 0
	}
}

func isNetworkEdgeResourceKind(kind string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "service", "ingress":
		return true
	default:
		return false
	}
}

func readyMismatch(ready string) bool {
	currentRaw, desiredRaw, ok := strings.Cut(strings.TrimSpace(ready), "/")
	if !ok {
		return false
	}
	current, currentErr := strconv.Atoi(strings.TrimSpace(currentRaw))
	desired, desiredErr := strconv.Atoi(strings.TrimSpace(desiredRaw))
	return currentErr == nil && desiredErr == nil && desired > 0 && current < desired
}
