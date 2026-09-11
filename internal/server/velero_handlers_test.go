package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
)

// Velero resolves an unset spec.storageLocation to the location marked default.
// Reading absent as "no location" would drop every backup taken with the default
// settings out of the list — which is most of them — and leave a storage location
// page saying nothing depends on it.
func TestVeleroBackupLocation_AppliesVeleroDefault(t *testing.T) {
	for _, c := range []struct {
		name        string
		spec        map[string]interface{}
		defaultName string
		want        string
	}{
		{"explicit location", map[string]interface{}{"storageLocation": "dr-replica"}, "primary", "dr-replica"},
		{"absent uses the location flagged default", map[string]interface{}{}, "primary", "primary"},
		{"empty uses the location flagged default", map[string]interface{}{"storageLocation": ""}, "primary", "primary"},
		// Nothing claims the flag: fall back to the conventional name rather than
		// dropping the backup out of every location's list.
		{"absent with no flagged default", map[string]interface{}{}, "", "default"},
	} {
		t.Run(c.name, func(t *testing.T) {
			u := &unstructured.Unstructured{Object: map[string]interface{}{"spec": c.spec}}
			if got := veleroBackupLocation(u, c.defaultName); got != c.want {
				t.Errorf("veleroBackupLocation(%v, %q) = %q, want %q", c.spec, c.defaultName, got, c.want)
			}
		})
	}
}

// The name "default" is a convention, not the contract — the contract is
// spec.default on the location. An install that renamed its default location
// would otherwise have every backup taken with default settings attributed to a
// location that does not exist, and the real one would report holding nothing.
func TestVeleroDefaultLocationName(t *testing.T) {
	loc := func(name string, isDefault bool) *unstructured.Unstructured {
		u := &unstructured.Unstructured{Object: map[string]interface{}{
			"spec": map[string]interface{}{"default": isDefault},
		}}
		u.SetName(name)
		return u
	}

	if got := veleroDefaultLocationName([]*unstructured.Unstructured{
		loc("dr-replica", false), loc("primary", true),
	}); got != "primary" {
		t.Errorf("got %q, want the location carrying the flag", got)
	}

	// None flagged: say so rather than guessing, and let the caller fall back.
	if got := veleroDefaultLocationName([]*unstructured.Unstructured{
		loc("a", false), loc("b", false),
	}); got != "" {
		t.Errorf("got %q, want empty when no location claims the flag", got)
	}

	if got := veleroDefaultLocationName(nil); got != "" {
		t.Errorf("got %q, want empty for no locations", got)
	}
}

// A caller who may not list Backups is told so. An empty list would read as "this
// storage location holds nothing", which on the page someone opens to decide
// whether they can still restore is the worst possible wrong answer.
func TestVeleroStoredBackups_DeniesRatherThanReportingAnEmptyLocation(t *testing.T) {
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"velero"}}
	allow(perms, veleroGroup, "backups", "velero", false)
	env.srv.permCache.Set("nobody", nil, perms)

	resp := env.authGet(t, "/api/velero/backupstoragelocations/velero/default/backups", "nobody", "")
	defer resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("status = %d, want 403 for a caller who may not list backups", resp.StatusCode)
	}
}

// Same contract as the other reverse lookups: an absent CRD is a real "nothing
// here"; every other failure is a failure to look and must not read as an empty
// location.
func TestVeleroStoredBackups_SeparatesAnAbsentCRDFromAFailedRead(t *testing.T) {
	src := mustReadSource(t, "velero_handlers.go")
	for _, want := range []string{"k8s.ErrUnknownDynamicKind", "errDynamicNotSynced", "listDynamicSynced", "sanitizeForLog"} {
		if !strings.Contains(src, want) {
			t.Errorf("handler does not use %s — a failed or unsynced read would report an empty location", want)
		}
	}
}

