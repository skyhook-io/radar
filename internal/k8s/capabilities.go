package k8s

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	authv1 "k8s.io/api/authorization/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/skyhook-io/radar/pkg/k8score"
)

// ResourcePermissions indicates which resource types the user can list/watch
type ResourcePermissions struct {
	Pods                     bool `json:"pods"`
	Services                 bool `json:"services"`
	Deployments              bool `json:"deployments"`
	DaemonSets               bool `json:"daemonSets"`
	StatefulSets             bool `json:"statefulSets"`
	ReplicaSets              bool `json:"replicaSets"`
	Ingresses                bool `json:"ingresses"`
	IngressClasses           bool `json:"ingressClasses"`
	ConfigMaps               bool `json:"configMaps"`
	Secrets                  bool `json:"secrets"`
	Events                   bool `json:"events"`
	PersistentVolumeClaims   bool `json:"persistentVolumeClaims"`
	Nodes                    bool `json:"nodes"`
	Namespaces               bool `json:"namespaces"`
	Jobs                     bool `json:"jobs"`
	CronJobs                 bool `json:"cronJobs"`
	HorizontalPodAutoscalers bool `json:"horizontalPodAutoscalers"`
	PersistentVolumes        bool `json:"persistentVolumes"`
	StorageClasses           bool `json:"storageClasses"`
	PodDisruptionBudgets     bool `json:"podDisruptionBudgets"`
	NetworkPolicies          bool `json:"networkPolicies"`
	ServiceAccounts          bool `json:"serviceAccounts"`
	Roles                    bool `json:"roles"`
	ClusterRoles             bool `json:"clusterRoles"`
	RoleBindings             bool `json:"roleBindings"`
	ClusterRoleBindings      bool `json:"clusterRoleBindings"`
	LimitRanges              bool `json:"limitRanges"`
	ResourceQuotas           bool `json:"resourceQuotas"`
	Gateways                 bool `json:"gateways"`
	HTTPRoutes               bool `json:"httpRoutes"`
	VerticalPodAutoscalers   bool `json:"verticalPodAutoscalers"`
}

// PermissionCheckResult holds the result of resource access probes.
//
// Two views over the same probe pass:
//   - Perms / NamespaceScoped / Namespace: a uniform projection (Perm=true if
//     any scope works; NamespaceScoped=true if at least one kind ended up
//     namespace-scoped). Used by callers that just want a "can the user see
//     anything?" answer.
//   - Scopes / ScopeNamespaces: the per-kind authoritative map that drives
//     informer wiring — some kinds may be cluster-wide while others are
//     namespace-scoped on the same cluster, and a namespace-scoped kind may
//     be readable in several explicitly named namespaces.
type PermissionCheckResult struct {
	Perms           *ResourcePermissions
	NamespaceScoped bool   // True if at least one resource type ended up namespace-scoped
	Namespace       string // The fallback namespace used for namespace-scoped probes
	Scopes          map[string]k8score.ResourceScope
	ScopeNamespaces map[string][]string // Per-kind namespace-scoped informer fanout; nil/empty means use Scopes[key].Namespace
}

// Capabilities represents the features available based on RBAC permissions
type Capabilities struct {
	Exec           bool                     `json:"exec"`                  // Can create pods/exec (terminal feature)
	LocalTerminal  bool                     `json:"localTerminal"`         // Local terminal available (not in-cluster, not disabled)
	Logs           bool                     `json:"logs"`                  // Can get pods/log (log viewer)
	PortForward    bool                     `json:"portForward"`           // Can create pods/portforward
	Secrets        bool                     `json:"secrets"`               // Can list secrets
	SecretsUpdate  bool                     `json:"secretsUpdate"`         // Can update secrets (inline editing)
	HelmWrite      bool                     `json:"helmWrite"`             // Helm write ops (detected via secrets/create as sentinel RBAC check)
	NodeWrite      bool                     `json:"nodeWrite"`             // Can patch nodes (cordon/uncordon/drain)
	WorkloadWrites WorkloadWritePermissions `json:"workloadWrites"`        // Can patch workload kinds (restart/scale controls)
	MCPEnabled     bool                     `json:"mcpEnabled"`            // MCP server is running
	Deployment     DeploymentInfo           `json:"deployment"`            // How / where this Radar binary is running. Tells the UI which chrome to render or suppress (e.g. embedded mode hides the cluster headline + local-MCP card because the hub already renders both).
	AuthEnabled    bool                     `json:"authEnabled,omitempty"` // Auth is enabled on the server
	Username       string                   `json:"username,omitempty"`    // Authenticated username (when auth enabled)
	Resources      *ResourcePermissions     `json:"resources,omitempty"`   // Per-resource-type permissions
	Visibility     *VisibilitySummary       `json:"visibility,omitempty"`  // Present when resource visibility is limited enough to make diagnostics incomplete
}

// WorkloadWritePermissions indicates which workload resources the user can patch.
type WorkloadWritePermissions struct {
	Deployments  bool `json:"deployments"`
	DaemonSets   bool `json:"daemonSets"`
	StatefulSets bool `json:"statefulSets"`
	Rollouts     bool `json:"rollouts"`
}

type WorkloadWriteCapabilityErrors struct {
	Deployments  bool
	DaemonSets   bool
	StatefulSets bool
	Rollouts     bool
}

type NamespaceCapabilityErrors struct {
	Exec           bool
	Logs           bool
	PortForward    bool
	WorkloadWrites WorkloadWriteCapabilityErrors
}

// NamespaceCapabilities holds the effective capabilities for a specific namespace.
type NamespaceCapabilities struct {
	Exec           bool                      `json:"exec"`
	Logs           bool                      `json:"logs"`
	PortForward    bool                      `json:"portForward"`
	WorkloadWrites WorkloadWritePermissions  `json:"workloadWrites"`
	Errors         NamespaceCapabilityErrors `json:"-"`
}

// DeploymentInfo describes how / where this Radar binary is running.
// The frontend uses Mode to gate chrome that only makes sense in some
// topologies — e.g. cloud-connected mode hides the cluster headline
// because Radar Cloud's hub renders it in the top bar; in-cluster mode
// falls back to the platform label for the cluster name because the
// kubeconfig context is the meaningless "in-cluster" sentinel.
//
// The set is closed; if a new topology ships (air-gapped, on-prem-SAML,
// BYOC, ...), add a member here and update consumers — the bool
// alternative would force every consumer to grow `mode-A || mode-B`
// disjunctions ad hoc.
type DeploymentMode string

const (
	// DeploymentModeLocal: Radar binary running on a developer's
	// machine with a kubeconfig. The most common OSS path.
	DeploymentModeLocal DeploymentMode = "local"
	// DeploymentModeInCluster: Radar pod running inside the cluster
	// it's observing, with no kubeconfig. The kubeconfig context name
	// is set to the literal "in-cluster" sentinel during bootstrap.
	// Frontend should fall back to the platform label for headlines.
	DeploymentModeInCluster DeploymentMode = "in-cluster"
	// DeploymentModeCloud: Radar pod running in-cluster AND tunneled
	// to Radar Cloud's hub (RADAR_CLOUD_MODE=true; technically a
	// superset of in-cluster mode plus the outbound tunnel). The hub
	// shell renders cluster identity + MCP discovery surfaces, so the
	// embedded UI suppresses both.
	DeploymentModeCloud DeploymentMode = "cloud"
)

