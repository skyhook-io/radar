package k8score

import (
	"fmt"
	"log"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
)

// APIResource represents a discovered API resource type.
type APIResource struct {
	Group      string   `json:"group"`
	Version    string   `json:"version"`
	Kind       string   `json:"kind"`
	Name       string   `json:"name"` // Plural name (e.g., "deployments")
	Namespaced bool     `json:"namespaced"`
	IsCRD      bool     `json:"isCrd"`
	Verbs      []string `json:"verbs"`
}

// DiscoveryStats holds read-only stats about API discovery state.
type DiscoveryStats struct {
	TotalResources int
	CRDCount       int
	LastRefresh    time.Time
}

// ResourceDiscovery manages discovery and caching of API resources.
// It is safe for concurrent use.
type ResourceDiscovery struct {
	client      discovery.DiscoveryInterface
	resources   []APIResource
	resourceMap map[string]APIResource // keyed by lowercase kind
	gvrMap      map[string]schema.GroupVersionResource
	lastRefresh time.Time
	partial     bool
	failedGroup map[string]bool
	cacheTTL    time.Duration
	mu          sync.RWMutex
}

// DiscoveryOption is a functional option for NewResourceDiscovery.
type DiscoveryOption func(*ResourceDiscovery)

// WithDiscoveryCacheTTL overrides the default 5-minute refresh interval.
func WithDiscoveryCacheTTL(d time.Duration) DiscoveryOption {
	return func(rd *ResourceDiscovery) {
		rd.cacheTTL = d
	}
}

// coreAPIGroups are groups that ship with Kubernetes core.
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

// versionStability returns a score for API version stability.
// Higher is more stable: stable (3) > beta (2) > alpha (1).
func versionStability(version string) int {
	if strings.Contains(version, "alpha") {
		return 1
	}
	if strings.Contains(version, "beta") {
		return 2
	}
	return 3 // v1, v2, etc.
}

// versionRegex parses Kubernetes API versions like "v1", "v2beta1", "v1alpha2".
var versionRegex = regexp.MustCompile(`^v(\d+)(?:(alpha|beta)(\d+))?$`)

// parseVersion extracts the numeric components of a Kubernetes API version.
func parseVersion(version string) (major, qualifierNum int) {
	m := versionRegex.FindStringSubmatch(version)
	if m == nil {
		return 0, 0
	}
	major, _ = strconv.Atoi(m[1])
	if m[3] != "" {
		qualifierNum, _ = strconv.Atoi(m[3])
	}
	return
}

// IsMoreStableVersion returns true if newVersion is more stable than oldVersion.
// Compares stability tier first (stable > beta > alpha), then numeric version
// within the same tier (v1beta3 > v1beta2, v2 > v1).
func IsMoreStableVersion(newVersion, oldVersion string) bool {
	newStab := versionStability(newVersion)
	oldStab := versionStability(oldVersion)
	if newStab != oldStab {
		return newStab > oldStab
	}
	newMajor, newQual := parseVersion(newVersion)
	oldMajor, oldQual := parseVersion(oldVersion)
	if newMajor != oldMajor {
		return newMajor > oldMajor
	}
	return newQual > oldQual
}

// NewResourceDiscovery creates a ResourceDiscovery backed by the given client.
// It performs an initial refresh; returns an error only if the client is nil.
func NewResourceDiscovery(client discovery.DiscoveryInterface, opts ...DiscoveryOption) (*ResourceDiscovery, error) {
	if client == nil {
		return nil, fmt.Errorf("discovery client must not be nil")
	}

	rd := &ResourceDiscovery{
		client:      client,
		resourceMap: make(map[string]APIResource),
		gvrMap:      make(map[string]schema.GroupVersionResource),
		cacheTTL:    5 * time.Minute,
	}
	for _, opt := range opts {
		opt(rd)
	}

	if err := rd.Refresh(); err != nil {
		// Log but don't fail — partial results are OK
		log.Printf("Warning: initial API resource discovery returned partial results: %v", err)
	}

	return rd, nil
}

