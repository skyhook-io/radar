package traffic

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
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

// BeylaSource implements TrafficSource for Grafana Beyla via Prometheus metrics.
type BeylaSource struct {
	k8sClient kubernetes.Interface
	queryFn   promQueryFunc
}

// NewBeylaSource creates a new Beyla traffic source wired to the shared Prometheus client.
func NewBeylaSource(client kubernetes.Interface) *BeylaSource {
	s := &BeylaSource{k8sClient: client}
	s.queryFn = s.defaultQuery
	return s
}

func (s *BeylaSource) Name() string { return "beyla" }

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

func (s *BeylaSource) Close() error { return nil }

func (s *BeylaSource) Detect(ctx context.Context) (*DetectionResult, error) {
	result := &DetectionResult{Available: false}

	// Phase 1: metric probe via Prometheus. Scoped to the same jobs the flow
	// queries read, so detection can't succeed on metrics GetFlows won't see.
	qr, err := s.query(ctx, fmt.Sprintf(`count(beyla_network_flow_bytes_total{%s})`, beylaJobSelector()))
	if err == nil && qr != nil && len(qr.Series) > 0 {
		result.Available = true
		result.Native = false
		result.Message = "Beyla detected via Prometheus metrics"
		result.Version = s.detectVersion(ctx)
		return result, nil
	}

	// Phase 2: pod label fallback
	if pods := s.countBeylaPods(ctx); pods > 0 {
		result.Available = true
		result.Native = false
		result.Message = fmt.Sprintf("Beyla detected via %d running pod(s) (Alloy or standalone)", pods)
		return result, nil
	}

	result.Message = "Beyla not detected. Install Alloy + Beyla for L7 traffic visibility."
	return result, nil
}

