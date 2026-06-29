package helmhistory

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	KindReleaseFailed     OperationKind = "release_failed"
	KindUpgradeFailed     OperationKind = "upgrade_failed"
	KindUpgradeRolledBack OperationKind = "upgrade_rolled_back"
	KindRollback          OperationKind = "rollback"
	KindPending           OperationKind = "pending"

	StatusFailed     OperationStatus = "failed"
	StatusRolledBack OperationStatus = "rolled_back"
	StatusCompleted  OperationStatus = "completed"
	StatusStuck      OperationStatus = "stuck_pending"

	ConfidenceHigh   Confidence = "high"
	ConfidenceMedium Confidence = "medium"
	ConfidenceLow    Confidence = "low"

	SourceStatus  Source = "helm_status"
	SourceHistory Source = "helm_history"
)

const defaultPendingStuckAfter = 10 * time.Minute

var (
	rollbackToPattern     = regexp.MustCompile(`(?i)^rollback to ([0-9]+)(?:\b|$)`)
	upgradeFailurePattern = regexp.MustCompile(`(?i)^upgrade(?:\s+"[^"]+")?\s+failed:`)
)

type OperationKind string

type OperationStatus string

type Confidence string

type Source string

type Revision struct {
	Revision    int
	Status      string
	Chart       string
	AppVersion  string
	Description string
	Updated     time.Time
}

type Operation struct {
	Kind               OperationKind   `json:"kind"`
	Status             OperationStatus `json:"status"`
	Source             Source          `json:"source"`
	Confidence         Confidence      `json:"confidence"`
	Message            string          `json:"message"`
	Evidence           string          `json:"evidence,omitempty"`
	FailureDescription string          `json:"failureDescription,omitempty"`
	Revision           int             `json:"revision,omitempty"`
	FailedRevision     int             `json:"failedRevision,omitempty"`
	RollbackRevision   int             `json:"rollbackRevision,omitempty"`
	TargetRevision     int             `json:"targetRevision,omitempty"`
	PendingStatus      string          `json:"pendingStatus,omitempty"`
	Updated            time.Time       `json:"updated,omitempty"`
}

type Options struct {
	PendingStuckAfter time.Duration
	MaxOperations     int
	Now               time.Time
}

type Analysis struct {
	LastOperation *Operation  `json:"lastOperation,omitempty"`
	Operations    []Operation `json:"operations,omitempty"`
}

func Analyze(releaseName string, currentRevision int, revisions []Revision, opts Options) Analysis {
	if len(revisions) == 0 {
		return Analysis{}
	}

	pendingStuckAfter := opts.PendingStuckAfter
	if pendingStuckAfter <= 0 {
		pendingStuckAfter = defaultPendingStuckAfter
	}
	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}

	ordered := append([]Revision(nil), revisions...)
	sort.Slice(ordered, func(i, j int) bool {
		return ordered[i].Revision < ordered[j].Revision
	})

	var ops []Operation
	for i := 0; i < len(ordered); i++ {
		rev := ordered[i]
		status := normalizeStatus(rev.Status)
		if status == "failed" {
			if i+1 < len(ordered) {
				next := ordered[i+1]
				if next.Revision == rev.Revision+1 && isCompletedRevisionStatus(next.Status) {
					if target, ok := rollbackTarget(next.Description); ok {
						op := rolledBackOperation(rev, next, target)
						ops = append(ops, op)
						i++
						continue
					}
				}
			}
			ops = append(ops, failedOperation(rev, SourceHistory))
			continue
		}
		if isCompletedRevisionStatus(status) {
			if target, ok := rollbackTarget(rev.Description); ok {
				ops = append(ops, rollbackOperation(rev, target))
			}
		}
	}

	current, hasCurrent := findCurrent(ordered, currentRevision)
	resolvedCurrentRevision := currentRevision
	if hasCurrent {
		resolvedCurrentRevision = current.Revision
	}
	var live *Operation
	if hasCurrent {
		switch status := normalizeStatus(current.Status); {
		case status == "failed":
			op := failedOperation(current, SourceStatus)
			live = &op
		case isPending(status) && !current.Updated.IsZero() && now.Sub(current.Updated) >= pendingStuckAfter:
			op := pendingOperation(current, now.Sub(current.Updated))
			live = &op
		}
	}
	if live != nil && live.Status == StatusFailed {
		ops = withoutDuplicateLiveFailure(ops, *live)
	}

	sort.Slice(ops, func(i, j int) bool {
		return operationRevision(ops[i]) > operationRevision(ops[j])
	})
	if opts.MaxOperations > 0 && len(ops) > opts.MaxOperations {
		ops = ops[:opts.MaxOperations]
	}

	var last *Operation
	if live != nil {
		last = live
	} else {
		for i := range ops {
			if operationRevision(ops[i]) == resolvedCurrentRevision && (ops[i].Kind == KindUpgradeRolledBack || ops[i].Kind == KindRollback) {
				op := ops[i]
				last = &op
				break
			}
		}
	}

	return Analysis{LastOperation: last, Operations: ops}
}

