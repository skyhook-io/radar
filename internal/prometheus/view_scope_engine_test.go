//go:build promengine

// These tests run the cluster scope checks against a real Prometheus, because
// the unit tests' fake querier can't tell whether the probe PromQL parses or
// means what the decision code assumes. They need Docker and are opt-in:
//
//	make test-prom-engine
package prometheus

import (
	"context"
	"fmt"
	"maps"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

const engineImage = "prom/prometheus:v3.5.0"

const (
	engineNS     = "payments"
	engineAPIPod = "api-7d9f-k2x8p"
	engineDBPod  = "db-0"
	engineWorker = "worker-5c6d-q7r2m"
	engineMiB    = 1 << 20
)

var (
	engineUIDs = map[string]map[string]string{
		"a": {engineAPIPod: "aaaaaaaa-0000-4000-8000-000000000001", engineDBPod: "aaaaaaaa-0000-4000-8000-000000000002"},
		"b": {engineAPIPod: "bbbbbbbb-0000-4000-8000-000000000001", engineDBPod: "bbbbbbbb-0000-4000-8000-000000000002", engineWorker: "bbbbbbbb-0000-4000-8000-000000000003"},
	}
	engineMemory = map[string]map[string]float64{
		"a": {engineAPIPod: 100, engineDBPod: 200},
		"b": {engineAPIPod: 1000, engineDBPod: 2000, engineWorker: 4000},
	}
	engineCPU = map[string]map[string]float64{
		"a": {engineAPIPod: 0.25, engineDBPod: 0.5},
		"b": {engineAPIPod: 1, engineDBPod: 2, engineWorker: 4},
	}
	engineAPods = []string{engineAPIPod, engineDBPod}
)

type engineSeries struct {
	labels     map[string]string
	rate       float64 // per second, for counters
	value      float64 // for gauges
	start, end time.Time
}

type engineFamily struct {
	name    string // sample name
	counter bool
	series  []engineSeries
}

// engineData is two clusters with the same namespace and Pod names, plus the
// unlabeled and stale cases. Samples end shortly before now so instant reads
// (the UID proof) still see them within the default 5m lookback.
func engineData(end time.Time) []engineFamily {
	start := end.Add(-3 * time.Hour)
	pod := func(cluster, name string, extra map[string]string) map[string]string {
		l := map[string]string{"namespace": engineNS, "pod": name}
		if cluster != "" {
			l["cluster"] = cluster
		}
		maps.Copy(l, extra)
		return l
	}
	var cpu, mem, info, owner, network, restarts, fs, terminated []engineSeries
	for _, cluster := range []string{"a", "b"} {
		for _, name := range sortedKeys(engineUIDs[cluster]) {
			cpu = append(cpu, engineSeries{labels: pod(cluster, name, map[string]string{"container": "app"}), rate: engineCPU[cluster][name], start: start, end: end})
			mem = append(mem, engineSeries{labels: pod(cluster, name, map[string]string{"container": "app"}), value: engineMemory[cluster][name] * engineMiB, start: start, end: end})
			info = append(info, engineSeries{labels: pod(cluster, name, map[string]string{"uid": engineUIDs[cluster][name], "node": "ip-10-0-1-5"}), value: 1, start: start, end: end})
			ownerKind, ownerName := "ReplicaSet", strings.Join(strings.Split(name, "-")[:2], "-")
			if name == engineDBPod {
				ownerKind, ownerName = "StatefulSet", "db"
			}
			owner = append(owner, engineSeries{labels: pod(cluster, name, map[string]string{"owner_kind": ownerKind, "owner_name": ownerName}), value: 1, start: start, end: end})
			fs = append(fs, engineSeries{labels: pod("", name, map[string]string{"device": "/dev/" + cluster}), value: 1, start: start, end: end})
		}
	}
	for _, name := range engineAPods {
		network = append(network, engineSeries{labels: pod("", name, map[string]string{"interface": "eth0"}), rate: 1000, start: start, end: end})
	}
	restarts = append(restarts, engineSeries{labels: pod("", engineWorker, map[string]string{"container": "app"}), rate: 0.001, start: start, end: end})
	terminated = append(terminated,
		engineSeries{labels: pod("a", engineDBPod, map[string]string{"container": "app", "reason": "OOMKilled"}), value: 1, start: start, end: end},
		engineSeries{labels: pod("b", engineDBPod, map[string]string{"container": "app", "reason": "OOMKilled"}), value: 1, start: start, end: end.Add(-2 * time.Hour)},
	)
	// OpenCost-style allocations without a cluster label: two clusters'
	// exporters, each in its own namespace, renaming the Pod's namespace to
	// exported_namespace, for different Pods. And one cluster's alone.
	var allocBoth, allocOne []engineSeries
	for cluster, name := range map[string]string{"a": engineAPIPod, "b": engineWorker} {
		allocBoth = append(allocBoth, engineSeries{labels: map[string]string{"namespace": "opencost-" + cluster, "exported_namespace": engineNS, "pod": name, "container": "app"}, value: 1, start: start, end: end})
	}
	for _, name := range engineAPods {
		allocOne = append(allocOne, engineSeries{labels: map[string]string{"namespace": "opencost-a", "exported_namespace": engineNS, "pod": name, "container": "app"}, value: 1, start: start, end: end})
	}
	var nodes []engineSeries
	for _, cluster := range []string{"a", "b"} {
		nodes = append(nodes, engineSeries{labels: map[string]string{"cluster": cluster, "instance": "10.0.1.5:9100", "cpu": "0", "mode": "idle"}, rate: 0.9, start: start, end: end})
	}
	return []engineFamily{
		{name: "container_cpu_usage_seconds_total", counter: true, series: cpu},
		{name: "container_memory_working_set_bytes", series: mem},
		{name: "kube_pod_info", series: info},
		{name: "kube_pod_owner", series: owner},
		{name: "node_cpu_seconds_total", counter: true, series: nodes},
		{name: "container_network_receive_bytes_total", counter: true, series: network},
		{name: "kube_pod_container_status_restarts_total", counter: true, series: restarts},
		{name: "container_fs_usage_bytes", series: fs},
		{name: "kube_pod_container_status_last_terminated_reason", series: terminated},
		{name: "container_cpu_allocation", series: allocBoth},
		{name: "container_memory_allocation_bytes", series: allocOne},
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func writeOpenMetrics(path string, families []engineFamily) error {
	var b strings.Builder
	for _, f := range families {
		if f.counter {
			fmt.Fprintf(&b, "# TYPE %s counter\n", strings.TrimSuffix(f.name, "_total"))
		} else {
			fmt.Fprintf(&b, "# TYPE %s gauge\n", f.name)
		}
		for _, s := range f.series {
			var pairs []string
			for _, k := range sortedKeys(s.labels) {
				pairs = append(pairs, fmt.Sprintf("%s=%q", k, s.labels[k]))
			}
			labels := "{" + strings.Join(pairs, ",") + "}"
			for ts := s.start; !ts.After(s.end); ts = ts.Add(30 * time.Second) {
				v := s.value
				if f.counter {
					v = s.rate * ts.Sub(s.start).Seconds()
				}
				fmt.Fprintf(&b, "%s%s %g %d\n", f.name, labels, v, ts.Unix())
			}
		}
	}
	b.WriteString("# EOF\n")
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func dockerRun(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// startEnginePrometheus backfills the data into a fresh TSDB and serves it.
func startEnginePrometheus(t *testing.T, end time.Time) string {
	t.Helper()
	if os.Getenv("RADAR_PROMENGINE") == "" {
		t.Skip("set RADAR_PROMENGINE=1 (make test-prom-engine) to run the real-Prometheus tests")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		t.Skipf("docker is not available: %v", err)
	}
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	if err := os.Mkdir(data, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := writeOpenMetrics(filepath.Join(dir, "series.om"), engineData(end)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "prometheus.yml"), []byte("global: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	user := fmt.Sprintf("%d:%d", os.Getuid(), os.Getgid())
	dockerRun(t, "run", "--rm", "--user", user, "-v", dir+":/work", "--entrypoint", "promtool", engineImage,
		"tsdb", "create-blocks-from", "openmetrics", "/work/series.om", "/work/data")
	id := dockerRun(t, "run", "-d", "--user", user, "-p", "127.0.0.1:0:9090", "-v", dir+":/work", engineImage,
		"--config.file=/work/prometheus.yml", "--storage.tsdb.path=/work/data", "--storage.tsdb.retention.time=3650d")
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", id).Run() })
	hostPort := dockerRun(t, "port", id, "9090/tcp")
	url := "http://" + strings.Split(hostPort, "\n")[0]
	deadline := time.Now().Add(60 * time.Second)
	for {
		resp, err := http.Get(url + "/-/ready")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return url
			}
		}
		if time.Now().After(deadline) {
			logs, _ := exec.Command("docker", "logs", id).CombinedOutput()
			t.Fatalf("Prometheus did not become ready at %s: %v\n%s", url, err, logs)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

func engineClient(url string, configured map[string]string) *Client {
	c := &Client{manualURL: url + "/", httpClient: &http.Client{Timeout: 10 * time.Second}}
	if configured != nil {
		c.workloadScope = &prom.WorkloadMetricsScope{ClusterLabels: configured}
	}
	return c
}

func podMetric(name, subject string, anchors ...string) ViewMetric {
	return ViewMetric{Name: name, Subject: subject, AnchorLabel: "pod", AnchorValues: anchors}
}

func resolve(t *testing.T, c *Client, span time.Duration, joined bool, metrics ...ViewMetric) ViewScope {
	t.Helper()
	scope, err := c.ResolveViewScope(context.Background(), ViewScopeRequest{View: t.Name(), Metrics: metrics, Joined: joined, Span: span}, nil)
	if err != nil {
		t.Fatalf("ResolveViewScope: %v", err)
	}
	if scope.State == ViewScopeUnknown {
		t.Fatalf("check failed against the real engine (a probe query errored or returned an unexpected shape): %+v", scope)
	}
	return scope
}

func wantFilters(t *testing.T, scope ViewScope, want map[string]string) {
	t.Helper()
	if !maps.Equal(scope.Filters, want) {
		t.Errorf("filters = %v, want %v", scope.Filters, want)
	}
}

type countingQuerier struct {
	inner instantQuerier
	n     int
}

func (c *countingQuerier) QueryAtPost(ctx context.Context, query string, at time.Time) (*prom.QueryResult, error) {
	c.n++
	return c.inner.QueryAtPost(ctx, query, at)
}

func scalarAt(t *testing.T, c *Client, query string, at time.Time) float64 {
	t.Helper()
	res, err := requireComplete(c.QueryAt(context.Background(), query, at))
	if err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	if res == nil || len(res.Series) != 1 || len(res.Series[0].DataPoints) != 1 {
		t.Fatalf("%s: want one sample, got %+v", query, res)
	}
	return res.Series[0].DataPoints[0].Value
}

func TestEngineViewScope(t *testing.T) {
	end := time.Now().Truncate(30 * time.Second).Add(-90 * time.Second)
	url := startEnginePrometheus(t, end)
	configuredA := engineClient(url, map[string]string{"cluster": "a"})
	unconfigured := engineClient(url, nil)
	podsSubject := `namespace="` + engineNS + `"`

	cpu := podMetric("container_cpu_usage_seconds_total", podsSubject, engineAPods...)
	mem := podMetric("container_memory_working_set_bytes", podsSubject, engineAPods...)
	info := podMetric("kube_pod_info", podsSubject, engineAPods...)
	owner := podMetric("kube_pod_owner", podsSubject, engineAPods...)
	nodeCPU := ViewMetric{Name: "node_cpu_seconds_total", AnchorLabel: "instance", AnchorValues: []string{"10.0.1.5:9100"}}
	ownUnlabeled := podMetric("container_network_receive_bytes_total", podsSubject, engineAPods...)
	foreignUnlabeled := podMetric("kube_pod_container_status_restarts_total", podsSubject, engineAPods...)
	bothUnlabeled := podMetric("container_fs_usage_bytes", podsSubject, engineAPods...)
	terminated := podMetric("kube_pod_container_status_last_terminated_reason", podsSubject, engineAPods...)
	absent := podMetric("radar_engine_absent_total", podsSubject, engineAPods...)
	// A cluster-wide sum's metrics, guarded against other clusters' objects.
	allocBoth := ViewMetric{Name: "container_cpu_allocation", AnchorLabel: "pod", AnchorValues: engineAPods, Foreign: PodForeignCheck(true)}
	allocOne := ViewMetric{Name: "container_memory_allocation_bytes", AnchorLabel: "pod", AnchorValues: engineAPods, Foreign: PodForeignCheck(true)}
	ownGuarded := ownUnlabeled
	ownGuarded.Foreign = PodForeignCheck(false)
	bothGuarded := bothUnlabeled
	bothGuarded.Foreign = PodForeignCheck(false)

	t.Run("configured label scopes every labeled metric to cluster a", func(t *testing.T) {
		scope := resolve(t, configuredA, time.Hour, true, cpu, mem, info, owner, nodeCPU)
		if scope.State != ViewScopeConfigured || scope.Withheld {
			t.Fatalf("scope = %+v", scope)
		}
		wantFilters(t, scope, map[string]string{cpu.Name: `cluster="a"`, mem.Name: `cluster="a"`, info.Name: `cluster="a"`, owner.Name: `cluster="a"`, nodeCPU.Name: `cluster="a"`})

		memQuery := "sum(" + vectorSelector(mem.Name, withScope(mem.Subject, scope.Filter(mem.Name))) + ")"
		if got, want := scalarAt(t, configuredA, memQuery, end), 300.0*engineMiB; got != want {
			t.Errorf("%s = %g, want cluster a's %g", memQuery, got, want)
		}
		if got, want := scalarAt(t, configuredA, "sum("+vectorSelector(mem.Name, mem.Subject)+")", end), 7300.0*engineMiB; got != want {
			t.Errorf("unfiltered memory = %g, want both clusters' %g", got, want)
		}
		cpuQuery := "sum(rate(" + vectorSelector(cpu.Name, withScope(cpu.Subject, scope.Filter(cpu.Name))) + "[5m]))"
		if got := scalarAt(t, configuredA, cpuQuery, end); math.Abs(got-0.75) > 1e-6 {
			t.Errorf("%s = %g, want cluster a's 0.75", cpuQuery, got)
		}
	})

	t.Run("configured label on a foreign-only unlabeled metric is label_missing", func(t *testing.T) {
		scope := resolve(t, configuredA, time.Hour, false, foreignUnlabeled, owner)
		if scope.State != ViewScopeLabelMissing || !scope.Withheld || len(scope.Filters) != 0 {
			t.Fatalf("scope = %+v", scope)
		}
		if !strings.Contains(scope.Note, foreignUnlabeled.Name) || strings.Contains(scope.Note, owner.Name) {
			t.Errorf("note = %q", scope.Note)
		}
	})

	t.Run("configured label on an unlabeled metric holding our Pods is accepted unfiltered", func(t *testing.T) {
		scope := resolve(t, configuredA, time.Hour, false, ownUnlabeled, owner)
		if scope.State != ViewScopeUnlabeled || scope.Withheld {
			t.Fatalf("scope = %+v", scope)
		}
		wantFilters(t, scope, map[string]string{ownUnlabeled.Name: "", owner.Name: `cluster="a"`})
	})

	// Two clusters leaving the label off one scrape job read as one identity
	// holding our Pods; kube-state-metrics, labeled, shows cluster b's Pod
	// among them.
	t.Run("configured label on an unlabeled metric holding another cluster's Pods is label_missing", func(t *testing.T) {
		scope := resolve(t, configuredA, time.Hour, true, allocBoth, owner)
		if scope.State != ViewScopeLabelMissing || !scope.Withheld || len(scope.Filters) != 0 {
			t.Fatalf("scope = %+v", scope)
		}
		if !strings.Contains(scope.Note, allocBoth.Name) || !strings.Contains(scope.Note, "another cluster") || strings.Contains(scope.Note, owner.Name) {
			t.Errorf("note = %q", scope.Note)
		}
		if got := scalarAt(t, configuredA, "sum("+allocBoth.Name+")", end); got != 2 {
			t.Errorf("the withheld sum would add both clusters: %g", got)
		}
	})

	t.Run("configured label on one cluster's unlabeled metric passes the foreign check", func(t *testing.T) {
		for _, m := range []ViewMetric{allocOne, ownGuarded} {
			scope := resolve(t, configuredA, time.Hour, true, m, owner)
			if scope.State != ViewScopeUnlabeled || scope.Withheld {
				t.Fatalf("%s: scope = %+v", m.Name, scope)
			}
			wantFilters(t, scope, map[string]string{m.Name: "", owner.Name: `cluster="a"`})
		}
	})

	// Pods named alike in both clusters can't be told apart: the documented
	// limit of the check.
	t.Run("same-named Pods pass the foreign check", func(t *testing.T) {
		sameNamed := bothGuarded
		sameNamed.Subject = podsSubject + "," + labelRegexMatcher("pod", engineAPods)
		if scope := resolve(t, configuredA, time.Hour, false, sameNamed); scope.State != ViewScopeUnlabeled {
			t.Fatalf("scope = %+v", scope)
		}
		// Cluster b's worker Pod has no namesake here, so the whole
		// namespace's series are withheld.
		if scope := resolve(t, configuredA, time.Hour, false, bothGuarded); scope.State != ViewScopeLabelMissing {
			t.Fatalf("whole namespace: scope = %+v", scope)
		}
	})

	t.Run("foreign check against an absent reference is unknown", func(t *testing.T) {
		m := allocOne
		m.Foreign = NodeForeignCheck("container")
		scope, err := configuredA.ResolveViewScope(context.Background(), ViewScopeRequest{View: t.Name(), Metrics: []ViewMetric{m}, Span: time.Hour}, nil)
		if err != nil || scope.State != ViewScopeUnknown || !scope.Withheld || !strings.Contains(scope.Note, "kube_node_info") {
			t.Fatalf("scope = %+v, err = %v", scope, err)
		}
	})

	// The same checks split into many small probes (by metric, and within a
	// metric's Pod list and anchors) must parse and decide as the batched
	// probe does.
	t.Run("split probes decide as the batched probe does", func(t *testing.T) {
		pods := append([]string{engineAPIPod, engineDBPod}, podNames(60, 40)...)
		subject := `namespace="` + engineNS + `",` + labelRegexMatcher("pod", pods)
		workloadOwn := ViewMetric{Name: ownUnlabeled.Name, Subject: subject, AnchorLabel: "pod", AnchorValues: pods, AnchorNamespace: engineNS}
		workloadCPU := ViewMetric{Name: cpu.Name, Subject: subject, AnchorLabel: "pod", AnchorValues: pods, AnchorNamespace: engineNS}
		workloadGuarded := ViewMetric{Name: ownUnlabeled.Name, Subject: subject, AnchorLabel: "pod", AnchorValues: pods, AnchorNamespace: engineNS, Foreign: PodForeignCheck(false)}
		allocSubject := allocBoth
		allocSubject.Subject = labelRegexMatcher("pod", append([]string{engineWorker}, pods...))
		cases := [][]ViewMetric{{cpu, mem, info, owner, nodeCPU}, {ownUnlabeled, owner}, {foreignUnlabeled, owner}, {bothUnlabeled}, {workloadOwn, workloadCPU}, {workloadGuarded}, {allocSubject}, {allocOne, ownGuarded}}
		for _, labels := range []map[string]string{{"cluster": "a"}, nil} {
			for _, metrics := range cases {
				req, err := normalizeViewScopeRequest(ViewScopeRequest{View: t.Name(), Metrics: metrics, Joined: true, Span: time.Hour})
				if err != nil {
					t.Fatal(err)
				}
				at := time.Now()
				decideAt := func(limit int) (ViewScope, int) {
					withProbeLimit(t, limit)
					counted := &countingQuerier{inner: unconfigured}
					scope, err := decideViewScope(context.Background(), counted, req, at, labels, true)
					if err != nil || scope.State == ViewScopeUnknown {
						t.Fatalf("limit %d, labels %v: scope = %+v, err = %v", limit, labels, scope, err)
					}
					return scope, counted.n
				}
				whole, wholeProbes := decideAt(prom.MaxWorkloadQueryBytes)
				split, splitProbes := decideAt(700)
				if !maps.Equal(whole.Filters, split.Filters) || whole.State != split.State || whole.Note != split.Note {
					t.Errorf("labels %v, %s: split %+v, batched %+v", labels, metrics[0].Name, split, whole)
				}
				if (metrics[0].Subject == subject || metrics[0].Subject == allocSubject.Subject || len(metrics) == 5) && splitProbes <= wholeProbes {
					t.Errorf("labels %v, %s: no split (%d probes, batched %d)", labels, metrics[0].Name, splitProbes, wholeProbes)
				}
			}
		}
	})

	var proven map[string]string
	t.Run("UID proof finds each cluster's label from kube_pod_info", func(t *testing.T) {
		client := prom.NewClient(prom.NewHTTPTransport(url, "", &http.Client{Timeout: 10 * time.Second}))
		for cluster, uids := range engineUIDs {
			var anchors []prom.WorkloadPodIdentity
			for _, name := range engineAPods {
				anchors = append(anchors, prom.WorkloadPodIdentity{Name: name, UID: uids[name]})
			}
			proof, err := probeHistoricalPartition(context.Background(), client, engineNS, anchors)
			if err != nil {
				t.Fatalf("cluster %s: %v", cluster, err)
			}
			if want := map[string]string{"cluster": cluster}; !maps.Equal(proof.scope.ClusterLabels, want) || proof.unlabeled {
				t.Errorf("cluster %s: proof = %+v, want %v", cluster, proof, want)
			}
			if cluster == "a" {
				proven = proof.scope.ClusterLabels
			}
		}
	})

	t.Run("proven labels", func(t *testing.T) {
		if proven == nil {
			t.Skip("no proven labels")
		}
		decideProven := func(t *testing.T, metrics ...ViewMetric) ViewScope {
			t.Helper()
			req, err := normalizeViewScopeRequest(ViewScopeRequest{View: t.Name(), Metrics: metrics, Span: time.Hour})
			if err != nil {
				t.Fatal(err)
			}
			scope, err := decideViewScope(context.Background(), unconfigured, req, time.Now(), proven, false)
			if err != nil || scope.State == ViewScopeUnknown {
				t.Fatalf("scope = %+v, err = %v", scope, err)
			}
			return scope
		}
		t.Run("labeled metrics are verified", func(t *testing.T) {
			scope := decideProven(t, cpu, owner)
			if scope.State != ViewScopeVerified || scope.Withheld {
				t.Fatalf("scope = %+v", scope)
			}
			wantFilters(t, scope, map[string]string{cpu.Name: `cluster="a"`, owner.Name: `cluster="a"`})
		})
		t.Run("unlabeled metric holding our Pod names is accepted unfiltered", func(t *testing.T) {
			scope := decideProven(t, ownUnlabeled, owner)
			if scope.State != ViewScopeUnlabeled || scope.Withheld {
				t.Fatalf("scope = %+v", scope)
			}
			wantFilters(t, scope, map[string]string{ownUnlabeled.Name: "", owner.Name: `cluster="a"`})
		})
		t.Run("foreign-only unlabeled metric is label_missing", func(t *testing.T) {
			if scope := decideProven(t, foreignUnlabeled); scope.State != ViewScopeLabelMissing || !scope.Withheld {
				t.Fatalf("scope = %+v", scope)
			}
		})
	})

	t.Run("no labels and series from both clusters is several_clusters", func(t *testing.T) {
		scope := resolve(t, unconfigured, time.Hour, false, cpu)
		if scope.State != ViewScopeSeveralClusters || !scope.Withheld || len(scope.Filters) != 0 {
			t.Fatalf("scope = %+v", scope)
		}
	})

	t.Run("no labels and one identity is unlabeled", func(t *testing.T) {
		workerCPU := podMetric(cpu.Name, `namespace="`+engineNS+`",pod=~"worker-.*"`)
		for _, m := range []ViewMetric{workerCPU, ownUnlabeled} {
			scope := resolve(t, unconfigured, time.Hour, false, m)
			if scope.State != ViewScopeUnlabeled || scope.Withheld {
				t.Fatalf("%s: scope = %+v", m.Subject, scope)
			}
			wantFilters(t, scope, map[string]string{m.Name: ""})
		}
		clusterAInfo := podMetric(info.Name, `cluster="a"`)
		if scope := resolve(t, unconfigured, time.Hour, true, workerCPU, clusterAInfo); scope.State != ViewScopeSeveralClusters {
			t.Fatalf("joined metrics on different clusters: scope = %+v", scope)
		}
	})

	// Two clusters' unlabeled series have no label to tell them apart: they
	// read as one identity. This is the documented blind spot; the view is
	// shown but marked unverified.
	t.Run("two unlabeled clusters read as one identity", func(t *testing.T) {
		scope := resolve(t, unconfigured, time.Hour, false, bothUnlabeled)
		if scope.State != ViewScopeUnlabeled || scope.Withheld || scope.Note != viewScopeUnlabeledNote {
			t.Fatalf("scope = %+v", scope)
		}
	})

	t.Run("absent metric is not withheld", func(t *testing.T) {
		scope := resolve(t, configuredA, time.Hour, false, absent)
		if scope.State != ViewScopeConfigured || scope.Withheld {
			t.Fatalf("configured: scope = %+v", scope)
		}
		wantFilters(t, scope, map[string]string{absent.Name: `cluster="a"`})
		scope = resolve(t, unconfigured, time.Hour, false, absent)
		if scope.State != ViewScopeUnlabeled || scope.Withheld {
			t.Fatalf("unconfigured: scope = %+v", scope)
		}
		wantFilters(t, scope, map[string]string{absent.Name: ""})
	})

	// Cluster b's series stopped two hours ago: a 1h check misses it, a 3h one
	// must see it.
	t.Run("span covers a series that stopped reporting", func(t *testing.T) {
		if scope := resolve(t, unconfigured, time.Hour, false, terminated); scope.State != ViewScopeUnlabeled {
			t.Fatalf("1h: scope = %+v", scope)
		}
		if scope := resolve(t, unconfigured, 3*time.Hour, false, terminated); scope.State != ViewScopeSeveralClusters {
			t.Fatalf("3h: scope = %+v", scope)
		}
		if scope := resolve(t, configuredA, 3*time.Hour, false, terminated); scope.State != ViewScopeConfigured {
			t.Fatalf("configured a, 3h: scope = %+v", scope)
		}
		configuredB := engineClient(url, map[string]string{"cluster": "b"})
		if scope := resolve(t, configuredB, 3*time.Hour, false, terminated); scope.State != ViewScopeConfigured {
			t.Fatalf("configured b, 3h: scope = %+v", scope)
		}
		// Within 1h only cluster a's labeled series exist; per the design a
		// metric showing only another cluster's identity is not ours.
		if scope := resolve(t, configuredB, time.Hour, false, terminated); scope.State != ViewScopeLabelMissing {
			t.Fatalf("configured b, 1h: scope = %+v", scope)
		}
	})
}
