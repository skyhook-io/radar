package k8s

import (
	"context"
	"log"
	"sync"
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
	Resources   *ResourcePermissions `json:"resources,omitempty"` // Per-resource-type permissions
}

var (
	cachedCapabilities *Capabilities
	capabilitiesMu     sync.RWMutex
	capabilitiesExpiry time.Time
	capabilitiesTTL    = 60 * time.Second

	// ForceDisableHelmWrite overrides the helmWrite capability to false (for dev testing)
	ForceDisableHelmWrite bool
)

// CheckCapabilities checks RBAC permissions using SelfSubjectAccessReview
// Results are cached for 60 seconds to avoid hammering the API
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

	// Check each capability in parallel using local variables to avoid data race.
	// Try cluster-wide first, then namespace-scoped as fallback for namespace-scoped users.
	fallbackNs := GetEffectiveNamespace()
	var wg sync.WaitGroup
	var execAllowed, logsAllowed, portForwardAllowed, secretsAllowed, helmWriteAllowed bool

	wg.Add(5)

	go func() {
		defer wg.Done()
		execAllowed = canI(checkCtx, "", "", "pods/exec", "create")
		if !execAllowed && fallbackNs != "" {
			execAllowed = canI(checkCtx, fallbackNs, "", "pods/exec", "create")
		}
	}()

	go func() {
		defer wg.Done()
		logsAllowed = canI(checkCtx, "", "", "pods/log", "get")
		if !logsAllowed && fallbackNs != "" {
			logsAllowed = canI(checkCtx, fallbackNs, "", "pods/log", "get")
		}
	}()

	go func() {
		defer wg.Done()
		portForwardAllowed = canI(checkCtx, "", "", "pods/portforward", "create")
		if !portForwardAllowed && fallbackNs != "" {
			portForwardAllowed = canI(checkCtx, fallbackNs, "", "pods/portforward", "create")
		}
	}()

	go func() {
		defer wg.Done()
		secretsAllowed = canI(checkCtx, "", "", "secrets", "list")
		if !secretsAllowed && fallbackNs != "" {
			secretsAllowed = canI(checkCtx, fallbackNs, "", "secrets", "list")
		}
	}()

	go func() {
		defer wg.Done()
		helmWriteAllowed = canI(checkCtx, "", "", "secrets", "create")
		if !helmWriteAllowed && fallbackNs != "" {
			helmWriteAllowed = canI(checkCtx, fallbackNs, "", "secrets", "create")
		}
	}()

	wg.Wait()

	// Build capabilities struct after all goroutines complete
	caps := &Capabilities{
		Exec:        execAllowed,
		Logs:        logsAllowed,
		PortForward: portForwardAllowed,
		Secrets:     secretsAllowed,
		HelmWrite:   helmWriteAllowed,
	}

	if ForceDisableHelmWrite {
		caps.HelmWrite = false
	}

	// Cache the result
	cachedCapabilities = caps
	capabilitiesExpiry = time.Now().Add(capabilitiesTTL)

	return caps, nil
}

// canI checks if the current user/service account can perform an action.
// The group parameter specifies the API group (empty string for core API resources).
func canI(ctx context.Context, namespace, group, resource, verb string) bool {
	k8sClient := GetClient()
	if k8sClient == nil {
		log.Printf("Warning: K8s client nil in canI check for %s %s", verb, resource)
		return false // Fail closed if no client
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
		// Log the error and fail closed
		log.Printf("Warning: SelfSubjectAccessReview failed for %s %s: %v", verb, resource, err)
		return false
	}

	return result.Status.Allowed
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
	resourcePermsTTL    = 60 * time.Second
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
	wg.Add(len(checks))

	for _, check := range checks {
		go func(c permCheck) {
			defer wg.Done()
			*c.result = canI(ctx, "", c.group, c.resource, "list")
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
					*c.result = canI(ctx, fallbackNs, c.group, c.resource, "list")
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
	resourcePermsExpiry = time.Now().Add(resourcePermsTTL)

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
