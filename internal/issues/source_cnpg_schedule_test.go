package issues

import (
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/issuesapi"
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
		map[string]any{"schedule": "0 0 * * * *", "suspend": suspend, "cluster": map[string]any{"name": "pg-main"}}, nil)
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
	}{
		{
			name:      "last success before the 13:00 run, which produced nothing",
			schedules: []*unstructured.Unstructured{cnpgHourly(false)},
			backups:   []*unstructured.Unstructured{cnpgBackupAt("b1", "completed", at(12, 0), at(12, 2)), cnpgBackupAt("b2", "failed", at(13, 0), at(13, 1))},
			want:      "No successful backup since ScheduledBackup hourly fired at 2026-09-30T13:00:00Z (0 0 * * * *)",
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
			name:      "no success ever: measured from the schedule's first fire",
			schedules: []*unstructured.Unstructured{cnpgHourly(false)},
			want:      "No successful backup observed since ScheduledBackup hourly first fired at 2026-09-28T15:00:00Z",
		},
		{
			name:      "the ObjectStore's recovery window counts as a success",
			cluster:   pluginCluster,
			schedules: []*unstructured.Unstructured{cnpgHourly(false)},
			stores:    []*unstructured.Unstructured{store(at(14, 1))},
		},
		{
			name:      "an older ObjectStore success still leaves the 13:00 run unaccounted for",
			cluster:   pluginCluster,
			schedules: []*unstructured.Unstructured{cnpgHourly(false)},
			stores:    []*unstructured.Unstructured{store(at(12, 1))},
			want:      "fired at 2026-09-30T13:00:00Z",
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
			if iss.Category != issuesapi.CategoryBackupFailed {
				t.Errorf("category = %v, want backup_failed", iss.Category)
			}
		})
	}
}

// Without a watched ScheduledBackup kind nothing is known about schedules, so
// nothing is said.
func TestCNPGScheduledRunNeedsTheScheduleKind(t *testing.T) {
	c := cnpgCluster(nil, nil)
	p := &fakeProvider{
		dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{cnpgClusterGVR: {c}},
		kinds:   map[schema.GroupVersionResource]string{cnpgClusterGVR: "Cluster"},
	}
	if got := detectCNPGScheduledRunIssues(p, cnpgClusterGVR, []*unstructured.Unstructured{c}, cnpgScheduleNow); len(got) != 0 {
		t.Errorf("issues = %v", reasonsOf(got))
	}
}

// The Compose path attributes the finding to the Cluster, next to its own
// condition-derived issues.
func TestCNPGScheduledRunThroughCompose(t *testing.T) {
	c := cnpgCluster(map[string]any{"instances": int64(1)}, map[string]any{"phase": "Cluster in healthy state", "readyInstances": int64(1)})
	sched := cnpgSchedObj("ScheduledBackup", "hourly", time.Now().Add(-48*time.Hour),
		map[string]any{"schedule": "0 0 * * * *", "cluster": map[string]any{"name": "pg-main"}}, nil)
	p := cnpgScheduleProvider([]*unstructured.Unstructured{c}, []*unstructured.Unstructured{sched}, nil, nil)
	got := Compose(p, Filters{Kinds: []string{"Cluster"}, Limit: NoLimit})
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