// Refresh fetches all API resources from the cluster.
func (d *ResourceDiscovery) Refresh() error {
	if d == nil || d.client == nil {
		return fmt.Errorf("discovery not initialized")
	}

	start := time.Now()
	_, apiResourceLists, err := d.client.ServerGroupsAndResources()
	if err != nil {
		log.Printf("Warning: partial error discovering API resources: %v", err)
	}
	failedGroups := make(map[string]bool)
	if groups, ok := discovery.GroupDiscoveryFailedErrorGroups(err); ok {
		for gv := range groups {
			failedGroups[gv.Group] = true
		}
	}
	partial := discovery.IsGroupDiscoveryFailedError(err) || len(failedGroups) > 0
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

		gv, err := schema.ParseGroupVersion(apiList.GroupVersion)
		if err != nil {
			continue
		}

		for _, apiRes := range apiList.APIResources {
			if strings.Contains(apiRes.Name, "/") {
				continue
			}

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

			d.addResourceLocked(resource)
		}
	}

	d.lastRefresh = time.Now()
	d.partial = partial
	d.failedGroup = failedGroups
	log.Printf("Discovered %d API resources (%d unique kinds)", len(d.resources), len(d.resourceMap)/2)

	return nil
}

// HasPartialDiscovery reports whether the last refresh missed one or more
// group/versions while still returning partial API resource data.
func (d *ResourceDiscovery) HasPartialDiscovery() bool {
	if d == nil {
		return false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.partial
}

// GroupHadPartialDiscovery reports whether a group was specifically missing
// from the last partial discovery response.
func (d *ResourceDiscovery) GroupHadPartialDiscovery(group string) bool {
	if d == nil {
		return false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.failedGroup[group]
}

// AddAPIResource registers a resource that was proven accessible without
// server discovery returning it, such as under restricted discovery RBAC.
func (d *ResourceDiscovery) AddAPIResource(resource APIResource) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.addResourceLocked(resource)
}

func (d *ResourceDiscovery) addResourceLocked(resource APIResource) {
	for i, existing := range d.resources {
		if existing.Group == resource.Group && existing.Version == resource.Version && existing.Name == resource.Name {
			d.resources[i] = resource
			d.indexResourceLocked(resource)
			return
		}
	}
	d.resources = append(d.resources, resource)
	d.indexResourceLocked(resource)
}

func (d *ResourceDiscovery) indexResourceLocked(resource APIResource) {
	gvr := schema.GroupVersionResource{
		Group:    resource.Group,
		Version:  resource.Version,
		Resource: resource.Name,
	}

	// Store in map by lowercase kind for lookup. Prefer non-CRD over CRD,
	// then stable versions within the same group.
	kindKey := strings.ToLower(resource.Kind)
	if existing, ok := d.resourceMap[kindKey]; !ok ||
		(!resource.IsCRD && existing.IsCRD) ||
		(resource.IsCRD == existing.IsCRD && existing.Group == resource.Group && IsMoreStableVersion(resource.Version, existing.Version)) {
		d.resourceMap[kindKey] = resource
		d.gvrMap[kindKey] = gvr
	}

	nameKey := strings.ToLower(resource.Name)
	if existing, ok := d.resourceMap[nameKey]; !ok ||
		(!resource.IsCRD && existing.IsCRD) ||
		(resource.IsCRD == existing.IsCRD && existing.Group == resource.Group && IsMoreStableVersion(resource.Version, existing.Version)) {
		d.resourceMap[nameKey] = resource
		d.gvrMap[nameKey] = gvr
	}
}

// Stats returns lightweight stats without triggering a refresh.
func (d *ResourceDiscovery) Stats() DiscoveryStats {
	if d == nil {
		return DiscoveryStats{}
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	crdCount := 0
	for _, res := range d.resources {
		if res.IsCRD {
			crdCount++
		}
	}

	return DiscoveryStats{
		TotalResources: len(d.resources),
		CRDCount:       crdCount,
		LastRefresh:    d.lastRefresh,
	}
}

// GetAPIResources returns all discovered API resources, deduplicating by
// name+group and keeping the most stable version.
func (d *ResourceDiscovery) GetAPIResources() ([]APIResource, error) {
	if d == nil {
		return nil, fmt.Errorf("resource discovery not initialized")
	}

	d.mu.RLock()
	needsRefresh := time.Since(d.lastRefresh) > d.cacheTTL
	d.mu.RUnlock()

	if needsRefresh {
		if err := d.Refresh(); err != nil {
			log.Printf("Warning: failed to refresh API resources: %v", err)
		}
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	type entry struct {
		index   int
		version string
	}
	seen := make(map[string]entry, len(d.resources))
	result := make([]APIResource, 0, len(d.resources))

	for _, res := range d.resources {
		key := res.Name + "/" + res.Group
		if existing, ok := seen[key]; !ok {
			seen[key] = entry{index: len(result), version: res.Version}
			result = append(result, res)
		} else if IsMoreStableVersion(res.Version, existing.version) {
			result[existing.index] = res
			seen[key] = entry{index: existing.index, version: res.Version}
		}
	}

	return result, nil
}

// GetGVR returns the GroupVersionResource for a given kind or plural name.
// WARNING: If multiple CRDs share the same Kind across different API groups,
// this returns whichever was discovered first. Use GetGVRWithGroup to disambiguate.
func (d *ResourceDiscovery) GetGVR(kindOrName string) (schema.GroupVersionResource, bool) {
	if d == nil {
		return schema.GroupVersionResource{}, false
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	gvr, ok := d.gvrMap[strings.ToLower(kindOrName)]
	return gvr, ok
}

// GetGVRWithGroup returns the GroupVersionResource for a kind with a specific API group.
func (d *ResourceDiscovery) GetGVRWithGroup(kindOrName string, group string) (schema.GroupVersionResource, bool) {
	if d == nil {
		return schema.GroupVersionResource{}, false
	}

	if group == "" {
		return d.GetGVR(kindOrName)
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

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

// GetResource returns the APIResource for a given kind or plural name.
func (d *ResourceDiscovery) GetResource(kindOrName string) (APIResource, bool) {
	if d == nil {
		return APIResource{}, false
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	res, ok := d.resourceMap[strings.ToLower(kindOrName)]
	return res, ok
}

// GetResourceWithGroup returns the APIResource for a kind in a specific
// API group. Mirrors GetGVRWithGroup but yields the full resource (incl.
// Namespaced) rather than just the GVR. Empty group falls back to the
// kind-keyed lookup (first match wins, with the same caveat as GetGVR).
//
// Used for authorization decisions where the caller has both kind and
// group from a request and needs to know the resource's scope before
// running a SubjectAccessReview.
func (d *ResourceDiscovery) GetResourceWithGroup(kindOrName, group string) (APIResource, bool) {
	if d == nil {
		return APIResource{}, false
	}

	if group == "" {
		return d.GetResource(kindOrName)
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	kindLower := strings.ToLower(kindOrName)
	for _, res := range d.resources {
		if (strings.ToLower(res.Kind) == kindLower || strings.ToLower(res.Name) == kindLower) && res.Group == group {
			return res, true
		}
	}
	return APIResource{}, false
}

// IsKnownResource checks if a kind or plural name is a known resource.
func (d *ResourceDiscovery) IsKnownResource(kindOrName string) bool {
	_, ok := d.GetResource(kindOrName)
	return ok
}

// IsCRD checks if a kind or plural name is a CRD (not a core resource).
func (d *ResourceDiscovery) IsCRD(kindOrName string) bool {
	res, ok := d.GetResource(kindOrName)
	return ok && res.IsCRD
}

// SupportsWatch checks if a resource supports list and watch verbs.
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

// SupportsWatchGVR checks if a GVR supports list and watch verbs.
func (d *ResourceDiscovery) SupportsWatchGVR(gvr schema.GroupVersionResource) bool {
	if d == nil {
		return false
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	for _, res := range d.resources {
		if res.Group != gvr.Group || res.Version != gvr.Version || res.Name != gvr.Resource {
			continue
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
	return false
}

// HasKindInGroup reports whether a specific Kind exists within an API
// group. Use this when you depend on a specific CRD being registered.
func (d *ResourceDiscovery) HasKindInGroup(kind, group string) bool {
	if d == nil {
		return false
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	kindLower := strings.ToLower(kind)
	for _, res := range d.resources {
		if res.Group == group && strings.ToLower(res.Kind) == kindLower {
			return true
		}
	}
	return false
}

// IsKyvernoInstalled reports whether the Kyverno admission controller's
// CRDs are present on the cluster. The check uses Kyverno's own Policy
// and ClusterPolicy CRDs as the signal — these are unique to Kyverno
// itself, whereas the PolicyReport CRDs (wgpolicyk8s.io) are emitted by
// several engines (Kyverno, Trivy, etc.) and so do not by themselves
// imply Kyverno is the source.
//
// The signal drives conditional eager warmup of PolicyReport informers:
// clusters without Kyverno keep the reports in the deferred-fetch tier
// and pay no extra memory or watch budget.
func (d *ResourceDiscovery) IsKyvernoInstalled() bool {
	if d == nil {
		return false
	}
	return d.HasKindInGroup("Policy", "kyverno.io") || d.HasKindInGroup("ClusterPolicy", "kyverno.io")
}

// GetKindForGVR returns the Kind name for a given GVR
// e.g., for GVR{Resource: "rollouts"}, returns "Rollout".
func (d *ResourceDiscovery) GetKindForGVR(gvr schema.GroupVersionResource) string {
	if d == nil {
		return ""
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	for _, res := range d.resources {
		if res.Group == gvr.Group && res.Version == gvr.Version && res.Name == gvr.Resource {
			return res.Kind
		}
	}
	return ""
}