// DeploymentInfo is exposed in the Capabilities response. Currently
// just Mode; reserved as a struct so future deployment-scoped facts
// (region, cluster id surface, helm chart version) can be added
// without another wire-shape change.
type DeploymentInfo struct {
	Mode DeploymentMode `json:"mode"`
}

var (
	cachedCapabilities   *Capabilities
	capabilitiesMu       sync.RWMutex
	capabilitiesExpiry   time.Time
	capabilitiesTTL      = 60 * time.Second
	capabilitiesErrorTTL = 5 * time.Second // Short TTL when API errors caused fail-closed results

	// Per-namespace capability cache for lazy RBAC re-checks.
	// When global checks (cluster-wide + effective-namespace) deny
	// exec/logs/portForward, callers can re-check for a specific namespace.
	nsCapCache map[string]*nsCapEntry
	nsCapMu    sync.RWMutex

	// ForceDisableHelmWrite overrides the helmWrite capability to false (for dev testing)
	ForceDisableHelmWrite bool
	// ForceDisableExec overrides the exec capability to false (for dev testing)
	ForceDisableExec bool
	// ForceDisableLocalTerminal overrides the localTerminal capability to false (for dev testing)
	ForceDisableLocalTerminal bool
)

type nsCapEntry struct {
	caps   NamespaceCapabilities
	expiry time.Time
}

// CheckCapabilities checks RBAC permissions using SelfSubjectAccessReview.
// Results are cached for 60 seconds normally, or 5 seconds when API errors
// caused fail-closed results (to allow rapid retry without long UI disruption).
func CheckCapabilities(ctx context.Context) (*Capabilities, error) {
	capabilitiesMu.RLock()
	if cachedCapabilities != nil && time.Now().Before(capabilitiesExpiry) {
		caps := *cachedCapabilities
		capabilitiesMu.RUnlock()
		return &caps, nil
	}
	capabilitiesMu.RUnlock()

	// Compute capabilities WITHOUT holding the write lock.
	// Multiple concurrent callers may race, but redundant checks are harmless.
	// Critical: holding the lock during network calls blocks
	// InvalidateCapabilitiesCache() during context switch.

	if GetClient() == nil {
		// Return all false if client not initialized (fail closed)
		log.Printf("Warning: K8s client not initialized, returning restricted capabilities")
		return &Capabilities{Exec: false, Logs: false, PortForward: false, Secrets: false, SecretsUpdate: false, HelmWrite: false}, nil
	}

	// Don't start RBAC checks when disconnected — the exec credential plugin
	// serializes all API calls per-process, so browser-polled capability checks
	// would block retry/context-switch connectivity tests.
	if GetConnectionStatus().State == StateDisconnected {
		return &Capabilities{}, nil
	}

	// Use the operation context so RBAC checks are canceled on context switch.
	// This prevents stale exec plugin calls from serializing and blocking the
	// new context's connectivity test.
	checkCtx, cancel := NewOperationContext(10 * time.Second)
	defer cancel()

	capStart := time.Now()
	logTiming("   [caps] CheckCapabilities starting RBAC checks")

	// Check each capability in parallel.
	// Try cluster-wide first, then namespace-scoped as fallback for namespace-scoped users.
	// Track API errors to avoid caching transient failures for the full TTL.
	fallbackNs := GetEffectiveNamespace()
	var hadErrors atomic.Bool

	type capCheck struct {
		group             string
		resource          string
		verb              string
		result            *bool
		namespaceFallback bool
	}

	caps := &Capabilities{}
	checks := []capCheck{
		{resource: "pods/exec", verb: "create", result: &caps.Exec, namespaceFallback: true},
		{resource: "pods/log", verb: "get", result: &caps.Logs, namespaceFallback: true},
		{resource: "pods/portforward", verb: "create", result: &caps.PortForward, namespaceFallback: true},
		{resource: "secrets", verb: "list", result: &caps.Secrets, namespaceFallback: true},
		{resource: "secrets", verb: "update", result: &caps.SecretsUpdate, namespaceFallback: true},
		{resource: "secrets", verb: "create", result: &caps.HelmWrite, namespaceFallback: true},
		{resource: "nodes", verb: "patch", result: &caps.NodeWrite, namespaceFallback: true},
		{group: "apps", resource: "deployments", verb: "patch", result: &caps.WorkloadWrites.Deployments},
		{group: "apps", resource: "daemonsets", verb: "patch", result: &caps.WorkloadWrites.DaemonSets},
		{group: "apps", resource: "statefulsets", verb: "patch", result: &caps.WorkloadWrites.StatefulSets},
		{group: "argoproj.io", resource: "rollouts", verb: "patch", result: &caps.WorkloadWrites.Rollouts},
	}

	var wg sync.WaitGroup
	wg.Add(len(checks))

	for _, check := range checks {
		go func(c capCheck) {
			defer wg.Done()
			allowed, apiErr := canI(checkCtx, "", c.group, c.resource, c.verb)
			if allowed {
				*c.result = true
				return
			}
			if fallbackNs != "" && c.namespaceFallback {
				allowed, nsApiErr := canI(checkCtx, fallbackNs, c.group, c.resource, c.verb)
				if allowed {
					*c.result = true
					return
				}
				apiErr = apiErr || nsApiErr
			}
			if apiErr {
				hadErrors.Store(true)
			}
		}(check)
	}

	wg.Wait()
	logTiming("   [caps] CheckCapabilities RBAC checks done (%v)", time.Since(capStart))

	// Local terminal is not RBAC-gated — it depends on runtime mode only
	caps.LocalTerminal = !IsInCluster() && !ForceDisableLocalTerminal

	// Port-forward binds a local TCP listener on the radar host; like the local
	// terminal it's meaningless in-cluster (the listener would be on the radar
	// pod, not the user's machine), so force it off regardless of RBAC. The HTTP
	// capabilities handler re-applies this after its namespace merge.
	if IsInCluster() {
		caps.PortForward = false
	}

	if ForceDisableHelmWrite {
		caps.HelmWrite = false
	}
	if ForceDisableExec {
		caps.Exec = false
	}

	// Cache the result. Use a short TTL if API errors caused fail-closed results,
	// so transient K8s API failures don't hide UI controls for a full minute.
	ttl := capabilitiesTTL
	if hadErrors.Load() {
		ttl = capabilitiesErrorTTL
		log.Printf("Warning: capability checks had API errors, using short cache TTL (%v)", ttl)
	}
	capabilitiesMu.Lock()
	cachedCapabilities = caps
	capabilitiesExpiry = time.Now().Add(ttl)
	capabilitiesMu.Unlock()

	return caps, nil
}

// canI checks if the current user/service account can perform an action.
// Returns (allowed, apiErr) — wraps k8score.CanI with the singleton client.
func canI(ctx context.Context, namespace, group, resource, verb string) (allowed bool, apiErr bool) {
	if ctx.Err() != nil {
		logTiming("   [caps] canI(%s %s) skipped: context canceled", verb, resource)
		return false, true
	}
	return k8score.CanI(ctx, GetClient(), namespace, group, resource, verb)
}

