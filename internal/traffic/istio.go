package traffic

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/skyhook-io/radar/internal/portforward"
	promclient "github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/pkg/prom"
)

// istiodSelector matches istiod Deployments by label rather than by name: a
// revisioned install is named istiod-<rev>, but the istiod chart labels every
// revision app=istiod.
const istiodSelector = "app=istiod"

// Namespaces where istiod is commonly deployed
var istioNamespaces = []string{"istio-system", "istio", "default"}

// IstioSource implements TrafficSource for Istio service mesh via Prometheus metrics.
// It uses the shared prometheus.Client for Prometheus discovery and queries,
// rather than maintaining its own connection state.
type IstioSource struct {
	k8sClient kubernetes.Interface
	queryFn   promQueryFunc
}

// NewIstioSource creates a new Istio traffic source
func NewIstioSource(client kubernetes.Interface) *IstioSource {
	s := &IstioSource{k8sClient: client}
	s.queryFn = s.defaultQuery
	return s
}

// Name returns the source identifier
func (s *IstioSource) Name() string {
	return "istio"
}

// Detect checks if Istio is available in the cluster by looking for istiod
func (s *IstioSource) Detect(ctx context.Context) (*DetectionResult, error) {
	result := &DetectionResult{
		Available: false,
	}

	for _, ns := range istioNamespaces {
		list, err := s.k8sClient.AppsV1().Deployments(ns).List(ctx, metav1.ListOptions{LabelSelector: istiodSelector})
		if err != nil || len(list.Items) == 0 {
			continue
		}

		deploys := list.Items
		sort.Slice(deploys, func(i, j int) bool {
			iReady, jReady := deploys[i].Status.ReadyReplicas > 0, deploys[j].Status.ReadyReplicas > 0
			if iReady != jReady {
				return iReady
			}
			return deploys[i].Name < deploys[j].Name
		})
		deploy := &deploys[0]

		totalReplicas := int32(1)
		if deploy.Spec.Replicas != nil {
			totalReplicas = *deploy.Spec.Replicas
		}

		if deploy.Status.ReadyReplicas > 0 {
			result.Available = true
			result.Message = fmt.Sprintf("Istio detected with istiod running in namespace %s (%d/%d ready)",
				ns, deploy.Status.ReadyReplicas, totalReplicas)

			var running []string
			for _, d := range deploys {
				if d.Status.ReadyReplicas > 0 {
					rev := d.Spec.Template.Labels["istio.io/rev"]
					if rev == "" {
						rev = d.Name
					}
					running = append(running, rev)
				}
			}
			sort.Strings(running)
			if len(running) > 1 {
				result.Message = fmt.Sprintf("Istio detected with %d istiod revisions running in namespace %s: %s",
					len(running), ns, strings.Join(running, ", "))
			}

			// Try to get version from pod labels
			if ver, ok := deploy.Spec.Template.Labels["istio.io/rev"]; ok && ver != "" {
				result.Version = ver
			} else if ver, ok := deploy.Labels["app.kubernetes.io/version"]; ok {
				result.Version = ver
			} else {
				// Extract version from istiod container image tag
				for _, c := range deploy.Spec.Template.Spec.Containers {
					if parts := strings.SplitN(c.Image, ":", 2); len(parts) == 2 {
						result.Version = parts[1]
						break
					}
				}
			}

			return result, nil
		}

		// Keep looking: a stale revision here must not hide a ready istiod in
		// a later namespace, so the first unready match is reported only if no
		// namespace has a ready one.
		if result.Message == "" {
			result.Message = fmt.Sprintf("istiod found in %s but not ready (%d/%d replicas)",
				ns, deploy.Status.ReadyReplicas, totalReplicas)
		}
	}

	if result.Message == "" {
		result.Message = "Istio not detected. Install Istio for service mesh traffic visibility."
	}
	return result, nil
}

// getPrometheusClient returns the shared prometheus client, or an error if unavailable
func (s *IstioSource) getPrometheusClient() (*promclient.Client, error) {
	client, connectionErr := promclient.ClientForOperation()
	if connectionErr != nil {
		return nil, connectionErr
	}
	return client, nil
}

// errIstioPrometheusUnavailable marks a query that never reached Prometheus
// because there is no client to send it through.
var errIstioPrometheusUnavailable = errors.New("prometheus not available for Istio metrics")

func (s *IstioSource) defaultQuery(ctx context.Context, query string) (*prom.QueryResult, error) {
	client, err := s.getPrometheusClient()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errIstioPrometheusUnavailable, err)
	}
	return client.Query(ctx, query)
}

