package filter

import (
	"strings"
	"testing"
)

func TestCompileObjectFilter_Simple(t *testing.T) {
	f, err := CompileObjectFilter(`kind == "Pod"`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	ok, err := f.Match(map[string]any{"kind": "Pod"})
	if err != nil || !ok {
		t.Fatalf("expected match, got ok=%v err=%v", ok, err)
	}
	ok, _ = f.Match(map[string]any{"kind": "Service"})
	if ok {
		t.Fatal("expected no match")
	}
}

func TestCompileObjectFilter_BoolCombinators(t *testing.T) {
	f, err := CompileObjectFilter(`kind == "Pod" && metadata.namespace.startsWith("prod-")`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	ok, _ := f.Match(map[string]any{
		"kind":     "Pod",
		"metadata": map[string]any{"namespace": "prod-east"},
	})
	if !ok {
		t.Fatal("expected match")
	}
	ok, _ = f.Match(map[string]any{
		"kind":     "Pod",
		"metadata": map[string]any{"namespace": "dev"},
	})
	if ok {
		t.Fatal("expected no match")
	}
}

func TestCompileObjectFilter_LabelMap(t *testing.T) {
	f, err := CompileObjectFilter(`labels["app"] == "cart"`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	ok, _ := f.Match(map[string]any{"labels": map[string]string{"app": "cart"}})
	if !ok {
		t.Fatal("expected match")
	}
}

func TestCompileObjectFilter_HasGuard(t *testing.T) {
	// Optional-field access: agent must use has() before drilling into
	// a missing path. CEL's protection against the LLM mistake of
	// reaching into a nil field.
	f, err := CompileObjectFilter(`has(status.readyReplicas) && status.readyReplicas == 0`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	ok, _ := f.Match(map[string]any{
		"status": map[string]any{"readyReplicas": int64(0)},
	})
	if !ok {
		t.Fatal("expected match")
	}
	// Missing field — has() short-circuits, no error.
	ok, errEval := f.Match(map[string]any{"status": map[string]any{}})
	if ok || errEval != nil {
		t.Fatalf("expected no-match no-error, got ok=%v err=%v", ok, errEval)
	}
}

func TestCompileObjectFilter_NumericComparison(t *testing.T) {
	f, err := CompileObjectFilter(`spec.replicas > 3`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	ok, _ := f.Match(map[string]any{"spec": map[string]any{"replicas": int64(5)}})
	if !ok {
		t.Fatal("expected match")
	}
	ok, _ = f.Match(map[string]any{"spec": map[string]any{"replicas": int64(2)}})
	if ok {
		t.Fatal("expected no match")
	}
}

func TestCompileObjectFilter_ParseError(t *testing.T) {
	_, err := CompileObjectFilter(`kind == ` /* dangling */)
	if err == nil {
		t.Fatal("expected parse error")
	}
	// cel-go emits position-tagged diagnostics like
	// "ERROR: <input>:1:9: Syntax error: ...". Assert on the marker
	// substring directly — the agent reads this verbatim to fix the
	// expression, so losing the position info is a real regression.
	msg := err.Error()
	if !strings.Contains(msg, "Syntax") {
		t.Errorf("expected cel diagnostic containing 'Syntax', got %q", msg)
	}
	if !strings.Contains(msg, ":1:") {
		t.Errorf("expected diagnostic to include position '<input>:1:N:', got %q", msg)
	}
}

func TestCompileObjectFilter_NonBool(t *testing.T) {
	// Filters must return bool — non-bool is rejected at compile time
	// so the agent doesn't waste a fan-out cycle.
	_, err := CompileObjectFilter(`kind`)
	if err == nil {
		t.Fatal("expected non-bool to be rejected at compile time")
	}
}

func TestCompileIssueFilter_SeverityCount(t *testing.T) {
	f, err := CompileIssueFilter(`severity == "critical" && count > 5`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	ok, _ := f.Match(map[string]any{
		"severity": "critical",
		"count":    int64(10),
	})
	if !ok {
		t.Fatal("expected match")
	}
	ok, _ = f.Match(map[string]any{
		"severity": "warning",
		"count":    int64(10),
	})
	if ok {
		t.Fatal("expected no match")
	}
}

// TestCompileIssueFilter_StuckAndRetryCount pins that the GitOps operation
// signals are CEL-filterable for agents (e.g. find stuck, high-retry failures).
// stuck is a bool — exercises the BindingBool env type.
func TestCompileIssueFilter_StuckAndRetryCount(t *testing.T) {
	f, err := CompileIssueFilter(`stuck && operation_retry_count >= 5`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	ok, err := f.Match(map[string]any{"stuck": true, "operation_retry_count": int64(5)})
	if err != nil || !ok {
		t.Fatalf("expected match, ok=%v err=%v", ok, err)
	}
	ok, _ = f.Match(map[string]any{"stuck": false, "operation_retry_count": int64(9)})
	if ok {
		t.Fatal("expected no match when not stuck")
	}
}

func TestCompileIssueFilter_DiagnosisFields(t *testing.T) {
	f, err := CompileIssueFilter(`remediation_kind == "create-namespace" && remediation_target == "demo" && cause.contains("namespace") && action == ""`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	ok, err := f.Match(map[string]any{
		"remediation_kind":   "create-namespace",
		"remediation_target": "demo",
		"cause":              "The destination namespace does not exist.",
		"action":             "",
	})
	if err != nil || !ok {
		t.Fatalf("expected match, ok=%v err=%v", ok, err)
	}
	ok, _ = f.Match(map[string]any{
		"remediation_kind":   "",
		"remediation_target": "",
		"cause":              "The destination namespace does not exist.",
		"action":             "",
	})
	if ok {
		t.Fatal("expected no match without structured remediation")
	}
}

func TestCachedObjectFilter_HitsCache(t *testing.T) {
	expr := `kind == "Service"`
	f1, err := CachedObjectFilter(expr)
	if err != nil {
		t.Fatal(err)
	}
	f2, err := CachedObjectFilter(expr)
	if err != nil {
		t.Fatal(err)
	}
	if f1 != f2 {
		t.Fatal("expected same instance from cache")
	}
}

func TestCachedFilters_EmptyExpressionReturnsNil(t *testing.T) {
	if f, err := CachedObjectFilter(""); f != nil || err != nil {
		t.Fatalf("empty obj: f=%v err=%v", f, err)
	}
	if f, err := CachedIssueFilter(""); f != nil || err != nil {
		t.Fatalf("empty issue: f=%v err=%v", f, err)
	}
}