// GetCachedCapabilities returns the cached capabilities without triggering
// RBAC checks. Returns nil if no cached result is available.
func GetCachedCapabilities() *Capabilities {
	capabilitiesMu.RLock()
	defer capabilitiesMu.RUnlock()
	if cachedCapabilities == nil {
		return nil
	}
	caps := *cachedCapabilities
	return &caps
}

// InvalidateCapabilitiesCache forces the next CheckCapabilities call to refresh
func InvalidateCapabilitiesCache() {
	capabilitiesMu.Lock()
	cachedCapabilities = nil
	capabilitiesMu.Unlock()

	// Also clear namespace-scoped cache
	nsCapMu.Lock()
	nsCapCache = nil
	nsCapMu.Unlock()
}

// CheckNamespaceCapabilities performs namespace-scoped RBAC checks for
// capabilities that drive resource-level controls.
func CheckNamespaceCapabilities(ctx context.Context, namespace string) (*NamespaceCapabilities, error) {
	if namespace == "" {
		return nil, nil
	}

	// Check namespace cache
	nsCapMu.RLock()
	if nsCapCache != nil {
		if entry, ok := nsCapCache[namespace]; ok && time.Now().Before(entry.expiry) {
			result := entry.caps
			nsCapMu.RUnlock()
			return &result, nil
		}
	}
	nsCapMu.RUnlock()

	if GetClient() == nil {
		return nil, nil // No override — caller will use global caps
	}

	checkCtx, cancel := NewOperationContext(10 * time.Second)
	defer cancel()

	result := &NamespaceCapabilities{}

	type capCheck struct {
		group    string
		resource string
		verb     string
		result   *bool
		apiError *bool
	}

	var checks []capCheck
	if !ForceDisableExec {
		checks = append(checks, capCheck{resource: "pods/exec", verb: "create", result: &result.Exec, apiError: &result.Errors.Exec})
	}
	checks = append(checks,
		capCheck{resource: "pods/log", verb: "get", result: &result.Logs, apiError: &result.Errors.Logs},
		capCheck{resource: "pods/portforward", verb: "create", result: &result.PortForward, apiError: &result.Errors.PortForward},
		capCheck{group: "apps", resource: "deployments", verb: "patch", result: &result.WorkloadWrites.Deployments, apiError: &result.Errors.WorkloadWrites.Deployments},
		capCheck{group: "apps", resource: "daemonsets", verb: "patch", result: &result.WorkloadWrites.DaemonSets, apiError: &result.Errors.WorkloadWrites.DaemonSets},
		capCheck{group: "apps", resource: "statefulsets", verb: "patch", result: &result.WorkloadWrites.StatefulSets, apiError: &result.Errors.WorkloadWrites.StatefulSets},
		capCheck{group: "argoproj.io", resource: "rollouts", verb: "patch", result: &result.WorkloadWrites.Rollouts, apiError: &result.Errors.WorkloadWrites.Rollouts},
	)

	var hadErrors atomic.Bool
	var wg sync.WaitGroup
	wg.Add(len(checks))
	for _, check := range checks {
		go func(c capCheck) {
			defer wg.Done()
			allowed, apiErr := canI(checkCtx, namespace, c.group, c.resource, c.verb)
			if allowed {
				*c.result = true
			}
			if apiErr {
				*c.apiError = true
				hadErrors.Store(true)
			}
		}(check)
	}
	wg.Wait()

	// Cache the result. Use short TTL when API errors caused fail-closed results,
	// matching the pattern in CheckCapabilities.
	ttl := capabilitiesTTL
	if hadErrors.Load() {
		ttl = capabilitiesErrorTTL
		log.Printf("Warning: namespace %s capability checks had API errors, using short cache TTL (%v)", namespace, ttl)
	}
	nsCapMu.Lock()
	if nsCapCache == nil {
		nsCapCache = make(map[string]*nsCapEntry)
	}
	nsCapCache[namespace] = &nsCapEntry{
		caps:   *result,
		expiry: time.Now().Add(ttl),
	}
	nsCapMu.Unlock()

	return result, nil
}

// Per-user capabilities cache (keyed by username)
var (
	userCapabilitiesCache          sync.Map // map[string]*userCapEntry
	userNamespaceCapabilitiesCache sync.Map // map[string]*userNSCapEntry
	userCapabilitiesTTL            = 60 * time.Second
)

type userCapEntry struct {
	caps      *Capabilities
	expiresAt time.Time
}

type userNSCapEntry struct {
	caps      NamespaceCapabilities
	expiresAt time.Time
}

func userNamespaceCapabilitiesCacheKey(username, namespace string) string {
	return username + "\x00" + namespace
}

// CheckCapabilitiesForUser runs SubjectAccessReview as the given user
// to determine what the user can do (exec, logs, delete, helm, etc.)
// Results are cached per-user with 60s TTL.
func CheckCapabilitiesForUser(ctx context.Context, username string, groups []string) (*Capabilities, error) {
	// Check cache
	if entry, ok := userCapabilitiesCache.Load(username); ok {
		e := entry.(*userCapEntry)
		if time.Now().Before(e.expiresAt) {
			caps := *e.caps
			return &caps, nil
		}
	}

	k8sClient := GetClient()
	if k8sClient == nil {
		return &Capabilities{}, nil
	}

	if GetConnectionStatus().State == StateDisconnected {
		return &Capabilities{}, nil
	}

	checkCtx, cancel := NewOperationContext(10 * time.Second)
	defer cancel()

	type capCheck struct {
		group             string
		resource          string
		verb              string
		result            *bool
		namespaceFallback bool
	}

	caps := &Capabilities{}
	checks := []capCheck{
		{resource: "pods/exec", verb: "create", result: &caps.Exec, namespaceFallback: true},
		{resource: "pods/log", verb: "get", result: &caps.Logs, namespaceFallback: true},
		{resource: "pods/portforward", verb: "create", result: &caps.PortForward, namespaceFallback: true},
		{resource: "secrets", verb: "list", result: &caps.Secrets, namespaceFallback: true},
		{resource: "secrets", verb: "update", result: &caps.SecretsUpdate, namespaceFallback: true},
		{resource: "secrets", verb: "create", result: &caps.HelmWrite, namespaceFallback: true},
		{resource: "nodes", verb: "patch", result: &caps.NodeWrite, namespaceFallback: true},
		{group: "apps", resource: "deployments", verb: "patch", result: &caps.WorkloadWrites.Deployments},
		{group: "apps", resource: "daemonsets", verb: "patch", result: &caps.WorkloadWrites.DaemonSets},
		{group: "apps", resource: "statefulsets", verb: "patch", result: &caps.WorkloadWrites.StatefulSets},
		{group: "argoproj.io", resource: "rollouts", verb: "patch", result: &caps.WorkloadWrites.Rollouts},
	}

	var wg sync.WaitGroup
	wg.Add(len(checks))

	for _, check := range checks {
		go func(c capCheck) {
			defer wg.Done()
			allowed, _ := canIAs(checkCtx, k8sClient, username, groups, "", c.group, c.resource, c.verb)
			if allowed {
				*c.result = true
				return
			}
			// Try namespace-scoped fallback
			if fallbackNs := GetEffectiveNamespace(); fallbackNs != "" && c.namespaceFallback {
				allowed, _ = canIAs(checkCtx, k8sClient, username, groups, fallbackNs, c.group, c.resource, c.verb)
				if allowed {
					*c.result = true
				}
			}
		}(check)
	}

	wg.Wait()

	if ForceDisableHelmWrite {
		caps.HelmWrite = false
	}

	// Cache result
	userCapabilitiesCache.Store(username, &userCapEntry{
		caps:      caps,
		expiresAt: time.Now().Add(userCapabilitiesTTL),
	})

	return caps, nil
}

