package prometheus

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

// The golden files pin the exact queries a store with no cluster labels
// receives. A single-cluster store must keep receiving them byte for byte.

func checkRightsizingGolden(t *testing.T, name string, queries []string) {
	t.Helper()
	got := strings.Join(slices.Compact(slices.Sorted(slices.Values(queries))), "\n") + "\n"
	path := filepath.Join("testdata", "rightsizing_golden", name+".txt")
	if os.Getenv("RADAR_UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(want) {
		t.Fatalf("%s queries changed\n got:\n%s\nwant:\n%s", name, got, want)
	}
}

type recordingRightsizingQuerier struct {
	mu      sync.Mutex
	queries []string
	answer  func(string) (*prom.QueryResult, error)
}

func (r *recordingRightsizingQuerier) Query(_ context.Context, query string) (*prom.QueryResult, error) {
	r.mu.Lock()
	r.queries = append(r.queries, query)
	r.mu.Unlock()
	if r.answer != nil {
		return r.answer(query)
	}
	return &prom.QueryResult{}, nil
}

func goldenWorkload(pods ...string) rightsizingWorkload {
	return rightsizingWorkload{
		containers: []containerSpec{{name: "app"}}, podNames: pods,
		currentPodOOM: map[string]bool{}, hpaManaged: map[string]bool{},
	}
}

func ownerProbeAnswers(query string) (*prom.QueryResult, error) {
	if strings.HasPrefix(query, "sum(count_over_time(") {
		return ksmAvailable(), nil
	}
	return &prom.QueryResult{}, nil
}

func TestRightsizingSingleClusterGoldenQueries(t *testing.T) {
	for _, tc := range []struct {
		name, kind string
		pods       []string
		owned      bool
	}{
		{name: "single_deployment_ksm", kind: "Deployment", pods: []string{"api-1"}, owned: true},
		{name: "single_statefulset_ksm", kind: "StatefulSet", pods: []string{"api-0"}, owned: true},
		{name: "single_daemonset_current_pods", kind: "DaemonSet", pods: []string{"api-x", "api.y"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := &recordingRightsizingQuerier{}
			if tc.owned {
				q.answer = ownerProbeAnswers
			}
			computeRightsizing(context.Background(), q, tc.kind, "payments", "api", goldenWorkload(tc.pods...), unlabeledRightsizingScope())
			checkRightsizingGolden(t, tc.name, q.queries)
		})
	}
}

func TestRightsizingScanSingleClusterGoldenQueries(t *testing.T) {
	client := &fakeScanQuerier{queryFn: func(query string) (*prom.QueryResult, error) {
		if strings.HasPrefix(query, "count(") {
			return ksmAvailable(), nil
		}
		return &prom.QueryResult{}, nil
	}}
	workloads := []scanWorkload{
		scanTestWorkload("Deployment", "payments", "api", "app"),
		scanTestWorkload("StatefulSet", "payments", "db", "db"),
		scanTestWorkload("DaemonSet", "infra", "agent", "agent"),
	}
	resolve := func(context.Context, []scanWorkload) (ViewScope, error) { return unlabeledRightsizingScope(), nil }
	computeRightsizingScanProgress(context.Background(), client, resolve, workloads, newRightsizingScanResponse(time.Unix(1700000000, 0), RightsizingScanScope{}), func(RightsizingScanResponse) {})
	checkRightsizingGolden(t, "scan_instant", client.queries)
	checkRightsizingGolden(t, "scan_range", client.rangeQueries)
}

// unlabeledRightsizingScope is what a store with no cluster labels resolves to.
func unlabeledRightsizingScope() ViewScope {
	filters := map[string]string{}
	for _, m := range rightsizingMetricNames {
		filters[m] = ""
	}
	return ViewScope{State: ViewScopeUnlabeled, Filters: filters, Note: viewScopeUnlabeledNote}
}
