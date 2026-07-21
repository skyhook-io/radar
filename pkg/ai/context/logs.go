package context

import (
	"fmt"
	"regexp"
	"strings"
)

// FilteredLogs contains selected, bounded log lines.
type FilteredLogs struct {
	Lines        []string `json:"lines"`
	TotalLines   int      `json:"totalLines"`
	MatchedLines int      `json:"matchedLines"`
	Fallback     bool     `json:"fallback"` // true if no error patterns matched, using last N raw lines
}

var errorPatterns = regexp.MustCompile(`(?i)(\bERROR\b|\bFATAL\b|\bWARN(?:ING)?\b|\bException\b|\bpanic:\b|\bTraceback\b|\bCRITICAL\b|"level"\s*:\s*"(?:error|fatal|warn)")`)

const (
	maxFilteredLines = 50
	headLines        = 30
	tailLines        = 20
	fallbackLines    = 20
)

// FilterLogs extracts diagnostically relevant log lines (errors, warnings, panics).
// If no error patterns match, falls back to the last 20 raw lines.
func FilterLogs(rawLogs string) FilteredLogs {
	if rawLogs == "" {
		return FilteredLogs{}
	}

	allLines := splitLogLines(rawLogs)
	totalLines := len(allLines)

	// Match error/warning patterns
	var matched []string
	for _, line := range allLines {
		if errorPatterns.MatchString(line) {
			matched = append(matched, line)
		}
	}

	if len(matched) == 0 {
		// Fallback: include last N raw lines
		start := 0
		if totalLines > fallbackLines {
			start = totalLines - fallbackLines
		}
		lines := deduplicateStackTraces(allLines[start:])
		return FilteredLogs{
			Lines:        redactLogLines(lines),
			TotalLines:   totalLines,
			MatchedLines: 0,
			Fallback:     true,
		}
	}

	return formatMatchedLogs(totalLines, matched)
}

func formatMatchedLogs(totalLines int, matched []string) FilteredLogs {
	matchedLines := len(matched)
	if len(matched) > maxFilteredLines {
		truncated := make([]string, 0, maxFilteredLines+1)
		truncated = append(truncated, matched[:headLines]...)
		truncated = append(truncated, fmt.Sprintf("... (%d lines omitted) ...", len(matched)-maxFilteredLines))
		truncated = append(truncated, matched[len(matched)-tailLines:]...)
		matched = truncated
	}

	lines := deduplicateStackTraces(matched)
	return FilteredLogs{
		Lines:        redactLogLines(lines),
		TotalLines:   totalLines,
		MatchedLines: matchedLines,
		Fallback:     false,
	}
}

// FilterLogsByPattern uses pattern instead of the diagnostic filter when set.
func FilterLogsByPattern(rawLogs, pattern string) (FilteredLogs, error) {
	if strings.TrimSpace(pattern) == "" {
		return FilterLogs(rawLogs), nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return FilteredLogs{}, err
	}
	allLines := splitLogLines(rawLogs)
	matched := make([]string, 0)
	for _, line := range allLines {
		if re.MatchString(line) {
			matched = append(matched, line)
		}
	}
	return formatMatchedLogs(len(allLines), matched), nil
}

func splitLogLines(rawLogs string) []string {
	if rawLogs == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(rawLogs, "\n"), "\n")
}

// deduplicateStackTraces collapses identical consecutive lines with a repeat count.
func deduplicateStackTraces(lines []string) []string {
	if len(lines) == 0 {
		return lines
	}

	var result []string
	prev := lines[0]
	count := 1

	for i := 1; i < len(lines); i++ {
		if lines[i] == prev {
			count++
		} else {
			if count > 1 {
				result = append(result, fmt.Sprintf("%s (repeated x%d)", prev, count))
			} else {
				result = append(result, prev)
			}
			prev = lines[i]
			count = 1
		}
	}
	// Flush last
	if count > 1 {
		result = append(result, fmt.Sprintf("%s (repeated x%d)", prev, count))
	} else {
		result = append(result, prev)
	}

	return result
}

func redactLogLines(lines []string) []string {
	result := make([]string, len(lines))
	for i, line := range lines {
		result[i] = RedactSecrets(line)
	}
	return result
}