// CheckNamespaceCapabilitiesForUser runs namespace-scoped SubjectAccessReview
// checks as the authenticated user.
func CheckNamespaceCapabilitiesForUser(ctx context.Context, username string, groups []string, namespace string) (*NamespaceCapabilities, error) {
	if namespace == "" {
		return nil, nil
	}

	cacheKey := userNamespaceCapabilitiesCacheKey(username, namespace)
	if entry, ok := userNamespaceCapabilitiesCache.Load(cacheKey); ok {
		e := entry.(*userNSCapEntry)
		if time.Now().Before(e.expiresAt) {
			result := e.caps
			return &result, nil
		}
	}

	k8sClient := GetClient()
	if k8sClient == nil {
		return nil, nil
	}

	if GetConnectionStatus().State == StateDisconnected {
		return nil, nil
	}

	checkCtx, cancel := NewOperationContext(10 * time.Second)
	defer cancel()

	result := &NamespaceCapabilities{}

	type capCheck struct {
		group    string
		resource string
		verb     string
		result   *bool
		apiError *bool
	}

	var checks []capCheck
	if !ForceDisableExec {
		checks = append(checks, capCheck{resource: "pods/exec", verb: "create", result: &result.Exec, apiError: &result.Errors.Exec})
	}
	checks = append(checks,
		capCheck{resource: "pods/log", verb: "get", result: &result.Logs, apiError: &result.Errors.Logs},
		capCheck{resource: "pods/portforward", verb: "create", result: &result.PortForward, apiError: &result.Errors.PortForward},
		capCheck{group: "apps", resource: "deployments", verb: "patch", result: &result.WorkloadWrites.Deployments, apiError: &result.Errors.WorkloadWrites.Deployments},
		capCheck{group: "apps", resource: "daemonsets", verb: "patch", result: &result.WorkloadWrites.DaemonSets, apiError: &result.Errors.WorkloadWrites.DaemonSets},
		capCheck{group: "apps", resource: "statefulsets", verb: "patch", result: &result.WorkloadWrites.StatefulSets, apiError: &result.Errors.WorkloadWrites.StatefulSets},
		capCheck{group: "argoproj.io", resource: "rollouts", verb: "patch", result: &result.WorkloadWrites.Rollouts, apiError: &result.Errors.WorkloadWrites.Rollouts},
	)

	var hadErrors atomic.Bool
	var wg sync.WaitGroup
	wg.Add(len(checks))
	for _, check := range checks {
		go func(c capCheck) {
			defer wg.Done()
			allowed, apiErr := canIAs(checkCtx, k8sClient, username, groups, namespace, c.group, c.resource, c.verb)
			if allowed {
				*c.result = true
			}
			if apiErr {
				*c.apiError = true
				hadErrors.Store(true)
			}
		}(check)
	}
	wg.Wait()

	ttl := userCapabilitiesTTL
	if hadErrors.Load() {
		ttl = capabilitiesErrorTTL
		log.Printf("Warning: namespace %s capability checks for user %s had API errors, using short cache TTL (%v)", SanitizeForLog(namespace), SanitizeForLog(username), ttl)
	}
	userNamespaceCapabilitiesCache.Store(cacheKey, &userNSCapEntry{
		caps:      *result,
		expiresAt: time.Now().Add(ttl),
	})

	return result, nil
}

// canIAs checks if a specific user can perform an action using SubjectAccessReview.
// Unlike canI which uses SelfSubjectAccessReview (checks the ServiceAccount),
// this checks on behalf of a specific user.
func canIAs(ctx context.Context, client *kubernetes.Clientset, username string, groups []string, namespace, group, resource, verb string) (bool, bool) {
	if ctx.Err() != nil {
		return false, true
	}
	if client == nil {
		return false, true
	}

	review := &authv1.SubjectAccessReview{
		Spec: authv1.SubjectAccessReviewSpec{
			User:   username,
			Groups: groups,
			ResourceAttributes: &authv1.ResourceAttributes{
				Namespace: namespace,
				Group:     group,
				Verb:      verb,
				Resource:  resource,
			},
		},
	}

	result, err := client.AuthorizationV1().SubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("Warning: SubjectAccessReview failed for user=%s %s %s: %v", SanitizeForLog(username), SanitizeForLog(verb), SanitizeForLog(resource), err)
		}
		return false, true
	}

	return result.Status.Allowed, false
}

// InvalidateUserCapabilitiesCache clears all per-user capability caches
func InvalidateUserCapabilitiesCache() {
	userCapabilitiesCache.Range(func(key, _ any) bool {
		userCapabilitiesCache.Delete(key)
		return true
	})
	userNamespaceCapabilitiesCache.Range(func(key, _ any) bool {
		userNamespaceCapabilitiesCache.Delete(key)
		return true
	})
}

var (
	cachedPermResult      *PermissionCheckResult
	resourcePermsMu       sync.RWMutex
	resourcePermsExpiry   time.Time
	resourcePermsTTL      = 60 * time.Second
	resourcePermsErrorTTL = 5 * time.Second // Short TTL when API errors caused fail-closed results
)

// resourceProbe describes one typed-resource probe target. The probe issues
// `list?limit=1` against this resource (cluster-wide first, then namespace-scoped
// fallback for non-cluster-scoped kinds when a fallback namespace is set), and
// the result drives whether an informer is created and at what scope.
type resourceProbe struct {
	key         string                      // ResourceType key (k8score.Pods etc.)
	gvr         schema.GroupVersionResource // For dynamic-client probe
	clusterOnly bool                        // true: cannot be namespace-scoped (nodes, namespaces, PV, storageclasses)
	field       *bool                       // Pointer into the boolean view on ResourcePermissions
	// requiresDiscovery marks dynamic-cache CRDs. For these, a NotFound on
	// the list probe means "CRD isn't installed" and we report the field
	// as false. Without this gate, capabilities.resources would
	// optimistically report true on clusters that don't have the CRD
	// installed, because probeListAccessWith treats NotFound the same as
	// any other transient error.
	requiresDiscovery bool
}

