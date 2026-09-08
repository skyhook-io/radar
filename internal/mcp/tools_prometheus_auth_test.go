package mcp

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"k8s.io/client-go/kubernetes"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/prometheus"
)

// The three unbounded Prometheus tools share one gate: an any-namespace
// "list pods" SubjectAccessReview. Each row below runs a tool with an input
// that succeeds against the fake Prometheus, so a failure can only be the
// gate.
var promToolRows = []struct {
	name string
	call func(ctx context.Context) (*mcp.CallToolResult, error)
}{
	{"query_prometheus", func(ctx context.Context) (*mcp.CallToolResult, error) {
		res, _, err := handleQueryPrometheus(ctx, nil, queryPrometheusInput{Query: "up"})
		return res, err
	}},
	{"discover_metrics", func(ctx context.Context) (*mcp.CallToolResult, error) {
		res, _, err := handleDiscoverMetrics(ctx, nil, discoverMetricsInput{Match: `{__name__=~"up"}`})
		return res, err
	}},
	{"get_prometheus_rules", func(ctx context.Context) (*mcp.CallToolResult, error) {
		res, _, err := handleGetPrometheusRules(ctx, nil, getPrometheusRulesInput{})
		return res, err
	}},
}

type sarCall struct {
	namespace, group, resource, verb string
}

// stubClusterWideSAR answers every SubjectAccessReview from decide and
// records the tuples asked. A placeholder clientset is installed so the
// helper takes the SAR path rather than its no-client fail-closed branch.
func stubClusterWideSAR(t *testing.T, decide func(sarCall) bool) *[]sarCall {
	t.Helper()
	previous := k8s.SetTestClient(&kubernetes.Clientset{})
	t.Cleanup(func() { k8s.SetTestClient(previous) })
	calls := &[]sarCall{}
	var mu sync.Mutex
	stubSubjectCanI(t, func(_ context.Context, _ kubernetes.Interface, _ string, _ []string, namespace, group, resource, verb string) (bool, error) {
		c := sarCall{namespace, group, resource, verb}
		mu.Lock()
		*calls = append(*calls, c)
		mu.Unlock()
		return decide(c), nil
	})
	return calls
}

func TestPrometheusTools_NoAuthPassesThrough(t *testing.T) {
	f := setupFakeProm(t)
	calls := stubClusterWideSAR(t, func(sarCall) bool { return false })

	for _, row := range promToolRows {
		t.Run(row.name, func(t *testing.T) {
			if _, err := row.call(context.Background()); err != nil {
				t.Fatalf("no-auth caller should pass through, got: %v", err)
			}
		})
	}
	if len(*calls) != 0 {
		t.Errorf("no-auth path must not issue a SubjectAccessReview, got %+v", *calls)
	}
	f.mu.Lock()
	probes := f.probeCalls
	f.mu.Unlock()
	if probes == 0 {
		t.Error("expected the tools to reach the fake prometheus")
	}
}

// A user who may list pods in alpha but not cluster-wide is refused with
// the message that names the missing grant, and Prometheus is never asked.
func TestPrometheusTools_NamespaceScopedUserDenied(t *testing.T) {
	f := setupFakeProm(t)
	ctx := withTestUserPerms(t, "alice", nil, []string{"alpha"})
	calls := stubClusterWideSAR(t, func(c sarCall) bool { return c.namespace == "alpha" })

	for _, row := range promToolRows {
		t.Run(row.name, func(t *testing.T) {
			_, err := row.call(ctx)
			if err == nil {
				t.Fatal("namespace-scoped user should be denied")
			}
			if err.Error() != prometheus.ClusterWideMetricsDeniedMessage {
				t.Fatalf("error = %q, want %q", err.Error(), prometheus.ClusterWideMetricsDeniedMessage)
			}
		})
	}
	for _, c := range *calls {
		if c != (sarCall{"", "", "pods", "list"}) {
			t.Errorf("gate asked %+v, want the any-namespace list-pods check", c)
		}
	}
	f.mu.Lock()
	probes := f.probeCalls
	f.mu.Unlock()
	if probes != 0 {
		t.Errorf("denied calls must not reach prometheus, saw %d connection probes", probes)
	}
}

func TestPrometheusTools_ClusterWideUserAllowed(t *testing.T) {
	setupFakeProm(t)
	ctx := withTestUserPerms(t, "bob", nil, []string{"alpha"})
	calls := stubClusterWideSAR(t, func(c sarCall) bool {
		return c.namespace == "" && c.resource == "pods" && c.verb == "list"
	})

	for _, row := range promToolRows {
		t.Run(row.name, func(t *testing.T) {
			res, err := row.call(ctx)
			if err != nil {
				t.Fatalf("cluster-wide reader should be allowed, got: %v", err)
			}
			if strings.Contains(extractText(t, res), prometheus.ClusterWideMetricsDeniedMessage) {
				t.Fatal("denial message leaked into a successful response")
			}
		})
	}
	if len(*calls) == 0 {
		t.Fatal("expected at least one SubjectAccessReview for an authenticated user")
	}
	if (*calls)[0] != (sarCall{"", "", "pods", "list"}) {
		t.Errorf("first SAR = %+v, want the any-namespace list-pods check", (*calls)[0])
	}
	// The verdict memoizes on the user's permission cache, so the three
	// tools share one review rather than issuing one apiece.
	if len(*calls) != 1 {
		t.Errorf("SAR issued %d times across three tools, want 1 (memoized)", len(*calls))
	}
}

// An apiserver that cannot answer is a denial, not a pass: the tools refuse
// without touching Prometheus, and the verdict is not memoized, so the next
// call asks again once the apiserver is back.
func TestPrometheusTools_SARErrorFailsClosedWithoutCaching(t *testing.T) {
	f := setupFakeProm(t)
	ctx := withTestUserPerms(t, "alice", nil, []string{"alpha"})
	previous := k8s.SetTestClient(&kubernetes.Clientset{})
	t.Cleanup(func() { k8s.SetTestClient(previous) })
	failing := true
	stubSubjectCanI(t, func(context.Context, kubernetes.Interface, string, []string, string, string, string, string) (bool, error) {
		if failing {
			return true, errors.New("apiserver unreachable")
		}
		return true, nil
	})

	for _, row := range promToolRows {
		_, err := row.call(ctx)
		if err == nil || err.Error() != prometheus.ClusterWideMetricsDeniedMessage {
			t.Fatalf("%s: SAR error should deny with the grant message, got %v", row.name, err)
		}
	}
	f.mu.Lock()
	probes := f.probeCalls
	f.mu.Unlock()
	if probes != 0 {
		t.Fatalf("denied calls must not reach prometheus, saw %d connection probes", probes)
	}

	failing = false
	if _, err := promToolRows[0].call(ctx); err != nil {
		t.Fatalf("once the apiserver answers, the same user should pass: %v", err)
	}
}
