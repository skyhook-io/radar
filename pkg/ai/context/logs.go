package context

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

// FilteredLogs contains selected, bounded log lines.
type FilteredLogs struct {
	Lines        []string `json:"lines"`
	TotalLines   int      `json:"totalLines"`
	MatchedLines int      `json:"matchedLines"`
	Fallback     bool     `json:"fallback"` // true if no error patterns matched, using last N raw lines
}

type logTimestampPattern struct {
	re     *regexp.Regexp
	layout string
}

var (
	// Beyond level words and structured-log levels: `panicked at` is Rust's
	// stable panic marker; SIG*/segmentation fault mark signal deaths; the
	// case-sensitive class alternative catches conventionally-named exception
	// finals ("KeyError: ...", "Caused by: java.net.ConnectException: ...",
	// "(NoMethodError)") that carry no standalone level word — case-sensitive
	// so lowercase prose such as "an error:" never matches.
	errorPatterns        = regexp.MustCompile(`(?i)(\bERROR\b|\bFATAL\b|\bWARN(?:ING)?\b|\bException\b|\bpanic:|\bpanicked at\b|\bTraceback\b|\bCRITICAL\b|\bSIG(?:SEGV|ABRT|BUS|ILL|FPE)\b|segmentation fault|"level"\s*:\s*"(?:error|fatal|warn)"|(?-i:\b[A-Z][A-Za-z]*(?:Error|Exception)[:)]))`)
	rfc3339LogPrefix     = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})) (.*)$`)
	bracketedLogPrefix   = regexp.MustCompile(`^\[(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2}))\] (.*)$`)
	spaceDateLogPrefix   = regexp.MustCompile(`^(\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}) (.*)$`)
	logTimestampPatterns = []logTimestampPattern{
		{rfc3339LogPrefix, time.RFC3339Nano},
		{bracketedLogPrefix, time.RFC3339Nano},
		{spaceDateLogPrefix, "2006/01/02 15:04:05"},
	}
)

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
	runs := deduplicateLogRuns(matched, true)
	formatted := make([]string, 0, len(runs))
	if len(runs) > maxFilteredLines {
		omittedLines := 0
		for _, run := range runs[headLines : len(runs)-tailLines] {
			omittedLines += run.occurrences
		}
		truncated := make([]string, 0, maxFilteredLines+1)
		for _, run := range runs[:headLines] {
			truncated = append(truncated, run.text)
		}
		truncated = append(truncated, fmt.Sprintf("... (%d lines omitted) ...", omittedLines))
		for _, run := range runs[len(runs)-tailLines:] {
			truncated = append(truncated, run.text)
		}
		formatted = truncated
	} else {
		for _, run := range runs {
			formatted = append(formatted, run.text)
		}
	}

	// The pre-pass deliberately leaves raw duplicates for the legacy post-cap collapse.
	lines := deduplicateStackTraces(formatted)
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

type normalizedLogLine struct {
	raw          string
	key          string
	timestamp    time.Time
	hasTimestamp bool
}

type deduplicatedLogRun struct {
	text        string
	occurrences int
}

func normalizeLogLine(line string) normalizedLogLine {
	for _, pattern := range logTimestampPatterns {
		match := pattern.re.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		timestamp, err := time.Parse(pattern.layout, match[1])
		if err == nil {
			return normalizedLogLine{raw: line, key: match[2], timestamp: timestamp, hasTimestamp: true}
		}
	}
	return normalizedLogLine{raw: line, key: line}
}

func deduplicateLogRuns(lines []string, timestampedOnly bool) []deduplicatedLogRun {
	if len(lines) == 0 {
		return nil
	}

	result := make([]deduplicatedLogRun, 0, len(lines))
	first := normalizeLogLine(lines[0])
	last := first
	count := 1

	flush := func() {
		text := first.raw
		if count > 1 {
			if first.hasTimestamp {
				text = fmt.Sprintf("%s (repeated ×%d, %s→%s)", first.raw, count, first.timestamp.Format("15:04:05"), last.timestamp.Format("15:04:05"))
			} else {
				text = fmt.Sprintf("%s (repeated x%d)", first.raw, count)
			}
		}
		result = append(result, deduplicatedLogRun{text: text, occurrences: count})
	}

	for _, line := range lines[1:] {
		current := normalizeLogLine(line)
		sameRun := current.key == last.key && current.hasTimestamp == last.hasTimestamp
		if timestampedOnly {
			sameRun = sameRun && current.hasTimestamp
		}
		if sameRun {
			last = current
			count++
			continue
		}
		flush()
		first = current
		last = current
		count = 1
	}
	flush()
	return result
}

// deduplicateStackTraces strips only validated timestamp prefixes before comparison.
func deduplicateStackTraces(lines []string) []string {
	runs := deduplicateLogRuns(lines, false)
	result := make([]string, 0, len(runs))
	for _, run := range runs {
		result = append(result, run.text)
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
