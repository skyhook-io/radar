package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/version"
)

func TestBrowserCheckConcurrencyBound(t *testing.T) {
	server := &Server{}
	for range maxConcurrentBrowserChecks {
		if !server.acquireBrowserCheckSlot() {
			t.Fatal("concurrent browser check rejected before reaching the limit")
		}
	}
	if server.acquireBrowserCheckSlot() {
		t.Fatal("browser check exceeded concurrency limit")
	}
	server.releaseBrowserCheckSlot()
	if !server.acquireBrowserCheckSlot() {
		t.Fatal("browser check slot was not reusable after release")
	}
	for range maxConcurrentBrowserChecks {
		server.releaseBrowserCheckSlot()
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