// resourceProbeTargets returns the resource kinds we probe access for.
// Includes dynamic-cache CRDs (those tagged requiresDiscovery) so their
// booleans land on ResourcePermissions for the UI snapshot — the dynamic
// cache doesn't write here itself.
func resourceProbeTargets(perms *ResourcePermissions) []resourceProbe {
	return []resourceProbe{
		{key: k8score.Pods, gvr: schema.GroupVersionResource{Version: "v1", Resource: "pods"}, field: &perms.Pods},
		{key: k8score.Services, gvr: schema.GroupVersionResource{Version: "v1", Resource: "services"}, field: &perms.Services},
		{key: k8score.ConfigMaps, gvr: schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}, field: &perms.ConfigMaps},
		{key: k8score.Secrets, gvr: schema.GroupVersionResource{Version: "v1", Resource: "secrets"}, field: &perms.Secrets},
		{key: k8score.Events, gvr: schema.GroupVersionResource{Version: "v1", Resource: "events"}, field: &perms.Events},
		{key: k8score.PersistentVolumeClaims, gvr: schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}, field: &perms.PersistentVolumeClaims},
		{key: k8score.ServiceAccounts, gvr: schema.GroupVersionResource{Version: "v1", Resource: "serviceaccounts"}, field: &perms.ServiceAccounts},
		{key: k8score.LimitRanges, gvr: schema.GroupVersionResource{Version: "v1", Resource: "limitranges"}, field: &perms.LimitRanges},
		{key: k8score.ResourceQuotas, gvr: schema.GroupVersionResource{Version: "v1", Resource: "resourcequotas"}, field: &perms.ResourceQuotas},
		{key: k8score.Nodes, gvr: schema.GroupVersionResource{Version: "v1", Resource: "nodes"}, clusterOnly: true, field: &perms.Nodes},
		{key: k8score.Namespaces, gvr: schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}, clusterOnly: true, field: &perms.Namespaces},
		{key: k8score.PersistentVolumes, gvr: schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumes"}, clusterOnly: true, field: &perms.PersistentVolumes},
		{key: k8score.Deployments, gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, field: &perms.Deployments},
		{key: k8score.DaemonSets, gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}, field: &perms.DaemonSets},
		{key: k8score.StatefulSets, gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}, field: &perms.StatefulSets},
		{key: k8score.ReplicaSets, gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "replicasets"}, field: &perms.ReplicaSets},
		{key: k8score.Ingresses, gvr: schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}, field: &perms.Ingresses},
		{key: k8score.IngressClasses, gvr: schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingressclasses"}, clusterOnly: true, field: &perms.IngressClasses},
		{key: k8score.NetworkPolicies, gvr: schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "networkpolicies"}, field: &perms.NetworkPolicies},
		{key: k8score.Jobs, gvr: schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}, field: &perms.Jobs},
		{key: k8score.CronJobs, gvr: schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "cronjobs"}, field: &perms.CronJobs},
		{key: k8score.HorizontalPodAutoscalers, gvr: schema.GroupVersionResource{Group: "autoscaling", Version: "v2", Resource: "horizontalpodautoscalers"}, field: &perms.HorizontalPodAutoscalers},
		{key: k8score.StorageClasses, gvr: schema.GroupVersionResource{Group: "storage.k8s.io", Version: "v1", Resource: "storageclasses"}, clusterOnly: true, field: &perms.StorageClasses},
		{key: k8score.PodDisruptionBudgets, gvr: schema.GroupVersionResource{Group: "policy", Version: "v1", Resource: "poddisruptionbudgets"}, field: &perms.PodDisruptionBudgets},
		{key: k8score.Roles, gvr: schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "roles"}, field: &perms.Roles},
		{key: k8score.ClusterRoles, gvr: schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterroles"}, clusterOnly: true, field: &perms.ClusterRoles},
		{key: k8score.RoleBindings, gvr: schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "rolebindings"}, field: &perms.RoleBindings},
		{key: k8score.ClusterRoleBindings, gvr: schema.GroupVersionResource{Group: "rbac.authorization.k8s.io", Version: "v1", Resource: "clusterrolebindings"}, clusterOnly: true, field: &perms.ClusterRoleBindings},
		// Dynamic-cache CRDs — the bool surfaces in the UI even though they're
		// served via the dynamic informer path, not buildInformerSetups().
		// requiresDiscovery makes IsNotFound deny instead of optimistically
		// allow, so we don't claim a CRD is supported when it isn't installed.
		{key: "gateways", gvr: schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"}, field: &perms.Gateways, requiresDiscovery: true},
		{key: "httproutes", gvr: schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}, field: &perms.HTTPRoutes, requiresDiscovery: true},
		{key: "verticalpodautoscalers", gvr: schema.GroupVersionResource{Group: "autoscaling.k8s.io", Version: "v1", Resource: "verticalpodautoscalers"}, field: &perms.VerticalPodAutoscalers, requiresDiscovery: true},
	}
}

// SanitizeForLog strips CR/LF from a string before it's written to a log.
// Use this for any value that originates from user-controlled input
// (HTTP request bodies, kubeconfig fields edited by the user, etc.) —
// without it, an attacker-controlled string containing newlines could
// inject forged log entries. CodeQL's `Log entries created from user
// input` rule fires on tainted strings even when wrapped in %q because
// the taint analyzer doesn't model fmt's escaping behavior; an explicit
// strings.ReplaceAll terminates the taint flow.
func SanitizeForLog(s string) string {
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\r", "")
	return s
}

// probeListAccessWith attempts a list?limit=1 against the GVR using the
// given dynamic client. Returns:
//   - allowed=true: list succeeded — informer can run.
//   - allowed=false, forbidden=true: explicit 403/401 — gate the informer.
//   - allowed=true, transient!=nil: non-auth error (network, 503, NotFound for
//     missing CRD, etc.). Treated as "allow optimistically" so a transient API
//     hiccup doesn't permanently disable the resource for the session — the
//     informer's reflector will retry. Same convention as the dynamic cache
//     probe in pkg/k8score/dynamic_cache.go.
//
// Exposed (lowercase but called from tests in the same package) so tests can
// drive it with a fake dynamic.Interface.
func probeListAccessWith(ctx context.Context, dyn dynamic.Interface, gvr schema.GroupVersionResource, namespace string) (allowed bool, forbidden bool, transient error) {
	if dyn == nil {
		return false, false, nil
	}
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	opts := metav1.ListOptions{Limit: 1}
	var err error
	if namespace != "" {
		_, err = dyn.Resource(gvr).Namespace(namespace).List(probeCtx, opts)
	} else {
		_, err = dyn.Resource(gvr).List(probeCtx, opts)
	}
	if err == nil {
		return true, false, nil
	}
	if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
		return false, true, nil
	}
	return true, false, err
}

// probeKindAccess probes a resource kind. For typed probes (single GVR)
// this is a thin wrapper over probeListAccessWith. For dynamic CRD probes
// (requiresDiscovery=true) it walks every version registered in
// supportedCRDFallbacks for the (group, resource) and treats the kind as
// "not installed" only when every version returns NotFound — otherwise
// a cluster serving only v1beta1 Gateway API would be wrongly reported
// as having no Gateway API at all.
func probeKindAccess(ctx context.Context, dyn dynamic.Interface, p resourceProbe, namespace string) (allowed bool, forbidden bool, transient error) {
	if !p.requiresDiscovery {
		return probeListAccessWith(ctx, dyn, p.gvr, namespace)
	}
	gvrs := resolveProbeGVRs(p)
	var (
		sawForbidden    bool
		lastNonNotFound error
	)
	for _, gvr := range gvrs {
		a, f, t := probeListAccessWith(ctx, dyn, gvr, namespace)
		if a && t == nil {
			return true, false, nil
		}
		if f {
			// Native K8s RBAC is version-agnostic (apiGroups stanza names
			// the group, not specific versions), so a 403 on any version
			// almost always means all versions are denied — short-circuit.
			// Webhook authorizers (OPA, Kyverno, GKE IAM) can theoretically
			// gate by version but rarely do; we accept the false-negative
			// on those clusters in exchange for not multiplying probe
			// latency by the number of CRD versions.
			sawForbidden = true
			break
		}
		if t != nil && !apierrors.IsNotFound(t) {
			lastNonNotFound = t
		}
	}
	if sawForbidden {
		return false, true, nil
	}
	if lastNonNotFound != nil {
		// At least one version hit a real transient (5xx, network, etc.).
		// Preserve optimistic-allow so the informer can retry rather than
		// permanently disabling the resource for the session — matches the
		// behavior for typed kinds in probeListAccessWith.
		return true, false, lastNonNotFound
	}
	// All versions returned NotFound — CRD truly not installed. Cached for
	// the full 60s TTL (transient=nil → hadErrors stays false). If the
	// CRD installs mid-cache, the user sees stale capabilities until the
	// next TTL expiry or context-switch invalidation; CRD installs are
	// operator-initiated and rare enough that this is acceptable.
	return false, false, nil
}

