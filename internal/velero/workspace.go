// Package velero serves Radar's Velero views: it reads the caller's Velero
// objects through the observations the server supplies and turns them into
// readings with pkg/velero. Permission checks and cache access belong to the
// server adapter; this package decides what is shown and how certain it is.
package velero

import (
	"context"
	"sort"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	domain "github.com/skyhook-io/radar/pkg/velero"
)

const (
	schedulesKey = "schedules"
	backupsKey   = "backups"
	locationsKey = "backupStorageLocations"
	restoresKey  = "restores"
	reposKey     = "backupRepositories"

	// maxIssuesPerInstallation bounds the issues returned for one Velero
	// namespace; IssueTotals carries the count before the cap.
	maxIssuesPerInstallation = 50
)

var groups = []string{domain.Group}

// WorkspaceKinds are the Velero kinds the views read.
func WorkspaceKinds() []integration.WorkspaceKind {
	return append([]integration.WorkspaceKind(nil), workspaceKinds...)
}

var workspaceKinds = []integration.WorkspaceKind{
	{Key: schedulesKey, Group: domain.Group, Kind: "Schedule", Resource: "schedules"},
	{Key: backupsKey, Group: domain.Group, Kind: "Backup", Resource: "backups"},
	{Key: locationsKey, Group: domain.Group, Kind: "BackupStorageLocation", Resource: "backupstoragelocations"},
	{Key: restoresKey, Group: domain.Group, Kind: "Restore", Resource: "restores"},
	{Key: reposKey, Group: domain.Group, Kind: "BackupRepository", Resource: "backuprepositories"},
}

// Observations are the caller-scoped reads the server supplies.
type Observations struct {
	// Installed: discovery serves velero.io.
	Installed bool
	// Namespaces is the request scope: nil for every namespace the caller may
	// read, or the forced --namespace-scope target.
	Namespaces []string
	// ForcedNamespace is set when Radar runs with --namespace-scope.
	ForcedNamespace string
	WorkspaceRead   func(ctx context.Context, k integration.WorkspaceKind, namespaces []string) (integration.KindAccess, []*unstructured.Unstructured)
	// Deployments returns a namespace's Deployments; read=false when the
	// caller may not read them or Radar holds none for that namespace.
	Deployments func(ctx context.Context, namespace string) (deployments []*appsv1.Deployment, read bool)
}

// Reader builds the Velero views for one request.
type Reader struct {
	ClusterContext string
	Observations   Observations
	Now            func() time.Time
}

func (r *Reader) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

// Installation is one namespace Velero objects were read in.
type Installation struct {
	Namespace       string               `json:"namespace"`
	Server          domain.ServerConfig  `json:"server"`
	DefaultLocation string               `json:"defaultLocation,omitempty"`
	DefaultSource   domain.DefaultSource `json:"defaultSource"`
	// UnassignedBackups is nil when the namespace's Backups were not read.
	UnassignedBackups *int `json:"unassignedBackups,omitempty"`
}

// Issue is the subset of an Issues row the views render.
type Issue struct {
	ID        string             `json:"id"`
	Severity  issuesapi.Severity `json:"severity"`
	Category  issuesapi.Category `json:"category"`
	Kind      string             `json:"kind"`
	Group     string             `json:"group,omitempty"`
	Namespace string             `json:"namespace,omitempty"`
	Name      string             `json:"name"`
	Reason    string             `json:"reason"`
	Message   string             `json:"message,omitempty"`
	Cause     string             `json:"cause,omitempty"`
	Action    string             `json:"action,omitempty"`
	FirstSeen time.Time          `json:"first_seen,omitzero"`
	// Affected are the runs this row covers, which raise no row of their own:
	// for an unavailable location, the runs Velero rejected because of it.
	Affected []issuesapi.Ref `json:"affected,omitempty"`
}

