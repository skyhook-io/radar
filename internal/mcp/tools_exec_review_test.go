package mcp

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/k8score"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	clienttesting "k8s.io/client-go/testing"
)

func TestExecPodWorkloadListsOnlyCallerVisiblePods(t *testing.T) {
	defer k8s.ResetTestState()
	controller := true
	sts := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "shop", UID: "sts"}, Spec: appsv1.StatefulSetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "db"}}}, Status: appsv1.StatefulSetStatus{UpdateRevision: "current"}}
	visible, hidden := runningPod("db-visible", true, "app"), runningPod("db-secret", false, "app")
	for _, pod := range []*corev1.Pod{visible, hidden} {
		pod.Labels = map[string]string{"app": "db", appsv1.ControllerRevisionHashLabelKey: "current"}
		pod.OwnerReferences = []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: "db", UID: "sts", Controller: &controller}}
	}
	hidden.Status.Phase = corev1.PodPending
	if err := k8s.InitTestResourceCache(fake.NewClientset(sts, visible, hidden)); err != nil {
		t.Fatal(err)
	}
	ctx := withTestUserPerms(t, "exec-pod-list-reader", nil, []string{"shop"})
	for _, denied := range []bool{true, false} {
		t.Run(map[bool]string{true: "forbidden", false: "visible-only"}[denied], func(t *testing.T) {
			client := fake.NewClientset(sts, visible)
			lists := 0
			client.PrependReactor("list", "pods", func(action clienttesting.Action) (bool, runtime.Object, error) {
				lists++
				if action.GetNamespace() != "shop" || action.(clienttesting.ListAction).GetListRestrictions().Labels.String() != "app=db" {
					t.Errorf("pod list not scoped to workload: %+v", action)
				}
				if denied {
					return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", errors.New("cannot list pods"))
				}
				return true, &corev1.PodList{Items: []corev1.Pod{*visible}}, nil
			})
			var calls []execCall
			result, _, err := execPod(ctx, client, nil, fakeExec(&calls, "", "", nil), execPodInput{Namespace: "shop", Name: "db", Kind: "statefulset", Command: []string{"true"}})
			if lists != 1 {
				t.Fatalf("pod lists=%d", lists)
			}
			if denied {
				if err == nil || !strings.Contains(err.Error(), "rbac_denied") || strings.Contains(err.Error(), visible.Name) || strings.Contains(err.Error(), hidden.Name) || len(calls) != 0 {
					t.Fatalf("result=%v calls=%v err=%v", result, calls, err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				got := decodeToolResult(t, result)
				if got["podSelection"] != "db-visible of 1 pods of statefulsets db" {
					t.Fatalf("selection=%v", got["podSelection"])
				}
			}
		})
	}
}

func TestExecPodTargetsInitAndEphemeralContainers(t *testing.T) {
	always := corev1.ContainerRestartPolicyAlways
	pod := runningPod("web-1", true, "app")
	pod.Spec.InitContainers = []corev1.Container{{Name: "istio-proxy", RestartPolicy: &always}, {Name: "setup"}}
	pod.Spec.EphemeralContainers = []corev1.EphemeralContainer{{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debugger"}}}
	for _, tc := range []struct{ name, explicit, annotation, want string }{
		{"native mesh sidecar", "istio-proxy", "", "istio-proxy"},
		{"ordinary init", "setup", "", "setup"},
		{"ephemeral", "debugger", "", "debugger"},
		{"annotated native sidecar", "", "istio-proxy", "istio-proxy"},
		{"annotated ordinary init", "", "setup", "setup"},
		{"annotated ephemeral", "", "debugger", "debugger"},
		{"fallback app only", "", "", "app"},
		{"invalid annotation falls back", "", "missing", "app"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := pod.DeepCopy()
			p.Annotations = map[string]string{k8score.DefaultContainerAnnotation: tc.annotation}
			var calls []execCall
			_, _, err := execPod(t.Context(), fake.NewClientset(p), nil, fakeExec(&calls, "", "", nil), execPodInput{Namespace: "shop", Name: p.Name, Container: tc.explicit, Command: []string{"true"}})
			if err != nil || len(calls) != 1 || calls[0].container != tc.want {
				t.Fatalf("calls=%v err=%v", calls, err)
			}
		})
	}
	var calls []execCall
	_, _, err := execPod(t.Context(), fake.NewClientset(pod), nil, fakeExec(&calls, "", "", nil), execPodInput{Namespace: "shop", Name: pod.Name, Container: "missing", Command: []string{"true"}})
	if err == nil || !strings.Contains(err.Error(), "containers: app, istio-proxy, setup, debugger") || len(calls) != 0 {
		t.Fatalf("calls=%v err=%v", calls, err)
	}
}

func TestExecPodTargetsRunningInitContainerInPendingPod(t *testing.T) {
	pod := runningPod("web-1", false, "app")
	pod.Spec.InitContainers = []corev1.Container{{Name: "wait-for-db"}}
	pod.Status.Phase = corev1.PodPending
	pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{
		Name:  "wait-for-db",
		State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	}}
	var calls []execCall
	_, _, err := execPod(t.Context(), fake.NewClientset(pod), nil, fakeExec(&calls, "", "", nil), execPodInput{
		Namespace: "shop", Name: pod.Name, Container: "wait-for-db", Command: []string{"true"},
	})
	if err != nil || len(calls) != 1 || calls[0].pod != pod.Name || calls[0].container != "wait-for-db" {
		t.Fatalf("calls=%v err=%v", calls, err)
	}
}

func TestExecPodRealSPDYForbiddenResponse(t *testing.T) {
	for _, code := range []int{http.StatusForbidden, http.StatusInternalServerError} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			t.Cleanup(k8s.SetTestConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected}))
			var websocket, spdy atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/api/v1/namespaces/shop/pods/web-1" {
					_ = json.NewEncoder(w).Encode(runningPod("web-1", true, "app"))
					return
				}
				if r.URL.Path != "/api/v1/namespaces/shop/pods/web-1/exec" {
					t.Errorf("unexpected path %s", r.URL.Path)
				}
				if r.Method == http.MethodGet {
					websocket.Add(1)
				} else if r.Method == http.MethodPost {
					spdy.Add(1)
				}
				responseCode := code
				if r.Method == http.MethodGet {
					responseCode = http.StatusForbidden
				}
				w.WriteHeader(responseCode)
				_ = json.NewEncoder(w).Encode(metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: metav1.StatusFailure, Code: int32(responseCode), Message: "exec refused (403 text does not establish status)"})
			}))
			defer server.Close()
			previous := k8s.SetTestConfig(&rest.Config{Host: server.URL})
			t.Cleanup(func() { k8s.SetTestConfig(previous) })
			_, _, err := handleExecPod(t.Context(), nil, execPodInput{Namespace: "shop", Name: "web-1", Command: []string{"true"}})
			if err == nil {
				t.Fatal("exec succeeded")
			}
			denied := strings.Contains(err.Error(), "rbac_denied") && strings.Contains(err.Error(), "get_subject_permissions")
			if denied != (code == http.StatusForbidden) || websocket.Load() != 1 || spdy.Load() != 1 {
				t.Fatalf("websocket=%d spdy=%d err=%v", websocket.Load(), spdy.Load(), err)
			}
		})
	}
}

