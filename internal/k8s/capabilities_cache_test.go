package k8s

import "testing"

func TestInvalidateCapabilitiesCacheClearsUserCaches(t *testing.T) {
	InvalidateCapabilitiesCache()
	t.Cleanup(InvalidateCapabilitiesCache)

	const username = "alice"
	namespaceKey := userNamespaceCapabilitiesCacheKey(username, nil, "default")
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

func TestUserNamespaceCapabilitiesCacheKeyIncludesGroups(t *testing.T) {
	withGroup := userNamespaceCapabilitiesCacheKey("alice", []string{"radar:idp:payments"}, "payments")
	withoutGroup := userNamespaceCapabilitiesCacheKey("alice", nil, "payments")
	if withGroup == withoutGroup {
		t.Fatal("a user who lost a group must not reuse the verdict cached for the old group set")
	}
	reordered := userNamespaceCapabilitiesCacheKey("alice", []string{"b", "a"}, "payments")
	sorted := userNamespaceCapabilitiesCacheKey("alice", []string{"a", "b"}, "payments")
	if reordered != sorted {
		t.Error("group order must not split the cache")
	}
}
