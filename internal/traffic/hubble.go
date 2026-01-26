package traffic

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	hubbleRelayService   = "hubble-relay"
	hubbleRelayNamespace = "kube-system"
	hubbleRelayPort      = 4245
	hubbleHTTPPort       = 12000 // Hubble UI HTTP API port (if available)
	hubbleDetectTimeout  = 5 * time.Second
)

// HubbleSource implements TrafficSource for Hubble/Cilium
type HubbleSource struct {
	k8sClient   kubernetes.Interface
	relayAddr   string
	httpClient  *http.Client
	isConnected bool
	mu          sync.RWMutex
}

// NewHubbleSource creates a new Hubble traffic source
func NewHubbleSource(client kubernetes.Interface) *HubbleSource {
	return &HubbleSource{
		k8sClient: client,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Name returns the source identifier
func (h *HubbleSource) Name() string {
	return "hubble"
}

// Detect checks if Hubble is available in the cluster
func (h *HubbleSource) Detect(ctx context.Context) (*DetectionResult, error) {
	result := &DetectionResult{
		Available: false,
	}

	// Step 1: Check for Cilium ConfigMap (indicates Cilium is installed)
	ciliumConfig, err := h.k8sClient.CoreV1().ConfigMaps(hubbleRelayNamespace).Get(ctx, "cilium-config", metav1.GetOptions{})
	hasCilium := err == nil

	// Check if Hubble is enabled in Cilium config
	hubbleEnabled := false
	if hasCilium && ciliumConfig.Data != nil {
		hubbleEnabled = ciliumConfig.Data["enable-hubble"] == "true"
	}

	// Step 2: Check for Hubble Relay pods
	relayPods, err := h.k8sClient.CoreV1().Pods(hubbleRelayNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: "k8s-app=hubble-relay",
	})
	hasRelayPods := err == nil && len(relayPods.Items) > 0

	// Count running pods
	runningPods := 0
	if hasRelayPods {
		for _, pod := range relayPods.Items {
			if pod.Status.Phase == "Running" {
				runningPods++
			}
		}
	}

	// Step 3: Check for Hubble Relay service
	relaySvc, err := h.k8sClient.CoreV1().Services(hubbleRelayNamespace).Get(ctx, hubbleRelayService, metav1.GetOptions{})
	hasRelayService := err == nil

	// Step 4: Determine status
	isNative := h.isNativeHubble(ctx)

	if !hasCilium {
		result.Message = "Cilium CNI not detected. Install Cilium with Hubble for traffic visibility."
		return result, nil
	}

	if !hubbleEnabled {
		result.Message = "Cilium is installed but Hubble is not enabled. Enable Hubble observability."
		if isNative {
			result.Message += " For GKE, run: gcloud container clusters update CLUSTER --enable-dataplane-v2-observability"
		} else {
			result.Message += " Run: cilium hubble enable"
		}
		return result, nil
	}

	if !hasRelayPods {
		result.Message = "Hubble is enabled but Hubble Relay pods not found. The Relay may still be deploying."
		return result, nil
	}

	if runningPods == 0 {
		result.Message = fmt.Sprintf("Hubble Relay pods exist (%d) but none are running", len(relayPods.Items))
		return result, nil
	}

	if !hasRelayService {
		result.Message = "Hubble Relay pods are running but service not exposed"
		return result, nil
	}

	// All checks passed - Hubble is available
	h.mu.Lock()
	h.relayAddr = fmt.Sprintf("%s.%s.svc.cluster.local:%d",
		relaySvc.Name, hubbleRelayNamespace, relaySvc.Spec.Ports[0].Port)
	h.isConnected = true
	h.mu.Unlock()

	result.Available = true
	result.Native = isNative
	result.Message = fmt.Sprintf("Hubble Relay detected with %d running pod(s)", runningPods)

	// Try to get version from Cilium config
	if ciliumConfig.Labels != nil {
		if ver, ok := ciliumConfig.Labels["cilium.io/version"]; ok {
			result.Version = ver
		}
	}

	return result, nil
}

// isNativeHubble checks if this is GKE Dataplane V2 (native Hubble)
func (h *HubbleSource) isNativeHubble(ctx context.Context) bool {
	// Check for GKE by looking at node provider ID
	nodes, err := h.k8sClient.CoreV1().Nodes().List(ctx, metav1.ListOptions{Limit: 1})
	if err != nil || len(nodes.Items) == 0 {
		return false
	}

	node := nodes.Items[0]

	// GKE nodes have gce:// provider ID
	if strings.HasPrefix(node.Spec.ProviderID, "gce://") {
		// Check for Dataplane V2 specific labels or annotations
		if _, ok := node.Labels["cloud.google.com/gke-nodepool"]; ok {
			return true
		}
	}

	return false
}

// GetFlows retrieves flows from Hubble
// Note: Full gRPC implementation requires cilium/cilium package.
// This is a placeholder that returns aggregated flow data.
func (h *HubbleSource) GetFlows(ctx context.Context, opts FlowOptions) (*FlowsResponse, error) {
	h.mu.RLock()
	connected := h.isConnected
	h.mu.RUnlock()

	if !connected {
		// Try to detect first
		result, err := h.Detect(ctx)
		if err != nil || !result.Available {
			return nil, fmt.Errorf("Hubble not available: %s", result.Message)
		}
	}

	// For V1, we'll use the Hubble HTTP API if available (hubble-ui backend)
	// or return instructions for manual port-forward
	flows, err := h.fetchFlowsViaHTTP(ctx, opts)
	if err != nil {
		log.Printf("[hubble] HTTP API not available: %v", err)
		return &FlowsResponse{
			Source:    "hubble",
			Timestamp: time.Now(),
			Flows:     []Flow{},
			Warning:   fmt.Sprintf("Hubble API not reachable: %v", err),
		}, nil
	}

	return &FlowsResponse{
		Source:    "hubble",
		Timestamp: time.Now(),
		Flows:     flows,
	}, nil
}

// fetchFlowsViaHTTP tries to fetch flows via Hubble UI HTTP API
func (h *HubbleSource) fetchFlowsViaHTTP(ctx context.Context, opts FlowOptions) ([]Flow, error) {
	// Check if Hubble UI service exists
	uiSvc, err := h.k8sClient.CoreV1().Services(hubbleRelayNamespace).Get(ctx, "hubble-ui", metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("hubble-ui service not found: %w", err)
	}

	// Try to connect to Hubble UI backend API
	// Note: This typically requires port-forward or cluster-internal access
	url := fmt.Sprintf("http://%s.%s.svc.cluster.local:%d/api/v1/flows",
		uiSvc.Name, hubbleRelayNamespace, hubbleHTTPPort)

	if opts.Namespace != "" {
		url += "?namespace=" + opts.Namespace
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}

	resp, err := h.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	var hubbleFlows []struct {
		Flow struct {
			Source struct {
				PodName   string   `json:"pod_name"`
				Namespace string   `json:"namespace"`
				Labels    []string `json:"labels"`
				IP        string   `json:"IP"`
			} `json:"source"`
			Destination struct {
				PodName   string   `json:"pod_name"`
				Namespace string   `json:"namespace"`
				Labels    []string `json:"labels"`
				IP        string   `json:"IP"`
			} `json:"destination"`
			L4 struct {
				TCP *struct {
					DestinationPort int `json:"destination_port"`
				} `json:"TCP"`
				UDP *struct {
					DestinationPort int `json:"destination_port"`
				} `json:"UDP"`
			} `json:"l4"`
			Verdict string `json:"verdict"`
			Time    string `json:"time"`
		} `json:"flow"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&hubbleFlows); err != nil {
		return nil, err
	}

	flows := make([]Flow, 0, len(hubbleFlows))
	for _, hf := range hubbleFlows {
		f := hf.Flow
		flow := Flow{
			Source: Endpoint{
				Name:      f.Source.PodName,
				Namespace: f.Source.Namespace,
				Kind:      "Pod",
				IP:        f.Source.IP,
			},
			Destination: Endpoint{
				Name:      f.Destination.PodName,
				Namespace: f.Destination.Namespace,
				Kind:      "Pod",
				IP:        f.Destination.IP,
			},
			Protocol:    "tcp",
			Verdict:     strings.ToLower(f.Verdict),
			Connections: 1,
		}

		if f.L4.TCP != nil {
			flow.Port = f.L4.TCP.DestinationPort
			flow.Protocol = "tcp"
		} else if f.L4.UDP != nil {
			flow.Port = f.L4.UDP.DestinationPort
			flow.Protocol = "udp"
		}

		// Handle external endpoints
		if f.Source.PodName == "" && f.Source.IP != "" {
			flow.Source.Kind = "External"
			flow.Source.Name = f.Source.IP
		}
		if f.Destination.PodName == "" && f.Destination.IP != "" {
			flow.Destination.Kind = "External"
			flow.Destination.Name = f.Destination.IP
		}

		// Parse timestamp
		if ts, err := time.Parse(time.RFC3339Nano, f.Time); err == nil {
			flow.LastSeen = ts
		} else {
			flow.LastSeen = time.Now()
		}

		// Extract workload from labels
		flow.Source.Workload = extractWorkloadFromLabels(f.Source.Labels)
		flow.Destination.Workload = extractWorkloadFromLabels(f.Destination.Labels)

		flows = append(flows, flow)
	}

	return flows, nil
}

// extractWorkloadFromLabels extracts workload name from pod labels
func extractWorkloadFromLabels(labels []string) string {
	labelMap := make(map[string]string)
	for _, l := range labels {
		parts := strings.SplitN(l, "=", 2)
		if len(parts) == 2 {
			labelMap[parts[0]] = parts[1]
		}
	}

	// Common workload labels in order of preference
	for _, key := range []string{"app", "app.kubernetes.io/name", "k8s-app", "name"} {
		if name, ok := labelMap[key]; ok {
			return name
		}
	}

	return ""
}

// StreamFlows returns a channel of flows for real-time updates
// Note: Full streaming requires gRPC client to Hubble Relay
func (h *HubbleSource) StreamFlows(ctx context.Context, opts FlowOptions) (<-chan Flow, error) {
	flowCh := make(chan Flow, 100)

	go func() {
		defer close(flowCh)

		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				// Poll for flows (simulated streaming)
				response, err := h.GetFlows(ctx, opts)
				if err != nil {
					log.Printf("[hubble] Error fetching flows: %v", err)
					continue
				}

				for _, flow := range response.Flows {
					select {
					case flowCh <- flow:
					case <-ctx.Done():
						return
					default:
						// Channel full, drop flow
					}
				}
			}
		}
	}()

	return flowCh, nil
}

// Close cleans up resources
func (h *HubbleSource) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.isConnected = false
	return nil
}

// GetPortForwardInstructions returns kubectl commands for manual access
func (h *HubbleSource) GetPortForwardInstructions() string {
	return `To access Hubble flows directly, run:

# Port-forward Hubble Relay (gRPC API)
kubectl -n kube-system port-forward svc/hubble-relay 4245:4245

# Then use Hubble CLI:
hubble observe --server localhost:4245

# Or port-forward Hubble UI:
kubectl -n kube-system port-forward svc/hubble-ui 12000:80
# Then open http://localhost:12000`
}
