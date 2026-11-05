package mcp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync/atomic"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/servicehttp"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"strings"
	"testing"
)

func TestCurlServiceThroughMCPChecksPermissionAndReturnsApplicationResponse(t *testing.T) {
	enableExecToolsForTest(t)
	priorMode := k8s.ForceInCluster
	k8s.ForceInCluster = true
	t.Cleanup(func() { k8s.ForceInCluster = priorMode })
	var hits atomic.Int32
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Authorization") != "" || r.Header.Get("Impersonate-User") != "" {
			t.Error("Kubernetes credentials reached the application")
		}
		if !strings.HasPrefix(r.Host, "app.team-a.svc.cluster.local:") || r.URL.RequestURI() != "/health?full=true" {
			t.Errorf("wrong target: %s %s", r.Host, r.URL.RequestURI())
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, "queue unavailable")
	}))
	defer app.Close()
	endpoint, _ := url.Parse(app.URL)
	port, _ := strconv.Atoi(endpoint.Port())
	var allowed atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var review authv1.SelfSubjectAccessReview
		if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
			t.Error(err)
		}
		a := review.Spec.ResourceAttributes
		if a == nil || a.Namespace != "team-a" || a.Name != "app" || a.Resource != "services" || a.Subresource != "proxy" || a.Verb != "get" {
			t.Errorf("incorrect authorization: %+v", a)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(authv1.SelfSubjectAccessReview{TypeMeta: metav1.TypeMeta{Kind: "SelfSubjectAccessReview", APIVersion: "authorization.k8s.io/v1"}, Status: authv1.SubjectAccessReviewStatus{Allowed: allowed.Load()}})
	}))
	defer api.Close()
	client, err := kubernetes.NewForConfig(&rest.Config{Host: api.URL, BearerToken: "fixture-kubernetes-token", ContentConfig: rest.ContentConfig{ContentType: "application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	previous := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(previous) })
	cacheClient := fake.NewClientset(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "team-a"}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeClusterIP, ClusterIP: endpoint.Hostname(), Selector: map[string]string{"app": "fixture"}, Ports: []corev1.ServicePort{{Port: int32(port)}}}})
	if err := k8s.InitTestResourceCache(cacheClient); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetResourceCache)
	session := connectTestServer(t)
	args := map[string]any{"namespace": "team-a", "name": "app", "port": endpoint.Port(), "path": "/health?full=true"}
	for _, permitted := range []bool{false, true} {
		allowed.Store(permitted)
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: "curl_service", Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		if !permitted {
			if !result.IsError || hits.Load() != 0 {
				t.Fatalf("denied call reached Service: %+v hits=%d", result, hits.Load())
			}
			if !strings.Contains(result.Content[0].(*mcp.TextContent).Text, "Radar's ServiceAccount") {
				t.Fatalf("no-auth refusal names the wrong principal: %+v", result)
			}
			continue
		}
		var body servicehttp.Response
		if err := json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &body); err != nil {
			t.Fatal(err)
		}
		if result.IsError || body.Status != 503 || body.Body != "queue unavailable" || hits.Load() != 1 {
			t.Fatalf("application failure lost through MCP: %+v hits=%d", body, hits.Load())
		}
	}
}

func TestCurlServiceResultBoundsHeadersAndBody(t *testing.T) {
	result, _, err := curlServiceResult(servicehttp.Response{Status: 503, Headers: map[string]string{"X-Large": strings.Repeat("x", 2<<20), "Content-Type": "text/plain"}, Body: strings.Repeat("z", 512<<10), BodyBytes: 512 << 10})
	if err != nil {
		t.Fatal(err)
	}
	encoded := []byte(result.Content[0].(*mcp.TextContent).Text)
	var got servicehttp.Response
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if got.Status != 503 || !got.Truncated || !got.HeadersTruncated || got.Headers["X-Large"] != "" || got.Headers["Content-Type"] != "text/plain" || len(got.Body) > execOutputCap || got.BodyBytes != 512<<10 {
		t.Fatalf("bad bounded response: %d bytes, truncated=%v", len(encoded), got.Truncated)
	}
}
func TestCurlServiceResultMarksInvalidUTF8(t *testing.T) {
	result, _, err := curlServiceResult(servicehttp.Response{Body: string([]byte{0xff, 'x'}), BodyBytes: 2})
	if err != nil {
		t.Fatal(err)
	}
	encoded := []byte(result.Content[0].(*mcp.TextContent).Text)
	var got servicehttp.Response
	json.Unmarshal(encoded, &got)
	if !got.BodyEncodingLoss || got.BodyBytes != 2 {
		t.Fatalf("lost binary metadata: %s", encoded)
	}
}

func TestCurlServiceHeaderTruncationDoesNotMarkCompleteBody(t *testing.T) {
	result, _, err := curlServiceResult(servicehttp.Response{Body: "complete", Headers: map[string]string{"X-Large": strings.Repeat("x", 32<<10)}})
	if err != nil {
		t.Fatal(err)
	}
	var got servicehttp.Response
	json.Unmarshal([]byte(result.Content[0].(*mcp.TextContent).Text), &got)
	if !got.HeadersTruncated || got.Truncated || got.Body != "complete" {
		t.Fatalf("ambiguous truncation: %+v", got)
	}
}
