package reachability

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/trace"
	"github.com/skyhook-io/radar/pkg/probe"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

// TestUncleanProbeStatus pins that the unclean-fold reason matches what actually
// happened: a failed connect blames NetworkPolicy / mTLS, but a backend that
// answered with a 5xx must NOT - the pod reached a server.
func TestUncleanProbeStatus(t *testing.T) {
	didntConnect := []probe.Result{{Layer: "tcp", OK: false}}
	if got := uncleanProbeStatus(didntConnect); !strings.Contains(got, "didn't get through") {
		t.Errorf("failed connect should read as a transport failure, got %q", got)
	}
	reached5xx := []probe.Result{
		{Layer: "tcp", OK: true, Tone: probe.ToneHealthy},
		{Layer: "http", OK: true, Tone: probe.ToneDegraded},
	}
	got := uncleanProbeStatus(reached5xx)
	if strings.Contains(got, "didn't get through") || strings.Contains(got, "NetworkPolicy") {
		t.Errorf("a reached-but-5xx result must not blame NetworkPolicy, got %q", got)
	}
	if !strings.Contains(got, "server error") {
		t.Errorf("a reached-but-5xx result should say the backend returned a server error, got %q", got)
	}
	certFail := []probe.Result{
		{Layer: probe.LayerTCP, OK: true, Tone: probe.ToneHealthy},
		{Layer: probe.LayerTLS, OK: true, Tone: probe.ToneDegraded},
	}
	gotCert := uncleanProbeStatus(certFail)
	if strings.Contains(gotCert, "server error") || !strings.Contains(gotCert, "certificate") {
		t.Errorf("a degraded TLS result should read as a certificate problem, not a server error, got %q", gotCert)
	}
}

// TestRunInClusterTests_NilClientIsHonest: with no impersonated client in context,
// the in-cluster run must report an honest per-route status + a copyable fallback
// command - never panic, never silently drop the request.
func TestRunInClusterTests_NilClientIsHonest(t *testing.T) {
	tr := &trace.Trace{
		Routes: []trace.RouteResult{{
			Route: "api", Target: "api:80", Outcome: trace.OutcomeReached, Confidence: trace.ConfidenceIndirect,
			InClusterRequest: &trace.ProbeRequest{Protocol: "http", Scheme: "http", Path: "/"},
		}},
	}
	tests, byTarget := RunInClusterTests(context.Background(), tr, "prod")
	if len(tests) != 1 {
		t.Fatalf("want 1 per-route result, got %d", len(tests))
	}
	if tests[0].Status == "" {
		t.Error("a nil client must yield an honest status, not an empty/successful result")
	}
	if tests[0].FallbackCommand == "" || !strings.Contains(tests[0].FallbackCommand, "kubectl run") {
		t.Errorf("nil-client result must carry a copyable fallback command, got %q", tests[0].FallbackCommand)
	}
	if len(byTarget) != 0 {
		t.Error("no results should be folded into the trace when the probe couldn't run")
	}
}

// TestRunInClusterTests_NilClientDoesNotConsumeCap: a nil client fails auth for
// EVERY route and creates no probe pod, so it must not burn the per-call probe
// cap - routes past the cap must still report the auth failure, never "capped".
func TestRunInClusterTests_NilClientDoesNotConsumeCap(t *testing.T) {
	var routes []trace.RouteResult
	for i := 0; i < MaxInClusterProbes+2; i++ {
		routes = append(routes, trace.RouteResult{
			Route: fmt.Sprintf("r%d", i), Target: fmt.Sprintf("svc%d:80", i),
			Outcome: trace.OutcomeReached, Confidence: trace.ConfidenceIndirect,
			InClusterRequest: &trace.ProbeRequest{Protocol: "http", Scheme: "http", Path: "/"},
		})
	}
	tests, _ := RunInClusterTests(context.Background(), &trace.Trace{Routes: routes}, "prod")
	if len(tests) != len(routes) {
		t.Fatalf("want %d results, got %d", len(routes), len(tests))
	}
	for i, r := range tests {
		if strings.Contains(r.Status, "capped") {
			t.Errorf("route %d mislabeled 'capped' under a nil client: %q", i, r.Status)
		}
		if !strings.Contains(r.Status, "auth") {
			t.Errorf("route %d should report the auth failure, got %q", i, r.Status)
		}
	}
}

