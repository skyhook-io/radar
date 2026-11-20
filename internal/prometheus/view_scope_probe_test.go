package prometheus

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

// evaluatingStore answers scope probes from a list of series the way a store
// would evaluate them, so a split probe's answers can be compared with the
// unsplit one's.
type evaluatingStore struct {
	t       *testing.T
	series  []map[string]string
	mu      sync.Mutex
	queries []string
}

var evaluatedPart = regexp.MustCompile(`label_replace\((topk\(2, count by \(([^)]*)\) \(count_over_time\(|count by \(([^)]*)\) \(count_over_time\(|count\(count_over_time\()([a-zA-Z_:][a-zA-Z0-9_:]*)\{(.*?)\}\[\d+s\]\)+, "(radar_metric|radar_anchor)", "([^"]+)", "", ""\)`)

func (s *evaluatingStore) QueryAtPost(_ context.Context, query string, _ time.Time) (*prom.QueryResult, error) {
	s.mu.Lock()
	s.queries = append(s.queries, query)
	s.mu.Unlock()
	return &prom.QueryResult{Series: s.evaluate(query)}, nil
}

func (s *evaluatingStore) evaluate(query string) []prom.Series {
	s.t.Helper()
	parts := evaluatedPart.FindAllStringSubmatch(query, -1)
	whole := make([]string, len(parts))
	for i, p := range parts {
		whole[i] = p[0]
	}
	if strings.Join(whole, " or ") != query {
		s.t.Fatalf("store can't read the probe: %s", query)
	}
	var out []prom.Series
	seen := map[string]bool{}
	add := func(labels map[string]string) {
		if key := viewRowKey(labels); !seen[key] {
			seen[key] = true
			out = append(out, vec(labels, 1))
		}
	}
	for _, p := range parts {
		matchers := parseViewMatchers(p[5])
		var matched []map[string]string
		for _, series := range s.series {
			if series["__name__"] != p[4] {
				continue
			}
			ok := true
			for _, m := range matchers {
				if m.label == "" {
					s.t.Fatalf("store can't read the selector %q", p[5])
				}
				match, known := m.matches(series[m.label])
				ok = ok && match && known
			}
			if ok {
				matched = append(matched, series)
			}
		}
		if p[6] == viewScopeAnchorLabel {
			if len(matched) > 0 {
				add(map[string]string{viewScopeAnchorLabel: p[7]})
			}
			continue
		}
		keys := strings.Split(p[2]+p[3], ",")
		var groups []map[string]string
		groupSeen := map[string]bool{}
		for _, series := range matched {
			row := map[string]string{viewScopeMetricLabel: p[7]}
			for _, k := range keys {
				if v := series[k]; v != "" {
					row[k] = v
				}
			}
			if key := viewRowKey(row); !groupSeen[key] {
				groupSeen[key] = true
				groups = append(groups, row)
			}
		}
		slices.SortFunc(groups, func(a, b map[string]string) int { return strings.Compare(viewRowKey(a), viewRowKey(b)) })
		if strings.HasPrefix(p[1], "topk") && len(groups) > 2 {
			groups = groups[:2]
		}
		for _, row := range groups {
			add(row)
		}
	}
	return out
}

func podNames(n, length int) []string {
	names := make([]string, n)
	for i := range names {
		suffix := fmt.Sprintf("-%03d", i)
		names[i] = strings.Repeat("p", length-len(suffix)) + suffix
	}
	return names
}

// workloadChartRequest is a workload chart's check: its Pods in the subject,
// the first of them as anchors.
func workloadChartRequest(joined bool, pods []string, metrics ...string) ViewScopeRequest {
	subject := `namespace="shop",` + labelRegexMatcher("pod", pods)
	var ms []ViewMetric
	for _, name := range metrics {
		ms = append(ms, ViewMetric{Name: name, Subject: subject, AnchorLabel: "pod", AnchorValues: pods, AnchorNamespace: "shop"})
	}
	return viewRequest(joined, ms...)
}

var cadvisorMetrics = []string{"container_cpu_usage_seconds_total", "container_memory_working_set_bytes", "container_cpu_cfs_throttled_periods_total", "container_cpu_cfs_periods_total"}

