package prometheus

import (
	"context"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

const (
	evalSel     = `\{(.*?)\}\[\d+s\]`
	evalKeys    = `\(([^)]*)\)`
	evalName    = `([a-zA-Z_:][a-zA-Z0-9_:]*)`
	evalTagTail = `", "", ""\)`
)

// The reference set the evaluating store reads back: objects outside this
// cluster, as otherClusterObjects spells them.
const evalOther = `count by ` + evalKeys + ` \(count_over_time\(` + evalName + `\{\}\[\d+s\]\)\) unless on` + evalKeys + ` count by ` + evalKeys + ` \(count_over_time\(` + evalName + evalSel + `\)\)`

var (
	evalReferencePart = regexp.MustCompile(`^label_replace\(count\(count_over_time\(` + evalName + evalSel + `\)\), "radar_reference", "([^"]+)` + evalTagTail)
	evalOtherPart     = regexp.MustCompile(`^label_replace\(count\(` + evalOther + `\), "radar_other_cluster", "([^"]+)` + evalTagTail)
	evalForeignPart   = regexp.MustCompile(`^label_replace\(count\(count by ` + evalKeys + ` \(((?:label_replace\()*)count by ` + evalKeys + ` \(count_over_time\(` + evalName + evalSel + `\)\)((?:, "\w+", "\$1", "\w+", "\(\.\*\)"\))*)\) and on` + evalKeys + ` \(` + evalOther + `\)\), "radar_foreign", "([^"]+)` + evalTagTail)
	evalRename        = regexp.MustCompile(`, "(\w+)", "\$1", "(\w+)", "\(\.\*\)"\)`)
)

// otherObjects is what otherClusterObjects selects: keys of ref's series
// outside ours.
func (s *evaluatingStore) otherObjects(ref, ours string, keys []string) map[string]bool {
	tuple := func(series map[string]string) string {
		values := make([]string, len(keys))
		for i, k := range keys {
			values[i] = series[k]
		}
		return strings.Join(values, "\x00")
	}
	mine := map[string]bool{}
	for _, series := range s.selected(ref, ours) {
		mine[tuple(series)] = true
	}
	out := map[string]bool{}
	for _, series := range s.selected(ref, "") {
		if t := tuple(series); !mine[t] {
			out[t] = true
		}
	}
	return out
}

// evaluateForeign evaluates one foreign-check part at the start of rest and
// returns its length, or 0 when rest starts with none.
func (s *evaluatingStore) evaluateForeign(rest string, add func(map[string]string)) int {
	s.t.Helper()
	if p := evalReferencePart.FindStringSubmatch(rest); p != nil {
		if len(s.selected(p[1], p[2])) > 0 {
			add(map[string]string{viewScopeReferenceLabel: p[3]})
		}
		return len(p[0])
	}
	if p := evalOtherPart.FindStringSubmatch(rest); p != nil {
		if len(s.otherObjects(p[2], p[6], strings.Split(p[1], ","))) > 0 {
			add(map[string]string{viewScopeOtherLabel: p[7]})
		}
		return len(p[0])
	}
	p := evalForeignPart.FindStringSubmatch(rest)
	if p == nil {
		return 0
	}
	refKeys := strings.Split(p[1], ",")
	renamed := map[string]string{}
	for _, r := range evalRename.FindAllStringSubmatch(p[6], -1) {
		renamed[r[1]] = r[2]
	}
	if strings.Count(p[2], "label_replace(") != len(renamed) {
		s.t.Fatalf("store can't read the renames: %s", p[0])
	}
	other := s.otherObjects(p[9], p[13], refKeys)
	for _, series := range s.selected(p[4], p[5]) {
		values := make([]string, len(refKeys))
		for i, k := range refKeys {
			if from, ok := renamed[k]; ok {
				values[i] = series[from]
			} else {
				values[i] = series[k]
			}
		}
		if other[strings.Join(values, "\x00")] {
			add(map[string]string{viewScopeForeignLabel: p[14]})
			break
		}
	}
	return len(p[0])
}

func ksmPod(cluster map[string]string, namespace, pod string) map[string]string {
	s := map[string]string{"__name__": "kube_pod_info", "namespace": namespace, "pod": pod}
	for k, v := range cluster {
		s[k] = v
	}
	return s
}

func unlabeledSeries(metric, namespace, pod string, extra ...string) map[string]string {
	s := map[string]string{"__name__": metric, "namespace": namespace, "pod": pod}
	for i := 0; i+1 < len(extra); i += 2 {
		s[extra[i]] = extra[i+1]
	}
	return s
}

var (
	clusterB         = map[string]string{"cluster": "b"}
	foreignCPU       = ViewMetric{Name: "container_cpu_usage_seconds_total", Subject: `namespace="shop"`, AnchorLabel: "pod", AnchorValues: []string{"api-0"}, AnchorNamespace: "shop", Foreign: PodForeignCheck(false)}
	foreignAlloc     = ViewMetric{Name: "container_cpu_allocation", AnchorLabel: "pod", AnchorValues: []string{"api-0"}, Foreign: PodForeignCheck(true)}
	labeledOwner     = ViewMetric{Name: "kube_pod_owner", Subject: `namespace="shop"`, AnchorLabel: "pod", AnchorValues: []string{"api-0"}, AnchorNamespace: "shop"}
	foreignCheckTags = []string{viewScopeReferenceLabel, viewScopeOtherLabel, viewScopeForeignLabel}
)

func countForeignProbes(queries []string) (reference, foreign int) {
	for _, q := range queries {
		switch {
		case strings.Contains(q, `"`+viewScopeForeignLabel+`"`):
			foreign++
		case strings.Contains(q, `"`+viewScopeReferenceLabel+`"`):
			reference++
		}
	}
	return reference, foreign
}

func TestForeignCheckQueryShape(t *testing.T) {
	req := viewRequest(false, foreignAlloc, ViewMetric{Name: "node_total_hourly_cost", Foreign: NodeForeignCheck("node")})
	var got []string
	for _, p := range viewReferenceParts(req.Metrics, `cluster="a"`, time.Hour) {
		got = append(got, p.String())
	}
	podOther := `count by (namespace,pod) (count_over_time(kube_pod_info{}[3600s])) unless on(namespace,pod) count by (namespace,pod) (count_over_time(kube_pod_info{cluster="a"}[3600s]))`
	nodeOther := `count by (node) (count_over_time(kube_node_info{}[3600s])) unless on(node) count by (node) (count_over_time(kube_node_info{cluster="a"}[3600s]))`
	want := []string{
		`label_replace(count(count_over_time(kube_pod_info{cluster="a"}[3600s])), "radar_reference", "kube_pod_info/namespace,pod", "", "")`,
		`label_replace(count(` + podOther + `), "radar_other_cluster", "kube_pod_info/namespace,pod", "", "")`,
		`label_replace(count(count_over_time(kube_node_info{cluster="a"}[3600s])), "radar_reference", "kube_node_info/node", "", "")`,
		`label_replace(count(` + nodeOther + `), "radar_other_cluster", "kube_node_info/node", "", "")`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("reference parts\n got: %q\nwant: %q", got, want)
	}
	got = nil
	for _, p := range viewForeignParts(req.Metrics, `cluster="a"`, time.Hour) {
		got = append(got, p.String())
	}
	want = []string{
		`label_replace(count(count by (namespace,pod) (label_replace(count by (exported_namespace,pod) (count_over_time(container_cpu_allocation{exported_namespace!=""}[3600s])), "namespace", "$1", "exported_namespace", "(.*)")) and on(namespace,pod) (` + podOther + `)), "radar_foreign", "container_cpu_allocation", "", "")`,
		`label_replace(count(count by (namespace,pod) (count by (namespace,pod) (count_over_time(container_cpu_allocation{exported_namespace=""}[3600s]))) and on(namespace,pod) (` + podOther + `)), "radar_foreign", "container_cpu_allocation", "", "")`,
		`label_replace(count(count by (node) (count by (node) (count_over_time(node_total_hourly_cost{}[3600s]))) and on(node) (` + nodeOther + `)), "radar_foreign", "node_total_hourly_cost", "", "")`,
	}
	if !slices.Equal(got, want) {
		t.Fatalf("foreign parts\n got: %q\nwant: %q", got, want)
	}
	renamed := viewRequest(false, ViewMetric{Name: "pv_hourly_cost", Subject: `x="y"`, Foreign: PersistentVolumeForeignCheck("volumename")})
	parts := viewForeignParts(renamed.Metrics, `cluster="a"`, time.Hour)
	if len(parts) != 1 || !strings.HasPrefix(parts[0].String(), `label_replace(count(count by (persistentvolume) (label_replace(count by (volumename) (count_over_time(pv_hourly_cost{x="y"}[3600s])), "persistentvolume", "$1", "volumename", "(.*)")) and on(persistentvolume) (`) {
		t.Fatalf("renamed key: %v", parts)
	}
}

func TestForeignCheckDecisions(t *testing.T) {
	ours := ksmPod(clusterA, "shop", "api-0")
	theirs := ksmPod(clusterB, "shop", "web-0")
	cpu := foreignCPU.Name
	for _, tc := range []struct {
		name       string
		series     []map[string]string
		want       ViewScopeState
		note       []string
		references int
		foreign    int
	}{
		{
			name:   "another cluster's Pod among the series",
			series: []map[string]string{ours, theirs, unlabeledSeries(cpu, "shop", "api-0"), unlabeledSeries(cpu, "shop", "web-0")},
			want:   ViewScopeLabelMissing, note: []string{cpu, "another cluster", `cluster="a"`, "--prometheus-cluster-label"},
			references: 1, foreign: 1,
		},
		{
			name:   "only this cluster's Pods",
			series: []map[string]string{ours, theirs, unlabeledSeries(cpu, "shop", "api-0")},
			want:   ViewScopeUnlabeled, references: 1, foreign: 1,
		},
		{
			name:   "another cluster's Pod outside the subject",
			series: []map[string]string{ours, ksmPod(clusterB, "pay", "web-0"), unlabeledSeries(cpu, "shop", "api-0"), unlabeledSeries(cpu, "pay", "web-0")},
			want:   ViewScopeUnlabeled, references: 1, foreign: 1,
		},
		{
			name:   "same-named Pod in both clusters",
			series: []map[string]string{ours, ksmPod(clusterB, "shop", "api-0"), unlabeledSeries(cpu, "shop", "api-0")},
			want:   ViewScopeUnlabeled, references: 1,
		},
		{
			name:   "reference lists no other cluster",
			series: []map[string]string{ours, ksmPod(clusterA, "shop", "web-0"), unlabeledSeries(cpu, "shop", "api-0"), unlabeledSeries(cpu, "shop", "web-0")},
			want:   ViewScopeUnlabeled, references: 1,
		},
		{
			name:   "reference absent",
			series: []map[string]string{unlabeledSeries(cpu, "shop", "api-0")},
			want:   ViewScopeUnknown, note: []string{cpu, "kube_pod_info", `cluster="a"`}, references: 1,
		},
		{
			name:   "reference holds only another cluster",
			series: []map[string]string{theirs, unlabeledSeries(cpu, "shop", "api-0")},
			want:   ViewScopeUnknown, note: []string{"kube_pod_info"}, references: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &evaluatingStore{t: t, series: append(tc.series, probeSeries(labeledOwner.Name, "api-0", "cluster", "a"))}
			scope, err := decideViewScope(context.Background(), store, viewRequest(true, foreignCPU, labeledOwner), time.Now(), clusterA, false)
			if err != nil || scope.State != tc.want {
				t.Fatalf("scope = %+v, err = %v", scope, err)
			}
			for _, s := range tc.note {
				if !strings.Contains(scope.Note, s) {
					t.Errorf("note %q lacks %q", scope.Note, s)
				}
			}
			if tc.want == ViewScopeUnlabeled {
				if scope.Withheld || scope.Filter(cpu) != "" || scope.Filter(labeledOwner.Name) != `cluster="a"` {
					t.Errorf("accepted scope = %+v", scope)
				}
			} else if !scope.Withheld || len(scope.Filters) != 0 || strings.Contains(scope.Note, labeledOwner.Name) {
				t.Errorf("withheld scope = %+v", scope)
			}
			if tc.want == ViewScopeUnknown && viewScopeTTL(scope) != 0 {
				t.Error("an unchecked scope was cached")
			}
			references, foreign := countForeignProbes(store.queries)
			if references != tc.references || foreign != tc.foreign || len(store.queries) != 2+references+foreign {
				t.Errorf("probes: %d reference, %d foreign, %d total", references, foreign, len(store.queries))
			}
		})
	}
}

