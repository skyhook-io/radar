package helm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func countingHelmStorage(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var lists atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/version":
			_, _ = io.WriteString(w, `{"major":"1","minor":"30","gitVersion":"v1.30.0"}`)
		case r.URL.Path == "/api":
			_, _ = io.WriteString(w, `{"kind":"APIVersions","versions":["v1"]}`)
		case r.URL.Path == "/apis":
			_, _ = io.WriteString(w, `{"kind":"APIGroupList","apiVersion":"v1","groups":[]}`)
		case strings.HasSuffix(r.URL.Path, "/secrets"):
			lists.Add(1)
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"SecretList","items":[]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, &lists
}

// A client bound to an explicit rest.Config must read release storage on
// that cluster, even when Radar's own client points somewhere else.
func TestStandaloneClientReadsReleaseStorageOnItsOwnCluster(t *testing.T) {
	radarCluster, radarLists := countingHelmStorage(t)
	ownCluster, ownLists := countingHelmStorage(t)
	radarClient, err := kubernetes.NewForConfig(&rest.Config{Host: radarCluster.URL})
	if err != nil {
		t.Fatal(err)
	}
	previous := k8s.SetTestClient(radarClient)
	t.Cleanup(func() { k8s.SetTestClient(previous) })

	c := NewStandaloneClient(&rest.Config{Host: ownCluster.URL})
	ctx := context.Background()
	if _, err := c.BatchCheckUpgrades(ctx, "shop"); err != nil {
		t.Fatalf("BatchCheckUpgrades: %v", err)
	}
	if _, err := c.ListReleasesAcrossNamespaces(ctx, []string{"shop"}, "", nil); err != nil {
		t.Fatalf("ListReleasesAcrossNamespaces: %v", err)
	}
	if _, err := c.ListReleases(ctx, "shop"); err != nil {
		t.Fatalf("ListReleases: %v", err)
	}
	if radarLists.Load() != 0 || ownLists.Load() != 3 {
		t.Fatalf("release storage lists: Radar's cluster %d, the client's own %d; want 0 and 3", radarLists.Load(), ownLists.Load())
	}
}

// With no explicit rest.Config and Radar's client not set up yet, a release
// read reports that instead of panicking on a nil client.
func TestReleaseStorageReadWithoutAClusterClientFails(t *testing.T) {
	previous := k8s.SetTestClient(nil)
	t.Cleanup(func() { k8s.SetTestClient(previous) })
	c := &Client{}
	if _, err := c.ListReleasesAcrossNamespaces(context.Background(), []string{"shop"}, "", nil); err == nil {
		t.Fatal("want an error with no cluster client")
	}
}