// istioReporterSelector picks one side's report of each request: the
// destination proxy's.
const istioReporterSelector = `reporter="destination"`

const istioTCPMetric = "istio_tcp_connections_opened_total"

// istioScopes keeps each Istio metric family to this cluster's series: HTTP
// (istio_requests_total and its byte sums, reported by the same proxies) and
// TCP. They are checked separately, like Beyla's, so a family scraped without
// the cluster label does not empty the other.
type istioScopes struct {
	http, tcp clusterScope
	// tcpUnchecked is set when Radar could not check which cluster the TCP
	// series belong to. They are left out and named as missing, as a failed
	// TCP query is, rather than drawn unchecked.
	tcpUnchecked bool
}

// resolveScopes decides each family's cluster scope. Filtering applies only to
// a configured metrics store: one Radar discovered in the cluster holds this
// cluster's metrics.
func (s *IstioSource) resolveScopes(ctx context.Context) (istioScopes, error) {
	url, _ := metricsConfig()
	http, err := scopeForMetric(ctx, "istio_requests_total", istioReporterSelector, url != "")
	if err != nil {
		return istioScopes{}, err
	}
	tcp, err := scopeForMetric(ctx, istioTCPMetric, istioReporterSelector, url != "")
	return istioScopes{http: http, tcp: tcp, tcpUnchecked: err != nil}, nil
}

func (s *IstioSource) identityRows(ctx context.Context, query string) ([]map[string]string, error) {
	qr, err := s.queryFn(ctx, query)
	if err != nil || qr == nil {
		return nil, err
	}
	rows := make([]map[string]string, 0, len(qr.Series))
	for _, series := range qr.Series {
		rows = append(rows, series.Labels)
	}
	return rows, nil
}

// familyMismatch explains why the cluster matchers left a family with no
// series: it looks at the family's series present in the window without them.
func (s *IstioSource) familyMismatch(ctx context.Context, opts FlowOptions, sc clusterScope, metric string) string {
	if sc.matchers == "" {
		return ""
	}
	present := func(more string) string {
		return fmt.Sprintf(`last_over_time(%s{%s%s}[5m])`, metric, istioReporterSelector, more)
	}
	selector := present("")
	if opts.Namespace != "" {
		safeNS := prom.SanitizeLabelValue(opts.Namespace)
		selector = present(fmt.Sprintf(`, source_workload_namespace="%s"`, safeNS)) + " or " +
			present(fmt.Sprintf(`, destination_workload_namespace="%s"`, safeNS))
	}
	rows, err := s.identityRows(ctx, sc.identityCountQuery(selector))
	if err != nil {
		return ""
	}
	family := "Istio HTTP"
	if metric == istioTCPMetric {
		family = "Istio TCP"
	}
	return sc.clusterMismatchWarning(rows, family)
}

// errIstioTCPUnchecked stands in for the TCP query when its cluster scope
// could not be checked.
var errIstioTCPUnchecked = errors.New("could not check which cluster the TCP series belong to")

