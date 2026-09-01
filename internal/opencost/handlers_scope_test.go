package opencost

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	pkgopencost "github.com/skyhook-io/radar/pkg/opencost"
)

// startFakeProm serves cost metrics for team-a, team-b and kube-system so the
// scope tests can assert on which namespaces survive to the response.
func startFakeProm(t *testing.T) {
	t.Helper()

	vector := func(label string, samples map[string]string) string {
		parts := make([]string, 0, len(samples))
		for name, value := range samples {
			parts = append(parts, `{"metric":{"`+label+`":"`+name+`"},"value":[1700000000,"`+value+`"]}`)
		}
		return `{"status":"success","data":{"resultType":"vector","result":[` + strings.Join(parts, ",") + `]}}`
	}
	matrix := func(samples map[string]string) string {
		parts := make([]string, 0, len(samples))
		for ns, value := range samples {
			parts = append(parts, `{"metric":{"namespace":"`+ns+`"},"values":[[1700000000,"`+value+`"]]}`)
		}
		return `{"status":"success","data":{"resultType":"matrix","result":[` + strings.Join(parts, ",") + `]}}`
	}

	namespaceSamples := map[string]string{"team-a": "3", "team-b": "2", "kube-system": "1"}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = r.ParseForm()
		query := r.Form.Get("query")

		if query == "up" {
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"job":"prometheus"},"value":[1700000000,"1"]}]}}`))
			return
		}
		if r.URL.Path == "/api/v1/query_range" {
			_, _ = w.Write([]byte(matrix(namespaceSamples)))
			return
		}
		// Order matters: the per-namespace allocation queries join against the
		// node cost metrics, so they mention both. Namespace grouping wins.
		if strings.Contains(query, "by (namespace)") {
			_, _ = w.Write([]byte(vector("namespace", namespaceSamples)))
			return
		}
		if strings.Contains(query, "node_total_hourly_cost") || strings.Contains(query, "node_cpu_hourly_cost") || strings.Contains(query, "node_ram_hourly_cost") {
			// Cluster-scoped node series, and the summary's node-total ceiling —
			// deliberately larger than the sum of the namespace rows.
			_, _ = w.Write([]byte(vector("node", map[string]string{"node-1": "6", "node-2": "4"})))
			return
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))

	prometheuspkg.Initialize(nil, nil, "test")
	prometheuspkg.SetManualURL(srv.URL)
	t.Cleanup(func() {
		srv.Close()
		prometheuspkg.Reset()
		prometheuspkg.Initialize(nil, nil, "")
	})
}

// unrestricted is the scope a no-auth or cluster-admin caller resolves to.
var unrestricted = Scope{CanReadNodes: true}

func fixedScope(scope Scope) ScopeResolver {
	return func(*http.Request) Scope { return scope }
}

func getSummary(t *testing.T, scope Scope) *pkgopencost.CostSummary {
	t.Helper()
	w := httptest.NewRecorder()
	handleSummary(w, httptest.NewRequest(http.MethodGet, "/opencost/summary", nil), func() string { return "USD" }, fixedScope(scope))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var body pkgopencost.CostSummary
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (body %s)", err, w.Body.String())
	}
	return &body
}

func namespaceNames(rows []pkgopencost.NamespaceCost) []string {
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		names = append(names, row.Name)
	}
	return names
}

func TestSummaryUnrestrictedReturnsEveryNamespace(t *testing.T) {
	startFakeProm(t)

	got := getSummary(t, unrestricted)
	if !got.Available {
		t.Fatalf("Available = false, reason = %q", got.Reason)
	}
	if len(got.Namespaces) != 3 {
		t.Errorf("namespaces = %v, want all three", namespaceNames(got.Namespaces))
	}
	if got.Restricted {
		t.Error("Restricted = true, want false for an unrestricted caller")
	}
}

func TestSummaryIsFilteredToTheUsersNamespaces(t *testing.T) {
	startFakeProm(t)

	got := getSummary(t, Scope{Namespaces: []string{"team-a"}})
	if !got.Available {
		t.Fatalf("Available = false, reason = %q", got.Reason)
	}
	if names := namespaceNames(got.Namespaces); len(names) != 1 || names[0] != "team-a" {
		t.Fatalf("namespaces = %v, want only [team-a]", names)
	}
	if !got.Restricted {
		t.Error("Restricted = false, want true so the UI can label the view")
	}
}

// The regression the discussion actually reports: a namespace-scoped user was
// shown the cluster total. The total must not exceed what their own rows sum to.
func TestSummaryTotalExcludesNamespacesTheUserCannotSee(t *testing.T) {
	startFakeProm(t)

	clusterTotal := getSummary(t, unrestricted).TotalHourlyCost
	restricted := getSummary(t, Scope{Namespaces: []string{"team-a"}})

	var rowSum float64
	for _, row := range restricted.Namespaces {
		rowSum += row.HourlyCost
	}
	if restricted.TotalHourlyCost != rowSum {
		t.Errorf("TotalHourlyCost = %v, want %v (the sum of the visible rows)", restricted.TotalHourlyCost, rowSum)
	}
	if restricted.TotalHourlyCost >= clusterTotal {
		t.Errorf("restricted total %v is not below the cluster total %v — cost is still leaking", restricted.TotalHourlyCost, clusterTotal)
	}
}

func TestSummaryDeniedWhenUserHasNoNamespaces(t *testing.T) {
	startFakeProm(t)

	got := getSummary(t, Scope{Namespaces: []string{}})
	if got.Available {
		t.Error("Available = true, want false")
	}
	if got.Reason != pkgopencost.ReasonAccessDenied {
		t.Errorf("Reason = %q, want %q", got.Reason, pkgopencost.ReasonAccessDenied)
	}
	if len(got.Namespaces) != 0 {
		t.Errorf("namespaces = %v, want none", namespaceNames(got.Namespaces))
	}
	if got.Currency != "USD" {
		t.Errorf("Currency = %q, want USD", got.Currency)
	}
}

func getWorkloads(t *testing.T, scope Scope, namespace string) *pkgopencost.WorkloadCostResponse {
	t.Helper()
	w := httptest.NewRecorder()
	handleWorkloads(w, httptest.NewRequest(http.MethodGet, "/opencost/workloads?namespace="+namespace, nil), func() string { return "USD" }, fixedScope(scope))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var body pkgopencost.WorkloadCostResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (body %s)", err, w.Body.String())
	}
	return &body
}

func TestWorkloadsDeniedOutsideTheUsersNamespaces(t *testing.T) {
	startFakeProm(t)

	got := getWorkloads(t, Scope{Namespaces: []string{"team-a"}}, "team-b")
	if got.Available {
		t.Error("Available = true, want false for a namespace the user cannot see")
	}
	if got.Reason != pkgopencost.ReasonAccessDenied {
		t.Errorf("Reason = %q, want %q", got.Reason, pkgopencost.ReasonAccessDenied)
	}
	if len(got.Workloads) != 0 {
		t.Errorf("Workloads = %+v, want none", got.Workloads)
	}
	if got.Namespace != "team-b" || got.Currency != "USD" {
		t.Errorf("denied response lost its envelope: %+v", got)
	}
}

func TestWorkloadsServedInsideTheUsersNamespaces(t *testing.T) {
	startFakeProm(t)

	got := getWorkloads(t, Scope{Namespaces: []string{"team-a"}}, "team-a")
	if !got.Available {
		t.Errorf("Available = false (reason %q), want true — an in-scope namespace was not served", got.Reason)
	}
	if !got.Restricted {
		t.Error("Restricted = false, want true — the caller is namespace-scoped")
	}
}

func TestWorkloadsUnrestrictedIsUnchanged(t *testing.T) {
	startFakeProm(t)

	got := getWorkloads(t, unrestricted, "team-b")
	// Assert Available, not merely "not denied": with Prometheus absent the
	// response is unavailable/no_prometheus, so a not-denied check alone would
	// pass without the request ever reaching the compute layer.
	if !got.Available {
		t.Errorf("Available = false (reason %q), want true — the unrestricted caller was not served", got.Reason)
	}
	if got.Restricted {
		t.Error("Restricted = true, want false for an unrestricted caller")
	}
}

func getTrend(t *testing.T, scope Scope) *pkgopencost.CostTrendResponse {
	t.Helper()
	w := httptest.NewRecorder()
	handleTrend(w, httptest.NewRequest(http.MethodGet, "/opencost/trend?range=24h", nil), func() string { return "USD" }, fixedScope(scope))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var body pkgopencost.CostTrendResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (body %s)", err, w.Body.String())
	}
	return &body
}

func TestTrendSeriesFilteredToTheUsersNamespaces(t *testing.T) {
	startFakeProm(t)

	if all := getTrend(t, unrestricted); len(all.Series) < 2 {
		t.Fatalf("fixture produced %d series, need at least 2 to prove filtering", len(all.Series))
	}

	got := getTrend(t, Scope{Namespaces: []string{"team-a"}})
	if len(got.Series) != 1 || got.Series[0].Namespace != "team-a" {
		t.Fatalf("series = %+v, want only team-a", got.Series)
	}
	if !got.Restricted {
		t.Error("Restricted = false, want true")
	}
}

func TestTrendDeniedWhenUserHasNoNamespaces(t *testing.T) {
	startFakeProm(t)

	got := getTrend(t, Scope{Namespaces: []string{}})
	if got.Available {
		t.Error("Available = true, want false")
	}
	if got.Reason != pkgopencost.ReasonAccessDenied {
		t.Errorf("Reason = %q, want %q", got.Reason, pkgopencost.ReasonAccessDenied)
	}
	if len(got.Series) != 0 {
		t.Errorf("Series = %+v, want none", got.Series)
	}
	if got.Range != "24h" {
		t.Errorf("Range = %q, want the normalized 24h the query path would echo", got.Range)
	}
}

func getNodes(t *testing.T, scope Scope) *pkgopencost.NodeCostResponse {
	t.Helper()
	w := httptest.NewRecorder()
	handleNodes(w, httptest.NewRequest(http.MethodGet, "/opencost/nodes", nil), func() string { return "USD" }, fixedScope(scope))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
	}
	var body pkgopencost.NodeCostResponse
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (body %s)", err, w.Body.String())
	}
	return &body
}

// Node cost is cluster-scoped — there is no per-namespace slice of it to show,
// so a user without `list nodes` gets nothing rather than a partial figure.
func TestNodesDeniedWithoutNodeReadAccess(t *testing.T) {
	startFakeProm(t)

	got := getNodes(t, Scope{Namespaces: []string{"team-a"}, CanReadNodes: false})
	if got.Available {
		t.Error("Available = true, want false")
	}
	if got.Reason != pkgopencost.ReasonAccessDenied {
		t.Errorf("Reason = %q, want %q", got.Reason, pkgopencost.ReasonAccessDenied)
	}
	if len(got.Nodes) != 0 {
		t.Errorf("Nodes = %+v, want none", got.Nodes)
	}
}

func TestNodesServedWithNodeReadAccess(t *testing.T) {
	startFakeProm(t)

	got := getNodes(t, Scope{Namespaces: []string{"team-a"}, CanReadNodes: true})
	if got.Reason == pkgopencost.ReasonAccessDenied {
		t.Error("a caller with list-nodes must not be denied")
	}
	if !got.Available || len(got.Nodes) == 0 {
		t.Errorf("want node costs, got %+v", got)
	}
}

func TestNodesUnrestrictedIsUnchanged(t *testing.T) {
	startFakeProm(t)

	got := getNodes(t, unrestricted)
	if !got.Available || len(got.Nodes) == 0 {
		t.Errorf("unrestricted caller lost node costs: %+v", got)
	}
}
