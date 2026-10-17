package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestVitalsCollectorScopeMarksPartialWithoutHidingCapacity(t *testing.T) {
	client := fake.NewClientset(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}, Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: mustQty(t, "4"), corev1.ResourceMemory: mustQty(t, "8Gi")}}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod-1", Namespace: "alpha"}, Spec: corev1.PodSpec{NodeName: "node-1", Containers: []corev1.Container{{Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: mustQty(t, "500m")}}}}}},
	)
	t.Cleanup(func() {
		k8s.ResetResourceCache()
		if err := k8s.InitTestResourceCache(testFakeClient); err != nil {
			t.Fatal(err)
		}
	})
	k8s.ResetResourceCache()
	if err := k8s.InitScopedTestResourceCache(client, map[string]k8score.ResourceScope{"nodes": {Enabled: true}, "pods": {Enabled: true, Namespace: "alpha"}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, query string
		complete    bool
	}{
		{"unscoped", "", false},
		{"covered", "?namespaces=alpha", true},
		{"mixed", "?namespaces=alpha,beta", false},
		{"excluded", "?namespaces=beta", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			(&Server{}).handleVitals(w, httptest.NewRequest(http.MethodGet, "/api/vitals"+tc.query, nil))
			var vitals VitalsResponse
			if w.Code != 200 {
				t.Fatalf("status=%d: %s", w.Code, w.Body.String())
			}
			if err := json.Unmarshal(w.Body.Bytes(), &vitals); err != nil {
				t.Fatal(err)
			}
			if vitals.CPU == nil || vitals.CPU.CapacityMillis != 4000 || vitals.Completeness.Complete != tc.complete {
				t.Fatalf("capacity or completeness changed: %+v", vitals)
			}
			if !tc.complete && !slices.Contains(vitals.Completeness.Restricted, "Pod") {
				t.Fatalf("uncovered Pods must be explicit: %+v", vitals.Completeness)
			}
		})
	}
}

// Which namespaces Radar collects is known while Pods still load, so a
// namespace-scoped cache marks Pods restricted before they finish loading.
func TestVitalsCollectorScopeMarksPodsRestrictedWhileLoading(t *testing.T) {
	client := fake.NewClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-1"}, Status: corev1.NodeStatus{Allocatable: corev1.ResourceList{corev1.ResourceCPU: mustQty(t, "4"), corev1.ResourceMemory: mustQty(t, "8Gi")}}})
	t.Cleanup(func() {
		k8s.ResetResourceCache()
		if err := k8s.InitTestResourceCache(testFakeClient); err != nil {
			t.Fatal(err)
		}
	})
	k8s.ResetResourceCache()
	if err := k8s.InitTestResourceCacheFromConfig(k8score.CacheConfig{
		Client:              client,
		ResourceTypes:       map[string]bool{"nodes": true, "pods": true},
		ResourceScopes:      map[string]k8score.ResourceScope{"nodes": {Enabled: true}, "pods": {Enabled: true, Namespace: "alpha"}},
		DeferredTypes:       map[string]bool{"pods": true},
		DebugSyncDelays:     map[string]time.Duration{"pods": time.Hour},
		DeferredSyncTimeout: time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	cache := k8s.GetResourceCache()
	waitFor(t, 5*time.Second, func() bool { return cache.AssessKindRead("nodes", nil).Readiness == k8s.KindReady })
	if cache.AssessKindRead("pods", nil).Readiness != k8s.KindPending {
		t.Fatal("the fixture must leave Pods loading")
	}
	w := httptest.NewRecorder()
	(&Server{}).handleVitals(w, httptest.NewRequest(http.MethodGet, "/api/vitals", nil))
	var vitals VitalsResponse
	if w.Code != 200 {
		t.Fatalf("status=%d: %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &vitals); err != nil {
		t.Fatal(err)
	}
	if vitals.Completeness.Complete || !slices.Contains(vitals.Completeness.Restricted, "Pod") {
		t.Fatalf("Pods outside the collector's namespaces must stay explicit while loading: %+v", vitals.Completeness)
	}
}
