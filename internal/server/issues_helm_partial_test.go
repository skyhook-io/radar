package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/k8s"
)

// useHelmStoragePerNamespace points Radar's Helm client at an API server that
// answers Helm's release Secret lists with an empty list in readable
// namespaces and a 403 everywhere else.
func useHelmStoragePerNamespace(t *testing.T, readable ...string) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/version":
			_, _ = io.WriteString(w, `{"major":"1","minor":"30","gitVersion":"v1.30.0"}`)
		case r.URL.Path == "/api":
			_, _ = io.WriteString(w, `{"kind":"APIVersions","versions":["v1"]}`)
		case r.URL.Path == "/apis":
			_, _ = io.WriteString(w, `{"kind":"APIGroupList","apiVersion":"v1","groups":[]}`)
		case strings.HasSuffix(r.URL.Path, "/secrets") && strings.Contains(r.URL.RawQuery, "owner"):
			for _, ns := range readable {
				if strings.Contains(r.URL.Path, "/namespaces/"+ns+"/") {
					_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"SecretList","items":[]}`)
					return
				}
			}
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	config := &rest.Config{Host: server.URL}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	previousClient := k8s.SetTestClient(client)
	if err := k8s.InitTestDynamicResourceCache(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	discovered := make(chan struct{})
	dc := k8s.GetDynamicResourceCache()
	dc.OnCRDDiscoveryComplete(func() { close(discovered) })
	dc.DiscoverAllCRDs()
	select {
	case <-discovered:
	case <-time.After(5 * time.Second):
		t.Fatal("empty CRD discovery did not complete")
	}
	helm.ResetClient()
	if err := helm.InitializeWithRESTConfig(config); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		helm.ResetClient()
		k8s.SetTestClient(previousClient)
	})
}

// Someone who can read Helm's storage in team-a but not team-b sees team-b's
// releases in the Helm view, built from their objects. Issues must say it
// didn't check team-b instead of reading as nothing wrong, whether the read
// named both namespaces or Radar narrowed "all namespaces" to team-a.
func TestIssuesSayWhichNamespacesHelmWasNotCheckedIn(t *testing.T) {
	useTestResourceCache(t, fake.NewClientset(testNamespaces("team-a", "team-b")...))
	useHelmStoragePerNamespace(t, "team-a")
	env := newAuthTestServer(t)

	perms := &auth.UserPermissions{AllowedNamespaces: nil}
	perms.SetCanI("list", "", "secrets", "", false)
	perms.SetCanI("list", "", "secrets", "team-a", true)
	perms.SetCanI("list", "", "secrets", "team-b", false)
	env.srv.permCache.Set("mixed", nil, perms)

	want := []map[string]any{{"source": "helm", "reason": "caller_access_denied", "namespaces": []any{"team-b"}}}
	for _, path := range []string{"/api/issues", "/api/issues?namespaces=team-a,team-b"} {
		t.Run(path, func(t *testing.T) {
			resp := env.authGet(t, path, "mixed", "")
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d", resp.StatusCode)
			}
			var body struct {
				Unchecked []map[string]any `json:"unchecked_sources"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			var helmEntries []map[string]any
			for _, entry := range body.Unchecked {
				if entry["source"] == "helm" {
					helmEntries = append(helmEntries, entry)
				}
			}
			if !reflect.DeepEqual(helmEntries, want) {
				t.Fatalf("helm unchecked = %v, want %v", helmEntries, want)
			}
		})
	}
}

// Home's Helm card for the same caller: Helm's records from team-a, and team-b
// named as unread rather than left out of the count without a word.
func TestDashboardHelmNamesNamespacesItCouldNotRead(t *testing.T) {
	useTestResourceCache(t, fake.NewClientset(testNamespaces("team-a", "team-b")...))
	useHelmStoragePerNamespace(t, "team-a")
	env := newAuthTestServer(t)

	perms := &auth.UserPermissions{AllowedNamespaces: nil}
	perms.SetCanI("list", "", "secrets", "", false)
	perms.SetCanI("list", "", "secrets", "team-a", true)
	perms.SetCanI("list", "", "secrets", "team-b", false)
	env.srv.permCache.Set("mixed", nil, perms)

	for _, path := range []string{"/api/dashboard/helm", "/api/dashboard/helm?namespaces=team-a,team-b"} {
		t.Run(path, func(t *testing.T) {
			resp := env.authGet(t, path, "mixed", "")
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status %d", resp.StatusCode)
			}
			var summary DashboardHelmSummary
			if err := json.NewDecoder(resp.Body).Decode(&summary); err != nil {
				t.Fatal(err)
			}
			if summary.Restricted || summary.Error != "" {
				t.Fatalf("summary = %+v, want a partial answer", summary)
			}
			if summary.UnreadNamespaces == nil || summary.UnreadNamespaces.Count != 1 || !reflect.DeepEqual(summary.UnreadNamespaces.Names, []string{"team-b"}) {
				t.Fatalf("unread = %+v, want team-b", summary.UnreadNamespaces)
			}
		})
	}
}

// When no namespace's Secrets check is answered, the read falls back to one
// cluster-wide list, which the caller may be refused even though a namespace
// would allow it. Neither Home nor Issues may report that refusal as the
// caller's access.
func TestHelmUnansweredChecksAreNotReportedAsDenials(t *testing.T) {
	useTestResourceCache(t, fake.NewClientset(testNamespaces("team-a", "team-b")...))
	useHelmStoragePerNamespace(t, "team-a")
	env := newAuthTestServer(t)

	// Only the cluster-wide answer is known; the per-namespace checks reach
	// the test API server, which has no SubjectAccessReview route.
	perms := &auth.UserPermissions{AllowedNamespaces: nil}
	perms.SetCanI("list", "", "secrets", "", false)
	env.srv.permCache.Set("unanswered", nil, perms)

	resp := env.authGet(t, "/api/dashboard/helm", "unanswered", "")
	var summary DashboardHelmSummary
	if err := json.NewDecoder(resp.Body).Decode(&summary); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if summary.Restricted || summary.ErrorCode != ErrCodeUnreachable {
		t.Fatalf("summary = %+v, want a retryable error, not restricted", summary)
	}

	resp = env.authGet(t, "/api/issues", "unanswered", "")
	var body struct {
		Unchecked []map[string]any `json:"unchecked_sources"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var helmEntries []map[string]any
	for _, entry := range body.Unchecked {
		if entry["source"] == "helm" {
			helmEntries = append(helmEntries, entry)
		}
	}
	if len(helmEntries) != 1 || helmEntries[0]["reason"] != "read_failed" {
		t.Fatalf("helm unchecked = %v, want one read_failed entry", helmEntries)
	}
}
