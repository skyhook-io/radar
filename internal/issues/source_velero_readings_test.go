package issues

import (
	"fmt"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/issuesapi"
	veleropkg "github.com/skyhook-io/radar/pkg/velero"
)

var (
	veleroNow         = time.Date(2026, 11, 15, 12, 30, 0, 0, time.UTC)
	veleroScheduleGVR = schema.GroupVersionResource{Group: VeleroGroup, Version: "v1", Resource: "schedules"}
	veleroBackupGVR   = schema.GroupVersionResource{Group: VeleroGroup, Version: "v1", Resource: "backups"}
	veleroBSLGVR      = schema.GroupVersionResource{Group: VeleroGroup, Version: "v1", Resource: "backupstoragelocations"}
)

// veleroReadingsProvider adds the Velero namespace's Deployments to the fake.
type veleroReadingsProvider struct {
	*fakeProvider
	deployments []*appsv1.Deployment
	deployRead  bool
}

func (p *veleroReadingsProvider) NamespaceDeployments(string) ([]*appsv1.Deployment, bool) {
	return p.deployments, p.deployRead
}

func veleroReadingObj(kind, name string, created time.Time, labels map[string]any, spec, status map[string]any) *unstructured.Unstructured {
	meta := map[string]any{"name": name, "namespace": "velero", "creationTimestamp": created.Format(time.RFC3339)}
	if labels != nil {
		meta["labels"] = labels
	}
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "velero.io/v1", "kind": kind, "metadata": meta, "spec": spec, "status": status}}
}

// veleroServerDeployment is the server scaled to `running`, its rollout done.
func veleroServerDeployment(running int32) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "velero", Namespace: "velero", Generation: 1},
		Spec: appsv1.DeploymentSpec{Replicas: &running, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{
			{Name: "velero", Image: "velero/velero:v1.18.1", Command: []string{"/velero"}, Args: []string{"server"}},
		}}}},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 1, Replicas: running, UpdatedReplicas: running},
	}
}

// An hourly schedule whose last submission was `ago` before veleroNow, with
// that run's Backup present and completed.
func hourlyLastRan(ago time.Duration) []*unstructured.Unstructured {
	last := veleroNow.Add(-ago).Truncate(time.Hour)
	schedule := veleroReadingObj("Schedule", "hourly", veleroNow.Add(-72*time.Hour), nil,
		map[string]any{"schedule": "0 * * * *", "template": map[string]any{}},
		map[string]any{"phase": "Enabled", "lastBackup": last.Format(time.RFC3339)})
	run := veleroReadingObj("Backup", "hourly-"+last.Format("20060102150405"), last, map[string]any{veleropkg.ScheduleNameLabel: "hourly"},
		map[string]any{"storageLocation": "default"}, map[string]any{"phase": "Completed"})
	return []*unstructured.Unstructured{schedule, run}
}

func veleroLocation(lastValidated time.Duration) *unstructured.Unstructured {
	return veleroReadingObj("BackupStorageLocation", "default", veleroNow.Add(-72*time.Hour), nil,
		map[string]any{"provider": "aws", "default": true, "validationFrequency": "1m"},
		map[string]any{"phase": "Available", "lastValidationTime": veleroNow.Add(-lastValidated).Format(time.RFC3339)})
}

func veleroReadings(t *testing.T, objs []*unstructured.Unstructured, location *unstructured.Unstructured, deployments []*appsv1.Deployment, deployRead bool, watchBackups bool) []Issue {
	t.Helper()
	dynamic := map[schema.GroupVersionResource][]*unstructured.Unstructured{veleroBSLGVR: {location}}
	kinds := map[schema.GroupVersionResource]string{veleroBSLGVR: "BackupStorageLocation", veleroScheduleGVR: "Schedule"}
	if watchBackups {
		kinds[veleroBackupGVR] = "Backup"
		dynamic[veleroBackupGVR] = []*unstructured.Unstructured{}
	}
	dynamic[veleroScheduleGVR] = []*unstructured.Unstructured{}
	for _, o := range objs {
		switch o.GetKind() {
		case "Schedule":
			dynamic[veleroScheduleGVR] = append(dynamic[veleroScheduleGVR], o)
		case "Backup":
			if watchBackups {
				dynamic[veleroBackupGVR] = append(dynamic[veleroBackupGVR], o)
			}
		}
	}
	p := &veleroReadingsProvider{fakeProvider: &fakeProvider{dynamic: dynamic, kinds: kinds}, deployments: deployments, deployRead: deployRead}
	return detectVeleroReadingIssues(p, veleroBSLGVR, []*unstructured.Unstructured{location}, veleroNow)
}

