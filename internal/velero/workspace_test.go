package velero

import (
	"context"
	"fmt"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
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
