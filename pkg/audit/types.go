package audit

import (
	"github.com/skyhook-io/radar/pkg/checks"
	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

	// Crossplane resources arrive unstructured because every provider ships
	// its own CRDs — there's no typed Go schema to share across them. The
	// audit layer doesn't enumerate kinds; it inspects spec/status shape.
	// Populated by callers from a dynamic resource cache; nil when Crossplane
	// isn't installed or RBAC denies discovery.
	ManagedResources   []*unstructured.Unstructured // detected by spec.providerConfigRef (v1) or spec.crossplane.providerConfigRef (v2)
	CompositeResources []*unstructured.Unstructured // detected by spec.resourceRefs / spec.crossplane.resourceRefs; includes v1 Claims

	// Traefik CRDs arrive unstructured (no shared typed schema). Used by the
	// reference-integrity checks (route → Service/Middleware). Populated from the
	// dynamic cache; nil when Traefik isn't installed or RBAC denies it. Both the
	// traefik.io and legacy traefik.containo.us groups land here.
	IngressRoutes   []*unstructured.Unstructured // IngressRoute / IngressRouteTCP / IngressRouteUDP (scoped to audited namespaces)
	Middlewares     []*unstructured.Unstructured // Middleware / MiddlewareTCP (cluster-wide, for cross-ns ref resolution)
	TraefikServices []*unstructured.Unstructured // TraefikService (cluster-wide)
	// MiddlewareSubjects are Middleware/MiddlewareTCP in the AUDITED namespaces —
	// the subjects of the chain/errors ref-integrity checks. Distinct from
	// Middlewares (cluster-wide targets): refs resolve against the whole cluster,
	// but findings are only reported for middlewares we're auditing, so a
	// middleware in a non-audited namespace never leaks into a scoped scan.
	MiddlewareSubjects []*unstructured.Unstructured
	// TraefikAuthoritativeKinds keys (group\x00Kind) the target kinds served by a
	// synced cluster-wide informer. The dangling-ref check asserts "missing" only
	// for kinds present here — otherwise the cache may know only a subset of
	// namespaces and absence isn't conclusive. nil → assert nothing (safe default).
	TraefikAuthoritativeKinds map[string]bool
	// AllServices is the cluster-wide Service list (all namespaces) for resolving
	// Traefik route → Service references, including cross-namespace ones.
	AllServices []*corev1.Service
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
//
// Checks is the catalog (checkID -> definition) — kept under the "checks" JSON
// tag for back-compat with already-deployed agents/connectors. GroupedChecks is
// the remediation-queue rollup; it rides under a separate tag rather than
// renaming the catalog so older consumers don't break.
type ScanResults struct {
	Summary  ScanSummary          `json:"summary"`
	Findings []Finding            `json:"findings"`
	Groups   []ResourceGroup      `json:"groups"`
	Checks   map[string]CheckMeta `json:"checks"`
	// GroupedChecks is the per-check remediation-queue rollup (one Check per
	// failing check). Populated by the HTTP audit handler post local-settings —
	// not by RunChecks, which doesn't carry the request context BuildChecks
	// needs. Omitted from the raw scan the Hub fan-out consumes: the Hub
	// recomputes the rollup itself after applying org Checks policy.
	GroupedChecks []checks.Check `json:"groupedChecks,omitempty"`
}

// ResourceGroup aggregates findings for a single resource.
// Groups are sorted by severity (danger first), then by name.
// Group disambiguates kinds that collide across API groups
// (e.g. core/Service vs serving.knative.dev/Service); empty for the
// core API group.
type ResourceGroup struct {
	Kind      string    `json:"kind"`
	Group     string    `json:"group,omitempty"`
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
// Group disambiguates kinds that collide across API groups
// (e.g. core/Service vs serving.knative.dev/Service); empty for the
// core API group. Check emission sites leave Group="" — buildResults
// populates it via groupForBuiltinKind so the (Kind→Group) map lives
// in one place rather than every check function.
type Finding struct {
	Kind      string `json:"kind"`
	Group     string `json:"group,omitempty"`
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
