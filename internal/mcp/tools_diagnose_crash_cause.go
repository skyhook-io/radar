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

var fatalCrashLogPattern = regexp.MustCompile(`(?i)(\bpanic:|\bFATAL\b|\bException\b|\bTraceback\b|\bCRITICAL\b)`)

type diagnoseCrashCause struct {
	Pods       []string `json:"pods"`
	Container  string   `json:"container"`
	Reason     string   `json:"reason,omitempty"`
	ExitCode   int32    `json:"exitCode"`
	LogLine    string   `json:"logLine"`
	LogSource  string   `json:"logSource"`
	BestEffort bool     `json:"bestEffort,omitempty"`
}

type diagnoseLogKey struct {
	pod       string
	container string
}

type diagnoseCrashCauseKey struct {
	container  string
	reason     string
	exitCode   int32
	logLine    string
	logSource  string
	bestEffort bool
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
			line := selectCrashLogLine(logs.Logs.Lines)
			if line == "" {
				continue
			}
			line = truncateCrashLogLine(line, maxCrashCauseRunes)
			key := diagnoseCrashCauseKey{
				container:  status.Name,
				reason:     term.Reason,
				exitCode:   term.ExitCode,
				logLine:    line,
				logSource:  logSource,
				bestEffort: logs.Logs.Fallback,
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
				Pods:       []string{pod.Name},
				Container:  status.Name,
				Reason:     term.Reason,
				ExitCode:   term.ExitCode,
				LogLine:    line,
				LogSource:  logSource,
				BestEffort: logs.Logs.Fallback,
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

func selectCrashLogLine(lines []string) string {
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" && !isOmittedLogSentinel(line) && fatalCrashLogPattern.MatchString(line) {
			return line
		}
	}
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line != "" && !isOmittedLogSentinel(line) {
			return line
		}
	}
	return ""
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
