package server

import "testing"

func TestIsLogsUnavailableNotice(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"kubelet apology", "unable to retrieve container logs for containerd://6e2396b58f81ea5e", true},
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
