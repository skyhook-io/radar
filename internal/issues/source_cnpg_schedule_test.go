package issues

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/issuesapi"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	cnpgObjectStoreGVR = schema.GroupVersionResource{Group: "barmancloud.cnpg.io", Version: "v1", Resource: "objectstores"}
	// 2026-09-30 14:30:00 UTC; the hourly schedule last fired at 14:00:00.
	cnpgScheduleNow = time.Date(2026, 9, 30, 14, 30, 0, 0, time.UTC)
)

func cnpgSchedObj(kind, name string, created time.Time, spec, status map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       kind,
		"metadata":   map[string]any{"name": name, "namespace": "pg", "creationTimestamp": created.Format(time.RFC3339)},
		"spec":       spec,
	}}
	if status != nil {
		u.Object["status"] = status
	}
	return u
}

func cnpgHourly(suspend bool) *unstructured.Unstructured {
	return cnpgSchedObj("ScheduledBackup", "hourly", cnpgScheduleNow.Add(-48*time.Hour),
		map[string]any{"schedule": "0 0 * * * *", "suspend": suspend, "cluster": map[string]any{"name": "pg-main"}}, map[string]any{"lastScheduleTime": "2026-09-30T14:00:00Z"})
}

func cnpgBackupAt(name, phase string, started, stopped time.Time) *unstructured.Unstructured {
	status := map[string]any{"phase": phase}
	if !started.IsZero() {
		status["startedAt"] = started.Format(time.RFC3339)
	}
	if !stopped.IsZero() {
		status["stoppedAt"] = stopped.Format(time.RFC3339)
	}
	created := started
	if created.IsZero() {
		created = cnpgScheduleNow.Add(-time.Minute)
	}
	return cnpgSchedObj("Backup", name, created, map[string]any{"cluster": map[string]any{"name": "pg-main"}}, status)
}

func cnpgScheduleProvider(clusters []*unstructured.Unstructured, schedules, backups, stores []*unstructured.Unstructured) *fakeProvider {
	return &fakeProvider{
		dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{
			cnpgClusterGVR:         clusters,
			cnpgScheduledBackupGVR: schedules,
			cnpgBackupGVR:          backups,
			cnpgObjectStoreGVR:     stores,
		},
		kinds: map[schema.GroupVersionResource]string{
			cnpgClusterGVR:         "Cluster",
			cnpgScheduledBackupGVR: "ScheduledBackup",
			cnpgBackupGVR:          "Backup",
			cnpgObjectStoreGVR:     "ObjectStore",
		},
	}
}

