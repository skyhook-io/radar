package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func useWhatsNewStorage(t *testing.T, server bool) {
	t.Helper()
	orig := whatsNewUsesServerStorage
	whatsNewUsesServerStorage = func(*Server) bool { return server }
	t.Cleanup(func() { whatsNewUsesServerStorage = orig })
}

func getWhatsNew(t *testing.T) whatsNewResponse {
	t.Helper()
	w := httptest.NewRecorder()
	(&Server{}).handleGetWhatsNew(w, httptest.NewRequest(http.MethodGet, "/api/whats-new", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body %s", w.Code, w.Body.String())
	}
	var resp whatsNewResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func markWhatsNewSeen(body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	(&Server{}).handleMarkWhatsNewSeen(w, httptest.NewRequest(http.MethodPost, "/api/whats-new/seen", strings.NewReader(body)))
	return w
}

func TestWhatsNewRecordsSeenVersionUnderRadarDir(t *testing.T) {
	useWhatsNewStorage(t, true)
	home := t.TempDir()
	t.Setenv("HOME", home)

	resp := getWhatsNew(t)
	if resp.Storage != "server" {
		t.Fatalf("storage = %q, want server for a local install", resp.Storage)
	}
	if resp.SeenVersion != nil {
		t.Fatalf("seenVersion = %q before anything was recorded, want absent", *resp.SeenVersion)
	}
	if resp.PriorInstall != whatsNewPriorInstall {
		t.Fatalf("priorInstall = %v, want the startup snapshot %v", resp.PriorInstall, whatsNewPriorInstall)
	}

	if w := markWhatsNewSeen(`{"version":"v1.15.0"}`); w.Code != http.StatusNoContent {
		t.Fatalf("POST status = %d, body %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(filepath.Join(home, ".radar", whatsNewFile)); err != nil {
		t.Fatalf("state file not written: %v", err)
	}
	if got := getWhatsNew(t).SeenVersion; got == nil || *got != "v1.15.0" {
		t.Fatalf("seenVersion after POST = %v, want v1.15.0", got)
	}
}

func TestWhatsNewSeenVersionOnlyMovesForward(t *testing.T) {
	useWhatsNewStorage(t, true)
	t.Setenv("HOME", t.TempDir())
	for _, v := range []string{"v1.15.2", "v1.14.9", "v1.15.2-rc.1"} {
		if w := markWhatsNewSeen(`{"version":"` + v + `"}`); w.Code != http.StatusNoContent {
			t.Fatalf("POST %s: status = %d", v, w.Code)
		}
	}
	if got := getWhatsNew(t).SeenVersion; got == nil || *got != "v1.15.2" {
		t.Fatalf("seenVersion = %v, want v1.15.2 kept over older writes", got)
	}
	if w := markWhatsNewSeen(`{"version":"v1.16.0"}`); w.Code != http.StatusNoContent {
		t.Fatalf("POST v1.16.0: status = %d", w.Code)
	}
	if got := getWhatsNew(t).SeenVersion; got == nil || *got != "v1.16.0" {
		t.Fatalf("seenVersion = %v, want v1.16.0", got)
	}
}

func TestWhatsNewConcurrentAcknowledgmentsKeepTheNewest(t *testing.T) {
	useWhatsNewStorage(t, true)
	t.Setenv("HOME", t.TempDir())
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(minor int) {
			defer wg.Done()
			markWhatsNewSeen(fmt.Sprintf(`{"version":"v1.%d.0"}`, minor))
		}(i % 20)
	}
	wg.Wait()
	if got := getWhatsNew(t).SeenVersion; got == nil || *got != "v1.19.0" {
		t.Fatalf("seenVersion = %v after concurrent writes, want v1.19.0", got)
	}
}

func TestWhatsNewReplacesAnUnreadableRecord(t *testing.T) {
	useWhatsNewStorage(t, true)
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".radar")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, whatsNewFile), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := getWhatsNew(t).SeenVersion; got != nil {
		t.Fatalf("seenVersion = %q from a corrupt file, want absent", *got)
	}
	if w := markWhatsNewSeen(`{"version":"v1.15.0"}`); w.Code != http.StatusNoContent {
		t.Fatalf("POST status = %d", w.Code)
	}
	if got := getWhatsNew(t).SeenVersion; got == nil || *got != "v1.15.0" {
		t.Fatalf("seenVersion = %v, want the corrupt record replaced", got)
	}
	leftovers, _ := filepath.Glob(filepath.Join(dir, "*.tmp"))
	if len(leftovers) != 0 {
		t.Fatalf("temp files left behind: %v", leftovers)
	}
}

func TestWhatsNewRejectsMalformedSeenVersions(t *testing.T) {
	useWhatsNewStorage(t, true)
	t.Setenv("HOME", t.TempDir())
	for _, body := range []string{`{"version":""}`, `{"version":"latest"}`, `{"version":"v1.2.3","extra":1}`, `not json`} {
		if w := markWhatsNewSeen(body); w.Code != http.StatusBadRequest {
			t.Errorf("POST %s: status = %d, want 400", body, w.Code)
		}
	}
	if w := markWhatsNewSeen(`{"version":"` + strings.Repeat("1", 2048) + `"}`); w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized body: status = %d, want 413", w.Code)
	}
}

func TestWhatsNewLeavesInClusterStateToTheBrowser(t *testing.T) {
	useWhatsNewStorage(t, false)
	home := t.TempDir()
	t.Setenv("HOME", home)
	resp := getWhatsNew(t)
	if resp.Storage != "browser" || resp.SeenVersion != nil || resp.PriorInstall {
		t.Fatalf("in-cluster response = %+v, want browser storage and no server state", resp)
	}
	if resp.CurrentVersion == "" {
		t.Fatal("currentVersion missing")
	}
	if w := markWhatsNewSeen(`{"version":"v1.15.0"}`); w.Code != http.StatusConflict {
		t.Fatalf("POST status = %d, want 409", w.Code)
	}
	if _, err := os.Stat(filepath.Join(home, ".radar", whatsNewFile)); !os.IsNotExist(err) {
		t.Fatalf("in-cluster POST wrote a state file (err=%v)", err)
	}
}

func TestRadarDirHadStateIgnoresItsOwnFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if radarDirHadState() {
		t.Fatal("missing ~/.radar reported as prior state")
	}
	dir := filepath.Join(home, ".radar")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, whatsNewFile), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if radarDirHadState() {
		t.Fatal("only the What's New file present, reported as prior state")
	}
	if err := os.WriteFile(filepath.Join(dir, "install-id"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !radarDirHadState() {
		t.Fatal("existing install-id not reported as prior state")
	}
}
