package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
)

// fakeNamespaceAPIServer publishes a clientset whose namespace LIST and
// SubjectAccessReview answers come from the given status codes, so the
// handlers see real client errors rather than a stubbed result.
func fakeNamespaceAPIServer(t *testing.T, listStatus, sarStatus int, namespaces ...string) {
	t.Helper()
	apiserver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/namespaces":
			if listStatus != http.StatusOK {
				w.WriteHeader(listStatus)
				_ = json.NewEncoder(w).Encode(map[string]any{"kind": "Status", "apiVersion": "v1", "status": "Failure", "code": listStatus})
				return
			}
			items := make([]map[string]any, 0, len(namespaces))
			for _, ns := range namespaces {
				items = append(items, map[string]any{"metadata": map[string]any{"name": ns}})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"kind": "NamespaceList", "apiVersion": "v1", "items": items})
		case r.Method == http.MethodPost && r.URL.Path == "/apis/authorization.k8s.io/v1/subjectaccessreviews":
			w.WriteHeader(sarStatus)
			_ = json.NewEncoder(w).Encode(map[string]any{"kind": "Status", "apiVersion": "v1", "status": "Failure", "code": sarStatus})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(apiserver.Close)

	client, err := kubernetes.NewForConfig(&rest.Config{Host: apiserver.URL})
	if err != nil {
		t.Fatalf("build clientset: %v", err)
	}
	prevClient := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(prevClient) })
	prevConn := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	t.Cleanup(func() { k8s.SetConnectionStatus(prevConn) })
}

func TestGetAccessibleNamespacesWithSource(t *testing.T) {
	cases := []struct {
		name       string
		listStatus int
		want       k8s.NamespaceListSource
	}{
		{"list succeeds", http.StatusOK, k8s.NamespaceListCluster},
		{"list forbidden", http.StatusForbidden, k8s.NamespaceListSeeded},
		{"list unauthorized", http.StatusUnauthorized, k8s.NamespaceListSeeded},
		{"list errors", http.StatusInternalServerError, k8s.NamespaceListFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fakeNamespaceAPIServer(t, tc.listStatus, http.StatusForbidden, "alpha", "beta")
			if _, got := k8s.GetAccessibleNamespacesWithSource(context.Background()); got != tc.want {
				t.Errorf("source = %q, want %q", got, tc.want)
			}
		})
	}
}

// A failed namespace LIST leaves only the seeds, which says nothing about
// whether a saved pick outside them is still valid.
func TestNamespaceScope_ListFailureKeepsSavedPick(t *testing.T) {
	prevCtx := k8s.SetTestContextName("test-ctx")
	t.Cleanup(func() { k8s.SetTestContextName(prevCtx) })
	// The context namespace is the only seed; a nil seed list would pass every
	// pick through untouched and never reach the trim.
	prevNs := k8s.SetTestContextNamespace("default")
	t.Cleanup(func() { k8s.SetTestContextNamespace(prevNs) })
	fakeNamespaceAPIServer(t, http.StatusInternalServerError, http.StatusForbidden)

	srv := New(Config{DevMode: true})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() {
		ts.Close()
		srv.Stop()
	})
	req := httptest.NewRequest("GET", "/api/cluster/namespace-scope", nil)
	// "broken" exists in the shared fixture cache, so the deleted-namespace
	// prune keeps it; it is not among the seeds.
	srv.setActiveNamespaceForUser(req, []string{"broken"})

	resp, err := http.Get(ts.URL + "/api/cluster/namespace-scope")
	if err != nil {
		t.Fatalf("GET namespace-scope: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("namespace-scope status = %d, want 200", resp.StatusCode)
	}
	if got := srv.getActiveNamespaceForUser(req); len(got) != 1 || got[0] != "broken" {
		t.Errorf("saved pick after failed LIST = %v, want [broken]", got)
	}
}

func TestNamespaceScope_DiscoveryFailureKeepsSavedPick(t *testing.T) {
	prevCtx := k8s.SetTestContextName("test-ctx")
	t.Cleanup(func() { k8s.SetTestContextName(prevCtx) })
	fakeNamespaceAPIServer(t, http.StatusOK, http.StatusInternalServerError, "default", "broken")
	env := newAuthTestServer(t)
	aliceReq := requestWithUser("GET", "/api/cluster/namespace-scope", &auth.User{Username: "alice"})
	// "default" exists in the shared fixture cache, so the deleted-namespace
	// prune leaves it alone and only the access-check path could drop it.
	env.srv.setActiveNamespaceForUser(aliceReq, []string{"default"})

	resp := env.authGet(t, "/api/cluster/namespace-scope", "alice", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("namespace-scope status = %d, want 200", resp.StatusCode)
	}
	if got := env.srv.getActiveNamespaceForUser(aliceReq); len(got) != 1 || got[0] != "default" {
		t.Errorf("saved pick after failed access check = %v, want [default]", got)
	}
}

func TestParseNamespacesForUser_DiscoveryFailureKeepsSavedPick(t *testing.T) {
	prevCtx := k8s.SetTestContextName("test-ctx")
	t.Cleanup(func() { k8s.SetTestContextName(prevCtx) })
	fakeNamespaceAPIServer(t, http.StatusOK, http.StatusInternalServerError, "default", "broken")
	env := newAuthTestServer(t)
	aliceReq := requestWithUser("GET", "/api/resources/pods", &auth.User{Username: "alice"})
	env.srv.setActiveNamespaceForUser(aliceReq, []string{"default"})

	if got := env.srv.parseNamespacesForUser(aliceReq); len(got) != 0 {
		t.Errorf("namespaces on failed access check = %v, want fail-closed empty", got)
	}
	if got := env.srv.getActiveNamespaceForUser(aliceReq); len(got) != 1 || got[0] != "default" {
		t.Errorf("saved pick after failed access check = %v, want [default]", got)
	}
}
