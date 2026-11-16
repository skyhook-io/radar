package issues

import (
	"cmp"
	"regexp"
	"slices"

	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// One Velero fault can stop every schedule, or fail every run that writes to
// one location. These name the runs and schedules the fault's own issue
// covers; the covered ones raise nothing of their own while it lasts, as a
// Kueue queue's issue covers the runs it blocks. Like every affected-resources
// fact, the links are redacted to what the caller can read, and no count is
// given that would describe the rest.
const (
	factVeleroLateSchedules = "velero_late_schedules"
	factVeleroRejectedRuns  = "velero_rejected_runs"
)

const (
	veleroLateSchedulesMessage = "These schedules have missed runs, and none runs until the server starts."
	veleroRejectedRunsMessage  = "Velero rejected these runs because this location was unavailable."
)

// The validation errors Velero writes when a run's location is unavailable
// (pkg/controller/backup_controller.go and restore_controller.go, 1.18). Only
// a run whose one error is this, naming the location, is covered: any other
// error is a cause of its own.
var veleroUnavailableLocationErrors = map[string]*regexp.Regexp{
	"Backup":  regexp.MustCompile(`^backup can't be created because BackupStorageLocation (\S+) is in Unavailable status\.$`),
	"Restore": regexp.MustCompile(`^The BSL (\S+) is unavailable, cannot retrieve the backup$`),
}

// foldVeleroLocationRejections moves the runs Velero rejected because their
// location is unavailable under that location's issue. When the location
// recovers its issue goes, and each rejected run that a later run hasn't
// superseded is raised on its own again.
func foldVeleroLocationRejections(in []Issue) []Issue {
	locations := map[string]int{}
	for i, iss := range in {
		if iss.Group == VeleroGroup && iss.Kind == "BackupStorageLocation" && iss.Reason == ReasonVeleroBSLUnavailable {
			locations[iss.Namespace+"/"+iss.Name] = i
		}
	}
	if len(locations) == 0 {
		return in
	}
	covered := map[int][]Ref{}
	out := in[:0:0]
	for _, iss := range in {
		if at, ok := veleroRejectingLocation(iss, locations); ok {
			covered[at] = append(covered[at], Ref{Group: iss.Group, Kind: iss.Kind, Namespace: iss.Namespace, Name: iss.Name})
			continue
		}
		out = append(out, iss)
	}
	if len(covered) == 0 {
		return in
	}
	for i := range out {
		if out[i].Group != VeleroGroup || out[i].Reason != ReasonVeleroBSLUnavailable {
			continue
		}
		at := locations[out[i].Namespace+"/"+out[i].Name]
		if refs := covered[at]; len(refs) > 0 {
			out[i] = withAffectedRuns(out[i], factVeleroRejectedRuns, veleroRejectedRunsMessage, refs)
		}
	}
	return out
}

func veleroRejectingLocation(iss Issue, locations map[string]int) (int, bool) {
	if iss.Group != VeleroGroup || (iss.Reason != ReasonVeleroBackupValidationFailed && iss.Reason != ReasonVeleroRestoreValidationFailed) {
		return 0, false
	}
	pattern, ok := veleroUnavailableLocationErrors[iss.Kind]
	if !ok {
		return 0, false
	}
	m := pattern.FindStringSubmatch(iss.Message)
	if m == nil {
		return 0, false
	}
	at, ok := locations[iss.Namespace+"/"+m[1]]
	return at, ok
}

func withAffectedRuns(iss Issue, factType, message string, refs []Ref) Issue {
	refs = slices.Clone(refs)
	slices.SortFunc(refs, func(a, b Ref) int { return cmp.Or(cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Name, b.Name)) })
	iss.AffectedRefs = append(slices.Clone(iss.AffectedRefs), refs...)
	// The issue stands for what it covers: a rollup, not a candidate cause of it.
	if iss.DiagnosticContext == nil {
		iss.DiagnosticContext = &issuesapi.DiagnosticContext{Role: issuesapi.DiagnosticRoleRollup}
	}
	iss.DiagnosticContext = withFact(iss.DiagnosticContext, affectedRefsFact(factType, message, refs))
	return iss
}