func byReason(issues []Issue) map[string]Issue {
	out := map[string]Issue{}
	for _, i := range issues {
		out[i.Reason] = i
	}
	return out
}

func hasRead(i Issue, group, resource string) bool {
	for _, r := range i.RequiredReads {
		if r.Group == group && r.Resource == resource {
			return true
		}
	}
	return false
}

func TestVeleroServerNotRunningLeadsAndExplainsItsConsequences(t *testing.T) {
	got := byReason(veleroReadings(t, hourlyLastRan(3*time.Hour), veleroLocation(time.Hour),
		[]*appsv1.Deployment{veleroServerDeployment(0)}, true, true))

	server, ok := got[ReasonVeleroServerNotRunning]
	if !ok || server.Severity != SeverityCritical || server.Kind != "Deployment" || server.Name != "velero" {
		t.Fatalf("server issue %+v, want a critical one on the velero Deployment", server)
	}
	if !hasRead(server, "apps", "deployments") {
		t.Errorf("the server issue must need the Deployments read it rests on: %+v", server.RequiredReads)
	}
	// One fault, one issue: the late schedule is named on the server's issue
	// and raises none of its own.
	if late, ok := got[ReasonVeleroScheduleLate]; ok {
		t.Fatalf("late schedule raised on its own while the server is down: %+v", late)
	}
	if fact := veleroFact(server, factVeleroLateSchedules); fact == nil || len(fact.Refs) != 1 || fact.Refs[0].Name != "hourly" {
		t.Fatalf("server issue facts %+v, want the late schedule named", server.DiagnosticContext)
	}
	if len(server.AffectedRefs) != 1 || server.AffectedRefs[0].Kind != "Schedule" {
		t.Errorf("affected %+v, want the schedule, so its drawer shows the server's issue", server.AffectedRefs)
	}
	// Overdue validation is the stopped server's consequence; it is said once.
	if _, ok := got[ReasonVeleroLocationValidationOverdue]; ok {
		t.Error("validation overdue was raised again while the server is down")
	}
}

func veleroFact(i Issue, factType string) *issuesapi.DiagnosticFact {
	if i.DiagnosticContext == nil {
		return nil
	}
	for _, f := range i.DiagnosticContext.Facts {
		if f.Type == factType {
			return &f
		}
	}
	return nil
}

// A hundred schedules stopped by one server are one issue, not a hundred and one.
func TestVeleroStoppedServerIsOneIssueForEveryScheduleItStops(t *testing.T) {
	var objs []*unstructured.Unstructured
	for i := range 100 {
		for _, o := range hourlyLastRan(3 * time.Hour) {
			name := fmt.Sprintf("hourly-%03d", i)
			if o.GetKind() == "Schedule" {
				o.SetName(name)
			} else {
				o.SetName(name + "-run")
				o.SetLabels(map[string]string{veleropkg.ScheduleNameLabel: name})
			}
			objs = append(objs, o)
		}
	}
	issues := veleroReadings(t, objs, veleroLocation(time.Hour), []*appsv1.Deployment{veleroServerDeployment(0)}, true, true)
	if len(issues) != 1 || issues[0].Reason != ReasonVeleroServerNotRunning {
		t.Fatalf("got %d issues (%v), want only the server's", len(issues), reasonsOf(issues))
	}
	if n := len(issues[0].AffectedRefs); n != 100 {
		t.Errorf("affected %d schedules, want all 100 so each one's drawer finds the issue", n)
	}
	if fact := veleroFact(issues[0], factVeleroLateSchedules); fact == nil || len(fact.Refs) != maxAffectedRefLinks {
		t.Errorf("fact %+v, want the links capped", fact)
	}
}