// GetFlows retrieves flows from Istio metrics via the shared Prometheus client
func (s *IstioSource) GetFlows(ctx context.Context, opts FlowOptions) (*FlowsResponse, error) {
	var missing []string

	scopes, err := s.resolveScopes(ctx)
	if err != nil {
		return scopeCheckFailed("istio"), nil
	}

	httpFlows, err := s.queryHTTPFlows(ctx, opts, scopes.http, &missing)
	if errors.Is(err, errIstioPrometheusUnavailable) {
		return &FlowsResponse{
			Source:    "istio",
			Timestamp: time.Now(),
			Flows:     []Flow{},
			Warning:   "Prometheus not available for Istio metrics",
		}, nil
	}
	if err != nil {
		log.Printf("[istio] Error querying HTTP flows: %v", err)
		return &FlowsResponse{
			Source:    "istio",
			Timestamp: time.Now(),
			Flows:     []Flow{},
			Warning:   fmt.Sprintf("Failed to query Prometheus for Istio metrics: %v", err),
		}, nil
	}

	httpCount := len(httpFlows)
	tcpFlows, tcpErr := []Flow(nil), errIstioTCPUnchecked
	if !scopes.tcpUnchecked {
		tcpFlows, tcpErr = s.queryTCPFlows(ctx, opts, scopes.tcp)
	}
	if tcpErr != nil {
		log.Printf("[istio] Error querying TCP flows (continuing with HTTP only): %v", tcpErr)
		missing = append(missing, "TCP connections")
	} else {
		httpFlows = append(httpFlows, tcpFlows...)
	}

	log.Printf("[istio] Retrieved %d flows from Prometheus", len(httpFlows))
	response := &FlowsResponse{
		Source:    "istio",
		Timestamp: time.Now(),
		Flows:     httpFlows,
	}
	// With no flows a missing enrichment qualifies nothing, but missing TCP
	// connections can be why there are no flows: a TCP-only mesh whose query
	// failed must not read as idle.
	if len(missing) > 0 && (len(httpFlows) > 0 || tcpErr != nil) {
		// Without this the edges read as measured: a failed 5xx query shows as no
		// errors, and failed TCP or byte queries as no traffic of that kind.
		response.Warning = fmt.Sprintf("Istio metrics are incomplete: %s could not be read from Prometheus, so those figures are missing from these edges rather than zero.", strings.Join(missing, ", "))
		if scopes.tcpUnchecked {
			response.Warning += " " + uncheckedFamilyNote
			response.WarningClusterScope = true
		}
		// Missing figures qualify the edges shown. Missing TCP connections are
		// edges that may not be shown at all, which holds whatever is filtered.
		response.WarningKind = WarningPartial
		if tcpErr != nil {
			response.WarningKind = WarningTransient
		}
	}
	mismatch := ""
	switch {
	case len(httpFlows) == 0:
		if mismatch = s.familyMismatch(ctx, opts, scopes.http, "istio_requests_total"); mismatch == "" && tcpErr == nil {
			mismatch = s.familyMismatch(ctx, opts, scopes.tcp, istioTCPMetric)
		}
	case httpCount == 0:
		// TCP connections drew, but the cluster filter may have removed every
		// HTTP series. Say so, or the map reads as carrying no requests.
		s.noteFamilyMismatch(ctx, opts, response, scopes.http, "istio_requests_total", "HTTP requests aren't shown on this map.")
	case len(tcpFlows) == 0 && tcpErr == nil:
		s.noteFamilyMismatch(ctx, opts, response, scopes.tcp, istioTCPMetric, "TCP connections aren't shown on this map.")
	}
	applyClusterScopeWarning(response, mismatch, scopes.http.mixed || scopes.tcp.mixed)
	return response, nil
}

// noteFamilyMismatch adds why one family is missing while the other drew.
// Its edges are missing, not figures on the edges shown, so the warning holds
// whatever a viewer's filters leave, as missing TCP connections do.
func (s *IstioSource) noteFamilyMismatch(ctx context.Context, opts FlowOptions, response *FlowsResponse, sc clusterScope, metric, lead string) {
	reason := s.familyMismatch(ctx, opts, sc, metric)
	if reason == "" {
		return
	}
	response.Warning = strings.TrimSpace(lead + " " + reason + " " + response.Warning)
	response.WarningClusterScope = true
	if response.WarningKind != WarningTransient {
		response.WarningKind = WarningIncomplete
	}
}

// flowKey uniquely identifies a source→destination service pair for map lookups
type flowKey struct {
	srcWorkload string
	srcNs       string
	dstWorkload string
	dstNs       string
}

// istioSeriesKey identifies one request series. A workload pair can carry
// several — one per destination Service and protocol — so every figure joined
// onto a series has to be keyed this finely, or the pair's total is attached to
// each of them and counted once per series.
type istioSeriesKey struct {
	flowKey
	dstService string
	protocol   string
}

// istioHTTPGroupBy is shared by every HTTP query so their series join one to one.
const istioHTTPGroupBy = "source_workload, source_workload_namespace, destination_workload, destination_workload_namespace, destination_service_name, request_protocol"

// istioRateQuery builds `sum by (groupBy) (rate(metric{reporter="destination"extra}[5m]))`
// within the cluster scope. A namespace filter has to become two OR'd
// selectors: PromQL cannot express "source OR destination namespace matches"
// inside one label selector.
func istioRateQuery(groupBy, metric, extra, namespace string, sc clusterScope) string {
	sum := func(more string) string {
		return fmt.Sprintf(`sum by (%s) (rate(%s{%s}[5m]))`, groupBy, metric, sc.with(istioReporterSelector+extra+more))
	}
	if namespace == "" {
		return sum("")
	}
	safeNS := prom.SanitizeLabelValue(namespace)
	return sum(fmt.Sprintf(`, source_workload_namespace="%s"`, safeNS)) + " or " +
		sum(fmt.Sprintf(`, destination_workload_namespace="%s"`, safeNS))
}

