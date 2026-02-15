package context

import (
	"strings"
	"testing"
)

func TestAssembleContext_BasicStructure(t *testing.T) {
	sections := ContextSections{
		ResourceKind:      "Pod",
		ResourceNamespace: "default",
		ResourceName:      "my-pod",
		MinifiedResource:  `{"kind":"Pod","metadata":{"name":"my-pod"}}`,
		Events:            "[Warning] BackOff (x5): Back-off restarting",
		Logs:              "ERROR: connection refused",
		Metrics:           "app: CPU 100m/req:200m/lim:500m",
		Relationships:     "Owner: ReplicaSet/my-rs",
		Notes:             []string{"0 traffic metrics available"},
	}

	result := AssembleContext(sections, BudgetCloud)

	// Should have resource identifier
	if !strings.Contains(result, "Pod/default/my-pod") {
		t.Error("Expected resource identifier")
	}

	// Should have XML-delimited sections
	for _, tag := range []string{"resource", "events", "logs", "metrics", "relationships", "context_notes"} {
		if !strings.Contains(result, "<"+tag+">") || !strings.Contains(result, "</"+tag+">") {
			t.Errorf("Expected <%s> section", tag)
		}
	}

	// Empty sections should not appear
	if strings.Contains(result, "<gitops>") || strings.Contains(result, "<traffic>") {
		t.Error("Empty sections should not appear")
	}

	// Notes should be included
	if !strings.Contains(result, "0 traffic metrics available") {
		t.Error("Expected context notes")
	}
}

func TestAssembleContext_LocalBudgetTruncates(t *testing.T) {
	// Create sections that exceed local budget (~3K tokens = ~12K chars)
	bigContent := strings.Repeat("x", 5000)
	sections := ContextSections{
		ResourceKind:      "Pod",
		ResourceNamespace: "default",
		ResourceName:      "my-pod",
		MinifiedResource:  bigContent,
		Events:            bigContent,
		Logs:              bigContent,
		Metrics:           bigContent,
		Relationships:     bigContent,
	}

	result := AssembleContext(sections, BudgetLocal)

	// Result should be bounded by budget
	maxExpected := BudgetLocal.maxChars() + 500 // some overhead tolerance
	if len(result) > maxExpected {
		t.Errorf("Result (%d chars) exceeds local budget (%d chars)", len(result), maxExpected)
	}
}

func TestAssembleContext_EmptySections(t *testing.T) {
	sections := ContextSections{
		ResourceKind:      "Pod",
		ResourceNamespace: "default",
		ResourceName:      "my-pod",
		MinifiedResource:  `{"kind":"Pod"}`,
	}

	result := AssembleContext(sections, BudgetCloud)

	if !strings.Contains(result, "<resource>") {
		t.Error("Expected resource section")
	}
	if strings.Contains(result, "<events>") {
		t.Error("Empty events should not appear")
	}
	if strings.Contains(result, "<logs>") {
		t.Error("Empty logs should not appear")
	}
}

func TestAssembleContext_CloudBudgetFitsMore(t *testing.T) {
	content := strings.Repeat("x", 3000)
	sections := ContextSections{
		ResourceKind:      "Pod",
		ResourceNamespace: "default",
		ResourceName:      "my-pod",
		MinifiedResource:  content,
		Events:            content,
		Logs:              content,
		Metrics:           content,
	}

	localResult := AssembleContext(sections, BudgetLocal)
	cloudResult := AssembleContext(sections, BudgetCloud)

	// Cloud should have more content than local
	if len(cloudResult) <= len(localResult) {
		t.Error("Cloud budget should allow more content than local")
	}
}

func TestAssembleContext_PriorityOrder(t *testing.T) {
	sections := ContextSections{
		ResourceKind:      "Pod",
		ResourceNamespace: "default",
		ResourceName:      "my-pod",
		MinifiedResource:  "resource data",
		Events:            "event data",
		Logs:              "log data",
		Relationships:     "rel data",
	}

	result := AssembleContext(sections, BudgetCloud)

	// Resource should appear before events, events before logs
	resourceIdx := strings.Index(result, "<resource>")
	eventsIdx := strings.Index(result, "<events>")
	logsIdx := strings.Index(result, "<logs>")
	relsIdx := strings.Index(result, "<relationships>")

	if resourceIdx > eventsIdx {
		t.Error("Resource should appear before events")
	}
	if eventsIdx > logsIdx {
		t.Error("Events should appear before logs")
	}
	if logsIdx > relsIdx {
		t.Error("Logs should appear before relationships")
	}
}
