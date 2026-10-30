package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

func TestNodeCertificateResourceSurfacesAndPermissions(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 100}); err != nil {
		t.Fatal(err)
	}
	timeline.SetObservationStartForTest(now.Add(-24 * time.Hour))
	var objects []runtime.Object
	for _, tc := range []struct {
		name string
		days int
		age  time.Duration
	}{{"renewal", 99, time.Minute}, {"urgent", 6, time.Minute}, {"stale", -1, 3 * time.Hour}} {
		node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: tc.name, UID: types.UID(tc.name + "-uid"), CreationTimestamp: metav1.NewTime(now.Add(-48 * time.Hour))}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.34.4+rke2r1"}, Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}}}
		event := &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: tc.name + "-warning", Namespace: "default", UID: types.UID(tc.name + "-event")}, Type: "Warning", Reason: "CertificateExpirationWarning", InvolvedObject: corev1.ObjectReference{Kind: "Node", Name: tc.name, UID: types.UID(tc.name)}, LastTimestamp: metav1.NewTime(now.Add(-tc.age)), Message: fmt.Sprintf("certificate CN=client will expire within 120 days at %s", now.Add(time.Duration(tc.days)*24*time.Hour).Format(time.RFC3339))}
		objects = append(objects, node, event)
	}
	useTestResourceCache(t, fake.NewClientset(objects...))
	s := newAuthServer(auth.Config{Mode: "proxy"})
	permissions := &auth.UserPermissions{AllowedNamespaces: []string{"team"}}
	permissions.SetCanI("get", "", "nodes", "", true)
	permissions.SetCanI("list", "", "nodes", "", true)
	// Normalized Node history follows list-nodes, independently of raw Event access.
	permissions.SetCanI("list", "", "events", "default", false)
	s.permCache.Set("pg-user", nil, permissions)
	for _, tc := range []struct {
		name, state string
		issues      int
	}{{"renewal", "reported", 0}, {"urgent", "reported", 1}, {"stale", "unverified", 0}} {
		t.Run(tc.name, func(t *testing.T) {
			w := resourceIssuesRequest(t, s, "/api/issues/resource/Node/_/"+tc.name+"?coverage=1")
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d: %s", w.Code, w.Body.String())
			}
			var response ResourceIssuesResponse
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.NodeCertificateReport == nil || response.NodeCertificateReport.State != tc.state || len(response.Issues) != tc.issues {
				t.Fatalf("wrong report or Issues: %s", w.Body.String())
			}
			if tc.issues > 0 && (response.Issues[0].Reason != k8s.NodeCertificateIssueReason || response.Issues[0].Severity != "critical" || response.Issues[0].Category != "certificate_not_ready") {
				t.Fatalf("standard Issue contract lost: %+v", response.Issues)
			}
		})
	}
	w := resourceIssuesRequest(t, s, "/api/issues/resource/Node/_/urgent")
	var legacy []json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &legacy); err != nil || len(legacy) != 1 {
		t.Fatalf("legacy Issue array changed: %s", w.Body.String())
	}
	ai := chi.NewRouter()
	ai.Get("/api/ai/resources/{kind}/{namespace}/{name}", s.handleAIGetResource)
	aiRequest := func() *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		ai.ServeHTTP(w, requestWithUser(http.MethodGet, "/api/ai/resources/nodes/_/urgent", &auth.User{Username: "pg-user"}))
		return w
	}
	w = aiRequest()
	var aiResponse map[string]json.RawMessage
	if err := json.Unmarshal(w.Body.Bytes(), &aiResponse); err != nil || aiResponse["nodeCertificateReport"] == nil {
		t.Fatalf("AI context lost Node report: status=%d %s", w.Code, w.Body.String())
	}
	permissions.SetCanI("list", "", "nodes", "", false)
	w = resourceIssuesRequest(t, s, "/api/issues/resource/Node/_/urgent?coverage=1")
	var denied ResourceIssuesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &denied); err != nil || denied.NodeCertificateReport != nil || len(denied.Issues) != 0 {
		t.Fatalf("list-nodes denial exposed normalized report: %s", w.Body.String())
	}
	if denied.Withheld == nil || denied.Withheld.Issues == 0 {
		t.Fatalf("denied Issue vanished without the standard withheld count: %s", w.Body.String())
	}
	w = aiRequest()
	aiResponse = nil
	if err := json.Unmarshal(w.Body.Bytes(), &aiResponse); err != nil || aiResponse["nodeCertificateReport"] != nil {
		t.Fatalf("AI report bypassed list-nodes: %s", w.Body.String())
	}
	permissions.SetCanI("get", "", "nodes", "", false)
	w = resourceIssuesRequest(t, s, "/api/issues/resource/Node/_/urgent?coverage=1")
	if w.Code != http.StatusForbidden {
		t.Fatalf("Node get denial bypassed: status=%d %s", w.Code, w.Body.String())
	}
}
