package openobserve

import (
	"strings"
	"testing"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/parser"
)

var uidOpts = RewriteOptions{Matchers: []Matcher{{Name: "radar_cluster_uid", Value: "u-1"}}}

func TestRewrite(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"simple selector", `up`, `up{radar_cluster_uid="u-1"}`},
		{"selector with matchers", `kube_pod_info{namespace="a"}`, `kube_pod_info{namespace="a",radar_cluster_uid="u-1"}`},
		{"rate over range", `rate(x[5m])`, `rate(x{radar_cluster_uid="u-1"}[5m])`},
		{"sum by uppercase label", `sum by (Namespace) (rate(x{Pod="p"}[5m]))`, `sum by (namespace, radar_cluster_uid) (rate(x{pod="p",radar_cluster_uid="u-1"}[5m]))`},
		{"topk", `topk(5, x)`, `topk by (radar_cluster_uid) (5, x{radar_cluster_uid="u-1"})`},
		{"histogram_quantile", `histogram_quantile(0.9, sum by (le) (rate(x_bucket[5m])))`, `histogram_quantile(0.9, sum by (le, radar_cluster_uid) (rate(x_bucket{radar_cluster_uid="u-1"}[5m])))`},
		{"subquery", `max_over_time(rate(x[1m])[10m:1m])`, `max_over_time(rate(x{radar_cluster_uid="u-1"}[1m])[10m:1m])`},
		{"offset", `x offset 5m`, `x{radar_cluster_uid="u-1"} offset 5m`},
		{"at modifier", `x @ 1700000000`, `x{radar_cluster_uid="u-1"} @ 1700000000.000`},
		{"at end with range", `rate(x[5m] @ end())`, `rate(x{radar_cluster_uid="u-1"}[5m] @ end())`},
		{"group_left join", `a / on (Pod) group_left (Node) b`, `a{radar_cluster_uid="u-1"} / on (pod, radar_cluster_uid) group_left (node) b{radar_cluster_uid="u-1"}`},
		{"ignoring", `a + ignoring (Container) b`, `a{radar_cluster_uid="u-1"} + ignoring (container) b{radar_cluster_uid="u-1"}`},
		{"or", `x or y`, `x{radar_cluster_uid="u-1"} or y{radar_cluster_uid="u-1"}`},
		{"unless", `x unless y`, `x{radar_cluster_uid="u-1"} unless y{radar_cluster_uid="u-1"}`},
		{"any metric name", `{__name__=~".+"}`, `{__name__=~".+",radar_cluster_uid="u-1"}`},
		{"name matcher uppercase", `{__name__="X"}`, `{__name__="x",radar_cluster_uid="u-1"}`},
		{"name regex keeps escapes", `{__name__=~"Node_\\S+"}`, `{__name__=~"node_\\S+",radar_cluster_uid="u-1"}`},
		{"absent", `absent(x)`, `absent(x{radar_cluster_uid="u-1"})`},
		{"label_replace", `label_replace(x, "Dst", "$1", "SRC", "(.*)")`, `label_replace(x{radar_cluster_uid="u-1"}, "dst", "$1", "src", "(.*)")`},
		{"label_join", `label_join(x, "Dst", ",", "A", "B")`, `label_join(x{radar_cluster_uid="u-1"}, "dst", ",", "a", "b")`},
		{"count_values", `count_values("Value", x)`, `count_values by (radar_cluster_uid) ("value", x{radar_cluster_uid="u-1"})`},
		{"without", `sum without (Instance) (x)`, `sum without (instance) (x{radar_cluster_uid="u-1"})`},
		{"no selectors", `vector(1)`, `label_replace(vector(1), "radar_cluster_uid", "u-1", "", "")`},
		{"scalar arithmetic", `1 + 2`, `1 + 2`},
		// The caller's matchers on a forced label stay beside the forced one,
		// which narrows them: never wider than this cluster.
		{"override equality", `x{cluster="other",radar_cluster_uid="evil"}`, `x{cluster="other",radar_cluster_uid="evil",radar_cluster_uid="u-1"}`},
		{"override regex", `x{radar_cluster_uid=~".*"}`, `x{radar_cluster_uid="u-1",radar_cluster_uid=~".*"}`},
		{"override negative", `x{radar_cluster_uid!="u-1"}`, `x{radar_cluster_uid!="u-1",radar_cluster_uid="u-1"}`},
		{"override uppercase label", `x{RADAR_CLUSTER_UID="evil"}`, `x{radar_cluster_uid="evil",radar_cluster_uid="u-1"}`},
		{"same as forced", `x{radar_cluster_uid="u-1"}`, `x{radar_cluster_uid="u-1"}`},
		{"uppercase metric", `node_memory_MemTotal_bytes`, `node_memory_memtotal_bytes{radar_cluster_uid="u-1"}`},
		{"nested binary and paren", `(a - b) / scalar(c)`, `(a{radar_cluster_uid="u-1"} - b{radar_cluster_uid="u-1"}) / scalar(c{radar_cluster_uid="u-1"})`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Rewrite(tc.in, uidOpts)
			if err != nil {
				t.Fatalf("Rewrite(%q): %v", tc.in, err)
			}
			if got != tc.want {
				t.Fatalf("Rewrite(%q)\n got  %s\n want %s", tc.in, got, tc.want)
			}
		})
	}
}

