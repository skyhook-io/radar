package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/k8s"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// useHelmStorageAnswering points Radar's Helm client at an API server whose
// Helm release Secret lists all answer with status and body.
func useHelmStorageAnswering(t *testing.T, status int, body string) {
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
			w.WriteHeader(status)
			_, _ = io.WriteString(w, body)
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
	helm.ResetClient()
	if err := helm.InitializeWithRESTConfig(config); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		helm.ResetClient()
		k8s.SetTestClient(previousClient)
	})
}

func issuesWire(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", recorder.Code, recorder.Body.String())
	}
	var wire map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &wire); err != nil {
		t.Fatalf("decode: %v; body: %s", err, recorder.Body.String())
	}
	return wire
}

// helmUncheckedWire is the unchecked_sources entry for a failed Helm read.
const helmUncheckedWire = `[{"reason":"read_failed","source":"helm"}]`

// uncheckedSourcesWire renders the response's unchecked_sources, or "" when
// the field is absent.
func uncheckedSourcesWire(t *testing.T, wire map[string]any) string {
	t.Helper()
	got, ok := wire["unchecked_sources"]
	if !ok {
		return ""
	}
	out, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// When the Helm release read fails, the response must say Helm wasn't
// checked. Otherwise a cluster whose only problem is a failed Helm release
// reads as having no issues.
func TestIssuesSayWhenHelmReleasesCouldNotBeChecked(t *testing.T) {
	useHelmStorageAnswering(t, http.StatusInternalServerError,
		`{"kind":"Status","apiVersion":"v1","status":"Failure","message":"etcdserver: request timed out","code":500}`)

	recorder := httptest.NewRecorder()
	testServerSrv.handleIssues(recorder, httptest.NewRequest(http.MethodGet, "/api/issues?namespace=shop", nil))

	if got := uncheckedSourcesWire(t, issuesWire(t, recorder)); got != helmUncheckedWire {
		t.Errorf("unchecked_sources = %s, want %s", got, helmUncheckedWire)
	}
}

// Radar's own request deadline cutting the read short is a failed check:
// the caller is still waiting for the answer.
func TestIssuesSayHelmUncheckedWhenRadarDeadlineEndsTheRead(t *testing.T) {
	storage := useStalledHelmStorage(t)
	safety := time.AfterFunc(3*time.Second, func() { close(storage.release) })
	defer safety.Stop()

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	recorder := httptest.NewRecorder()
	testServerSrv.handleIssues(recorder, httptest.NewRequest(http.MethodGet, "/api/issues?namespace=shop", nil).WithContext(ctx))

	if got := uncheckedSourcesWire(t, issuesWire(t, recorder)); got != helmUncheckedWire {
		t.Errorf("unchecked_sources = %s, want %s", got, helmUncheckedWire)
	}
}

func TestIssuesDoNotSayHelmUncheckedWhenItWasNot(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		query  string
	}{
		{
			name:   "read succeeded",
			status: http.StatusOK,
			body:   `{"apiVersion":"v1","kind":"SecretList","items":[]}`,
			query:  "namespace=shop",
		},
		{
			// A caller who may not list release Secrets sees no Helm issues,
			// as with any other kind it can't list.
			name:   "caller may not list release Secrets",
			status: http.StatusForbidden,
			body:   `{"kind":"Status","apiVersion":"v1","status":"Failure","message":"secrets is forbidden: User \"dev\" cannot list resource \"secrets\" in API group \"\" in the namespace \"shop\"","reason":"Forbidden","code":403}`,
			query:  "namespace=shop",
		},
		{
			name:   "Helm releases not asked for",
			status: http.StatusInternalServerError,
			body:   `{"kind":"Status","apiVersion":"v1","status":"Failure","message":"etcdserver: request timed out","code":500}`,
			query:  "namespace=shop&kind=Pod",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useHelmStorageAnswering(t, tc.status, tc.body)
			recorder := httptest.NewRecorder()
			testServerSrv.handleIssues(recorder, httptest.NewRequest(http.MethodGet, "/api/issues?"+tc.query, nil))
			if got := uncheckedSourcesWire(t, issuesWire(t, recorder)); got != "" {
				t.Errorf("unchecked_sources = %s, want it absent", got)
			}
		})
	}
}

// A caller that left gets no answer, so its read cut short is not a failure
// to report.
func TestIssuesDoNotSayHelmUncheckedWhenCallerLeft(t *testing.T) {
	storage := useStalledHelmStorage(t)
	safety := time.AfterFunc(3*time.Second, func() { close(storage.release) })
	defer safety.Stop()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-storage.listStarted
		cancel()
	}()
	recorder := httptest.NewRecorder()
	testServerSrv.handleIssues(recorder, httptest.NewRequest(http.MethodGet, "/api/issues?namespace=shop", nil).WithContext(ctx))

	if got := uncheckedSourcesWire(t, issuesWire(t, recorder)); got != "" {
		t.Errorf("unchecked_sources = %s, want it absent", got)
	}
}

// A 401 is Radar's own credentials failing, not the caller's permissions.
func TestIssuesSayHelmUncheckedWhenRadarCredentialsAreRejected(t *testing.T) {
	useHelmStorageAnswering(t, http.StatusUnauthorized,
		`{"kind":"Status","apiVersion":"v1","status":"Failure","message":"Unauthorized","reason":"Unauthorized","code":401}`)

	recorder := httptest.NewRecorder()
	testServerSrv.handleIssues(recorder, httptest.NewRequest(http.MethodGet, "/api/issues?namespace=shop", nil))

	if got := uncheckedSourcesWire(t, issuesWire(t, recorder)); got != helmUncheckedWire {
		t.Errorf("unchecked_sources = %s, want %s", got, helmUncheckedWire)
	}
}
