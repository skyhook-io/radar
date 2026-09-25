package prom

import "testing"

func TestRedactURLsDropsTheAddressAndKeepsTheCall(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want string
	}{
		{
			name: "userinfo credentials",
			in:   `upstream returned 503 for https://admin:s3cret@prom.internal:9090/api/v1/query_range?query=up: overloaded`,
			want: `upstream returned 503 for <redacted>/api/v1/query_range: overloaded`,
		},
		{
			// An auth proxy is commonly configured with the credential in the
			// query string rather than in userinfo.
			name: "query-string token",
			in:   `prom: query error from https://prom.internal:9090/api/v1/query?token=hunter2: bad_data (execution)`,
			want: `prom: query error from <redacted>/api/v1/query: bad_data (execution)`,
		},
		{
			name: "quoted url from net/url",
			in:   `Get "http://prom.internal:9090/api/v1/query": dial tcp: i/o timeout`,
			want: `Get "<redacted>/api/v1/query": dial tcp: i/o timeout`,
		},
		{
			name: "bare address with no path",
			in:   `prom: query error from https://prom.internal:9090: bad_data`,
			want: `prom: query error from <redacted>: bad_data`,
		},
		{
			name: "no url to redact",
			in:   `kube_pod_owner returned no series`,
			want: `kube_pod_owner returned no series`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := RedactURLs(tc.in); got != tc.want {
				t.Errorf("RedactURLs(%q)\n got %q\nwant %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSafeAddressKeepsTheHostAndDropsTheCredential(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://admin:s3cret@prom.internal:9090", "https://prom.internal:9090"},
		{"https://prom.internal:9090/prefix?token=hunter2", "https://prom.internal:9090/prefix"},
		{"http://prom.internal:9090", "http://prom.internal:9090"},
		{"https://admin:s3cr%zz@prom.internal", "<redacted>"},
	} {
		if got := SafeAddress(tc.in); got != tc.want {
			t.Errorf("SafeAddress(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
