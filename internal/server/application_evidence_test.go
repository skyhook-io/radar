package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	collector "github.com/skyhook-io/radar/internal/runtimeevidence"
	"github.com/skyhook-io/radar/internal/trace"
	evidence "github.com/skyhook-io/radar/pkg/runtimeevidence"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

const evidenceRequestBody = `{"application":"vault","namespace":"default","pod":"vault-0","uid":"pod-uid","context":"test-context","confirmNetworkAccess":true}`

func evidenceRequest(method, url, body string) *http.Request {
	r := httptest.NewRequest(method, url, strings.NewReader(body))
	r.RemoteAddr = "127.0.0.1:12345"
	return r
}

func TestApplicationEvidenceDeploymentGate(t *testing.T) {
	t.Cleanup(k8s.SetTestLocalMode())
	for _, mode := range []string{"remote", "auth", "tunnel", "user", "cloud", "in-cluster"} {
		t.Run(mode, func(t *testing.T) {
			s := &Server{}
			r := evidenceRequest(http.MethodPost, "/api/application-evidence/collect", evidenceRequestBody)
			switch mode {
			case "cloud":
				t.Setenv("RADAR_CLOUD_MODE", "true")
			case "in-cluster":
				old := k8s.ForceInCluster
				k8s.ForceInCluster = true
				t.Cleanup(func() { k8s.ForceInCluster = old })
			case "remote":
				r.RemoteAddr = "192.0.2.1:1234"
			case "auth":
				s.authConfig.Mode = "proxy"
			case "tunnel":
				s.cloudConnectCfg.CloudTunnelConfigured = true
			case "user":
				r = r.WithContext(auth.ContextWithUser(r.Context(), &auth.User{Username: "alice"}))
			}
			w := httptest.NewRecorder()
			s.handleCollectApplicationEvidence(w, r)
			if w.Code != http.StatusNotFound {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
			w = httptest.NewRecorder()
			s.handleApplicationEvidenceCandidates(w, r)
			if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"enabled":false`) {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
		})
	}
}

func TestApplicationEvidenceRejectsUnapprovedRequests(t *testing.T) {
	t.Cleanup(k8s.SetTestLocalMode())
	for _, tc := range []struct {
		name, body, origin string
		status             int
	}{
		{"cross-origin", evidenceRequestBody, "https://evil.example", 403},
		{"no confirmation", strings.Replace(evidenceRequestBody, "true", "false", 1), "", 400},
		{"no UID", strings.Replace(evidenceRequestBody, "pod-uid", "", 1), "", 400},
		{"wrong application", strings.Replace(evidenceRequestBody, "vault", "arbitrary", 1), "", 400},
		{"extra JSON", evidenceRequestBody + `{}`, "", 400},
		{"custom endpoint", strings.TrimSuffix(evidenceRequestBody, "}") + `,"url":"http://example.com"}`, "", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &Server{}
			r := evidenceRequest(http.MethodPost, "/api/application-evidence/collect", tc.body)
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			w := httptest.NewRecorder()
			s.handleCollectApplicationEvidence(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
		})
	}
}

func TestApplicationEvidenceCollectContextAndIdentity(t *testing.T) {
	t.Cleanup(k8s.SetTestLocalMode())
	for _, scenario := range []string{"collect", "stale before", "changed during", "switched away and back", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			operation, cancelOperation := context.WithCancel(context.Background())
			defer cancelOperation()
			active := "test-context"
			calls := 0
			api := &applicationEvidenceAPI{
				operationContext: func() context.Context { return operation },
				snapshot:         func() (*rest.Config, string) { return &rest.Config{Host: "https://unused.invalid"}, "test-context" },
				currentContext:   func() string { return active },
				collect: func(ctx context.Context, _ kubernetes.Interface, _ *rest.Config, a evidence.Adapter, target collector.Target) collector.Result {
					calls++
					if scenario == "switched away and back" {
						cancelOperation()
						select {
						case <-ctx.Done():
						case <-time.After(time.Second):
							t.Fatal("context switch did not cancel collection")
						}
					}
					if a != evidence.Vault || target.UID != "pod-uid" || target.Namespace != "default" || target.Pod != "vault-0" {
						t.Fatalf("wrong selected target: %+v", target)
					}
					if scenario == "cancelled" && ctx.Err() == nil {
						t.Fatal("cancellation not propagated")
					}
					if scenario == "changed during" {
						active = "different"
					}
					return collector.Result{Target: target, Outcome: "unavailable", Reason: "test"}
				},
			}
			if scenario == "stale before" {
				active = "different"
			}
			r := evidenceRequest(http.MethodPost, "/api/application-evidence/collect", evidenceRequestBody)
			if scenario == "cancelled" {
				ctx, cancel := context.WithCancel(r.Context())
				cancel()
				r = r.WithContext(ctx)
			}
			w := httptest.NewRecorder()
			(&Server{applicationEvidence: api}).handleCollectApplicationEvidence(w, r)
			want := http.StatusOK
			if scenario == "stale before" || scenario == "changed during" || scenario == "switched away and back" {
				want = http.StatusConflict
			}
			if w.Code != want {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
			if scenario == "stale before" && calls != 0 {
				t.Fatal("collected after context changed")
			}
			if scenario != "stale before" && calls != 1 {
				t.Fatalf("calls=%d", calls)
			}
			if (scenario == "changed during" || scenario == "switched away and back") && strings.Contains(w.Body.String(), "pod-uid") {
				t.Fatal("stale result escaped")
			}
		})
	}
}

func TestApplicationEvidenceCandidatesDoNotCollect(t *testing.T) {
	t.Cleanup(k8s.SetTestLocalMode())
	permissionServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"authorization.k8s.io/v1","kind":"SelfSubjectAccessReview","status":{"allowed":false}}`))
	}))
	defer permissionServer.Close()
	api := &applicationEvidenceAPI{
		operationContext: context.Background,
		snapshot:         func() (*rest.Config, string) { return &rest.Config{Host: permissionServer.URL}, "test-context" },
		currentContext:   func() string { return "test-context" },
		resolve: func(_ context.Context, _ trace.Deps, subject collector.Subject) collector.CandidateSet {
			return collector.CandidateSet{SubjectUID: "subject-uid", Candidates: []collector.Candidate{{Application: evidence.Vault, Target: collector.Target{Namespace: "default", Pod: "vault-0", UID: "pod-uid"}}}}
		},
		collect: func(context.Context, kubernetes.Interface, *rest.Config, evidence.Adapter, collector.Target) collector.Result {
			t.Fatal("GET triggered collection")
			return collector.Result{}
		},
	}
	w := httptest.NewRecorder()
	(&Server{applicationEvidence: api}).handleApplicationEvidenceCandidates(w, evidenceRequest(http.MethodGet, "/api/application-evidence/candidates?kind=Pod&namespace=default&name=vault-0", ""))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
	var result applicationEvidenceCandidatesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Enabled || len(result.Candidates) != 0 || !result.CoverageLimited {
		t.Fatalf("denied target advertised: %+v", result)
	}
}

