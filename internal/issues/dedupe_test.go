package issues

import (
	"testing"

	"github.com/skyhook-io/radar/pkg/issuesapi"
)

func TestDedupePodSchedulingOverProblem(t *testing.T) {
	sched := Issue{Source: SourceScheduling, Kind: "Pod", Namespace: "ns", Name: "web-abc"}
	problemSamePod := Issue{Source: SourceProblem, Kind: "Pod", Namespace: "ns", Name: "web-abc"}
	problemOtherPod := Issue{Source: SourceProblem, Kind: "Pod", Namespace: "ns", Name: "api-xyz"}

	t.Run("drops problem row when scheduling row covers the same pod", func(t *testing.T) {
		out := dedupePodSchedulingOverProblem([]Issue{sched, problemSamePod})
		if len(out) != 1 || out[0].Source != SourceScheduling {
			t.Fatalf("expected only the scheduling row to survive, got %+v", out)
		}
	})

	// The >10m stuck-pod case the doc comment guards: a problem-source row with
	// no scheduling counterpart is the pod's only row and must NOT be dropped.
	t.Run("keeps problem row with no scheduling counterpart", func(t *testing.T) {
		out := dedupePodSchedulingOverProblem([]Issue{sched, problemOtherPod})
		var keptOther bool
		for _, i := range out {
			if i.Name == "api-xyz" {
				keptOther = true
			}
		}
		if !keptOther {
			t.Fatalf("expected the uncovered problem row to survive, got %+v", out)
		}
	})

	t.Run("no scheduling rows is a no-op", func(t *testing.T) {
		in := []Issue{problemSamePod, problemOtherPod}
		out := dedupePodSchedulingOverProblem(in)
		if len(out) != 2 {
			t.Fatalf("expected both rows to survive when no scheduling row exists, got %+v", out)
		}
	})
}

func TestDedupeConditionOverMissingRef(t *testing.T) {
	missing := Issue{
		Source:    SourceMissingRef,
		Group:     "gateway.networking.k8s.io",
		Kind:      "HTTPRoute",
		Namespace: "prod",
		Name:      "broken",
		Category:  issuesapi.CategoryGatewayRouteInvalid,
	}
	conditionSameObject := Issue{
		Source:    SourceCondition,
		Group:     "gateway.networking.k8s.io",
		Kind:      "HTTPRoute",
		Namespace: "prod",
		Name:      "broken",
		Category:  issuesapi.CategoryGatewayRouteInvalid,
	}
	conditionOtherCategory := conditionSameObject
	conditionOtherCategory.Category = issuesapi.CategoryGatewayNotReady
	conditionOtherObject := conditionSameObject
	conditionOtherObject.Name = "other"

	out := dedupeConditionOverMissingRef([]Issue{missing, conditionSameObject, conditionOtherCategory, conditionOtherObject})
	if len(out) != 3 {
		t.Fatalf("expected only the condition echo to be dropped, got %+v", out)
	}
	for _, i := range out {
		if i.Source == SourceCondition && i.Name == "broken" && i.Category == issuesapi.CategoryGatewayRouteInvalid {
			t.Fatalf("same-object condition echo survived: %+v", out)
		}
	}
}
