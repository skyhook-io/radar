package issues

import (
	"fmt"
	"strings"
	"time"

	"github.com/skyhook-io/radar/pkg/cnpg"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	cnpgGroup       = "postgresql.cnpg.io"
	cnpgBarmanGroup = "barmancloud.cnpg.io"

	ReasonCNPGScheduledRunNoBackup = "CNPGScheduledRunNoBackup"
)

// cnpgBackupPhasesInFlight: a run that may still succeed. The operator's own
// terminal phases are completed and failed (walArchivingFailing also ends a
// run); anything else, including a phase a newer minor adds, counts as still
// running, which only ever delays the finding.
var cnpgBackupPhasesDone = map[string]bool{"completed": true, "failed": true, "walArchivingFailing": true}

// cnpgNamespaceBackupEvidence is what one namespace holds about backups.
type cnpgNamespaceBackupEvidence struct {
	schedules []*unstructured.Unstructured
	backups   []*unstructured.Unstructured
	stores    []*unstructured.Unstructured
	// schedulesKnown, backupsKnown, storesKnown: the kind is watched and its
	// list was read, so an empty list means none rather than unread. A run is
	// judged missed only from evidence that was read: without schedules
	// nothing is evaluated, without Backups no cluster is, and without
	// ObjectStores no cluster whose backups land in one is.
	schedulesKnown bool
	backupsKnown   bool
	storesKnown    bool
}

// detectCNPGScheduledRunIssues raises, per Cluster, that a schedule fired and
// no successful backup exists from that run onwards. The missed-run detector
// on the ScheduledBackup answers a different question (the operator did not
// start a run it set for itself); this one fires when runs start and fail, or
// succeed nowhere this cluster's evidence can see.
//
// The claim is exactly what is compared: the newest successful backup this
// cluster has (Backup objects, the ObjectStore's recovery window, the in-tree
// status field) is older than a time the schedule fired, with the last
// successful backup's own duration allowed for the run to finish. A run still
// in progress holds the finding back.
func detectCNPGScheduledRunIssues(p Provider, clusterGVR schema.GroupVersionResource, clusters []*unstructured.Unstructured, now time.Time) []Issue {
	if len(clusters) == 0 {
		return nil
	}
	gvrs := map[string]schema.GroupVersionResource{}
	for _, g := range p.WatchedDynamic() {
		if g.Group == cnpgGroup || g.Group == cnpgBarmanGroup {
			gvrs[g.Group+"/"+p.KindForGVR(g)] = g
		}
	}
	schedGVR, ok := gvrs[cnpgGroup+"/ScheduledBackup"]
	if !ok {
		return nil
	}
	evidence := map[string]*cnpgNamespaceBackupEvidence{}
	load := func(ns string) *cnpgNamespaceBackupEvidence {
		if e, ok := evidence[ns]; ok {
			return e
		}
		e := &cnpgNamespaceBackupEvidence{}
		if items, err := p.ListDynamic(schedGVR, ns); err == nil {
			e.schedules, e.schedulesKnown = items, true
		}
		if g, ok := gvrs[cnpgGroup+"/Backup"]; ok {
			if items, err := p.ListDynamic(g, ns); err == nil {
				e.backups, e.backupsKnown = items, true
			}
		}
		if g, ok := gvrs[cnpgBarmanGroup+"/ObjectStore"]; ok {
			if items, err := p.ListDynamic(g, ns); err == nil {
				e.stores, e.storesKnown = items, true
			}
		}
		evidence[ns] = e
		return e
	}

	var out []Issue
	for _, c := range clusters {
		if c.GroupVersionKind().Group != "" && c.GroupVersionKind().Group != cnpgGroup {
			continue
		}
		e := load(c.GetNamespace())
		if !e.schedulesKnown || !e.backupsKnown {
			continue
		}
		if cnpgBarmanPlugin(c).objectStore != "" && !e.storesKnown {
			continue
		}
		if iss, ok := cnpgScheduledRunIssue(clusterGVR, c, e, now); ok {
			ns := c.GetNamespace()
			iss.RequiredReads = []EvidenceRead{{Group: cnpgGroup, Resource: "clusters", Namespace: ns, Verb: "list"}, {Group: cnpgGroup, Resource: "scheduledbackups", Namespace: ns, Verb: "list"}, {Group: cnpgGroup, Resource: "backups", Namespace: ns, Verb: "list"}}
			if cnpgBarmanPlugin(c).objectStore != "" {
				iss.RequiredReads = append(iss.RequiredReads, EvidenceRead{Group: cnpgBarmanGroup, Resource: "objectstores", Namespace: ns, Verb: "list"})
			}
			out = append(out, iss)
		}
	}
	return out
}

