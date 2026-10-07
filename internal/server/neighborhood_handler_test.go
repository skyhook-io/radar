package server

import (
	"encoding/json"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/topology"
)

func TestParseNeighborhoodOptions_Defaults(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/ai/neighborhood/Pod/prod/cart", nil)
	opts := parseNeighborhoodOptions(r)
	if opts.Profile != topology.ProfileAuto {
		t.Errorf("default profile = %q, want auto", opts.Profile)
	}
	if opts.Hops != 1 {
		t.Errorf("default hops = %d, want 1", opts.Hops)
	}
	if opts.MaxNodes != 25 {
		t.Errorf("default max_nodes = %d, want 25", opts.MaxNodes)
	}
}

func TestParseNeighborhoodOptions_Custom(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/ai/neighborhood/Pod/prod/cart?profile=all&hops=2&max_nodes=10", nil)
	opts := parseNeighborhoodOptions(r)
	if opts.Profile != topology.ProfileAll {
		t.Errorf("profile = %q, want all", opts.Profile)
	}
	if opts.Hops != 2 {
		t.Errorf("hops = %d, want 2", opts.Hops)
	}
	if opts.MaxNodes != 10 {
		t.Errorf("max_nodes = %d, want 10", opts.MaxNodes)
	}
}

func TestParseNeighborhoodOptions_MaxNodesClamp(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/ai/neighborhood/Pod/prod/cart?max_nodes=99999", nil)
	opts := parseNeighborhoodOptions(r)
	if opts.MaxNodes != 200 {
		t.Errorf("max_nodes clamp = %d, want 200", opts.MaxNodes)
	}
}

// TestParseNeighborhoodOptions_HopsClamp pins that REST applies the hops=2
// clamp at the handler level too, matching MaxNodes. BFS clamps internally,
// but the handler-level clamp keeps opts.Hops correct if anything inspects
// or logs it before BFS, and matches the doc on parseNeighborhoodOptions.
func TestParseNeighborhoodOptions_HopsClamp(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/ai/neighborhood/Pod/prod/cart?hops=99", nil)
	opts := parseNeighborhoodOptions(r)
	if opts.Hops != 2 {
		t.Errorf("hops clamp = %d, want 2", opts.Hops)
	}
}

func TestParseNeighborhoodOptions_InvalidValues(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/ai/neighborhood/Pod/prod/cart?hops=abc&max_nodes=-5", nil)
	opts := parseNeighborhoodOptions(r)
	if opts.Hops != 1 {
		t.Errorf("invalid hops should fall back to default 1, got %d", opts.Hops)
	}
	if opts.MaxNodes != 25 {
		t.Errorf("invalid max_nodes should fall back to default 25, got %d", opts.MaxNodes)
	}
}

// TestParseNeighborhoodOptions_ProfileNormalization pins that REST exposes
// only auto/all. Unknown semantic bucket names fall back to ProfileAuto
// rather than silently expanding to all edge types.
func TestParseNeighborhoodOptions_ProfileNormalization(t *testing.T) {
	cases := []struct {
		query string
		want  topology.Profile
	}{
		{"profile=all", topology.ProfileAll},
		{"profile=All", topology.ProfileAll},             // case-insensitive
		{"profile=%20%20all%20%20", topology.ProfileAll}, // whitespace trim
		{"profile=management", topology.ProfileAuto},     // unsupported bucket → auto
		{"profile=networking", topology.ProfileAuto},     // unsupported bucket → auto
		{"profile=policy", topology.ProfileAuto},         // unsupported bucket → auto
		{"profile=security", topology.ProfileAuto},       // unsupported bucket → auto
		{"profile=garbage", topology.ProfileAuto},        // unknown → auto
		{"profile=", topology.ProfileAuto},               // empty → auto
		{"", topology.ProfileAuto},                       // missing → auto
	}
	for _, c := range cases {
		t.Run(c.query, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/ai/neighborhood/Pod/prod/cart?"+c.query, nil)
			opts := parseNeighborhoodOptions(r)
			if opts.Profile != c.want {
				t.Errorf("profile = %q, want %q", opts.Profile, c.want)
			}
		})
	}
}

func TestNeighborhoodMissingRootDoesNotEstablishAbsenceDuringSync(t *testing.T) {
	old := k8s.GetConnectionStatus()
	t.Cleanup(func() {
		k8s.SetConnectionStatus(old)
		k8s.ResetResourceCache()
		if err := k8s.InitTestResourceCache(testFakeClient); err != nil {
			t.Fatal(err)
		}
	})
	k8s.ResetResourceCache()
	if err := k8s.InitTestPromotedSyncingCache(fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}}), 5*time.Second, 300*time.Millisecond, map[string]time.Duration{"replicasets": time.Second}); err != nil {
		t.Fatal(err)
	}
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	env := newAuthTestServer(t)
	env.srv.permCache.Set("bob", nil, &auth.UserPermissions{AllowedNamespaces: []string{"default"}})
	probeKind := func(kind string) (int, string) {
		resp := env.authGet(t, "/api/ai/neighborhood/"+kind+"/default/missing", "bob", "")
		defer resp.Body.Close()
		var body struct {
			ErrorCode string `json:"error_code"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, body.ErrorCode
	}
	probe := func() (int, string) { return probeKind("ReplicaSet") }
	for _, kind := range []string{"replicaset", "ReplicaSet", "REPLICASET"} {
		if status, code := probeKind(kind); status != http.StatusServiceUnavailable || code != "kind_sync_pending" {
			t.Fatalf("pending %s: %d %s", kind, status, code)
		}
	}
	if status, code := probe(); status != http.StatusServiceUnavailable || code != "kind_sync_pending" {
		t.Fatalf("pending inventory: %d %s", status, code)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		status, code := probe()
		if status == http.StatusServiceUnavailable && code == "kind_sync_failed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("failed inventory: %d %s", status, code)
		}
		time.Sleep(20 * time.Millisecond)
	}
	for {
		status, code := probe()
		if status == http.StatusNotFound {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("ready absent root: %d %s", status, code)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
