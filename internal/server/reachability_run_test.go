package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/reachability"
	"github.com/skyhook-io/radar/internal/trace"
	"github.com/skyhook-io/radar/pkg/probe"
)

// TestStampInClusterProbes_DemotesDegraded pins that a throwaway-pod result that
// reached the backend but degraded (OK=true, ToneDegraded - a 5xx or TLS/cert
// problem) is stamped as a SKIP, not a live degraded probe. The fold logic keeps
// it informational, so the matrix must not paint the path degraded.
func TestStampInClusterProbes_DemotesDegraded(t *testing.T) {
	tr := &trace.Trace{
		Downstream: []trace.Hop{
			{Resource: trace.ResourceRef{Kind: "Service", Name: "api"}},
		},
	}
	tests := []reachability.InClusterTestResult{{
		Route:  "api",
		Target: "api:80",
		Results: []probe.Result{
			{Layer: probe.LayerHTTP, OK: true, Tone: probe.ToneDegraded},
		},
	}}
	stampInClusterProbes(tr, tests)

	var inCluster []probe.Result
	for _, p := range tr.Downstream[0].Probes {
		if p.Vantage == probe.VantageInCluster {
			inCluster = append(inCluster, p)
		}
	}
	if len(inCluster) != 1 {
		t.Fatalf("want 1 stamped in-cluster probe, got %d", len(inCluster))
	}
	if !inCluster[0].Skipped {
		t.Errorf("a reached-but-degraded throwaway probe must be demoted to a skip so the matrix does not condemn the path, got %+v", inCluster[0])
	}
}

// TestStampInClusterProbes_KeepsCleanReach pins that a genuinely clean throwaway
// reach (OK, healthy) is stamped as a live, non-skipped probe - it is real
// evidence the matrix should show.
func TestStampInClusterProbes_KeepsCleanReach(t *testing.T) {
	tr := &trace.Trace{
		Downstream: []trace.Hop{
			{Resource: trace.ResourceRef{Kind: "Service", Name: "api"}},
		},
	}
	tests := []reachability.InClusterTestResult{{
		Route:  "api",
		Target: "api:80",
		Results: []probe.Result{
			{Layer: probe.LayerHTTP, OK: true, Tone: probe.ToneHealthy},
		},
	}}
	stampInClusterProbes(tr, tests)

	if len(tr.Downstream[0].Probes) != 1 || tr.Downstream[0].Probes[0].Skipped {
		t.Errorf("a clean in-cluster reach must stay a live probe, got %+v", tr.Downstream[0].Probes)
	}
	if tr.Downstream[0].Probes[0].Port != 80 {
		t.Errorf("stamped probe port = %d, want 80 so it rejoins the route row", tr.Downstream[0].Probes[0].Port)
	}
}

// A same-namespace probe result (empty TargetNamespace = the subject's own
// namespace, the producer's omitempty convention) must never stamp its live
// evidence onto a cross-namespace hop that shares the backend name - Gateway
// API cross-namespace backendRefs make same-named Services across namespaces a
// real shape, and "real in-cluster traffic" is the strongest evidence class.
func TestStampInClusterProbes_SameNSResultSkipsCrossNSTwin(t *testing.T) {
	tr := &trace.Trace{
		Subject: trace.ResourceRef{Kind: "HTTPRoute", Namespace: "ns1", Name: "route"},
		Downstream: []trace.Hop{
			{Resource: trace.ResourceRef{Kind: "Service", Namespace: "ns1", Name: "api"}},
			{Resource: trace.ResourceRef{Kind: "Service", Namespace: "ns2", Name: "api"}},
		},
	}
	stampInClusterProbes(tr, []reachability.InClusterTestResult{{
		Route:  "api",
		Target: "api:80",
		// No TargetNamespace: this is the subject-namespace backend.
		Results: []probe.Result{{Layer: probe.LayerHTTP, OK: true, Tone: probe.ToneHealthy}},
	}})

	if got := len(tr.Downstream[0].Probes); got != 1 {
		t.Errorf("subject-ns hop probes = %d, want 1", got)
	}
	if got := len(tr.Downstream[1].Probes); got != 0 {
		t.Errorf("cross-ns twin hop probes = %d, want 0 - it was never dialled", got)
	}
}

