package k8s

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// APIResource represents a discovered API resource type
type APIResource struct {
	Group      string   `json:"group"`
	Version    string   `json:"version"`
	Kind       string   `json:"kind"`
	Name       string   `json:"name"` // Plural name (e.g., "deployments")
	Namespaced bool     `json:"namespaced"`
	IsCRD      bool     `json:"isCrd"`
	Verbs      []string `json:"verbs"`
}

// ResourceDiscovery manages discovery and caching of API resources
type ResourceDiscovery struct {
	resources   []APIResource
	resourceMap map[string]APIResource // keyed by lowercase kind
	gvrMap      map[string]schema.GroupVersionResource
	lastRefresh time.Time
	cacheTTL    time.Duration
	mu          sync.RWMutex
}

var (
	resourceDiscovery *ResourceDiscovery
	discoveryOnce     sync.Once
	discoveryMu       sync.Mutex
)

// coreAPIGroups are groups that ship with Kubernetes core
var coreAPIGroups = map[string]bool{
	"":                             true,
	"apps":                         true,
	"batch":                        true,
	"autoscaling":                  true,
	"networking.k8s.io":            true,
	"policy":                       true,
	"rbac.authorization.k8s.io":    true,
	"storage.k8s.io":               true,
	"admissionregistration.k8s.io": true,
	"apiextensions.k8s.io":         true,
	"certificates.k8s.io":          true,
	"coordination.k8s.io":          true,
	"discovery.k8s.io":             true,
	"events.k8s.io":                true,
	"flowcontrol.apiserver.k8s.io": true,
	"node.k8s.io":                  true,
	"scheduling.k8s.io":            true,
}

// InitResourceDiscovery initializes the resource discovery module
func InitResourceDiscovery() error {
	var initErr error
	discoveryOnce.Do(func() {
		resourceDiscovery = &ResourceDiscovery{
			resourceMap: make(map[string]APIResource),
			gvrMap:      make(map[string]schema.GroupVersionResource),
			cacheTTL:    5 * time.Minute,
		}
		initErr = resourceDiscovery.refresh()
	})
	return initErr
}

// GetResourceDiscovery returns the singleton discovery instance
func GetResourceDiscovery() *ResourceDiscovery {
	return resourceDiscovery
}

// ResetResourceDiscovery clears the resource discovery instance
// This must be called before ReinitResourceDiscovery when switching contexts
func ResetResourceDiscovery() {
	discoveryMu.Lock()
	defer discoveryMu.Unlock()

	resourceDiscovery = nil
	discoveryOnce = sync.Once{}
}

// ReinitResourceDiscovery reinitializes resource discovery after a context switch
// Must call ResetResourceDiscovery first
func ReinitResourceDiscovery() error {
	return InitResourceDiscovery()
}

// refresh fetches all API resources from the cluster
func (d *ResourceDiscovery) refresh() error {
	client := GetDiscoveryClient()
	if client == nil {
		return fmt.Errorf("discovery client not initialized")
	}

	start := time.Now()
	_, apiResourceLists, err := client.ServerGroupsAndResources()
	if err != nil {
		// Log partial results - some resources may fail but others succeed
		log.Printf("Warning: partial error discovering API resources: %v", err)
	}
	log.Printf("API resource discovery took %v", time.Since(start))

	d.mu.Lock()
	defer d.mu.Unlock()

	d.resources = nil
	d.resourceMap = make(map[string]APIResource)
	d.gvrMap = make(map[string]schema.GroupVersionResource)

	for _, apiList := range apiResourceLists {
		if apiList == nil {
			continue
		}

		// Parse group/version from apiList.GroupVersion (format: "group/version" or "version" for core)
		gv, err := schema.ParseGroupVersion(apiList.GroupVersion)
		if err != nil {
			continue
		}

		for _, apiRes := range apiList.APIResources {
			// Skip subresources (e.g., pods/log, deployments/scale)
			if strings.Contains(apiRes.Name, "/") {
				continue
			}

			// Determine if this is a CRD based on group
			isCRD := !coreAPIGroups[gv.Group]

			resource := APIResource{
				Group:      gv.Group,
				Version:    gv.Version,
				Kind:       apiRes.Kind,
				Name:       apiRes.Name,
				Namespaced: apiRes.Namespaced,
				IsCRD:      isCRD,
				Verbs:      apiRes.Verbs,
			}

			d.resources = append(d.resources, resource)

			// Store in map by lowercase kind for lookup
			kindKey := strings.ToLower(apiRes.Kind)
			// Prefer non-CRD resources if there's a conflict (unlikely)
			if existing, ok := d.resourceMap[kindKey]; !ok || (!isCRD && existing.IsCRD) {
				d.resourceMap[kindKey] = resource
				d.gvrMap[kindKey] = schema.GroupVersionResource{
					Group:    gv.Group,
					Version:  gv.Version,
					Resource: apiRes.Name,
				}
			}

			// Also store by plural name (lowercase)
			nameKey := strings.ToLower(apiRes.Name)
			if existing, ok := d.resourceMap[nameKey]; !ok || (!isCRD && existing.IsCRD) {
				d.resourceMap[nameKey] = resource
				d.gvrMap[nameKey] = schema.GroupVersionResource{
					Group:    gv.Group,
					Version:  gv.Version,
					Resource: apiRes.Name,
				}
			}
		}
	}

	d.lastRefresh = time.Now()
	log.Printf("Discovered %d API resources (%d unique kinds)", len(d.resources), len(d.resourceMap)/2)

	return nil
}

