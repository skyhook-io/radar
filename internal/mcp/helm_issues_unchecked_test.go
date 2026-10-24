package mcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
)

// An agent asking "what's broken?" must learn when Helm releases weren't
// checked, rather than read their absence as healthy.
func TestIssuesToolSaysWhenHelmReleasesCouldNotBeChecked(t *testing.T) {
	cases := []struct {
		name          string
		status        int
		body          string
		wantUnchecked bool
	}{
		{
			name:          "API server error",
			status:        http.StatusInternalServerError,
			body:          `{"kind":"Status","apiVersion":"v1","status":"Failure","message":"etcdserver: request timed out","code":500}`,
			wantUnchecked: true,
		},
		{
			name:   "caller may not list release Secrets",
			status: http.StatusForbidden,
			body:   `{"kind":"Status","apiVersion":"v1","status":"Failure","message":"secrets is forbidden","reason":"Forbidden","code":403}`,
		},
		{
			// Radar's own credentials failing is not the caller's permissions.
			name:          "API server rejects Radar's credentials",
			status:        http.StatusUnauthorized,
			body:          `{"kind":"Status","apiVersion":"v1","status":"Failure","message":"Unauthorized","reason":"Unauthorized","code":401}`,
			wantUnchecked: true,
		},
		{
			name:   "read succeeded",
			status: http.StatusOK,
			body:   `{"apiVersion":"v1","kind":"SecretList","items":[]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}})
			if err := k8s.InitTestResourceCache(client); err != nil {
				t.Fatalf("InitTestResourceCache: %v", err)
			}
			t.Cleanup(k8s.ResetTestState)

			storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if !strings.HasSuffix(r.URL.Path, "/secrets") || !strings.Contains(r.URL.RawQuery, "owner") {
					http.NotFound(w, r)
					return
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			t.Cleanup(storage.Close)
			helm.ResetClient()
			if err := helm.InitializeWithRESTConfig(&rest.Config{Host: storage.URL}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(helm.ResetClient)

			result, _, err := handleIssuesTool(context.Background(), nil, issuesInput{Namespace: "shop"})
			if err != nil {
				t.Fatalf("handleIssuesTool: %v", err)
			}
			var wire map[string]any
			if err := json.Unmarshal([]byte(extractText(t, result)), &wire); err != nil {
				t.Fatalf("decode: %v", err)
			}
			got, present := wire["unchecked_sources"]
			gotJSON, _ := json.Marshal(got)
			if tc.wantUnchecked && string(gotJSON) != `[{"reason":"read_failed","source":"helm"}]` {
				t.Errorf("unchecked_sources = %s, want the helm read_failed entry", gotJSON)
			}
			if !tc.wantUnchecked && present {
				t.Errorf("unchecked_sources = %s, want it absent", gotJSON)
			}
		})
	}
}

// A caller that already left gets no answer, so its read cut short is not
// a failed check.
func TestNativeHelmIssuesDoNotSayUncheckedWhenCallerLeft(t *testing.T) {
	helm.ResetClient()
	if err := helm.InitializeWithRESTConfig(&rest.Config{Host: "http://127.0.0.1:1"}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(helm.ResetClient)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, unchecked := nativeHelmIssuesForContext(ctx, []string{"shop"}, issues.Filters{}); unchecked {
		t.Error("a caller that left was told Helm releases couldn't be checked")
	}
}

// get_dashboard must not tell an agent "RBAC denied" when the read failed for
// a reason the caller's permissions can't fix.
func TestDashboardHelmSeparatesDeniedFromFailed(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		body       string
		wantDenied bool
	}{
		{
			name:       "caller may not list release Secrets",
			status:     http.StatusForbidden,
			body:       `{"kind":"Status","apiVersion":"v1","status":"Failure","message":"secrets is forbidden: User \"dev\" cannot list resource \"secrets\"","reason":"Forbidden","code":403}`,
			wantDenied: true,
		},
		{
			name:   "API server rejects Radar's credentials",
			status: http.StatusUnauthorized,
			body:   `{"kind":"Status","apiVersion":"v1","status":"Failure","message":"Unauthorized","reason":"Unauthorized","code":401}`,
		},
		{
			name:   "Radar may not impersonate the caller",
			status: http.StatusForbidden,
			body:   `{"kind":"Status","apiVersion":"v1","status":"Failure","message":"users \"dev\" is forbidden: User \"system:serviceaccount:radar:radar\" cannot impersonate resource \"users\" in API group \"\" at the cluster scope","reason":"Forbidden","code":403}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}})
			if err := k8s.InitTestResourceCache(client); err != nil {
				t.Fatalf("InitTestResourceCache: %v", err)
			}
			t.Cleanup(k8s.ResetTestState)
			storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if !strings.HasSuffix(r.URL.Path, "/secrets") {
					http.NotFound(w, r)
					return
				}
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			t.Cleanup(storage.Close)
			helm.ResetClient()
			if err := helm.InitializeWithRESTConfig(&rest.Config{Host: storage.URL}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(helm.ResetClient)

			d := buildDashboard(context.Background(), k8s.GetResourceCache(), "shop", false, false)
			if !d.HelmReleases.Unavailable {
				t.Fatalf("Helm releases reported available after a failed read: %+v", d.HelmReleases)
			}
			if denied := strings.HasPrefix(d.HelmReleases.UnavailableReason, "RBAC denied"); denied != tc.wantDenied {
				t.Fatalf("reason = %q, want RBAC denied = %v", d.HelmReleases.UnavailableReason, tc.wantDenied)
			}
		})
	}
}