// WorkspaceResponse is GET /api/velero/workspace.
type WorkspaceResponse struct {
	// ObservedAt is when the readings were computed; every time in the
	// response is absolute, so a client never recomputes state from it.
	ObservedAt      time.Time                           `json:"observedAt"`
	Installed       bool                                `json:"installed"`
	Context         string                              `json:"context"`
	ForcedNamespace string                              `json:"forcedNamespace,omitempty"`
	Coverage        map[string]integration.KindCoverage `json:"coverage"`
	Installations   []Installation                      `json:"installations"`
	Schedules       []domain.ScheduleReading            `json:"schedules"`
	Locations       []domain.LocationReading            `json:"locations"`
	Issues          []Issue                             `json:"issues"`
	// IssueTotals counts, per installation namespace, the issues visible to
	// the caller before the per-installation cap.
	IssueTotals map[string]int `json:"issueTotals"`
}

type inventory struct {
	access  map[string]integration.KindAccess
	objects map[string]map[string][]*unstructured.Unstructured // key -> namespace -> objects
}

func (inv inventory) covered(key, namespace string) bool { return inv.access[key].Covers(namespace) }

func (inv inventory) in(key, namespace string) []*unstructured.Unstructured {
	return inv.objects[key][namespace]
}

func (r *Reader) read(ctx context.Context, namespaces []string) inventory {
	inv := inventory{access: map[string]integration.KindAccess{}, objects: map[string]map[string][]*unstructured.Unstructured{}}
	for _, k := range workspaceKinds {
		access, items := r.Observations.WorkspaceRead(ctx, k, namespaces)
		inv.access[k.Key] = access
		byNS := map[string][]*unstructured.Unstructured{}
		for _, u := range integration.KeepGroups(items, groups) {
			byNS[u.GetNamespace()] = append(byNS[u.GetNamespace()], u)
		}
		inv.objects[k.Key] = byNS
	}
	return inv
}