func mustReadSource(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

// Velero retains backups until their TTL expires, so one location can hold
// thousands. The list is a page and the counts are the location's — the panel
// reading this renders a chip per entry and prints "N of M", so an uncapped list
// is both a wall of chips and a response that grows without bound.
func TestVeleroStoredBackupsIsAPageNotTheWholeLocation(t *testing.T) {
	if maxStoredBackupsListed <= 0 {
		t.Fatal("the cap must be positive")
	}
	// Newest-first ordering is what makes truncation safe: the most recent
	// restorable point is derived from this list, so the newest must survive.
	resp := VeleroStoredBackupsResponse{}
	for i := 0; i < maxStoredBackupsListed+50; i++ {
		resp.Backups = append(resp.Backups, VeleroStoredBackup{
			Namespace: "velero",
			Name:      fmt.Sprintf("b-%03d", i),
			Phase:     "Completed",
			Completed: fmt.Sprintf("2026-08-%02dT00:00:00Z", 1+(i%27)),
		})
	}
	total := len(resp.Backups)
	sort.Slice(resp.Backups, func(i, j int) bool {
		if resp.Backups[i].Completed != resp.Backups[j].Completed {
			return resp.Backups[i].Completed > resp.Backups[j].Completed
		}
		return resp.Backups[i].Name < resp.Backups[j].Name
	})
	newest := resp.Backups[0]
	resp.Stored = total
	if len(resp.Backups) > maxStoredBackupsListed {
		resp.Backups = resp.Backups[:maxStoredBackupsListed]
		resp.Truncated = true
	}

	if !resp.Truncated {
		t.Error("a location holding more than the cap must report the list as truncated")
	}
	if resp.Stored != total {
		t.Errorf("Stored = %d, want %d — the count describes the location, not the page", resp.Stored, total)
	}
	if len(resp.Backups) != maxStoredBackupsListed {
		t.Errorf("listed %d, want the cap of %d", len(resp.Backups), maxStoredBackupsListed)
	}
	if resp.Backups[0].Name != newest.Name {
		t.Errorf("newest entry %q did not survive truncation (got %q) — the restorable point is derived from it",
			newest.Name, resp.Backups[0].Name)
	}
}

// "Restorable" is the number the storage location page leads with, and Velero
// deletes a backup once its expiration passes — so a Completed backup that has
// aged out is not something to go back to. While Velero's controller is down
// those sit in the list looking finished long after the data behind them went,
// which is exactly when someone is reading this page.
func TestVeleroExpiredDecidesWhatCountsAsARestorePoint(t *testing.T) {
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	future := time.Now().Add(time.Hour).UTC().Format(time.RFC3339)

	for _, c := range []struct {
		name, expiration string
		want             bool
	}{
		{"past its expiration", past, true},
		{"still inside its TTL", future, false},
		// Velero only sets expiration once a backup has an outcome, and a backup
		// with no TTL never expires. Reading absent as expired would drop every
		// one of those out of the count.
		{"no expiration set", "", false},
		// A field Radar cannot parse says nothing about the backup. Guessing
		// "expired" would delete restore points from the count on a formatting
		// difference.
		{"unparseable", "whenever", false},
	} {
		if got := veleroExpired(c.expiration); got != c.want {
			t.Errorf("%s: veleroExpired(%q) = %v, want %v", c.name, c.expiration, got, c.want)
		}
	}

	// No source-level guard on the wiring: matching the handler's text broke the
	// moment the expression was refactored, while the behaviour it stood for was
	// unchanged. TestVeleroStoredBackupsCountsWhatCanActuallyBeRestored drives
	// the handler over an expired backup and asserts both counts, which is what
	// the grep was approximating.
}

// The counts on the storage location page, driven through the handler rather
// than recomputed beside it. Restorable is the number an operator reads to
// answer "can I restore" — it is wrong if it counts a backup Velero has already
// aged out, and wrong in the other direction if it misses one taken with the
// default settings, which is most of them.
func TestVeleroStoredBackupsCountsWhatCanActuallyBeRestored(t *testing.T) {
	past := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	future := time.Now().Add(720 * time.Hour).UTC().Format(time.RFC3339)

	backup := func(name, location, phase, expiration, completed string) *unstructured.Unstructured {
		spec := map[string]any{}
		if location != "" {
			spec["storageLocation"] = location
		}
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "velero.io/v1",
			"kind":       "Backup",
			"metadata":   map[string]any{"name": name, "namespace": "velero"},
			"spec":       spec,
			"status": map[string]any{
				"phase": phase, "expiration": expiration, "completionTimestamp": completed,
			},
		}}
	}
	location := func(name string, isDefault bool) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "velero.io/v1",
			"kind":       "BackupStorageLocation",
			"metadata":   map[string]any{"name": name, "namespace": "velero"},
			"spec":       map[string]any{"default": isDefault},
		}}
	}

	// The install that catches the assumption: the default location is not the
	// one named "default".
	seedVeleroDynamic(t,
		location("primary", true),
		location("dr-replica", false),
		backup("nightly-fresh", "", "Completed", future, "2026-08-16T01:00:00Z"),
		backup("nightly-aged", "", "Completed", past, "2026-06-01T01:00:00Z"),
		backup("nightly-broke", "", "Failed", future, "2026-08-15T01:00:00Z"),
		backup("dr-copy", "dr-replica", "Completed", future, "2026-08-16T02:00:00Z"),
	)

	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"velero"}}
	allow(perms, veleroGroup, "backups", "velero", true)
	env.srv.permCache.Set("reader", nil, perms)

	resp := env.authGet(t, "/api/velero/backupstoragelocations/velero/primary/backups", "reader", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var got VeleroStoredBackupsResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// The three backups that named no location belong to "primary" because it
	// carries spec.default — not to a location called "default".
	if got.Stored != 3 {
		t.Errorf("Stored = %d, want 3 — the backups with no location belong to the default one: %+v", got.Stored, got.Backups)
	}
	if got.Restorable != 1 {
		t.Errorf("Restorable = %d, want 1 — only nightly-fresh is Completed and still inside its TTL", got.Restorable)
	}
	// Both counts are the location's, not the page's. The panel prints them
	// beside Stored, so one derived from a capped list would disagree with the
	// total sitting next to it.
	if got.Expired != 1 {
		t.Errorf("Expired = %d, want 1 — nightly-aged completed but has aged out", got.Expired)
	}
	if len(got.Backups) == 0 || got.Backups[0].Name != "nightly-fresh" {
		t.Errorf("first entry = %+v, want nightly-fresh — the most recent restorable point is read off the top", got.Backups)
	}
	for _, b := range got.Backups {
		if b.Name == "dr-copy" {
			t.Error("dr-copy is stored in dr-replica and must not be counted against primary")
		}
	}
}

