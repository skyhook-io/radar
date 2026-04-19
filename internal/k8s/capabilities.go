package k8s

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"

	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/skyhook-io/radar/pkg/k8score"
)

// clusterScopedResources are K8s resources that exist at cluster scope (not namespaced).
// These cannot be checked with a namespace-scoped SelfSubjectAccessReview.
var clusterScopedResources = map[string]bool{
	"nodes":             true,
	"namespaces":        true,
	"persistentvolumes": true,
	"storageclasses":    true,
}

// ResourcePermissions indicates which resource types the user can list/watch
type ResourcePermissions struct {
	Pods                     bool `json:"pods"`
	Services                 bool `json:"services"`
	Deployments              bool `json:"deployments"`
	DaemonSets               bool `json:"daemonSets"`
	StatefulSets             bool `json:"statefulSets"`
	ReplicaSets              bool `json:"replicaSets"`
	Ingresses                bool `json:"ingresses"`
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
	LimitRanges              bool `json:"limitRanges"`
	Gateways                 bool `json:"gateways"`
	HTTPRoutes               bool `json:"httpRoutes"`
}

// PermissionCheckResult holds the result of RBAC permission checks
type PermissionCheckResult struct {
	Perms           *ResourcePermissions
	NamespaceScoped bool   // True if permissions are namespace-scoped (not cluster-wide)
	Namespace       string // The namespace checked, when namespace-scoped
}

// Capabilities represents the features available based on RBAC permissions
type Capabilities struct {
	Exec          bool                 `json:"exec"`                    // Can create pods/exec (terminal feature)
	LocalTerminal bool                 `json:"localTerminal"`           // Local terminal available (not in-cluster, not disabled)
	Logs          bool                 `json:"logs"`                    // Can get pods/log (log viewer)
	PortForward   bool                 `json:"portForward"`             // Can create pods/portforward
	Secrets       bool                 `json:"secrets"`                 // Can list secrets
	SecretsUpdate bool                 `json:"secretsUpdate"`           // Can update secrets (inline editing)
	HelmWrite     bool                 `json:"helmWrite"`               // Helm write ops (detected via secrets/create as sentinel RBAC check)
	NodeWrite     bool                 `json:"nodeWrite"`               // Can patch nodes (cordon/uncordon/drain)
	MCPEnabled    bool                 `json:"mcpEnabled"`              // MCP server is running
	AuthEnabled   bool                 `json:"authEnabled,omitempty"`   // Auth is enabled on the server
	Username      string               `json:"username,omitempty"`      // Authenticated username (when auth enabled)
	Resources     *ResourcePermissions `json:"resources,omitempty"`     // Per-resource-type permissions
}

