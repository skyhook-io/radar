package argoapi

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestManagedResourcesQueryEncodingAndAuth(t *testing.T) {
	tests := []struct {
		name       string
		query      ManagedResourcesQuery
		wantPath   string
		wantParams url.Values
	}{
		{
			name:       "app name only",
			query:      ManagedResourcesQuery{AppName: "guestbook"},
			wantPath:   "/api/v1/applications/guestbook/managed-resources",
			wantParams: url.Values{},
		},
		{
			name: "all filters set",
			query: ManagedResourcesQuery{
				AppName:      "guestbook",
				AppNamespace: "argocd",
				Project:      "default",
				Group:        "apps",
				Kind:         "Deployment",
				Namespace:    "prod",
				Name:         "web",
			},
			wantPath: "/api/v1/applications/guestbook/managed-resources",
			wantParams: url.Values{
				"appNamespace": {"argocd"},
				"project":      {"default"},
				"group":        {"apps"},
				"kind":         {"Deployment"},
				"namespace":    {"prod"},
				"name":         {"web"},
			},
		},
		{
			name:     "partial filters omit empties",
			query:    ManagedResourcesQuery{AppName: "guestbook", Kind: "Service"},
			wantPath: "/api/v1/applications/guestbook/managed-resources",
			wantParams: url.Values{
				"kind": {"Service"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotPath, gotAuth string
			var gotParams url.Values
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				gotParams = r.URL.Query()
				gotAuth = r.Header.Get("Authorization")
				_, _ = w.Write([]byte(`{"items": []}`))
			}))
			defer srv.Close()

			c := New(Options{BaseURL: srv.URL, Token: "test-token"})
			if _, err := c.ManagedResources(context.Background(), tt.query); err != nil {
				t.Fatalf("ManagedResources: %v", err)
			}
			if gotPath != tt.wantPath {
				t.Errorf("path = %q, want %q", gotPath, tt.wantPath)
			}
			if gotAuth != "Bearer test-token" {
				t.Errorf("Authorization = %q, want Bearer test-token", gotAuth)
			}
			if len(gotParams) != len(tt.wantParams) {
				t.Errorf("params = %v, want %v", gotParams, tt.wantParams)
			}
			for k, want := range tt.wantParams {
				if got := gotParams.Get(k); got != want[0] {
					t.Errorf("param %s = %q, want %q", k, got, want[0])
				}
			}
		})
	}
}