// A metric checked without the reference, or one whose labels carry this
// cluster, sends no foreign-check probe.
func TestForeignCheckOnlyForAnchoredUnlabeledMetrics(t *testing.T) {
	data := []map[string]string{ksmPod(clusterA, "shop", "api-0"), ksmPod(clusterB, "shop", "web-0"), unlabeledSeries(foreignCPU.Name, "shop", "web-0")}
	for _, tc := range []struct {
		name   string
		labels map[string]string
		series []map[string]string
	}{
		{"no labels known", nil, append(data, unlabeledSeries(foreignCPU.Name, "shop", "api-0"))},
		{"metric carries this cluster's label", clusterA, append(data[:2:2], probeSeries(foreignCPU.Name, "api-0", "cluster", "a"))},
		{"metric not anchored", clusterA, data},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &evaluatingStore{t: t, series: tc.series}
			if _, err := decideViewScope(context.Background(), store, viewRequest(false, foreignCPU), time.Now(), tc.labels, false); err != nil {
				t.Fatal(err)
			}
			for _, q := range store.queries {
				for _, tag := range foreignCheckTags {
					if strings.Contains(q, tag) {
						t.Fatalf("foreign-check probe sent: %s", q)
					}
				}
			}
		})
	}
}

// A series is this cluster's only when it carries every one of its labels.
func TestForeignCheckMultipleClusterLabels(t *testing.T) {
	labels := map[string]string{"cluster": "a", "region": "eu"}
	ours := ksmPod(labels, "shop", "api-0")
	sameClusterOtherRegion := ksmPod(map[string]string{"cluster": "a", "region": "us"}, "shop", "web-0")
	for _, tc := range []struct {
		name string
		pod  string
		want ViewScopeState
	}{
		{"object under one of our labels only", "web-0", ViewScopeLabelMissing},
		{"object under all our labels", "api-0", ViewScopeUnlabeled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &evaluatingStore{t: t, series: []map[string]string{ours, sameClusterOtherRegion, unlabeledSeries(foreignCPU.Name, "shop", "api-0"), unlabeledSeries(foreignCPU.Name, "shop", tc.pod)}}
			scope, err := decideViewScope(context.Background(), store, viewRequest(false, foreignCPU), time.Now(), labels, true)
			if err != nil || scope.State != tc.want {
				t.Fatalf("scope = %+v, err = %v", scope, err)
			}
		})
	}
	if got := viewReferenceParts([]ViewMetric{foreignCPU}, `cluster="a",region="eu"`, time.Hour)[1].String(); !strings.Contains(got, `unless on(namespace,pod) count by (namespace,pod) (count_over_time(kube_pod_info{cluster="a",region="eu"}[3600s]))`) {
		t.Fatalf("other-cluster objects must be the complement of all our labels: %s", got)
	}
}

