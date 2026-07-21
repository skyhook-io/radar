package context

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestFilterLogs_ErrorLines(t *testing.T) {
	lines := []string{
		"2024-01-15 INFO Starting application",
		"2024-01-15 INFO Connecting to database",
		"2024-01-15 ERROR Failed to connect to database: connection refused",
		"2024-01-15 INFO Retrying...",
		"2024-01-15 FATAL Unable to start: database unavailable",
	}
	input := strings.Join(lines, "\n")

	result := FilterLogs(input)

	if result.Fallback {
		t.Error("Expected error pattern match, got fallback")
	}
	if result.TotalLines != 5 {
		t.Errorf("Expected TotalLines=5, got %d", result.TotalLines)
	}
	if len(result.Lines) != 2 {
		t.Errorf("Expected 2 matched lines, got %d: %v", len(result.Lines), result.Lines)
	}
	if !strings.Contains(result.Lines[0], "ERROR") {
		t.Errorf("Expected ERROR line, got: %s", result.Lines[0])
	}
	if !strings.Contains(result.Lines[1], "FATAL") {
		t.Errorf("Expected FATAL line, got: %s", result.Lines[1])
	}
}

func TestFilterLogs_WarningLines(t *testing.T) {
	lines := []string{
		"2024-01-15 INFO ok",
		"2024-01-15 WARN disk usage high",
		"2024-01-15 WARNING memory pressure",
	}
	input := strings.Join(lines, "\n")

	result := FilterLogs(input)

	if result.Fallback {
		t.Error("Expected match, got fallback")
	}
	if len(result.Lines) != 2 {
		t.Errorf("Expected 2 lines, got %d", len(result.Lines))
	}
}

func TestFilterLogs_JSONLevelError(t *testing.T) {
	lines := []string{
		`{"level":"info","msg":"starting"}`,
		`{"level":"error","msg":"connection failed"}`,
		`{"level":"info","msg":"retrying"}`,
	}
	input := strings.Join(lines, "\n")

	result := FilterLogs(input)

	if result.Fallback {
		t.Error("Expected match, got fallback")
	}
	if len(result.Lines) != 1 {
		t.Errorf("Expected 1 line, got %d", len(result.Lines))
	}
}

func TestFilterLogs_FallbackWhenNoErrors(t *testing.T) {
	lines := make([]string, 30)
	for i := range lines {
		lines[i] = fmt.Sprintf("2024-01-15 INFO normal log line %d", i)
	}
	input := strings.Join(lines, "\n")

	result := FilterLogs(input)

	if !result.Fallback {
		t.Error("Expected fallback mode")
	}
	if result.TotalLines != 30 {
		t.Errorf("Expected TotalLines=30, got %d", result.TotalLines)
	}
	if result.MatchedLines != 0 {
		t.Errorf("Expected MatchedLines=0, got %d", result.MatchedLines)
	}
	if len(result.Lines) != 20 {
		t.Errorf("Expected 20 fallback lines, got %d", len(result.Lines))
	}
}

func TestFilterLogs_TruncatesLargeMatchSet(t *testing.T) {
	lines := make([]string, 100)
	for i := range lines {
		lines[i] = fmt.Sprintf("ERROR failure number %d", i)
	}
	input := strings.Join(lines, "\n")

	result := FilterLogs(input)

	if result.Fallback {
		t.Error("Should not be fallback")
	}
	// 30 head + 1 omitted line + 20 tail = 51
	if len(result.Lines) > 51 {
		t.Errorf("Expected at most 51 lines after truncation, got %d", len(result.Lines))
	}
	if result.MatchedLines != 100 {
		t.Errorf("Expected MatchedLines=100 before truncation, got %d", result.MatchedLines)
	}
	// Check that omitted message is present
	found := false
	for _, line := range result.Lines {
		if strings.Contains(line, "omitted") {
			found = true
			break
		}
	}
	if !found {
		t.Error("Expected omitted lines indicator")
	}
}

func TestFilterLogs_DeduplicatesIdenticalLines(t *testing.T) {
	lines := make([]string, 10)
	for i := range lines {
		lines[i] = "ERROR same error repeated"
	}
	input := strings.Join(lines, "\n")

	result := FilterLogs(input)

	if len(result.Lines) != 1 {
		t.Errorf("Expected 1 deduplicated line, got %d: %v", len(result.Lines), result.Lines)
	}
	if !strings.Contains(result.Lines[0], "repeated x10") {
		t.Errorf("Expected repeat count, got: %s", result.Lines[0])
	}
}

