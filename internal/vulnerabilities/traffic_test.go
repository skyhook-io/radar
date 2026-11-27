package vulnerabilities

import (
	"context"
	"fmt"
	"slices"
	"testing"
	"time"

	pkgtraffic "github.com/skyhook-io/radar/pkg/traffic"
)

func flowPod(ns, name, workload, kind string) pkgtraffic.Endpoint {
	return pkgtraffic.Endpoint{Namespace: ns, Name: name, Kind: pkgtraffic.EndpointKindPod, Workload: workload, WorkloadKind: kind}
}

var (
	world    = pkgtraffic.Endpoint{Name: "world", Kind: pkgtraffic.EndpointKindExternal, IP: "203.0.113.9"}
	nodeHost = pkgtraffic.Endpoint{Name: "remote-node", Kind: pkgtraffic.EndpointKindHost, IP: "10.0.0.5"}
)

func flow(src, dst pkgtraffic.Endpoint) pkgtraffic.Flow {
	return pkgtraffic.Flow{Source: src, Destination: dst, Connections: 1, Verdict: "forwarded"}
}

// What counts as seen, per source. Hubble counts only its world and CIDR
// identities, so an outside client a node rewrote (SNAT) reads as the node
// and is a miss, never a mark; the metrics sources count any caller but a
// node, since they call a node External like any address outside.
func TestTrafficReceiver(t *testing.T) {
	front := flowPod("shop", "frontend-7d9f-x2k4q", "frontend", "Deployment")
	other := flowPod("shop", "checkout-5c-abc", "checkout", "Deployment")
	isNode := func(e pkgtraffic.Endpoint) bool { return e.Namespace == "" && e.Name == "kind-worker" }
	metricsPod := func(ns, name string) pkgtraffic.Endpoint {
		return pkgtraffic.Endpoint{Namespace: ns, Name: name, Kind: pkgtraffic.EndpointKindPod, Workload: name}
	}
	caretta := func(ns, name string) pkgtraffic.Endpoint {
		return pkgtraffic.Endpoint{Namespace: ns, Name: name, Kind: pkgtraffic.EndpointKindExternal}
	}
	for _, tc := range []struct {
		name        string
		f           pkgtraffic.Flow
		fromOutside bool
		want        string // namespace/name/kind, or "" for not counted
	}{
		{"hubble: from the world", flow(world, front), true, "shop/frontend/Deployment"},
		{"hubble: SNAT makes it the node", flow(nodeHost, front), true, ""},
		{"hubble: from a pod inside", flow(other, front), true, ""},
		{"hubble: dropped by policy", func() pkgtraffic.Flow { f := flow(world, front); f.Verdict = "dropped"; return f }(), true, ""},
		{"hubble: an error verdict was never delivered", func() pkgtraffic.Flow { f := flow(world, front); f.Verdict = "error"; return f }(), true, ""},
		{"hubble: outbound to the world", flow(front, world), true, ""},
		{"hubble: a bare pod is its own workload", flow(world, flowPod("shop", "debug", "", "")), true, "shop/debug/Pod"},
		{"metrics: another workload calls it", flow(metricsPod("shop", "checkout"), metricsPod("shop", "frontend")), false, "shop/frontend/"},
		{"metrics: an address outside", flow(caretta("", "203.0.113.9"), metricsPod("shop", "frontend")), false, "shop/frontend/"},
		{"metrics: a node (kubelet probes)", flow(caretta("", "kind-worker"), metricsPod("shop", "frontend")), false, ""},
		{"metrics: a host endpoint", flow(nodeHost, metricsPod("shop", "frontend")), false, ""},
		{"metrics: its own replicas", flow(metricsPod("shop", "frontend"), metricsPod("shop", "frontend")), false, ""},
		{"metrics: its own replicas, kinds known", flow(flowPod("shop", "frontend-1-a", "frontend", "Deployment"), front), false, ""},
		{"metrics: a StatefulSet calls the Deployment of its name", flow(flowPod("shop", "frontend-0", "frontend", "StatefulSet"), front), false, "shop/frontend/Deployment"},
		{"istio: a peer it couldn't identify", flow(pkgtraffic.Endpoint{Namespace: "unknown", Name: "unknown", Kind: pkgtraffic.EndpointKindPod, Workload: "unknown"}, metricsPod("shop", "frontend")), false, ""},
		{"metrics: a caller with no name or address", flow(pkgtraffic.Endpoint{Kind: pkgtraffic.EndpointKindExternal}, metricsPod("shop", "frontend")), false, ""},
		{"metrics: a 5xx response still arrived", func() pkgtraffic.Flow {
			f := flow(metricsPod("shop", "checkout"), metricsPod("shop", "frontend"))
			f.Verdict, f.Connections, f.RequestRate = "error", 0, 0.2
			return f
		}(), false, "shop/frontend/"},
		{"no activity in the window", func() pkgtraffic.Flow { f := flow(world, front); f.Connections = 0; return f }(), true, ""},
		{"direction unknown", func() pkgtraffic.Flow {
			f := flow(metricsPod("shop", "a"), metricsPod("shop", "b"))
			f.DirectionUnknown = true
			return f
		}(), false, ""},
		{"caretta: a node, as Caretta files it", flow(pkgtraffic.Endpoint{Namespace: "node", Name: "vuln-review-control-plane", Kind: "node"}, metricsPod("shop", "frontend")), false, ""},
		{"caretta: an address it couldn't resolve calls in", flow(pkgtraffic.Endpoint{Namespace: "external", Name: "203.0.113.9", Kind: "external"}, metricsPod("shop", "frontend")), false, "shop/frontend/"},
		{"caretta: names the workload kind", flow(metricsPod("shop", "checkout"), pkgtraffic.Endpoint{Namespace: "shop", Name: "frontend", Kind: "Deployment", Workload: "frontend"}), false, "shop/frontend/Deployment"},
		{"caretta: to an outside address", flow(metricsPod("shop", "frontend"), pkgtraffic.Endpoint{Namespace: "external", Name: "203.0.113.9", Kind: "external"}), false, ""},
		{"caretta: to a node", flow(metricsPod("shop", "frontend"), pkgtraffic.Endpoint{Namespace: "node", Name: "n1", Kind: "node"}), false, ""},
		{"to a Service, not a workload", flow(metricsPod("shop", "a"), pkgtraffic.Endpoint{Namespace: "shop", Name: "svc", Kind: "Service"}), false, ""},
	} {
		ns, name, kind, ok := receiver(tc.f, tc.fromOutside, isNode)
		got := ""
		if ok {
			got = ns + "/" + name + "/" + kind
		}
		if got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The page marks only exposed workloads, so when the list is capped the
// exposed ones it would mark are kept and unexposed ones left out.
func TestTrafficAnswerListsExposedFirst(t *testing.T) {
	a := WorkloadRef{Kind: "Deployment", Namespace: "shop", Name: "a"}
	z := WorkloadRef{Kind: "Deployment", Namespace: "shop", Name: "z"}
	ev := trafficFixture(t, "hubble", flow(world, flowPod("shop", "a-1", "a", "Deployment")), flow(world, flowPod("shop", "z-1", "z", "Deployment")))
	targets := []target{{workload: a}, {workload: z}}
	got := newTrafficIndex(ev, targets).answer(targets, 1, func(w WorkloadRef) bool { return w == z })
	if got.WorkloadsTotal != 2 || !slices.Equal(got.Workloads, []WorkloadRef{z}) {
		t.Fatalf("capped at one: %+v of %d", got.Workloads, got.WorkloadsTotal)
	}
}

// Only Hubble is a source that tells outside callers apart.
func TestTrafficFromOutsideIsPerSource(t *testing.T) {
	for source, want := range map[string]bool{"hubble": true, "caretta": false, "beyla": false, "istio": false} {
		if got := NewTrafficEvidence(source, time.Minute, false, nil, nil).FromOutside; got != want {
			t.Errorf("%s: FromOutside %v, want %v", source, got, want)
		}
	}
}

func trafficFixture(t *testing.T, source string, flows ...pkgtraffic.Flow) *TrafficEvidence {
	t.Helper()
	return NewTrafficEvidence(source, 15*time.Minute, source == "hubble", flows, nil)
}

func mustBuildTraffic(t *testing.T, view View, f Filter, export bool, ev *TrafficEvidence) Response {
	t.Helper()
	items, targets := exposureFixture()
	resp, err := buildWith(context.Background(), items, targets, view, f, export, nil, ev)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

// The answer names the workloads seen, marks the workload rows, and the
// filter keeps the rows a seen workload runs. Without a source there is no
// answer and the filter keeps everything: nothing seen is not "no traffic".
func TestTrafficSeenInTheAnswer(t *testing.T) {
	ev := trafficFixture(t, "hubble", flow(world, flowPod("shop", "worker-1-abc", "worker", "Deployment")))
	resp := mustBuildTraffic(t, ViewImage, Filter{}, false, ev)
	if resp.Traffic == nil || resp.Traffic.Source != "hubble" || !resp.Traffic.FromOutside || resp.Traffic.WindowSeconds != 900 || !resp.Traffic.HistoryLimited ||
		!slices.Equal(resp.Traffic.Workloads, []WorkloadRef{worker}) || resp.Traffic.WorkloadsTotal != 1 {
		t.Fatalf("traffic: %+v", resp.Traffic)
	}
	images := mustBuildTraffic(t, ViewImage, Filter{TrafficSeenOnly: true}, false, ev).Images
	if len(images) != 1 || images[0].Image != "acme/base:1" || images[0].WorkloadsTotal != 2 {
		t.Fatalf("filtered images: %+v", images)
	}
	workloads := mustBuildTraffic(t, ViewWorkload, Filter{}, false, ev).Workloads
	for _, w := range workloads {
		if w.TrafficSeen != (w.Name == "worker") {
			t.Errorf("workload %s: TrafficSeen %v", w.Name, w.TrafficSeen)
		}
	}
	if got := mustBuildTraffic(t, ViewWorkload, Filter{TrafficSeenOnly: true}, false, ev).Workloads; len(got) != 1 || got[0].Name != "worker" {
		t.Fatalf("filtered workloads: %+v", got)
	}
	if got := mustBuildTraffic(t, ViewCVE, Filter{TrafficSeenOnly: true, Severities: []Severity{SeverityCritical}}, false, ev).CVEs; len(got) != 0 {
		t.Fatalf("seen AND critical: %+v", got)
	}

	none := mustBuildTraffic(t, ViewImage, Filter{TrafficSeenOnly: true}, false, nil)
	if none.Traffic != nil || len(none.Images) != 2 {
		t.Fatalf("no source: traffic %+v, %d images", none.Traffic, len(none.Images))
	}
	empty := mustBuildTraffic(t, ViewImage, Filter{}, false, trafficFixture(t, "caretta"))
	if empty.Traffic == nil || empty.Traffic.WorkloadsTotal != 0 || empty.Traffic.Workloads == nil {
		t.Fatalf("a source that saw nothing still answered: %+v", empty.Traffic)
	}
}

// A source that names no kind matches a workload by name only when one
// workload has it, so a StatefulSet named like a seen Deployment isn't
// marked.
func TestTrafficNameOnlyMatchNeedsOneWorkload(t *testing.T) {
	seen := pkgtraffic.Endpoint{Namespace: "shop", Name: "frontend", Kind: pkgtraffic.EndpointKindPod, Workload: "frontend"}
	ev := trafficFixture(t, "caretta", flow(pkgtraffic.Endpoint{Namespace: "shop", Name: "checkout", Kind: pkgtraffic.EndpointKindPod, Workload: "checkout"}, seen))
	items, targets := exposureFixture()
	twin := WorkloadRef{Kind: "StatefulSet", Group: "apps", Namespace: "shop", Name: "frontend"}
	one, err := buildWith(context.Background(), items, targets, ViewImage, Filter{}, false, nil, ev)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(one.Traffic.Workloads, []WorkloadRef{frontend}) {
		t.Fatalf("one frontend: %+v", one.Traffic.Workloads)
	}
	targets = append(targets, running(liveTarget("shop", "StatefulSet", "frontend", "app", "acme/base:1", "u-ss", twin), "sha256:base"))
	two, err := buildWith(context.Background(), items, targets, ViewImage, Filter{}, false, nil, ev)
	if err != nil {
		t.Fatal(err)
	}
	if two.Traffic.WorkloadsTotal != 0 {
		t.Fatalf("two workloads named frontend: %+v", two.Traffic.Workloads)
	}
}

// The CSV says yes or nothing, with the window and what counted on every
// row once a source answered, and nothing at all without one.
func TestExportTrafficColumns(t *testing.T) {
	ev := trafficFixture(t, "hubble", flow(world, flowPod("shop", "frontend-1-abc", "frontend", "Deployment")))
	for _, view := range []View{ViewImage, ViewFix, ViewCVE, ViewWorkload, ViewFindings} {
		cols := ExportColumns[view]
		if !slices.Equal(cols[len(cols)-3:], trafficColumns) {
			t.Fatalf("%s header ends %v", view, cols[len(cols)-3:])
		}
		rows := exportRows(t, mustBuildTraffic(t, view, Filter{}, true, ev), Filter{})
		yes := 0
		for _, r := range rows {
			switch r["traffic_seen"] {
			case "yes":
				yes++
			case "":
			default:
				t.Fatalf("%s: traffic_seen %q", view, r["traffic_seen"])
			}
			if r["traffic_window"] != "up to 15m" || r["traffic_from"] != "outside_cluster" {
				t.Fatalf("%s: window %q from %q", view, r["traffic_window"], r["traffic_from"])
			}
		}
		if yes == 0 || yes == len(rows) {
			t.Fatalf("%s: %d of %d rows seen", view, yes, len(rows))
		}
		for _, r := range exportRows(t, mustBuildTraffic(t, view, Filter{}, true, nil), Filter{}) {
			if r["traffic_seen"] != "" || r["traffic_window"] != "" || r["traffic_from"] != "" {
				t.Fatalf("%s without a source: %v", view, r)
			}
		}
	}
	findings := exportRows(t, mustBuildTraffic(t, ViewFindings, Filter{}, true, ev), Filter{TrafficSeenOnly: true})
	for _, r := range findings {
		if r["workload"] != "frontend" {
			t.Fatalf("findings under the filter: %v", r)
		}
	}
	metrics := exportRows(t, mustBuildTraffic(t, ViewWorkload, Filter{}, true, trafficFixture(t, "beyla")), Filter{})
	if metrics[0]["traffic_window"] != "15m" || metrics[0]["traffic_from"] != "any_caller" {
		t.Fatalf("beyla: %v", metrics[0])
	}
}

// One fetch is a whole source answer, as large as Hubble's flow budget: it
// is indexed once per fetch, and each read is a lookup per workload.
func BenchmarkTrafficEvidenceBusyFlowTable(b *testing.B) {
	flows := make([]pkgtraffic.Flow, 50_000)
	for i := range flows {
		src := world
		if i%3 != 0 {
			src = flowPod(fmt.Sprintf("ns-%d", i%40), fmt.Sprintf("client-%d", i%500), fmt.Sprintf("client-%d", i%500), "Deployment")
		}
		flows[i] = flow(src, flowPod(fmt.Sprintf("ns-%d", i%40), fmt.Sprintf("app-%d-x", i%2000), fmt.Sprintf("app-%d", i%2000), "Deployment"))
	}
	b.ReportAllocs()
	for b.Loop() {
		NewTrafficEvidence("hubble", 15*time.Minute, true, flows, nil)
	}
}
