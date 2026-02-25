package traffic

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/skyhook-io/radar/internal/portforward"
	promclient "github.com/skyhook-io/radar/internal/prometheus"
)

const (
	istiodName = "istiod"
)

// Namespaces where istiod is commonly deployed
var istioNamespaces = []string{"istio-system", "istio", "default"}

// IstioSource implements TrafficSource for Istio service mesh via Prometheus metrics.
// It uses the shared prometheus.Client for Prometheus discovery and queries,
// rather than maintaining its own connection state.
type IstioSource struct {
	k8sClient kubernetes.Interface
}

// NewIstioSource creates a new Istio traffic source
func NewIstioSource(client kubernetes.Interface) *IstioSource {
	return &IstioSource{
		k8sClient: client,
	}
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
		deploy, err := s.k8sClient.AppsV1().Deployments(ns).Get(ctx, istiodName, metav1.GetOptions{})
		if err != nil {
			continue
		}

		totalReplicas := int32(1)
		if deploy.Spec.Replicas != nil {
			totalReplicas = *deploy.Spec.Replicas
		}

		if deploy.Status.ReadyReplicas > 0 {
			result.Available = true
			result.Message = fmt.Sprintf("Istio detected with istiod running in namespace %s (%d/%d ready)",
				ns, deploy.Status.ReadyReplicas, totalReplicas)

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

		result.Message = fmt.Sprintf("istiod found in %s but not ready (%d/%d replicas)",
			ns, deploy.Status.ReadyReplicas, totalReplicas)
		return result, nil
	}

	result.Message = "Istio not detected. Install Istio for service mesh traffic visibility."
	return result, nil
}

// getPrometheusClient returns the shared prometheus client, or an error if unavailable
func (s *IstioSource) getPrometheusClient() (*promclient.Client, error) {
	client := promclient.GetClient()
	if client == nil {
		return nil, fmt.Errorf("prometheus client not initialized")
	}
	return client, nil
}

// GetFlows retrieves flows from Istio metrics via the shared Prometheus client
func (s *IstioSource) GetFlows(ctx context.Context, opts FlowOptions) (*FlowsResponse, error) {
	client, err := s.getPrometheusClient()
	if err != nil {
		return &FlowsResponse{
			Source:    "istio",
			Timestamp: time.Now(),
			Flows:     []Flow{},
			Warning:   "Prometheus not available for Istio metrics",
		}, nil
	}

	// Build HTTP request rate query
	httpFlows, err := s.queryHTTPFlows(ctx, client, opts)
	if err != nil {
		log.Printf("[istio] Error querying HTTP flows: %v", err)
		return &FlowsResponse{
			Source:    "istio",
			Timestamp: time.Now(),
			Flows:     []Flow{},
			Warning:   fmt.Sprintf("Failed to query Prometheus for Istio metrics: %v", err),
		}, nil
	}

	// Query TCP connections
	tcpFlows, err := s.queryTCPFlows(ctx, client, opts)
	if err != nil {
		log.Printf("[istio] Error querying TCP flows (continuing with HTTP only): %v", err)
	} else {
		httpFlows = append(httpFlows, tcpFlows...)
	}

	log.Printf("[istio] Retrieved %d flows from Prometheus", len(httpFlows))
	return &FlowsResponse{
		Source:    "istio",
		Timestamp: time.Now(),
		Flows:     httpFlows,
	}, nil
}

// flowKey uniquely identifies a source→destination service pair for map lookups
type flowKey struct {
	srcWorkload string
	srcNs       string
	dstWorkload string
	dstNs       string
}

