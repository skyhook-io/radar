package opencost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/pkg/prom"
)

func TestComputeApplicationCostTrendFromProm_BatchesByNamespaceAndReportsPartialCoverage(t *testing.T) {
	var queries []string
	client := applicationTrendProm(t, func(q string) string {
		queries = append(queries, q)
		return appMatrixBody([]appTrendSeries{
			{"Deployment", "api", []dpoint{{1700000000, 1}, {1700003600, 2}}},
			{"StatefulSet", "db", []dpoint{{1700000000, 2}, {1700003600, 3}}},
		})
	})

	got := ComputeApplicationCostTrendFromProm(context.Background(), client, ApplicationTrendOptions{
		Range: "24h",
		Workloads: []ApplicationWorkloadRef{
			{Namespace: "default", Kind: "Deployment", Name: "api"},
			{Namespace: "default", Kind: "StatefulSet", Name: "db"},
			{Namespace: "default", Kind: "DaemonSet", Name: "agent"},
			{Namespace: "default", Kind: "Job", Name: "import"},
		},
	})

	if len(queries) != 1 {
		t.Fatalf("expected one range query for one namespace, got %d", len(queries))
	}
	query := queries[0]
	if !strings.Contains(query, `kube_replicaset_owner{namespace="default", owner_kind="Deployment", owner_name=~"api", owner_is_controller="true"}`) {
		t.Fatalf("deployment selector missing from app trend query:\n%s", query)
	}
	if !strings.Contains(query, `kube_pod_owner{namespace="default", owner_kind="StatefulSet", owner_name=~"db", owner_is_controller="true"}`) {
		t.Fatalf("statefulset selector missing from app trend query:\n%s", query)
	}
	if !strings.Contains(query, `kube_pod_owner{namespace="default", owner_kind="DaemonSet", owner_name=~"agent", owner_is_controller="true"}`) {
		t.Fatalf("daemonset selector missing from app trend query:\n%s", query)
	}
	if !got.Available || !got.Partial {
		t.Fatalf("expected available partial trend response, got %+v", got)
	}
	if got.Coverage.Total != 4 || got.Coverage.Included != 2 {
		t.Fatalf("coverage = %+v, want total=4 included=2", got.Coverage)
	}
	if len(got.Coverage.Unavailable) != 1 || got.Coverage.Unavailable[0].Name != "agent" || got.Coverage.Unavailable[0].Reason != ReasonNoMetrics {
		t.Fatalf("unexpected unavailable coverage: %+v", got.Coverage.Unavailable)
	}
	if len(got.Coverage.Unsupported) != 1 || got.Coverage.Unsupported[0].Kind != "Job" {
		t.Fatalf("unexpected unsupported coverage: %+v", got.Coverage.Unsupported)
	}
	if got.WindowTotalCost != 5 {
		t.Fatalf("WindowTotalCost = %v, want 5", got.WindowTotalCost)
	}
	if len(got.DataPoints) != 2 || got.DataPoints[0].Value != 3 || got.DataPoints[1].Value != 5 {
		t.Fatalf("merged datapoints = %+v, want values 3 and 5", got.DataPoints)
	}
	if len(got.Series) != 2 || got.Series[0].Name != "db" || got.Series[1].Name != "api" {
		t.Fatalf("series should be sorted by window total desc, got %+v", got.Series)
	}
}

func TestComputeApplicationCostTrendFromProm_PreservesConnectionFailureReason(t *testing.T) {
	ref := ApplicationWorkloadRef{Namespace: "default", Kind: "Deployment", Name: "api"}
	got := ComputeApplicationCostTrendFromProm(context.Background(), nil, ApplicationTrendOptions{
		Range:             "24h",
		Workloads:         []ApplicationWorkloadRef{ref},
		UnavailableReason: ReasonQueryError,
	})

	if got.Reason != ReasonQueryError || len(got.Coverage.Unavailable) != 1 || got.Coverage.Unavailable[0].Reason != ReasonQueryError {
		t.Fatalf("connection failure reason was not preserved: %+v", got)
	}
}

func TestPromRegexAlternationEscapesLabelAndRegexCharacters(t *testing.T) {
	value := `worker"name.test`
	want := prom.EscapeRegexMeta(prom.SanitizeLabelValue(value))
	if got := promRegexAlternation([]string{value, value}); got != want {
		t.Fatalf("promRegexAlternation() = %q, want %q", got, want)
	}
}

func applicationTrendProm(t *testing.T, bodyForQuery func(string) string) *prom.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(bodyForQuery(r.URL.Query().Get("query"))))
	}))
	t.Cleanup(srv.Close)
	return prom.NewClient(prom.NewHTTPTransport(srv.URL, "", nil))
}

type appTrendSeries struct {
	kind   string
	name   string
	points []dpoint
}

func appMatrixBody(series []appTrendSeries) string {
	type point = []interface{}
	type entry struct {
		Metric map[string]string `json:"metric"`
		Values []point           `json:"values"`
	}
	body := struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string  `json:"resultType"`
			Result     []entry `json:"result"`
		} `json:"data"`
	}{Status: "success"}
	body.Data.ResultType = "matrix"
	for _, s := range series {
		values := make([]point, 0, len(s.points))
		for _, p := range s.points {
			values = append(values, point{float64(p.ts), formatFloat(p.v)})
		}
		body.Data.Result = append(body.Data.Result, entry{
			Metric: map[string]string{"owner_kind": s.kind, "owner_name": s.name},
			Values: values,
		})
	}
	b, _ := json.Marshal(body)
	return string(b)
}
