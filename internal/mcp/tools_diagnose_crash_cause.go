package mcp

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/skyhook-io/radar/pkg/health"
)

const (
	maxDiagnoseCrashCauses = 10
	maxCrashCauseRunes     = 500
	maxCrashCauseBytes     = 8 * 1024
)

// Crash-class line shapes: runtime panic/abort markers (Go, Rust, JVM, Python,
// signals) plus conventionally-named exception classes ("KeyError:",
// "Caused by: java.net.ConnectException:", "(NoMethodError)"). The bare
// Exception word and the class-name alternative are case-sensitive: real
// crash headers capitalize ("Exception in thread ...", "KeyError:"), while
// lowercase prose ("caught exception in background thread", "an error:") is
// commentary that must not outrank a later real crash header. .NET's
// "Unhandled exception." still qualifies via its exception class name.
var fatalCrashLogPattern = regexp.MustCompile(`(?i)(\bpanic:|\bpanicked at\b|\bFATAL\b|\bTraceback\b|\bCRITICAL\b|\bSIG(?:SEGV|ABRT|BUS|ILL|FPE)\b|segmentation fault|(?-i:\bException\b|\b[A-Z][A-Za-z]*(?:Error|Exception)[:)]))`)

// How the crash-evidence line was chosen — carried on the response so the
// agent can weigh the line's confidence itself instead of treating every
// selection as a verified cause.
const (
	crashLineFatalPattern    = "fatal_pattern"         // matched a crash-class marker
	crashLineHeaderOnly      = "traceback_header_only" // a traceback occurred but its informative line was not captured; read the full logs
	crashLineLastMatchedLine = "last_matched_line"     // no crash-class marker; latest line matching the log filter (may be ERROR or WARN)
	crashLineLogTail         = "log_tail"              // nothing matched the log filter at all; raw tail
)

type diagnoseCrashCause struct {
	Pods      []string `json:"pods"`
	Container string   `json:"container"`
	Reason    string   `json:"reason,omitempty"`
	ExitCode  int32    `json:"exitCode"`
	LogLine   string   `json:"logLine"`
	LogSource string   `json:"logSource"`
	// LineSelection states how LogLine was picked (fatal_pattern,
	// traceback_header_only, last_matched_line, log_tail) — descending
	// confidence.
	LineSelection string `json:"logLineSelection"`
}

type diagnoseLogKey struct {
	pod       string
	container string
}

type diagnoseCrashCauseKey struct {
	container     string
	reason        string
	exitCode      int32
	logLine       string
	logSource     string
	lineSelection string
}

func crashCauseForDiagnose(pods []*corev1.Pod, current, previous []podLogEntry, now time.Time) ([]diagnoseCrashCause, bool) {
	currentByContainer := indexDiagnoseLogs(current)
	previousByContainer := indexDiagnoseLogs(previous)
	causes := make([]diagnoseCrashCause, 0)
	causeIndex := make(map[diagnoseCrashCauseKey]int)
	truncated := false
	usedBytes := 2
	orderedPods := append([]*corev1.Pod(nil), pods...)
	sort.SliceStable(orderedPods, func(i, j int) bool {
		if orderedPods[i] == nil {
			return false
		}
		if orderedPods[j] == nil {
			return true
		}
		return orderedPods[i].Name < orderedPods[j].Name
	})

	for _, pod := range orderedPods {
		if pod == nil {
			continue
		}
		for _, status := range health.ActiveCrashLoopContainerStatuses(pod, now) {
			term := status.LastTerminationState.Terminated
			logs := previousByContainer[diagnoseLogKey{pod: pod.Name, container: status.Name}]
			logSource := "previous"
			if currentTerm := status.State.Terminated; currentTerm != nil {
				if currentTerm.Reason == "OOMKilled" {
					continue
				}
				if isCrashTermination(currentTerm) {
					term = currentTerm
					logs = currentByContainer[diagnoseLogKey{pod: pod.Name, container: status.Name}]
					logSource = "current"
				}
			}
			if term == nil || logs == nil || logs.Error != "" {
				continue
			}
			line, selection := selectCrashLogLine(logs.Logs.Lines, logs.Logs.Fallback)
			if line == "" {
				continue
			}
			line = truncateCrashLogLine(line, maxCrashCauseRunes)
			key := diagnoseCrashCauseKey{
				container:     status.Name,
				reason:        term.Reason,
				exitCode:      term.ExitCode,
				logLine:       line,
				logSource:     logSource,
				lineSelection: selection,
			}
			if i, ok := causeIndex[key]; ok {
				updated := causes[i]
				updated.Pods = append(append([]string(nil), updated.Pods...), pod.Name)
				delta := diagnoseCrashCauseJSONSize(updated) - diagnoseCrashCauseJSONSize(causes[i])
				if usedBytes+delta > maxCrashCauseBytes {
					truncated = true
					continue
				}
				causes[i] = updated
				usedBytes += delta
				continue
			}
			cause := diagnoseCrashCause{
				Pods:          []string{pod.Name},
				Container:     status.Name,
				Reason:        term.Reason,
				ExitCode:      term.ExitCode,
				LogLine:       line,
				LogSource:     logSource,
				LineSelection: selection,
			}
			rowBytes := diagnoseCrashCauseJSONSize(cause)
			if len(causes) > 0 {
				rowBytes++
			}
			if len(causes) == maxDiagnoseCrashCauses || usedBytes+rowBytes > maxCrashCauseBytes {
				truncated = true
				continue
			}
			causeIndex[key] = len(causes)
			causes = append(causes, cause)
			usedBytes += rowBytes
		}
	}
	return causes, truncated
}

