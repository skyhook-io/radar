package traffic

import (
	"context"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/skyhook-io/radar/internal/portforward"
	promclient "github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/pkg/prom"
)

const (
	// defaultBeylaJobSelector is used unless overridden via SetBeylaJobSelector
	// (wired to --beyla-job-selector) for clusters running Alloy/Beyla under a
	// non-default Prometheus job name.
	defaultBeylaJobSelector = `job=~".*beyla.*|.*alloy.*"`
	// Rate window in the PromQL queries; used to turn per-second rates back
	// into absolute counts for the window.
	beylaRateWindowSeconds = 300

	// Grafana's Beyla vendors upstream OBI and renames the flow metric back to
	// beyla_*; OBI itself emits obi_*. Both distributions are current, so the
	// prefix is resolved once in Detect rather than assumed.
	beylaFlowMetric = "beyla_network_flow_bytes_total"
	obiFlowMetric   = "obi_network_flow_bytes_total"
)

// beylaJobSelector returns the PromQL job-label matcher fragment (e.g.
// `job=~".*beyla.*"`) used to scope Beyla queries, honoring any operator
// override.
func beylaJobSelector() string {
	if selector := BeylaJobSelector(); selector != "" {
		return selector
	}
	return defaultBeylaJobSelector
}

type promQueryFunc func(ctx context.Context, query string) (*prom.QueryResult, error)

// usableSample reports whether a series value can be turned into a count. The
// comparison alone is not enough: NaN fails every ordered comparison, so `val <= 0`
// admits it, and converting NaN or an infinity to an integer is platform-defined —
// on amd64 it yields the most negative int64, which then sums into byte totals.
// A ratio of two rates, as the mean-latency query is, produces NaN at zero traffic.
func usableSample(val float64) bool {
	return val > 0 && !math.IsNaN(val) && !math.IsInf(val, 0)
}

// BeylaSource implements TrafficSource for Grafana Beyla via Prometheus metrics.
type BeylaSource struct {
	k8sClient kubernetes.Interface
	queryFn   promQueryFunc

	// mu guards the fields Detect resolves and the pollers read. Manager releases
	// its own lock before calling into a source, so a re-detection can land while
	// a StreamFlows goroutine is mid-poll.
	mu sync.RWMutex
	// flowMetric is the network-flow metric name this cluster actually exposes,
	// resolved by Detect. Empty until then; flowMetricName falls back to the
	// Beyla spelling so a GetFlows before Detect still queries something valid.
	flowMetric string
}

// NewBeylaSource creates a new Beyla traffic source wired to the shared Prometheus client.
func NewBeylaSource(client kubernetes.Interface) *BeylaSource {
	s := &BeylaSource{k8sClient: client}
	s.queryFn = s.defaultQuery
	return s
}

func (s *BeylaSource) Name() string { return "beyla" }

func (s *BeylaSource) flowMetricName() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.flowMetric != "" {
		return s.flowMetric
	}
	return beylaFlowMetric
}

func (s *BeylaSource) setFlowMetric(metric string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.flowMetric = metric
}

func (s *BeylaSource) defaultQuery(ctx context.Context, query string) (*prom.QueryResult, error) {
	client := promclient.GetClient()
	if client == nil {
		return nil, fmt.Errorf("prometheus client not initialized")
	}
	return client.Query(ctx, query)
}

func (s *BeylaSource) query(ctx context.Context, query string) (*prom.QueryResult, error) {
	return s.queryFn(ctx, query)
}

// Connect delegates to the shared Prometheus client's EnsureConnected.
func (s *BeylaSource) Connect(ctx context.Context, contextName string) (*portforward.ConnectionInfo, error) {
	client := promclient.GetClient()
	if client == nil {
		return &portforward.ConnectionInfo{Connected: false, Error: "Prometheus client not initialized"}, nil
	}
	_, _, err := client.EnsureConnected(ctx)
	if err != nil {
		return &portforward.ConnectionInfo{Connected: false, Error: fmt.Sprintf("Failed to connect to Prometheus: %v", err)}, nil
	}
	status := client.GetStatus()
	info := &portforward.ConnectionInfo{Connected: true, Address: status.Address, ContextName: contextName}
	if status.Service != nil {
		info.Namespace = status.Service.Namespace
		info.ServiceName = status.Service.Name
	}
	return info, nil
}

// ConnectionInfo implements ConnectionReporter. Beyla's data path is the
// shared Prometheus client, so its status is the only honest answer here.
func (s *BeylaSource) ConnectionInfo() *portforward.ConnectionInfo {
	client := promclient.GetClient()
	if client == nil {
		return &portforward.ConnectionInfo{Connected: false}
	}
	return connectionInfoFromPromStatus(client.GetStatus())
}

func (s *BeylaSource) Close() error { return nil }

func (s *BeylaSource) Detect(ctx context.Context) (*DetectionResult, error) {
	result := &DetectionResult{Available: false}

	// Probe each prefix in turn and remember which answered. Deliberately not a
	// single regex union of the two names: a cluster part-way through migrating
	// from Beyla to OBI would run both, and summing them would double every edge.
	for _, metric := range []string{beylaFlowMetric, obiFlowMetric} {
		qr, err := s.query(ctx, fmt.Sprintf(`count(%s{%s})`, metric, beylaJobSelector()))
		if err != nil || qr == nil || len(qr.Series) == 0 {
			continue
		}
		s.setFlowMetric(metric)
		result.Available = true
		result.Native = false
		result.Message = fmt.Sprintf("Beyla detected via Prometheus metrics (%s)", metric)
		result.Version = s.detectVersion(ctx)
		return result, nil
	}

	// No flow metric under the selector. build_info survives when the network
	// feature is off — it is opt-in via OTEL_EBPF_METRICS_FEATURES and off by
	// default — so it is what separates "Beyla is here" from "Beyla is not
	// installed". Pod labels cannot make that distinction:
	// app.kubernetes.io/name=alloy matches every Alloy install, and most Alloy
	// installs carry no Beyla at all. Reporting Available on that basis wins the
	// source priority order in manager.go and then renders a permanently empty
	// graph with nothing to explain it, which is why availability now requires
	// data, the same way the Caretta source validates its backend really holds
	// Caretta metrics before claiming it.
	//
	// What build_info cannot establish is *why* the flow metric is missing. A
	// cluster with the network feature off and a genuinely idle cluster both
	// produce no series, so the message offers both rather than asserting the
	// first — telling someone to enable a feature they already enabled sends them
	// looking in the wrong place.
	if version := s.detectVersion(ctx); version != "" {
		result.Version = version
		result.Present = true
		result.Message = fmt.Sprintf("Beyla %s is running, but Prometheus holds no network flow metrics for it. "+
			`Either the network feature is off — add "network" to OTEL_EBPF_METRICS_FEATURES — `+
			"or it is on and no traffic has been observed yet.", version)
		return result, nil
	}

	// Nothing under the selector at all. If Beyla is in Prometheus under some other
	// job name, the selector is the problem and telling the operator to enable a
	// feature they have already enabled sends them the wrong way.
	if version := s.buildInfoVersion(ctx, false); version != "" {
		result.Version = version
		result.Present = true
		result.Message = fmt.Sprintf("Beyla %s is in Prometheus, but none of its metrics match %s. "+
			"Point --beyla-job-selector at the job Beyla is scraped under.", version, beylaJobSelector())
		return result, nil
	}

	// Pods are a diagnostic only, never a reason to claim availability: if
	// something Beyla-shaped is running but Prometheus holds nothing for it, the
	// useful thing to say is that the scrape or the job selector is the problem.
	if pods := s.countBeylaPods(ctx); pods > 0 {
		result.Present = true
		result.Message = fmt.Sprintf("Found %d running Alloy or Beyla pod(s), but Prometheus holds no Beyla metrics. "+
			"Check that Prometheus scrapes Beyla, and that --beyla-job-selector matches its job label.", pods)
		return result, nil
	}

	// Says to enable the network feature up front. It is opt-in and off by default,
	// so advice that stops at "install it" ends with an install that produces an
	// empty map and a second message explaining why.
	result.Message = `Beyla not detected. Install Alloy + Beyla for traffic visibility, ` +
		`and include "network" in OTEL_EBPF_METRICS_FEATURES — the traffic map needs it and it is off by default.`
	return result, nil
}

