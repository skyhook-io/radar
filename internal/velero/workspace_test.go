package velero

import (
	"context"
	"fmt"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/issues"
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

func (f fakeRead) reader(iss []issues.Issue) *Reader {
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
			Issues:      func(context.Context, []string) []issues.Issue { return iss },
		},
	}
}

func TestWorkspaceIssuesNeedTheirSubjectAndEveryRequiredRead(t *testing.T) {
	f := fakeRead{
		objects: map[string][]*unstructured.Unstructured{"schedules": {obj("Schedule", "velero", "s", nil, map[string]any{"schedule": "0 * * * *"}, map[string]any{"phase": "Enabled"})}},
		access:  map[string]integration.KindAccess{"restores": {State: integration.KindCoverageDenied}},
	}
	iss := []issues.Issue{
		{ID: "backup", Group: domain.Group, Kind: "Backup", Namespace: "velero", Name: "b"},
		{ID: "restore", Group: domain.Group, Kind: "Restore", Namespace: "velero", Name: "r"},
		{ID: "needs-restores", Group: domain.Group, Kind: "Backup", Namespace: "velero", Name: "c",
			RequiredReads: []issues.EvidenceRead{{Group: domain.Group, Resource: "restores", Namespace: "velero", Verb: "list"}}},
		{ID: "needs-pods", Group: domain.Group, Kind: "Backup", Namespace: "velero", Name: "d",
			RequiredReads: []issues.EvidenceRead{{Resource: "pods", Namespace: "velero", Verb: "list"}}},
		{ID: "cnpg", Group: "postgresql.cnpg.io", Kind: "Backup", Namespace: "velero", Name: "e"},
	}
	got := f.reader(iss).Workspace(context.Background())
	if len(got.Issues) != 1 || got.Issues[0].ID != "backup" {
		t.Fatalf("issues %+v, want only the backup whose reads are all covered", got.Issues)
	}
	if got.IssueTotals["velero"] != 1 {
		t.Fatalf("totals %+v must count only what is shown or withheld by the cap, never denied rows", got.IssueTotals)
	}
}

func TestWorkspaceIssuesCapPerInstallation(t *testing.T) {
	var iss []issues.Issue
	for i := range maxIssuesPerInstallation + 5 {
		iss = append(iss, issues.Issue{ID: fmt.Sprint("a", i), Group: domain.Group, Kind: "Backup", Namespace: "velero", Name: fmt.Sprint(i)})
	}
	iss = append(iss, issues.Issue{ID: "other", Group: domain.Group, Kind: "Backup", Namespace: "kommander", Name: "x"})
	f := fakeRead{objects: map[string][]*unstructured.Unstructured{}, access: map[string]integration.KindAccess{}}
	got := f.reader(iss).Workspace(context.Background())
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
// three locations. The numbers go in the PR; the point is that the per-request
// cost grows with the backups, not with schedules × backups.
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
	r := f.reader(nil)
	b.ReportAllocs()
	for b.Loop() {
		_ = r.Workspace(context.Background())
	}
}