// Workspace reads every Velero installation the caller can see.
func (r *Reader) Workspace(ctx context.Context) WorkspaceResponse {
	now := r.now()
	resp := WorkspaceResponse{
		ObservedAt: now, Installed: r.Observations.Installed, Context: r.ClusterContext,
		ForcedNamespace: r.Observations.ForcedNamespace,
		Coverage:        map[string]integration.KindCoverage{},
		Installations:   []Installation{}, Schedules: []domain.ScheduleReading{}, Locations: []domain.LocationReading{},
		Issues: []Issue{}, IssueTotals: map[string]int{},
	}
	if !resp.Installed {
		for _, k := range workspaceKinds {
			resp.Coverage[k.Key] = integration.KindCoverage{State: integration.KindCoverageNotInstalled}
		}
		return resp
	}
	inv := r.read(ctx, r.Observations.Namespaces)
	for key, acc := range inv.access {
		resp.Coverage[key] = acc.Coverage()
	}

	for _, ns := range installationNamespaces(inv) {
		inst := r.installation(ctx, inv, ns)
		resp.Installations = append(resp.Installations, inst)
		server := inst.Server
		backups, backupsRead := inv.in(backupsKey, ns), inv.covered(backupsKey, ns)
		bySchedule := backupsBySchedule(backups)
		for _, s := range inv.in(schedulesKey, ns) {
			resp.Schedules = append(resp.Schedules, domain.ReadSchedule(domain.ScheduleInput{
				Schedule: s, Backups: bySchedule[s.GetName()], BackupsRead: backupsRead, Server: server,
				DefaultLocation: inst.DefaultLocation, Now: now,
			}))
		}
		for _, l := range inv.in(locationsKey, ns) {
			resp.Locations = append(resp.Locations, domain.ReadLocation(domain.LocationInput{
				Location: l, Backups: backups, BackupsRead: backupsRead,
				Schedules: inv.in(schedulesKey, ns), SchedulesRead: inv.covered(schedulesKey, ns),
				Server: server, DefaultLocation: inst.DefaultLocation, DefaultSource: inst.DefaultSource, Now: now,
			}))
		}
	}
	sort.Slice(resp.Schedules, func(i, j int) bool {
		a, b := resp.Schedules[i], resp.Schedules[j]
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	sort.Slice(resp.Locations, func(i, j int) bool {
		a, b := resp.Locations[i], resp.Locations[j]
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		return a.Name < b.Name
	})
	resp.Issues, resp.IssueTotals = issuesOf(inv)
	return resp
}

// backupsBySchedule groups a namespace's backups by the schedule that
// submitted them, so each schedule reads only its own.
func backupsBySchedule(backups []*unstructured.Unstructured) map[string][]*unstructured.Unstructured {
	out := map[string][]*unstructured.Unstructured{}
	for _, b := range backups {
		if name, ok := b.GetLabels()[domain.ScheduleNameLabel]; ok {
			out[name] = append(out[name], b)
		}
	}
	return out
}

// installationNamespaces are the namespaces any Velero object was read in.
func installationNamespaces(inv inventory) []string {
	seen := map[string]bool{}
	for _, byNS := range inv.objects {
		for ns, items := range byNS {
			if len(items) > 0 {
				seen[ns] = true
			}
		}
	}
	out := make([]string, 0, len(seen))
	for ns := range seen {
		out = append(out, ns)
	}
	sort.Strings(out)
	return out
}

func (r *Reader) installation(ctx context.Context, inv inventory, ns string) Installation {
	deployments, read := r.Observations.Deployments(ctx, ns)
	server := domain.ServerConfigFromDeployments(deployments, read)
	inst := Installation{Namespace: ns, Server: server}
	if inv.covered(locationsKey, ns) {
		inst.DefaultLocation, inst.DefaultSource = domain.DefaultLocation(inv.in(locationsKey, ns), server)
	} else {
		inst.DefaultSource = domain.DefaultUnknown
	}
	if inv.covered(backupsKey, ns) {
		n := domain.UnassignedBackups(inv.in(backupsKey, ns))
		inst.UnassignedBackups = &n
	}
	return inst
}

// issuesOf raises the Problems surface's Velero rows from the objects read
// here, so every row rests only on kinds the caller could read in its
// namespace, and caps each installation. Nothing about a row the caller could
// not read is counted or named.
func issuesOf(inv inventory) ([]Issue, map[string]int) {
	byKind := map[string][]*unstructured.Unstructured{}
	for _, k := range workspaceKinds {
		for ns, items := range inv.objects[k.Key] {
			if inv.covered(k.Key, ns) {
				byKind[k.Kind] = append(byKind[k.Kind], items...)
			}
		}
	}
	out, totals := []Issue{}, map[string]int{}
	for _, iss := range issues.DetectVelero(byKind) {
		totals[iss.Namespace]++
		if totals[iss.Namespace] > maxIssuesPerInstallation {
			continue
		}
		out = append(out, Issue{
			ID: iss.ID, Severity: iss.Severity, Category: iss.Category, Kind: iss.Kind, Group: iss.Group,
			Namespace: iss.Namespace, Name: iss.Name, Reason: iss.Reason, Message: iss.Message,
			Cause: iss.Cause, Action: iss.Action, FirstSeen: iss.FirstSeen, Affected: iss.AffectedRefs,
		})
	}
	return out, totals
}

// ScheduleActivityResponse is GET /api/velero/schedules/{ns}/{name}/activity.
type ScheduleActivityResponse struct {
	ObservedAt time.Time                           `json:"observedAt"`
	Reading    domain.ScheduleReading              `json:"reading"`
	Coverage   map[string]integration.KindCoverage `json:"coverage"`
	// ServerStopped: the installation's server Deployment was read and has no
	// running replicas, so a backup created now waits until it starts.
	ServerStopped   bool `json:"serverStopped,omitempty"`
	BackupsDisabled bool `json:"backupsDisabled,omitempty"`
}

// ScheduleActivity reads one Schedule with its recent runs. found=false when
// the caller can read Schedules there but this one does not exist; readable
// false when they cannot read Schedules in that namespace.
func (r *Reader) ScheduleActivity(ctx context.Context, namespace, name string) (resp ScheduleActivityResponse, readable, found bool) {
	now := r.now()
	scope := []string{namespace}
	inv := r.read(ctx, scope)
	resp = ScheduleActivityResponse{ObservedAt: now, Coverage: map[string]integration.KindCoverage{}}
	for key, acc := range inv.access {
		resp.Coverage[key] = acc.Coverage()
	}
	if !inv.covered(schedulesKey, namespace) {
		return resp, false, false
	}
	var target *unstructured.Unstructured
	for _, s := range inv.in(schedulesKey, namespace) {
		if s.GetName() == name {
			target = s
		}
	}
	if target == nil {
		return resp, true, false
	}
	inst := r.installation(ctx, inv, namespace)
	resp.Reading = domain.ReadSchedule(domain.ScheduleInput{
		Schedule: target, Backups: inv.in(backupsKey, namespace), BackupsRead: inv.covered(backupsKey, namespace),
		Server: inst.Server, DefaultLocation: inst.DefaultLocation, Now: now, WithRecent: true,
	})
	resp.ServerStopped = inst.Server.Down()
	resp.BackupsDisabled = inst.Server.FlagsKnown() && inst.Server.BackupsDisabled
	return resp, true, true
}

// maxRuns bounds a runs response; the newest are kept.
const maxRuns = 5000

// Run is one Backup or Restore as its list and its pages show it.
type Run struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UID       string `json:"uid"`
	Phase     string `json:"phase,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Created   string `json:"created,omitempty"`
	Started   string `json:"started,omitempty"`
	Completed string `json:"completed,omitempty"`
	// Expiration is a Backup's; Velero deletes it at the next garbage collection after it.
	Expiration string `json:"expiration,omitempty"`
	// Schedule is the Backup's velero.io/schedule-name label.
	Schedule        string `json:"schedule,omitempty"`
	StorageLocation string `json:"storageLocation,omitempty"`
	// ItemsDone and ItemsTotal: items backed up, or restored; absent until Velero counts them.
	ItemsDone  *int64 `json:"itemsDone,omitempty"`
	ItemsTotal *int64 `json:"itemsTotal,omitempty"`
	Errors     int64  `json:"errors"`
	Warnings   int64  `json:"warnings"`
	// BackupName, NamespaceMapping and the namespace selection are a Restore's:
	// together they say where it restored into. The selection is always sent
	// for a Restore, empty meaning every namespace in the backup.
	BackupName         string            `json:"backupName,omitempty"`
	ScheduleName       string            `json:"scheduleName,omitempty"`
	NamespaceMapping   map[string]string `json:"namespaceMapping,omitempty"`
	IncludedNamespaces *[]string         `json:"includedNamespaces,omitempty"`
	ExcludedNamespaces *[]string         `json:"excludedNamespaces,omitempty"`
}

// RunsResponse is GET /api/velero/runs/{backups|restores}.
type RunsResponse struct {
	ObservedAt time.Time                `json:"observedAt"`
	Coverage   integration.KindCoverage `json:"coverage"`
	Runs       []Run                    `json:"runs"`
	// Total counts the runs read before the cap; Runs holds the newest.
	Total     int  `json:"total"`
	Truncated bool `json:"truncated,omitempty"`
}

// RunsKey is the workspace kind key for a runs route's kind, or "".
func RunsKey(plural string) string {
	switch plural {
	case "backups":
		return backupsKey
	case "restores":
		return restoresKey
	}
	return ""
}

type RunsFilter struct {
	Schedule string
	Backup   string
}

// Runs lists one kind's runs the caller can read, newest first: in one
// namespace, or across the request scope when namespace is "". Read through
// the same per-kind access as the views, so a Velero-only reader gets them;
// Coverage says what that read covered.
func (r *Reader) Runs(ctx context.Context, key, namespace string, filter RunsFilter) RunsResponse {
	var scope []string
	if namespace != "" {
		scope = []string{namespace}
	} else if r.Observations.Namespaces != nil {
		scope = r.Observations.Namespaces
	}
	var kind integration.WorkspaceKind
	for _, k := range workspaceKinds {
		if k.Key == key {
			kind = k
		}
	}
	access, items := r.Observations.WorkspaceRead(ctx, kind, scope)
	resp := RunsResponse{ObservedAt: r.now(), Coverage: access.Coverage(), Runs: []Run{}}
	for _, u := range integration.KeepGroups(items, groups) {
		if namespace != "" && u.GetNamespace() != namespace {
			continue
		}
		if key == backupsKey && filter.Schedule != "" && u.GetLabels()[domain.ScheduleNameLabel] != filter.Schedule {
			continue
		}
		if key == restoresKey && filter.Backup != "" {
			backup, _, _ := unstructured.NestedString(u.Object, "spec", "backupName")
			if backup != filter.Backup {
				continue
			}
		}
		resp.Runs = append(resp.Runs, runOf(u, key == restoresKey))
	}
	sort.Slice(resp.Runs, func(i, j int) bool {
		a, b := runStart(resp.Runs[i]), runStart(resp.Runs[j])
		if a != b {
			return a > b
		}
		return resp.Runs[i].Name < resp.Runs[j].Name
	})
	resp.Total = len(resp.Runs)
	if len(resp.Runs) > maxRuns {
		resp.Runs, resp.Truncated = resp.Runs[:maxRuns], true
	}
	return resp
}

func runStart(r Run) string {
	if r.Started != "" {
		return r.Started
	}
	return r.Created
}

func runOf(u *unstructured.Unstructured, restore bool) Run {
	str := func(fields ...string) string { v, _, _ := unstructured.NestedString(u.Object, fields...); return v }
	num := func(fields ...string) int64 { v, _, _ := unstructured.NestedInt64(u.Object, fields...); return v }
	opt := func(fields ...string) *int64 {
		if v, found, _ := unstructured.NestedInt64(u.Object, fields...); found {
			return &v
		}
		return nil
	}
	run := Run{
		Namespace: u.GetNamespace(), Name: u.GetName(), UID: string(u.GetUID()),
		Phase: str("status", "phase"), Created: u.GetCreationTimestamp().UTC().Format(time.RFC3339),
		Started: str("status", "startTimestamp"), Completed: str("status", "completionTimestamp"),
		Expiration: str("status", "expiration"), Schedule: u.GetLabels()[domain.ScheduleNameLabel],
		StorageLocation: str("spec", "storageLocation"),
		ItemsTotal:      opt("status", "progress", "totalItems"),
		Errors:          num("status", "errors"), Warnings: num("status", "warnings"),
		BackupName: str("spec", "backupName"), ScheduleName: str("spec", "scheduleName"),
	}
	run.Reason = str("status", "failureReason")
	if run.Reason == "" {
		validation, _, _ := unstructured.NestedStringSlice(u.Object, "status", "validationErrors")
		run.Reason = strings.Join(validation, "; ")
	}
	if restore {
		run.ItemsDone = opt("status", "progress", "itemsRestored")
		run.Schedule = ""
		if m, found, _ := unstructured.NestedStringMap(u.Object, "spec", "namespaceMapping"); found && len(m) > 0 {
			run.NamespaceMapping = m
		}
		included, _, _ := unstructured.NestedStringSlice(u.Object, "spec", "includedNamespaces")
		excluded, _, _ := unstructured.NestedStringSlice(u.Object, "spec", "excludedNamespaces")
		if included == nil {
			included = []string{}
		}
		if excluded == nil {
			excluded = []string{}
		}
		run.IncludedNamespaces, run.ExcludedNamespaces = &included, &excluded
	} else {
		run.ItemsDone = opt("status", "progress", "itemsBackedUp")
	}
	return run
}
