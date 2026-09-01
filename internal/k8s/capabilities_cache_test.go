package k8s

import "testing"

func TestInvalidateCapabilitiesCacheClearsUserCaches(t *testing.T) {
	InvalidateCapabilitiesCache()
	t.Cleanup(InvalidateCapabilitiesCache)

	const username = "alice"
	namespaceKey := userNamespaceCapabilitiesCacheKey(username, "default")
	userCapabilitiesCache.Store(username, &userCapEntry{})
	userNamespaceCapabilitiesCache.Store(namespaceKey, &userNSCapEntry{})

	InvalidateCapabilitiesCache()

	if _, ok := userCapabilitiesCache.Load(username); ok {
		t.Error("user capabilities cache was not cleared")
	}
	if _, ok := userNamespaceCapabilitiesCache.Load(namespaceKey); ok {
		t.Error("user namespace capabilities cache was not cleared")
	}
}