// queryHTTPFlows queries istio_requests_total for HTTP/gRPC traffic.
// Response codes are aggregated (not split per-code) and a separate error query
// provides 5xx error rates per service pair.
func (s *IstioSource) queryHTTPFlows(ctx context.Context, client *promclient.Client, opts FlowOptions) ([]Flow, error) {
	// Main query: all requests, no response_code grouping
	query := `sum by (source_workload, source_workload_namespace, destination_workload, destination_workload_namespace, destination_service_name, request_protocol, reporter) (rate(istio_requests_total{reporter="destination"}[5m]))`
	if opts.Namespace != "" {
		safeNS := promclient.SanitizeLabelValue(opts.Namespace)
		query = fmt.Sprintf(`sum by (source_workload, source_workload_namespace, destination_workload, destination_workload_namespace, destination_service_name, request_protocol, reporter) (rate(istio_requests_total{reporter="destination", source_workload_namespace="%s"}[5m])) or sum by (source_workload, source_workload_namespace, destination_workload, destination_workload_namespace, destination_service_name, request_protocol, reporter) (rate(istio_requests_total{reporter="destination", destination_workload_namespace="%s"}[5m]))`,
			safeNS, safeNS)
	}

	// Error query: 5xx only
	errorQuery := `sum by (source_workload, source_workload_namespace, destination_workload, destination_workload_namespace, reporter) (rate(istio_requests_total{reporter="destination", response_code=~"5.."}[5m]))`
	if opts.Namespace != "" {
		safeNS := promclient.SanitizeLabelValue(opts.Namespace)
		errorQuery = fmt.Sprintf(`sum by (source_workload, source_workload_namespace, destination_workload, destination_workload_namespace, reporter) (rate(istio_requests_total{reporter="destination", response_code=~"5..", source_workload_namespace="%s"}[5m])) or sum by (source_workload, source_workload_namespace, destination_workload, destination_workload_namespace, reporter) (rate(istio_requests_total{reporter="destination", response_code=~"5..", destination_workload_namespace="%s"}[5m]))`,
			safeNS, safeNS)
	}

	result, err := client.Query(ctx, query)
	if err != nil {
		return nil, err
	}

	// Build error rate map
	errorRates := make(map[flowKey]float64)
	errorResult, err := client.Query(ctx, errorQuery)
	if err != nil {
		log.Printf("[istio] Error querying 5xx rates (continuing without error data): %v", err)
	} else {
		for _, series := range errorResult.Series {
			if len(series.DataPoints) == 0 {
				continue
			}
			val := series.DataPoints[0].Value
			if val <= 0 {
				continue
			}
			labels := series.Labels
			key := flowKey{
				srcWorkload: labels["source_workload"],
				srcNs:       labels["source_workload_namespace"],
				dstWorkload: labels["destination_workload"],
				dstNs:       labels["destination_workload_namespace"],
			}
			errorRates[key] = val
		}
	}

	// Query byte metrics
	bytesSentMap, bytesRecvMap := s.queryByteMetrics(ctx, client, opts)

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

		connections := int64(val)
		if connections == 0 {
			connections = 1 // fractional rate (< 1 req/s) still means traffic exists
		}

		protocol := strings.ToLower(labels["request_protocol"])
		if protocol == "" {
			protocol = "http"
		}

		srcWorkload := labels["source_workload"]
		srcNs := labels["source_workload_namespace"]
		dstWorkload := labels["destination_workload"]
		dstNs := labels["destination_workload_namespace"]
		dstService := labels["destination_service_name"]

		key := flowKey{srcWorkload: srcWorkload, srcNs: srcNs, dstWorkload: dstWorkload, dstNs: dstNs}
		errRate := errorRates[key]

		verdict := "forwarded"
		if errRate > 0 {
			verdict = "error"
		}

		// Approximate bytes from rate * window (5m = 300s)
		bytesSent := int64(bytesSentMap[key] * 300)
		bytesRecv := int64(bytesRecvMap[key] * 300)

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
			Protocol:    protocol,
			L7Protocol:  strings.ToUpper(protocol),
			Connections: connections,
			BytesSent:   bytesSent,
			BytesRecv:   bytesRecv,
			Verdict:     verdict,
			LastSeen:    time.Now(),
			RequestRate: val,
			ErrorRate:   errRate,
		}

		// Use destination service name if workload is unknown
		if flow.Destination.Name == "" || flow.Destination.Name == "unknown" {
			if dstService != "" {
				flow.Destination.Name = dstService
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

// queryByteMetrics queries istio_request_bytes_sum and istio_response_bytes_sum
// to get byte throughput per service pair. Returns maps of (src,dst) → bytes/sec rate.
func (s *IstioSource) queryByteMetrics(ctx context.Context, client *promclient.Client, opts FlowOptions) (sent map[flowKey]float64, recv map[flowKey]float64) {
	sent = make(map[flowKey]float64)
	recv = make(map[flowKey]float64)

	sentQuery := `sum by (source_workload, source_workload_namespace, destination_workload, destination_workload_namespace) (rate(istio_request_bytes_sum{reporter="destination"}[5m]))`
	recvQuery := `sum by (source_workload, source_workload_namespace, destination_workload, destination_workload_namespace) (rate(istio_response_bytes_sum{reporter="destination"}[5m]))`
	if opts.Namespace != "" {
		safeNS := promclient.SanitizeLabelValue(opts.Namespace)
		sentQuery = fmt.Sprintf(`sum by (source_workload, source_workload_namespace, destination_workload, destination_workload_namespace) (rate(istio_request_bytes_sum{reporter="destination", source_workload_namespace="%s"}[5m])) or sum by (source_workload, source_workload_namespace, destination_workload, destination_workload_namespace) (rate(istio_request_bytes_sum{reporter="destination", destination_workload_namespace="%s"}[5m]))`,
			safeNS, safeNS)
		recvQuery = fmt.Sprintf(`sum by (source_workload, source_workload_namespace, destination_workload, destination_workload_namespace) (rate(istio_response_bytes_sum{reporter="destination", source_workload_namespace="%s"}[5m])) or sum by (source_workload, source_workload_namespace, destination_workload, destination_workload_namespace) (rate(istio_response_bytes_sum{reporter="destination", destination_workload_namespace="%s"}[5m]))`,
			safeNS, safeNS)
	}

	parseByteResult := func(result *promclient.QueryResult, target map[flowKey]float64) {
		if result == nil {
			return
		}
		for _, series := range result.Series {
			if len(series.DataPoints) == 0 {
				continue
			}
			val := series.DataPoints[0].Value
			if val <= 0 {
				continue
			}
			labels := series.Labels
			key := flowKey{
				srcWorkload: labels["source_workload"],
				srcNs:       labels["source_workload_namespace"],
				dstWorkload: labels["destination_workload"],
				dstNs:       labels["destination_workload_namespace"],
			}
			target[key] = val
		}
	}

	sentResult, err := client.Query(ctx, sentQuery)
	if err != nil {
		log.Printf("[istio] Error querying request bytes (continuing without byte data): %v", err)
	} else {
		parseByteResult(sentResult, sent)
	}

	recvResult, err := client.Query(ctx, recvQuery)
	if err != nil {
		log.Printf("[istio] Error querying response bytes (continuing without byte data): %v", err)
	} else {
		parseByteResult(recvResult, recv)
	}

	return sent, recv
}

// queryTCPFlows queries istio_tcp_connections_opened_total for TCP traffic
func (s *IstioSource) queryTCPFlows(ctx context.Context, client *promclient.Client, opts FlowOptions) ([]Flow, error) {
	query := `sum by (source_workload, source_workload_namespace, destination_workload, destination_workload_namespace, destination_service_name, reporter) (rate(istio_tcp_connections_opened_total{reporter="destination"}[5m]))`
	if opts.Namespace != "" {
		safeNS := promclient.SanitizeLabelValue(opts.Namespace)
		query = fmt.Sprintf(`sum by (source_workload, source_workload_namespace, destination_workload, destination_workload_namespace, destination_service_name, reporter) (rate(istio_tcp_connections_opened_total{reporter="destination", source_workload_namespace="%s"}[5m])) or sum by (source_workload, source_workload_namespace, destination_workload, destination_workload_namespace, destination_service_name, reporter) (rate(istio_tcp_connections_opened_total{reporter="destination", destination_workload_namespace="%s"}[5m]))`,
			safeNS, safeNS)
	}

	result, err := client.Query(ctx, query)
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

		connections := int64(val)
		if connections == 0 {
			connections = 1
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
			Connections: connections,
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

// Close cleans up resources (no-op since we use the shared prometheus client)
func (s *IstioSource) Close() error {
	return nil
}
