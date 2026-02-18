package k8s

import (
	"context"
	"log"
	"sync"
	"sync/atomic"
	"time"

	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// clusterScopedResources are K8s resources that exist at cluster scope (not namespaced).
// These cannot be checked with a namespace-scoped SelfSubjectAccessReview.
var clusterScopedResources = map[string]bool{
	"nodes":      true,
	"namespaces": true,
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
	Exec        bool                 `json:"exec"`                // Can create pods/exec (terminal feature)
	Logs        bool                 `json:"logs"`                // Can get pods/log (log viewer)
	PortForward bool                 `json:"portForward"`         // Can create pods/portforward
	Secrets     bool                 `json:"secrets"`             // Can list secrets
	HelmWrite   bool                 `json:"helmWrite"`           // Helm write ops (detected via secrets/create as sentinel RBAC check)
	MCPEnabled  bool                 `json:"mcpEnabled"`          // MCP server is running
	Resources   *ResourcePermissions `json:"resources,omitempty"` // Per-resource-type permissions
}

var (
	cachedCapabilities *Capabilities
	capabilitiesMu     sync.RWMutex
	capabilitiesExpiry time.Time
	capabilitiesTTL      = 60 * time.Second
	capabilitiesErrorTTL = 5 * time.Second // Short TTL when API errors caused fail-closed results

	// ForceDisableHelmWrite overrides the helmWrite capability to false (for dev testing)
	ForceDisableHelmWrite bool
)

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

	// Need to refresh capabilities
	capabilitiesMu.Lock()
	defer capabilitiesMu.Unlock()

	// Double-check after acquiring write lock
	if cachedCapabilities != nil && time.Now().Before(capabilitiesExpiry) {
		caps := *cachedCapabilities
		return &caps, nil
	}

	if GetClient() == nil {
		// Return all false if client not initialized (fail closed)
		log.Printf("Warning: K8s client not initialized, returning restricted capabilities")
		return &Capabilities{Exec: false, Logs: false, PortForward: false, Secrets: false, HelmWrite: false}, nil
	}

	// Use a background context so that HTTP request cancellation doesn't cause
	// transient failures to be cached as "denied" for the full TTL.
	checkCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

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
		{"secrets", "create", &caps.HelmWrite},
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

	if ForceDisableHelmWrite {
		caps.HelmWrite = false
	}

	// Cache the result. Use a short TTL if API errors caused fail-closed results,
	// so transient K8s API failures don't hide UI controls for a full minute.
	ttl := capabilitiesTTL
	if hadErrors.Load() {
		ttl = capabilitiesErrorTTL
		log.Printf("Warning: capability checks had API errors, using short cache TTL (%v)", ttl)
	}
	cachedCapabilities = caps
	capabilitiesExpiry = time.Now().Add(ttl)

	return caps, nil
}

// canI checks if the current user/service account can perform an action.
// The group parameter specifies the API group (empty string for core resources like pods, secrets).
// Returns (allowed, apiErr) where apiErr=true means the API call itself failed
// (distinct from RBAC denial where allowed=false, apiErr=false).
func canI(ctx context.Context, namespace, group, resource, verb string) (allowed bool, apiErr bool) {
	k8sClient := GetClient()
	if k8sClient == nil {
		log.Printf("Warning: K8s client nil in canI check for %s %s", verb, resource)
		return false, true
	}

	review := &authv1.SelfSubjectAccessReview{
		Spec: authv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authv1.ResourceAttributes{
				Namespace: namespace, // Empty = cluster-wide
				Group:     group,     // API group (empty = core)
				Verb:      verb,
				Resource:  resource,
			},
		},
	}

	result, err := k8sClient.AuthorizationV1().SelfSubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		log.Printf("Warning: SelfSubjectAccessReview failed for %s %s: %v", verb, resource, err)
		return false, true
	}

	return result.Status.Allowed, false
}

// InvalidateCapabilitiesCache forces the next CheckCapabilities call to refresh
func InvalidateCapabilitiesCache() {
	capabilitiesMu.Lock()
	defer capabilitiesMu.Unlock()
	cachedCapabilities = nil
}

var (
	cachedPermResult    *PermissionCheckResult
	resourcePermsMu     sync.RWMutex
	resourcePermsExpiry time.Time
	resourcePermsTTL         = 60 * time.Second
	resourcePermsErrorTTL    = 5 * time.Second // Short TTL when API errors caused fail-closed results
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

	resourcePermsMu.Lock()
	defer resourcePermsMu.Unlock()

	// Double-check after acquiring write lock
	if cachedPermResult != nil && time.Now().Before(resourcePermsExpiry) {
		permsCopy := *cachedPermResult.Perms
		return &PermissionCheckResult{
			Perms:           &permsCopy,
			NamespaceScoped: cachedPermResult.NamespaceScoped,
			Namespace:       cachedPermResult.Namespace,
		}
	}

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
	}

	// Phase 1: Check all resources cluster-wide
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

	cachedPermResult = result
	ttl := resourcePermsTTL
	if hadErrors.Load() {
		ttl = resourcePermsErrorTTL
		log.Printf("Warning: resource permission checks had API errors, using short cache TTL (%v)", ttl)
	}
	resourcePermsExpiry = time.Now().Add(ttl)

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

// --- State Cache Integration ---

