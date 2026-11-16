package traffic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"slices"
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
	k8sClient          kubernetes.Interface
	queryFn            promQueryFunc
	waypointsFn        func() map[istioWorkload]bool
	ambientNamespaceFn func() []string
}

// NewIstioSource creates a new Istio traffic source
func NewIstioSource(client kubernetes.Interface) *IstioSource {
	s := &IstioSource{k8sClient: client, waypointsFn: cachedIstioWaypoints, ambientNamespaceFn: cachedAmbientNamespaces}
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
	return client.QueryPost(ctx, query)
}

// istioReporterSelector picks the receiving side's report of each request:
// the destination sidecar's, or in ambient mode the destination's waypoint's.
// A destination is either sidecar-injected or behind a waypoint, so the two
// never report the same request.
const istioReporterSelector = `reporter=~"destination|waypoint"`

// istioAllReportsSelector also takes client proxies' reports. The HTTP scope
// checks use it while client reports into ambient namespaces are read, so
// those too are kept to this cluster's series.
const istioAllReportsSelector = `reporter=~"destination|waypoint|source"`

const istioTCPMetric = "istio_tcp_connections_opened_total"

// istioScopes keeps each Istio metric family to this cluster's series: HTTP
// (istio_requests_total and its byte sums, reported by the same proxies) and
// TCP. They are checked separately, like Beyla's, so a family scraped without
// the cluster label does not empty the other.
type istioScopes struct {
	http, tcp clusterScope
	// httpSelector is the reporter selector the HTTP scope was checked with.
	httpSelector string
	// tcpUnchecked is set when Radar could not check which cluster the TCP
	// series belong to. They are left out and named as missing, as a failed
	// TCP query is, rather than drawn unchecked.
	tcpUnchecked bool
}