func TestExecPodClientAndExecutorShareConfigSnapshot(t *testing.T) {
	t.Cleanup(k8s.SetTestConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected}))
	var podReads, execRequests atomic.Int32
	replacement := &rest.Config{Host: "https://cluster-b.invalid", BearerToken: "cluster-b-token", WrapTransport: func(http.RoundTripper) http.RoundTripper {
		return execRoundTripper(func(r *http.Request) (*http.Response, error) {
			t.Errorf("used replacement config: %s", r.URL)
			return nil, errors.New("wrong cluster")
		})
	}}
	cfg := &rest.Config{Host: "https://cluster-a.invalid", BearerToken: "cluster-a-token", WrapTransport: func(http.RoundTripper) http.RoundTripper {
		k8s.SetTestConfig(replacement)
		return execRoundTripper(func(r *http.Request) (*http.Response, error) {
			if r.URL.Host != "cluster-a.invalid" || r.Header.Get("Authorization") != "Bearer cluster-a-token" || r.Header.Get("Impersonate-User") != "exec-snapshot-reader" {
				t.Errorf("mixed connection: url=%s headers=%v", r.URL, r.Header)
			}
			if strings.HasSuffix(r.URL.Path, "/exec") {
				execRequests.Add(1)
				return nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods/exec"}, "web-1", errors.New("denied"))
			}
			podReads.Add(1)
			recorder := httptest.NewRecorder()
			recorder.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(recorder).Encode(runningPod("web-1", true, "app"))
			return recorder.Result(), nil
		})
	}}
	previous := k8s.SetTestConfig(cfg)
	t.Cleanup(func() { k8s.SetTestConfig(previous) })
	ctx := pkgauth.ContextWithUser(t.Context(), &pkgauth.User{Username: "exec-snapshot-reader"})
	_, _, err := handleExecPod(ctx, nil, execPodInput{Namespace: "shop", Name: "web-1", Command: []string{"true"}})
	if err == nil || !strings.Contains(err.Error(), "rbac_denied") || podReads.Load() != 1 || execRequests.Load() == 0 {
		t.Fatalf("podReads=%d execRequests=%d err=%v", podReads.Load(), execRequests.Load(), err)
	}
}
