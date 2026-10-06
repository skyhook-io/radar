package helm

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"

	"helm.sh/helm/v3/pkg/cli"
	"k8s.io/client-go/rest"
)

// The streaming rollback is a cluster write; when a user is attached it must
// reach the API server as that user, never as Radar's ServiceAccount.
func TestRollbackWithProgressAsUser_Impersonates(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	var groups [][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Header.Get("Impersonate-User"))
		groups = append(groups, r.Header.Values("Impersonate-Group"))
		mu.Unlock()
		http.Error(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403}`, http.StatusForbidden)
	}))
	defer srv.Close()

	c := &Client{settings: cli.New(), restConfig: &rest.Config{Host: srv.URL}}
	if err := c.RollbackWithProgressAsUser("team-a", "web", 1, "alice", []string{"radar:idp:team-a"}, nil); err == nil {
		t.Fatal("rollback succeeded against a server that forbids everything")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 {
		t.Fatal("rollback made no API request")
	}
	for i, u := range seen {
		if u != "alice" {
			t.Errorf("request %d Impersonate-User = %q, want alice", i, u)
		}
		// IdP orgs grant access through the group, so dropping it would deny
		// a user Kubernetes allows.
		if !slices.Contains(groups[i], "radar:idp:team-a") {
			t.Errorf("request %d Impersonate-Group = %v, want radar:idp:team-a", i, groups[i])
		}
	}
}
