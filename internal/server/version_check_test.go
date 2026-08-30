package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/version"
)

func TestVersionCheckSelectsMeteredAndReleaseOnlyPaths(t *testing.T) {
	ordinary := &version.UpdateInfo{CurrentVersion: "ordinary"}
	releaseOnly := &version.UpdateInfo{CurrentVersion: "release-only"}
	var calls []string
	ordinaryCheck := func(context.Context) *version.UpdateInfo {
		calls = append(calls, "ordinary")
		return ordinary
	}
	releaseOnlyCheck := func(context.Context) *version.UpdateInfo {
		calls = append(calls, "release-only")
		return releaseOnly
	}

	for _, mode := range []k8s.DeploymentMode{k8s.DeploymentModeLocal, k8s.DeploymentModeInCluster} {
		calls = nil
		if got := checkForUpdateForDeployment(context.Background(), mode, ordinaryCheck, releaseOnlyCheck); got != ordinary {
			t.Errorf("mode %q selected release-only update check", mode)
		}
		if len(calls) != 1 || calls[0] != "ordinary" {
			t.Errorf("mode %q calls = %v, want ordinary", mode, calls)
		}
	}

	calls = nil
	if got := checkForUpdateForDeployment(context.Background(), k8s.DeploymentModeCloud, ordinaryCheck, releaseOnlyCheck); got != releaseOnly {
		t.Error("Cloud selected metered update check")
	}
	if len(calls) != 1 || calls[0] != "release-only" {
		t.Errorf("Cloud calls = %v, want release-only", calls)
	}
}

func TestBrowserCheckVolumeBounds(t *testing.T) {
	server := &Server{}
	day := time.Now().UTC().Format("2006-01-02")
	var slots []chan struct{}
	for range maxConcurrentBrowserChecks {
		slot, claimed := server.claimBrowserCheck(day)
		if !claimed {
			t.Fatal("concurrent browser check rejected before reaching the limit")
		}
		slots = append(slots, slot)
	}
	if _, claimed := server.claimBrowserCheck(day); claimed {
		t.Fatal("browser check exceeded concurrency limit")
	}
	for _, slot := range slots {
		<-slot
	}

	server.browserCheckCount = maxBrowserChecksPerDay
	if _, claimed := server.claimBrowserCheck(day); claimed {
		t.Fatal("browser check exceeded daily limit")
	}
	if slot, claimed := server.claimBrowserCheck("next-day"); !claimed {
		t.Fatal("browser check limit did not reset for a new day")
	} else {
		<-slot
	}
}

func TestVersionCheckBrowserAcceptsOneBestEffortAttempt(t *testing.T) {
	previousVersion := version.Current
	version.SetCurrent("dev")
	k8s.ForceInCluster = true
	t.Cleanup(func() {
		version.SetCurrent(previousVersion)
		k8s.ForceInCluster = false
	})

	response := httptest.NewRecorder()
	(&Server{}).handleVersionCheckBrowser(
		response,
		httptest.NewRequest(http.MethodPost, "/api/version-check/browser", nil),
	)
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestVersionCheckBrowserRejectsOtherDeploymentModes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		cloud string
	}{
		{name: "local"},
		{name: "cloud", cloud: "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("RADAR_CLOUD_MODE", tc.cloud)
			restoreLocalMode := k8s.SetTestLocalMode()
			t.Cleanup(restoreLocalMode)

			response := httptest.NewRecorder()
			(&Server{}).handleVersionCheckBrowser(
				response,
				httptest.NewRequest(http.MethodPost, "/api/version-check/browser", nil),
			)
			if response.Code != http.StatusNotFound {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
		})
	}
}
