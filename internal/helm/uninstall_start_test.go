package helm

import (
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/helmhistory"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/release"
	helmtime "helm.sh/helm/v3/pkg/time"
	"k8s.io/client-go/kubernetes/fake"
)

func freshUninstallStarts(t *testing.T) {
	t.Helper()
	previous := uninstallStarts
	uninstallStarts = newUninstallStartMemo()
	t.Cleanup(func() { uninstallStarts = previous })
}

func uninstallRowFromStorage(t *testing.T, rel *release.Release) HelmRelease {
	t.Helper()
	snapshot, err := helmReleaseStorageSnapshotWithClient(fake.NewSimpleClientset(helmReleaseSecret(t, "helm-storage", rel, true)), "")
	if err != nil {
		t.Fatal(err)
	}
	rows := helmReleaseRowsFromStorageSnapshot(snapshot, nil, "")
	if len(rows) != 1 {
		t.Fatalf("rows = %#v", rows)
	}
	return rows[0]
}

func TestUninstallStuckClockSurvivesFailedRetry(t *testing.T) {
	freshUninstallStarts(t)
	now := time.Now().Truncate(time.Second)
	firstAttempt := now.Add(-25 * time.Minute)
	rel := helmTestRelease("deleting", "demo", 2, release.StatusUninstalling, "Deletion in progress")
	rel.Info.LastDeployed = helmtime.Time{Time: now.Add(-48 * time.Hour)}
	rel.Info.Deleted = helmtime.Time{Time: firstAttempt}

	first := uninstallRowFromStorage(t, rel)
	if first.LastOperation == nil || !first.LastOperation.Updated.Equal(firstAttempt) {
		t.Fatalf("first observation = %#v, want stuck since %v", first.LastOperation, firstAttempt)
	}

	// A retry that fails on the same hook rewrites Info.Deleted to its own start.
	rel.Info.Deleted = helmtime.Time{Time: now.Add(-time.Minute)}
	retried := uninstallRowFromStorage(t, rel)
	op := retried.LastOperation
	if op == nil {
		t.Fatal("failed retry hid the stuck uninstall")
	}
	if !op.Updated.Equal(firstAttempt) || !strings.Contains(op.Message, "for 25m") {
		t.Fatalf("after retry: updated = %v, message = %q; want the first attempt's %v and 25m", op.Updated, op.Message, firstAttempt)
	}
}

func TestUninstallStuckClockSurvivesFailedRetryInDetail(t *testing.T) {
	freshUninstallStarts(t)
	now := time.Now().Truncate(time.Second)
	firstAttempt := now.Add(-25 * time.Minute)
	cfg := memoryActionConfig(t)
	rel := &release.Release{
		Name:      "deleting",
		Namespace: "default",
		Version:   1,
		Chart:     &chart.Chart{Metadata: &chart.Metadata{Name: "demo", Version: "1.0.0"}},
		Info: &release.Info{
			Status:       release.StatusUninstalling,
			LastDeployed: helmtime.Time{Time: now.Add(-48 * time.Hour)},
			Deleted:      helmtime.Time{Time: firstAttempt},
		},
	}
	if err := cfg.Releases.Create(rel); err != nil {
		t.Fatal(err)
	}
	if detail, err := getReleaseWith(cfg, "", "default", "deleting"); err != nil || detail.LastOperation == nil {
		t.Fatalf("first detail = %#v, %v", detail, err)
	}

	rel.Info.Deleted = helmtime.Time{Time: now.Add(-time.Minute)}
	if err := cfg.Releases.Update(rel); err != nil {
		t.Fatal(err)
	}
	detail, err := getReleaseWith(cfg, "", "default", "deleting")
	if err != nil {
		t.Fatal(err)
	}
	if detail.LastOperation == nil || !detail.LastOperation.Updated.Equal(firstAttempt) {
		t.Fatalf("detail after retry = %#v, want stuck since %v", detail.LastOperation, firstAttempt)
	}
	if len(detail.History) != 1 || !detail.History[0].Deleted.Equal(rel.Info.Deleted.Time) {
		t.Fatalf("history = %#v, want Helm's own latest Info.Deleted", detail.History)
	}
}