// NamespaceCapabilities holds the effective exec/logs/portForward capabilities
// for a specific namespace. When global checks deny these capabilities,
// namespace-scoped RBAC re-checks may grant them.
type NamespaceCapabilities struct {
	Exec        bool `json:"exec"`
	Logs        bool `json:"logs"`
	PortForward bool `json:"portForward"`
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
	nsCapCache   map[string]*nsCapEntry
	nsCapMu      sync.RWMutex

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
		resource string
		verb     string
		result   *bool
	}

	caps := &Capabilities{}
	checks := []capCheck{
		{"pods/exec", "create", &caps.Exec},
		{"pods/log", "get", &caps.Logs},
		{"pods/portforward", "create", &caps.PortForward},
		{"secrets", "list", &caps.Secrets},
		{"secrets", "update", &caps.SecretsUpdate},
		{"secrets", "create", &caps.HelmWrite},
		{"nodes", "patch", &caps.NodeWrite},
	}

	var wg sync.WaitGroup
	wg.Add(len(checks))

	for _, check := range checks {
		go func(c capCheck) {
			defer wg.Done()
			allowed, apiErr := canI(checkCtx, "", "", c.resource, c.verb)
			if allowed {
				*c.result = true
				return
			}
			if fallbackNs != "" {
				allowed, nsApiErr := canI(checkCtx, fallbackNs, "", c.resource, c.verb)
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

// CheckNamespaceCapabilities performs namespace-scoped RBAC checks for capabilities
// that were denied by global checks (cluster-wide + effective-namespace fallback).
// This enables lazy re-checking when a user views a resource in a specific namespace —
// they may have namespace-scoped RoleBindings that grant exec/logs/portForward in
// namespaces other than the kubeconfig default.
//
// Returns nil if no namespace-scoped re-check is needed (all capabilities already allowed).
func CheckNamespaceCapabilities(ctx context.Context, namespace string, globalCaps *Capabilities) (*NamespaceCapabilities, error) {
	if namespace == "" {
		return nil, nil
	}

	// If all three are already allowed globally, no need for namespace check
	if globalCaps.Exec && globalCaps.Logs && globalCaps.PortForward {
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

	result := &NamespaceCapabilities{
		Exec:        globalCaps.Exec,
		Logs:        globalCaps.Logs,
		PortForward: globalCaps.PortForward,
	}

	// Only re-check capabilities that were denied globally
	type capCheck struct {
		resource string
		verb     string
		result   *bool
	}

	var checks []capCheck
	if !globalCaps.Exec && !ForceDisableExec {
		checks = append(checks, capCheck{"pods/exec", "create", &result.Exec})
	}
	if !globalCaps.Logs {
		checks = append(checks, capCheck{"pods/log", "get", &result.Logs})
	}
	if !globalCaps.PortForward {
		checks = append(checks, capCheck{"pods/portforward", "create", &result.PortForward})
	}

	if len(checks) == 0 {
		return result, nil
	}

	var hadErrors atomic.Bool
	var wg sync.WaitGroup
	wg.Add(len(checks))
	for _, check := range checks {
		go func(c capCheck) {
			defer wg.Done()
			allowed, apiErr := canI(checkCtx, namespace, "", c.resource, c.verb)
			if allowed {
				*c.result = true
			}
			if apiErr {
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
	userCapabilitiesCache   sync.Map // map[string]*userCapEntry
	userCapabilitiesTTL     = 60 * time.Second
)

type userCapEntry struct {
	caps      *Capabilities
	expiresAt time.Time
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
		resource string
		verb     string
		result   *bool
	}

	caps := &Capabilities{}
	checks := []capCheck{
		{"pods/exec", "create", &caps.Exec},
		{"pods/log", "get", &caps.Logs},
		{"pods/portforward", "create", &caps.PortForward},
		{"secrets", "list", &caps.Secrets},
		{"secrets", "update", &caps.SecretsUpdate},
		{"secrets", "create", &caps.HelmWrite},
		{"nodes", "patch", &caps.NodeWrite},
	}

	var wg sync.WaitGroup
	wg.Add(len(checks))

	for _, check := range checks {
		go func(c capCheck) {
			defer wg.Done()
			allowed, _ := canIAs(checkCtx, k8sClient, username, groups, "", "", c.resource, c.verb)
			if allowed {
				*c.result = true
				return
			}
			// Try namespace-scoped fallback
			if fallbackNs := GetEffectiveNamespace(); fallbackNs != "" {
				allowed, _ = canIAs(checkCtx, k8sClient, username, groups, fallbackNs, "", c.resource, c.verb)
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
			log.Printf("Warning: SubjectAccessReview failed for user=%s %s %s: %v", username, verb, resource, err)
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
}

var (
	cachedPermResult      *PermissionCheckResult
	resourcePermsMu       sync.RWMutex
	resourcePermsExpiry   time.Time
	resourcePermsTTL      = 60 * time.Second
	resourcePermsErrorTTL = 5 * time.Second // Short TTL when API errors caused fail-closed results
)

// CheckResourcePermissions checks RBAC permissions for all resource types using
// SelfSubjectAccessReview. Results are cached for 60 seconds.
// This is used at informer startup to decide which informers to create.
//
// For namespace-scoped users (e.g., ServiceAccounts with RoleBindings), cluster-wide
// checks will fail. When a fallback namespace is available (from kubeconfig context
// or --namespace flag), namespace-scoped checks are tried as a second pass.
func CheckResourcePermissions(ctx context.Context) *PermissionCheckResult {
	resourcePermsMu.RLock()
	if cachedPermResult != nil && time.Now().Before(resourcePermsExpiry) {
		permsCopy := *cachedPermResult.Perms
		result := &PermissionCheckResult{
			Perms:           &permsCopy,
			NamespaceScoped: cachedPermResult.NamespaceScoped,
			Namespace:       cachedPermResult.Namespace,
		}
		resourcePermsMu.RUnlock()
		return result
	}
	resourcePermsMu.RUnlock()

	// Compute RBAC permissions WITHOUT holding the write lock.
	// Multiple concurrent callers may race, but redundant checks are harmless.
	// Critical: holding the lock during network calls blocks
	// InvalidateResourcePermissionsCache() during context switch.

	if GetClient() == nil {
		log.Printf("Warning: K8s client not initialized, returning no resource permissions")
		return &PermissionCheckResult{Perms: &ResourcePermissions{}}
	}

	type permCheck struct {
		group    string // API group ("" for core, "apps", "batch", etc.)
		resource string
		result   *bool
	}

	perms := &ResourcePermissions{}
	checks := []permCheck{
		// Core API group
		{"", "pods", &perms.Pods},
		{"", "services", &perms.Services},
		{"", "configmaps", &perms.ConfigMaps},
		{"", "secrets", &perms.Secrets},
		{"", "events", &perms.Events},
		{"", "persistentvolumeclaims", &perms.PersistentVolumeClaims},
		{"", "nodes", &perms.Nodes},
		{"", "namespaces", &perms.Namespaces},
		// apps group
		{"apps", "deployments", &perms.Deployments},
		{"apps", "daemonsets", &perms.DaemonSets},
		{"apps", "statefulsets", &perms.StatefulSets},
		{"apps", "replicasets", &perms.ReplicaSets},
		// networking.k8s.io group
		{"networking.k8s.io", "ingresses", &perms.Ingresses},
		// gateway.networking.k8s.io group
		{"gateway.networking.k8s.io", "gateways", &perms.Gateways},
		{"gateway.networking.k8s.io", "httproutes", &perms.HTTPRoutes},
		// batch group
		{"batch", "jobs", &perms.Jobs},
		{"batch", "cronjobs", &perms.CronJobs},
		// autoscaling group
		{"autoscaling", "horizontalpodautoscalers", &perms.HorizontalPodAutoscalers},
		// core group (cluster-scoped)
		{"", "persistentvolumes", &perms.PersistentVolumes},
		// storage.k8s.io group
		{"storage.k8s.io", "storageclasses", &perms.StorageClasses},
		// policy group
		{"policy", "poddisruptionbudgets", &perms.PodDisruptionBudgets},
		// networking.k8s.io group
		{"networking.k8s.io", "networkpolicies", &perms.NetworkPolicies},
		// core (namespaced) — inheritance lookups for audit checks
		{"", "serviceaccounts", &perms.ServiceAccounts},
		{"", "limitranges", &perms.LimitRanges},
	}

	// Phase 1: Check all resources cluster-wide
	logTiming("   [perms] Phase 1 starting: %d cluster-wide RBAC checks", len(checks))
	phase1Start := time.Now()
	var wg sync.WaitGroup
	var hadErrors atomic.Bool
	wg.Add(len(checks))

	for _, check := range checks {
		go func(c permCheck) {
			defer wg.Done()
			allowed, apiErr := canI(ctx, "", c.group, c.resource, "list")
			*c.result = allowed
			if apiErr {
				hadErrors.Store(true)
			}
		}(check)
	}

	wg.Wait()
	logTiming("    RBAC phase 1 (cluster-wide, %d checks): %v", len(checks), time.Since(phase1Start))

	// Bail early if context was canceled (e.g., version check failed while
	// RBAC checks were in-flight). No point starting Phase 2.
	if ctx.Err() != nil {
		logTiming("   [perms] Bailing after Phase 1: context canceled")
		result := &PermissionCheckResult{Perms: perms}
		resourcePermsMu.Lock()
		cachedPermResult = result
		resourcePermsExpiry = time.Now().Add(resourcePermsErrorTTL)
		resourcePermsMu.Unlock()
		return result
	}

	// Phase 2: If all namespace-scoped resources failed and we have a fallback namespace,
	// retry those checks scoped to the specific namespace.
	fallbackNs := GetEffectiveNamespace()
	namespaceScoped := false

	if fallbackNs != "" {
		allNamespacedFailed := true
		for _, check := range checks {
			if !clusterScopedResources[check.resource] && *check.result {
				allNamespacedFailed = false
				break
			}
		}

		if allNamespacedFailed {
			log.Printf("RBAC: cluster-wide checks failed for all namespaced resources, retrying in namespace %q", fallbackNs)

			var nsChecks []permCheck
			for i := range checks {
				if !clusterScopedResources[checks[i].resource] {
					nsChecks = append(nsChecks, checks[i])
				}
			}

			wg.Add(len(nsChecks))
			for _, check := range nsChecks {
				go func(c permCheck) {
					defer wg.Done()
					allowed, apiErr := canI(ctx, fallbackNs, c.group, c.resource, "list")
					*c.result = allowed
					if apiErr {
						hadErrors.Store(true)
					}
				}(check)
			}
			wg.Wait()

			// If any namespace-scoped check passed, we're in namespace-scoped mode
			for _, check := range nsChecks {
				if *check.result {
					namespaceScoped = true
					break
				}
			}
		}
	}

	// Log which resources are restricted
	var restricted []string
	for _, check := range checks {
		if !*check.result {
			restricted = append(restricted, check.resource)
		}
	}
	if len(restricted) > 0 {
		if namespaceScoped {
			log.Printf("RBAC: namespace-scoped mode (namespace=%s), restricted resources: %v", fallbackNs, restricted)
		} else {
			log.Printf("RBAC: restricted resources (no list permission): %v", restricted)
		}
	}

	result := &PermissionCheckResult{
		Perms:           perms,
		NamespaceScoped: namespaceScoped,
		Namespace:       fallbackNs,
	}

	resourcePermsMu.Lock()
	cachedPermResult = result
	ttl := resourcePermsTTL
	if hadErrors.Load() {
		ttl = resourcePermsErrorTTL
		log.Printf("Warning: resource permission checks had API errors, using short cache TTL (%v)", ttl)
	}
	resourcePermsExpiry = time.Now().Add(ttl)
	resourcePermsMu.Unlock()

	return result
}

// GetCachedPermissionResult returns the cached permission check result, if available.
// Used by dynamic cache to determine namespace scoping without re-running checks.
func GetCachedPermissionResult() *PermissionCheckResult {
	resourcePermsMu.RLock()
	defer resourcePermsMu.RUnlock()
	if cachedPermResult == nil {
		return nil
	}
	result := *cachedPermResult
	return &result
}

// InvalidateResourcePermissionsCache forces the next CheckResourcePermissions call to refresh
func InvalidateResourcePermissionsCache() {
	resourcePermsMu.Lock()
	defer resourcePermsMu.Unlock()
	cachedPermResult = nil
}