func rolledBackOperation(failed, rollback Revision, target int) Operation {
	return Operation{
		Kind:               KindUpgradeRolledBack,
		Status:             StatusRolledBack,
		Source:             SourceHistory,
		Confidence:         ConfidenceMedium,
		Message:            fmt.Sprintf("Upgrade failed at rev %d; Helm rolled back to rev %d as rev %d.", failed.Revision, target, rollback.Revision),
		Evidence:           fmt.Sprintf("failed revision %d followed by rollback revision %d", failed.Revision, rollback.Revision),
		FailureDescription: strings.TrimSpace(failed.Description),
		FailedRevision:     failed.Revision,
		RollbackRevision:   rollback.Revision,
		TargetRevision:     target,
		Updated:            rollback.Updated,
	}
}

func failedOperation(rev Revision, source Source) Operation {
	kind := KindReleaseFailed
	message := fmt.Sprintf("Release failed at rev %d.", rev.Revision)
	if isUpgradeFailureDescription(rev.Description) {
		kind = KindUpgradeFailed
		message = fmt.Sprintf("Upgrade failed at rev %d.", rev.Revision)
	}
	if rev.Description != "" {
		message = message + " " + rev.Description
	}
	evidence := "Helm history revision status is failed"
	if source == SourceStatus {
		evidence = "latest Helm revision status is failed"
	}
	return Operation{
		Kind:       kind,
		Status:     StatusFailed,
		Source:     source,
		Confidence: ConfidenceHigh,
		Message:    message,
		Evidence:   evidence,
		Revision:   rev.Revision,
		Updated:    rev.Updated,
	}
}

func rollbackOperation(rev Revision, target int) Operation {
	return Operation{
		Kind:           KindRollback,
		Status:         StatusCompleted,
		Source:         SourceHistory,
		Confidence:     ConfidenceMedium,
		Message:        fmt.Sprintf("Helm rolled back to rev %d as rev %d.", target, rev.Revision),
		Evidence:       fmt.Sprintf("revision %d description indicates rollback", rev.Revision),
		Revision:       rev.Revision,
		TargetRevision: target,
		Updated:        rev.Updated,
	}
}

func pendingOperation(rev Revision, age time.Duration) Operation {
	return Operation{
		Kind:          KindPending,
		Status:        StatusStuck,
		Source:        SourceStatus,
		Confidence:    ConfidenceHigh,
		Message:       fmt.Sprintf("Release has been %s for %s.", rev.Status, formatDuration(age)),
		Evidence:      "latest Helm revision is still pending",
		Revision:      rev.Revision,
		PendingStatus: rev.Status,
		Updated:       rev.Updated,
	}
}

func rollbackTarget(description string) (int, bool) {
	matches := rollbackToPattern.FindStringSubmatch(strings.TrimSpace(description))
	if len(matches) != 2 {
		return 0, false
	}
	var target int
	if _, err := fmt.Sscanf(matches[1], "%d", &target); err != nil || target <= 0 {
		return 0, false
	}
	return target, true
}

func isUpgradeFailureDescription(description string) bool {
	description = strings.TrimSpace(description)
	if description == "" {
		return false
	}
	return upgradeFailurePattern.MatchString(description)
}

func findCurrent(ordered []Revision, currentRevision int) (Revision, bool) {
	for i := len(ordered) - 1; i >= 0; i-- {
		if currentRevision <= 0 || ordered[i].Revision == currentRevision {
			return ordered[i], true
		}
	}
	return Revision{}, false
}

func isPending(status string) bool {
	status = normalizeStatus(status)
	return status == "pending-install" || status == "pending-upgrade" || status == "pending-rollback"
}

func isCompletedRevisionStatus(status string) bool {
	status = normalizeStatus(status)
	return status == "deployed" || status == "superseded"
}

func normalizeStatus(status string) string {
	return strings.ToLower(strings.TrimSpace(status))
}

func withoutDuplicateLiveFailure(ops []Operation, live Operation) []Operation {
	out := ops[:0]
	for _, op := range ops {
		if op.Status == StatusFailed && op.Revision == live.Revision {
			continue
		}
		out = append(out, op)
	}
	return out
}

func operationRevision(op Operation) int {
	if op.RollbackRevision > 0 {
		return op.RollbackRevision
	}
	if op.Revision > 0 {
		return op.Revision
	}
	return op.FailedRevision
}

func formatDuration(d time.Duration) string {
	if d < time.Hour {
		mins := int(d.Round(time.Minute) / time.Minute)
		if mins < 1 {
			mins = 1
		}
		return fmt.Sprintf("%dm", mins)
	}
	hours := int(d.Round(time.Hour) / time.Hour)
	if hours < 1 {
		hours = 1
	}
	return fmt.Sprintf("%dh", hours)
}
