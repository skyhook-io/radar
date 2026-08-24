// Package traffic provides shared types and aggregation logic for traffic flow analysis.
package traffic

import "time"

// Flow represents a single network flow between two endpoints.
type Flow struct {
	Source           Endpoint `json:"source"`
	Destination      Endpoint `json:"destination"`
	Protocol         string   `json:"protocol"` // tcp, udp, http, grpc
	Port             int      `json:"port"`
	L7Protocol       string   `json:"l7Protocol,omitempty"` // HTTP, gRPC, DNS (if L7 visibility)
	HTTPMethod       string   `json:"httpMethod,omitempty"`
	HTTPPath         string   `json:"httpPath,omitempty"`
	HTTPStatus       int      `json:"httpStatus,omitempty"`
	LatencyNs        uint64   `json:"latencyNs,omitempty"`    // from Layer7.latency_ns (RESPONSE flows)
	L7Type           string   `json:"l7Type,omitempty"`       // REQUEST, RESPONSE, SAMPLE
	HTTPProtocol     string   `json:"httpProtocol,omitempty"` // HTTP/1.1, HTTP/2
	HTTPHeaders      []string `json:"httpHeaders,omitempty"`  // allowlisted headers as "key: value"
	DNSQuery         string   `json:"dnsQuery,omitempty"`
	DNSIPs           []string `json:"dnsIPs,omitempty"`
	DNSTTL           uint32   `json:"dnsTTL,omitempty"`
	DNSRCode         uint32   `json:"dnsRCode,omitempty"` // 0=NoError, 3=NXDomain
	DNSQTypes        []string `json:"dnsQTypes,omitempty"`
	TrafficDirection string   `json:"trafficDirection,omitempty"` // ingress, egress
	DropReasonDesc   string   `json:"dropReasonDesc,omitempty"`
	SourceService    string   `json:"sourceService,omitempty"`
	DestService      string   `json:"destService,omitempty"`
	BytesSent        int64    `json:"bytesSent"`
	BytesRecv        int64    `json:"bytesRecv"`
	Connections      int64    `json:"connections"`
	// DirectionUnknown marks a conversation whose initiator could not be
	// established, so the two endpoints are ordered arbitrarily and BytesSent /
	// BytesRecv follow that ordering rather than describing a caller and a callee.
	// The graph draws these without an arrowhead: the traffic is real, only its
	// direction is not known.
	DirectionUnknown bool      `json:"directionUnknown,omitempty"`
	Verdict          string    `json:"verdict"` // forwarded, dropped, error
	LastSeen         time.Time `json:"lastSeen"`
	// L7 stats (populated by Istio source)
	RequestRate float64 `json:"requestRate,omitempty"` // requests per second
	ErrorRate   float64 `json:"errorRate,omitempty"`   // 5xx errors per second
}

// Endpoint represents a source or destination in a flow.
type Endpoint struct {
	Name      string            `json:"name"`               // Pod or service name
	Namespace string            `json:"namespace"`          // Namespace
	Kind      string            `json:"kind"`               // Pod, Service, External
	IP        string            `json:"ip,omitempty"`       // IP address
	Labels    map[string]string `json:"labels,omitempty"`   // K8s labels
	Workload  string            `json:"workload,omitempty"` // Parent workload name (Deployment, etc.)
	Port      int               `json:"port,omitempty"`     // Port number
}

// FlowOptions contains options for querying flows.
type FlowOptions struct {
	Namespace string        // Filter by namespace (empty = all)
	Since     time.Duration // Look back period (default: 5 minutes)
	Follow    bool          // Stream new flows
	Limit     int           // Max flows to return (0 = no limit)
}

// FlowsResponse contains the flows and metadata.
type FlowsResponse struct {
	Source    string    `json:"source"`    // Which traffic source provided this data
	Timestamp time.Time `json:"timestamp"` // When this data was collected
	Flows     []Flow    `json:"flows"`
	Warning   string    `json:"warning,omitempty"` // Non-fatal warning (e.g., query errors)
	// WarningKind separates a warning worth retrying from one that is simply the
	// truth about this data. Empty means transient, so a source that does not set
	// it keeps the retrying behaviour it had. A client must not retry
	// WarningPartial: the answer will not change, and the warning explains
	// something the user needs to read rather than wait out.
	WarningKind string `json:"warningKind,omitempty"`
}

// Warning kinds for FlowsResponse.WarningKind.
const (
	// WarningTransient marks a condition that may resolve on its own — a query
	// that failed, a port-forward still coming up.
	WarningTransient = "transient"
	// WarningPartial marks flows that are correct but incomplete, for a reason
	// retrying cannot fix (a source not exporting an attribute, traffic that
	// cannot be oriented). Always shown alongside whatever flows did arrive.
	WarningPartial = "partial"
)