// TestRewriteClusterLabelOverrides: the caller's own matchers on the cluster
// label are kept beside the forced one, so they can only narrow the answer:
// {cluster!="oo-b"} answers nothing rather than this cluster's series.
func TestRewriteClusterLabelOverrides(t *testing.T) {
	opts := RewriteOptions{Matchers: []Matcher{{Name: "cluster", Value: "oo-b"}}}
	for in, kept := range map[string]string{
		`x{cluster="other"}`:                           `cluster="other"`,
		`x{cluster=~".*"}`:                             `cluster=~".*"`,
		`x{cluster!="me"}`:                             `cluster!="me"`,
		`x{cluster!~"oo-b"}`:                           `cluster!~"oo-b"`,
		`x{Cluster="other"}`:                           `cluster="other"`,
		`rate(x{cluster=~".+"}[5m]) or on() vector(0)`: `cluster=~".+"`,
	} {
		got, err := Rewrite(in, opts)
		if err != nil {
			t.Fatalf("Rewrite(%q): %v", in, err)
		}
		assertEverySelectorScoped(t, got, "cluster", "oo-b")
		if !strings.Contains(got, kept) {
			t.Fatalf("Rewrite(%q) = %s, dropped the caller's %s", in, got, kept)
		}
	}
}

func TestRewriteEverySelectorScoped(t *testing.T) {
	opts := RewriteOptions{Matchers: []Matcher{{Name: "cluster", Value: "oo-b"}, {Name: "Radar_Cluster_UID", Value: "u-1"}}}
	for _, in := range []string{
		`sum(rate(container_cpu_usage_seconds_total{container!=""}[5m])) by (pod) / on(pod) group_left kube_pod_info`,
		`max_over_time(rate(x[1m] offset 1h)[30m:5m] @ 1700000000) unless absent_over_time(y[1h])`,
		`label_replace(sum by (Pod) (x), "p", "$1", "Pod", "(.*)") or {__name__=~"kube_.+",namespace="ns"}`,
		`quantile_over_time(0.99, {__name__="Http_Requests"}[1h:])`,
	} {
		got, err := Rewrite(in, opts)
		if err != nil {
			t.Fatalf("Rewrite(%q): %v", in, err)
		}
		assertEverySelectorScoped(t, got, "cluster", "oo-b")
		assertEverySelectorScoped(t, got, "radar_cluster_uid", "u-1")
		if got != strings.ToLower(got) && !strings.Contains(got, "$1") {
			t.Fatalf("Rewrite(%q) left upper case: %s", in, got)
		}
	}
}

func TestRewriteErrors(t *testing.T) {
	tests := []struct {
		name string
		q    string
		opts RewriteOptions
	}{
		{"unparseable", `sum(rate(x[5m]`, uidOpts},
		{"empty", ``, uidOpts},
		{"no matchers", `up`, RewriteOptions{}},
		{"empty matcher value", `up`, RewriteOptions{Matchers: []Matcher{{Name: "cluster"}}}},
		{"metric name matcher", `up`, RewriteOptions{Matchers: []Matcher{{Name: "__name__", Value: "x"}}}},
		{"duplicate matcher", `up`, RewriteOptions{Matchers: []Matcher{{Name: "cluster", Value: "a"}, {Name: "Cluster", Value: "b"}}}},
		{"unknown dialect", `up`, RewriteOptions{Matchers: uidOpts.Matchers, Dialect: "datadog"}},
		{"experimental function", `info(up)`, uidOpts},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got, err := Rewrite(tc.q, tc.opts); err == nil {
				t.Fatalf("Rewrite(%q) = %q, want error", tc.q, got)
			}
		})
	}
}

func TestRewriteSeriesSelector(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{`up`, `up{radar_cluster_uid="u-1"}`},
		{`{__name__=~"Kube_.+",Namespace="a"}`, `{__name__=~"kube_.+",namespace="a",radar_cluster_uid="u-1"}`},
		{`x{radar_cluster_uid=~".*"}`, `x{radar_cluster_uid="u-1",radar_cluster_uid=~".*"}`},
	}
	for _, tc := range tests {
		got, err := RewriteSeriesSelector(tc.in, uidOpts)
		if err != nil || got != tc.want {
			t.Fatalf("RewriteSeriesSelector(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
		}
	}
	for _, bad := range []string{`rate(x[5m])`, `x[5m]`, `x or y`, `x offset 5m`, `{`} {
		if got, err := RewriteSeriesSelector(bad, uidOpts); err == nil {
			t.Fatalf("RewriteSeriesSelector(%q) = %q, want error", bad, got)
		}
	}
	if _, err := RewriteSeriesSelector(`up`, RewriteOptions{}); err == nil {
		t.Fatal("empty matchers accepted")
	}
}

func TestSelectorForLabelValues(t *testing.T) {
	got := SelectorForLabelValues([]Matcher{{Name: "Cluster", Value: `o"o`}, {Name: "radar_cluster_uid", Value: "u"}})
	if got != `{cluster="o\"o",radar_cluster_uid="u"}` {
		t.Fatalf("got %s", got)
	}
	if _, err := parser.ParseMetricSelector(got); err != nil {
		t.Fatalf("selector does not parse: %v", err)
	}
}

func assertEverySelectorScoped(t *testing.T, q, name, value string) {
	t.Helper()
	expr, err := parser.ParseExpr(q)
	if err != nil {
		t.Fatalf("output %q does not parse: %v", q, err)
	}
	selectors := 0
	parser.Inspect(expr, func(node parser.Node, _ []parser.Node) error {
		vs, ok := node.(*parser.VectorSelector)
		if !ok {
			return nil
		}
		selectors++
		forced := 0
		for _, m := range vs.LabelMatchers {
			if m.Name == name && m.Type == labels.MatchEqual && m.Value == value {
				forced++
			}
		}
		if forced != 1 {
			t.Errorf("selector %s in %q: want one %s=%q matcher, have %d", vs, q, name, value, forced)
		}
		return nil
	})
	if selectors == 0 {
		t.Fatalf("no selectors in %q", q)
	}
}