func probeSeries(metric, pod string, labels ...string) map[string]string {
	s := map[string]string{"__name__": metric, "namespace": "shop", "pod": pod}
	for i := 0; i+1 < len(labels); i += 2 {
		s[labels[i]] = labels[i+1]
	}
	return s
}

func withProbeLimit(t *testing.T, limit int) {
	t.Helper()
	old := viewProbeMaxBytes
	viewProbeMaxBytes = limit
	t.Cleanup(func() { viewProbeMaxBytes = old })
}

// probeServer is a store behind HTTP that records how each probe was sent.
type probeServer struct {
	store   *evaluatingStore
	mu      sync.Mutex
	methods []string
	sizes   []int
}

func newProbeClient(t *testing.T, ps *probeServer) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("parse form: %v", err)
		}
		query := r.Form.Get("query")
		if query == "up" {
			_, _ = w.Write([]byte(authProbeBody))
			return
		}
		ps.mu.Lock()
		ps.methods = append(ps.methods, r.Method)
		ps.sizes = append(ps.sizes, len(query))
		ps.mu.Unlock()
		if r.Method == http.MethodPost && (r.URL.RawQuery != "" || r.PostForm.Get("time") == "") {
			t.Errorf("POST probe with URL query %q, form time %q", r.URL.RawQuery, r.PostForm.Get("time"))
		}
		_, _ = w.Write(attributionEvidence(ps.store.evaluate(query), ""))
	}))
	t.Cleanup(srv.Close)
	return &Client{manualURL: srv.URL, httpClient: srv.Client()}
}

func (ps *probeServer) check(t *testing.T) {
	t.Helper()
	if len(ps.methods) == 0 {
		t.Fatal("no probe sent")
	}
	for i, method := range ps.methods {
		if method != http.MethodPost {
			t.Errorf("probe %d sent by %s", i, method)
		}
		if ps.sizes[i] > prom.MaxWorkloadQueryBytes {
			t.Errorf("probe %d is %d bytes, over %d", i, ps.sizes[i], prom.MaxWorkloadQueryBytes)
		}
	}
	t.Logf("%d probes, bytes %v", len(ps.sizes), ps.sizes)
}

// A workload chart over 200 Pods with 50-character names, whose cAdvisor
// series lack the cluster label: every probe is POSTed and within the limit,
// and the anchored metrics are still accepted.
func TestViewScopeProbesBoundedForLargeWorkload(t *testing.T) {
	pods := podNames(200, 50)
	req := workloadChartRequest(false, pods, cadvisorMetrics...)
	var data []map[string]string
	for _, metric := range cadvisorMetrics {
		for _, pod := range pods {
			data = append(data, probeSeries(metric, pod))
		}
	}
	for _, tc := range []struct {
		name   string
		labels map[string]string
		want   ViewScopeState
	}{
		{"labeled", clusterA, ViewScopeUnlabeled},
		{"unlabeled", nil, ViewScopeUnlabeled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ps := &probeServer{store: &evaluatingStore{t: t, series: data}}
			c := newProbeClient(t, ps)
			scope, err := decideViewScope(context.Background(), c, req, time.Now(), tc.labels, false)
			if err != nil || scope.State != tc.want || scope.Withheld {
				t.Fatalf("scope = %+v err = %v", scope, err)
			}
			ps.check(t)
		})
	}
}

// Rightsizing with labeled kube-state-metrics and unlabeled cAdvisor: four
// pending metrics, each anchored on the same 50 long Pod names.
func TestViewScopeProbesBoundedForMixedLabels(t *testing.T) {
	pods := podNames(50, 80)
	var metrics []ViewMetric
	var data []map[string]string
	for _, name := range append([]string{"kube_pod_owner"}, cadvisorMetrics...) {
		metrics = append(metrics, ViewMetric{Name: name, Subject: `namespace="shop"`, AnchorLabel: "pod", AnchorValues: pods, AnchorNamespace: "shop"})
		for _, pod := range pods {
			if name == "kube_pod_owner" {
				data = append(data, probeSeries(name, pod, "cluster", "a"))
			} else {
				data = append(data, probeSeries(name, pod))
			}
		}
	}
	req := viewRequest(true, metrics...)
	if n := len(viewIdentityQuery(req.Metrics[1:], partitionLabels, req.Span, true)); n <= prom.MaxWorkloadQueryBytes {
		t.Fatalf("scenario too small to need a split: %d bytes", n)
	}
	ps := &probeServer{store: &evaluatingStore{t: t, series: data}}
	c := newProbeClient(t, ps)
	scope, err := decideViewScope(context.Background(), c, req, time.Now(), clusterA, false)
	if err != nil || scope.State != ViewScopeUnlabeled || scope.Withheld || scope.Filter("kube_pod_owner") != `cluster="a"` {
		t.Fatalf("scope = %+v err = %v", scope, err)
	}
	ps.check(t)
	if len(ps.sizes) < 3 {
		t.Errorf("want the identity check split over several probes, got %d probes", len(ps.sizes))
	}
}