func istioSeriesKeyFrom(labels map[string]string) istioSeriesKey {
	return istioSeriesKey{
		flowKey: flowKey{
			srcWorkload: labels["source_workload"],
			srcNs:       labels["source_workload_namespace"],
			dstWorkload: labels["destination_workload"],
			dstNs:       labels["destination_workload_namespace"],
		},
		dstService: labels["destination_service_name"],
		protocol:   labels["request_protocol"],
	}
}

// queryHTTPSeriesRates reads an enrichment query's values by series. A zero or
// negative rate is no measurement, so it is left out rather than joined as one.
func (s *IstioSource) queryHTTPSeriesRates(ctx context.Context, query string) (map[istioSeriesKey]float64, error) {
	result, err := s.queryFn(ctx, query)
	if err != nil {
		return nil, err
	}
	rates := make(map[istioSeriesKey]float64)
	for _, series := range result.Series {
		if len(series.DataPoints) == 0 || series.DataPoints[0].Value <= 0 {
			continue
		}
		rates[istioSeriesKeyFrom(series.Labels)] += series.DataPoints[0].Value
	}
	return rates, nil
}

// queryHTTPFlows queries istio_requests_total for HTTP/gRPC traffic.
// Response codes are aggregated (not split per-code) and a separate error query
// provides 5xx error rates per series. Optional figures that cannot be read are
// appended to missing.
func (s *IstioSource) queryHTTPFlows(ctx context.Context, opts FlowOptions, sc clusterScope, missing *[]string) ([]Flow, error) {
	result, err := s.queryFn(ctx, istioRateQuery(istioHTTPGroupBy, "istio_requests_total", "", opts.Namespace, sc))
	if err != nil {
		return nil, err
	}

	errorRates, err := s.queryHTTPSeriesRates(ctx, istioRateQuery(istioHTTPGroupBy, "istio_requests_total", `, response_code=~"5.."`, opts.Namespace, sc))
	if err != nil {
		log.Printf("[istio] Error querying 5xx rates (continuing without error data): %v", err)
		*missing = append(*missing, "5xx error rates")
	}

	bytesSent, sentErr := s.queryHTTPSeriesRates(ctx, istioRateQuery(istioHTTPGroupBy, "istio_request_bytes_sum", "", opts.Namespace, sc))
	if sentErr != nil {
		log.Printf("[istio] Error querying request bytes (continuing without byte data): %v", sentErr)
	}
	bytesRecv, recvErr := s.queryHTTPSeriesRates(ctx, istioRateQuery(istioHTTPGroupBy, "istio_response_bytes_sum", "", opts.Namespace, sc))
	if recvErr != nil {
		log.Printf("[istio] Error querying response bytes (continuing without byte data): %v", recvErr)
	}
	if sentErr != nil || recvErr != nil {
		*missing = append(*missing, "request/response bytes")
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

		protocol := strings.ToLower(labels["request_protocol"])
		if protocol == "" {
			protocol = "http"
		}

		key := istioSeriesKeyFrom(labels)
		errRate := errorRates[key]

		verdict := "forwarded"
		if errRate > 0 {
			verdict = "error"
		}

		flow := Flow{
			Source: Endpoint{
				Name:      key.srcWorkload,
				Namespace: key.srcNs,
				Kind:      "Pod",
				Workload:  key.srcWorkload,
			},
			Destination: Endpoint{
				Name:      key.dstWorkload,
				Namespace: key.dstNs,
				Kind:      "Pod",
				Workload:  key.dstWorkload,
			},
			Protocol:    protocol,
			L7Protocol:  strings.ToUpper(protocol),
			DestService: key.dstService,
			// A fractional rate below one request a second still means traffic.
			Connections: RoundRate(val),
			// Approximate bytes from rate * window (5m = 300s)
			BytesSent:   int64(bytesSent[key] * 300),
			BytesRecv:   int64(bytesRecv[key] * 300),
			Verdict:     verdict,
			LastSeen:    time.Now(),
			RequestRate: val,
			ErrorRate:   errRate,
		}

		// Use destination service name if workload is unknown
		if flow.Destination.Name == "" || flow.Destination.Name == "unknown" {
			if key.dstService != "" {
				flow.Destination.Name = key.dstService
				flow.Destination.Kind = "Service"
			}
		}

		// Handle external sources (no namespace)
		if flow.Source.Namespace == "" && flow.Source.Name != "" {
			flow.Source.Kind = "External"
		}
		if flow.Destination.Namespace == "" && flow.Destination.Name != "" {
			flow.Destination.Kind = "External"
		}

		flows = append(flows, flow)
	}

	return flows, nil
}