// TestRunInClusterTests_PathOverrideThreads pins that the operator's custom path
// reaches every route's probe (the fallback command mirrors the Job args exactly)
// and that the result row honestly records the tested path - while the trace's
// own route request stays untouched.
func TestRunInClusterTests_PathOverrideThreads(t *testing.T) {
	tr := &trace.Trace{
		Routes: []trace.RouteResult{{
			Route: "api", Target: "api:80", Outcome: trace.OutcomeReached, Confidence: trace.ConfidenceIndirect,
			InClusterRequest: &trace.ProbeRequest{Protocol: "http", Scheme: "http", Path: "/declared", PathGuessed: true},
		}},
	}
	tests, _ := RunInClusterTestsWithOptions(context.Background(), tr, "prod", InClusterTestOptions{PathOverride: "/healthz"})
	if len(tests) != 1 {
		t.Fatalf("want 1 result, got %d", len(tests))
	}
	if !strings.Contains(tests[0].FallbackCommand, "--path '/healthz'") {
		t.Errorf("fallback must probe the OVERRIDDEN path, got %q", tests[0].FallbackCommand)
	}
	if tests[0].Request == nil || tests[0].Request.Path != "/healthz" {
		t.Errorf("result row must record the tested path, got %+v", tests[0].Request)
	}
	if tests[0].Request.PathGuessed {
		t.Error("an explicit operator path is not a guess - PathGuessed must be cleared")
	}
	if tr.Routes[0].InClusterRequest.Path != "/declared" || !tr.Routes[0].InClusterRequest.PathGuessed {
		t.Errorf("the trace's own route request must stay untouched, got %+v", tr.Routes[0].InClusterRequest)
	}
}

// TestRunInClusterTests_DeclaredPathSanitized pins that EVERY probe path is
// sanitized before it reaches the Job - not only the operator override. A raw
// declared path carrying CR/LF (a malformed route spec) must reach the runner
// with the control characters stripped and a leading slash forced.
func TestRunInClusterTests_DeclaredPathSanitized(t *testing.T) {
	calls := stubCleanProbe(t)
	tr := &trace.Trace{
		Routes: []trace.RouteResult{{
			Route: "api", Target: "api:80", Outcome: trace.OutcomeReached, Confidence: trace.ConfidenceIndirect,
			InClusterRequest: &trace.ProbeRequest{Protocol: "http", Scheme: "http", Path: "api/v1\r\nX-Injected: 1"},
		}},
	}
	runInClusterTests(context.Background(), fake.NewSimpleClientset(), "img:test", tr, "prod", InClusterTestOptions{})
	if len(*calls) != 1 {
		t.Fatalf("want 1 probe call, got %d", len(*calls))
	}
	got := (*calls)[0].Path
	if strings.ContainsAny(got, "\r\n") {
		t.Errorf("declared path reached the runner with CR/LF unstripped: %q", got)
	}
	if got != "/api/v1X-Injected: 1" {
		t.Errorf("declared path not sanitized as expected, got %q", got)
	}
}

func TestRunInClusterTests_ProtocolSelectsProbeLayers(t *testing.T) {
	calls := stubCleanProbe(t)
	tr := &trace.Trace{
		Routes: []trace.RouteResult{
			{
				Route: "web", Target: "web:8080",
				InClusterRequest: &trace.ProbeRequest{Protocol: "http", Scheme: "http", Path: "/"},
			},
			{
				Route: "database", Target: "database:5432",
				InClusterRequest: &trace.ProbeRequest{Protocol: "tcp", Scheme: "http", Path: "/"},
			},
			{
				Route: "database-on-443", Target: "database:443",
				InClusterRequest: &trace.ProbeRequest{Protocol: "tcp", Scheme: "https", Path: "/"},
			},
		},
	}
	tests, _ := runInClusterTests(context.Background(), fake.NewSimpleClientset(), "img:test", tr, "prod", InClusterTestOptions{})
	if len(*calls) != 3 {
		t.Fatalf("want 3 probe calls, got %d", len(*calls))
	}
	if got := (*calls)[0].Layers; got != "tcp,http" {
		t.Errorf("HTTP route layers = %q, want tcp,http", got)
	}
	if got := (*calls)[0].Scheme; got != "http" {
		t.Errorf("HTTP route scheme = %q, want http", got)
	}
	if got := (*calls)[1].Layers; got != "tcp" {
		t.Errorf("non-HTTP route layers = %q, want tcp", got)
	}
	if got := (*calls)[1].Scheme; got != "" {
		t.Errorf("non-HTTP route scheme = %q, want empty", got)
	}
	if got := (*calls)[2].Layers; got != "tcp" {
		t.Errorf("non-HTTP route on port 443 layers = %q, want tcp", got)
	}
	if got := (*calls)[2].Scheme; got != "" {
		t.Errorf("non-HTTP route on port 443 scheme = %q, want empty so TLS cannot run", got)
	}
	for _, i := range []int{1, 2} {
		if (*calls)[i].Path != "" || (*calls)[i].Host != "" {
			t.Errorf("TCP route %d carried HTTP options: %+v", i, (*calls)[i])
		}
		if tests[i].Request == nil || tests[i].Request.Scheme != "" || tests[i].Request.Path != "" || tests[i].Request.Host != "" {
			t.Errorf("TCP result %d carried contradictory HTTP request fields: %+v", i, tests[i].Request)
		}
	}
}