// detectVersion reads the version out of build_info, which is present whenever
// Beyla is running regardless of which metric features are enabled. Scoped to the
// same jobs the flow queries read, so it cannot report a version for an instance
// GetFlows will never see.
func (s *BeylaSource) detectVersion(ctx context.Context) string {
	return s.buildInfoVersion(ctx, true)
}

// buildInfoVersion reads the version from build_info, optionally without the job
// selector. The unscoped form exists only to tell two failures apart: Beyla
// installed with the network feature off, versus Beyla installed and emitting
// fine but under a job name the selector does not match. Both look like "no flow
// metric", and they need opposite fixes.
//
// obi_build_info is inferred from the flow-metric prefix rather than observed —
// upstream OBI was not available to scrape. A wrong guess is harmless: the query
// returns no series and the next candidate is tried.
func (s *BeylaSource) buildInfoVersion(ctx context.Context, scoped bool) string {
	for _, metric := range []string{"beyla_build_info", "obi_build_info"} {
		query := metric
		if scoped {
			query = fmt.Sprintf(`%s{%s}`, metric, beylaJobSelector())
		}
		qr, err := s.query(ctx, query)
		if err != nil || qr == nil {
			continue
		}
		for _, series := range qr.Series {
			if v := series.Labels["version"]; v != "" {
				return v
			}
			if v := series.Labels["beyla_version"]; v != "" {
				return v
			}
		}
	}
	return ""
}

func (s *BeylaSource) countBeylaPods(ctx context.Context) int {
	count := 0
	for _, label := range []string{"app.kubernetes.io/name=alloy", "app.kubernetes.io/name=beyla"} {
		pods, err := s.k8sClient.CoreV1().Pods("").List(ctx, metav1.ListOptions{LabelSelector: label})
		if err != nil {
			log.Printf("[beyla] Failed to list pods matching %s: %v", label, err)
			continue
		}
		for i := range pods.Items {
			if pods.Items[i].Status.Phase == corev1.PodRunning {
				count++
			}
		}
	}
	return count
}

// l4Key identifies one conversation for dedup. It covers every label
// beylaL4GroupBy groups by except the two owner types, which are excluded
// deliberately — see preferL4. transport is the raw label rather than the mapped
// protocol, since mapBeylaTransport collapses everything that is not TCP or UDP
// into "tcp" and would merge genuinely distinct series.
type l4Key struct {
	srcNs, srcName string
	dstNs, dstName string
	dstPort        int
	transport      string
}

// dstKey identifies a destination workload, without a port. Needed because
// dst_port is opt-in: when it is absent every L4 edge carries port 0, so a
// destination's HTTP traffic has to be aggregated across ports.
type dstKey struct {
	dstNs, dstName string
}

// dstPortKey identifies a destination workload and the port it was served on.
// The HTTP server-duration metric carries server_port, so L7 results join to the
// exact L4 port rather than being matched by destination and guessed at.
type dstPortKey struct {
	dstNs, dstName string
	port           int
}

// l4LabelPresence records which of the optional network attributes the scrape
// actually carried. dst_port and transport are Default:false in Beyla's
// attribute registry, so a stock install exports neither and every flow arrives
// with no port and no protocol. That is worth telling the user about rather than
// silently rendering port 0 and calling everything TCP.
type l4LabelPresence struct {
	// metric is the flow metric this cluster exposes, carried so the warning can
	// name the right attributes.select key rather than assuming Beyla's spelling.
	metric string
	// portedDsts are the destinations Beyla reported a real port for, recorded
	// before nameless series are dropped. A destination's only port-bearing
	// traffic can come from a caller Beyla cannot name — an external client — and
	// that series never becomes a flow. Judging "does this destination have ports"
	// from the surviving flows alone would miss it, and the port-0 leftover would
	// then be handed HTTP data that belongs to the caller who was dropped.
	portedDsts map[dstKey]bool
	port       bool
	transport  bool
	// replyLossFraction is how much of the return traffic Prometheus could not
	// measure, between 0 and 1. Exporting dst.port makes Beyla label each reply
	// with the client's port, which is new for every connection and gone again
	// within seconds, so most reply counters are never observed twice and no rate
	// can be derived from them. How much that costs depends on the workload —
	// long-lived connections keep a stable port and measure fine — so it is
	// measured rather than assumed from the configuration.
	replyLossFraction float64
}

func (s *BeylaSource) GetFlows(ctx context.Context, opts FlowOptions) (*FlowsResponse, error) {
	flows, presence, err := s.getFlowsInternal(ctx, opts)
	if err != nil {
		log.Printf("[beyla] Error fetching flows: %v", err)
		return &FlowsResponse{Source: "beyla", Timestamp: time.Now(), Flows: []Flow{},
			Warning:     fmt.Sprintf("Failed to query Beyla metrics: %v", err),
			WarningKind: WarningTransient}, nil
	}
	// Measured here rather than inside getFlowsInternal because only this path
	// builds a warning from it. StreamFlows shares that helper and discards the
	// presence it returns, so probing there would query the response series on
	// every poll and throw the answer away.
	// Only when dst.port is exported: without it replies carry no port, so they
	// aggregate into a handful of stable counters and nothing is lost.
	if presence.port {
		presence.replyLossFraction = s.replyLossFraction(ctx, beylaWindow(opts.Since))
	}

	response := &FlowsResponse{Source: "beyla", Timestamp: time.Now(), Flows: flows}
	if warning := presence.warning(len(flows)); warning != "" {
		response.Warning = warning
		// Describes how Beyla is configured, not a hiccup. Marked so the client
		// shows it beside the flows instead of retrying for a better answer that
		// is never coming.
		response.WarningKind = WarningPartial
	}
	return response, nil
}

