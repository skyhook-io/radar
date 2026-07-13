package argoapi

import (
	"os"
	"path/filepath"
	"testing"
)

const cliConfigFixture = `contexts:
- name: prod
  server: argocd.example.com
  user: prod
- name: local
  server: localhost:8080
  user: local
current-context: local
servers:
- server: argocd.example.com
  grpc-web: true
- server: localhost:8080
  insecure: true
  plain-text: true
users:
- name: prod
  auth-token: prod-token
  refresh-token: prod-refresh
- name: local
  auth-token: local-token
`

func writeFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTokenFromCLIConfig(t *testing.T) {
	tests := []struct {
		name      string
		config    string
		serverURL string
		want      string
		wantErr   bool
	}{
		{
			name:      "match by bare host",
			config:    cliConfigFixture,
			serverURL: "argocd.example.com",
			want:      "prod-token",
		},
		{
			name:      "match tolerates scheme on serverURL",
			config:    cliConfigFixture,
			serverURL: "https://argocd.example.com",
			want:      "prod-token",
		},
		{
			name:      "match tolerates trailing path",
			config:    cliConfigFixture,
			serverURL: "https://argocd.example.com/",
			want:      "prod-token",
		},
		{
			name:      "match by host with port picks the right context",
			config:    cliConfigFixture,
			serverURL: "http://localhost:8080",
			want:      "local-token",
		},
		{
			name:      "empty serverURL falls back to current-context",
			config:    cliConfigFixture,
			serverURL: "",
			want:      "local-token",
		},
		{
			name:      "unknown server errors",
			config:    cliConfigFixture,
			serverURL: "https://other.example.com",
			wantErr:   true,
		},
		{
			name: "missing user errors",
			config: `contexts:
- name: prod
  server: argocd.example.com
  user: ghost
current-context: prod
users: []
`,
			serverURL: "argocd.example.com",
			wantErr:   true,
		},
		{
			name: "user without auth-token errors",
			config: `contexts:
- name: prod
  server: argocd.example.com
  user: prod
users:
- name: prod
`,
			serverURL: "argocd.example.com",
			wantErr:   true,
		},
		{
			name:      "empty serverURL without current-context errors",
			config:    "contexts: []\nusers: []\n",
			serverURL: "",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := writeFixture(t, tt.config)
			got, err := TokenFromCLIConfig(path, tt.serverURL)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got token %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("TokenFromCLIConfig: %v", err)
			}
			if got != tt.want {
				t.Errorf("token = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTokenFromCLIConfigMissingFile(t *testing.T) {
	if _, err := TokenFromCLIConfig(filepath.Join(t.TempDir(), "nope"), "argocd.example.com"); err == nil {
		t.Fatal("expected error for missing config file")
	}
}

func TestCLISessionFromConfig(t *testing.T) {
	const realHostFixture = `contexts:
- name: prod
  server: argocd.example.com
  user: prod
current-context: prod
servers:
- server: argocd.example.com
  insecure: true
users:
- name: prod
  auth-token: prod-token
`

	t.Run("current-context session", func(t *testing.T) {
		sess, err := CLISessionFromConfig(writeFixture(t, realHostFixture))
		if err != nil {
			t.Fatalf("CLISessionFromConfig: %v", err)
		}
		if sess == nil {
			t.Fatal("expected a session, got nil")
		}
		if sess.Server != "argocd.example.com" || sess.User != "prod" || !sess.Insecure {
			t.Errorf("session = %+v, want {argocd.example.com prod true}", sess)
		}
	})

	t.Run("loopback session suppressed (port-forward artifact)", func(t *testing.T) {
		// cliConfigFixture's current-context is `local` → localhost:8080.
		sess, err := CLISessionFromConfig(writeFixture(t, cliConfigFixture))
		if err != nil || sess != nil {
			t.Fatalf("loopback: got (%+v, %v), want (nil, nil)", sess, err)
		}
	})

	t.Run("missing file → nil, no error", func(t *testing.T) {
		sess, err := CLISessionFromConfig(filepath.Join(t.TempDir(), "nope"))
		if err != nil || sess != nil {
			t.Fatalf("missing file: got (%+v, %v), want (nil, nil)", sess, err)
		}
	})

	t.Run("no current-context → nil", func(t *testing.T) {
		sess, err := CLISessionFromConfig(writeFixture(t, "contexts: []\nusers: []\n"))
		if err != nil || sess != nil {
			t.Fatalf("no current-context: got (%+v, %v), want (nil, nil)", sess, err)
		}
	})

	t.Run("context without a token → nil", func(t *testing.T) {
		cfg := "contexts:\n- name: prod\n  server: argocd.example.com\n  user: prod\ncurrent-context: prod\nusers:\n- name: prod\n"
		sess, err := CLISessionFromConfig(writeFixture(t, cfg))
		if err != nil || sess != nil {
			t.Fatalf("no token: got (%+v, %v), want (nil, nil)", sess, err)
		}
	})
}

func TestIsLoopbackHost(t *testing.T) {
	cases := map[string]bool{
		"localhost":              true,
		"localhost:8080":         true,
		"127.0.0.1":              true,
		"127.0.0.1:8080":         true,
		"http://127.0.0.1:8080":  true,
		"127.5.5.5":              true,
		"[::1]:8080":             true,
		"https://[::1]:8080":     true,
		"::1":                    true,
		"argocd.example.com":     false,
		"argocd.example.com:443": false,
		"10.0.0.1:6443":          false,
		"[2001:db8::1]:8080":     false,
	}
	for server, want := range cases {
		if got := isLoopbackHost(server); got != want {
			t.Errorf("isLoopbackHost(%q) = %v, want %v", server, got, want)
		}
	}
}