func (s *BeylaSource) detectVersion(ctx context.Context) string {
	qr, err := s.query(ctx, `beyla_build_info`)
	if err != nil {
		return ""
	}
	for _, series := range qr.Series {
		if v := series.Labels["version"]; v != "" {
			return v
		}
		if v := series.Labels["beyla_version"]; v != "" {
			return v
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

// l4Key uniquely identifies an L4 flow for dedup. Must include every label
// beylaL4GroupBy groups by (dst_port, transport) — otherwise distinct series
// (e.g. TCP and UDP to the same endpoint/port) collide and one is dropped.
type l4Key struct {
	srcNs, srcName string
	dstNs, dstName string
	dstPort        int
	protocol       string
}

// dstKey identifies a destination workload only. Beyla's HTTP server-duration
// metric is recorded server-side and carries no source labels at all — it
// can't be paired by (src, dst) the way the network-flow metric can — so L7
// results are matched onto L4 flows by destination alone.
type dstKey struct {
	dstNs, dstName string
}

func (s *BeylaSource) GetFlows(ctx context.Context, opts FlowOptions) (*FlowsResponse, error) {
	flows, err := s.getFlowsInternal(ctx, opts)
	if err != nil {
		log.Printf("[beyla] Error fetching flows: %v", err)
		return &FlowsResponse{Source: "beyla", Timestamp: time.Now(), Flows: []Flow{},
			Warning: fmt.Sprintf("Failed to query Beyla metrics: %v", err)}, nil
	}
	return &FlowsResponse{Source: "beyla", Timestamp: time.Now(), Flows: flows}, nil
}

func (s *BeylaSource) getFlowsInternal(ctx context.Context, opts FlowOptions) ([]Flow, error) {
	l4Flows, dstPorts, err := s.queryL4(ctx, opts)
	if err != nil {
		return nil, fmt.Errorf("L4 query: %w", err)
	}

	l7Flows, err := s.queryL7(ctx, opts)
	if err != nil {
		log.Printf("[beyla] L7 query failed (continuing with L4 only): %v", err)
		l7Flows = nil
	}

	l4Map := make(map[l4Key]*Flow, len(l4Flows))
	byDst := make(map[dstKey][]*Flow, len(l4Flows))
	for i := range l4Flows {
		f := &l4Flows[i]
		l4Map[l4FlowKey(f)] = f
		dst := dstKey{f.Destination.Namespace, f.Destination.Name}
		byDst[dst] = append(byDst[dst], f)
	}

	// Merge L7 into L4 by destination only: the HTTP server-duration metric is
	// recorded on the serving side and has no source labels, so there's no way
	// to attribute a request to a particular caller. A destination may have
	// several L4 edges (different callers/ports); each gets a share of the
	// destination's total HTTP metadata. L7 series with no matching L4
	// destination are dropped — without a source there's no edge to draw.
	for _, l7 := range busiestL7PerDst(l7Flows) {
		dst := dstKey{l7.Destination.Namespace, l7.Destination.Name}
		existing, ok := byDst[dst]
		if !ok {
			continue
		}
		// The HTTP metric has no port label, so if this destination fans out
		// over more than one distinct L4 port there's no way to tell which one
		// is actually HTTP (e.g. an app port alongside a raw-TCP DB port) —
		// skip rather than mislabel every port as HTTP. Checked against
		// dstPorts (every port Beyla reported for this destination) rather
		// than len(existing): a port can be dropped from existing/l4Map for
		// having an unresolved (e.g. external) source name while still being
		// the real HTTP port, and existing's remaining single port would
		// otherwise look unambiguous.
		if len(dstPorts[dst]) != 1 {
			continue
		}
		// l7.RequestRate describes the whole destination, so it must be split
		// across its L4 edges rather than copied onto each one — otherwise
		// aggregation overcounts by the number of edges. Weight the split by
		// each edge's L4 byte volume as the best available proxy for its share
		// of the destination's request traffic.
		var totalBytes int64
		for _, f := range existing {
			totalBytes += f.BytesSent + f.BytesRecv
		}
		for _, f := range existing {
			f.L7Protocol = l7.L7Protocol
			f.HTTPMethod = l7.HTTPMethod
			f.HTTPPath = l7.HTTPPath
			f.HTTPStatus = l7.HTTPStatus
			if totalBytes > 0 {
				share := float64(f.BytesSent+f.BytesRecv) / float64(totalBytes)
				f.RequestRate = l7.RequestRate * share
			} else {
				f.RequestRate = l7.RequestRate / float64(len(existing))
			}
		}
	}

	result := make([]Flow, 0, len(l4Map))
	for _, f := range l4Map {
		result = append(result, *f)
	}
	return result, nil
}

func busiestL7PerDst(l7Flows []Flow) []Flow {
	best := make(map[dstKey]Flow, len(l7Flows))
	topRate := make(map[dstKey]float64, len(l7Flows))
	for _, f := range l7Flows {
		dst := dstKey{f.Destination.Namespace, f.Destination.Name}
		cur, ok := best[dst]
		if !ok {
			best[dst], topRate[dst] = f, f.RequestRate
			continue
		}
		// Rates cover the whole destination; the route/method/status shown is
		// the busiest single series.
		if f.RequestRate > topRate[dst] {
			topRate[dst] = f.RequestRate
			cur.HTTPMethod, cur.HTTPPath, cur.HTTPStatus = f.HTTPMethod, f.HTTPPath, f.HTTPStatus
		}
		cur.RequestRate += f.RequestRate
		best[dst] = cur
	}
	out := make([]Flow, 0, len(best))
	for _, f := range best {
		out = append(out, f)
	}
	return out
}

func l4FlowKey(f *Flow) l4Key {
	return l4Key{
		srcNs: f.Source.Namespace, srcName: f.Source.Name,
		dstNs: f.Destination.Namespace, dstName: f.Destination.Name,
		dstPort: f.Port, protocol: f.Protocol,
	}
}

const (
	beylaL4GroupBy = `k8s_src_owner_name, k8s_src_namespace, k8s_src_owner_type, k8s_dst_owner_name, k8s_dst_namespace, k8s_dst_owner_type, dst_port, transport`
	// beylaL7Metric is Beyla's OTel-aligned HTTP server histogram; there is no
	// millisecond variant, and it carries no source-side labels at all (see
	// beylaL7GroupBy below).
	beylaL7Metric = "http_server_request_duration_seconds_count"
	// beylaL7GroupBy labels come from http_server_request_duration_seconds,
	// which is recorded server-side only. k8s_owner_name is Beyla's own
	// resolved owner (Deployment/ReplicaSet/StatefulSet/DaemonSet, whichever
	// applies) — the same concept as k8s_dst_owner_name on the L4 metric, so
	// destinations from both metrics line up. No caller/source labels exist
	// on this metric at all, unlike beyla_network_flow_bytes_total.
	beylaL7GroupBy = `k8s_namespace_name, k8s_owner_name, k8s_pod_name, http_request_method, http_route, http_response_status_code`
)

// beylaRateQuery builds `sum by (groupBy) (rate(metric{job=~...}[5m]))`. A
// namespace filter has to become two OR'd selectors: PromQL cannot express
// "src OR dst namespace matches" inside a single label selector.
func beylaRateQuery(groupBy, metric, namespace string) string {
	sum := func(extra string) string {
		return fmt.Sprintf(`sum by (%s) (rate(%s{%s%s}[5m]))`, groupBy, metric, beylaJobSelector(), extra)
	}
	if namespace == "" {
		return sum("")
	}
	return sum(fmt.Sprintf(`, k8s_src_namespace=%q`, namespace)) + " or " +
		sum(fmt.Sprintf(`, k8s_dst_namespace=%q`, namespace))
}

// beylaL7RateQuery builds the L7 query. Unlike beylaRateQuery, there's only
// one namespace label to filter on (k8s_namespace_name) since the metric has
// no source side.
func beylaL7RateQuery(namespace string) string {
	extra := ""
	if namespace != "" {
		extra = fmt.Sprintf(`, k8s_namespace_name=%q`, namespace)
	}
	return fmt.Sprintf(`sum by (%s) (rate(%s{%s%s}[5m]))`, beylaL7GroupBy, beylaL7Metric, beylaJobSelector(), extra)
}

func (s *BeylaSource) queryL4(ctx context.Context, opts FlowOptions) ([]Flow, map[dstKey]map[int]struct{}, error) {
	query := beylaRateQuery(beylaL4GroupBy, "beyla_network_flow_bytes_total", opts.Namespace)
	result, err := s.query(ctx, query)
	if err != nil {
		return nil, nil, err
	}
	flows, dstPorts := s.parseL4Flows(result)
	return flows, dstPorts, nil
}

func (s *BeylaSource) queryL7(ctx context.Context, opts FlowOptions) ([]Flow, error) {
	query := beylaL7RateQuery(opts.Namespace)
	result, err := s.query(ctx, query)
	if err != nil {
		return nil, err
	}
	return s.parseL7Flows(result), nil
}

// parseL4Flows returns the L4 flows plus, separately, every distinct
// destination port Beyla reported traffic on for each destination —
// including ports whose flow got dropped below for an unresolved source
// name. Callers that need to know whether a destination is unambiguously
// single-port (e.g. before trusting L7 HTTP metadata has the right target)
// must use the latter, not the port set of the returned flows: a real HTTP
// port can be the one that gets dropped (external caller, no owner name),
// leaving a surviving non-HTTP port that would otherwise look unambiguous.
func (s *BeylaSource) parseL4Flows(result *prom.QueryResult) ([]Flow, map[dstKey]map[int]struct{}) {
	if result == nil {
		return nil, nil
	}
	flows := make([]Flow, 0, len(result.Series))
	dstPorts := make(map[dstKey]map[int]struct{})
	for _, series := range result.Series {
		labels := series.Labels
		if len(series.DataPoints) == 0 {
			continue
		}
		val := series.DataPoints[0].Value
		if val <= 0 {
			continue
		}

		srcName := pickLabel(labels, "k8s_src_owner_name", "k8s_src_name")
		srcNs := labels["k8s_src_namespace"]
		srcType := pickLabel(labels, "k8s_src_owner_type", "k8s_src_type")
		dstName := pickLabel(labels, "k8s_dst_owner_name", "k8s_dst_name")
		dstNs := labels["k8s_dst_namespace"]
		dstType := pickLabel(labels, "k8s_dst_owner_type", "k8s_dst_type")
		port := parseIntLabel(labels["dst_port"])

		if dstName != "" {
			dst := dstKey{dstNs, dstName}
			if dstPorts[dst] == nil {
				dstPorts[dst] = make(map[int]struct{})
			}
			dstPorts[dst][port] = struct{}{}
		}

		// A nameless endpoint renders as an anonymous node the UI can't resolve
		// or navigate to, so drop the series rather than emit a phantom.
		if srcName == "" || dstName == "" {
			continue
		}

		flow := Flow{
			Source:      Endpoint{Name: srcName, Namespace: srcNs, Kind: mapBeylaKind(srcType), Workload: srcName},
			Destination: Endpoint{Name: dstName, Namespace: dstNs, Kind: mapBeylaKind(dstType), Workload: dstName, Port: port},
			Protocol:    mapBeylaTransport(labels["transport"]),
			Port:        port,
			Verdict:     "forwarded",
			LastSeen:    time.Now(),
			BytesSent:   int64(val * beylaRateWindowSeconds),
			Connections: 1,
		}

		if flow.Source.Namespace == "" && flow.Source.Name != "" {
			flow.Source.Kind = "External"
		}
		if flow.Destination.Namespace == "" && flow.Destination.Name != "" {
			flow.Destination.Kind = "External"
		}

		flows = append(flows, flow)
	}
	return flows, dstPorts
}

// parseL7Flows reads http_server_request_duration_seconds_count series. The
// metric is server-side only, so Source is left empty here — getFlowsInternal
// attaches the caller identity from the matching L4 flow(s) instead.
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
		if val <= 0 {
			continue
		}

		dstNs := labels["k8s_namespace_name"]
		dstName, dstKind := pickBeylaOwner(labels)
		if dstName == "" {
			continue
		}

		flow := Flow{
			Destination: Endpoint{Name: dstName, Namespace: dstNs, Kind: dstKind, Workload: dstName},
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
			Connections: max(int64(val*beylaRateWindowSeconds), 1),
		}

		flows = append(flows, flow)
	}
	return flows
}

// pickBeylaOwner resolves the destination workload name Beyla attached to an
// HTTP server-duration series. k8s_owner_name is Beyla's own resolved owner —
// the same value beyla_network_flow_bytes_total exposes as k8s_dst_owner_name
// — so L7 results line up with L4 destinations for the same workload; a bare
// Pod (no owner) falls back to its pod name.
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
				response, err := s.GetFlows(ctx, opts)
				if err != nil {
					log.Printf("[beyla] Error fetching flows: %v", err)
					continue
				}
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