// While the server runs, a late schedule is its own issue: nothing else explains it.
func TestVeleroLateScheduleIsItsOwnIssueWhileTheServerRuns(t *testing.T) {
	got := byReason(veleroReadings(t, hourlyLastRan(3*time.Hour), veleroLocation(time.Minute),
		[]*appsv1.Deployment{veleroServerDeployment(1)}, true, true))
	if _, ok := got[ReasonVeleroScheduleLate]; !ok {
		t.Fatalf("got %v, want the late schedule", got)
	}
	if _, ok := got[ReasonVeleroServerNotRunning]; ok {
		t.Error("a running server raised an issue")
	}
}

func TestVeleroLateScheduleSeverityFollowsMissedRuns(t *testing.T) {
	for _, tc := range []struct {
		ago  time.Duration
		want Severity
	}{
		{90 * time.Minute, SeverityWarning},
		{4 * time.Hour, SeverityCritical},
	} {
		t.Run(fmt.Sprint(tc.ago), func(t *testing.T) {
			got := byReason(veleroReadings(t, hourlyLastRan(tc.ago), veleroLocation(time.Second),
				[]*appsv1.Deployment{veleroServerDeployment(1)}, true, true))
			late, ok := got[ReasonVeleroScheduleLate]
			if !ok || late.Severity != tc.want {
				t.Fatalf("late %+v, want severity %s", late, tc.want)
			}
			if !hasRead(late, VeleroGroup, "schedules") || !hasRead(late, VeleroGroup, "backups") || !hasRead(late, "apps", "deployments") {
				t.Errorf("a late schedule rests on schedules, backups and the server: %+v", late.RequiredReads)
			}
			if _, ok := got[ReasonVeleroServerNotRunning]; ok {
				t.Error("a running server was reported stopped")
			}
		})
	}
}

// Without the namespace's Backups, a run Radar cannot see may be holding the
// schedule; without the server's zone, "late" is an estimate. Neither is
// raised: the Velero views show both as readings.
func TestVeleroUncertainReadingsRaiseNothing(t *testing.T) {
	if got := byReason(veleroReadings(t, hourlyLastRan(4*time.Hour), veleroLocation(time.Second), []*appsv1.Deployment{veleroServerDeployment(1)}, true, false)); len(got) != 0 {
		t.Errorf("unread Backups raised %v", got)
	}
	// No Deployment read and a run name that does not parse: the zone is unknown.
	objs := hourlyLastRan(4 * time.Hour)
	objs[1].SetName("hourly-manual")
	if got := byReason(veleroReadings(t, objs, veleroLocation(time.Second), nil, false, true)); len(got) != 0 {
		t.Errorf("an uncertain zone raised %v", got)
	}
}

func TestVeleroScheduleThatCanNeverRun(t *testing.T) {
	name := strings.Repeat("x", 64)
	schedule := veleroReadingObj("Schedule", name, veleroNow.Add(-time.Hour), nil,
		map[string]any{"schedule": "0 * * * *", "template": map[string]any{}}, map[string]any{"phase": "Enabled"})
	got := byReason(veleroReadings(t, []*unstructured.Unstructured{schedule}, veleroLocation(time.Second), []*appsv1.Deployment{veleroServerDeployment(1)}, true, true))
	if iss, ok := got[ReasonVeleroScheduleCannotRun]; !ok || iss.Severity != SeverityCritical {
		t.Fatalf("got %v, want a critical cannot-run issue", got)
	}
}

func TestVeleroValidationOverdueWhileTheServerRuns(t *testing.T) {
	got := byReason(veleroReadings(t, nil, veleroLocation(time.Hour), []*appsv1.Deployment{veleroServerDeployment(1)}, true, true))
	iss, ok := got[ReasonVeleroLocationValidationOverdue]
	if !ok || iss.Kind != "BackupStorageLocation" || iss.Severity != SeverityWarning {
		t.Fatalf("got %v, want a warning on the location", got)
	}
}