// resolveProbeGVRs returns every version registered in supportedCRDFallbacks
// for a dynamic probe, so the probe matches what the dynamic cache can
// actually serve. Typed probes return their single configured GVR.
func resolveProbeGVRs(p resourceProbe) []schema.GroupVersionResource {
	if !p.requiresDiscovery {
		return []schema.GroupVersionResource{p.gvr}
	}
	for _, c := range supportedCRDFallbacks {
		if c.Group != p.gvr.Group || c.Resource != p.gvr.Resource {
			continue
		}
		out := make([]schema.GroupVersionResource, 0, len(c.Versions))
		for _, v := range c.Versions {
			out = append(out, schema.GroupVersionResource{Group: c.Group, Version: v, Resource: c.Resource})
		}
		if len(out) > 0 {
			return out
		}
	}
	return []schema.GroupVersionResource{p.gvr}
}

// CheckResourcePermissions probes list access for every typed resource and
// returns per-kind scope plus a uniform projection. Results are cached for
// 60s (5s on transient errors).
//
// Per-kind probe behavior:
//   - Cluster-wide list?limit=1 first.
//   - If 403/401 and the kind is namespaceable AND a fallback namespace is set,
//     retry scoped to that namespace.
//   - Anything still 403/401 → kind is denied.
//   - Anything that returns a non-auth error → optimistically allowed
//     cluster-wide, except dynamic-cache probes use NotFound to mean the
//     CRD is not installed.
//
// This is authoritative because it IS the operation the informer will perform.
// SelfSubjectAccessReview is one indirection too many — it can disagree with
// reality on clusters using webhook authorizers (e.g. GKE IAM).
func CheckResourcePermissions(ctx context.Context) *PermissionCheckResult {
	resourcePermsMu.RLock()
	if cachedPermResult != nil && time.Now().Before(resourcePermsExpiry) {
		// Deep-copy so callers can't mutate the cached result.
		permsCopy := *cachedPermResult.Perms
		scopesCopy := make(map[string]k8score.ResourceScope, len(cachedPermResult.Scopes))
		for k, v := range cachedPermResult.Scopes {
			scopesCopy[k] = v
		}
		scopeNamespacesCopy := make(map[string][]string, len(cachedPermResult.ScopeNamespaces))
		for k, v := range cachedPermResult.ScopeNamespaces {
			scopeNamespacesCopy[k] = append([]string(nil), v...)
		}
		result := &PermissionCheckResult{
			Perms:           &permsCopy,
			NamespaceScoped: cachedPermResult.NamespaceScoped,
			Namespace:       cachedPermResult.Namespace,
			Scopes:          scopesCopy,
			ScopeNamespaces: scopeNamespacesCopy,
		}
		resourcePermsMu.RUnlock()
		return result
	}
	resourcePermsMu.RUnlock()

	// Compute probes WITHOUT holding the write lock — concurrent callers
	// may race but redundant probes are harmless. Holding the lock during
	// network calls would block InvalidateResourcePermissionsCache() during
	// context switch.

	if GetClient() == nil || GetDynamicClient() == nil {
		log.Printf("Warning: K8s client not initialized, returning no resource permissions")
		return &PermissionCheckResult{Perms: &ResourcePermissions{}, Scopes: map[string]k8score.ResourceScope{}}
	}

	forceNamespace := ForceNamespaceScope
	scopeNamespaces := buildScopeCandidates(ctx)
	if forceNamespace {
		if target := GetNamespaceScopeTarget(); target != "" {
			scopeNamespaces = mergeForcedScopeCandidate(target, scopeNamespaces)
		} else {
			log.Printf("Warning: --namespace-scope enabled but no namespace target is configured")
		}
	}

	result, hadErrors := probeResourceAccess(ctx, GetDynamicClient(), scopeNamespaces, forceNamespace)

	resourcePermsMu.Lock()
	cachedPermResult = result
	ttl := resourcePermsTTL
	if hadErrors {
		ttl = resourcePermsErrorTTL
		log.Printf("Warning: resource access probes had API errors, using short cache TTL (%v)", ttl)
	}
	resourcePermsExpiry = time.Now().Add(ttl)
	resourcePermsMu.Unlock()

	return result
}

// MaxScopeCandidates was promoted to a package variable in deadlines.go
// (default 20) so operators with clusters carrying more namespaces than the
// original cap can widen the fanout via flag/env without recompiling.

// buildScopeCandidates returns the namespace candidates for the fallback
// probe when cluster-wide list is denied. Kubeconfig context (or
// --namespace) first; then namespaces from GetAccessibleNamespaces, in the
// cluster-list order (alphabetical). Empty when no fallback is configured
// and the user can't enumerate namespaces — caller treats that as "no
// fallback".
func buildScopeCandidates(ctx context.Context) []string {
	// GetEffectiveNamespace would return only one (kubeconfig context wins
	// over --namespace). Reach into both globals so when an operator sets
	// `--namespace` distinct from the context, both surface as candidates.
	clientMu.RLock()
	ctxNs := contextNamespace
	flagNamespaces := fallbackNamespaceCandidatesLocked()
	clientMu.RUnlock()

	accessible, authoritative := GetAccessibleNamespaces(ctx)
	out, dropped := mergeScopeCandidateLists(ctxNs, flagNamespaces, accessible, authoritative)
	if dropped > 0 {
		// Capped: kinds the user can list only in a dropped namespace stay
		// marked denied. This must log on the non-authoritative path too —
		// that is where operator-named --namespaces entries get dropped
		// (e.g. a full-cap list plus a distinct kubeconfig context ns).
		log.Printf("RBAC: candidate namespaces truncated (cap=%d, %d dropped); kinds reachable only in dropped namespaces will be marked denied", MaxScopeCandidates, dropped)
	}
	if !authoritative {
		// Authoritative=false means the user can't list namespaces. Without
		// that list the probe can only try whatever the operator named
		// explicitly — log it so an operator diagnosing "Radar disabled my
		// kinds" has a breadcrumb instead of silence.
		log.Printf("RBAC: namespace discovery non-authoritative (cluster-wide list namespaces denied); fallback candidates limited to %v", out)
	}
	return out
}