func TestUninstallStartIgnoresReadsThatStraddleAContextSwitch(t *testing.T) {
	freshUninstallStarts(t)
	now := time.Now().Truncate(time.Second)
	firstAttempt := now.Add(-25 * time.Minute)
	deployed := now.Add(-48 * time.Hour)
	previous := uninstallStartKey{context: "previous-context", storageNamespace: "helm-storage", name: "deleting"}
	uninstallStarts.observe(previous, helmhistory.Revision{Revision: 2, Status: "uninstalling", Updated: deployed, Deleted: firstAttempt}, now)
	if op := analyzeReleaseHistory("", "helm-storage", "deleting", 2, []HelmRevision{{Revision: 2, Status: "uninstalling", Updated: deployed, Deleted: firstAttempt}}, helmhistory.Options{}).LastOperation; op == nil {
		t.Fatal("current-context observation did not report the stuck uninstall")
	}

	// The read started under the previous context but ran after the switch, so
	// its same-named release may belong to the current cluster.
	straddling := []HelmRevision{{Revision: 2, Status: "deployed", Updated: deployed}}
	analyzeReleaseHistory("previous-context", "helm-storage", "deleting", 2, straddling, helmhistory.Options{})

	current := uninstallStartKey{storageNamespace: "helm-storage", name: "deleting"}
	for _, key := range []uninstallStartKey{previous, current} {
		if entry, ok := uninstallStarts.entries[key]; !ok || !entry.started.Equal(firstAttempt) {
			t.Fatalf("anchor for %+v = %#v, %v; want it untouched at %v", key, entry, ok, firstAttempt)
		}
	}
}

func TestUninstallStartMemo(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	deployed := now.Add(-48 * time.Hour)
	key := uninstallStartKey{context: "prod", storageNamespace: "helm-storage", name: "deleting"}
	uninstalling := func(revision int, deployed, deleted time.Time) helmhistory.Revision {
		return helmhistory.Revision{Revision: revision, Status: "uninstalling", Updated: deployed, Deleted: deleted}
	}
	first, retry := now.Add(-30*time.Minute), now.Add(-time.Minute)

	t.Run("failed retry keeps the earliest start", func(t *testing.T) {
		m := newUninstallStartMemo()
		m.observe(key, uninstalling(2, deployed, first), now.Add(-20*time.Minute))
		if got := m.observe(key, uninstalling(2, deployed, retry), now); !got.Equal(first) {
			t.Fatalf("got %v, want %v", got, first)
		}
	})
	t.Run("leaving uninstalling resets", func(t *testing.T) {
		m := newUninstallStartMemo()
		m.observe(key, uninstalling(2, deployed, first), now.Add(-20*time.Minute))
		m.observe(key, helmhistory.Revision{Revision: 2, Status: "uninstalled", Updated: deployed, Deleted: first}, now.Add(-10*time.Minute))
		if got := m.observe(key, uninstalling(2, deployed, retry), now); !got.Equal(retry) {
			t.Fatalf("got %v, want %v", got, retry)
		}
	})
	t.Run("revision change resets", func(t *testing.T) {
		m := newUninstallStartMemo()
		m.observe(key, uninstalling(2, deployed, first), now.Add(-20*time.Minute))
		if got := m.observe(key, uninstalling(3, deployed.Add(time.Hour), retry), now); !got.Equal(retry) {
			t.Fatalf("got %v, want %v", got, retry)
		}
	})
	t.Run("reinstall at the same revision resets", func(t *testing.T) {
		m := newUninstallStartMemo()
		m.observe(key, uninstalling(1, deployed, first), now.Add(-20*time.Minute))
		if got := m.observe(key, uninstalling(1, now.Add(-5*time.Minute), retry), now); !got.Equal(retry) {
			t.Fatalf("got %v, want %v", got, retry)
		}
	})
	t.Run("other releases and contexts keep their own clocks", func(t *testing.T) {
		m := newUninstallStartMemo()
		m.observe(key, uninstalling(2, deployed, first), now.Add(-20*time.Minute))
		for _, other := range []uninstallStartKey{
			{context: "staging", storageNamespace: key.storageNamespace, name: key.name},
			{context: key.context, storageNamespace: "other", name: key.name},
			{context: key.context, storageNamespace: key.storageNamespace, name: "other"},
		} {
			if got := m.observe(other, uninstalling(2, deployed, retry), now); !got.Equal(retry) {
				t.Fatalf("%+v: got %v, want %v", other, got, retry)
			}
		}
	})
	t.Run("missing start keeps an observed anchor and invents none", func(t *testing.T) {
		m := newUninstallStartMemo()
		if got := m.observe(key, uninstalling(2, deployed, time.Time{}), now.Add(-20*time.Minute)); !got.IsZero() {
			t.Fatalf("got %v, want no start", got)
		}
		m.observe(key, uninstalling(2, deployed, first), now.Add(-10*time.Minute))
		if got := m.observe(key, uninstalling(2, deployed, time.Time{}), now); !got.Equal(first) {
			t.Fatalf("got %v, want %v", got, first)
		}
	})
	t.Run("unobserved releases are dropped", func(t *testing.T) {
		m := newUninstallStartMemo()
		m.observe(key, uninstalling(2, deployed, first), now)
		later := now.Add(uninstallStartRetain + time.Hour)
		m.observe(uninstallStartKey{context: "prod", storageNamespace: "helm-storage", name: "other"}, helmhistory.Revision{Revision: 1, Status: "deployed"}, later)
		if _, ok := m.entries[key]; ok || len(m.entries) != 0 {
			t.Fatalf("entries = %#v, want the stale anchor dropped", m.entries)
		}
	})
}