// OpenCost's exporter may carry the Pod's namespace in exported_namespace,
// with namespace then naming the exporter's own; each spelling is checked.
func TestForeignCheckExportedNamespace(t *testing.T) {
	alloc := foreignAlloc.Name
	base := []map[string]string{ksmPod(clusterA, "shop", "api-0"), ksmPod(clusterB, "shop", "web-0"), ksmPod(clusterB, "opencost", "api-0")}
	for _, tc := range []struct {
		name   string
		series []map[string]string
		want   ViewScopeState
	}{
		{"foreign Pod in exported_namespace", []map[string]string{
			unlabeledSeries(alloc, "opencost", "api-0", "exported_namespace", "shop"),
			unlabeledSeries(alloc, "opencost-b", "web-0", "exported_namespace", "shop"),
		}, ViewScopeLabelMissing},
		{"foreign Pod in namespace", []map[string]string{
			unlabeledSeries(alloc, "shop", "api-0"),
			unlabeledSeries(alloc, "shop", "web-0"),
		}, ViewScopeLabelMissing},
		// namespace="opencost" with pod api-0 is another cluster's object
		// only if read as the Pod's namespace; exported_namespace says it's
		// this cluster's shop/api-0.
		{"exporter's namespace not read as the Pod's", []map[string]string{
			unlabeledSeries(alloc, "opencost", "api-0", "exported_namespace", "shop"),
		}, ViewScopeUnlabeled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &evaluatingStore{t: t, series: append(slices.Clone(base), tc.series...)}
			scope, err := decideViewScope(context.Background(), store, viewRequest(true, foreignAlloc), time.Now(), clusterA, true)
			if err != nil || scope.State != tc.want {
				t.Fatalf("scope = %+v, err = %v", scope, err)
			}
		})
	}
}