func TestManagedResourcesDecodesItems(t *testing.T) {
	fixture := `{
		"items": [
			{
				"group": "apps",
				"kind": "Deployment",
				"namespace": "prod",
				"name": "web",
				"targetState": "{\"apiVersion\":\"apps/v1\",\"kind\":\"Deployment\"}",
				"liveState": "{\"apiVersion\":\"apps/v1\",\"kind\":\"Deployment\",\"status\":{}}",
				"normalizedLiveState": "{\"apiVersion\":\"apps/v1\",\"kind\":\"Deployment\"}",
				"predictedLiveState": "{\"apiVersion\":\"apps/v1\",\"kind\":\"Deployment\",\"spec\":{}}",
				"modified": true
			},
			{
				"kind": "Job",
				"namespace": "prod",
				"name": "db-migrate",
				"hook": true
			}
		]
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(fixture))
	}))
	defer srv.Close()

	c := New(Options{BaseURL: srv.URL, Token: "t"})
	items, err := c.ManagedResources(context.Background(), ManagedResourcesQuery{AppName: "guestbook"})
	if err != nil {
		t.Fatalf("ManagedResources: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("len(items) = %d, want 2", len(items))
	}

	d := items[0]
	if d.Group != "apps" || d.Kind != "Deployment" || d.Namespace != "prod" || d.Name != "web" {
		t.Errorf("identity fields = %+v", d)
	}
	if !strings.Contains(d.TargetState, `"kind":"Deployment"`) {
		t.Errorf("TargetState = %q", d.TargetState)
	}
	if !strings.Contains(d.LiveState, `"status"`) {
		t.Errorf("LiveState = %q", d.LiveState)
	}
	if d.NormalizedLiveState == "" || d.PredictedLiveState == "" {
		t.Errorf("normalized/predicted states missing: %+v", d)
	}
	if !d.Modified {
		t.Error("Modified = false, want true")
	}
	if d.Hook {
		t.Error("items[0].Hook = true, want false")
	}
	if !items[1].Hook {
		t.Error("items[1].Hook = false, want true")
	}
}

func TestManagedResourcesRequiresAppName(t *testing.T) {
	c := New(Options{BaseURL: "http://unused"})
	if _, err := c.ManagedResources(context.Background(), ManagedResourcesQuery{}); err == nil {
		t.Fatal("expected error for empty AppName")
	}
}

func TestRevisionMetadata(t *testing.T) {
	var gotPath, gotAuth string
	var gotParams url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotParams = r.URL.Query()
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{
			"author": "Jane Doe <jane@example.com>",
			"date": "2026-07-09T12:00:00Z",
			"tags": ["v1.2.0"],
			"message": "fix: raise memory to 512Mi",
			"signatureInfo": "gpg: Good signature from Jane"
		}`))
	}))
	defer srv.Close()

	c := New(Options{BaseURL: srv.URL, Token: "test-token"})
	meta, err := c.RevisionMetadata(context.Background(), RevisionMetadataQuery{
		AppName:      "guestbook",
		AppNamespace: "argocd",
		Project:      "default",
		SourceIndex:  "1",
		Revision:     "abc123",
	})
	if err != nil {
		t.Fatalf("RevisionMetadata: %v", err)
	}
	if gotPath != "/api/v1/applications/guestbook/revisions/abc123/metadata" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotParams.Get("appNamespace") != "argocd" || gotParams.Get("project") != "default" || gotParams.Get("sourceIndex") != "1" {
		t.Errorf("params = %v", gotParams)
	}
	if meta.Author != "Jane Doe <jane@example.com>" || meta.Message != "fix: raise memory to 512Mi" {
		t.Errorf("meta = %+v", meta)
	}
	if len(meta.Tags) != 1 || meta.Tags[0] != "v1.2.0" {
		t.Errorf("tags = %v", meta.Tags)
	}
	if meta.SignatureInfo == "" {
		t.Error("signatureInfo empty, want a value")
	}
}

func TestRevisionMetadataRequiresAppAndRevision(t *testing.T) {
	c := New(Options{BaseURL: "http://unused"})
	if _, err := c.RevisionMetadata(context.Background(), RevisionMetadataQuery{Revision: "abc"}); err == nil {
		t.Error("expected error for empty AppName")
	}
	if _, err := c.RevisionMetadata(context.Background(), RevisionMetadataQuery{AppName: "app"}); err == nil {
		t.Error("expected error for empty Revision")
	}
}