// The inverse direction: a result stamped for the cross-namespace backend lands
// only on its own hop, never on the subject-namespace twin.
func TestStampInClusterProbes_CrossNSResultSkipsSubjectNSTwin(t *testing.T) {
	tr := &trace.Trace{
		Subject: trace.ResourceRef{Kind: "HTTPRoute", Namespace: "ns1", Name: "route"},
		Downstream: []trace.Hop{
			{Resource: trace.ResourceRef{Kind: "Service", Namespace: "ns1", Name: "api"}},
			{Resource: trace.ResourceRef{Kind: "Service", Namespace: "ns2", Name: "api"}},
		},
	}
	stampInClusterProbes(tr, []reachability.InClusterTestResult{{
		Route:           "api",
		Target:          "api:80",
		TargetNamespace: "ns2",
		Results:         []probe.Result{{Layer: probe.LayerHTTP, OK: true, Tone: probe.ToneHealthy}},
	}})

	if got := len(tr.Downstream[0].Probes); got != 0 {
		t.Errorf("subject-ns hop probes = %d, want 0", got)
	}
	if got := len(tr.Downstream[1].Probes); got != 1 {
		t.Errorf("cross-ns hop probes = %d, want 1", got)
	}
}

// The one mutating action here is creating probe Pods in the customer's
// cluster. The Cloud-role gate (Member+) is the only thing between a Cloud
// Viewer and that - regression means a viewer creates pods.
func TestTraceInClusterRequiresCloudMember(t *testing.T) {
	// Only recognized sub-Member tiers are denied: an unrecognized tier group
	// maps to RoleNone, which AtLeast() deliberately bypasses so non-Cloud
	// deploys aren't gated.
	for _, tier := range []string{"cloud:viewer", "radar:viewer"} {
		t.Run(tier, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/trace/Service/prod/web/in-cluster", strings.NewReader(`{}`))
			rctx := chi.NewRouteContext()
			rctx.URLParams.Add("kind", "Service")
			rctx.URLParams.Add("namespace", "prod")
			rctx.URLParams.Add("name", "web")
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
			req = req.WithContext(auth.ContextWithUser(req.Context(), userWithGroups(tier)))
			rec := httptest.NewRecorder()

			(&Server{}).handleTraceInCluster(rec, req)

			if rec.Code != http.StatusForbidden {
				t.Fatalf("status = %d, want 403; body=%s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), auth.ErrCodeCloudRoleInsufficient) {
				t.Fatalf("body %q missing %q", rec.Body.String(), auth.ErrCodeCloudRoleInsufficient)
			}
		})
	}
}

// An out-of-scope namespace must come back as an unknown-verdict trace, never a
// 403/404 - a status-shaped answer would be an existence oracle across
// namespaces. Mirrors handleTrace.
func TestTraceInClusterDoesNotLeakExistenceAcrossNamespaces(t *testing.T) {
	s := newTestServer(t)
	s.permCache = auth.NewPermissionCache()
	s.permCache.Set("u@example.com", nil, &auth.UserPermissions{AllowedNamespaces: []string{"ns-a"}})

	req := httptest.NewRequest(http.MethodPost, "/api/trace/Service/ns-b/secret-svc/in-cluster", strings.NewReader(`{}`))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("kind", "Service")
	rctx.URLParams.Add("namespace", "ns-b")
	rctx.URLParams.Add("name", "secret-svc")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	req = req.WithContext(auth.ContextWithUser(req.Context(), userWithGroups("radar:member")))
	rec := httptest.NewRecorder()

	s.handleTraceInCluster(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a status-shaped denial is an existence oracle); body=%s", rec.Code, rec.Body.String())
	}
	var resp traceInClusterResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Trace == nil || resp.Trace.Verdict != trace.VerdictUnknown {
		t.Fatalf("verdict = %+v, want unknown", resp.Trace)
	}
	if len(resp.Trace.Downstream) != 0 {
		t.Fatalf("downstream = %d hops, want 0 - nothing may leak", len(resp.Trace.Downstream))
	}
}
