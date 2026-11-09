package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/investigationrefs"
	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/investigation"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	streamhttp "k8s.io/streaming/pkg/httpstream"
)

type execRoundTripper func(*http.Request) (*http.Response, error)

func (f execRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestExecPodUsesCallerIdentityAndCodedForbidden(t *testing.T) {
	t.Cleanup(k8s.SetTestConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected}))
	podReads, execRequests := 0, 0
	cfg := &rest.Config{Host: "https://exec-test.invalid", WrapTransport: func(http.RoundTripper) http.RoundTripper {
		return execRoundTripper(func(r *http.Request) (*http.Response, error) {
			if r.Header.Get("Impersonate-User") != "alice" || !slices.Equal(r.Header.Values("Impersonate-Group"), []string{"operators"}) {
				t.Errorf("lost impersonation on %s: %v", r.URL.Path, r.Header)
			}
			code := http.StatusOK
			var payload any
			switch r.URL.Path {
			case "/api/v1/namespaces/shop/pods/web-1":
				podReads++
				payload = runningPod("web-1", true, "istio-proxy", "app")
			case "/api/v1/namespaces/shop/pods/web-1/exec":
				execRequests++
				q := r.URL.Query()
				if !slices.Equal(q["command"], []string{"printf", "%s", "a b"}) || q.Get("container") != "app" || q.Get("stdin") == "true" || q.Get("tty") == "true" {
					t.Errorf("exec options: %v", q)
				}
				if r.Method == http.MethodGet {
					return nil, &streamhttp.UpgradeFailureError{Cause: errors.New("WebSocket upgrade refused")}
				}
				code = http.StatusForbidden
				payload = apierrors.NewForbidden(schema.GroupResource{Resource: "pods/exec"}, "web-1", errors.New("denied")).ErrStatus
			default:
				t.Fatalf("unexpected request: %s", r.URL)
				return nil, errors.New("unexpected")
			}
			body, err := json.Marshal(payload)
			if err != nil {
				return nil, err
			}
			return &http.Response{StatusCode: code, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(string(body))), Request: r}, nil
		})
	}}
	prev := k8s.SetTestConfig(cfg)
	t.Cleanup(func() { k8s.SetTestConfig(prev) })
	ctx := pkgauth.ContextWithUser(t.Context(), &pkgauth.User{Username: "alice", Groups: []string{"operators"}})
	_, _, err := handleExecPod(ctx, nil, execPodInput{Namespace: "shop", Name: "web-1", Command: []string{"printf", "%s", "a b"}})
	if err == nil || !strings.Contains(err.Error(), "rbac_denied") || !strings.Contains(err.Error(), "get_subject_permissions") {
		t.Fatalf("error=%v", err)
	}
	if podReads != 1 || execRequests == 0 {
		t.Fatalf("podReads=%d execRequests=%d", podReads, execRequests)
	}
}

func TestExecPodDoesNotFallbackWhenImpersonationFails(t *testing.T) {
	t.Cleanup(k8s.SetTestConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected}))
	prev := k8s.SetTestConfig(nil)
	t.Cleanup(func() { k8s.SetTestConfig(prev) })
	ctx := pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: "alice"})
	_, _, err := handleExecPod(ctx, nil, execPodInput{Namespace: "shop", Name: "web-1", Command: []string{"true"}})
	if err == nil {
		t.Fatal("missing impersonation config was accepted")
	}
}

func TestExecPodExcludedFromReadOnlyMounts(t *testing.T) {
	enableExecToolsForTest(t)
	if investigation.IsReadOnlyTool("exec_pod") {
		t.Fatal("exec in read-only allowlist")
	}
	if investigation.IsWriteTool("exec_pod") {
		t.Fatal("exec in built-in AI apply allowlist")
	}
	refs := investigationrefs.NewRegistry()
	scope := strings.Repeat("a", 26)
	lease, err := refs.Begin(scope)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	for _, h := range []http.Handler{NewReadOnlyHandler(), NewInvestigationHandler(refs)} {
		req := httptest.NewRequest(http.MethodPost, "http://localhost/mcp?scope="+scope, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("MCP-Protocol-Version", "2025-06-18")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"get_resource"`) {
			t.Fatalf("list failed: %d %s", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), `"name":"exec_pod"`) {
			t.Fatal("read-only mount exposed exec")
		}
	}
}

func TestExecPodRolloutUsesCurrentHash(t *testing.T) {
	defer k8s.ResetTestState()
	controller := true
	var objects []runtime.Object
	for _, hash := range []string{"stable", "current"} {
		rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: hash, Namespace: "shop", UID: types.UID(hash), OwnerReferences: []metav1.OwnerReference{{APIVersion: "argoproj.io/v1alpha1", Kind: "Rollout", Name: "web", UID: "rollout", Controller: &controller}}}}
		pod := runningPod(hash, true, "app")
		pod.Labels = map[string]string{"rollouts-pod-template-hash": hash}
		pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: hash, UID: types.UID(hash), Controller: &controller}}
		objects = append(objects, rs, pod)
	}
	client := fake.NewClientset(objects...)
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatal(err)
	}
	observed := "2"
	cfg := &rest.Config{Host: "https://exec-test.invalid", WrapTransport: func(http.RoundTripper) http.RoundTripper {
		return execRoundTripper(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/apis/argoproj.io/v1alpha1/namespaces/shop/rollouts/web" || r.Header.Get("Impersonate-User") != "alice" {
				t.Errorf("unexpected request: %s %v", r.URL, r.Header)
			}
			payload := `{"apiVersion":"argoproj.io/v1alpha1","kind":"Rollout","metadata":{"name":"web","namespace":"shop","uid":"rollout","generation":2},"spec":{"selector":{}},"status":{"currentPodHash":"current","stableRS":"stable","observedGeneration":"` + observed + `"}}`
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(payload)), Request: r}, nil
		})
	}}
	previous := k8s.SetTestConfig(cfg)
	defer k8s.SetTestConfig(previous)
	ctx := pkgauth.ContextWithUser(t.Context(), &pkgauth.User{Username: "alice"})
	dyn, err := dynamic.NewForConfig(k8s.ConfigFromContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	pods, err := currentExecPods(ctx, client, dyn, "rollouts", "shop", "web")
	if err != nil || len(pods) != 1 || pods[0].Name != "current" {
		t.Fatalf("pods=%v err=%v", pods, err)
	}
	observed = "1"
	if _, err := currentExecPods(ctx, client, dyn, "rollouts", "shop", "web"); err == nil {
		t.Fatal("stale current hash accepted")
	}
}