func mergeForcedScopeCandidate(target string, candidates []string) []string {
	if target == "" {
		return candidates
	}
	out := []string{target}
	for _, ns := range candidates {
		if ns == "" || ns == target {
			continue
		}
		out = append(out, ns)
	}
	return out
}

// mergeScopeCandidates is the pure-function core of buildScopeCandidates:
// dedup + cap + drop-counting with no globals or network calls. When
// authoritative is false the accessible list is ignored — the caller has
// already decided that list is not trustworthy as a probe target.
func mergeScopeCandidates(ctxNs, flagNs string, accessible []string, authoritative bool) (out []string, dropped int) {
	return mergeScopeCandidateLists(ctxNs, []string{flagNs}, accessible, authoritative)
}

func mergeScopeCandidateLists(ctxNs string, flagNamespaces []string, accessible []string, authoritative bool) (out []string, dropped int) {
	seen := map[string]bool{}
	out = make([]string, 0, MaxScopeCandidates)
	atCap := false
	add := func(ns string) {
		if ns == "" || seen[ns] {
			return
		}
		if atCap {
			dropped++
			return
		}
		seen[ns] = true
		out = append(out, ns)
		if len(out) >= MaxScopeCandidates {
			atCap = true
		}
	}
	add(ctxNs)
	for _, ns := range flagNamespaces {
		add(ns)
	}
	if !authoritative {
		// Operator-named namespaces can exceed the cap too — report those
		// drops instead of discarding the count, or the truncation warning
		// can never fire for exactly the users who typed the list out.
		return out, dropped
	}
	// Iterate the full accessible list after cap so add() counts drops.
	for _, ns := range accessible {
		add(ns)
	}
	return out, dropped
}

// pickPrimaryNs picks the namespace that PermissionCheckResult.Namespace
// reports. The dynamic CRD cache reads it as NamespaceFallback (single
// anchor for all CRD informers); it has to be a namespace where the user
// actually has reads, otherwise CRD informers get pinned where they 403.
// Walk candidates in order and pick the first one any typed kind landed
// in. Fall back to scopeNamespaces[0] when nothing was granted — the
// dynamic cache short-circuits on NamespaceScoped=false in that case, so
// the value is only used by the diagnostics page (preserves prior shape).
func pickPrimaryNs(scopeNamespaces []string, scopes map[string]k8score.ResourceScope) string {
	granted := map[string]bool{}
	for _, s := range scopes {
		if s.Enabled && s.Namespace != "" {
			granted[s.Namespace] = true
		}
	}
	for _, ns := range scopeNamespaces {
		if granted[ns] {
			// CRDs the user reads in the OTHER granted namespaces will
			// silently 403 — proper fix needs multi-ns scope per kind in
			// pkg/k8score; warn so the operator can see the asymmetry.
			if len(granted) > 1 {
				log.Printf("RBAC: typed kinds granted in %d distinct namespaces; CRD informer fallback pinned to %q — CRDs in the others will be marked denied", len(granted), SanitizeForLog(ns))
			}
			return ns
		}
	}
	if len(scopeNamespaces) > 0 {
		return scopeNamespaces[0]
	}
	return ""
}

