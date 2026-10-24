package k8score

import (
	"strings"
	"testing"
)

func TestLogFollowError(t *testing.T) {
	for _, tc := range []struct {
		name, line string
		failure    bool
	}{
		{"file limit", "failed to create fsnotify watcher: too many open files", true},
		{"system file limit", "failed to create fsnotify watcher: too many open files in system\n", true},
		{"watch limit", `failed to watch directory "/var/log/private": no space left on device`, true},
		{"unavailable", "unable to retrieve container logs for containerd://abc", true},
		{"application message", "2026-10-20T10:00:00Z failed to create fsnotify watcher: too many open files", false},
		{"partial application record", "2026-10-20T10:00:00Z partialfailed to create fsnotify watcher: too many open files", false},
		{"near match", "failed to create fsnotify watcher: unknown error", false},
		{"application multiline", "application output\nfailed to create fsnotify watcher: too many open files", false},
		{"other directory failure", `failed to watch directory "/var/log/private": permission denied`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := LogFollowError(tc.line)
			if (err != nil) != tc.failure {
				t.Fatalf("error=%v, want failure=%v", err, tc.failure)
			}
			if err != nil && strings.Contains(err.Error(), "/var/log/private") {
				t.Fatal("node path leaked into the message")
			}
		})
	}
	if IsLogsUnavailableNotice("failed to create fsnotify watcher: too many open files") {
		t.Fatal("the snapshot matcher must preserve this ambiguous application output")
	}
}