func TestVeleroRepositoryMaintenance(t *testing.T) {
	repo := func(phase string, results ...string) *unstructured.Unstructured {
		var runs []any
		for i, r := range results {
			runs = append(runs, map[string]any{"result": r, "message": "error running maintenance: unable to connect", "startTimestamp": veleroNow.Add(time.Duration(i-len(results)) * time.Hour).Format(time.RFC3339)})
		}
		return veleroReadingObj("BackupRepository", "app-default-kopia", veleroNow.Add(-72*time.Hour), nil, map[string]any{}, map[string]any{"phase": phase, "recentMaintenance": runs})
	}
	gvr := schema.GroupVersionResource{Group: VeleroGroup, Version: "v1", Resource: "backuprepositories"}
	failing := veleroRepositoryIssues(gvr, "BackupRepository", []*unstructured.Unstructured{repo("Ready", "Succeeded", "Failed", "Failed")})
	if len(failing) != 1 || failing[0].Reason != ReasonVeleroRepositoryMaintenanceFails || !strings.Contains(failing[0].Message, "2 of the last 3") {
		t.Fatalf("got %+v, want one maintenance issue counting the failures", failing)
	}
	if recovered := veleroRepositoryIssues(gvr, "BackupRepository", []*unstructured.Unstructured{repo("Ready", "Failed", "Succeeded")}); len(recovered) != 0 {
		t.Errorf("a repository whose newest maintenance succeeded raised %+v", recovered)
	}
	notReady := veleroRepositoryIssues(gvr, "BackupRepository", []*unstructured.Unstructured{repo("NotReady", "Failed")})
	if len(notReady) != 1 || notReady[0].Reason != ReasonVeleroRepositoryNotReady {
		t.Errorf("a NotReady repository raises only its own issue: %+v", notReady)
	}
}

func TestVeleroReadingCategories(t *testing.T) {
	for reason, kind := range map[string]string{
		ReasonVeleroServerNotRunning:     "Deployment",
		ReasonVeleroScheduleLate:         "Schedule",
		ReasonVeleroScheduleHeld:         "Schedule",
		ReasonVeleroScheduleNotValidated: "Schedule",
	} {
		group := VeleroGroup
		if kind == "Deployment" {
			group = "apps"
		}
		if got := Classify(classifyInput{Source: SourceCondition, APIGroup: group, Kind: kind, Reason: reason}); got != issuesapi.CategoryBackupStalled {
			t.Errorf("%s classified %s, want backup_stalled", reason, got)
		}
	}
	if got := Classify(classifyInput{Source: SourceCondition, APIGroup: VeleroGroup, Kind: "BackupRepository", Reason: ReasonVeleroRepositoryMaintenanceFails}); got != issuesapi.CategoryBackupTargetUnavailable {
		t.Errorf("maintenance classified %s", got)
	}
}

// The readings are raised while visiting the storage locations, so a request
// for Schedules or Deployments alone must still visit them. A request for
// Schedules finds the stopped server's issue through the schedules it covers.
func TestVeleroReadingsSurviveAKindFilter(t *testing.T) {
	objs := hourlyLastRan(4 * time.Hour)
	location := veleroLocation(time.Second)
	dynamic := map[schema.GroupVersionResource][]*unstructured.Unstructured{
		veleroBSLGVR: {location}, veleroScheduleGVR: {objs[0]}, veleroBackupGVR: {objs[1]},
	}
	kinds := map[schema.GroupVersionResource]string{veleroBSLGVR: "BackupStorageLocation", veleroScheduleGVR: "Schedule", veleroBackupGVR: "Backup"}
	p := &veleroReadingsProvider{fakeProvider: &fakeProvider{dynamic: dynamic, kinds: kinds}, deployments: []*appsv1.Deployment{veleroServerDeployment(0)}, deployRead: true}
	for _, kind := range []string{"Schedule", "Deployment"} {
		f := Filters{Kinds: []string{kind}}
		got := applyFilters(detectGenericCRDIssues(p, f, nil, &uncheckedSet{}), f)
		if len(got) != 1 || got[0].Reason != ReasonVeleroServerNotRunning {
			t.Errorf("kind=%s returned %v, want the server's issue", kind, reasonsOf(got))
		}
	}
}
