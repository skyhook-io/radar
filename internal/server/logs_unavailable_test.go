package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsLogsUnavailableNotice(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"kubelet apology", "unable to retrieve container logs for containerd://6e2396b58f81ea5e", true},
		{"capitalised variant", "Unable to retrieve container logs for containerd://6e2396b58f81", true},
		{"with surrounding whitespace", "  unable to retrieve container logs for docker://abc\n", true},
		{"a real log line that mentions retrieving", "2026-09-18T05:00:00Z unable to retrieve config from vault", false},
		{"ordinary output", "2026-09-18T05:00:00Z RUN-da859e57 booting", false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		if got := isLogsUnavailableNotice(tc.body); got != tc.want {
			t.Errorf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}

// Whether a container has logs changes constantly, and several answers this
// handler gives are cacheable by default. A browser that caches one keeps
// showing it after the container has restarted and the logs exist.
func TestPodLogsAreNotCacheable(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/api/pods/shop/api-0/logs?container=app", nil)
	rec := httptest.NewRecorder()

	// No cluster client, so this returns early; the header must already be set.
	s.handlePodLogs(rec, req)

	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}

// The follow path reaches this message for the current run as well, so it must
// not describe every failure as a missing previous run.
func TestLogsUnavailableMessageMatchesWhatWasAsked(t *testing.T) {
	current := logsUnavailableMessage(false)
	if strings.Contains(strings.ToLower(current), "previous") {
		t.Errorf("current-run message claims a previous run: %q", current)
	}

	prev := logsUnavailableMessage(true)
	if !strings.Contains(prev, "previous run") {
		t.Errorf("previous-run message does not say which run: %q", prev)
	}
	// The current run is still readable, and the control that gets there is
	// two inches above the message. A dead end here sends the reader away.
	if !strings.Contains(prev, "Deselect Previous run") {
		t.Errorf("previous-run message offers no way out: %q", prev)
	}

	// "the node" is a machine the reader has no relationship with. Every
	// reader-facing sentence names Kubernetes instead.
	for _, m := range []string{current, prev} {
		if strings.Contains(strings.ToLower(m), "the node") {
			t.Errorf("message %q names the node to a reader who has never met it", m)
		}
	}

	// An instruction has to point at a control that exists. "Try again in a
	// moment" pointed at nothing.
	if !strings.Contains(current, "Select Refresh") {
		t.Errorf("current-run message names no control: %q", current)
	}

	// Neither may assert that the lines are gone. The node answers the same
	// way when its runtime is briefly unreachable.
	for _, m := range []string{current, prev} {
		for _, claim := range []string{"no longer", "deleted", "gone", "lost"} {
			if strings.Contains(strings.ToLower(m), claim) {
				t.Errorf("message %q asserts %q, which the node did not tell us", m, claim)
			}
		}
	}
}

// The workload snapshot is the pod snapshot's twin: same mutable answer, same
// default cacheability, and it was left out of the first fix.
func TestWorkloadLogsAreNotCacheable(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodGet, "/api/workloads/Deployment/shop/api/logs", nil)
	rec := httptest.NewRecorder()

	s.handleWorkloadLogs(rec, req)

	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}
}
