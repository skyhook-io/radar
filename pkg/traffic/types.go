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
	DirectionUnknown bool   `json:"directionUnknown,omitempty"`
	Verdict          string `json:"verdict"` // forwarded, dropped, error
	// PolicyVerdict is the network plugin's own account of which policies
	// decided this flow, when it reports one (Hubble does). It is the ground
	// truth a static evaluation can only approximate, and it names policy
	// kinds the static model cannot see, such as CiliumNetworkPolicy.
	PolicyVerdict *PolicyVerdict `json:"policyVerdict,omitempty"`
	LastSeen      time.Time      `json:"lastSeen"`
	// L7 stats (populated by Istio source)
	RequestRate float64 `json:"requestRate,omitempty"` // requests per second
	ErrorRate   float64 `json:"errorRate,omitempty"`   // 5xx errors per second
}

// PolicyVerdict is the set of policies the network plugin reports as having
// allowed or denied a flow.
type PolicyVerdict struct {
	AllowedBy []PolicyRef `json:"allowedBy,omitempty"`
	DeniedBy  []PolicyRef `json:"deniedBy,omitempty"`
	// Withheld counts denying references the plugin reported that were
	// removed before delivery because the caller may not read policies of
	// that kind there. The verdict still says a policy blocked the flow; only
	// its identity is kept back. Allowing references the caller may not read
	// are dropped without a count — they never explain a drop.
	Withheld int `json:"withheld,omitempty"`
}

// PolicyRef identifies a policy by kind, namespace and name; a cluster-scoped
// kind has an empty namespace.
type PolicyRef struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

// Endpoint kinds. Only Pod carries labels and a namespace a policy can
// select; the others say what kind of non-pod the plugin identified, which
// decides what a policy evaluation may conclude about it.
const (
	EndpointKindPod = "Pod"
	// EndpointKindExternal: outside the cluster (the world, or a CIDR identity).
	EndpointKindExternal = "External"
	// EndpointKindHost: a node, the host network, or the API server.
	EndpointKindHost = "Host"
	// EndpointKindUnknown: the plugin reported no usable identity.
	EndpointKindUnknown = "Unknown"
	// EndpointKindWorkload: the pods of one workload, as the graph shows them.
	EndpointKindWorkload = "Workload"
)

// Endpoint represents a source or destination in a flow.
type Endpoint struct {
	Name      string            `json:"name"`               // Pod or service name
	Namespace string            `json:"namespace"`          // Namespace
	Kind      string            `json:"kind"`               // Pod, Service, External, Host, Unknown
	IP        string            `json:"ip,omitempty"`       // IP address
	Labels    map[string]string `json:"labels,omitempty"`   // K8s labels
	Workload  string            `json:"workload,omitempty"` // Parent workload name (Deployment, etc.)
	// WorkloadKind is the kind of that workload — Deployment, StatefulSet,
	// CronJob, … — when it was resolved from the pod's owners.
	WorkloadKind string `json:"workloadKind,omitempty"`
	Port         int    `json:"port,omitempty"` // Port number
}

// GraphEndpoint is the endpoint as the graph draws it: a pod with a known
// workload becomes that workload, so a Deployment with fifty replicas is one
// node rather than fifty, the way metric-based sources report it already.
// Records keep their pods; only the aggregation and selection matching use
// this. An endpoint already named after its workload (Istio and Beyla report
// workloads as pods) is left as its source reported it.
func GraphEndpoint(e Endpoint) Endpoint {
	if e.Kind != EndpointKindPod || e.Workload == "" || e.Namespace == "" || e.Workload == e.Name {
		return e
	}
	return Endpoint{
		Name:         e.Workload,
		Namespace:    e.Namespace,
		Kind:         EndpointKindWorkload,
		Workload:     e.Workload,
		WorkloadKind: e.WorkloadKind,
	}
}

// GraphFlows maps each flow's endpoints to GraphEndpoint, returning copies.
func GraphFlows(flows []Flow) []Flow {
	out := make([]Flow, len(flows))
	for i, f := range flows {
		f.Source = GraphEndpoint(f.Source)
		f.Destination = GraphEndpoint(f.Destination)
		out[i] = f
	}
	return out
}

// FlowOptions contains options for querying flows.
type FlowOptions struct {
	Namespace string        // Filter by namespace (empty = all)
	Since     time.Duration // Look back period (default: 5 minutes)
	Follow    bool          // Stream new flows
	Limit     int           // Max flows to return (0 = no limit)
	// Namespaces keeps flows with either endpoint in one of these namespaces
	// (nil = all). A source that can filter on several namespaces at once uses
	// it in place of Namespace, so a multi-namespace view stops spending the
	// source's limits on traffic it is about to discard.
	Namespaces []string
	// ExcludeNamespaces drops flows with either endpoint in one of these
	// namespaces, and ExcludeHost flows to or from a node or the host network.
	// Both are what the view hides anyway; pushing them to the source keeps
	// that traffic from using up the source's limits.
	ExcludeNamespaces []string
	ExcludeHost       bool
	// Match narrows the query to the traffic behind one selection in the graph.
	// A source may use it to fetch less; the result still has to be filtered
	// with Match.Matches, since a source's own filters can be coarser.
	Match *FlowMatch
}

