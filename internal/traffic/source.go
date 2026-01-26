// Package traffic provides traffic source detection and flow aggregation.
package traffic

import (
	"context"
	"time"
)

// TrafficSource interface - each traffic monitoring tool implements this
type TrafficSource interface {
	// Name returns the source identifier (e.g., "hubble", "caretta")
	Name() string

	// Detect checks if this traffic source is available in the cluster
	Detect(ctx context.Context) (*DetectionResult, error)

	// GetFlows retrieves aggregated flow data from the source
	GetFlows(ctx context.Context, opts FlowOptions) (*FlowsResponse, error)

	// StreamFlows returns a channel of flows for real-time updates
	StreamFlows(ctx context.Context, opts FlowOptions) (<-chan Flow, error)

	// Close cleans up any resources (e.g., gRPC connections)
	Close() error
}

// DetectionResult contains the result of a traffic source detection
type DetectionResult struct {
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
	Native    bool   `json:"native"` // True if built into the cluster (e.g., Cilium/Hubble in GKE)
	Message   string `json:"message,omitempty"`
}

// FlowOptions contains options for querying flows
type FlowOptions struct {
	Namespace string        // Filter by namespace (empty = all)
	Since     time.Duration // Look back period (default: 5 minutes)
	Follow    bool          // Stream new flows
	Limit     int           // Max flows to return (0 = no limit)
}

// Flow represents a single network flow between two endpoints
type Flow struct {
	Source      Endpoint  `json:"source"`
	Destination Endpoint  `json:"destination"`
	Protocol    string    `json:"protocol"` // tcp, udp, http, grpc
	Port        int       `json:"port"`
	L7Protocol  string    `json:"l7Protocol,omitempty"` // HTTP, gRPC, DNS (if L7 visibility)
	HTTPMethod  string    `json:"httpMethod,omitempty"`
	HTTPPath    string    `json:"httpPath,omitempty"`
	HTTPStatus  int       `json:"httpStatus,omitempty"`
	BytesSent   int64     `json:"bytesSent"`
	BytesRecv   int64     `json:"bytesRecv"`
	Connections int64     `json:"connections"`
	Verdict     string    `json:"verdict"` // forwarded, dropped, error
	LastSeen    time.Time `json:"lastSeen"`
}

// Endpoint represents a source or destination in a flow
type Endpoint struct {
	Name      string            `json:"name"`                // Pod or service name
	Namespace string            `json:"namespace"`           // Namespace
	Kind      string            `json:"kind"`                // Pod, Service, External
	IP        string            `json:"ip,omitempty"`        // IP address
	Labels    map[string]string `json:"labels,omitempty"`    // K8s labels
	Workload  string            `json:"workload,omitempty"`  // Parent workload name (Deployment, etc.)
	Port      int               `json:"port,omitempty"`      // Port number
}

// FlowsResponse contains the flows and metadata
type FlowsResponse struct {
	Source    string    `json:"source"`    // Which traffic source provided this data
	Timestamp time.Time `json:"timestamp"` // When this data was collected
	Flows     []Flow    `json:"flows"`
	Warning   string    `json:"warning,omitempty"` // Non-fatal warning (e.g., query errors)
}

// AggregatedFlow represents flows aggregated by service pair
type AggregatedFlow struct {
	Source      Endpoint  `json:"source"`
	Destination Endpoint  `json:"destination"`
	Protocol    string    `json:"protocol"`
	Port        int       `json:"port"`
	FlowCount   int64     `json:"flowCount"`
	BytesSent   int64     `json:"bytesSent"`
	BytesRecv   int64     `json:"bytesRecv"`
	Connections int64     `json:"connections"`
	LastSeen    time.Time `json:"lastSeen"`
	// L7 stats (if available)
	RequestCount int64   `json:"requestCount,omitempty"`
	ErrorCount   int64   `json:"errorCount,omitempty"`
	AvgLatencyMs float64 `json:"avgLatencyMs,omitempty"`
}

// ClusterInfo contains cluster platform and CNI information
type ClusterInfo struct {
	Platform     string `json:"platform"`     // gke, eks, aks, generic
	CNI          string `json:"cni"`          // cilium, calico, flannel, vpc-cni, azure-cni, etc.
	DataplaneV2  bool   `json:"dataplaneV2"`  // GKE-specific: is Dataplane V2 enabled?
	ClusterName  string `json:"clusterName"`  // Cluster name if available
	K8sVersion   string `json:"k8sVersion"`   // Kubernetes version
}

// SourceStatus represents the status of a detected traffic source
type SourceStatus struct {
	Name      string `json:"name"`
	Status    string `json:"status"` // available, not_found, error
	Version   string `json:"version,omitempty"`
	Native    bool   `json:"native"`
	Message   string `json:"message,omitempty"`
}

// Recommendation contains installation recommendations for a traffic source
type Recommendation struct {
	Name           string `json:"name"`
	Reason         string `json:"reason"`
	InstallCommand string `json:"installCommand,omitempty"` // For non-Helm installs (e.g., gcloud commands)
	DocsURL        string `json:"docsUrl,omitempty"`
	// Helm chart info (for one-click install via Helm view)
	HelmChart *HelmChartInfo `json:"helmChart,omitempty"`
	// Alternative option (for cases where there are two good choices)
	AlternativeName    string `json:"alternativeName,omitempty"`
	AlternativeReason  string `json:"alternativeReason,omitempty"`
	AlternativeDocsURL string `json:"alternativeDocsUrl,omitempty"`
}

// HelmChartInfo contains info needed to install a chart via the Helm view
type HelmChartInfo struct {
	Repo      string `json:"repo"`      // Repository name (e.g., "groundcover")
	RepoURL   string `json:"repoUrl"`   // Repository URL
	ChartName string `json:"chartName"` // Chart name (e.g., "caretta")
	Version   string `json:"version"`   // Optional specific version
}

// SourcesResponse is the response for GET /api/traffic/sources
type SourcesResponse struct {
	Cluster      ClusterInfo      `json:"cluster"`
	Detected     []SourceStatus   `json:"detected"`
	NotDetected  []string         `json:"notDetected"`
	Recommended  *Recommendation  `json:"recommended,omitempty"`
}
