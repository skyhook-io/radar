package upgradereadiness

import (
	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	policyv1 "k8s.io/api/policy/v1"
	storagev1 "k8s.io/api/storage/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type Verdict string

const (
	VerdictBlocked         Verdict = "blocked"
	VerdictWarning         Verdict = "warning"
	VerdictReview          Verdict = "review"
	VerdictNoKnownBlockers Verdict = "no_known_blockers"
	VerdictUnknown         Verdict = "unknown"
)

type CheckStatus string

const (
	CheckPassed        CheckStatus = "passed"
	CheckBlocked       CheckStatus = "blocked"
	CheckWarning       CheckStatus = "warning"
	CheckReview        CheckStatus = "review"
	CheckUnknown       CheckStatus = "unknown"
	CheckNotApplicable CheckStatus = "not_applicable"
)

type Level string

const (
	LevelBlocker Level = "blocker"
	LevelWarning Level = "warning"
	LevelReview  Level = "review"
)

type Input struct {
	// Namespaces is nil for a cluster-wide scan and populated when live
	// namespaced evidence was filtered by an RBAC or configured cache ceiling.
	Namespaces []string
	// CacheScopedKinds records informer kinds whose cached live evidence covers
	// only the listed namespaces. It is separate from Namespaces because mixed
	// per-kind RBAC scopes cannot be represented by one global filter.
	CacheScopedKinds map[string][]string

	Pods         []*corev1.Pod
	Deployments  []*appsv1.Deployment
	ReplicaSets  []*appsv1.ReplicaSet
	StatefulSets []*appsv1.StatefulSet
	DaemonSets   []*appsv1.DaemonSet
	Jobs         []*batchv1.Job
	CronJobs     []*batchv1.CronJob
	Services     []*corev1.Service
	ConfigMaps   []*corev1.ConfigMap
	// WebhookServices contains the explicitly collected Service backends
	// referenced by admission and CRD conversion webhooks. It must not be
	// inferred from Services, whose informer may have a different RBAC scope.
	WebhookServices        []*corev1.Service
	PersistentVolumeClaims []*corev1.PersistentVolumeClaim
	PersistentVolumes      []*corev1.PersistentVolume
	Nodes                  []*corev1.Node
	Events                 []*corev1.Event
	PodDisruptionBudgets   []*policyv1.PodDisruptionBudget
	EndpointSlices         []*discoveryv1.EndpointSlice
	CSIDrivers             []*storagev1.CSIDriver

	SchedulingV1Alpha2Objects            []*unstructured.Unstructured
	SchedulingV1Alpha2Installed          bool
	SchedulingV1Alpha2DiscoveryAvailable bool
	SchedulingV1Alpha2UnavailableKinds   []string

	// AdmissionWebhookConfigurations, CustomResourceDefinitions, and
	// APIServices are nil
	// when their cluster-scoped sources could not be read. Empty, non-nil
	// slices mean the sources were inspected and contained no objects.
	AdmissionWebhookConfigurations []*unstructured.Unstructured
	// AdmissionWebhookUnavailableKinds records configuration
	// kinds that could not be listed while preserving evidence from readable
	// kinds.
	AdmissionWebhookUnavailableKinds []string
	// AdmissionWebhookDeniedKinds is the subset of unavailable webhook
	// configuration kinds whose list request was denied.
	AdmissionWebhookDeniedKinds []string
	CustomResourceDefinitions   []*unstructured.Unstructured
	APIServices                 []*unstructured.Unstructured
	// NodeProxyForbidden records that authorization or an observed request
	// denied access to kubelet metrics or effective configuration.
	NodeProxyForbidden bool
	// NodeRuntimeEvidence is nil when kubelet metrics and configuration could not be inspected.
	NodeRuntimeEvidence []NodeRuntimeEvidence

	// SourceObjects are live typed objects whose kubectl last-applied annotation
	// can preserve the apiVersion originally submitted to the API server.
	SourceObjects []metav1.Object
	// SourceObjectUnavailableKinds records direct source-object list failures.
	// Successful kinds remain usable while checks expose the coverage gap.
	SourceObjectUnavailableKinds []string
	// ManifestResources come from rendered Helm release manifests. Unlike live
	// objects, they preserve the apiVersion that a future reconcile will submit.
	ManifestResources []ManifestResource
	// HelmUnavailableNamespaces names namespaces whose stored release
	// manifests could not be read while other namespaces were inspected.
	HelmUnavailableNamespaces []string
	// HelmScopedNamespaces records a Secret-RBAC ceiling narrower than the
	// workload scan scope.
	HelmScopedNamespaces []string
	ManifestParseErrors  int

	// DeprecatedAPIRequests is nil when /metrics could not be read. An empty,
	// non-nil slice means the sampled API server process had no active series.
	DeprecatedAPIRequests []DeprecatedAPIRequest
	// DeprecatedAPIMetricsWindow is the observed API server process lifetime,
	// formatted for display by the collector that read the metrics endpoint.
	DeprecatedAPIMetricsWindow string
	// PrometheusRules is nil when the CRD exists but Radar could not inspect it.
	// An empty slice with PrometheusRulesInstalled=false means not applicable.
	PrometheusRules                     []*unstructured.Unstructured
	PrometheusRulesInstalled            bool
	PrometheusRulesDiscoveryAvailable   bool
	PrometheusRuleUnavailableNamespaces []string
	Platform                            string
}

type ManifestResource struct {
	APIVersion      string
	Kind            string
	Namespace       string
	Name            string
	Source          string
	SourceNamespace string
	SourceName      string
	Object          *unstructured.Unstructured
}

// NodeRuntimeEvidence preserves upgrade-related kubelet metrics and effective
// configuration sampled from one node. Availability is distinct from zero values.
type NodeRuntimeEvidence struct {
	NodeName                  string
	MetricsAvailable          bool
	CgroupVersion             int
	CgroupVersionAvailable    bool
	CRILosingSupportVersion   string
	CRILosingSupportAvailable bool
	ConfigAvailable           bool
	EventRecordQPS            int32
	EventRecordQPSAvailable   bool
	FeatureGates              map[string]bool
	SELinuxMismatchWarnings   float64
	SELinuxMismatchErrors     float64
}

type DeprecatedAPIRequest struct {
	Group          string
	Version        string
	Resource       string
	Subresource    string
	RemovedRelease string
	Requests       float64
}

type ResourceRef struct {
	Group     string `json:"group,omitempty"`
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type Evidence struct {
	Source string `json:"source"`
	Path   string `json:"path"`
	Detail string `json:"detail,omitempty"`
}

type Reference struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

type Finding struct {
	RuleID      string       `json:"ruleID"`
	Title       string       `json:"title"`
	Level       Level        `json:"level"`
	Resource    *ResourceRef `json:"resource,omitempty"`
	ManagedBy   *ResourceRef `json:"managedBy,omitempty"`
	Evidence    Evidence     `json:"evidence"`
	AppliesFrom string       `json:"appliesFrom,omitempty"`
	Impact      string       `json:"impact"`
	Remediation string       `json:"remediation"`
	References  []Reference  `json:"references"`
}

type Check struct {
	ID           string      `json:"id"`
	Category     string      `json:"category"`
	Title        string      `json:"title"`
	Status       CheckStatus `json:"status"`
	Summary      string      `json:"summary"`
	EvidenceNote string      `json:"evidenceNote,omitempty"`
	Caveat       string      `json:"caveat,omitempty"`
	Scope        string      `json:"scope"`
	Inspected    int         `json:"inspected,omitempty"`
	AppliesFrom  string      `json:"appliesFrom,omitempty"`
	Findings     []Finding   `json:"findings"`
	References   []Reference `json:"references,omitempty"`
}

type Summary struct {
	Blocked       int `json:"blocked"`
	Warnings      int `json:"warnings"`
	Reviews       int `json:"reviews"`
	Passed        int `json:"passed"`
	Unknown       int `json:"unknown"`
	NotApplicable int `json:"notApplicable"`
	Findings      int `json:"findings"`
}

type Coverage struct {
	Source           string              `json:"source"`
	State            string              `json:"state"`
	UnavailableKinds []string            `json:"unavailableKinds,omitempty"`
	ScopedNamespaces []string            `json:"scopedNamespaces,omitempty"`
	ScopedKinds      map[string][]string `json:"scopedKinds,omitempty"`
}

type ScanResults struct {
	CurrentVersion  string   `json:"currentVersion"`
	TargetVersion   string   `json:"targetVersion"`
	ReviewedThrough string   `json:"reviewedThrough"`
	Verdict         Verdict  `json:"verdict"`
	Summary         Summary  `json:"summary"`
	Checks          []Check  `json:"checks"`
	Coverage        Coverage `json:"coverage"`
}
