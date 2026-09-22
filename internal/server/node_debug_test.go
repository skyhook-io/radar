package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/k8s"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func nodeDebugTestRouter(t *testing.T, handler http.HandlerFunc) http.Handler {
	t.Helper()
	api := httptest.NewServer(handler)
	t.Cleanup(api.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: api.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	previousClient := k8s.SetTestClient(client)
	previousStatus := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	t.Cleanup(func() { k8s.SetTestClient(previousClient); k8s.SetConnectionStatus(previousStatus) })
	s := &Server{}
	router := chi.NewRouter()
	router.Post("/nodes/{name}/debug", s.handleNodeDebug)
	router.Delete("/nodes/{name}/debug", s.handleNodeDebugCleanup)
	return router
}

func TestNodeDebugResponseUID(t *testing.T) {
	router := nodeDebugTestRouter(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodPost:
			var pod corev1.Pod
			if err := json.NewDecoder(r.Body).Decode(&pod); err != nil {
				t.Error(err)
			}
			pod.UID = "actual-created-uid"
			json.NewEncoder(w).Encode(pod)
		case http.MethodGet:
			json.NewEncoder(w).Encode(corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodRunning}})
		default:
			t.Errorf("unexpected method %s", r.Method)
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	})
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/nodes/node-a/debug", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("response: %d %s", response.Code, response.Body)
	}
	var result NodeDebugResponse
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.UID != "actual-created-uid" || result.Status != "running" {
		t.Fatalf("result: %+v", result)
	}
}

func TestNodeDebugCleanupIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, query    string
		upstream, want int
	}{
		{"exact pod", "namespace=default&podName=pod-a&uid=fdde0bca-d4df-4263-bf04-88ce73cc90c1", 200, 200},
		{"already gone", "namespace=default&podName=pod-a&uid=fdde0bca-d4df-4263-bf04-88ce73cc90c1", 404, 200},
		{"UID conflict", "namespace=default&podName=pod-a&uid=fdde0bca-d4df-4263-bf04-88ce73cc90c1", 409, 409},
		{"forbidden", "namespace=default&podName=pod-a&uid=fdde0bca-d4df-4263-bf04-88ce73cc90c1", 403, 403},
		{"missing all", "", 0, 400},
		{"missing namespace", "podName=pod-a&uid=fdde0bca-d4df-4263-bf04-88ce73cc90c1", 0, 400},
		{"missing name", "namespace=default&uid=fdde0bca-d4df-4263-bf04-88ce73cc90c1", 0, 400},
		{"missing UID", "namespace=default&podName=pod-a", 0, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			router := nodeDebugTestRouter(t, func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.Method != http.MethodDelete || r.URL.Path != "/api/v1/namespaces/default/pods/pod-a" {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL)
				}
				var options metav1.DeleteOptions
				if err := json.NewDecoder(r.Body).Decode(&options); err != nil {
					t.Error(err)
				}
				if options.Preconditions == nil || options.Preconditions.UID == nil || *options.Preconditions.UID != "fdde0bca-d4df-4263-bf04-88ce73cc90c1" {
					t.Errorf("missing precondition: %+v", options)
				}
				reason := map[int]metav1.StatusReason{404: metav1.StatusReasonNotFound, 409: metav1.StatusReasonConflict, 403: metav1.StatusReasonForbidden}[tc.upstream]
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.upstream)
				json.NewEncoder(w).Encode(metav1.Status{Reason: reason, Code: int32(tc.upstream)})
			})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/nodes/node-a/debug?"+tc.query, nil))
			if response.Code != tc.want {
				t.Fatalf("response: %d %s", response.Code, response.Body)
			}
			wantRequests := 1
			if tc.upstream == 0 {
				wantRequests = 0
			}
			if requests != wantRequests {
				t.Fatalf("requests: got %d, want %d", requests, wantRequests)
			}
		})
	}
}

func TestNodeDebugCleanupRejectsMalformedIdentityBeforeClientAccess(t *testing.T) {
	const uid = "fdde0bca-d4df-4263-bf04-88ce73cc90c1"
	for _, tc := range []struct{ name, namespace, podName, uid string }{
		{"namespace whitespace", " default", "pod", uid},
		{"namespace uppercase", "Default", "pod", uid},
		{"namespace slash", "team/a", "pod", uid},
		{"namespace too long", strings.Repeat("a", 64), "pod", uid},
		{"pod whitespace", "default", "pod ", uid},
		{"pod slash", "default", "a/b", uid},
		{"pod uppercase", "default", "Pod", uid},
		{"pod too long", "default", strings.Repeat("a", 254), uid},
		{"UID whitespace", "default", "pod", " " + uid},
		{"UID malformed", "default", "pod", "not-a-uuid"},
		{"UID URN", "default", "pod", "urn:uuid:" + uid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			router := nodeDebugTestRouter(t, func(w http.ResponseWriter, r *http.Request) { requests++; w.WriteHeader(http.StatusOK) })
			query := url.Values{"namespace": {tc.namespace}, "podName": {tc.podName}, "uid": {tc.uid}}.Encode()
			for _, noClient := range []bool{false, true} {
				if noClient {
					k8s.SetTestClient(nil)
				}
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest(http.MethodDelete, "/nodes/node-a/debug?"+query, nil))
				if response.Code != http.StatusBadRequest {
					t.Fatalf("noClient=%v: got %d %s", noClient, response.Code, response.Body)
				}
			}
			if requests != 0 {
				t.Fatalf("invalid identity issued %d Kubernetes requests", requests)
			}
		})
	}
}