// probeResourceAccess is the testable inner of CheckResourcePermissions.
// It does the actual probing with the supplied dynamic client and namespaces,
// with no caching and no global state. The returned bool is true when at
// least one probe hit a non-auth (transient) error — caller uses this to
// shorten the cache TTL so the next attempt re-probes.
//
// scopeNamespaces are candidate fallback namespaces; see buildScopeCandidates
// for how production callers populate it. Pass nil/empty to disable fallback.
//
// forceNamespace describes the role of scopeNamespaces:
//   - false: probe cluster-wide first; on 403 walk candidates until one
//     grants list. Per-user view filtering happens at the HTTP layer (see
//     internal/server/namespace_scope.go), so the cache preferentially boots
//     cluster-wide.
//   - true: probe namespaced kinds ONLY in the first scopeNamespaces entry.
//     Used by --namespace-scope to pin the informer cache. Cluster-only kinds
//     (nodes, namespaces, PV, storageclasses, ingressclasses) are still probed
//     cluster-wide since they have no namespace dimension to pin to.
func probeResourceAccess(ctx context.Context, dyn dynamic.Interface, scopeNamespaces []string, forceNamespace bool) (*PermissionCheckResult, bool) {
	perms := &ResourcePermissions{}
	probes := resourceProbeTargets(perms)

	type probeOutcome struct {
		scope      k8score.ResourceScope
		namespaces []string
	}
	outcomes := make([]probeOutcome, len(probes))

	var forcedNs string
	if forceNamespace && len(scopeNamespaces) > 0 {
		forcedNs = scopeNamespaces[0]
	}

	logTiming("   [perms] Probing list access for %d typed resources (scopeNamespaces=%q forced=%v)", len(probes), scopeNamespaces, forceNamespace)
	probeStart := time.Now()
	var wg sync.WaitGroup
	var hadErrors atomic.Bool
	var truncatedMu sync.Mutex
	var truncatedKinds []string
	wg.Add(len(probes))

	for i, p := range probes {
		go func(i int, p resourceProbe) {
			defer wg.Done()
			// A panic inside the dynamic client (codec issues, nil-interface
			// returns from a misbehaving fake in tests, version-skew bugs
			// in client-go) would otherwise crash the whole server. Recover
			// so one bad GVR can't take down capabilities probing.
			defer func() {
				if r := recover(); r != nil {
					log.Printf("[perms] panic probing %s: %v", SanitizeForLog(p.key), r)
					hadErrors.Store(true)
				}
			}()

			if forceNamespace {
				// Cluster-only kinds (nodes, namespaces, PV…) have no namespace
				// dimension — pin them cluster-wide regardless of the picked
				// namespace, so a cluster-admin who scoped to a namespace still
				// sees Node counts, Namespace lists, and node metrics.
				if p.clusterOnly {
					allowed, _, transient := probeKindAccess(ctx, dyn, p, "")
					if transient != nil {
						hadErrors.Store(true)
					}
					if allowed {
						outcomes[i] = probeOutcome{scope: k8score.ResourceScope{Enabled: true, Namespace: ""}}
					}
					return
				}
				if forcedNs == "" {
					return
				}
				nsAllowed, _, nsTransient := probeKindAccess(ctx, dyn, p, forcedNs)
				if nsTransient != nil {
					hadErrors.Store(true)
				}
				if nsAllowed {
					outcomes[i] = probeOutcome{scope: k8score.ResourceScope{Enabled: true, Namespace: forcedNs}}
				}
				return
			}

			allowed, forbidden, transient := probeKindAccess(ctx, dyn, p, "")
			if transient != nil {
				hadErrors.Store(true)
			}
			if allowed {
				outcomes[i] = probeOutcome{scope: k8score.ResourceScope{Enabled: true, Namespace: ""}}
				return
			}
			// Cluster-wide denied. Cluster-scoped kinds have no fallback.
			// Dynamic CRDs that were marked not-installed (all versions
			// returned NotFound) also skip the fallback — the namespace
			// probe would hit the same NotFound for every version.
			if !forbidden || p.clusterOnly || len(scopeNamespaces) == 0 {
				return
			}
			// Try candidate namespaces in priority order. Every grant becomes
			// part of the per-kind informer fanout; Namespace keeps the first
			// grant as the stable primary for legacy diagnostics and dynamic
			// fallback paths.
			grantedNamespaces := make([]string, 0, len(scopeNamespaces))
			for _, ns := range scopeNamespaces {
				if ctx.Err() != nil {
					// Probe budget exhausted mid-fanout. Keep the grants
					// collected so far, but record the cut so the operator
					// can see why later candidates came up missing instead
					// of silently treating them as denied.
					hadErrors.Store(true)
					truncatedMu.Lock()
					truncatedKinds = append(truncatedKinds, p.key)
					truncatedMu.Unlock()
					break
				}
				nsAllowed, _, nsTransient := probeKindAccess(ctx, dyn, p, ns)
				if nsTransient != nil {
					hadErrors.Store(true)
				}
				if nsAllowed {
					grantedNamespaces = append(grantedNamespaces, ns)
				}
			}
			if len(grantedNamespaces) > 0 {
				outcomes[i] = probeOutcome{
					scope:      k8score.ResourceScope{Enabled: true, Namespace: grantedNamespaces[0]},
					namespaces: grantedNamespaces,
				}
			}
		}(i, p)
	}

	wg.Wait()
	logTiming("    Probe phase (%d resources): %v", len(probes), time.Since(probeStart))

	if ctx.Err() != nil {
		// Deadline expired mid-probe. Keep every outcome that completed —
		// returning empty scopes here would silently start Radar with no
		// typed informers at all. hadErrors forces the short error TTL, so
		// a full re-probe happens soon regardless.
		hadErrors.Store(true)
		if len(truncatedKinds) > 0 {
			sort.Strings(truncatedKinds)
			log.Printf("RBAC: probe deadline expired before all candidate namespaces were tried for %d kind(s) (%s); untried namespaces are treated as denied until the next probe", len(truncatedKinds), strings.Join(truncatedKinds, ", "))
		} else {
			logTiming("   [perms] Probe deadline expired; keeping completed outcomes")
		}
	}

	// Apply outcomes to perms (boolean projection) and build the scope map.
	scopes := make(map[string]k8score.ResourceScope, len(probes))
	scopeNamespacesByKind := make(map[string][]string)
	namespaceScoped := false
	var (
		restricted   []string
		nsScopedKeys []string
	)
	for i, p := range probes {
		r := outcomes[i]
		scopes[p.key] = r.scope
		if r.scope.Enabled {
			*p.field = true
			if r.scope.Namespace != "" {
				namespaceScoped = true
				nsScopedKeys = append(nsScopedKeys, p.key)
				if len(r.namespaces) > 1 {
					scopeNamespacesByKind[p.key] = append([]string(nil), r.namespaces...)
				}
			}
		} else {
			restricted = append(restricted, p.key)
		}
	}

	// Strip CR/LF defensively before logging — candidates come from
	// operator-controlled config (kubeconfig context, --namespace flag,
	// accessible-namespaces list); a malicious kubeconfig could otherwise
	// inject lines. CodeQL doesn't model %q escaping, so be explicit.
	scopedDetail := make([]string, 0, len(nsScopedKeys))
	for _, k := range nsScopedKeys {
		if namespaces := scopeNamespacesByKind[k]; len(namespaces) > 1 {
			safeNamespaces := make([]string, 0, len(namespaces))
			for _, ns := range namespaces {
				safeNamespaces = append(safeNamespaces, SanitizeForLog(ns))
			}
			scopedDetail = append(scopedDetail, fmt.Sprintf("%s=%q", k, safeNamespaces))
		} else {
			scopedDetail = append(scopedDetail, fmt.Sprintf("%s=%s", k, SanitizeForLog(scopes[k].Namespace)))
		}
	}
	sort.Strings(scopedDetail)
	candidatesLog := make([]string, 0, len(scopeNamespaces))
	for _, ns := range scopeNamespaces {
		candidatesLog = append(candidatesLog, SanitizeForLog(ns))
	}
	if len(restricted) > 0 {
		sort.Strings(restricted)
		if namespaceScoped {
			log.Printf("RBAC: mixed scope (candidates=%q; ns-scoped: %s); denied: %s",
				candidatesLog, strings.Join(scopedDetail, ", "), strings.Join(restricted, ", "))
		} else {
			log.Printf("RBAC: restricted resources (no list permission): %s", strings.Join(restricted, ", "))
		}
	} else if namespaceScoped {
		log.Printf("RBAC: mixed scope (candidates=%q; ns-scoped: %s); all kinds accessible",
			candidatesLog, strings.Join(scopedDetail, ", "))
	}

	// In forced-namespace mode the user's intent is to be ns-scoped — even
	// if every typed probe failed (e.g. they picked a namespace they have
	// no access to). Force NamespaceScoped=true so the dynamic cache scopes
	// CRD informers to the same namespace and doesn't silently fall through
	// to cluster-wide watches.
	if forceNamespace && forcedNs != "" {
		namespaceScoped = true
	}

	// Namespace is the single-valued anchor consumed by the dynamic CRD
	// cache (internal/k8s/dynamic_cache.go) as NamespaceFallback and by
	// the diagnostics page (internal/server/diagnostics.go). It must be a
	// namespace where the user actually has reads — otherwise CRD informers
	// silently 403. Walk candidates in order and pick the first one that
	// granted at least one typed kind. Fall back to scopeNamespaces[0]
	// when nothing was granted (NamespaceScoped is false in that case, so
	// the dynamic cache ignores this field anyway).
	primaryNs := pickPrimaryNs(scopeNamespaces, scopes)
	return &PermissionCheckResult{
		Perms:           perms,
		NamespaceScoped: namespaceScoped,
		Namespace:       primaryNs,
		Scopes:          scopes,
		ScopeNamespaces: scopeNamespacesByKind,
	}, hadErrors.Load()
}

// GetCachedPermissionResult returns the cached permission check result, if
// available. Returns a deep copy so callers can mutate Perms or Scopes
// without corrupting the cache (mirrors the cache-hit path in
// CheckResourcePermissions).
func GetCachedPermissionResult() *PermissionCheckResult {
	resourcePermsMu.RLock()
	defer resourcePermsMu.RUnlock()
	if cachedPermResult == nil {
		return nil
	}
	permsCopy := *cachedPermResult.Perms
	scopesCopy := make(map[string]k8score.ResourceScope, len(cachedPermResult.Scopes))
	for k, v := range cachedPermResult.Scopes {
		scopesCopy[k] = v
	}
	scopeNamespacesCopy := make(map[string][]string, len(cachedPermResult.ScopeNamespaces))
	for k, v := range cachedPermResult.ScopeNamespaces {
		scopeNamespacesCopy[k] = append([]string(nil), v...)
	}
	return &PermissionCheckResult{
		Perms:           &permsCopy,
		NamespaceScoped: cachedPermResult.NamespaceScoped,
		Namespace:       cachedPermResult.Namespace,
		Scopes:          scopesCopy,
		ScopeNamespaces: scopeNamespacesCopy,
	}
}

// InvalidateResourcePermissionsCache forces the next CheckResourcePermissions call to refresh
func InvalidateResourcePermissionsCache() {
	resourcePermsMu.Lock()
	defer resourcePermsMu.Unlock()
	cachedPermResult = nil
}
