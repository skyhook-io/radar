package issues

import (
	"fmt"
	"slices"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	veleropkg "github.com/skyhook-io/radar/pkg/velero"
)

// Velero problems no single object records: a server that is not running, a
// schedule that stopped producing backups, a location Velero stopped checking.
// Each is the reading the Velero views show, from the same rules (pkg/velero),
// raised only when Radar is certain of it.
const (
	ReasonVeleroServerNotRunning           = "VeleroServerNotRunning"
	ReasonVeleroScheduleLate               = "VeleroScheduleLate"
	ReasonVeleroScheduleCannotRun          = "VeleroScheduleCannotRun"
	ReasonVeleroScheduleHeld               = "VeleroScheduleHeld"
	ReasonVeleroScheduleNotValidated       = "VeleroScheduleNotValidated"
	ReasonVeleroLocationValidationOverdue  = "VeleroLocationValidationOverdue"
	ReasonVeleroRepositoryMaintenanceFails = "BackupRepositoryMaintenanceFailed"
)

// lateCriticalAfter: missed scheduled times before a late schedule is critical.
// One missed run can be a blip; three is a pattern nobody noticed.
const lateCriticalAfter = 3

// deploymentsProvider reads a namespace's Deployments from Radar's cache.
// read=false when Radar does not hold them there.
type deploymentsProvider interface {
	NamespaceDeployments(namespace string) ([]*appsv1.Deployment, bool)
}

// detectVeleroReadingIssues raises, per installation (a namespace holding a
// storage location), what the Velero views read: the server stopped, each
// schedule's state, and overdue validation. Called with the installation's
// storage locations.
//
// Only certain readings are raised. A schedule is judged only when its
// namespace's Backups were read in full; an estimate made without the
// server's time zone ("probably late") stays a reading on the Velero views,
// because an Issue there would be a false alarm around every daylight-saving
// change. While the server is down, held runs and overdue validation are its
// consequences and are not raised again; late schedules are, since each is a
// backup that will never exist.
func detectVeleroReadingIssues(p Provider, bslGVR schema.GroupVersionResource, locations []*unstructured.Unstructured, now time.Time) []Issue {
	if len(locations) == 0 {
		return nil
	}
	gvrs := map[string]schema.GroupVersionResource{}
	for _, g := range p.WatchedDynamic() {
		if g.Group == VeleroGroup {
			gvrs[p.KindForGVR(g)] = g
		}
	}
	read := func(kind, ns string) ([]*unstructured.Unstructured, bool) {
		g, ok := gvrs[kind]
		if !ok {
			return nil, false
		}
		// Synced is read before the list: the list may start the informer,
		// and only one already synced makes an empty answer mean none.
		synced := true
		if sp, ok := p.(dynamicSyncProvider); ok {
			synced = sp.DynamicNamespaceSynced(g, ns)
		}
		items, err := p.ListDynamic(g, ns)
		return items, err == nil && synced
	}

	byNS := map[string][]*unstructured.Unstructured{}
	for _, l := range locations {
		byNS[l.GetNamespace()] = append(byNS[l.GetNamespace()], l)
	}
	namespaces := make([]string, 0, len(byNS))
	for ns := range byNS {
		namespaces = append(namespaces, ns)
	}
	slices.Sort(namespaces)

	var out []Issue
	for _, ns := range namespaces {
		server, serverRead := veleroServer(p, ns)
		serverReads := []EvidenceRead{}
		if serverRead {
			serverReads = append(serverReads, EvidenceRead{Group: "apps", Resource: "deployments", Namespace: ns, Verb: "list"})
		}
		// The schedules a stopped server leaves late are listed on its issue
		// instead of raising one each: one fault, one issue.
		var lateWhileDown []Ref

		defaultLocation, defaultSource := veleropkg.DefaultLocation(byNS[ns], server)
		for _, l := range byNS[ns] {
			reading := veleropkg.ReadLocation(veleropkg.LocationInput{Location: l, Server: server, DefaultLocation: defaultLocation, DefaultSource: defaultSource, Now: now})
			if !reading.Attention || server.Down() {
				continue
			}
			message := "Velero has not validated this location since it was created, so whether it can be written to is unknown."
			if reading.LastValidation != nil && reading.Frequency != nil {
				message = fmt.Sprintf("Velero last validated this location %s ago and should every %s, so an unreachable bucket would go unnoticed.",
					formatVeleroDuration(now.Sub(*reading.LastValidation)), formatVeleroDuration(time.Duration(reading.Frequency.Seconds)*time.Second))
			}
			iss := veleroStateIssue(bslGVR, "BackupStorageLocation", ns, l.GetName(), SeverityWarning, ReasonVeleroLocationValidationOverdue, message, l.GetCreationTimestamp().Time)
			iss.RequiredReads = append([]EvidenceRead{{Group: VeleroGroup, Resource: "backupstoragelocations", Namespace: ns, Verb: "list"}}, serverReads...)
			out = append(out, iss)
		}

		schedules, schedulesKnown := read("Schedule", ns)
		backups, backupsKnown := read("Backup", ns)
		if !schedulesKnown || !backupsKnown {
			schedules = nil
		}
		scheduleGVR := gvrs["Schedule"]
		for _, s := range schedules {
			reading := veleropkg.ReadSchedule(veleropkg.ScheduleInput{Schedule: s, Backups: backups, BackupsRead: true, Server: server, DefaultLocation: defaultLocation, Now: now})
			if server.Down() && reading.Attention && reading.State == veleropkg.StateLate {
				lateWhileDown = append(lateWhileDown, Ref{Group: VeleroGroup, Kind: "Schedule", Namespace: ns, Name: s.GetName()})
				continue
			}
			reason, severity, message, ok := veleroScheduleIssueFor(reading, server, now)
			if !ok {
				continue
			}
			iss := veleroStateIssue(scheduleGVR, "Schedule", ns, s.GetName(), severity, reason, message, s.GetCreationTimestamp().Time)
			iss.RequiredReads = append([]EvidenceRead{
				{Group: VeleroGroup, Resource: "schedules", Namespace: ns, Verb: "list"},
				{Group: VeleroGroup, Resource: "backups", Namespace: ns, Verb: "list"},
			}, serverReads...)
			out = append(out, iss)
		}
		if server.Down() {
			iss := newConditionIssue(schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, "Deployment", ns, server.Deployment,
				SeverityCritical, ReasonVeleroServerNotRunning,
				fmt.Sprintf("The Velero server Deployment %s has no running replicas, so no scheduled backup is submitted and no storage location is validated.", server.Deployment),
				time.Time{}, false, "", time.Time{})
			iss.FirstSeen, iss.LastSeen = time.Time{}, now
			iss.RequiredReads = serverReads
			if len(lateWhileDown) > 0 {
				iss = withAffectedRuns(iss, factVeleroLateSchedules, veleroLateSchedulesMessage, lateWhileDown)
			}
			out = append(out, iss)
		}
	}
	return out
}