func TestCNPGScheduledRunNoBackup(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 9, 30, h, m, 0, 0, time.UTC) }
	pluginCluster := cnpgCluster(map[string]any{"plugins": []any{map[string]any{
		"name": "barman-cloud.cloudnative-pg.io", "parameters": map[string]any{"barmanObjectName": "store", "serverName": "main-v2"},
	}}}, nil)
	store := func(last time.Time) *unstructured.Unstructured {
		u := cnpgSchedObj("ObjectStore", "store", at(0, 0), map[string]any{}, map[string]any{
			"serverRecoveryWindow": map[string]any{"main-v2": map[string]any{"lastSuccessfulBackupTime": last.Format(time.RFC3339)}},
		})
		u.SetAPIVersion("barmancloud.cnpg.io/v1")
		return u
	}

	tests := []struct {
		name      string
		cluster   *unstructured.Unstructured
		schedules []*unstructured.Unstructured
		backups   []*unstructured.Unstructured
		stores    []*unstructured.Unstructured
		want      string
		run       string // the run the issue dates itself from (first_seen)
	}{
		{
			name:      "last success before the reported 14:00 run, which produced nothing",
			schedules: []*unstructured.Unstructured{cnpgHourly(false)},
			backups:   []*unstructured.Unstructured{cnpgBackupAt("b1", "completed", at(12, 0), at(12, 2)), cnpgBackupAt("b2", "failed", at(13, 0), at(13, 1))},
			want:      "ScheduledBackup hourly (every hour, on the hour) has had no successful backup since its run",
			run:       "2026-09-30T14:00:00Z",
		},
		{
			name:      "the 14:00 run succeeded",
			schedules: []*unstructured.Unstructured{cnpgHourly(false)},
			backups:   []*unstructured.Unstructured{cnpgBackupAt("b1", "completed", at(14, 0), at(14, 3))},
		},
		{
			// The 14:00 run is due, but the last success took 40 minutes, so
			// 14:30 is still inside the time a run takes here.
			name:      "within the observed duration of the last success",
			schedules: []*unstructured.Unstructured{cnpgHourly(false)},
			backups:   []*unstructured.Unstructured{cnpgBackupAt("b1", "completed", at(13, 5), at(13, 45))},
		},
		{
			name:      "a run started after the fire time is still in progress",
			schedules: []*unstructured.Unstructured{cnpgHourly(false)},
			backups:   []*unstructured.Unstructured{cnpgBackupAt("b1", "completed", at(11, 0), at(11, 1)), cnpgBackupAt("b2", "running", at(12, 0), time.Time{})},
		},
		{
			name:      "suspended schedules raise nothing",
			schedules: []*unstructured.Unstructured{cnpgHourly(true)},
			backups:   []*unstructured.Unstructured{cnpgBackupAt("b1", "completed", at(1, 0), at(1, 1))},
		},
		{
			name:      "no success ever: measured from the reported run",
			schedules: []*unstructured.Unstructured{cnpgHourly(false)},
			want:      "has had no successful backup observed since its reported run",
			run:       "2026-09-30T14:00:00Z",
		},
		{
			name:      "the ObjectStore's recovery window counts as a success",
			cluster:   pluginCluster,
			schedules: []*unstructured.Unstructured{cnpgHourly(false)},
			stores:    []*unstructured.Unstructured{store(at(14, 1))},
		},
		{
			name:      "an older ObjectStore success still leaves the reported 14:00 run unaccounted for",
			cluster:   pluginCluster,
			schedules: []*unstructured.Unstructured{cnpgHourly(false)},
			stores:    []*unstructured.Unstructured{store(at(12, 1))},
			want:      "since its run",
			run:       "2026-09-30T14:00:00Z",
		},
		{
			name:      "unparseable schedule",
			schedules: []*unstructured.Unstructured{cnpgSchedObj("ScheduledBackup", "bad", at(0, 0), map[string]any{"schedule": "every hour", "cluster": map[string]any{"name": "pg-main"}}, nil)},
		},
		{
			name:      "schedule for another cluster",
			schedules: []*unstructured.Unstructured{cnpgSchedObj("ScheduledBackup", "other", at(0, 0), map[string]any{"schedule": "0 0 * * * *", "cluster": map[string]any{"name": "pg-other"}}, nil)},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.cluster
			if c == nil {
				c = cnpgCluster(nil, nil)
			}
			p := cnpgScheduleProvider([]*unstructured.Unstructured{c}, tc.schedules, tc.backups, tc.stores)
			got := detectCNPGScheduledRunIssues(p, cnpgClusterGVR, []*unstructured.Unstructured{c}, cnpgScheduleNow)
			if tc.want == "" {
				if len(got) != 0 {
					t.Fatalf("unexpected issue: %s", got[0].Message)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("issues = %v, want one", reasonsOf(got))
			}
			iss := got[0]
			if iss.Reason != ReasonCNPGScheduledRunNoBackup || iss.Kind != "Cluster" || iss.Name != "pg-main" || iss.Severity != SeverityWarning {
				t.Errorf("issue = %+v", iss)
			}
			if !strings.Contains(iss.Message, tc.want) {
				t.Errorf("message = %q, want it to contain %q", iss.Message, tc.want)
			}
			if tc.run != "" && iss.FirstSeen.UTC().Format(time.RFC3339) != tc.run {
				t.Errorf("first_seen = %s, want the run %s", iss.FirstSeen, tc.run)
			}
			if iss.Category != issuesapi.CategoryBackupFailed {
				t.Errorf("category = %v, want backup_failed", iss.Category)
			}
		})
	}
}

func TestCNPGScheduledRunUsesReportedInstant(t *testing.T) {
	c := cnpgCluster(nil, nil)
	sched := cnpgSchedObj("ScheduledBackup", "nightly", cnpgScheduleNow.Add(-48*time.Hour),
		map[string]any{"schedule": "0 0 2 * * *", "cluster": map[string]any{"name": "pg-main"}},
		map[string]any{"lastScheduleTime": "2026-09-30T02:00:00-04:00"})
	p := cnpgScheduleProvider([]*unstructured.Unstructured{c}, []*unstructured.Unstructured{sched}, nil, nil)
	got := detectCNPGScheduledRunIssues(p, cnpgClusterGVR, []*unstructured.Unstructured{c}, cnpgScheduleNow)
	if len(got) != 1 || !got[0].FirstSeen.Equal(time.Date(2026, 9, 30, 6, 0, 0, 0, time.UTC)) || !strings.Contains(got[0].Message, "every day at 02:00 operator clock") {
		t.Fatalf("issues=%+v", got)
	}
	for _, reported := range []string{"", "invalid", "2026-10-01T00:00:00Z", "2026-09-01T00:00:00Z"} {
		_ = unstructured.SetNestedField(sched.Object, reported, "status", "lastScheduleTime")
		if got := detectCNPGScheduledRunIssues(p, cnpgClusterGVR, []*unstructured.Unstructured{c}, cnpgScheduleNow); len(got) != 0 {
			t.Errorf("unestablished run %q raised %+v", reported, got)
		}
	}
}