func cnpgScheduledRunIssue(gvr schema.GroupVersionResource, cluster *unstructured.Unstructured, e *cnpgNamespaceBackupEvidence, now time.Time) (Issue, bool) {
	name := cluster.GetName()
	lastSuccess, lastDuration := cnpgLatestSuccessfulBackup(cluster, e)

	var worst struct {
		schedule, spec string
		fired          time.Time
	}
	for _, s := range e.schedules {
		if cnpgSpecClusterName(s) != name {
			continue
		}
		if suspended, _, _ := unstructured.NestedBool(s.Object, "spec", "suspend"); suspended {
			continue
		}
		spec, _, _ := unstructured.NestedString(s.Object, "spec", "schedule")
		if _, err := cnpg.ParseSchedule(spec); err != nil {
			continue
		}
		// Cron alone cannot establish a run's time without the operator's clock.
		reported, _, _ := unstructured.NestedString(s.Object, "status", "lastScheduleTime")
		fired, err := time.Parse(time.RFC3339, reported)
		if err != nil || !fired.After(lastSuccess) || fired.After(now) || fired.Before(s.GetCreationTimestamp().Time) {
			continue
		}
		if now.Sub(fired) <= lastDuration+cnpgScheduledBackupGrace {
			continue
		}
		if worst.fired.IsZero() || fired.Before(worst.fired) {
			worst.schedule, worst.spec, worst.fired = s.GetName(), spec, fired
		}
	}
	if worst.fired.IsZero() || cnpgBackupInFlightSince(name, e.backups, lastSuccess) {
		return Issue{}, false
	}

	// The run's time is the issue's first_seen (a reader shows it as an age);
	// the message names the schedule in words so nobody has to decode cron.
	reading := cnpg.DescribeSchedule(worst.spec)
	if reading == "" {
		reading = "cron " + worst.spec
	}
	msg := fmt.Sprintf("ScheduledBackup %s (%s) has had no successful backup since its run", worst.schedule, reading)
	if lastSuccess.IsZero() {
		msg = fmt.Sprintf("ScheduledBackup %s (%s) has had no successful backup observed since its reported run", worst.schedule, reading)
	}
	return newConditionIssue(gvr, "Cluster", cluster.GetNamespace(), name, SeverityWarning,
		ReasonCNPGScheduledRunNoBackup, msg, worst.fired, true, ReasonCNPGScheduledRunNoBackup, cluster.GetCreationTimestamp().Time), true
}

// cnpgLatestSuccessfulBackup is the newest success across the three places
// CNPG records one, plus the duration of the newest completed Backup object
// (zero when unknown).
func cnpgLatestSuccessfulBackup(cluster *unstructured.Unstructured, e *cnpgNamespaceBackupEvidence) (time.Time, time.Duration) {
	name := cluster.GetName()
	var latest time.Time
	var latestBackup time.Time
	var duration time.Duration
	for _, b := range e.backups {
		if cnpgSpecClusterName(b) != name || !strings.HasPrefix(b.GetAPIVersion(), cnpgGroup+"/") {
			continue
		}
		if phase, _, _ := unstructured.NestedString(b.Object, "status", "phase"); phase != "completed" {
			continue
		}
		stopped := cnpgStatusTime(b, "stoppedAt")
		if stopped.IsZero() {
			stopped = cnpgStatusTime(b, "startedAt")
		}
		if stopped.IsZero() || !stopped.After(latestBackup) {
			continue
		}
		latestBackup = stopped
		if started := cnpgStatusTime(b, "startedAt"); !started.IsZero() && stopped.After(started) {
			duration = stopped.Sub(started)
		} else {
			duration = 0
		}
	}
	latest = latestBackup

	plugin := cnpgBarmanPlugin(cluster)
	if plugin.objectStore != "" {
		for _, s := range e.stores {
			if s.GetName() != plugin.objectStore {
				continue
			}
			if t := cnpgParseTime(nestedString(s.Object, "status", "serverRecoveryWindow", plugin.serverName, "lastSuccessfulBackupTime")); t.After(latest) {
				latest = t
			}
		}
	} else if !plugin.present {
		if t := cnpgParseTime(nestedString(cluster.Object, "status", "lastSuccessfulBackup")); t.After(latest) {
			latest = t
		}
	}
	return latest, duration
}

func cnpgBackupInFlightSince(cluster string, backups []*unstructured.Unstructured, since time.Time) bool {
	for _, b := range backups {
		if cnpgSpecClusterName(b) != cluster || !strings.HasPrefix(b.GetAPIVersion(), cnpgGroup+"/") {
			continue
		}
		phase, _, _ := unstructured.NestedString(b.Object, "status", "phase")
		if cnpgBackupPhasesDone[phase] {
			continue
		}
		if !b.GetCreationTimestamp().Time.Before(since) {
			return true
		}
	}
	return false
}

type cnpgPluginRef struct {
	present     bool
	objectStore string
	serverName  string
}

// cnpgBarmanPlugin mirrors getCNPGClusterBarmanPlugin in k8s-ui: the
// barman-cloud plugin entry, its ObjectStore and the server key the recovery
// window is recorded under (serverName, else the cluster name).
func cnpgBarmanPlugin(cluster *unstructured.Unstructured) cnpgPluginRef {
	plugin, present := cnpg.ParseBackupDeclaration(cluster).BarmanPlugin()
	return cnpgPluginRef{present: present, objectStore: plugin.ObjectStore, serverName: plugin.ServerName}
}

func cnpgSpecClusterName(u *unstructured.Unstructured) string {
	return nestedString(u.Object, "spec", "cluster", "name")
}

func cnpgStatusTime(u *unstructured.Unstructured, field string) time.Time {
	return cnpgParseTime(nestedString(u.Object, "status", field))
}

func cnpgParseTime(v string) time.Time {
	if v == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}
	}
	return t
}

func nestedString(obj map[string]any, path ...string) string {
	v, _, _ := unstructured.NestedString(obj, path...)
	return v
}