func veleroServer(p Provider, ns string) (veleropkg.ServerConfig, bool) {
	dp, ok := p.(deploymentsProvider)
	if !ok {
		return veleropkg.ServerConfigFromDeployments(nil, false), false
	}
	deployments, read := dp.NamespaceDeployments(ns)
	return veleropkg.ServerConfigFromDeployments(deployments, read), read
}

// veleroScheduleIssueFor is the Issue a schedule's reading raises, if any.
func veleroScheduleIssueFor(r veleropkg.ScheduleReading, server veleropkg.ServerConfig, now time.Time) (reason string, severity Severity, message string, ok bool) {
	if !r.Attention {
		return "", "", "", false
	}
	switch r.State {
	case veleropkg.StateCannotRun:
		return ReasonVeleroScheduleCannotRun, SeverityCritical,
			"This schedule's name is longer than 63 characters, too long for the label Velero puts on its backups, so Velero submits no runs. Recreate it with a shorter name.", true
	case veleropkg.StateAlwaysDue:
		return ReasonVeleroScheduleCannotRun, SeverityCritical,
			"This cron expression has no future time, so Velero treats the schedule as always due and submits a new backup whenever the previous one has started.", true
	case veleropkg.StateLate:
		since := "since the schedule was created"
		if r.LastSubmitted != nil {
			since = fmt.Sprintf("for %s", formatVeleroDuration(now.Sub(*r.LastSubmitted)))
		}
		missed := "its scheduled time passed"
		if r.MissedRuns == 1 {
			missed = "1 scheduled time passed"
		} else if r.MissedRuns > 1 {
			missed = fmt.Sprintf("%d scheduled times passed", r.MissedRuns)
		}
		cause := " Velero does not catch up on missed runs."
		severity := SeverityWarning
		if r.MissedRuns >= lateCriticalAfter {
			severity = SeverityCritical
		}
		return ReasonVeleroScheduleLate, severity, fmt.Sprintf("No backup submitted %s; %s.%s", since, missed, cause), true
	case veleropkg.StateHeld:
		if server.Down() || r.Blocker == nil {
			return "", "", "", false
		}
		return ReasonVeleroScheduleHeld, SeverityWarning,
			fmt.Sprintf("Held by backup %s, which Velero has not started for %s; Velero submits nothing new for this schedule until it does.", r.Blocker.Name, formatVeleroDuration(now.Sub(r.Blocker.Created))), true
	case veleropkg.StateNotValidated:
		if server.Down() {
			return "", "", "", false
		}
		return ReasonVeleroScheduleNotValidated, SeverityWarning,
			fmt.Sprintf("Velero has not validated this schedule since it was created %s ago, so its controller may not be processing schedules.", formatVeleroDuration(now.Sub(r.Created))), true
	}
	return "", "", "", false
}