func TestRunInClusterTests_TCPPathOverrideIgnored(t *testing.T) {
	calls := stubCleanProbe(t)
	tr := &trace.Trace{
		Routes: []trace.RouteResult{{
			Route: "database", Target: "database:5432",
			InClusterRequest: &trace.ProbeRequest{
				Protocol: "tcp", Scheme: "https", Host: "database.example", Path: "/declared", PathGuessed: true,
			},
		}},
	}

	tests, byTarget := runInClusterTests(
		context.Background(),
		fake.NewSimpleClientset(),
		"img:test",
		tr,
		"prod",
		InClusterTestOptions{PathOverride: "/healthz"},
	)

	if len(*calls) != 1 {
		t.Fatalf("want 1 probe call, got %d", len(*calls))
	}
	call := (*calls)[0]
	if call.Layers != "tcp" || call.Scheme != "" || call.Host != "" || call.Path != "" {
		t.Errorf("TCP probe options = %+v, want transport-only options", call)
	}
	if len(tests) != 1 || tests[0].Status != "" {
		t.Fatalf("TCP success should fold normally despite an HTTP path override, got %+v", tests)
	}
	if tests[0].Request == nil || tests[0].Request.Protocol != "tcp" ||
		tests[0].Request.Scheme != "" || tests[0].Request.Host != "" ||
		tests[0].Request.Path != "" || tests[0].Request.PathGuessed {
		t.Errorf("TCP result request = %+v, want only protocol=tcp", tests[0].Request)
	}
	if _, ok := byTarget[trace.InClusterResultKey("database", "database:5432", "")]; !ok {
		t.Errorf("TCP result was not folded: %v", byTarget)
	}
}

// stubCleanProbe replaces the per-route probe with a canned CLEAN result (no
// probe pod, no cluster), restoring the real runner on cleanup. Returns the
// recorded per-route RunOptions so tests can assert what would have been dialed.
func stubCleanProbe(t *testing.T) *[]RunOptions {
	t.Helper()
	calls := &[]RunOptions{}
	orig := runProbe
	runProbe = func(_ context.Context, _ kubernetes.Interface, opts RunOptions) ([]probe.Result, string, error) {
		*calls = append(*calls, opts)
		return []probe.Result{{Layer: "http", OK: true, Tone: probe.ToneHealthy}}, FallbackCommand(opts), nil
	}
	t.Cleanup(func() { runProbe = orig })
	return calls
}