// AggregatedFlow represents flows aggregated by service pair.
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
	// DirectionUnknown is set when the flows behind this edge could not be
	// oriented; the graph then draws it without an arrowhead.
	DirectionUnknown bool `json:"directionUnknown,omitempty"`
	// L7 stats (if available)
	L7Protocol       string           `json:"l7Protocol,omitempty"` // HTTP, gRPC, DNS (from majority of flows)
	RequestCount     int64            `json:"requestCount,omitempty"`
	ErrorCount       int64            `json:"errorCount,omitempty"`
	AvgLatencyMs     float64          `json:"avgLatencyMs,omitempty"`
	LatencyP50Ms     float64          `json:"latencyP50Ms,omitempty"`
	LatencyP95Ms     float64          `json:"latencyP95Ms,omitempty"`
	LatencyP99Ms     float64          `json:"latencyP99Ms,omitempty"`
	HTTPStatusCounts map[string]int64 `json:"httpStatusCounts,omitempty"` // "2xx": 150, "5xx": 3
	TopHTTPPaths     []HTTPPathStat   `json:"topHTTPPaths,omitempty"`
	TopDNSQueries    []DNSQueryStat   `json:"topDNSQueries,omitempty"`
	VerdictCounts    map[string]int64 `json:"verdictCounts,omitempty"` // "forwarded": 500, "dropped": 3
	DropReasons      map[string]int64 `json:"dropReasons,omitempty"`
}

// HTTPPathStat tracks request statistics for a specific HTTP method+path combination.
type HTTPPathStat struct {
	Method   string  `json:"method"`
	Path     string  `json:"path"`
	Count    int64   `json:"count"`
	AvgMs    float64 `json:"avgMs,omitempty"`
	ErrorPct float64 `json:"errorPct,omitempty"` // 4xx+5xx percentage
}

// DNSQueryStat tracks statistics for a specific DNS query domain.
type DNSQueryStat struct {
	Query   string `json:"query"`
	Count   int64  `json:"count"`
	NXCount int64  `json:"nxCount,omitempty"` // NXDOMAIN responses
	AvgTTL  uint32 `json:"avgTTL,omitempty"`
}

// DetectionResult contains the result of a traffic source detection.
type DetectionResult struct {
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
	Native    bool   `json:"native"` // True if built into the cluster (e.g., Cilium/Hubble in GKE)
	Message   string `json:"message,omitempty"`
	// Present means the source was found in the cluster but cannot be used as it
	// stands — misconfigured, or not being scraped. Only meaningful when Available
	// is false, and it is what separates a problem the user can fix from a source
	// they simply have not installed. Absence needs no explanation; a broken
	// install does.
	Present bool `json:"present,omitempty"`
}

// ClusterInfo contains cluster platform and CNI information.
type ClusterInfo struct {
	Platform    string `json:"platform"`    // rke2, gke, eks, aks, minikube, kind, docker-desktop, openshift, rancher, generic
	CNI         string `json:"cni"`         // cilium, canal, calico, flannel, vpc-cni, azure-cni, gke-native, unknown
	DataplaneV2 bool   `json:"dataplaneV2"` // GKE-specific: is Dataplane V2 enabled?
	ClusterName string `json:"clusterName"` // Cluster name if available
	K8sVersion  string `json:"k8sVersion"`  // Kubernetes version
}

// SourceStatus represents the status of a detected traffic source.
type SourceStatus struct {
	Name    string `json:"name"`
	Status  string `json:"status"` // available, not_found, error
	Version string `json:"version,omitempty"`
	Native  bool   `json:"native"`
	Message string `json:"message,omitempty"`
}

// Recommendation contains installation recommendations for a traffic source.
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

// HelmChartInfo contains info needed to install a chart via the Helm view.
type HelmChartInfo struct {
	Repo          string         `json:"repo"`                    // Repository name (e.g., "groundcover")
	RepoURL       string         `json:"repoUrl"`                 // Repository URL
	ChartName     string         `json:"chartName"`               // Chart name (e.g., "caretta")
	Version       string         `json:"version"`                 // Optional specific version
	DefaultValues map[string]any `json:"defaultValues,omitempty"` // Default values to pre-populate in the install wizard
}

// SourcesResponse is the response for GET /api/traffic/sources.
type SourcesResponse struct {
	Cluster     ClusterInfo     `json:"cluster"`
	Active      string          `json:"active"`
	Detected    []SourceStatus  `json:"detected"`
	NotDetected []string        `json:"notDetected"`
	Recommended *Recommendation `json:"recommended,omitempty"`
}
