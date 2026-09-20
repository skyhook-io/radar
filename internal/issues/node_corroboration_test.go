package issues

import (
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"k8s.io/apimachinery/pkg/types"
)

func TestNodeCorroborationContextPreservesRepresentativeAndRollup(t *testing.T) {
	now := time.Now()
	detection := k8s.Detection{Kind: "Pod", Namespace: "visible", Name: "one", Reason: "IPExhaustion", Severity: "critical", Message: "specific per-Pod failure", OwnerKind: "Deployment", OwnerName: "workload", NodeStartupCorroboration: &k8s.NodeStartupCorroboration{Node: "node-a", PodCount: 7, OwnerCount: 3}}
	for _, name := range []string{"one", "two", "three", "four", "five", "six", "seven"} {
		detection.NodeStartupCorroboration.Pods = append(detection.NodeStartupCorroboration.Pods, types.NamespacedName{Namespace: "visible", Name: name})
	}
	first := fromProblem(detection, now, SourceScheduling)
	if first.Message != detection.Message || first.DiagnosticContext == nil {
		t.Fatalf("lost failure/context: %+v", first)
	}
	fact := first.DiagnosticContext.Facts[0]
	if fact.Confidence != "" || len(fact.Refs) != 5 || !strings.Contains(fact.Message, "Showing 5 of 7") || !strings.Contains(fact.Message, "including Pod visible/one") {
		t.Fatalf("misleading or unbounded fact: %+v", fact)
	}
	second := first
	second.Name = "other-node-pod"
	second.DiagnosticContext = &issuesapi.DiagnosticContext{Role: issuesapi.DiagnosticRoleContext, Facts: []issuesapi.DiagnosticFact{{Type: factNodeStartupCorroboration, Message: "different cohort on node-b", Refs: []Ref{{Kind: "Pod", Namespace: "visible", Name: "other-node-pod"}}}}}
	grouped := GroupIssues([]Issue{first, second})
	if len(grouped) != 1 {
		t.Fatalf("group count %d", len(grouped))
	}
	originalFact := grouped[0].DiagnosticContext.Facts[0]
	provider := &fakeProvider{}
	for iteration := 0; iteration < 2; iteration++ {
		grouped = enrichDiagnosticContext(grouped, []Issue{first, second}, grouped, provider)
		counts := map[string]int{}
		for _, got := range grouped[0].DiagnosticContext.Facts {
			counts[got.Type]++
			if got.Type == factNodeStartupCorroboration && got.Message != originalFact.Message {
				t.Fatalf("cohorts were merged: %+v", got)
			}
		}
		if counts[factNodeStartupCorroboration] != 1 || counts[factOwnerRollup] != 1 {
			t.Fatalf("lost/duplicated context after pass%d: %+v", iteration, counts)
		}
	}
	detection.NodeStartupCorroboration = nil
	if got := fromProblem(detection, now, SourceScheduling); got.DiagnosticContext != nil {
		t.Fatalf("invented evidence: %+v", got)
	}
}