// TestRunInClusterTests_OverrideMismatchIsEvidenceOnly pins the vouching
// boundary: a CLEAN probe of an operator-override path folds ONLY into routes
// whose own declared path equals the override. The non-matching route keeps the
// probe as an informational row - folding it would read "verified over live
// traffic" for a declared path that was never dialed.
func TestRunInClusterTests_OverrideMismatchIsEvidenceOnly(t *testing.T) {
	calls := stubCleanProbe(t)
	tr := &trace.Trace{
		Routes: []trace.RouteResult{
			{
				Route: "shop.example.com/admin", Target: "admin:80",
				Outcome: trace.OutcomeReached, Confidence: trace.ConfidenceIndirect,
				InClusterRequest: &trace.ProbeRequest{Protocol: "http", Scheme: "http", Path: "/admin"},
			},
			{
				Route: "shop.example.com/healthz", Target: "web:80",
				Outcome: trace.OutcomeReached, Confidence: trace.ConfidenceIndirect,
				InClusterRequest: &trace.ProbeRequest{Protocol: "http", Scheme: "http", Path: "/healthz"},
			},
		},
	}
	tests, byTarget := runInClusterTests(context.Background(), fake.NewSimpleClientset(), "img:test", tr, "prod", InClusterTestOptions{PathOverride: "/healthz"})
	if len(tests) != 2 {
		t.Fatalf("want 2 rows, got %d", len(tests))
	}
	if len(*calls) != 2 {
		t.Fatalf("both routes must still be probed on the override path, got %d probes", len(*calls))
	}
	for _, c := range *calls {
		if c.Path != "/healthz" {
			t.Errorf("every probe must dial the operator's path, got %q", c.Path)
		}
	}

	// Non-matching route: evidence only, never folded.
	mismatch := tests[0]
	if len(mismatch.Results) == 0 {
		t.Error("the override probe result must be SHOWN on the non-matching route (evidence)")
	}
	for _, want := range []string{"evidence only", "/healthz", "/admin", "not tested"} {
		if !strings.Contains(mismatch.Status, want) {
			t.Errorf("mismatch row must mention %q, got %q", want, mismatch.Status)
		}
	}
	// Matching route: folds exactly as an un-overridden clean probe would.
	match := tests[1]
	if match.Status != "" {
		t.Errorf("matching-path override must fold cleanly (no caveat), got %q", match.Status)
	}
	if match.Request == nil || match.Request.PathGuessed {
		t.Error("an explicit operator path is not a guess - PathGuessed must be cleared")
	}
	if len(byTarget) != 1 {
		t.Fatalf("ONLY the matching route may fold, got %d folded keys: %v", len(byTarget), byTarget)
	}
	if _, ok := byTarget[trace.InClusterResultKey("shop.example.com/healthz", "web:80", "")]; !ok {
		t.Errorf("the matching route's key must be folded, got %v", byTarget)
	}
}

// TestRunInClusterTests_NoOverrideCleanProbeFolds pins the baseline: without an
// override, a clean probe of the route's own declared path folds into byTarget.
func TestRunInClusterTests_NoOverrideCleanProbeFolds(t *testing.T) {
	stubCleanProbe(t)
	tr := &trace.Trace{
		Routes: []trace.RouteResult{{
			Route: "api", Target: "api:80", Outcome: trace.OutcomeReached, Confidence: trace.ConfidenceIndirect,
			InClusterRequest: &trace.ProbeRequest{Protocol: "http", Scheme: "http", Path: "/"},
		}},
	}
	tests, byTarget := runInClusterTests(context.Background(), fake.NewSimpleClientset(), "img:test", tr, "prod", InClusterTestOptions{})
	if len(tests) != 1 || tests[0].Status != "" {
		t.Fatalf("clean declared-path probe must fold without caveat, got %+v", tests)
	}
	if _, ok := byTarget[trace.InClusterResultKey("api", "api:80", "")]; !ok {
		t.Errorf("declared-path probe must fold into byTarget, got %v", byTarget)
	}
}