func TestApplicationEvidenceChecksCandidatesConcurrently(t *testing.T) {
	var arrived atomic.Int32
	ready := make(chan struct{})
	permissions := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if arrived.Add(1) == 3 {
			close(ready)
		}
		select {
		case <-ready:
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"authorization.k8s.io/v1","kind":"SelfSubjectAccessReview","status":{"allowed":true}}`))
	}))
	defer permissions.Close()
	t.Cleanup(k8s.SetTestLocalMode())
	api := &applicationEvidenceAPI{
		operationContext: context.Background,
		snapshot:         func() (*rest.Config, string) { return &rest.Config{Host: permissions.URL}, "test-context" },
		currentContext:   func() string { return "test-context" },
		resolve: func(context.Context, trace.Deps, collector.Subject) collector.CandidateSet {
			candidates := make([]collector.Candidate, 3)
			for i := range candidates {
				candidates[i] = collector.Candidate{Application: evidence.Vault, Target: collector.Target{Namespace: "default", Pod: fmt.Sprintf("vault-%d", i), UID: fmt.Sprint(i)}}
			}
			return collector.CandidateSet{Candidates: candidates}
		},
	}
	w := httptest.NewRecorder()
	(&Server{applicationEvidence: api}).handleApplicationEvidenceCandidates(w, evidenceRequest(http.MethodGet, "/api/application-evidence/candidates?kind=Pod&namespace=default&name=vault-0", ""))
	var result applicationEvidenceCandidatesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(result.Candidates) != 3 || result.CoverageLimited {
		t.Fatalf("status=%d result=%+v", w.Code, result)
	}
}

func TestApplicationEvidencePermissionTimeoutCanBeRetried(t *testing.T) {
	permissions := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(300 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"authorization.k8s.io/v1","kind":"SelfSubjectAccessReview","status":{"allowed":true}}`))
	}))
	defer permissions.Close()
	t.Cleanup(k8s.SetTestLocalMode())
	api := &applicationEvidenceAPI{
		operationContext: context.Background,
		snapshot:         func() (*rest.Config, string) { return &rest.Config{Host: permissions.URL}, "test-context" },
		currentContext:   func() string { return "test-context" },
		resolve: func(context.Context, trace.Deps, collector.Subject) collector.CandidateSet {
			return collector.CandidateSet{SubjectUID: "uid", Candidates: []collector.Candidate{{Application: evidence.Vault, Target: collector.Target{Namespace: "default", Pod: "vault-0", UID: "uid"}}}}
		},
		collect: func(context.Context, kubernetes.Interface, *rest.Config, evidence.Adapter, collector.Target) collector.Result {
			t.Fatal("permission retry collected application evidence")
			return collector.Result{}
		},
	}
	s := &Server{applicationEvidence: api}
	for _, retry := range []bool{false, true} {
		w := httptest.NewRecorder()
		s.handleApplicationEvidenceCandidates(w, evidenceRequest(http.MethodGet, fmt.Sprintf("/api/application-evidence/candidates?kind=Pod&namespace=default&name=vault-0&retryPermissions=%t", retry), ""))
		var result applicationEvidenceCandidatesResponse
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if w.Code != 200 || result.PermissionCheckTimedOut == retry || (len(result.Candidates) == 1) != retry {
			t.Fatalf("retry=%t status=%d result=%+v", retry, w.Code, result)
		}
	}
}

