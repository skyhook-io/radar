package velero

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/internal/integration"
	domain "github.com/skyhook-io/radar/pkg/velero"
)

var testNow = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

func obj(kind, ns, name string, labels map[string]any, spec, status map[string]any) *unstructured.Unstructured {
	meta := map[string]any{"name": name, "namespace": ns, "creationTimestamp": testNow.Add(-time.Hour).Format(time.RFC3339)}
	if labels != nil {
		meta["labels"] = labels
	}
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "velero.io/v1", "kind": kind, "metadata": meta, "spec": spec, "status": status}}
}

type fakeRead struct {
	objects map[string][]*unstructured.Unstructured // resource -> objects
	access  map[string]integration.KindAccess       // resource -> access (default full)
}

func (f fakeRead) reader() *Reader {
	return &Reader{
		Now: func() time.Time { return testNow },
		Observations: Observations{
			Installed: true,
			WorkspaceRead: func(_ context.Context, k integration.WorkspaceKind, _ []string) (integration.KindAccess, []*unstructured.Unstructured) {
				acc, ok := f.access[k.Resource]
				if !ok {
					acc = integration.KindAccess{State: integration.KindCoverageFull, All: true}
				}
				if !acc.Covers("velero") && acc.State != integration.KindCoveragePartial {
					return acc, nil
				}
				return acc, f.objects[k.Resource]
			},
			Deployments: func(context.Context, string) ([]*appsv1.Deployment, bool) { return nil, false },
		},
	}
}

func failedBackup(ns, name string) *unstructured.Unstructured {
	return obj("Backup", ns, name, nil, map[string]any{}, map[string]any{"phase": "Failed", "failureReason": "boom"})
}

func failedRestore(ns, name string) *unstructured.Unstructured {
	return obj("Restore", ns, name, nil, map[string]any{}, map[string]any{"phase": "Failed", "failureReason": "boom"})
}

// Issues come from the objects the caller read, so a kind denied here raises
// nothing, and a kind read only in some namespaces raises only there.
func TestWorkspaceIssuesComeOnlyFromKindsReadThere(t *testing.T) {
	f := fakeRead{
		objects: map[string][]*unstructured.Unstructured{
			"backups":  {failedBackup("velero", "b"), failedBackup("kommander", "k")},
			"restores": {failedRestore("velero", "r")},
		},
		access: map[string]integration.KindAccess{
			"restores": {State: integration.KindCoverageDenied},
			"backups":  {State: integration.KindCoveragePartial, Namespaces: map[string]bool{"velero": true}},
		},
	}
	got := f.reader().Workspace(context.Background())
	if len(got.Issues) != 1 || got.Issues[0].Kind != "Backup" || got.Issues[0].Name != "b" {
		t.Fatalf("issues %+v, want only the backup in the namespace whose Backups were read", got.Issues)
	}
	if got.IssueTotals["velero"] != 1 || got.IssueTotals["kommander"] != 0 {
		t.Fatalf("totals %+v must count only rows from objects read", got.IssueTotals)
	}
}

func TestWorkspaceIssuesCapPerInstallation(t *testing.T) {
	var backups []*unstructured.Unstructured
	for i := range maxIssuesPerInstallation + 5 {
		backups = append(backups, failedBackup("velero", fmt.Sprint("b", i)))
	}
	backups = append(backups, failedBackup("kommander", "x"))
	f := fakeRead{objects: map[string][]*unstructured.Unstructured{"backups": backups}}
	got := f.reader().Workspace(context.Background())
	shown := map[string]int{}
	for _, i := range got.Issues {
		shown[i.Namespace]++
	}
	if shown["velero"] != maxIssuesPerInstallation || shown["kommander"] != 1 {
		t.Fatalf("shown %+v, want the cap per installation so one cannot crowd out another", shown)
	}
	if got.IssueTotals["velero"] != maxIssuesPerInstallation+5 {
		t.Fatalf("totals %+v", got.IssueTotals)
	}
}

