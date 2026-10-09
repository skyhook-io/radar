package k8score

import "testing"

func TestIsLogsUnavailableNotice(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"kubelet notice", "unable to retrieve container logs for containerd://6e2396b58f81ea5e", true},
		{"with surrounding whitespace", "  unable to retrieve container logs for docker://abc\n", true},
		{"a timestamped line that mentions retrieving", "2026-09-18T05:00:00Z unable to retrieve config from vault", false},
		{"ordinary output", "2026-09-18T05:00:00Z checkout serving request 1", false},
		{"the sentence followed by real output", "unable to retrieve container logs for containerd://abc\nlistening on :8080", false},
		{"log file missing on the node", `failed to try resolving symlinks in path "/var/log/pods/shop_web-0_1f2e/app/0.log": lstat /var/log/pods/shop_web-0_1f2e/app/0.log: no such file or directory`, true},
		{"a timestamped line that mentions symlinks", "2026-09-18T05:00:00Z failed to try resolving symlinks in path /data", false},
		{"empty", "", false},
	} {
		if got := IsLogsUnavailableNotice(tc.body); got != tc.want {
			t.Errorf("%s: got %v, want %v", tc.name, got, tc.want)
		}
	}
}