// resolveScopes decides each family's cluster scope. Filtering applies only to
// a configured metrics store: one Radar discovered in the cluster holds this
// cluster's metrics.
func (s *IstioSource) resolveScopes(ctx context.Context, ambientNamespaces []string, w rateWindow) (istioScopes, error) {
	url, _ := metricsConfig()
	httpSelector := istioReporterSelector
	if len(ambientNamespaces) > 0 {
		httpSelector = istioAllReportsSelector
	}
	http, err := scopeForMetric(ctx, "istio_requests_total", httpSelector, url != "", w.span())
	if err != nil {
		return istioScopes{}, err
	}
	tcp, err := scopeForMetric(ctx, istioTCPMetric, istioReporterSelector, url != "", w.span())
	return istioScopes{http: http, tcp: tcp, httpSelector: httpSelector, tcpUnchecked: err != nil}, nil
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
func (s *IstioSource) familyMismatch(ctx context.Context, opts FlowOptions, sc clusterScope, metric, reporters string) string {
	if sc.matchers == "" {
		return ""
	}
	present := func(more string) string {
		return fmt.Sprintf(`last_over_time(%s{%s%s}[%s])`, metric, reporters, more, rateWindowFor(opts.Since).promQL)
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

// istioQueryFailure describes a failed query for the viewer without the
// request URL, which carries the query. When the query names the cluster's
// ambient namespaces, including ones the viewer cannot see, the store's own
// message is left out too, since some stores quote the query in it; its error
// type, a fixed vocabulary, still says what kind of failure it was. The server
// log keeps the full error.
func istioQueryFailure(err error, namesAmbient bool) string {
	var httpErr *prom.HTTPError
	if errors.As(err, &httpErr) {
		var body struct {
			ErrorType string `json:"errorType"`
			Error     string `json:"error"`
		}
		parsed := json.Unmarshal(httpErr.Body, &body) == nil
		switch {
		case namesAmbient && parsed && body.ErrorType != "":
			return fmt.Sprintf("Prometheus answered HTTP %d (%s)", httpErr.StatusCode, body.ErrorType)
		case namesAmbient:
			return fmt.Sprintf("Prometheus answered HTTP %d", httpErr.StatusCode)
		case parsed && body.Error != "":
			return fmt.Sprintf("Prometheus answered HTTP %d: %s", httpErr.StatusCode, body.Error)
		}
		detail := strings.TrimSpace(strings.ToValidUTF8(string(httpErr.Body), ""))
		if len(detail) > 512 {
			detail = strings.ToValidUTF8(detail[:512], "") + "…"
		}
		return strings.TrimSuffix(fmt.Sprintf("Prometheus answered HTTP %d: %s", httpErr.StatusCode, detail), ": ")
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if !namesAmbient {
			return urlErr.Err.Error()
		}
		// A transport error can quote the URL too, as a redirect's Location
		// does; a timeout or a failed dial says what happened without it.
		var opErr *net.OpError
		switch {
		case urlErr.Timeout():
			return "the request to Prometheus timed out"
		case errors.As(urlErr.Err, &opErr):
			return opErr.Error()
		}
	}
	if namesAmbient {
		return "Prometheus returned an error; Radar's server log has the details"
	}
	return err.Error()
}

// errIstioTCPUnchecked stands in for the TCP query when its cluster scope
// could not be checked.
var errIstioTCPUnchecked = errors.New("could not check which cluster the TCP series belong to")

// GetFlows retrieves flows from Istio metrics via the shared Prometheus client
func (s *IstioSource) GetFlows(ctx context.Context, opts FlowOptions) (*FlowsResponse, error) {
	var missing []string

	ambientNamespaces := s.ambientNamespaceFn()
	ambientKnown := ambientNamespaces
	scopes, err := s.resolveScopes(ctx, ambientNamespaces, rateWindowFor(opts.Since))
	if err != nil {
		return scopeCheckFailed("istio"), nil
	}
	longest := istioHTTPQuery("istio_requests_total", istio5xxSelector, opts.Namespace, scopes.http, ambientNamespaces, rateWindowFor(opts.Since))
	ambientOverBudget := len(ambientNamespaces) > 0 && len(longest) > istioQueryBudget
	if ambientOverBudget {
		log.Printf("[istio] %d ambient namespaces are too many to name in one query; client reports into them are not read", len(ambientNamespaces))
		ambientNamespaces = nil
		// The HTTP scope was checked against client reports that will not be read.
		if scopes, err = s.resolveScopes(ctx, nil, rateWindowFor(opts.Since)); err != nil {
			return scopeCheckFailed("istio"), nil
		}
	}

	httpFlows, err := s.queryHTTPFlows(ctx, opts, scopes.http, ambientNamespaces, &missing)
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
			Warning:   fmt.Sprintf("Failed to query Prometheus for Istio metrics: %s", istioQueryFailure(err, len(ambientNamespaces) > 0)),
		}, nil
	}

	waypoints := s.waypointsFn()
	httpFlows = withoutWaypointHops(httpFlows, waypoints)
	httpCount := len(httpFlows)
	tcpFlows, tcpErr := []Flow(nil), errIstioTCPUnchecked
	ztunnelReported := false
	if !scopes.tcpUnchecked {
		var reportedInto []string
		tcpFlows, reportedInto, tcpErr = s.queryTCPFlows(ctx, opts, scopes.tcp)
		ztunnelReported = slices.ContainsFunc(reportedInto, func(ns string) bool { return slices.Contains(ambientKnown, ns) })
		tcpFlows = withoutWaypointHops(tcpFlows, waypoints)
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
		if mismatch = s.familyMismatch(ctx, opts, scopes.http, "istio_requests_total", scopes.httpSelector); mismatch == "" && tcpErr == nil {
			mismatch = s.familyMismatch(ctx, opts, scopes.tcp, istioTCPMetric, istioReporterSelector)
		}
	case httpCount == 0:
		// TCP connections drew, but the cluster filter may have removed every
		// HTTP series. Say so, or the map reads as carrying no requests.
		s.noteFamilyMismatch(ctx, opts, response, scopes.http, "istio_requests_total", scopes.httpSelector, "HTTP requests aren't shown on this map.")
	case len(tcpFlows) == 0 && tcpErr == nil:
		s.noteFamilyMismatch(ctx, opts, response, scopes.tcp, istioTCPMetric, istioReporterSelector, "TCP connections aren't shown on this map.")
	}
	// Without a TCP read there is no evidence either way, and the TCP
	// warning already says so.
	if tcpErr == nil && !ztunnelReported && ambientInView(opts, ambientKnown, httpFlows) && !s.ztunnelScraped(ctx) {
		response.Warning = strings.TrimSpace(istioZtunnelUnscrapedNote + " " + response.Warning)
		if response.WarningKind != WarningTransient {
			response.WarningKind = WarningIncomplete
		}
	}
	if ambientOverBudget {
		response.Warning = strings.TrimSpace("Request and error rates for calls into ambient workloads without a waypoint aren't shown, because this cluster has more ambient namespaces than Radar can include in its Prometheus query. Those calls can still appear as TCP edges. " + response.Warning)
		if response.WarningKind != WarningTransient {
			response.WarningKind = WarningIncomplete
		}
	}
	applyClusterScopeWarning(response, mismatch, scopes.http.mixed || scopes.tcp.mixed)
	if len(response.Flows) == 0 && response.Warning == "" && s.tooFewSamples(ctx, opts, scopes) {
		response.Warning = istioTooFewSamplesNote
		response.WarningKind = WarningIncomplete
	}
	return response, nil
}

const istioTooFewSamplesNote = "Prometheus has Istio metrics in this time range but fewer than two samples per series, so no rate can be taken over it: its scrape interval is longer than this range allows. Choose a longer time range."

// tooFewSamples reports whether a short window is why the map is empty: the
// metrics are present in it, but rate needs two samples and none has them, as
// with a one-minute range over a one-minute scrape interval. A check that
// fails reports false, leaving the empty map as it was.
func (s *IstioSource) tooFewSamples(ctx context.Context, opts FlowOptions, scopes istioScopes) bool {
	w := rateWindowFor(opts.Since)
	if w.seconds >= defaultRateWindowSeconds {
		return false
	}
	present := false
	for _, family := range []struct {
		metric, reporters string
		sc                clusterScope
	}{
		{"istio_requests_total", scopes.httpSelector, scopes.http},
		{istioTCPMetric, istioReporterSelector, scopes.tcp},
	} {
		samples := func(more string) string {
			return fmt.Sprintf(`count_over_time(%s{%s}[%s])`, family.metric, family.sc.with(family.reporters+more), w.promQL)
		}
		selector := samples("")
		if opts.Namespace != "" {
			safeNS := prom.SanitizeLabelValue(opts.Namespace)
			selector = samples(fmt.Sprintf(`, source_workload_namespace="%s"`, safeNS)) + " or " +
				samples(fmt.Sprintf(`, destination_workload_namespace="%s"`, safeNS))
		}
		result, err := s.queryFn(ctx, fmt.Sprintf(`max(%s)`, selector))
		if err != nil || result == nil || len(result.Series) == 0 || len(result.Series[0].DataPoints) == 0 {
			continue
		}
		if result.Series[0].DataPoints[0].Value >= 2 {
			return false
		}
		present = true
	}
	return present
}

// istioZtunnelProbe finds ztunnel's build info, which it exports whether or
// not it carries traffic, under a label ztunnel sets itself rather than one a
// scrape config adds. It is not kept to this cluster: another cluster's
// ztunnel can only hide the note, never raise it falsely.
const istioZtunnelProbe = `count(istio_build{component="ztunnel"})`

const istioZtunnelUnscrapedNote = "Radar found no metrics from ztunnel, Istio's ambient node proxy, so TCP traffic into ambient workloads without a waypoint can't be shown. Check that Prometheus scrapes the ztunnel pods (port 15020) and keeps their metrics."

// ambientInView reports whether the view can hold traffic of an ambient
// namespace: the caller's namespaces include one, or a flow from them goes
// into one. No namespaces means the whole cluster.
func ambientInView(opts FlowOptions, ambient []string, flows []Flow) bool {
	if len(ambient) == 0 {
		return false
	}
	if len(opts.Namespaces) == 0 && opts.Namespace == "" {
		return true
	}
	inView := opts.Namespaces
	if len(inView) == 0 {
		inView = []string{opts.Namespace}
	}
	if slices.ContainsFunc(inView, func(ns string) bool { return slices.Contains(ambient, ns) }) {
		return true
	}
	return slices.ContainsFunc(flows, func(f Flow) bool {
		return slices.Contains(inView, f.Source.Namespace) && slices.Contains(ambient, f.Destination.Namespace)
	})
}

// ztunnelScraped reports whether Prometheus holds ztunnel's metrics. A check
// that fails, or whose answer the store marks partial or warns about, counts
// as scraped, so the note is only raised on a complete answer.
func (s *IstioSource) ztunnelScraped(ctx context.Context) bool {
	result, err := s.queryFn(ctx, istioZtunnelProbe)
	if err != nil {
		log.Printf("[istio] Could not check whether ztunnel is scraped: %v", err)
		return true
	}
	if result == nil || result.Annotations.Partial || len(result.Annotations.Warnings) > 0 {
		return true
	}
	return len(result.Series) > 0
}

// noteFamilyMismatch adds why one family is missing while the other drew.
// Its edges are missing, not figures on the edges shown, so the warning holds
// whatever a viewer's filters leave, as missing TCP connections do.
func (s *IstioSource) noteFamilyMismatch(ctx context.Context, opts FlowOptions, response *FlowsResponse, sc clusterScope, metric, reporters, lead string) {
	reason := s.familyMismatch(ctx, opts, sc, metric, reporters)
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

// istioRateQuery builds `sum by (groupBy) (rate(metric{selector}[window]))`
// within the cluster scope. A namespace filter has to become two OR'd
// selectors: PromQL cannot express "source OR destination namespace matches"
// inside one label selector.
func istioRateQuery(groupBy, metric, selector, namespace string, sc clusterScope, w rateWindow) string {
	sum := func(more string) string {
		return fmt.Sprintf(`sum by (%s) (rate(%s{%s}[%s]))`, groupBy, metric, sc.with(selector+more), w.promQL)
	}
	if namespace == "" {
		return sum("")
	}
	safeNS := prom.SanitizeLabelValue(namespace)
	return sum(fmt.Sprintf(`, source_workload_namespace="%s"`, safeNS)) + " or " +
		sum(fmt.Sprintf(`, destination_workload_namespace="%s"`, safeNS))
}

// istioQueryBudget bounds the HTTP query once it names the ambient namespaces.
// It is sent in a POST body, but stores limit query length too
// (VictoriaMetrics' default is 16 KiB), and a longer query would fail the whole
// HTTP query rather than only the client reports.
const istioQueryBudget = prom.MaxWorkloadQueryBytes

const istio5xxSelector = `, response_code=~"5.."`

// istioHTTPQuery reads one HTTP figure by series from the side that received
// the requests. Requests into an ambient namespace that no receiving proxy
// reported (a sidecar or ingress gateway calling a workload with no waypoint,
// or one that skipped it) are read from the client proxy instead, as Kiali
// does. Every figure of a series comes from one side: the receiving side's
// when it counted any of the series' requests in the window, the client's
// otherwise, so the two reports never add up. The test is on the request
// rate, not on presence: a receiver's counter that stopped moving is still
// present.
func istioHTTPQuery(metric, extra, namespace string, sc clusterScope, ambientNamespaces []string, w rateWindow) string {
	received := istioRateQuery(istioHTTPGroupBy, metric, istioReporterSelector+extra, namespace, sc, w)
	if len(ambientNamespaces) == 0 {
		return received
	}
	intoAmbient := fmt.Sprintf(`reporter="source", destination_workload_namespace=~"%s"`, strings.Join(ambientNamespaces, "|"))
	sent := istioRateQuery(istioHTTPGroupBy, metric, intoAmbient+extra, namespace, sc, w)
	receivedAny := fmt.Sprintf(`(%s) > 0`, istioRateQuery(istioHTTPGroupBy, "istio_requests_total", istioReporterSelector, namespace, sc, w))
	return fmt.Sprintf(`((%s) and (%s)) or ((%s) unless (%s))`, received, receivedAny, sent, receivedAny)
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
func (s *IstioSource) queryHTTPFlows(ctx context.Context, opts FlowOptions, sc clusterScope, ambientNamespaces []string, missing *[]string) ([]Flow, error) {
	w := rateWindowFor(opts.Since)
	query := func(metric, extra string) string {
		return istioHTTPQuery(metric, extra, opts.Namespace, sc, ambientNamespaces, w)
	}
	result, err := s.queryFn(ctx, query("istio_requests_total", ""))
	if err != nil {
		return nil, err
	}

	errorRates, err := s.queryHTTPSeriesRates(ctx, query("istio_requests_total", istio5xxSelector))
	if err != nil {
		log.Printf("[istio] Error querying 5xx rates (continuing without error data): %v", err)
		*missing = append(*missing, "5xx error rates")
	}

	bytesSent, sentErr := s.queryHTTPSeriesRates(ctx, query("istio_request_bytes_sum", ""))
	if sentErr != nil {
		log.Printf("[istio] Error querying request bytes (continuing without byte data): %v", sentErr)
	}
	bytesRecv, recvErr := s.queryHTTPSeriesRates(ctx, query("istio_response_bytes_sum", ""))
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
			// Approximate bytes: the rate times the window it was taken over.
			BytesSent:   int64(bytesSent[key] * w.seconds),
			BytesRecv:   int64(bytesRecv[key] * w.seconds),
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

// queryTCPFlows queries istio_tcp_connections_opened_total for TCP traffic.
// reportedInto is every destination namespace a series names, idle ones
// included: a long-lived connection opens none in the window, yet its series
// shows the reporting proxy is scraped.
func (s *IstioSource) queryTCPFlows(ctx context.Context, opts FlowOptions, sc clusterScope) (flows []Flow, reportedInto []string, err error) {
	query := istioRateQuery("source_workload, source_workload_namespace, destination_workload, destination_workload_namespace, destination_service_name", istioTCPMetric, istioReporterSelector, opts.Namespace, sc, rateWindowFor(opts.Since))

	result, err := s.queryFn(ctx, query)
	if err != nil {
		return nil, nil, err
	}

	flows = make([]Flow, 0, len(result.Series))
	for _, series := range result.Series {
		labels := series.Labels
		reportedInto = append(reportedInto, labels["destination_workload_namespace"])

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

	return flows, reportedInto, nil
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