func diagnoseCrashCauseJSONSize(cause diagnoseCrashCause) int {
	encoded, _ := json.Marshal(cause)
	return len(encoded)
}

func indexDiagnoseLogs(entries []podLogEntry) map[diagnoseLogKey]*podLogEntry {
	indexed := make(map[diagnoseLogKey]*podLogEntry, len(entries))
	for i := range entries {
		entry := &entries[i]
		indexed[diagnoseLogKey{pod: entry.Pod, container: entry.Container}] = entry
	}
	return indexed
}

func isCrashTermination(term *corev1.ContainerStateTerminated) bool {
	if term == nil || term.Reason == "OOMKilled" {
		return false
	}
	return term.ExitCode != 0 || term.Reason == "Error" || term.Reason == "CrashLoopBackOff"
}

// selectCrashLogLine picks the crash-evidence line and reports how it was
// chosen (the crashLine* tokens, descending confidence).
func selectCrashLogLine(lines []string, unfiltered bool) (string, string) {
	// Earliest fatal-class match wins: for Go panics and JVM exceptions the
	// first matched line is the block header that names the failure, while
	// later matches are nested exceptions or cleanup noise. The exception is
	// Python's bare "Traceback (most recent call last):" header, which names
	// nothing — there the informative line sits LATER (a chained traceback's
	// real error, or the exception on the block's last line), so bare headers
	// only win when nothing after them qualifies.
	header, headerIdx := "", -1
	for i, raw := range lines {
		line := strings.TrimSpace(raw)
		if line == "" || isOmittedLogSentinel(line) || !fatalCrashLogPattern.MatchString(line) {
			continue
		}
		if isBareTracebackHeader(line) {
			if header == "" {
				header, headerIdx = line, i
			}
			continue
		}
		return line, crashLineFatalPattern
	}
	tailSelection := crashLineLastMatchedLine
	if unfiltered {
		tailSelection = crashLineLogTail
	}
	// Generic tail fallback; with a bare header, only lines after it qualify —
	// a line preceding the traceback is unrelated. Lines after the header get
	// no special block claim: in the filtered stream they are just later
	// matched lines, not traceback content.
	for i := len(lines) - 1; i > headerIdx; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" && !isOmittedLogSentinel(line) {
			return line, tailSelection
		}
	}
	// Only the bare header matched: a crash signature exists but the log
	// filter did not capture its informative line — say so instead of
	// presenting the header as high-confidence evidence.
	if header != "" {
		return header, crashLineHeaderOnly
	}
	return "", ""
}

// isBareTracebackHeader matches Python's "Traceback (most recent call last):"
// line (possibly timestamp-prefixed), which carries no failure information.
// FilterLogs collapses consecutive identical lines by appending a
// " (repeated xN…)" annotation — a decorated header is still a bare header,
// and crashloops are exactly where headers repeat.
func isBareTracebackHeader(line string) bool {
	const marker = "(most recent call last):"
	idx := strings.Index(line, marker)
	if idx < 0 || !strings.Contains(line[:idx], "Traceback ") {
		return false
	}
	rest := strings.TrimSpace(line[idx+len(marker):])
	return rest == "" || strings.HasPrefix(rest, "(repeated ")
}

func isOmittedLogSentinel(line string) bool {
	return strings.HasPrefix(line, "... (") && strings.HasSuffix(line, " lines omitted) ...")
}

func truncateCrashLogLine(line string, maxRunes int) string {
	runes := []rune(line)
	if len(runes) <= maxRunes {
		return line
	}
	return string(runes[:maxRunes-1]) + "…"
}