// BenchmarkWorkspace10kBackups: 50 hourly schedules keeping 200 backups each,
// three locations. The per-request cost should grow with the backups, not with
// schedules × backups.
func BenchmarkWorkspace10kBackups(b *testing.B) {
	var schedules, backups, locations []*unstructured.Unstructured
	for s := range 50 {
		name := fmt.Sprintf("sched-%02d", s)
		schedules = append(schedules, obj("Schedule", "velero", name, nil,
			map[string]any{"schedule": "0 * * * *", "template": map[string]any{"storageLocation": fmt.Sprint("loc-", s%3)}},
			map[string]any{"phase": "Enabled", "lastBackup": testNow.Add(-30 * time.Minute).Format(time.RFC3339)}))
		for i := range 200 {
			backups = append(backups, obj("Backup", "velero", fmt.Sprintf("%s-%d", name, i), map[string]any{domain.ScheduleNameLabel: name},
				map[string]any{"storageLocation": fmt.Sprint("loc-", s%3)},
				map[string]any{"phase": "Completed", "expiration": testNow.Add(24 * time.Hour).Format(time.RFC3339)}))
		}
	}
	for l := range 3 {
		locations = append(locations, obj("BackupStorageLocation", "velero", fmt.Sprint("loc-", l), nil, map[string]any{"default": l == 0}, map[string]any{"phase": "Available"}))
	}
	f := fakeRead{objects: map[string][]*unstructured.Unstructured{"schedules": schedules, "backups": backups, "backupstoragelocations": locations}}
	r := f.reader()
	b.ReportAllocs()
	for b.Loop() {
		_ = r.Workspace(context.Background())
	}
}

func TestRuns(t *testing.T) {
	at := func(h int) string { return testNow.Add(time.Duration(-h) * time.Hour).Format(time.RFC3339) }
	backups := []*unstructured.Unstructured{
		obj("Backup", "velero", "older", map[string]any{domain.ScheduleNameLabel: "daily"}, map[string]any{"storageLocation": "default"},
			map[string]any{"phase": "Completed", "startTimestamp": at(5), "progress": map[string]any{"totalItems": int64(10), "itemsBackedUp": int64(10)}}),
		obj("Backup", "velero", "newer", nil, map[string]any{}, map[string]any{"phase": "PartiallyFailed", "startTimestamp": at(1), "errors": int64(2)}),
		obj("Backup", "other", "elsewhere", nil, map[string]any{}, map[string]any{"phase": "Completed", "startTimestamp": at(2)}),
	}
	restore := obj("Restore", "velero", "copy", nil, map[string]any{"backupName": "older", "namespaceMapping": map[string]any{"app": "app-copy"}, "includedNamespaces": []any{"app", "web"}, "excludedNamespaces": []any{"tmp"}},
		map[string]any{"phase": "Completed", "startTimestamp": at(1), "progress": map[string]any{"totalItems": int64(4), "itemsRestored": int64(4)}})

	t.Run("newest first, with what the lists show", func(t *testing.T) {
		resp := fakeRead{objects: map[string][]*unstructured.Unstructured{"backups": backups}}.reader().Runs(context.Background(), RunsKey("backups"), "", RunsFilter{})
		if len(resp.Runs) != 3 || resp.Runs[0].Name != "newer" || resp.Runs[2].Name != "older" {
			t.Fatalf("runs %+v", resp.Runs)
		}
		old := resp.Runs[2]
		if old.Schedule != "daily" || old.StorageLocation != "default" || old.ItemsDone == nil || *old.ItemsDone != 10 || *old.ItemsTotal != 10 {
			t.Errorf("run %+v", old)
		}
		if resp.Runs[0].Errors != 2 || resp.Runs[0].ItemsTotal != nil {
			t.Errorf("an uncounted run must not read as zero items: %+v", resp.Runs[0])
		}
	})

	t.Run("one installation", func(t *testing.T) {
		resp := fakeRead{objects: map[string][]*unstructured.Unstructured{"backups": backups}}.reader().Runs(context.Background(), RunsKey("backups"), "velero", RunsFilter{})
		if len(resp.Runs) != 2 || resp.Total != 2 {
			t.Fatalf("runs %+v", resp.Runs)
		}
	})

	t.Run("a restore's source and mapping", func(t *testing.T) {
		resp := fakeRead{objects: map[string][]*unstructured.Unstructured{"restores": {restore}}}.reader().Runs(context.Background(), RunsKey("restores"), "", RunsFilter{})
		r := resp.Runs[0]
		if r.BackupName != "older" || r.NamespaceMapping["app"] != "app-copy" || r.ItemsDone == nil || *r.ItemsDone != 4 {
			t.Errorf("restore %+v", r)
		}
		if r.IncludedNamespaces == nil || strings.Join(*r.IncludedNamespaces, ",") != "app,web" || r.ExcludedNamespaces == nil || strings.Join(*r.ExcludedNamespaces, ",") != "tmp" {
			t.Errorf("a restore's namespaces %v / %v", r.IncludedNamespaces, r.ExcludedNamespaces)
		}
	})

	t.Run("a restore of every namespace says so, and a backup carries no selection", func(t *testing.T) {
		every := obj("Restore", "velero", "all", nil, map[string]any{"backupName": "older"}, map[string]any{"phase": "Completed"})
		resp := fakeRead{objects: map[string][]*unstructured.Unstructured{"restores": {every}}}.reader().Runs(context.Background(), RunsKey("restores"), "", RunsFilter{})
		body, _ := json.Marshal(resp.Runs[0])
		if !strings.Contains(string(body), `"includedNamespaces":[]`) || !strings.Contains(string(body), `"excludedNamespaces":[]`) {
			t.Errorf("restore %s", body)
		}
		backupsResp := fakeRead{objects: map[string][]*unstructured.Unstructured{"backups": backups}}.reader().Runs(context.Background(), RunsKey("backups"), "", RunsFilter{})
		body, _ = json.Marshal(backupsResp.Runs[0])
		if strings.Contains(string(body), "includedNamespaces") {
			t.Errorf("backup %s", body)
		}
	})

	t.Run("a denied kind says so, with no runs", func(t *testing.T) {
		resp := fakeRead{
			objects: map[string][]*unstructured.Unstructured{"backups": backups},
			access:  map[string]integration.KindAccess{"backups": {State: integration.KindCoverageDenied}},
		}.reader().Runs(context.Background(), RunsKey("backups"), "", RunsFilter{})
		if resp.Coverage.State != integration.KindCoverageDenied || len(resp.Runs) != 0 {
			t.Errorf("coverage %+v runs %d", resp.Coverage, len(resp.Runs))
		}
	})

	if RunsKey("schedules") != "" {
		t.Error("only backups and restores are runs")
	}
}