// queryTCPFlows queries istio_tcp_connections_opened_total for TCP traffic
func (s *IstioSource) queryTCPFlows(ctx context.Context, opts FlowOptions, sc clusterScope) ([]Flow, error) {
	query := istioRateQuery("source_workload, source_workload_namespace, destination_workload, destination_workload_namespace, destination_service_name", istioTCPMetric, "", opts.Namespace, sc)

	result, err := s.queryFn(ctx, query)
	if err != nil {
		return nil, err
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

		srcWorkload := labels["source_workload"]
		srcNs := labels["source_workload_namespace"]
		dstWorkload := labels["destination_workload"]
		dstNs := labels["destination_workload_namespace"]
		dstService := labels["destination_service_name"]

		flow := Flow{
			Source: Endpoint{
				Name:      srcWorkload,
				Namespace: srcNs,
				Kind:      "Pod",
				Workload:  srcWorkload,
			},
			Destination: Endpoint{
				Name:      dstWorkload,
				Namespace: dstNs,
				Kind:      "Pod",
				Workload:  dstWorkload,
			},
			Protocol:    "tcp",
			DestService: dstService,
			Connections: RoundRate(val),
			Verdict:     "forwarded",
			LastSeen:    time.Now(),
		}

		if flow.Destination.Name == "" || flow.Destination.Name == "unknown" {
			if dstService != "" {
				flow.Destination.Name = dstService
				flow.Destination.Kind = "Service"
			}
		}

		if flow.Source.Namespace == "" && flow.Source.Name != "" {
			flow.Source.Kind = "External"
		}
		if flow.Destination.Namespace == "" && flow.Destination.Name != "" {
			flow.Destination.Kind = "External"
		}

		flows = append(flows, flow)
	}

	return flows, nil
}

// StreamFlows returns a channel of flows for real-time updates
func (s *IstioSource) StreamFlows(ctx context.Context, opts FlowOptions) (<-chan Flow, error) {
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
					log.Printf("[istio] Error fetching flows: %v", err)
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

// Connect triggers Prometheus discovery via the shared client.
// The shared prometheus.Client handles port-forwarding automatically.
func (s *IstioSource) Connect(ctx context.Context, contextName string) (*portforward.ConnectionInfo, error) {
	client, err := s.getPrometheusClient()
	if err != nil {
		return &portforward.ConnectionInfo{
			Connected: false,
			Error:     "Prometheus client not initialized",
		}, nil
	}

	// EnsureConnected triggers discovery + port-forward if needed
	_, _, err = client.EnsureConnected(ctx)
	if err != nil {
		return &portforward.ConnectionInfo{
			Connected: false,
			Error:     fmt.Sprintf("Failed to connect to Prometheus: %v", err),
		}, nil
	}

	status := client.GetStatus()
	info := &portforward.ConnectionInfo{
		Connected:   true,
		Address:     status.Address,
		ContextName: contextName,
	}
	if status.Service != nil {
		info.Namespace = status.Service.Namespace
		info.ServiceName = status.Service.Name
	}

	return info, nil
}

// ConnectionInfo implements ConnectionReporter. Istio's data path is the
// shared Prometheus client (whose forwards belong to the prometheus owner, not
// traffic), so its status is the only honest answer here.
func (s *IstioSource) ConnectionInfo() *portforward.ConnectionInfo {
	client, err := s.getPrometheusClient()
	if err != nil {
		return &portforward.ConnectionInfo{Connected: false}
	}
	return connectionInfoFromPromStatus(client.GetStatus())
}

// connectionInfoFromPromStatus maps the shared Prometheus client's status into
// traffic connection info — used by the sources whose data path is that client.
func connectionInfoFromPromStatus(status prom.Status) *portforward.ConnectionInfo {
	info := &portforward.ConnectionInfo{
		Connected:   status.Connected,
		Address:     status.Address,
		ContextName: status.ContextName,
	}
	if status.Service != nil {
		info.Namespace = status.Service.Namespace
		info.ServiceName = status.Service.Name
	}
	return info
}

// Close cleans up resources (no-op since we use the shared prometheus client)
func (s *IstioSource) Close() error {
	return nil
}