func TestForeignCheckIncompleteAnswerIsUnknown(t *testing.T) {
	data := []map[string]string{ksmPod(clusterA, "shop", "api-0"), ksmPod(clusterB, "shop", "web-0"), unlabeledSeries(foreignCPU.Name, "shop", "api-0")}
	for _, tag := range []string{viewScopeReferenceLabel, viewScopeForeignLabel} {
		t.Run(tag, func(t *testing.T) {
			store := &evaluatingStore{t: t, series: data}
			q := &fakeViewQuerier{answer: func(query string) (*prom.QueryResult, error) {
				res := &prom.QueryResult{Series: store.evaluate(query)}
				if strings.Contains(query, `"`+tag+`"`) {
					res.Annotations.Partial = true
				}
				return res, nil
			}}
			scope, err := decideViewScope(context.Background(), q, viewRequest(false, foreignCPU), time.Now(), clusterA, true)
			if scope.State != ViewScopeUnknown || !scope.Withheld || err == nil {
				t.Fatalf("scope = %+v, err = %v", scope, err)
			}
		})
	}
}

func TestForeignCheckNormalization(t *testing.T) {
	key := func(m ViewMetric) string {
		req, err := normalizeViewScopeRequest(ViewScopeRequest{View: "v", Metrics: []ViewMetric{m}})
		if err != nil {
			t.Fatal(err)
		}
		return viewScopeKey(1, 1, prom.WorkloadMetricsScope{}, "", false, req, true, time.Time{})
	}
	plain := foreignCPU
	plain.Foreign = ForeignCheck{}
	exported := foreignCPU
	exported.Foreign = PodForeignCheck(true)
	node := foreignCPU
	node.Foreign = NodeForeignCheck("node")
	keys := map[string]bool{key(foreignCPU): true, key(plain): true, key(exported): true, key(node): true}
	if len(keys) != 4 {
		t.Fatal("the foreign check must be part of the cache key")
	}
	reordered := foreignCPU
	reordered.Foreign.Keys = []ForeignKey{foreignCPU.Foreign.Keys[1], foreignCPU.Foreign.Keys[0]}
	if key(reordered) != key(foreignCPU) {
		t.Error("key order changed the cache key")
	}
	for name, f := range map[string]ForeignCheck{
		"no keys":                        {Reference: "kube_pod_info"},
		"bad reference":                  {Reference: "kube pod", Keys: []ForeignKey{{"pod", "pod"}}},
		"bad label":                      {Reference: "kube_pod_info", Keys: []ForeignKey{{"__name__", "pod"}}},
		"repeated label":                 {Reference: "kube_pod_info", Keys: []ForeignKey{{"pod", "pod"}, {"pod", "namespace"}}},
		"rename onto another key label":  {Reference: "kube_pod_info", Keys: []ForeignKey{{"pod", "namespace"}, {"namespace", "ns"}}},
		"exported without namespace key": {Reference: "kube_node_info", Keys: []ForeignKey{{"node", "node"}}, NamespaceExported: true},
		"exported_namespace as a key":    {Reference: "kube_pod_info", Keys: []ForeignKey{{"exported_namespace", "namespace"}}},
	} {
		m := foreignCPU
		m.Foreign = f
		if _, err := normalizeViewScopeRequest(ViewScopeRequest{View: "v", Metrics: []ViewMetric{m}}); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	m := foreignCPU
	m.Foreign = ForeignCheck{Keys: []ForeignKey{{"pod", "pod"}}}
	if req, err := normalizeViewScopeRequest(ViewScopeRequest{View: "v", Metrics: []ViewMetric{m}}); err != nil || !reflect.DeepEqual(req.Metrics[0].Foreign, ForeignCheck{}) {
		t.Errorf("keys without a reference kept: %+v %v", req.Metrics[0].Foreign, err)
	}
}

// Over a long Pod list, the foreign check splits like the other probes,
// every probe POSTed and within the limit, and decides as the batched check.
func TestForeignCheckSplitAndBounded(t *testing.T) {
	pods := podNames(200, 50)
	m := ViewMetric{Name: foreignCPU.Name, Subject: labelRegexMatcher("pod", pods), AnchorLabel: "pod", AnchorValues: pods, AnchorNamespace: "shop", Foreign: PodForeignCheck(true)}
	base := []map[string]string{ksmPod(clusterB, "shop", "elsewhere-0")}
	for _, pod := range pods {
		base = append(base, ksmPod(clusterA, "shop", pod))
	}
	for name, data := range map[string][]map[string]string{
		"foreign Pod in the last chunk": append(slices.Clone(base), unlabeledSeries(m.Name, "shop", pods[0]), unlabeledSeries(m.Name, "shop", pods[199]), ksmPod(clusterB, "shop", pods[199]+"x"), unlabeledSeries(m.Name, "opencost", pods[199]+"x", "exported_namespace", "shop")),
		"only ours":                     append(slices.Clone(base), unlabeledSeries(m.Name, "shop", pods[0]), unlabeledSeries(m.Name, "shop", pods[199])),
	} {
		t.Run(name, func(t *testing.T) {
			// The foreign Pod must fall inside the subject's Pod list.
			if strings.Contains(name, "foreign") {
				m.Subject = labelRegexMatcher("pod", append(slices.Clone(pods), pods[199]+"x"))
			}
			req := viewRequest(false, m)
			decide := func(limit int) (ViewScope, int) {
				withProbeLimit(t, limit)
				store := &evaluatingStore{t: t, series: data}
				scope, err := decideViewScope(context.Background(), store, req, time.Now(), clusterA, true)
				if err != nil {
					t.Fatalf("limit %d: %v", limit, err)
				}
				for _, q := range store.queries {
					if len(q) > limit {
						t.Fatalf("limit %d: probe of %d bytes", limit, len(q))
					}
				}
				_, foreign := countForeignProbes(store.queries)
				return scope, foreign
			}
			whole, wholeForeign := decide(1 << 30)
			split, splitForeign := decide(2500)
			if !reflect.DeepEqual(whole, split) {
				t.Fatalf("split decision differs\nwhole: %+v\nsplit: %+v", whole, split)
			}
			if splitForeign <= wholeForeign {
				t.Fatalf("limit forced no split of the foreign check: %d vs %d", splitForeign, wholeForeign)
			}
			want := ViewScopeUnlabeled
			if strings.Contains(name, "foreign") {
				want = ViewScopeLabelMissing
			}
			if whole.State != want {
				t.Fatalf("scope = %+v", whole)
			}

			ps := &probeServer{store: &evaluatingStore{t: t, series: data}}
			withProbeLimit(t, prom.MaxWorkloadQueryBytes)
			scope, err := decideViewScope(context.Background(), newProbeClient(t, ps), req, time.Now(), clusterA, true)
			if err != nil || scope.State != want {
				t.Fatalf("over HTTP: scope = %+v err = %v", scope, err)
			}
			ps.check(t)
		})
	}
}
