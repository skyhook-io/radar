package k8s

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/pkg/k8score"
)

func helmReleaseSecret(rv, status, payload string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: "sh.helm.release.v1.web.v1", Namespace: "apps", UID: "uid-web-1", ResourceVersion: rv,
			Labels: map[string]string{"owner": "helm", "name": "web", "status": status, "version": "1"},
		},
		Type: k8score.HelmReleaseSecretType,
		Data: map[string][]byte{"release": []byte(payload)},
	}
}

func cachedHelmReleaseSecret(t *testing.T, rv, status, payload string) *corev1.Secret {
	t.Helper()
	obj, err := k8score.DropManagedFields(helmReleaseSecret(rv, status, payload))
	if err != nil {
		t.Fatal(err)
	}
	return obj.(*corev1.Secret)
}

// Helm hooks rewrite a release Secret with the same labels. The cache holds
// digests, not payloads, and the timeline must record the rewrite exactly as
// it does when it can compare the payloads.
func TestComputeDiff_HelmReleaseRewriteMatchesUncachedDiff(t *testing.T) {
	uncached := ComputeDiff("Secret", helmReleaseSecret("10", "pending-upgrade", "hook-1"), helmReleaseSecret("11", "pending-upgrade", "hook-2"))
	cached := ComputeDiff("Secret",
		cachedHelmReleaseSecret(t, "10", "pending-upgrade", "hook-1"),
		cachedHelmReleaseSecret(t, "11", "pending-upgrade", "hook-2"))
	if uncached == nil || cached == nil || cached.Summary != uncached.Summary || cached.Summary != "data modified keys: [release]" {
		t.Fatalf("cached diff = %+v, uncached diff = %+v", cached, uncached)
	}
	if same := ComputeDiff("Secret",
		cachedHelmReleaseSecret(t, "11", "pending-upgrade", "hook-2"),
		cachedHelmReleaseSecret(t, "12", "pending-upgrade", "hook-2")); same != nil {
		t.Fatalf("unchanged payload recorded as a change: %+v", same)
	}
}

// Every path that hands cached Secrets to a caller serves Helm release
// Secrets without the digests the cache keeps in place of their values.
func TestCachedHelmReleaseSecretServedWithoutValues(t *testing.T) {
	t.Cleanup(func() { ResetResourceCache() })
	if err := InitTestResourceCache(fake.NewClientset(helmReleaseSecret("10", "deployed", "payload"))); err != nil {
		t.Fatal(err)
	}
	cache := GetResourceCache()
	if raw, _ := cache.Secrets().Secrets("apps").Get("sh.helm.release.v1.web.v1"); raw == nil || len(raw.Data["release"]) == 0 || string(raw.Data["release"]) == "payload" {
		t.Fatalf("cache should hold a digest, got %+v", raw)
	}

	check := func(path string, secret *corev1.Secret) {
		t.Helper()
		value, ok := secret.Data["release"]
		if !ok || value != nil {
			t.Fatalf("%s: release = %q (present %v), want the key with no value", path, value, ok)
		}
	}
	obj, err := FetchResource(cache, "secrets", "apps", "sh.helm.release.v1.web.v1")
	if err != nil {
		t.Fatal(err)
	}
	check("FetchResource", obj.(*corev1.Secret))
	objs, err := FetchResourceList(cache, "secrets", nil)
	if err != nil || len(objs) != 1 {
		t.Fatalf("FetchResourceList: %v %v", objs, err)
	}
	check("FetchResourceList", objs[0].(*corev1.Secret))

	u, err := cache.GetDynamicWithGroup(context.Background(), "secrets", "apps", "sh.helm.release.v1.web.v1", "")
	if err != nil {
		t.Fatal(err)
	}
	data, _ := u.Object["data"].(map[string]any)
	if value, ok := data["release"]; !ok || value != nil {
		t.Fatalf("GetDynamicWithGroup: release = %v (present %v), want the key with no value", value, ok)
	}
}
