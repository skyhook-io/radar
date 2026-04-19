package audit

import (
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
)

// CheckInput contains the typed K8s resources to check.
// Each field is optional — checks are skipped for nil/empty slices.
// Callers populate this from their own cache or API client.
type CheckInput struct {
	Pods                     []*corev1.Pod
	Deployments              []*appsv1.Deployment
	StatefulSets             []*appsv1.StatefulSet
	DaemonSets               []*appsv1.DaemonSet
	Services                 []*corev1.Service
	Ingresses                []*networkingv1.Ingress
	HorizontalPodAutoscalers []*autoscalingv2.HorizontalPodAutoscaler
	PodDisruptionBudgets     []*policyv1.PodDisruptionBudget
	ConfigMaps               []*corev1.ConfigMap
	Secrets                  []*corev1.Secret
	ServiceAccounts          []*corev1.ServiceAccount
	LimitRanges              []*corev1.LimitRange
	// ClusterVersion is the K8s server version (e.g. "1.30"). Used for deprecated API checks.
	ClusterVersion string
	// ServedAPIs lists API group/versions the cluster still serves (e.g. ["apps/v1", "batch/v1beta1"]).
	// Used to detect deprecated APIs. Callers populate from discovery client.
	ServedAPIs []string
	// PodMetrics provides live CPU/memory usage for utilization checks.
	// Optional — check is skipped when nil/empty. Callers populate from metrics-server or equivalent.
	PodMetrics []PodMetricsInput
}

// PodMetricsInput provides metrics data for resource utilization checks.
type PodMetricsInput struct {
	Namespace     string
	Name          string
	CPUUsage      int64 // millicores
	MemoryUsage   int64 // bytes
	CPURequest    int64 // millicores
	MemoryRequest int64 // bytes
}

// ScanResults is the output of RunChecks.
type ScanResults struct {
	Summary  ScanSummary          `json:"summary"`
	Findings []Finding            `json:"findings"`
	Groups   []ResourceGroup      `json:"groups"`
	Checks   map[string]CheckMeta `json:"checks"`
}

// ResourceGroup aggregates findings for a single resource.
// Groups are sorted by severity (danger first), then by name.
type ResourceGroup struct {
	Kind      string    `json:"kind"`
	Namespace string    `json:"namespace"`
	Name      string    `json:"name"`
	Warning   int       `json:"warning"`
	Danger    int       `json:"danger"`
	Findings  []Finding `json:"findings"`
}

// ScanSummary provides aggregate counts.
type ScanSummary struct {
	Passing    int                        `json:"passing"`
	Warning    int                        `json:"warning"`
	Danger     int                        `json:"danger"`
	Categories map[string]CategorySummary `json:"categories"`
}

// CategorySummary provides per-category counts.
type CategorySummary struct {
	Passing int `json:"passing"`
	Warning int `json:"warning"`
	Danger  int `json:"danger"`
}

// Finding represents a single best-practice violation.
type Finding struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	CheckID   string `json:"checkID"`
	Category  string `json:"category"` // "Security", "Reliability", "Efficiency"
	Severity  string `json:"severity"` // "warning" or "danger"
	Message   string `json:"message"`
}

// Categories
const (
	CategorySecurity    = "Security"
	CategoryReliability = "Reliability"
	CategoryEfficiency  = "Efficiency"
)

// Severities
const (
	SeverityWarning = "warning"
	SeverityDanger  = "danger"
)