// warning explains missing optional attributes in the terms an operator can act
// on. Only raised once there are flows to qualify: with no flows at all the
// absent labels are not the interesting fact.
func (p l4LabelPresence) warning(flowCount int) string {
	var parts []string

	if flowCount > 0 {
		var missing []string
		if !p.port {
			missing = append(missing, "dst.port")
		}
		if !p.transport {
			missing = append(missing, "transport")
		}
		// Worth saying only when it actually costs something: a handful of unmeasured
		// replies among thousands does not move the figure. The wording tracks the
		// measurement rather than sitting at its worst case — claiming "most" of a
		// 15% loss would be its own inaccuracy.
		if p.replyLossFraction > 0.1 {
			scale := "many"
			if p.replyLossFraction > 0.5 {
				scale = "most"
			}
			parts = append(parts, fmt.Sprintf("Received-byte figures on these edges are too low: %s replies could "+
				"not be measured. Each reply is labelled with the client's short-lived port, and those come and go "+
				"faster than they can be counted. Bytes sent, request rate, errors and latency are not affected. "+
				"Remove dst.port from attributes.select for %s if you need accurate received bytes.",
				scale, strings.TrimSuffix(p.metric, "_total")))
		}

		if len(missing) > 0 {
			// Names the metric this cluster actually exposes: on an OBI install the
			// attributes.select key is obi_network_flow_bytes, and advice pointing at
			// the other spelling does not work.
			// Each attribute has its own consequence, and only one of them may be
			// absent: advising transport on its own is deliberate, so a cluster that
			// took that advice has ports missing and protocols correct. Naming both
			// consequences either way would tell such an operator their change did
			// nothing.
			metric := strings.TrimSuffix(p.metric, "_total")
			switch {
			case !p.port && !p.transport:
				parts = append(parts, fmt.Sprintf("Beyla is not exporting dst.port and transport, so edges here "+
					"have no port or protocol detail: they appear as port 0, and UDP is shown as TCP. Both are "+
					"opt-in, added under attributes.select for %s.", metric))
			case !p.port:
				parts = append(parts, fmt.Sprintf("Beyla is not exporting dst.port, so every edge here appears as "+
					"port 0. It is opt-in, added under attributes.select for %s.", metric))
			case !p.transport:
				parts = append(parts, fmt.Sprintf("Beyla is not exporting transport, so UDP traffic here is shown "+
					"as TCP. It is opt-in, added under attributes.select for %s.", metric))
			}

			// The two are not equivalent and must not be recommended as a pair.
			// Adding transport is free. Adding dst.port buys per-port edges and costs
			// the received-byte figures.
			if !p.transport {
				parts = append(parts, "Adding transport is safe and makes UDP show as UDP.")
			}
			if !p.port {
				// Self-contained on purpose: the warning that explains this loss only
				// appears once dst.port is already exported, so a reader seeing this
				// sentence has not seen that explanation and never will from here.
				parts = append(parts, "Adding dst.port gives per-port edges, but replies would then carry the "+
					"client's short-lived port, and most of the return traffic could no longer be measured — so "+
					"received-byte figures would become unreliable. It is a trade rather than a fix.")
			}
		}
	}

	return strings.Join(parts, " ")
}

func (s *BeylaSource) getFlowsInternal(ctx context.Context, opts FlowOptions) ([]Flow, l4LabelPresence, error) {
	l4Map, presence, err := s.queryL4(ctx, opts)
	if err != nil {
		return nil, presence, fmt.Errorf("L4 query: %w", err)
	}

	l7Flows, err := s.queryL7(ctx, opts)
	if err != nil {
		log.Printf("[beyla] L7 query failed (continuing with L4 only): %v", err)
		l7Flows = nil
	}

	// Fill in what came back before anything else reads BytesRecv: the L7 split
	// below weights each caller by its share of the conversation, and half a
	// conversation is the wrong weight.
	// Received bytes are known per conversation, not per port: the response series
	// carry the client's ephemeral port, so they cannot be attributed to one of a
	// pair's edges. Where a pair has several edges the total is divided between
	// them by their share of bytes sent — copying it onto each would count the same
	// return traffic once per port.
	received := s.queryReceivedBytes(ctx, opts)
	if len(received) > 0 {
		sentPerPair := make(map[flowKey]int64)
		edgesPerPair := make(map[flowKey][]*Flow)
		for _, f := range l4Map {
			key := flowKey{
				srcNs: f.Source.Namespace, srcWorkload: f.Source.Name,
				dstNs: f.Destination.Namespace, dstWorkload: f.Destination.Name,
			}
			sentPerPair[key] += f.BytesSent
			edgesPerPair[key] = append(edgesPerPair[key], f)
		}
		for key, edges := range edgesPerPair {
			total, ok := received[key]
			if !ok {
				continue
			}
			sent := sentPerPair[key]
			for _, f := range edges {
				switch {
				case len(edges) == 1:
					f.BytesRecv = total
				case sent > 0:
					// In float64: the product of two byte counts overflows int64 at
					// roughly 3 GB each way within the window, which a multi-edge pair
					// reaches at about 10 MB/s and reports as a negative figure. A
					// float64 mantissa carries byte counts far beyond anything a
					// five-minute window can hold.
					f.BytesRecv = int64(float64(total) * float64(f.BytesSent) / float64(sent))
				default:
					f.BytesRecv = total / int64(len(edges))
				}
			}
		}
	}

	byDstPort := make(map[dstPortKey][]*Flow, len(l4Map))
	for _, f := range l4Map {
		key := dstPortKey{f.Destination.Namespace, f.Destination.Name, f.Port}
		byDstPort[key] = append(byDstPort[key], f)
	}

	// Merge L7 into L4. server_port on the HTTP metric names the port the requests
	// were served on, so there is no need to work out which of a destination's
	// ports is the HTTP one.
	//
	// Driven from the L4 buckets rather than from the HTTP records: each bucket is
	// then visited once, and its metadata is assigned once. Iterating the HTTP
	// records instead means several of them can land on the same bucket — which is
	// what happens by default, where dst_port is absent and every edge carries
	// port 0 — and each assignment overwrites the last rather than accumulating.
	// A destination can end up with both kinds of edge at once: for the five
	// minutes after dst.port is added or removed, the rate window still holds
	// series from the previous configuration. Where real ports exist for a
	// destination they are authoritative, and the port-0 bucket is a leftover — so
	// it must not also receive the destination-wide aggregate, or the same HTTP
	// rate lands on two edges.
	//
	// presence.portedDsts is used rather than the surviving buckets because a
	// destination's port-bearing traffic can come entirely from a caller Beyla
	// could not name, in which case no bucket carries that port at all.
	portedDsts := presence.portedDsts

	latency, errorRates := s.queryL7Detail(ctx, opts)
	perPort, perDst := l7ByPortAndDestination(l7Flows)
	for key, edges := range byDstPort {
		l7, ok := perPort[key]
		portless := false
		if !ok && key.port == 0 && !portedDsts[dstKey{key.dstNs, key.dstName}] {
			portless = true
			// These edges carry no port information and this destination has no
			// port-bearing edges either, so every port's HTTP traffic for it belongs
			// to them. Use the destination-wide aggregate, which has summed the
			// rates across ports.
			l7, ok = perDst[dstKey{key.dstNs, key.dstName}]
		}
		if !ok {
			continue
		}
		// The HTTP metric is recorded server-side and carries no source labels at
		// all, so a destination's request rate still has to be divided across the
		// callers that reached it rather than copied onto each one. Weight by L4
		// byte volume as the best available proxy for each caller's share;
		// server_port fixes which port, not which caller.
		var totalBytes int64
		for _, f := range edges {
			totalBytes += f.BytesSent + f.BytesRecv
		}
		for _, f := range edges {
			f.L7Protocol = l7.L7Protocol
			f.HTTPMethod = l7.HTTPMethod
			f.HTTPPath = l7.HTTPPath
			// Deliberately no HTTPStatus. A destination's requests are spread across
			// status codes and this is one series out of them, so recording it makes
			// the aggregate report a single status class as the whole distribution —
			// a destination serving 17% 5xx renders a confident all-2xx bar. The
			// error rate below carries the failure signal, which is how the other
			// rate-based source reports it: Istio queries 5xx separately and never
			// claims a status distribution either.

			// This caller's share of the destination's traffic. Everything the HTTP
			// metric reports is per destination, not per caller, so every figure
			// derived from it is divided the same way — mixing a split rate with an
			// unsplit error count produces ratios above 100%.
			share := 1 / float64(len(edges))
			if totalBytes > 0 {
				share = float64(f.BytesSent+f.BytesRecv) / float64(totalBytes)
			}
			f.RequestRate = l7.RequestRate * share
			// Same shape as the Istio source: a rate-based source puts its rate in
			// Connections and the graph labels it req/s rather than "conn".
			// RoundRate is the package's own rate-to-count conversion, and using it
			// keeps this equal to the RequestCount the aggregation derives from the
			// same rate — hand-rolling it here truncated where that rounds.
			f.Connections = RoundRate(f.RequestRate)

			// Latency is a duration, not a quantity, so it is not divided — every
			// caller on this port waited the same average. The error rate is a
			// quantity and is divided like the request rate.
			detailKey := dstPortKey{l7.Destination.Namespace, l7.Destination.Name, l7.Port}
			if seconds, ok := latency.forEdge(detailKey, portless); ok {
				f.LatencyNs = uint64(seconds * float64(time.Second))
			}
			if rate, ok := errorRates.forEdge(detailKey, portless); ok {
				// Split on the same basis as the request rate above. The HTTP metric
				// has no caller labels, so neither figure can be attributed exactly,
				// but they must at least be attributed consistently: giving each
				// caller the destination's whole error rate alongside its own share
				// of the requests lets the error count exceed the request count, and
				// the graph renders that ratio as a percentage.
				f.ErrorRate = rate * share
				// Istio marks the flow errored once any 5xx is present. The verdict
				// drives the flow-list badge; the graph colours the edge from the
				// error count the aggregation derives from this rate.
				f.Verdict = "error"
			}
		}
	}

	result := make([]Flow, 0, len(l4Map))
	for _, f := range l4Map {
		result = append(result, *f)
	}
	// Conversations Beyla could not orient are still conversations, and are drawn
	// without an arrowhead rather than left out. Leaving them out takes the
	// workload on the far end off the map with them — for DNS, that is coredns.
	result = append(result, s.queryUnorientable(ctx, opts)...)
	return result, presence, nil
}

