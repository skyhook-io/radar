package server

import (
	"io"
	"net/http"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
)

func TestResourceAvailabilityHead(t *testing.T) {
	for _, tc := range []struct {
		name   string
		path   string
		status int
	}{
		{"present secret", "/api/resources/secrets/default/nginx-tls", http.StatusOK},
		{"absent secret", "/api/resources/secrets/default/missing-secret", http.StatusNotFound},
		{"absent configmap", "/api/resources/configmaps/default/missing-config", http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := http.Head(testServer.URL + tc.path)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.status)
			}
			if resp.Header.Get("X-Radar-Resource-Read") != "true" {
				t.Fatal("missing authorized resource lookup marker")
			}
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if len(body) != 0 {
				t.Fatalf("HEAD transferred %d bytes", len(body))
			}
		})
	}
}

func TestResourceAvailabilityHeadDenied(t *testing.T) {
	env := newAuthTestServer(t)
	env.srv.permCache.Set("alice", nil, &auth.UserPermissions{AllowedNamespaces: []string{"default"}})
	seedServerSecretGetCanI(t, env, "alice", nil, []string{"default"})
	for _, name := range []string{"nginx-tls", "missing-secret"} {
		req, err := http.NewRequest(http.MethodHead, env.ts.URL+"/api/resources/secrets/default/"+name, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Forwarded-User", "alice")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s status = %d, want 403", name, resp.StatusCode)
		}
		if resp.Header.Get("X-Radar-Resource-Read") != "" {
			t.Fatal("denied lookup disclosed a resource marker")
		}
	}
}

func TestResourceAvailabilityHeadAllowed(t *testing.T) {
	env := newAuthTestServer(t)
	env.srv.permCache.Set("bob", nil, &auth.UserPermissions{AllowedNamespaces: []string{"default"}})
	seedServerSecretGetCanI(t, env, "bob", []string{"default"}, nil)
	req, err := http.NewRequest(http.MethodHead, env.ts.URL+"/api/resources/secrets/default/nginx-tls", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Forwarded-User", "bob")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || resp.Header.Get("X-Radar-Resource-Read") != "true" {
		t.Fatalf("authorized HEAD status = %d, marker = %q", resp.StatusCode, resp.Header.Get("X-Radar-Resource-Read"))
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil || len(body) != 0 {
		t.Fatalf("authorized HEAD body length = %d, error = %v", len(body), err)
	}
}
