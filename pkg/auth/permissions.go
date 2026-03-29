package auth

import (
	"context"
	"log"
	"sync"
	"time"

	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// UserPermissions holds cached permission data for an authenticated user
type UserPermissions struct {
	AllowedNamespaces []string  // nil = all namespaces (cluster admin)
	ExpiresAt         time.Time
}

// PermissionCache caches per-user permission lookups (thread-safe)
type PermissionCache struct {
	mu    sync.RWMutex
	cache map[string]*UserPermissions // keyed by username
	ttl   time.Duration
}

// NewPermissionCache creates a new permission cache with a 2-minute TTL
func NewPermissionCache() *PermissionCache {
	return &PermissionCache{
		cache: make(map[string]*UserPermissions),
		ttl:   2 * time.Minute,
	}
}

// Get returns cached permissions for a user, or nil if not cached/expired
func (pc *PermissionCache) Get(username string) *UserPermissions {
	pc.mu.RLock()
	defer pc.mu.RUnlock()

	perms, ok := pc.cache[username]
	if !ok || time.Now().After(perms.ExpiresAt) {
		return nil
	}
	return perms
}

// Set stores permissions for a user
func (pc *PermissionCache) Set(username string, perms *UserPermissions) {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	perms.ExpiresAt = time.Now().Add(pc.ttl)
	pc.cache[username] = perms
}

// Invalidate removes all cached permissions (e.g., on context switch)
func (pc *PermissionCache) Invalidate() {
	pc.mu.Lock()
	defer pc.mu.Unlock()
	pc.cache = make(map[string]*UserPermissions)
}

// DiscoverNamespaces checks which namespaces a user can access via SubjectAccessReview.
// Returns nil for "all namespaces" (cluster admin), empty slice for "no access".
func DiscoverNamespaces(ctx context.Context, client kubernetes.Interface, username string, groups []string, allNamespaces []string) ([]string, error) {
	// Step 1: Check cluster-wide "list pods" — if allowed, user is effectively cluster-admin
	allowed, err := SubjectCanI(ctx, client, username, groups, "", "", "pods", "list")
	if err != nil {
		log.Printf("[auth] DiscoverNamespaces: cluster-scope SAR failed for %s: %v", username, err)
		return nil, err
	}
	log.Printf("[auth] DiscoverNamespaces: user=%s cluster-scope list pods = %v", username, allowed)
	if allowed {
		return nil, nil // nil = all namespaces
	}

	// Step 2: Parallel check each namespace for "list pods" OR "list deployments"
	type nsResult struct {
		namespace string
		allowed   bool
	}

	results := make(chan nsResult, len(allNamespaces))
	sem := make(chan struct{}, 10) // limit to 10 concurrent checks

	var wg sync.WaitGroup
	for _, ns := range allNamespaces {
		wg.Add(1)
		go func(namespace string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			// Check "list pods" in this namespace
			ok, _ := SubjectCanI(ctx, client, username, groups, namespace, "", "pods", "list")
			if ok {
				results <- nsResult{namespace: namespace, allowed: true}
				return
			}
			// Fallback: check "list deployments"
			ok, _ = SubjectCanI(ctx, client, username, groups, namespace, "apps", "deployments", "list")
			if ok {
				results <- nsResult{namespace: namespace, allowed: true}
				return
			}
			results <- nsResult{namespace: namespace, allowed: false}
		}(ns)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	allowedNS := make([]string, 0) // empty (not nil) = no access; nil = all namespaces
	for r := range results {
		if r.allowed {
			allowedNS = append(allowedNS, r.namespace)
		}
	}

	return allowedNS, nil
}

// SubjectCanI performs a SubjectAccessReview (not SelfSubject) to check if a specific
// user can perform an action. This uses the ServiceAccount's permissions to check
// on behalf of the user.
func SubjectCanI(ctx context.Context, client kubernetes.Interface, username string, groups []string, namespace, group, resource, verb string) (bool, error) {
	review := &authv1.SubjectAccessReview{
		Spec: authv1.SubjectAccessReviewSpec{
			User:   username,
			Groups: groups,
			ResourceAttributes: &authv1.ResourceAttributes{
				Namespace: namespace,
				Group:     group,
				Resource:  resource,
				Verb:      verb,
			},
		},
	}

	result, err := client.AuthorizationV1().SubjectAccessReviews().Create(ctx, review, metav1.CreateOptions{})
	if err != nil {
		log.Printf("[auth] SubjectAccessReview failed for user=%s %s %s/%s: %v", username, verb, group, resource, err)
		return false, err
	}

	return result.Status.Allowed, nil
}

// FilterNamespacesForUser intersects requested namespaces with user's allowed namespaces.
// If user has no restrictions (AllowedNamespaces == nil), returns requested namespaces unchanged.
// If requested is empty (all namespaces), returns user's allowed namespaces.
func FilterNamespacesForUser(requested []string, user *User, perms *UserPermissions) []string {
	if user == nil || perms == nil {
		return requested
	}
	if perms.AllowedNamespaces == nil {
		return requested // User has access to all namespaces
	}

	allowedSet := make(map[string]bool, len(perms.AllowedNamespaces))
	for _, ns := range perms.AllowedNamespaces {
		allowedSet[ns] = true
	}

	if len(requested) == 0 {
		// No filter requested — return all allowed namespaces
		return perms.AllowedNamespaces
	}

	// Intersect: empty result (not nil) means no access
	result := make([]string, 0)
	for _, ns := range requested {
		if allowedSet[ns] {
			result = append(result, ns)
		}
	}
	return result
}