// l7ByPortAndDestination collapses the HTTP series two ways: per destination and
// port, and per destination across all its ports. Both aggregate the same way —
// rates summed, and the route, method and status taken from the busiest single
// series so the edge label describes traffic that really happened.
//
// The destination-wide view exists because dst_port is opt-in: without it every
// L4 edge carries port 0, and a destination serving HTTP on several ports has all
// of that traffic on those same edges. Summing across ports is the only honest
// answer there; attaching each port's record in turn would report whichever came
// last.
func l7ByPortAndDestination(l7Flows []Flow) (map[dstPortKey]Flow, map[dstKey]Flow) {
	perPort := make(map[dstPortKey]Flow, len(l7Flows))
	perPortTop := make(map[dstPortKey]float64, len(l7Flows))
	perDst := make(map[dstKey]Flow, len(l7Flows))
	perDstTop := make(map[dstKey]float64, len(l7Flows))

	for _, f := range l7Flows {
		portKey := dstPortKey{f.Destination.Namespace, f.Destination.Name, f.Port}
		if cur, ok := perPort[portKey]; ok {
			if f.RequestRate > perPortTop[portKey] {
				perPortTop[portKey] = f.RequestRate
				cur.HTTPMethod, cur.HTTPPath, cur.HTTPStatus = f.HTTPMethod, f.HTTPPath, f.HTTPStatus
			}
			cur.RequestRate += f.RequestRate
			perPort[portKey] = cur
		} else {
			perPort[portKey], perPortTop[portKey] = f, f.RequestRate
		}

		dst := dstKey{f.Destination.Namespace, f.Destination.Name}
		if cur, ok := perDst[dst]; ok {
			if f.RequestRate > perDstTop[dst] {
				perDstTop[dst] = f.RequestRate
				cur.HTTPMethod, cur.HTTPPath, cur.HTTPStatus = f.HTTPMethod, f.HTTPPath, f.HTTPStatus
			}
			cur.RequestRate += f.RequestRate
			perDst[dst] = cur
		} else {
			perDst[dst], perDstTop[dst] = f, f.RequestRate
		}
	}
	return perPort, perDst
}

// preferL4 chooses between two series that describe the same conversation.
//
// Beyla reports a Service-routed conversation twice — once attributed to the
// destination workload and once to the Service in front of it — with byte
// identical values. Both carry the same source, destination name, port and
// transport, so they land on the same l4Key and one has to win. Keeping both
// would double the traffic on every Service-routed edge, which is most of them;
// letting Prometheus result order decide makes the rendered Kind arbitrary.
// Radar's graph navigates to workloads, so the workload attribution wins.
func preferL4(incumbent, candidate *Flow) *Flow {
	switch {
	case serviceEndpoints(candidate) < serviceEndpoints(incumbent):
		// The candidate is the workload attribution of a Service-routed
		// conversation; the incumbent is the Service's copy of the same bytes.
		candidate.BytesRecv = incumbent.BytesRecv
		return candidate
	case serviceEndpoints(candidate) > serviceEndpoints(incumbent):
		return incumbent
	default:
		// Neither is a Service copy of the other, so both are attributions of one
		// conversation under two owner types — Beyla reports those with identical
		// values, so adding them would double the edge. Taking the larger keeps the
		// conversation's real size and, unlike keeping whichever arrived first, does
		// not depend on the order Prometheus returned them in.
		incumbent.BytesSent = max(incumbent.BytesSent, candidate.BytesSent)
		incumbent.BytesRecv = max(incumbent.BytesRecv, candidate.BytesRecv)
		return incumbent
	}
}