// TestNormalizeProbePath pins the honest normalization: leading slash, control
// chars stripped, and NO path.Clean - dot segments and doubled slashes are part
// of the HTTP request target the user asked for, not filesystem noise.
func TestNormalizeProbePath(t *testing.T) {
	cases := []struct{ in, want string }{
		{"", ""},
		{"   ", ""},
		{"/", "/"},
		{"healthz", "/healthz"},
		{"/a/b", "/a/b"},
		{"/a//b", "/a//b"},         // no slash collapsing
		{"/a/../b", "/a/../b"},     // no dot-segment resolution
		{"/a\x00b\x1f\x7f", "/ab"}, // control chars stripped
	}
	for _, c := range cases {
		if got := NormalizeProbePath(c.in); got != c.want {
			t.Errorf("NormalizeProbePath(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestRunInClusterTests_BudgetExhaustedRows: when the request deadline can't fit
// another meaningful probe run, the remaining routes must get explicit
// "not tested" rows (skipped ≠ failed, and skipped ≠ silence) - not a confusing
// context-canceled error from a run that was doomed at start.
func TestRunInClusterTests_BudgetExhaustedRows(t *testing.T) {
	var routes []trace.RouteResult
	for i := 0; i < 3; i++ {
		routes = append(routes, trace.RouteResult{
			Route: fmt.Sprintf("r%d", i), Target: fmt.Sprintf("svc%d:80", i),
			Outcome: trace.OutcomeReached, Confidence: trace.ConfidenceIndirect,
			InClusterRequest: &trace.ProbeRequest{Protocol: "http", Scheme: "http", Path: "/"},
		})
	}
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(time.Second))
	defer cancel()
	tests, byTarget := runInClusterTests(ctx, fake.NewSimpleClientset(), "img:test", &trace.Trace{Routes: routes}, "prod", InClusterTestOptions{})
	if len(tests) != len(routes) {
		t.Fatalf("want %d rows, got %d", len(routes), len(tests))
	}
	for i, r := range tests {
		if !strings.Contains(r.Status, "not tested") || !strings.Contains(r.Status, "budget exhausted") {
			t.Errorf("route %d: want an explicit budget-exhausted row, got %q", i, r.Status)
		}
		if !strings.Contains(r.Status, fmt.Sprintf("(0 of %d routes tested)", len(routes))) {
			t.Errorf("route %d: row must say how far the run got, got %q", i, r.Status)
		}
		if strings.Contains(r.Status, "context canceled") {
			t.Errorf("route %d: the confusing context-canceled wording must not surface, got %q", i, r.Status)
		}
	}
	if len(byTarget) != 0 {
		t.Error("nothing ran, so nothing may fold into the trace")
	}
}

// TestRunInClusterTests_NoDeadlineRunsNormally: without a request deadline the
// budget logic must not kick in - the run proceeds under the per-run cap alone.
func TestRunInClusterTests_NoDeadlineRunsNormally(t *testing.T) {
	tr := &trace.Trace{
		Routes: []trace.RouteResult{{
			Route: "api", Target: "api:80", Outcome: trace.OutcomeReached, Confidence: trace.ConfidenceIndirect,
			InClusterRequest: &trace.ProbeRequest{Protocol: "http", Scheme: "http", Path: "/"},
		}},
	}
	// The fake client denies the capability SSAR by default, so the run reports
	// the permission denial - the point is that it RAN (no budget row).
	tests, _ := runInClusterTests(context.Background(), fake.NewSimpleClientset(), "img:test", tr, "prod", InClusterTestOptions{})
	if len(tests) != 1 {
		t.Fatalf("want 1 row, got %d", len(tests))
	}
	if strings.Contains(tests[0].Status, "budget exhausted") {
		t.Errorf("no deadline must mean no budget stop, got %q", tests[0].Status)
	}
}

// TestRunInClusterTests_ZeroEligibleRoutesIsExplicit: a subject with nothing
// testable must return an explicit row saying why, never an empty (silent)
// result the operator can't distinguish from success.
func TestRunInClusterTests_ZeroEligibleRoutesIsExplicit(t *testing.T) {
	cases := []struct {
		name string
		tr   *trace.Trace
		want string
	}{
		{
			"Gateway subject (no route carries a request)",
			&trace.Trace{
				Subject: trace.ResourceRef{Kind: "Gateway", Name: "team-gw"},
				Routes:  []trace.RouteResult{{Route: "listener-https", Target: ""}},
			},
			"isn't supported for Gateway subjects yet",
		},
		{
			"no routes at all",
			&trace.Trace{Subject: trace.ResourceRef{Kind: "Gateway", Name: "edge"}},
			"isn't supported for Gateway subjects yet",
		},
		{
			"all routes benign (scaled to zero)",
			&trace.Trace{
				Subject: trace.ResourceRef{Kind: "Service", Name: "sleeper"},
				Routes: []trace.RouteResult{{
					Route: "svc", Target: "sleeper:80", Benign: true,
					InClusterRequest: &trace.ProbeRequest{Protocol: "http", Scheme: "http", Path: "/"},
				}},
			},
			"intentionally scaled to zero",
		},
		{
			"routes without a concrete request",
			&trace.Trace{
				Subject: trace.ResourceRef{Kind: "Ingress", Name: "web"},
				Routes:  []trace.RouteResult{{Route: "host/path", Target: "web:80"}},
			},
			"isn't supported for this subject yet",
		},
	}
	for _, c := range cases {
		tests, byTarget := RunInClusterTests(context.Background(), c.tr, "prod")
		if len(tests) != 1 {
			t.Fatalf("%s: want exactly 1 explicit row, got %d", c.name, len(tests))
		}
		if !strings.Contains(tests[0].Status, c.want) {
			t.Errorf("%s: status = %q, want it to mention %q", c.name, tests[0].Status, c.want)
		}
		if tests[0].FallbackCommand != "" {
			t.Errorf("%s: nothing to run manually, fallback must be empty, got %q", c.name, tests[0].FallbackCommand)
		}
		if len(byTarget) != 0 {
			t.Errorf("%s: nothing ran, nothing may fold into the trace", c.name)
		}
	}
}
