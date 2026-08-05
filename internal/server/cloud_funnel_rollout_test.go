package server

import "testing"

func TestCloudFunnelBucketing(t *testing.T) {
	if !cloudFunnelBucketed(100, "") {
		t.Fatal("100 percent must include installs with no persistable identity")
	}
	if cloudFunnelBucketed(0, "some-install") {
		t.Fatal("0 percent must exclude everyone")
	}
	if cloudFunnelBucketed(50, "") {
		t.Fatal("a partial rollout must exclude installs with no persistable identity")
	}

	// Deterministic: the same install gets the same verdict on every call.
	for _, id := range []string{"a", "install-1", "0123456789abcdef"} {
		first := cloudFunnelBucketed(50, id)
		for i := 0; i < 3; i++ {
			if cloudFunnelBucketed(50, id) != first {
				t.Fatalf("verdict for %q is not deterministic", id)
			}
		}
	}

	// Monotonic: raising the percentage never removes an install from the
	// cohort — a ramp must only ever add users.
	for _, id := range []string{"a", "b", "c", "d", "e", "f", "g", "h"} {
		inAt := func(p uint32) bool { return cloudFunnelBucketed(p, id) }
		for p := uint32(1); p <= 100; p++ {
			if inAt(p-1) && !inAt(p) {
				t.Fatalf("install %q fell out of the cohort when ramping %d → %d", id, p-1, p)
			}
		}
	}
}

// An in-cluster install whose Deployment can't be read (chart predating the
// downward-API env vars, restricted RBAC) must still get a bucket key. Returning
// "" would pin it out of every rollout below 100% permanently — the failure that
// left ~17% of 1.9.0 in-cluster installs unable to reach the funnel at all.
func TestBucketKeyFallsBackWhenDeploymentUnreadable(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)

	// No cluster access in a test binary, so InstalledAt() is 0 — and
	// IsInCluster() is true here because no kubeconfig path is configured.
	key := bucketKey()
	if key == "" {
		t.Fatal("in-cluster install with an unreadable Deployment got no bucket key")
	}
	if !cloudFunnelBucketed(100, key) {
		t.Fatal("the fallback key is not usable for bucketing")
	}
	if bucketKey() != key {
		t.Fatal("fallback key is not stable across calls within a process")
	}
}

func TestCloudFunnelEnvOverrideWins(t *testing.T) {
	t.Setenv("RADAR_CLOUD_FUNNEL", "on")
	if !cloudFunnelInCohort() {
		t.Fatal("RADAR_CLOUD_FUNNEL=on did not force the funnel on")
	}
	t.Setenv("RADAR_CLOUD_FUNNEL", "off")
	if cloudFunnelInCohort() {
		t.Fatal("RADAR_CLOUD_FUNNEL=off did not force the funnel off")
	}
}
