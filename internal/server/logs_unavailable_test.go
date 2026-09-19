package server

import (
	"errors"
	"testing"
)

// The kubelet answers with a 200 whose entire body is its own apology when it
// no longer holds a container's log file. Without recognising it, that sentence
// reaches the reader as a line their workload appears to have printed.
func TestIsLogsUnavailableNotice(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"kubelet apology", "unable to retrieve container logs for containerd://6e2396b58f81ea5e", true},
		{"with surrounding whitespace", "  unable to retrieve container logs for docker://abc\n", true},
		{"a real line that happens to mention retrieving", "2026-09-18T05:00:00Z unable to retrieve config from vault", false},
		{"ordinary output", "2026-09-18T05:00:00Z checkout serving request 1", false},
		{"empty", "", false},
	} {
		if got := isLogsUnavailableNotice(tc.body); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Asking for an earlier run of a container that never restarted is an ordinary
// fact about the pod. It arrives as a 400 from the apiserver, and surfacing it
// as a 500 told the reader Radar had broken.
func TestIsNoPreviousContainer(t *testing.T) {
	apiserver := errors.New(`previous terminated container "app" in pod "api-0" not found`)
	if !isNoPreviousContainer(apiserver) {
		t.Error("the apiserver's no-earlier-run answer should be recognised")
	}
	if isNoPreviousContainer(errors.New("connection refused")) {
		t.Error("an unrelated failure must not be reported as a missing earlier run")
	}
	if isNoPreviousContainer(nil) {
		t.Error("no error is not a missing earlier run")
	}
}