// Splitting a probe, by metric or within one metric's Pod list and anchors,
// gives the decision the unsplit probe gives.
func TestViewScopeSplitProbesKeepDecisions(t *testing.T) {
	pods := podNames(200, 50)
	first, last := pods[0], pods[199]
	anchorLast := pods[49]
	m1, m2 := cadvisorMetrics[0], cadvisorMetrics[1]
	scenarios := map[string][]map[string]string{
		"labeled":                         {probeSeries(m1, first, "cluster", "a"), probeSeries(m2, last, "cluster", "a")},
		"identities in different chunks":  {probeSeries(m1, first, "cluster_name", "x"), probeSeries(m1, last, "cluster_name", "y"), probeSeries(m2, first)},
		"anchor only in the last chunk":   {probeSeries(m1, anchorLast), probeSeries(m2, anchorLast)},
		"no anchor among the series":      {probeSeries(m1, last), probeSeries(m2, last)},
		"metrics on different clusters":   {probeSeries(m1, first, "cluster_name", "x"), probeSeries(m2, last, "cluster_name", "y")},
		"another cluster's value":         {probeSeries(m1, first, "cluster", "b"), probeSeries(m2, last, "cluster", "a")},
		"readback over different chunks":  {probeSeries(m1, first, "cluster", "a"), probeSeries(m1, last, "cluster", "a", "region", "eu"), probeSeries(m2, first, "cluster", "a")},
		"foreign series beside our label": {probeSeries(m1, first, "cluster", "a"), probeSeries(m1, last, "cluster", "b")},
	}
	for name, data := range scenarios {
		for _, joined := range []bool{false, true} {
			for _, labels := range []map[string]string{clusterA, nil} {
				t.Run(fmt.Sprintf("%s/joined=%v/labels=%v", name, joined, labels), func(t *testing.T) {
					req := workloadChartRequest(joined, pods, m1, m2)
					decide := func(limit int) (ViewScope, int) {
						withProbeLimit(t, limit)
						store := &evaluatingStore{t: t, series: data}
						scope, err := decideViewScope(context.Background(), store, req, time.Now(), labels, true)
						if err != nil {
							t.Fatalf("limit %d: %v", limit, err)
						}
						for _, q := range store.queries {
							if len(q) > limit {
								t.Fatalf("limit %d: probe of %d bytes", limit, len(q))
							}
						}
						return scope, len(store.queries)
					}
					whole, wholeQueries := decide(1 << 30)
					split, splitQueries := decide(2500)
					if !reflect.DeepEqual(whole, split) {
						t.Fatalf("split decision differs\nwhole: %+v\nsplit: %+v", whole, split)
					}
					if splitQueries <= wholeQueries {
						t.Fatalf("limit forced no split: %d vs %d queries", splitQueries, wholeQueries)
					}
				})
			}
		}
	}
}

// A check that no split brings under the limit fails the decision; it is
// never sent in part.
func TestViewScopeUnsplittableProbeIsUnknown(t *testing.T) {
	long := ViewMetric{Name: "container_cpu_usage_seconds_total", Subject: `namespace="shop",pod=~"` + strings.Repeat("x", prom.MaxWorkloadQueryBytes) + `"`}
	for _, labels := range []map[string]string{clusterA, nil} {
		store := &evaluatingStore{t: t}
		scope, err := decideViewScope(context.Background(), store, viewRequest(false, long), time.Now(), labels, true)
		if scope.State != ViewScopeUnknown || !scope.Withheld || err == nil || len(store.queries) != 0 {
			t.Fatalf("labels=%v: scope = %+v err = %v queries = %d", labels, scope, err, len(store.queries))
		}
	}
}

