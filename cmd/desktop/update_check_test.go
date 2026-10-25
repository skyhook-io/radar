package main

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/timeline"
	versionpkg "github.com/skyhook-io/radar/internal/version"
)

func TestManualUpdateCheckBridgeCoalescesClicksAndEncodesResult(t *testing.T) {
	a := NewDesktopApp(nil, timeline.StoreConfig{})
	a.ctx = context.Background()
	scripts := make(chan string, 4)
	a.execJS = func(script string) { scripts <- script }
	release := make(chan struct{})
	var checks atomic.Int32
	want := &versionpkg.UpdateInfo{CurrentVersion: "1.2.3", LatestVersion: "1.3.0", UpdateAvail: true, ReleaseNotes: "</script>\u2028test"}
	a.checkForUpdate = func(context.Context) *versionpkg.UpdateInfo {
		checks.Add(1)
		<-release
		return want
	}
	item := findMenuItem(t, findSubmenu(t, createMenu(a, "1.2.3", "darwin"), "Help"), "Check for Updates...")
	item.Click(nil)
	if script := <-scripts; !strings.Contains(script, "radar:check-for-updates") {
		t.Fatalf("missing start event: %s", script)
	}
	item.Click(nil)
	close(release)
	var script string
	select {
	case script = <-scripts:
	case <-time.After(5 * time.Second):
		t.Fatal("no result event")
	}
	a.updateCheckMu.Lock()
	a.updateCheckMu.Unlock()
	if checks.Load() != 1 || len(scripts) != 0 {
		t.Fatal("overlapping click started a second check")
	}
	if strings.Contains(script, "</script>") {
		t.Fatal("result contains unescaped script content")
	}
	payload := strings.TrimSuffix(strings.TrimPrefix(script, "window.dispatchEvent(new CustomEvent('radar:update-check-result', {detail:"), "}))")
	var got versionpkg.UpdateInfo
	if err := json.Unmarshal([]byte(payload), &got); err != nil {
		t.Fatalf("invalid event payload: %v", err)
	}
	if got.ReleaseNotes != want.ReleaseNotes || got.LatestVersion != want.LatestVersion {
		t.Fatalf("result changed in transit: %+v", got)
	}
}

func TestManualUpdateCheckDoesNotDispatchAfterCancellation(t *testing.T) {
	a := NewDesktopApp(nil, timeline.StoreConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	a.ctx = ctx
	scripts := make(chan string, 4)
	a.execJS = func(script string) { scripts <- script }
	a.checkForUpdate = func(ctx context.Context) *versionpkg.UpdateInfo {
		<-ctx.Done()
		return &versionpkg.UpdateInfo{Error: ctx.Err().Error()}
	}
	a.checkForUpdates()
	<-scripts
	cancel()
	a.updateCheckMu.Lock()
	a.updateCheckMu.Unlock()
	a.checkForUpdates()
	if len(scripts) != 0 {
		t.Fatal("dispatched an event after shutdown")
	}
}

func TestManualUpdateCheckBeforeStartupIsIgnored(t *testing.T) {
	a := NewDesktopApp(nil, timeline.StoreConfig{})
	a.execJS = func(string) { t.Fatal("dispatched before startup") }
	a.checkForUpdates()
}