func TestRepositories(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"items":[
			{"repo":"https://github.com/org/broken","type":"git","connectionState":{"status":"Failed","message":"authentication required"}},
			{"repo":"https://github.com/org/healthy","connectionState":{"status":"Successful"}}
		]}`))
	}))
	defer srv.Close()

	c := New(Options{BaseURL: srv.URL, Token: "test-token"})
	repos, err := c.Repositories(context.Background())
	if err != nil {
		t.Fatalf("Repositories: %v", err)
	}
	if gotPath != "/api/v1/repositories" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer test-token" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if len(repos) != 2 {
		t.Fatalf("len(repos) = %d, want 2", len(repos))
	}
	if repos[0].Repo != "https://github.com/org/broken" || repos[0].ConnectionState.Status != "Failed" {
		t.Errorf("repos[0] = %+v", repos[0])
	}
	if repos[0].ConnectionState.Message != "authentication required" {
		t.Errorf("repos[0] message = %q", repos[0].ConnectionState.Message)
	}
	if repos[1].ConnectionState.Status != "Successful" {
		t.Errorf("repos[1] status = %q", repos[1].ConnectionState.Status)
	}
}

func TestStatusErrors(t *testing.T) {
	tests := []struct {
		name          string
		status        int
		body          string
		wantIs        error
		wantContain   string
		wantNotSecret string
	}{
		{
			name:        "401 unauthorized",
			status:      http.StatusUnauthorized,
			body:        `{"error": "invalid session", "message": "invalid session: token is expired", "code": 16}`,
			wantIs:      ErrUnauthorized,
			wantContain: "token is expired",
		},
		{
			name:        "403 forbidden",
			status:      http.StatusForbidden,
			body:        `{"error": "permission denied", "message": "permission denied: applications, get", "code": 7}`,
			wantIs:      ErrUnauthorized,
			wantContain: "permission denied",
		},
		{
			name:        "404 not found",
			status:      http.StatusNotFound,
			body:        `{"error": "not found", "message": "applications.argoproj.io \"nope\" not found", "code": 5}`,
			wantIs:      ErrNotFound,
			wantContain: "not found",
		},
		{
			name:        "500 with argo error body extracts message",
			status:      http.StatusInternalServerError,
			body:        `{"error": "boom", "message": "error getting cached app managed resources"}`,
			wantContain: "error getting cached app managed resources",
		},
		{
			name:        "500 with argo body but no message falls back to error field",
			status:      http.StatusInternalServerError,
			body:        `{"error": "boom"}`,
			wantContain: "boom",
		},
		{
			// A non-structured body (HTML error page, proxy dump) is NOT echoed —
			// it can carry secret/manifest content and lands in server logs.
			name:        "502 with non-JSON body is omitted, not echoed",
			status:      http.StatusBadGateway,
			body:        "upstream connect error with secret token abc123",
			wantContain: "omitted",
		},
		{
			name:          "long structured message is capped, raw body never leaks",
			status:        http.StatusInternalServerError,
			body:          `{"message": "` + strings.Repeat("x", 500) + `"}`,
			wantContain:   "…",
			wantNotSecret: strings.Repeat("x", 500),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer srv.Close()

			c := New(Options{BaseURL: srv.URL, Token: "t"})
			_, err := c.ManagedResources(context.Background(), ManagedResourcesQuery{AppName: "guestbook"})
			if err == nil {
				t.Fatalf("expected error for status %d", tt.status)
			}
			if tt.wantIs != nil && !errors.Is(err, tt.wantIs) {
				t.Errorf("errors.Is(err, %v) = false, err = %v", tt.wantIs, err)
			}
			if tt.wantIs == nil && (errors.Is(err, ErrUnauthorized) || errors.Is(err, ErrNotFound)) {
				t.Errorf("err unexpectedly matches a sentinel: %v", err)
			}
			if !strings.Contains(err.Error(), tt.wantContain) {
				t.Errorf("err = %q, want substring %q", err, tt.wantContain)
			}
			if tt.wantNotSecret != "" && strings.Contains(err.Error(), tt.wantNotSecret) {
				t.Errorf("err leaked raw body content: %q", err)
			}
		})
	}
}

func TestUserInfo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/session/userinfo" {
			t.Errorf("path = %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer t" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error": "no session", "message": "no session information"}`))
			return
		}
		_, _ = w.Write([]byte(`{"loggedIn": true, "username": "admin", "iss": "argocd", "groups": ["admins"]}`))
	}))
	defer srv.Close()

	c := New(Options{BaseURL: srv.URL, Token: "t"})
	info, err := c.UserInfo(context.Background())
	if err != nil {
		t.Fatalf("UserInfo: %v", err)
	}
	if !info.LoggedIn || info.Username != "admin" || info.Iss != "argocd" || len(info.Groups) != 1 {
		t.Errorf("UserInfo = %+v", info)
	}

	unauth := New(Options{BaseURL: srv.URL})
	if _, err := unauth.UserInfo(context.Background()); !errors.Is(err, ErrUnauthorized) {
		t.Errorf("err = %v, want ErrUnauthorized", err)
	}
}

func TestVersion(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/version" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"Version": "v2.13.1+af54ef8", "BuildDate": "2024-11-20"}`))
	}))
	defer srv.Close()

	c := New(Options{BaseURL: srv.URL})
	v, err := c.Version(context.Background())
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if v != "v2.13.1+af54ef8" {
		t.Errorf("Version = %q", v)
	}
}

func TestInsecureSkipTLSVerify(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"Version": "v2.13.1"}`))
	}))
	defer srv.Close()

	strict := New(Options{BaseURL: srv.URL})
	if _, err := strict.Version(context.Background()); err == nil {
		t.Fatal("expected TLS verification failure without InsecureSkipTLSVerify")
	}

	insecure := New(Options{BaseURL: srv.URL, InsecureSkipTLSVerify: true})
	v, err := insecure.Version(context.Background())
	if err != nil {
		t.Fatalf("Version with InsecureSkipTLSVerify: %v", err)
	}
	if v != "v2.13.1" {
		t.Errorf("Version = %q", v)
	}
}