// Excludes reports whether a flow falls under ExcludeNamespaces or ExcludeHost.
func (o FlowOptions) Excludes(f Flow) bool {
	if o.ExcludeHost && (f.Source.Kind == EndpointKindHost || f.Destination.Kind == EndpointKindHost) {
		return true
	}
	for _, ns := range o.ExcludeNamespaces {
		if (f.Source.Namespace != "" && f.Source.Namespace == ns) || (f.Destination.Namespace != "" && f.Destination.Namespace == ns) {
			return true
		}
	}
	return false
}

// EndpointRef names an endpoint the way aggregation keys it: namespace and
// name. Kind is carried so a source can tell which references it can filter
// on natively (a pod) from ones it cannot (an external address, the host).
type EndpointRef struct {
	Namespace    string `json:"namespace,omitempty"`
	Name         string `json:"name"`
	Kind         string `json:"kind,omitempty"`
	WorkloadKind string `json:"workloadKind,omitempty"`
}

// matches compares a reference with an endpoint as it was reported or as the
// graph names it, so a pod reference matches its own records and a workload
// reference matches those of each of its pods.
func (r EndpointRef) matches(e Endpoint) bool {
	if r.Namespace != e.Namespace {
		return false
	}
	return r.Name == e.Name || r.Name == GraphEndpoint(e).Name
}

// EndpointPair is one edge of the aggregation, keyed as AggregateFlows keys
// it: the two endpoints, the port (zero when the source reports none), and
// whether the direction is known.
type EndpointPair struct {
	Source           EndpointRef `json:"source"`
	Destination      EndpointRef `json:"destination"`
	Port             int         `json:"port"`
	DirectionUnknown bool        `json:"directionUnknown,omitempty"`
}

// FlowMatch selects the flows behind a graph node or edge: those between one
// of the pairs, in the direction the aggregation recorded them.
type FlowMatch struct {
	Pairs []EndpointPair `json:"pairs"`
}

// Matches reports whether a flow belongs to the selection.
func (m *FlowMatch) Matches(f Flow) bool {
	if m == nil {
		return true
	}
	for _, p := range m.Pairs {
		if p.Source.matches(f.Source) && p.Destination.matches(f.Destination) &&
			p.Port == f.Port && p.DirectionUnknown == f.DirectionUnknown {
			return true
		}
	}
	return false
}

// Size is how many pairs the selection carries.
func (m *FlowMatch) Size() int {
	if m == nil {
		return 0
	}
	return len(m.Pairs)
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
	// something the user needs to read rather than wait out. WarningIncomplete
	// is not retried either.
	WarningKind string `json:"warningKind,omitempty"`
	// CoveredSince is when Flows start being complete, set when a source read
	// only its newest records and some of the window did not fit: Hubble
	// returns at most NodeFlowLimit flows per node. Before it, traffic may be
	// missing; after it, nothing was cut. Nil when the whole window is covered.
	CoveredSince  *time.Time `json:"coveredSince,omitempty"`
	NodeFlowLimit int        `json:"nodeFlowLimit,omitempty"`
	// FlowLimit is set when the source kept only its newest FlowLimit flows in
	// total, so the oldest part of the window was dropped; CoveredSince then
	// accounts for it too.
	FlowLimit int `json:"flowLimit,omitempty"`
}

// Warning kinds for FlowsResponse.WarningKind.
const (
	// WarningTransient marks a condition that may resolve on its own — a query
	// that failed, a port-forward still coming up. A retry is worthwhile.
	WarningTransient = "transient"
	// WarningIncomplete marks a fetch that succeeded but could not see
	// everything: events the source lost, nodes it could not reach. Flows may
	// be missing, so it holds even when the flows it came with are filtered
	// away and matters most when there are none. Retrying at once does not
	// help; the next refresh reads afresh.
	WarningIncomplete = "incomplete"
	// WarningPartial marks flows that are correct but have values missing or
	// wrong (a source not exporting an attribute, traffic that cannot be
	// oriented, a figure whose query failed this time). It is about the flows it
	// came with, so it is shown beside them and not retried for, and it goes
	// when they are filtered away.
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
	L7Protocol   string `json:"l7Protocol,omitempty"` // HTTP, gRPC, DNS (from majority of flows)
	RequestCount int64  `json:"requestCount,omitempty"`
	ErrorCount   int64  `json:"errorCount,omitempty"`
	// RequestRate and ErrorRate are the per-second rates a metric-based source
	// measured, summed unrounded. RequestCount and ErrorCount hold the same
	// figures rounded with a floor of one, which keeps a trickle visible but makes
	// any ratio of the two meaningless at low rates: 0.3 req/s with 0.01 err/s
	// rounds to one of each, a 100% error rate.
	RequestRate  float64 `json:"requestRate,omitempty"`
	ErrorRate    float64 `json:"errorRate,omitempty"`
	AvgLatencyMs float64 `json:"avgLatencyMs,omitempty"`
	// LatencySamples is how many measured responses the latency figures come
	// from, for a source that reports individual responses. A client combining
	// edges weights their averages by it. Unset for metric-based sources, whose
	// averages are weighted by RequestRate instead.
	LatencySamples   int64            `json:"latencySamples,omitempty"`
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