func serviceEndpoints(f *Flow) int {
	count := 0
	if f.Source.Kind == "Service" {
		count++
	}
	if f.Destination.Kind == "Service" {
		count++
	}
	return count
}

const (
	beylaL4GroupBy = `k8s_src_owner_name, k8s_src_namespace, k8s_src_owner_type, k8s_dst_owner_name, k8s_dst_namespace, k8s_dst_owner_type, dst_port, transport`
	// beylaL4DirectionFilter drops the response half of every conversation.
	// Beyla emits both directions with source and destination swapped, so without
	// this each edge gains a mirror twin pointing the wrong way — and once
	// dst.port is selected each response series carries the client's ephemeral
	// port, turning one conversation into hundreds of series.
	//
	// It requires "request" rather than merely excluding "response" because there
	// is a third value, "unknown", which is where UDP lands — and Beyla labels
	// *both* directions of a UDP conversation "unknown". Keeping them means every
	// DNS conversation draws a mirrored pair, and once dst.port is selected the
	// reverse half carries the client's ephemeral port: on a 4-pod cluster that
	// alone produced 287 spurious coredns edges out of 289 flows. Nothing in the
	// labels can orient an "unknown" pair, since after filtering it is
	// indistinguishable from two services genuinely calling each other.
	//
	// The cost is that UDP traffic does not appear in the graph at all. That is
	// surfaced to the user rather than left implicit — see l4LabelPresence.
	beylaL4DirectionFilter = `, direction="request"`
	// beylaUnknownDirection selects the conversations Beyla could not orient. They
	// are read separately and drawn without an arrowhead rather than dropped.
	beylaUnknownDirection = `, direction="unknown"`
	// beylaL7Metric is Beyla's OTel-aligned HTTP server histogram; there is no
	// millisecond variant.
	beylaL7Metric = "http_server_request_duration_seconds_count"
	// beylaL7GroupBy labels come from http_server_request_duration_seconds, which
	// is recorded server-side only. k8s_owner_name is Beyla's own resolved owner,
	// the same concept as k8s_dst_owner_name on the L4 metric, so destinations
	// from both metrics line up. server_port names the port the requests were
	// served on, which is what lets L7 join to a specific L4 edge. No caller or
	// source labels exist on this metric at all.
	beylaL7GroupBy = `k8s_namespace_name, k8s_owner_name, k8s_pod_name, server_port, http_request_method, http_route, http_response_status_code`
	// beylaL7DetailGroupBy keys latency and errors the way parseL7Flows names a
	// destination — owner first, pod name when a workload has no owner. Grouping by
	// owner alone collapses every owner-less series in a namespace into one row with
	// an empty name, which is then dropped, so a bare pod gets a request rate but
	// never a latency or an error.
	beylaL7DetailGroupBy = `k8s_namespace_name, k8s_owner_name, k8s_pod_name, server_port`
)

// rateWindow is the span every query in this source rates over, and the span the
// byte totals are derived for. Both have to come from the same place: a rate is
// per-second, so turning it back into a total means multiplying by exactly the
// window that produced it.
type rateWindow struct {
	promQL  string
	seconds float64
}

// beylaWindow turns the caller's requested span into a window these queries can
// use. The traffic view offers 1m to 1h and the value arrives here; ignoring it
// leaves the control inert, which is what it was.
//
// Anything shorter than a minute cannot be rated: Prometheus needs two samples in
// the window, and a scrape interval of 15s makes a sub-minute span a coin toss. The
// default matches the view's own default rather than being an arbitrary floor.
func beylaWindow(since time.Duration) rateWindow {
	if since < time.Minute {
		return rateWindow{promQL: "5m", seconds: beylaRateWindowSeconds}
	}
	// Expressed in seconds so any duration the caller asks for is valid PromQL.
	// time.Duration'''s own format is not: it renders an hour as "1h0m0s".
	return rateWindow{promQL: fmt.Sprintf("%ds", int(since.Seconds())), seconds: since.Seconds()}
}

// beylaRateQuery builds `sum by (groupBy) (rate(metric{job=~...,extra}[window]))`. A
// namespace filter has to become two OR'd selectors: PromQL cannot express
// "src OR dst namespace matches" inside a single label selector.
func beylaRateQuery(groupBy, metric, namespace, extra string, w rateWindow) string {
	sum := func(more string) string {
		return fmt.Sprintf(`sum by (%s) (rate(%s{%s%s%s}[%s]))`, groupBy, metric, beylaJobSelector(), extra, more, w.promQL)
	}
	if namespace == "" {
		return sum("")
	}
	return sum(fmt.Sprintf(`, k8s_src_namespace=%q`, namespace)) + " or " +
		sum(fmt.Sprintf(`, k8s_dst_namespace=%q`, namespace))
}

// beylaL7LatencyQuery averages the HTTP server duration per destination and port.
// Beyla exports the histogram's sum and count; their ratio over the same window is
// the mean, which is what the flow list's Latency column shows. Hubble fills the
// same field from packet timing.
func beylaL7LatencyQuery(namespace string, w rateWindow) string {
	extra := ""
	if namespace != "" {
		extra = fmt.Sprintf(`, k8s_namespace_name=%q`, namespace)
	}
	group := beylaL7DetailGroupBy
	return fmt.Sprintf(`sum by (%s) (rate(http_server_request_duration_seconds_sum{%s%s}[%s])) / sum by (%s) (rate(%s{%s%s}[%s]))`,
		group, beylaJobSelector(), extra, w.promQL, group, beylaL7Metric, beylaJobSelector(), extra, w.promQL)
}

// beylaL7ErrorQuery is the 5xx share of requests, the same definition the Istio
// source uses for ErrorRate and what the graph's "Errors (5xx)" legend entry means.
func beylaL7ErrorQuery(namespace string, w rateWindow) string {
	extra := ""
	if namespace != "" {
		extra = fmt.Sprintf(`, k8s_namespace_name=%q`, namespace)
	}
	group := beylaL7DetailGroupBy
	return fmt.Sprintf(`sum by (%s) (rate(%s{%s%s, http_response_status_code=~"5.."}[%s]))`,
		group, beylaL7Metric, beylaJobSelector(), extra, w.promQL)
}

// beylaL7RateQuery builds the L7 query. Unlike beylaRateQuery, there's only
// one namespace label to filter on (k8s_namespace_name) since the metric has
// no source side.
func beylaL7RateQuery(namespace string, w rateWindow) string {
	extra := ""
	if namespace != "" {
		extra = fmt.Sprintf(`, k8s_namespace_name=%q`, namespace)
	}
	return fmt.Sprintf(`sum by (%s) (rate(%s{%s%s}[%s]))`, beylaL7GroupBy, beylaL7Metric, beylaJobSelector(), extra, w.promQL)
}

