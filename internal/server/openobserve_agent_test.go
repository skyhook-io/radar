package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/cloud"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func TestOpenObserveAgentRoutesAreTunnelOnly(t *testing.T) {
	s := &Server{}
	for _, route := range []struct {
		method, path string
		handler      http.HandlerFunc
	}{
		{http.MethodGet, "/api/agent/metrics-pod-sample", s.handleMetricsPodSample},
		{http.MethodPost, "/api/agent/metrics-source/refresh", s.handleMetricsSourceRefresh},
		{http.MethodGet, "/api/metrics/openobserve/collector", s.handleOpenObserveCollectorPresence},
	} {
		// Spoofed identity headers on the ordinary listener, and the tunnel
		// without a caller, both look like a missing route.
		for name, h := range map[string]http.Handler{"direct": route.handler, "tunnel without caller": cloud.AuthenticatedTunnelHandler(route.handler)} {
			req := httptest.NewRequest(route.method, route.path, nil)
			req.Header.Set("X-Forwarded-User", "owner")
			w := httptest.NewRecorder()
			h.ServeHTTP(w, req)
			if w.Code != http.StatusNotFound {
				t.Fatalf("%s %s: got %d, want 404", route.path, name, w.Code)
			}
		}
		// A direct request carrying a user (auth middleware ran) but not the
		// tunnel mark is still refused.
		req := httptest.NewRequest(route.method, route.path, nil)
		req = req.WithContext(auth.ContextWithUser(req.Context(), &auth.User{Username: "owner"}))
		w := httptest.NewRecorder()
		route.handler.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s with user but no tunnel: got %d, want 404", route.path, w.Code)
		}
	}
}

func TestMetricsSourceRefreshInsideTunnel(t *testing.T) {
	s := &Server{}
	req := httptest.NewRequest(http.MethodPost, "/api/agent/metrics-source/refresh", nil)
	req = req.WithContext(auth.ContextWithUser(req.Context(), &auth.User{Username: "owner"}))
	w := httptest.NewRecorder()
	cloud.AuthenticatedTunnelHandler(http.HandlerFunc(s.handleMetricsSourceRefresh)).ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("got %d, want 204", w.Code)
	}
}

func runningPod(ns, name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, UID: types.UID("uid-" + ns + "-" + name)},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func TestMetricsPodSampleOneListableNamespace(t *testing.T) {
	var pods []*corev1.Pod
	for i := range 150 {
		pods = append(pods, runningPod("big-denied", fmt.Sprintf("p-%03d", i)))
	}
	for i := range 120 {
		pods = append(pods, runningPod("busy", fmt.Sprintf("web-%03d", i)))
	}
	for i := range 5 {
		pods = append(pods, runningPod("small", fmt.Sprintf("s-%d", i)))
	}
	pending := runningPod("busy", "zz-pending")
	pending.Status.Phase = corev1.PodPending
	terminating := runningPod("busy", "zz-terminating")
	now := metav1.Now()
	terminating.DeletionTimestamp = &now
	pods = append(pods, pending, terminating)

	s := &Server{permCache: auth.NewPermissionCache()}
	permissions := &auth.UserPermissions{AllowedNamespaces: []string{"busy", "small"}}
	permissions.SetCanI("list", "", "pods", "big-denied", false)
	permissions.SetCanI("list", "", "pods", "busy", true)
	permissions.SetCanI("list", "", "pods", "small", true)
	s.permCache.Set("viewer", nil, permissions)
	r := httptest.NewRequest(http.MethodGet, "/api/agent/metrics-pod-sample", nil)
	r = r.WithContext(auth.ContextWithUser(r.Context(), &auth.User{Username: "viewer"}))

	sample, ok := s.samplePods(r, pods)
	if !ok || sample.Namespace != "busy" {
		t.Fatalf("sample namespace = %q ok=%v", sample.Namespace, ok)
	}
	if len(sample.Pods) != maxMetricsPodSample {
		t.Fatalf("sample size = %d, want %d", len(sample.Pods), maxMetricsPodSample)
	}
	for _, p := range sample.Pods {
		if p.UID != "uid-busy-"+p.Name || p.Name == "zz-pending" || p.Name == "zz-terminating" {
			t.Fatalf("unexpected sample entry %+v", p)
		}
	}

	denied := &auth.UserPermissions{AllowedNamespaces: []string{}}
	for _, ns := range []string{"big-denied", "busy", "small"} {
		denied.SetCanI("list", "", "pods", ns, false)
	}
	s.permCache.Set("stranger", nil, denied)
	r = r.WithContext(auth.ContextWithUser(r.Context(), &auth.User{Username: "stranger"}))
	if _, ok := s.samplePods(r, pods); ok {
		t.Fatal("sample returned to a caller who cannot list Pods anywhere")
	}

	if _, ok := (&Server{}).samplePods(r, pods); ok {
		t.Fatal("sample returned without per-user authorization")
	}
}

func TestRunningOpenObserveCollector(t *testing.T) {
	for _, tc := range []struct {
		name   string
		labels map[string]string
		phase  corev1.PodPhase
		want   bool
	}{
		{"chart name", map[string]string{"app.kubernetes.io/name": "openobserve-collector"}, corev1.PodRunning, true},
		{"part of", map[string]string{"app.kubernetes.io/part-of": "openobserve-collector"}, corev1.PodRunning, true},
		{"instance", map[string]string{"app.kubernetes.io/instance": "openobserve-collector"}, corev1.PodRunning, true},
		{"openobserve server", map[string]string{"app.kubernetes.io/name": "openobserve"}, corev1.PodRunning, false},
		{"starting", map[string]string{"app.kubernetes.io/name": "openobserve-collector"}, corev1.PodPending, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: tc.labels}, Status: corev1.PodStatus{Phase: tc.phase}}
			if got := runningOpenObserveCollector(p); got != tc.want {
				t.Fatalf("detected=%v, want %v", got, tc.want)
			}
		})
	}
}