func TestApplicationEvidenceCandidatesDiscardSwitchedContext(t *testing.T) {
	t.Cleanup(k8s.SetTestLocalMode())
	operation, switchContext := context.WithCancel(context.Background())
	defer switchContext()
	api := &applicationEvidenceAPI{
		operationContext: func() context.Context { return operation },
		snapshot:         func() (*rest.Config, string) { return &rest.Config{Host: "https://unused.invalid"}, "test-context" },
		currentContext:   func() string { return "test-context" },
		resolve: func(context.Context, trace.Deps, collector.Subject) collector.CandidateSet {
			switchContext()
			return collector.CandidateSet{SubjectUID: "old-subject", Candidates: []collector.Candidate{}}
		},
	}
	w := httptest.NewRecorder()
	(&Server{applicationEvidence: api}).handleApplicationEvidenceCandidates(w, evidenceRequest(http.MethodGet, "/api/application-evidence/candidates?kind=Pod&namespace=default&name=vault-0", ""))
	if w.Code != http.StatusConflict || strings.Contains(w.Body.String(), "old-subject") {
		t.Fatalf("stale response: %d %s", w.Code, w.Body)
	}
}

func TestApplicationEvidenceRefusesTransitionBeforeSnapshot(t *testing.T) {
	t.Cleanup(k8s.SetTestLocalMode())
	t.Cleanup(k8s.SetTestContextOperationInProgress(true))
	api := &applicationEvidenceAPI{
		operationContext: context.Background,
		snapshot:         func() (*rest.Config, string) { t.Fatal("read a configuration during transition"); return nil, "" },
	}
	s := &Server{applicationEvidence: api}
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		w := httptest.NewRecorder()
		if method == http.MethodGet {
			s.handleApplicationEvidenceCandidates(w, evidenceRequest(method, "/api/application-evidence/candidates?kind=Pod&namespace=default&name=vault-0", ""))
		} else {
			s.handleCollectApplicationEvidence(w, evidenceRequest(method, "/api/application-evidence/collect", evidenceRequestBody))
		}
		if w.Code != http.StatusConflict {
			t.Fatalf("%s: status=%d body=%s", method, w.Code, w.Body)
		}
	}
}