// ExportRBACResults exports the current RBAC permission check results for caching.
// Returns nil if no cached results are available.
func ExportRBACResults() []CachedRBACResult {
	resourcePermsMu.RLock()
	defer resourcePermsMu.RUnlock()

	if cachedPermResult == nil || cachedPermResult.Perms == nil {
		return nil
	}

	perms := cachedPermResult.Perms
	ns := cachedPermResult.Namespace
	nsScoped := cachedPermResult.NamespaceScoped

	results := []CachedRBACResult{
		{Resource: "pods", Group: "", Allowed: perms.Pods, NamespaceScoped: nsScoped, Namespace: ns},
		{Resource: "services", Group: "", Allowed: perms.Services, NamespaceScoped: nsScoped, Namespace: ns},
		{Resource: "configmaps", Group: "", Allowed: perms.ConfigMaps, NamespaceScoped: nsScoped, Namespace: ns},
		{Resource: "secrets", Group: "", Allowed: perms.Secrets, NamespaceScoped: nsScoped, Namespace: ns},
		{Resource: "events", Group: "", Allowed: perms.Events, NamespaceScoped: nsScoped, Namespace: ns},
		{Resource: "persistentvolumeclaims", Group: "", Allowed: perms.PersistentVolumeClaims, NamespaceScoped: nsScoped, Namespace: ns},
		{Resource: "nodes", Group: "", Allowed: perms.Nodes, NamespaceScoped: nsScoped, Namespace: ns},
		{Resource: "namespaces", Group: "", Allowed: perms.Namespaces, NamespaceScoped: nsScoped, Namespace: ns},
		{Resource: "deployments", Group: "apps", Allowed: perms.Deployments, NamespaceScoped: nsScoped, Namespace: ns},
		{Resource: "daemonsets", Group: "apps", Allowed: perms.DaemonSets, NamespaceScoped: nsScoped, Namespace: ns},
		{Resource: "statefulsets", Group: "apps", Allowed: perms.StatefulSets, NamespaceScoped: nsScoped, Namespace: ns},
		{Resource: "replicasets", Group: "apps", Allowed: perms.ReplicaSets, NamespaceScoped: nsScoped, Namespace: ns},
		{Resource: "ingresses", Group: "networking.k8s.io", Allowed: perms.Ingresses, NamespaceScoped: nsScoped, Namespace: ns},
		{Resource: "gateways", Group: "gateway.networking.k8s.io", Allowed: perms.Gateways, NamespaceScoped: nsScoped, Namespace: ns},
		{Resource: "httproutes", Group: "gateway.networking.k8s.io", Allowed: perms.HTTPRoutes, NamespaceScoped: nsScoped, Namespace: ns},
		{Resource: "jobs", Group: "batch", Allowed: perms.Jobs, NamespaceScoped: nsScoped, Namespace: ns},
		{Resource: "cronjobs", Group: "batch", Allowed: perms.CronJobs, NamespaceScoped: nsScoped, Namespace: ns},
		{Resource: "horizontalpodautoscalers", Group: "autoscaling", Allowed: perms.HorizontalPodAutoscalers, NamespaceScoped: nsScoped, Namespace: ns},
	}

	return results
}

// ImportRBACResults loads cached RBAC results, populating the cached permission result.
// Returns the reconstructed PermissionCheckResult. The in-memory RBAC cache is also
// populated so that InitResourceCache and InitDynamicResourceCache can use it.
func ImportRBACResults(results []CachedRBACResult) *PermissionCheckResult {
	if len(results) == 0 {
		return nil
	}

	perms := &ResourcePermissions{}
	nsScoped := false
	namespace := ""

	for _, r := range results {
		// Pick up namespace info from any entry
		if r.NamespaceScoped {
			nsScoped = true
			namespace = r.Namespace
		}

		switch r.Group + "/" + r.Resource {
		case "/pods":
			perms.Pods = r.Allowed
		case "/services":
			perms.Services = r.Allowed
		case "/configmaps":
			perms.ConfigMaps = r.Allowed
		case "/secrets":
			perms.Secrets = r.Allowed
		case "/events":
			perms.Events = r.Allowed
		case "/persistentvolumeclaims":
			perms.PersistentVolumeClaims = r.Allowed
		case "/nodes":
			perms.Nodes = r.Allowed
		case "/namespaces":
			perms.Namespaces = r.Allowed
		case "apps/deployments":
			perms.Deployments = r.Allowed
		case "apps/daemonsets":
			perms.DaemonSets = r.Allowed
		case "apps/statefulsets":
			perms.StatefulSets = r.Allowed
		case "apps/replicasets":
			perms.ReplicaSets = r.Allowed
		case "networking.k8s.io/ingresses":
			perms.Ingresses = r.Allowed
		case "gateway.networking.k8s.io/gateways":
			perms.Gateways = r.Allowed
		case "gateway.networking.k8s.io/httproutes":
			perms.HTTPRoutes = r.Allowed
		case "batch/jobs":
			perms.Jobs = r.Allowed
		case "batch/cronjobs":
			perms.CronJobs = r.Allowed
		case "autoscaling/horizontalpodautoscalers":
			perms.HorizontalPodAutoscalers = r.Allowed
		}
	}

	result := &PermissionCheckResult{
		Perms:           perms,
		NamespaceScoped: nsScoped,
		Namespace:       namespace,
	}

	// Populate the in-memory cache so GetCachedPermissionResult() works
	resourcePermsMu.Lock()
	cachedPermResult = result
	resourcePermsExpiry = time.Now().Add(resourcePermsTTL)
	resourcePermsMu.Unlock()

	log.Printf("Loaded RBAC permissions from cache (namespace_scoped=%v, namespace=%q)", nsScoped, namespace)
	return result
}