func TestViewAnchorSubject(t *testing.T) {
	anchors := []string{"api-0", "api-1"}
	for _, tc := range []struct {
		name, subject, namespace, want string
	}{
		{"Pod list holding the anchors", `namespace="shop",pod=~"^(api\\-0|api\\-1|api\\-2)$"`, "shop", `namespace="shop",pod=~"api-0|api-1"`},
		{"Pod pattern the anchors match", `namespace="shop",pod=~"api-.*"`, "", `namespace="shop",pod=~"api-0|api-1"`},
		{"single Pod", `namespace="shop",pod="api-0"`, "shop", `pod="api-0",namespace="shop",pod=~"api-0|api-1"`},
		{"Pod list missing an anchor", `pod=~"api-1|api-2"`, "shop", `pod=~"api-1|api-2",namespace="shop",pod=~"api-0|api-1"`},
		{"other namespace", `namespace="pay"`, "shop", `namespace="pay",namespace="shop",pod=~"api-0|api-1"`},
		{"other labels", `namespace="shop",container!=""`, "shop", `container!="",namespace="shop",pod=~"api-0|api-1"`},
		{"unreadable subject", `namespace="shop" , pod=~api`, "shop", `namespace="shop" , pod=~api,namespace="shop",pod=~"api-0|api-1"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := ViewMetric{Name: "m", Subject: tc.subject, AnchorLabel: "pod", AnchorValues: anchors, AnchorNamespace: tc.namespace}
			parts := viewIdentityParts([]ViewMetric{m}, []string{"cluster"}, time.Hour, true)
			want := `label_replace(count(count_over_time(m{` + tc.want + `}[3600s])), "radar_anchor", "m", "", "")`
			if len(parts) != 2 || parts[1].String() != want {
				t.Fatalf("anchor check\n got: %s\nwant: %s", parts[len(parts)-1].String(), want)
			}
		})
	}
}

func TestRegexAlternatives(t *testing.T) {
	for _, tc := range []struct {
		re       string
		pre, suf string
		alts     []string
		ok       bool
	}{
		{re: `a|b|c`, alts: []string{"a", "b", "c"}, ok: true},
		{re: `^(a\.b|c\|d)$`, pre: "^(", suf: ")$", alts: []string{`a\.b`, `c\|d`}, ok: true},
		{re: `(?:a|b)`, pre: "(?:", suf: ")", alts: []string{"a", "b"}, ok: true},
		{re: `(a|b)(:[0-9]+)?`, alts: []string{`(a|b)(:[0-9]+)?`}, ok: true},
		{re: `[|]|x`, alts: []string{`[|]`, "x"}, ok: true},
		{re: `[]|]|x`, alts: []string{`[]|]`, "x"}, ok: true},
		{re: `api-.*`, alts: []string{"api-.*"}, ok: true},
		{re: `(?i)a|b`, ok: false},
		{re: `(a|b`, ok: false},
		{re: `a\`, ok: false},
	} {
		pre, alts, suf, ok := regexAlternatives(tc.re)
		if ok != tc.ok || (ok && (pre != tc.pre || suf != tc.suf || !slices.Equal(alts, tc.alts))) {
			t.Errorf("%s: got %q %q %q %v", tc.re, pre, alts, suf, ok)
		}
	}
}

func TestParseViewMatchers(t *testing.T) {
	got := parseViewMatchers(`namespace="a\"b", pod=~"^(x\\-1)$",container!=""`)
	want := []viewMatcher{
		{label: "namespace", op: "=", value: `a"b`, raw: `namespace="a\"b"`},
		{label: "pod", op: "=~", value: `^(x\-1)$`, raw: `pod=~"^(x\\-1)$"`},
		{label: "container", op: "!=", value: "", raw: `container!=""`},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
	for _, bad := range []string{`pod=~x`, `pod="a" pod="b"`, `{pod="a"}`} {
		if got := parseViewMatchers(bad); len(got) != 1 || got[0].raw != bad || got[0].label != "" {
			t.Errorf("%s: got %+v", bad, got)
		}
	}
	if parseViewMatchers("") != nil {
		t.Error("empty selector has matchers")
	}
	if q := strconv.Quote(`a"b`); parseViewMatchers("namespace=" + q)[0].value != `a"b` {
		t.Error("quoted value not decoded")
	}
}