func TestFilterLogs_EmptyInput(t *testing.T) {
	result := FilterLogs("")
	if result.TotalLines != 0 {
		t.Errorf("Expected TotalLines=0, got %d", result.TotalLines)
	}
	if len(result.Lines) != 0 {
		t.Errorf("Expected 0 lines, got %d", len(result.Lines))
	}
}

func TestFilterLogs_PanicAndTraceback(t *testing.T) {
	lines := []string{
		"goroutine 1 [running]:",
		"panic: runtime error: index out of range",
		"  /app/main.go:42",
	}
	input := strings.Join(lines, "\n")

	result := FilterLogs(input)

	if result.Fallback {
		t.Error("Expected match on panic:")
	}
	if len(result.Lines) != 1 {
		t.Errorf("Expected 1 matched line (panic:), got %d: %v", len(result.Lines), result.Lines)
	}
}

func TestFilterLogs_RedactsSecrets(t *testing.T) {
	lines := []string{
		"ERROR failed to auth with key sk-abc123def456ghi789jkl012mno345pqr678stu901",
	}
	input := strings.Join(lines, "\n")

	result := FilterLogs(input)

	if strings.Contains(result.Lines[0], "sk-abc123") {
		t.Errorf("Secret not redacted in log line: %s", result.Lines[0])
	}
}

func TestFilterLogsByPattern_ReturnsNonDiagnosticMatches(t *testing.T) {
	lines := []string{
		"INFO checkout request ok",
		"INFO cart request slow",
		"INFO recommendation request ok",
	}
	input := strings.Join(lines, "\n")

	result, err := FilterLogsByPattern(input, "cart")
	if err != nil {
		t.Fatalf("FilterLogsByPattern returned error: %v", err)
	}
	if result.TotalLines != 3 {
		t.Errorf("Expected TotalLines=3 before grep, got %d", result.TotalLines)
	}
	if result.MatchedLines != 1 {
		t.Errorf("Expected MatchedLines=1, got %d", result.MatchedLines)
	}
	if result.Fallback {
		t.Error("Explicit grep must not use diagnostic fallback")
	}
	if len(result.Lines) != 1 || !strings.Contains(result.Lines[0], "cart request slow") {
		t.Fatalf("Expected cart line, got %#v", result.Lines)
	}
}

func TestFilterLogsByPattern_DotReturnsMixedLogs(t *testing.T) {
	input := strings.Join([]string{
		"INFO Kubernetes API proxy started on port 16443",
		"WARN deprecated option",
		"INFO in-cluster detection complete",
	}, "\n")

	result, err := FilterLogsByPattern(input, ".")
	if err != nil {
		t.Fatalf("FilterLogsByPattern returned error: %v", err)
	}
	if result.TotalLines != 3 || result.MatchedLines != 3 || result.Fallback {
		t.Fatalf("Unexpected metadata: %#v", result)
	}
	if !reflect.DeepEqual(result.Lines, strings.Split(input, "\n")) {
		t.Fatalf("Expected every line in order, got %#v", result.Lines)
	}
}

func TestFilterLogsByPattern_TargetedMatch(t *testing.T) {
	input := strings.Join([]string{
		"INFO Hidden namespaces: {kube-system}",
		"INFO starting server",
		"INFO Hidden namespaces: {monitoring}",
	}, "\n")

	result, err := FilterLogsByPattern(input, "Hidden")
	if err != nil {
		t.Fatalf("FilterLogsByPattern returned error: %v", err)
	}
	want := []string{
		"INFO Hidden namespaces: {kube-system}",
		"INFO Hidden namespaces: {monitoring}",
	}
	if result.TotalLines != 3 || result.MatchedLines != 2 || result.Fallback {
		t.Fatalf("Unexpected metadata: %#v", result)
	}
	if !reflect.DeepEqual(result.Lines, want) {
		t.Fatalf("Expected Hidden lines, got %#v", result.Lines)
	}
}

func TestFilterLogsByPattern_ZeroMatches(t *testing.T) {
	result, err := FilterLogsByPattern("INFO ready\nINFO serving", "missing")
	if err != nil {
		t.Fatalf("FilterLogsByPattern returned error: %v", err)
	}
	if result.TotalLines != 2 || result.MatchedLines != 0 || result.Fallback {
		t.Fatalf("Unexpected metadata: %#v", result)
	}
	if result.Lines == nil || len(result.Lines) != 0 {
		t.Fatalf("Expected an empty lines array, got %#v", result.Lines)
	}
}

