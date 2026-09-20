package context

import (
	"strings"
	"testing"
)

func TestAuthRedactionAcrossTextSurfaces(t *testing.T) {
	cases := []struct{ name, input, want string }{
		{"basic", "Authorization: Basic YXVkaXQ6ZHVtbXk=", "Authorization: Basic [REDACTED]"},
		{"basic-empty-password", "Authorization: Basic dTo=", "Authorization: Basic [REDACTED]"},
		{"basic-empty-user", "Authorization: Basic OnA=", "Authorization: Basic [REDACTED]"},
		{"basic-json", `{"Authorization":"Basic YXVkaXQ6ZHVtbXk="}`, `{"Authorization":"Basic [REDACTED]"}`},
		{"basic-proxy", "Proxy-Authorization: basic YXVkaXQ6ZHVtbXk=", "Proxy-Authorization: basic [REDACTED]"},
		{"basic-go-header", "Authorization:[Basic YXVkaXQ6ZHVtbXk=]", "Authorization:[Basic [REDACTED]]"},
		{"bearer-multiple-spaces", "Authorization: BEARER  fakeauditcredential1234567890", "Authorization: BEARER  [REDACTED]"},
		{"basic-challenge", `WWW-Authenticate: Basic realm="test"`, `WWW-Authenticate: Basic realm="test"`},
		{"short-bearer-unchanged", "Bearer short", "Bearer short"},
		{"region-prose", "ASIA Pacific asia-southeast1", "ASIA Pacific asia-southeast1"},
		{"basic-short", "Authorization: Basic dTpw", "Authorization: Basic [REDACTED]"},
		{"basic-mixed", "aUtHoRiZaTiOn: bAsIc\tYXVkaXQ6ZHVtbXk=", "aUtHoRiZaTiOn: bAsIc\t[REDACTED]"},
		{"bearer-lower", "authorization: bearer fakeauditcredential1234567890", "authorization: bearer [REDACTED]"},
		{"bearer-mixed", "Authorization: bEaReR\tfakeauditcredential1234567890==", "Authorization: bEaReR\t[REDACTED]"},
		{"asia", "AWS_ACCESS_KEY_ID=ASIAABCDEFGHIJKLMNOP", "AWS_ACCESS_KEY_ID=[REDACTED]"},
		{"akia-control", "key=AKIAABCDEFGHIJKLMNOP", "key=[REDACTED]"},
		{"bearer-control", "Authorization: Bearer fakeauditcredential1234567890", "Authorization: Bearer [REDACTED]"},
		{"url-control", "postgres://audit:dummy@db.example/test", "postgres://audit:[REDACTED]@db.example/test"},
		{"negative-0", "Basic authentication failed for pod api-123", "Basic authentication failed for pod api-123"},
		{"negative-1", "Basic configuration unavailable", "Basic configuration unavailable"},
		{"negative-2", "secretName=app-credentials secretKeyRef=client-auth", "secretName=app-credentials secretKeyRef=client-auth"},
		{"negative-3", "image=nginx@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "image=nginx@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{"negative-4", "containerID=containerd://bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "containerID=containerd://bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		{"negative-5", "region=ASIA_SOUTH request_id=abc-123", "region=ASIA_SOUTH request_id=abc-123"},
		{"negative-6", "AWS_ACCESS_KEY_ID=ASIA1234", "AWS_ACCESS_KEY_ID=ASIA1234"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Run("text", func(t *testing.T) {
				if got := RedactSecrets(tc.input); got != tc.want {
					t.Fatalf("got %q, want %q", got, tc.want)
				}
			})
			t.Run("inline", func(t *testing.T) {
				node := map[string]any{"description": tc.input}
				RedactInlineSecrets(node)
				if got := node["description"]; got != tc.want {
					t.Fatalf("got %q, want %q", got, tc.want)
				}
			})
			for _, mode := range []string{"diagnostic", "fallback", "grep"} {
				t.Run(mode, func(t *testing.T) {
					prefix := "WARN "
					if mode != "diagnostic" {
						prefix = "INFO "
					}
					var got FilteredLogs
					if mode == "grep" {
						var err error
						got, err = FilterLogsByPattern(prefix+tc.input, ".")
						if err != nil {
							t.Fatal(err)
						}
					} else {
						got = FilterLogs(prefix + tc.input)
					}
					if want := prefix + tc.want; strings.Join(got.Lines, "\n") != want {
						t.Fatalf("got %q, want %q", got.Lines, want)
					}
					if got.TotalLines != 1 || got.Fallback != (mode == "fallback") {
						t.Fatalf("unexpected coverage: %+v", got)
					}
				})
			}
		})
	}
}

func TestBasicAuthRedactionDoesNotCrossHeaderLines(t *testing.T) {
	for _, input := range []string{
		"Authorization:",
		"Authorization: Basic ",
		"Authorization:\r\nBasic configuration unavailable",
		"Authorization: Basic\r\nconfiguration unavailable",
	} {
		if got := RedactSecrets(input); got != input {
			t.Errorf("got %q, want %q", got, input)
		}
		node := map[string]any{"description": input}
		RedactInlineSecrets(node)
		if got := node["description"]; got != input {
			t.Errorf("inline got %q, want %q", got, input)
		}
	}
}