func (s *BeylaSource) queryL4(ctx context.Context, opts FlowOptions) (map[l4Key]*Flow, l4LabelPresence, error) {
	query := beylaRateQuery(beylaL4GroupBy, s.flowMetricName(), opts.Namespace, beylaL4DirectionFilter, beylaWindow(opts.Since))
	result, err := s.query(ctx, query)
	if err != nil {
		return nil, l4LabelPresence{}, err
	}
	flows, presence := s.parseL4Flows(result, beylaWindow(opts.Since))
	presence.metric = s.flowMetricName()
	return flows, presence, nil
}

// queryReceivedBytes reads the response half of each conversation, which the flow
// query itself excludes because it cannot be oriented into an edge. It is still
// the true count of bytes coming back, so it fills BytesRecv rather than being
// discarded — the same field Istio fills from istio_response_bytes_sum.
//
// Keyed by the forward edge: a response series runs destination-to-source, so its
// endpoints are inverted here. Port is left out of the key because response series
// carry the client's ephemeral port.
// replyLossFraction measures how much of the return traffic is present in
// Prometheus but cannot be turned into a rate.
//
// Deriving a rate needs a counter observed at least twice. When dst.port is
// exported, Beyla labels each reply with the client's port, so a new counter
// appears per connection and is gone within seconds — usually seen once, or not at
// all, between scrapes. Those bytes are dropped before any grouping this code
// could change, which is why the figure is disclosed rather than corrected.
//
// Whether it matters depends on the workload: long-lived connections hold a stable
// client port and measure normally. So this reports what was actually lost instead
// of inferring severity from the configuration. Returns 0 when nothing is missing
// or the probe cannot be answered — this drives a warning, and a warning invented
// from a failed query is worse than none.
func (s *BeylaSource) replyLossFraction(ctx context.Context, w rateWindow) float64 {
	metric := s.flowMetricName()
	// Deliberately not scoped to the namespace filter. This measures whether the
	// cluster's reply counters are measurable at all, which is a property of how
	// Beyla is configured rather than of whichever namespaces are on screen, and
	// scoping it would mean an `or` of two counts and no single value to read.
	query := fmt.Sprintf(
		`1 - ((count(rate(%s{%s, direction="response"}[%s]) > 0) or vector(0)) / (count(%s{%s, direction="response"}) or vector(1)))`,
		metric, beylaJobSelector(), w.promQL, metric, beylaJobSelector())
	result, err := s.query(ctx, query)
	if err != nil || result == nil || len(result.Series) == 0 || len(result.Series[0].DataPoints) == 0 {
		return 0
	}
	// A fraction outside (0, 1] is not an answer to the question asked, so treat it
	// as no answer rather than scaling a warning by it.
	val := result.Series[0].DataPoints[0].Value
	if math.IsNaN(val) || math.IsInf(val, 0) || val <= 0 || val > 1 {
		return 0
	}
	return val
}

func (s *BeylaSource) queryReceivedBytes(ctx context.Context, opts FlowOptions) map[flowKey]int64 {
	// k8s_*_owner_type belongs in the group-by even though the key ignores it.
	// Beyla reports a Service-routed conversation twice, once attributed to the
	// workload and once to the Service, with identical values; without the label
	// here PromQL's sum by adds the two together and every such edge reports
	// double the bytes coming back.
	groupBy := `k8s_src_owner_name, k8s_src_namespace, k8s_src_owner_type, k8s_dst_owner_name, k8s_dst_namespace, k8s_dst_owner_type`
	w := beylaWindow(opts.Since)
	query := beylaRateQuery(groupBy, s.flowMetricName(), opts.Namespace, `, direction="response"`, w)
	result, err := s.query(ctx, query)
	if err != nil || result == nil {
		// Received bytes are an enrichment; without them edges still draw.
		return nil
	}

	received := make(map[flowKey]int64, len(result.Series))
	for _, series := range result.Series {
		if len(series.DataPoints) == 0 {
			continue
		}
		val := series.DataPoints[0].Value
		if !usableSample(val) {
			continue
		}
		labels := series.Labels
		respSrc := pickLabel(labels, "k8s_src_owner_name", "k8s_src_name")
		respDst := pickLabel(labels, "k8s_dst_owner_name", "k8s_dst_name")
		if respSrc == "" || respDst == "" {
			continue
		}
		// Invert: the response's destination is the forward edge's source.
		// flowKey is the package's existing pair key, used the same way by the
		// Istio source to join its byte and error queries onto its flows.
		key := flowKey{
			srcNs: labels["k8s_dst_namespace"], srcWorkload: respDst,
			dstNs: labels["k8s_src_namespace"], dstWorkload: respSrc,
		}
		// The duplicate attributions carry the same value, so take one rather than
		// summing. Max is used instead of first-seen so the result does not depend
		// on the order Prometheus returned the series, and so a pair whose
		// attributions ever disagree reports the larger rather than their total.
		if bytes := int64(val * w.seconds); bytes > received[key] {
			received[key] = bytes
		}
	}
	return received
}