func TestCNPGScheduledRunNeedsReadableBackups(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2026, 9, 30, h, m, 0, 0, time.UTC) }
	schedules := []*unstructured.Unstructured{cnpgHourly(false)}
	backups := []*unstructured.Unstructured{cnpgBackupAt("b1", "completed", at(12, 0), at(12, 2))}
	plain := cnpgCluster(nil, nil)
	plugin := cnpgCluster(map[string]any{"plugins": []any{map[string]any{
		"name": "barman-cloud.cloudnative-pg.io", "parameters": map[string]any{"barmanObjectName": "store", "serverName": "main-v2"},
	}}}, nil)
	detect := func(p *fakeProvider, c *unstructured.Unstructured) []Issue {
		return detectCNPGScheduledRunIssues(p, cnpgClusterGVR, []*unstructured.Unstructured{c}, cnpgScheduleNow)
	}

	readable := cnpgScheduleProvider([]*unstructured.Unstructured{plain}, schedules, backups, nil)
	if got := detect(readable, plain); len(got) != 1 {
		t.Fatalf("with readable backups the missed run is reported, got %v", reasonsOf(got))
	}

	failed := cnpgScheduleProvider([]*unstructured.Unstructured{plain}, schedules, backups, nil)
	failed.listErr = map[schema.GroupVersionResource]error{cnpgBackupGVR: errors.New("forbidden")}
	if got := detect(failed, plain); len(got) != 0 {
		t.Errorf("a failed Backup list raised %v", reasonsOf(got))
	}

	unwatched := cnpgScheduleProvider([]*unstructured.Unstructured{plain}, schedules, backups, nil)
	delete(unwatched.dynamic, cnpgBackupGVR)
	if got := detect(unwatched, plain); len(got) != 0 {
		t.Errorf("an unwatched Backup kind raised %v", reasonsOf(got))
	}

	storeFailed := cnpgScheduleProvider([]*unstructured.Unstructured{plugin}, schedules, backups, nil)
	storeFailed.listErr = map[schema.GroupVersionResource]error{cnpgObjectStoreGVR: errors.New("forbidden")}
	if got := detect(storeFailed, plugin); len(got) != 0 {
		t.Errorf("an unread ObjectStore raised %v for a plugin cluster", reasonsOf(got))
	}
}

// The Compose path attributes the finding to the Cluster, next to its own
// condition-derived issues.
func TestCNPGScheduledRunThroughCompose(t *testing.T) {
	c := cnpgCluster(map[string]any{"instances": int64(1)}, map[string]any{"phase": "Cluster in healthy state", "readyInstances": int64(1)})
	sched := cnpgSchedObj("ScheduledBackup", "hourly", time.Now().Add(-48*time.Hour),
		map[string]any{"schedule": "0 0 * * * *", "cluster": map[string]any{"name": "pg-main"}}, map[string]any{"lastScheduleTime": time.Now().Add(-time.Hour).Format(time.RFC3339)})
	p := cnpgScheduleProvider([]*unstructured.Unstructured{c}, []*unstructured.Unstructured{sched}, nil, nil)
	got := Compose(p, Filters{Kinds: []string{"Cluster"}, Limit: NoLimit, AllowUnfilteredEvidence: true})
	found := false
	for _, iss := range got {
		if iss.Reason == ReasonCNPGScheduledRunNoBackup {
			found = true
		}
	}
	if !found {
		t.Errorf("issues = %v, want %s", reasonsOf(got), ReasonCNPGScheduledRunNoBackup)
	}
}

func TestCNPGScheduledRunExcludesPredecessorEvidence(t *testing.T) {
	c := cnpgCluster(map[string]any{"plugins": []any{map[string]any{"name": "barman-cloud.cloudnative-pg.io", "parameters": map[string]any{"barmanObjectName": "store", "serverName": "main"}}}}, nil)
	c.SetUID("current")
	c.SetCreationTimestamp(metav1.NewTime(cnpgScheduleNow.Add(-time.Hour)))
	old := cnpgBackupAt("old", "completed", cnpgScheduleNow.Add(-25*time.Minute), cnpgScheduleNow.Add(-20*time.Minute))
	_ = unstructured.SetNestedField(old.Object, "previous", "status", "pluginMetadata", "clusterUID")
	running := old.DeepCopy()
	_ = unstructured.SetNestedField(running.Object, "running", "status", "phase")
	store := cnpgSchedObj("ObjectStore", "store", cnpgScheduleNow.Add(-48*time.Hour), nil, map[string]any{"serverRecoveryWindow": map[string]any{"main": map[string]any{"lastSuccessfulBackupTime": cnpgScheduleNow.Add(-2 * time.Hour).Format(time.RFC3339)}}})
	store.SetAPIVersion("barmancloud.cnpg.io/v1")
	e := &cnpgNamespaceBackupEvidence{schedules: []*unstructured.Unstructured{cnpgHourly(false)}, backups: []*unstructured.Unstructured{old, running}, stores: []*unstructured.Unstructured{store}}
	if last, duration := cnpgLatestSuccessfulBackup(c, e); !last.IsZero() || duration != 0 {
		t.Fatalf("predecessor success counted: %s, %s", last, duration)
	}
	if _, ok := cnpgScheduledRunIssue(cnpgClusterGVR, c, e, cnpgScheduleNow); !ok {
		t.Fatal("predecessor run suppressed current missed-run finding")
	}
	c.SetCreationTimestamp(metav1.NewTime(cnpgScheduleNow.Add(-10 * time.Minute)))
	if _, ok := cnpgScheduledRunIssue(cnpgClusterGVR, c, e, cnpgScheduleNow); ok {
		t.Fatal("schedule fired before current Cluster existed")
	}
}