func TestRunsFilterBeforeCapAndReasons(t *testing.T) {
	for _, kind := range []string{"backups", "restores"} {
		t.Run(kind, func(t *testing.T) {
			var items []*unstructured.Unstructured
			for i := range maxRuns + 1 {
				items = append(items, obj("Backup", "velero", fmt.Sprintf("newer-%d", i), nil, map[string]any{"backupName": "other"}, map[string]any{"startTimestamp": testNow.Format(time.RFC3339)}))
			}
			match := obj("Backup", "velero", "old-match", map[string]any{domain.ScheduleNameLabel: "nightly"}, map[string]any{"backupName": "source"}, map[string]any{"phase": "FailedValidation", "validationErrors": []any{"first", "second"}})
			items = append(items, match)
			reader := fakeRead{objects: map[string][]*unstructured.Unstructured{kind: items}}.reader()
			resp := reader.Runs(t.Context(), RunsKey(kind), "velero", RunsFilter{Schedule: "nightly", Backup: "source"})
			if resp.Total != 1 || len(resp.Runs) != 1 || resp.Truncated || resp.Runs[0].Reason != "first; second" {
				t.Fatalf("%+v", resp)
			}
			match.Object["status"].(map[string]any)["failureReason"] = "preferred"
			resp = reader.Runs(t.Context(), RunsKey(kind), "velero", RunsFilter{Schedule: "nightly", Backup: "source"})
			if resp.Runs[0].Reason != "preferred" {
				t.Fatal(resp.Runs[0])
			}
		})
	}
}

func TestScheduleActivityBackupControllerFact(t *testing.T) {
	s := obj("Schedule", "velero", "nightly", nil, map[string]any{"schedule": "0 * * * *"}, map[string]any{"phase": "Enabled"})
	reader := fakeRead{objects: map[string][]*unstructured.Unstructured{"schedules": {s}}}.reader()
	one := int32(1)
	reader.Observations.Deployments = func(context.Context, string) ([]*appsv1.Deployment, bool) {
		return []*appsv1.Deployment{{
			Spec:   appsv1.DeploymentSpec{Replicas: &one, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Command: []string{"/velero", "server"}, Args: []string{"--disable-controllers=backup"}}}}}},
			Status: appsv1.DeploymentStatus{Replicas: 1, UpdatedReplicas: 1},
		}}, true
	}
	response, readable, found := reader.ScheduleActivity(t.Context(), "velero", "nightly")
	if !readable || !found || !response.BackupsDisabled || response.ServerStopped || response.Reading.State == domain.StateSchedulingDisabled {
		t.Fatalf("%+v read=%v found=%v", response, readable, found)
	}
}
