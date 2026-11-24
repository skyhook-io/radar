package openobserve

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/prometheus/prometheus/promql/parser"
)

var clusterOpts = RewriteOptions{Matchers: []Matcher{{Name: "cluster", Value: "oo-b"}}}

func TestRewriteKeepsClusterLabelThroughAggregation(t *testing.T) {
	tests := []struct{ in, want string }{
		{`sum(rate(x[5m]))`, `sum by (cluster) (rate(x{cluster="oo-b"}[5m]))`},
		{`sum by (pod) (x)`, `sum by (pod, cluster) (x{cluster="oo-b"})`},
		{`sum by (Cluster, pod) (x)`, `sum by (cluster, pod) (x{cluster="oo-b"})`},
		{`sum without (cluster, pod) (x)`, `sum without (pod) (x{cluster="oo-b"})`},
		{`count(x)`, `count by (cluster) (x{cluster="oo-b"})`},
		{`quantile(0.9, x)`, `quantile by (cluster) (0.9, x{cluster="oo-b"})`},
		{`bottomk(3, sum by (pod) (x))`, `bottomk by (cluster) (3, sum by (pod, cluster) (x{cluster="oo-b"}))`},
		{`count_values("v", x)`, `count_values by (cluster) ("v", x{cluster="oo-b"})`},
		{`a * on () group_left (cluster, node) b`, `a{cluster="oo-b"} * on (cluster) group_left (node) b{cluster="oo-b"}`},
		{`a + ignoring (cluster, pod) b`, `a{cluster="oo-b"} + ignoring (pod) b{cluster="oo-b"}`},
		{`x or vector(0)`, `x{cluster="oo-b"} or label_replace(vector(0), "cluster", "oo-b", "", "")`},
		{`sum(x) or on () vector(0)`, `sum by (cluster) (x{cluster="oo-b"}) or on (cluster) label_replace(vector(0), "cluster", "oo-b", "", "")`},
		{`scalar(sum(x))`, `scalar(sum by (cluster) (x{cluster="oo-b"}))`},
		{`absent(x)`, `absent(x{cluster="oo-b"})`},
		{`sum(x) / scalar(sum(y))`, `sum by (cluster) (x{cluster="oo-b"}) / scalar(sum by (cluster) (y{cluster="oo-b"}))`},
	}
	for _, tc := range tests {
		got, err := Rewrite(tc.in, clusterOpts)
		if err != nil {
			t.Fatalf("Rewrite(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("Rewrite(%q)\n got  %s\n want %s", tc.in, got, tc.want)
		}
	}
}

func TestRewriteVectorValueWithDollar(t *testing.T) {
	got, err := Rewrite(`vector(1)`, RewriteOptions{Matchers: []Matcher{{Name: "cluster", Value: "a$1"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got != `label_replace(vector(1), "cluster", "a$$1", "", "")` {
		t.Fatalf("got %s", got)
	}
}

// fakeStore answers every query with a fixed body, recording what was sent.
type fakeStore struct {
	body    string
	queries []string
}

func (f *fakeStore) Do(_ context.Context, _, _ string, params url.Values) ([]byte, error) {
	f.queries = append(f.queries, params.Get("query"))
	return []byte(f.body), nil
}

func (f *fakeStore) Address() string { return "fake" }

func resultLabels(t *testing.T, body []byte) []map[string]string {
	t.Helper()
	var resp struct {
		Data struct {
			Result []struct {
				Metric map[string]string `json:"metric"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}
	out := make([]map[string]string, 0, len(resp.Data.Result))
	for _, r := range resp.Data.Result {
		out = append(out, r.Metric)
	}
	return out
}

// An aggregated answer keeps the cluster label, so the filter keeps it.
func TestAggregatedAnswerPassesFilter(t *testing.T) {
	store := &fakeStore{body: `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"cluster":"oo-b"},"value":[1,"0.25"]}]}}`}
	rt := &RewritingTransport{Inner: store, Options: clusterOpts}
	body, err := rt.Do(context.Background(), "GET", "/api/v1/query", url.Values{"query": {`sum(rate(x[5m]))`}})
	if err != nil {
		t.Fatal(err)
	}
	if store.queries[0] != `sum by (cluster) (rate(x{cluster="oo-b"}[5m]))` {
		t.Fatalf("sent %s", store.queries[0])
	}
	got := resultLabels(t, body)
	if len(got) != 1 || got[0]["cluster"] != "oo-b" {
		t.Fatalf("aggregated answer dropped: %s", body)
	}
}

// A store that ignored the matcher (OpenObserve < v1.2 on a label the
// metric lacks) answers for every cluster; only ours survives.
func TestStoreIgnoringFilterIsCaughtByFilter(t *testing.T) {
	store := &fakeStore{body: `{"status":"success","data":{"resultType":"vector","result":[` +
		`{"metric":{"cluster":"oo-b","pod":"web"},"value":[1,"1"]},` +
		`{"metric":{"cluster":"cluster1","pod":"web"},"value":[1,"7"]},` +
		`{"metric":{"pod":"orphan"},"value":[1,"3"]}]}}`}
	dropped := 0
	rt := &RewritingTransport{Inner: store, Options: clusterOpts, OnDropped: func(n int) { dropped += n }}
	body, err := rt.Do(context.Background(), "GET", "/api/v1/query", url.Values{"query": {`sum by (cluster, pod) (x)`}})
	if err != nil {
		t.Fatal(err)
	}
	got := resultLabels(t, body)
	if len(got) != 1 || got[0]["cluster"] != "oo-b" || dropped != 2 {
		t.Fatalf("kept %v, dropped %d", got, dropped)
	}
}

// Every aggregation without `without` in a rewritten query groups by the
// forced labels, for a realistic spread of Radar queries.
func TestRewriteEveryAggregationKeepsForcedLabels(t *testing.T) {
	queries := []string{
		`sum(rate(container_cpu_usage_seconds_total{container!='',namespace='pay-ments',pod='api.x-1'}[5m])) by (pod,namespace)`,
		`sum by (pod,namespace) (max by (pod,namespace,container) (container_memory_working_set_bytes{container!='',namespace='pay-ments'}))`,
		`sum(rate(container_cpu_cfs_throttled_periods_total[5m])) by (pod) / sum(rate(container_cpu_cfs_periods_total[5m])) by (pod)`,
		`topk(5, sum by (namespace) (rate(container_cpu_usage_seconds_total[5m])))`,
		`label_replace(max by (namespace,pod,owner_name) (kube_pod_owner{owner_kind="ReplicaSet"}), "replicaset", "$1", "owner_name", "(.*)") * on (namespace,replicaset) group_left() max by (namespace,replicaset) (kube_replicaset_owner{owner_kind="Deployment"})`,
		`sum(round(increase(kube_pod_container_status_restarts_total{namespace='a'}[1h]))) or vector(0)`,
	}
	for _, q := range queries {
		out, err := Rewrite(q, clusterOpts)
		if err != nil {
			t.Fatalf("Rewrite(%q): %v", q, err)
		}
		expr, err := parser.ParseExpr(out)
		if err != nil {
			t.Fatal(err)
		}
		parser.Inspect(expr, func(node parser.Node, path []parser.Node) error {
			switch n := node.(type) {
			case *parser.AggregateExpr:
				if !n.Without && !contains(n.Grouping, "cluster") {
					t.Errorf("%s: aggregation %s drops the cluster label", out, n)
				}
			case *parser.BinaryExpr:
				if vm := n.VectorMatching; vm != nil && vm.On && !contains(vm.MatchingLabels, "cluster") {
					t.Errorf("%s: on() without the cluster label in %s", out, n)
				}
			case *parser.Call:
				if n.Func.Name == "vector" {
					parent, ok := path[len(path)-1].(*parser.Call)
					if !ok || parent.Func.Name != "label_replace" {
						t.Errorf("%s: vector() left unlabelled", out)
					}
				}
			}
			return nil
		})
		if strings.Contains(q, "replicaset") && !strings.Contains(out, "on (namespace, replicaset, cluster)") {
			t.Errorf("join lost: %s", out)
		}
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
