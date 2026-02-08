package k8s

import (
	"context"
	"log"
	"sync"
	"time"

	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Capabilities represents the features available based on RBAC permissions
type Capabilities struct {
	Exec        bool `json:"exec"`        // Can create pods/exec (terminal feature)
	Logs        bool `json:"logs"`        // Can get pods/log (log viewer)
	PortForward bool `json:"portForward"` // Can create pods/portforward
	Secrets     bool `json:"secrets"`     // Can list secrets
	HelmWrite   bool `json:"helmWrite"`   // Helm write ops (detected via secrets/create as sentinel RBAC check)
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

	// Check each capability in parallel using local variables to avoid data race
	var wg sync.WaitGroup
	var execAllowed, logsAllowed, portForwardAllowed, secretsAllowed, helmWriteAllowed bool

	wg.Add(5)

	go func() {
		defer wg.Done()
		execAllowed = canI(checkCtx, "", "pods/exec", "create")
	}()

	go func() {
		defer wg.Done()
		logsAllowed = canI(checkCtx, "", "pods/log", "get")
	}()

	go func() {
		defer wg.Done()
		portForwardAllowed = canI(checkCtx, "", "pods/portforward", "create")
	}()

	go func() {
		defer wg.Done()
		secretsAllowed = canI(checkCtx, "", "secrets", "list")
	}()

	go func() {
		defer wg.Done()
		helmWriteAllowed = canI(checkCtx, "", "secrets", "create")
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

// canI checks if the current user/service account can perform an action
func canI(ctx context.Context, namespace, resource, verb string) bool {
	k8sClient := GetClient()
	if k8sClient == nil {
		log.Printf("Warning: K8s client nil in canI check for %s %s", verb, resource)
		return false // Fail closed if no client
	}

	review := &authv1.SelfSubjectAccessReview{
		Spec: authv1.SelfSubjectAccessReviewSpec{
			ResourceAttributes: &authv1.ResourceAttributes{
				Namespace: namespace, // Empty = cluster-wide
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