// queryUnorientable reads the conversations Beyla reports as direction="unknown"
// — which is where UDP lands, DNS above all — and returns one edge per pair.
//
// Both halves carry that label, so neither can be called the request. Emitting
// both would draw a mirrored pair of arrows, and dropping them removes real
// traffic from the map along with the workload on the other end: with DNS gone,
// coredns disappears entirely. Instead the pair is collapsed into a single edge,
// ordered deterministically, marked as having no known direction so the graph
// leaves the arrowhead off.
//
// Ports are not carried. In a default install they are absent anyway, and where
// they exist one side holds the client's ephemeral port, so there is no single
// port that describes the conversation.
func (s *BeylaSource) queryUnorientable(ctx context.Context, opts FlowOptions) []Flow {
	groupBy := `k8s_src_owner_name, k8s_src_namespace, k8s_src_owner_type, k8s_dst_owner_name, k8s_dst_namespace, k8s_dst_owner_type, transport`
	w := beylaWindow(opts.Since)
	query := beylaRateQuery(groupBy, s.flowMetricName(), opts.Namespace, beylaUnknownDirection, w)
	result, err := s.query(ctx, query)
	if err != nil || result == nil {
		return nil
	}

	// One accumulator per conversation per attribution. Beyla reports a
	// Service-routed conversation twice — once attributed to the workload, once to
	// the Service — with identical values, so the two must not be added together.
	// Ports are absent from the group-by, so Prometheus has already summed across
	// them and each attribution yields one row per direction.
	byAttribution := make(map[attribution]*conversationBytes)

	for _, series := range result.Series {
		if len(series.DataPoints) == 0 {
			continue
		}
		val := series.DataPoints[0].Value
		if !usableSample(val) {
			continue
		}
		labels := series.Labels
		aName := pickLabel(labels, "k8s_src_owner_name", "k8s_src_name")
		bName := pickLabel(labels, "k8s_dst_owner_name", "k8s_dst_name")
		if aName == "" || bName == "" {
			continue
		}
		aNs, bNs := labels["k8s_src_namespace"], labels["k8s_dst_namespace"]
		aType, bType := labels["k8s_src_owner_type"], labels["k8s_dst_owner_type"]

		// Order the endpoints so both halves of a conversation land on one key.
		// Which end is "source" is arbitrary and the graph will not imply
		// otherwise, but it has to be stable across polls or the edge would flip.
		forward := aNs+"/"+aName < bNs+"/"+bName
		key := flowKey{srcNs: aNs, srcWorkload: aName, dstNs: bNs, dstWorkload: bName}
		if !forward {
			key = flowKey{srcNs: bNs, srcWorkload: bName, dstNs: aNs, dstWorkload: aName}
		}

		srcType, dstType := aType, bType
		if !forward {
			srcType, dstType = bType, aType
		}

		att := attribution{key: key, transport: labels["transport"], srcType: srcType, dstType: dstType}
		h, ok := byAttribution[att]
		if !ok {
			h = &conversationBytes{}
			byAttribution[att] = h
		}
		if bytes := int64(val * w.seconds); forward {
			h.forward += bytes
		} else {
			h.backward += bytes
		}
	}

	// Collapse the attributions of each conversation. The graph navigates to
	// workloads, so the attribution with fewer Service ends wins — the same choice
	// preferL4 makes on the oriented path. Deterministic, so the rendered kinds do
	// not depend on the order Prometheus returned the rows.
	chosen := make(map[conversation]attribution)
	totals := make(map[conversation]*conversationBytes)
	for att, h := range byAttribution {
		conv := conversation{key: att.key, transport: att.transport}
		if prev, ok := chosen[conv]; !ok || serviceEnds(att) < serviceEnds(prev) {
			chosen[conv] = att
		}
		if t, ok := totals[conv]; ok {
			// Same conversation under another attribution: identical values, so keep
			// the larger rather than adding a second copy of the same traffic.
			t.forward = max(t.forward, h.forward)
			t.backward = max(t.backward, h.backward)
		} else {
			totals[conv] = &conversationBytes{forward: h.forward, backward: h.backward}
		}
	}

	flows := make([]Flow, 0, len(totals))
	for conv, h := range totals {
		att := chosen[conv]
		// A conversation with itself has no forward and no backward: both halves
		// sort equal, so everything lands on one side. Report the traffic rather
		// than a zero.
		if conv.key.srcNs == conv.key.dstNs && conv.key.srcWorkload == conv.key.dstWorkload && h.forward == 0 {
			h.forward, h.backward = h.backward, 0
		}
		flows = append(flows, Flow{
			Source:      Endpoint{Name: conv.key.srcWorkload, Namespace: conv.key.srcNs, Kind: mapBeylaKind(att.srcType), Workload: conv.key.srcWorkload},
			Destination: Endpoint{Name: conv.key.dstWorkload, Namespace: conv.key.dstNs, Kind: mapBeylaKind(att.dstType), Workload: conv.key.dstWorkload},
			Protocol:    mapBeylaTransport(conv.transport),
			Verdict:     "forwarded",
			LastSeen:    time.Now(),
			BytesSent:   h.forward,
			BytesRecv:   h.backward,
			// Beyla reports no request count for these, and deriving one from bytes
			// would be a number it never measured.
			DirectionUnknown: true,
		})
		// Same rule the oriented path applies: an endpoint with no namespace is
		// outside the cluster, and the external filters key on that kind.
		f := &flows[len(flows)-1]
		if f.Source.Namespace == "" && f.Source.Name != "" {
			f.Source.Kind = "External"
		}
		if f.Destination.Namespace == "" && f.Destination.Name != "" {
			f.Destination.Kind = "External"
		}
	}
	return flows
}

// conversation identifies an unorientable exchange between two endpoints over one
// transport, without saying which end began it.
type conversation struct {
	key       flowKey
	transport string
}

// attribution is one conversation as Beyla reported it. The same exchange arrives
// twice when a Service fronts the destination, once under each owner type.
type attribution struct {
	key              flowKey
	transport        string
	srcType, dstType string
}

type conversationBytes struct {
	forward  int64
	backward int64
}

// serviceEnds counts how many ends of an attribution are the Service in front of
// a workload rather than the workload itself.
func serviceEnds(a attribution) int {
	n := 0
	if strings.EqualFold(a.srcType, "service") {
		n++
	}
	if strings.EqualFold(a.dstType, "service") {
		n++
	}
	return n
}

// queryL7Detail reads mean latency and 5xx rate per destination and port. Both are
// enrichments: a failure leaves the fields unset rather than blocking the edges.
func (s *BeylaSource) queryL7Detail(ctx context.Context, opts FlowOptions) (latency, errors l7Detail) {
	read := func(query string, combine func(a, b float64) float64) l7Detail {
		out := l7Detail{perPort: map[dstPortKey]float64{}, perDst: map[dstKey]float64{}}
		result, err := s.query(ctx, query)
		if err != nil || result == nil {
			return out
		}
		for _, series := range result.Series {
			if len(series.DataPoints) == 0 {
				continue
			}
			val := series.DataPoints[0].Value
			if !usableSample(val) {
				continue
			}
			name, _ := pickBeylaOwner(series.Labels)
			if name == "" {
				continue
			}
			ns := series.Labels["k8s_namespace_name"]
			// Series are grouped by pod, so a destination with more than one replica
			// reports the same owner and port once per pod. They combine the same way
			// a destination's ports do — overwriting would report a single replica's
			// figure as the whole destination's.
			port := dstPortKey{ns, name, parseIntLabel(series.Labels["server_port"])}
			if existing, ok := out.perPort[port]; ok {
				out.perPort[port] = combine(existing, val)
			} else {
				out.perPort[port] = val
			}
			dst := dstKey{ns, name}
			if existing, ok := out.perDst[dst]; ok {
				out.perDst[dst] = combine(existing, val)
			} else {
				out.perDst[dst] = val
			}
		}
		return out
	}
	// How two figures for one destination combine, whether they come from separate
	// ports or separate replicas. Error rates add up. Latencies are means, so the
	// combined figure is the largest of them rather than their sum — an average of
	// averages would need per-series request counts to weight it, and overstating
	// the worst port is safer than understating it.
	w := beylaWindow(opts.Since)
	return read(beylaL7LatencyQuery(opts.Namespace, w), math.Max),
		read(beylaL7ErrorQuery(opts.Namespace, w), func(a, b float64) float64 { return a + b })
}

// l7Detail holds a per-port measurement and its destination-wide equivalent, for
// the case where the L4 edges carry no port and everything for a destination lands
// on the same edge.
type l7Detail struct {
	perPort map[dstPortKey]float64
	perDst  map[dstKey]float64
}

// forEdge picks the figure matching how the L7 record was chosen: an exact port
// when the edges have one, the destination-wide figure when they do not.
func (d l7Detail) forEdge(key dstPortKey, portless bool) (float64, bool) {
	if portless {
		v, ok := d.perDst[dstKey{key.dstNs, key.dstName}]
		return v, ok
	}
	v, ok := d.perPort[key]
	return v, ok
}

func (s *BeylaSource) queryL7(ctx context.Context, opts FlowOptions) ([]Flow, error) {
	query := beylaL7RateQuery(opts.Namespace, beylaWindow(opts.Since))
	result, err := s.query(ctx, query)
	if err != nil {
		return nil, err
	}
	return s.parseL7Flows(result), nil
}