func TestFilterLogsByPattern_TruncatesMatches(t *testing.T) {
	lines := make([]string, 60)
	for i := range lines {
		lines[i] = fmt.Sprintf("selected line %d", i)
	}

	result, err := FilterLogsByPattern(strings.Join(lines, "\n"), "selected")
	if err != nil {
		t.Fatalf("FilterLogsByPattern returned error: %v", err)
	}
	if result.MatchedLines != 60 || len(result.Lines) != 51 {
		t.Fatalf("Expected 60 matches bounded to 51 response rows, got %#v", result)
	}
	if result.Lines[0] != "selected line 0" || result.Lines[29] != "selected line 29" {
		t.Fatalf("Unexpected head: %#v", result.Lines[:30])
	}
	if result.Lines[30] != "... (10 lines omitted) ..." {
		t.Fatalf("Unexpected omission marker: %q", result.Lines[30])
	}
	if result.Lines[31] != "selected line 40" || result.Lines[50] != "selected line 59" {
		t.Fatalf("Unexpected tail: %#v", result.Lines[31:])
	}
}

func TestFilterLogsByPattern_DeduplicatesAfterTruncation(t *testing.T) {
	lines := make([]string, 60)
	for i := range lines {
		lines[i] = "selected duplicate"
	}

	result, err := FilterLogsByPattern(strings.Join(lines, "\n"), "selected")
	if err != nil {
		t.Fatalf("FilterLogsByPattern returned error: %v", err)
	}
	want := []string{
		"selected duplicate (repeated x30)",
		"... (10 lines omitted) ...",
		"selected duplicate (repeated x20)",
	}
	if result.MatchedLines != 60 || !reflect.DeepEqual(result.Lines, want) {
		t.Fatalf("Unexpected result: %#v", result)
	}
}

func TestFilterLogsByPattern_RedactsSecrets(t *testing.T) {
	result, err := FilterLogsByPattern(
		"INFO auth key sk-abc123def456ghi789jkl012mno345pqr678stu901",
		"auth",
	)
	if err != nil {
		t.Fatalf("FilterLogsByPattern returned error: %v", err)
	}
	if len(result.Lines) != 1 || strings.Contains(result.Lines[0], "sk-abc123") {
		t.Fatalf("Secret not redacted in grep result: %#v", result.Lines)
	}
}

func TestFilterLogsByPattern_EmptyInput(t *testing.T) {
	result, err := FilterLogsByPattern("", ".")
	if err != nil {
		t.Fatalf("FilterLogsByPattern returned error: %v", err)
	}
	if result.TotalLines != 0 || result.MatchedLines != 0 || result.Fallback {
		t.Fatalf("Unexpected metadata: %#v", result)
	}
	if result.Lines == nil || len(result.Lines) != 0 {
		t.Fatalf("Expected an empty lines array, got %#v", result.Lines)
	}
}

func TestFilterLogsByPattern_WhitespaceSemantics(t *testing.T) {
	t.Run("meaningful whitespace", func(t *testing.T) {
		result, err := FilterLogsByPattern("frame\n  frame", `^  frame`)
		if err != nil {
			t.Fatalf("FilterLogsByPattern returned error: %v", err)
		}
		if !reflect.DeepEqual(result.Lines, []string{"  frame"}) {
			t.Fatalf("Expected whitespace-sensitive match, got %#v", result.Lines)
		}
	})

	t.Run("blank pattern uses diagnostic mode", func(t *testing.T) {
		result, err := FilterLogsByPattern("INFO ready\nERROR failed", "   ")
		if err != nil {
			t.Fatalf("FilterLogsByPattern returned error: %v", err)
		}
		if result.TotalLines != 2 || result.MatchedLines != 1 || result.Fallback {
			t.Fatalf("Unexpected metadata: %#v", result)
		}
		if !reflect.DeepEqual(result.Lines, []string{"ERROR failed"}) {
			t.Fatalf("Expected diagnostic filtering, got %#v", result.Lines)
		}
	})
}

func TestFilterLogsByPattern_InvalidRegex(t *testing.T) {
	_, err := FilterLogsByPattern("INFO ok", "[")
	if err == nil {
		t.Fatal("Expected invalid regex error")
	}
}