// An absent Velero is a real "nothing is stored here". Every other read failure
// is a failure to look, and must not render as an empty location — including a
// cache that has not synced, which is unknown rather than empty.
func TestVeleroStoredBackupsSeparatesNoVeleroFromAFailedRead(t *testing.T) {
	// A synced cache that has never heard of a Backup: Velero is not installed.
	seedVeleroKinds(t, []k8s.APIResource{{
		Group: veleroGroup, Version: "v1", Kind: "BackupStorageLocation",
		Name: "backupstoragelocations", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"},
	}})
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"velero"}}
	allow(perms, veleroGroup, "backups", "velero", true)
	env.srv.permCache.Set("reader", nil, perms)

	resp := env.authGet(t, "/api/velero/backupstoragelocations/velero/primary/backups", "reader", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 — a cluster with no Velero stores nothing", resp.StatusCode)
	}
	var got VeleroStoredBackupsResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Stored != 0 || len(got.Backups) != 0 {
		t.Errorf("got %+v, want an empty location", got)
	}

	// The other half of the contract — a cache that has not synced must not read
	// as an empty location — has no seam to drive from here: tearing the test
	// cache down routes through a different branch, so an assertion here would
	// pass without touching the one it names. It is pinned at the source level
	// instead, the same way the CNPG and policy lookups pin it.
}

var veleroTestKinds = []k8s.APIResource{
	{Group: veleroGroup, Version: "v1", Kind: "Backup", Name: "backups", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}},
	{Group: veleroGroup, Version: "v1", Kind: "BackupStorageLocation", Name: "backupstoragelocations", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}},
}

func seedVeleroDynamic(t *testing.T, objs ...runtime.Object) {
	t.Helper()
	seedVeleroKinds(t, veleroTestKinds, objs...)
}

func seedVeleroKinds(t *testing.T, kinds []k8s.APIResource, objs ...runtime.Object) {
	t.Helper()
	backupGVR := schema.GroupVersionResource{Group: veleroGroup, Version: "v1", Resource: "backups"}
	bslGVR := schema.GroupVersionResource{Group: veleroGroup, Version: "v1", Resource: "backupstoragelocations"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			backupGVR: "BackupList",
			bslGVR:    "BackupStorageLocationList",
		}, objs...)
	if err := k8s.InitTestDynamicResourceCache(dyn, kinds); err != nil {
		t.Fatalf("seed velero: %v", err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
}