// parseL4Flows turns network-flow series into deduplicated flows, keyed while the
// labels are still in hand — the raw transport value and the owner types needed
// to resolve a collision are not carried on Flow. It also reports which optional
// attributes the scrape included.
func (s *BeylaSource) parseL4Flows(result *prom.QueryResult, w rateWindow) (map[l4Key]*Flow, l4LabelPresence) {
	flows := make(map[l4Key]*Flow)
	var presence l4LabelPresence
	if result == nil {
		return flows, presence
	}
	for _, series := range result.Series {
		labels := series.Labels
		if len(series.DataPoints) == 0 {
			continue
		}
		val := series.DataPoints[0].Value
		if !usableSample(val) {
			continue
		}

		if labels["dst_port"] != "" {
			presence.port = true
		}
		if labels["transport"] != "" {
			presence.transport = true
		}

		srcName := pickLabel(labels, "k8s_src_owner_name", "k8s_src_name")
		srcNs := labels["k8s_src_namespace"]
		srcType := pickLabel(labels, "k8s_src_owner_type", "k8s_src_type")
		dstName := pickLabel(labels, "k8s_dst_owner_name", "k8s_dst_name")
		dstNs := labels["k8s_dst_namespace"]
		dstType := pickLabel(labels, "k8s_dst_owner_type", "k8s_dst_type")
		port := parseIntLabel(labels["dst_port"])

		// Recorded before the drop below: a destination's real ports are a fact
		// about the destination, and the series that carries one may have an
		// unnameable source that never becomes a flow.
		if dstName != "" && port != 0 {
			if presence.portedDsts == nil {
				presence.portedDsts = make(map[dstKey]bool)
			}
			presence.portedDsts[dstKey{dstNs, dstName}] = true
		}

		// A nameless endpoint renders as an anonymous node the UI can't resolve
		// or navigate to, so drop the series rather than emit a phantom.
		if srcName == "" || dstName == "" {
			continue
		}

		flow := &Flow{
			Source:      Endpoint{Name: srcName, Namespace: srcNs, Kind: mapBeylaKind(srcType), Workload: srcName},
			Destination: Endpoint{Name: dstName, Namespace: dstNs, Kind: mapBeylaKind(dstType), Workload: dstName, Port: port},
			Protocol:    mapBeylaTransport(labels["transport"]),
			Port:        port,
			Verdict:     "forwarded",
			LastSeen:    time.Now(),
			BytesSent:   int64(val * w.seconds),
			// Deliberately not set here. Beyla exports rates, not connection counts,
			// so there is no number to put in it — Hubble's `Connections: 1` means
			// "one observed event" and sums to a real count, which does not carry
			// over to a source that emits one series per aggregate. Where HTTP data
			// exists the merge below fills it with the request rate, the way Istio
			// does; where it does not, it stays zero rather than claiming one.
		}

		if flow.Source.Namespace == "" && flow.Source.Name != "" {
			flow.Source.Kind = "External"
		}
		if flow.Destination.Namespace == "" && flow.Destination.Name != "" {
			flow.Destination.Kind = "External"
		}

		key := l4Key{
			srcNs: srcNs, srcName: srcName,
			dstNs: dstNs, dstName: dstName,
			dstPort: port, transport: labels["transport"],
		}
		if existing, ok := flows[key]; ok {
			flows[key] = preferL4(existing, flow)
			continue
		}
		flows[key] = flow
	}
	return flows, presence
}

// parseL7Flows reads http_server_request_duration_seconds_count series. The
// metric is server-side only, so Source is left empty here — getFlowsInternal
// attaches the caller identity from the matching L4 flow(s) instead. Port comes
// from server_port and is what the join keys on.
func (s *BeylaSource) parseL7Flows(result *prom.QueryResult) []Flow {
	if result == nil {
		return nil
	}
	flows := make([]Flow, 0, len(result.Series))
	for _, series := range result.Series {
		labels := series.Labels
		if len(series.DataPoints) == 0 {
			continue
		}
		val := series.DataPoints[0].Value
		if !usableSample(val) {
			continue
		}

		dstNs := labels["k8s_namespace_name"]
		dstName, dstKind := pickBeylaOwner(labels)
		if dstName == "" {
			continue
		}
		port := parseIntLabel(labels["server_port"])

		flow := Flow{
			Destination: Endpoint{Name: dstName, Namespace: dstNs, Kind: dstKind, Workload: dstName, Port: port},
			Port:        port,
			L7Protocol:  "HTTP",
			HTTPMethod:  labels["http_request_method"],
			HTTPPath:    labels["http_route"],
			HTTPStatus:  parseIntLabel(labels["http_response_status_code"]),
			Verdict:     "forwarded",
			LastSeen:    time.Now(),
			// val is a per-second request rate over a 5m window, not a
			// connection count; Connections here is only a non-zero weight for
			// downstream aggregation.
			RequestRate: val,
		}

		flows = append(flows, flow)
	}
	return flows
}

// pickBeylaOwner resolves the destination workload name Beyla attached to an
// HTTP server-duration series. k8s_owner_name is Beyla's own resolved owner —
// the same value the network-flow metric exposes as k8s_dst_owner_name — so L7
// results line up with L4 destinations for the same workload; a bare Pod (no
// owner) falls back to its pod name.
func pickBeylaOwner(labels map[string]string) (name, kind string) {
	switch {
	case labels["k8s_owner_name"] != "":
		return labels["k8s_owner_name"], "Workload"
	case labels["k8s_pod_name"] != "":
		return labels["k8s_pod_name"], "Pod"
	default:
		return "", ""
	}
}

func pickLabel(labels map[string]string, keys ...string) string {
	for _, k := range keys {
		if v, ok := labels[k]; ok && v != "" {
			return v
		}
	}
	return ""
}

func parseIntLabel(s string) int {
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return v
}

func mapBeylaKind(beylaType string) string {
	switch strings.ToLower(beylaType) {
	case "pod":
		return "Pod"
	case "deployment", "replicaset", "statefulset", "daemonset":
		return "Workload"
	case "service":
		return "Service"
	default:
		return "Pod"
	}
}

func mapBeylaTransport(transport string) string {
	switch strings.ToUpper(transport) {
	case "TCP":
		return "tcp"
	case "UDP":
		return "udp"
	default:
		return "tcp"
	}
}

func (s *BeylaSource) StreamFlows(ctx context.Context, opts FlowOptions) (<-chan Flow, error) {
	flowCh := make(chan Flow, 100)
	go func() {
		defer close(flowCh)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				flows, _, err := s.getFlowsInternal(ctx, opts)
				if err != nil {
					log.Printf("[beyla] Error fetching flows: %v", err)
					continue
				}
				response := &FlowsResponse{Flows: flows}
				for _, flow := range response.Flows {
					select {
					case flowCh <- flow:
					case <-ctx.Done():
						return
					default:
					}
				}
			}
		}
	}()
	return flowCh, nil
}