// GetAPIResources returns all discovered API resources
func (d *ResourceDiscovery) GetAPIResources() ([]APIResource, error) {
	if d == nil {
		return nil, fmt.Errorf("resource discovery not initialized")
	}

	d.mu.RLock()
	needsRefresh := time.Since(d.lastRefresh) > d.cacheTTL
	d.mu.RUnlock()

	if needsRefresh {
		if err := d.refresh(); err != nil {
			log.Printf("Warning: failed to refresh API resources: %v", err)
		}
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	// Return a copy
	result := make([]APIResource, len(d.resources))
	copy(result, d.resources)
	return result, nil
}

// GetGVR returns the GroupVersionResource for a given kind or plural name
func (d *ResourceDiscovery) GetGVR(kindOrName string) (schema.GroupVersionResource, bool) {
	if d == nil {
		return schema.GroupVersionResource{}, false
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	gvr, ok := d.gvrMap[strings.ToLower(kindOrName)]
	return gvr, ok
}

// GetGVRWithGroup returns the GroupVersionResource for a kind with a specific API group
// This is needed to disambiguate resources with the same name in different groups
// (e.g., "nodes" in core vs "nodemetrics" in metrics.k8s.io)
func (d *ResourceDiscovery) GetGVRWithGroup(kindOrName string, group string) (schema.GroupVersionResource, bool) {
	if d == nil {
		return schema.GroupVersionResource{}, false
	}

	// If no group specified, fall back to standard lookup
	if group == "" {
		return d.GetGVR(kindOrName)
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	// Search through all resources for matching kind/name AND group
	kindLower := strings.ToLower(kindOrName)
	for _, res := range d.resources {
		if (strings.ToLower(res.Kind) == kindLower || strings.ToLower(res.Name) == kindLower) && res.Group == group {
			return schema.GroupVersionResource{
				Group:    res.Group,
				Version:  res.Version,
				Resource: res.Name,
			}, true
		}
	}

	return schema.GroupVersionResource{}, false
}

// GetResource returns the APIResource for a given kind or plural name
func (d *ResourceDiscovery) GetResource(kindOrName string) (APIResource, bool) {
	if d == nil {
		return APIResource{}, false
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	res, ok := d.resourceMap[strings.ToLower(kindOrName)]
	return res, ok
}

// IsKnownResource checks if a kind or plural name is a known resource
func (d *ResourceDiscovery) IsKnownResource(kindOrName string) bool {
	_, ok := d.GetResource(kindOrName)
	return ok
}

// IsCRD checks if a kind or plural name is a CRD (not a core resource)
func (d *ResourceDiscovery) IsCRD(kindOrName string) bool {
	res, ok := d.GetResource(kindOrName)
	return ok && res.IsCRD
}

// SupportsWatch checks if a resource supports list and watch verbs
// Resources like selfsubjectreviews and tokenreviews are create-only and cannot be watched
func (d *ResourceDiscovery) SupportsWatch(kindOrName string) bool {
	res, ok := d.GetResource(kindOrName)
	if !ok {
		return false
	}
	hasList := false
	hasWatch := false
	for _, verb := range res.Verbs {
		if verb == "list" {
			hasList = true
		}
		if verb == "watch" {
			hasWatch = true
		}
	}
	return hasList && hasWatch
}

// SupportsWatchGVR checks if a GVR supports list and watch verbs
func (d *ResourceDiscovery) SupportsWatchGVR(gvr schema.GroupVersionResource) bool {
	// Look up by plural resource name
	return d.SupportsWatch(gvr.Resource)
}

// GetKindForGVR returns the Kind name for a given GVR
// e.g., for GVR{Resource: "rollouts"}, returns "Rollout"
func (d *ResourceDiscovery) GetKindForGVR(gvr schema.GroupVersionResource) string {
	res, ok := d.GetResource(gvr.Resource)
	if ok {
		return res.Kind
	}
	return ""
}
