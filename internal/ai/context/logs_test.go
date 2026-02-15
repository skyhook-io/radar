package context

import (
	"fmt"
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
