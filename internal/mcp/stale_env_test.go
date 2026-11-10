package mcp

import (
	"context"
	"strings"
	"testing"
)

// get_resource on a rotated Secret, or on a Pod or workload that consumes it,
// carries staleEnv even with context=none. The diagnose fixture's cart Pod
// reads Secret/cart-db key password as env DB_PASSWORD and started before the
// Secret's managedFields data write, which Radar did not observe.
func TestGetResourceStaleEnv(t *testing.T) {
	grant := func(t *testing.T, pods, secrets bool) context.Context {
		t.Helper()
		ctx := withClusterAdmin(t, "admin")
		perms := getPermCache().Get("admin", nil)
		perms.SetCanI("get", "", "pods", "alpha", pods)
		perms.SetCanI("get", "", "secrets", "alpha", secrets)
		perms.SetCanI("get", "", "configmaps", "alpha", true)
		return ctx
	}
	get := func(t *testing.T, ctx context.Context, kind, name, contextMode string) string {
		t.Helper()
		result, _, err := handleGetResource(ctx, nil, getResourceInput{Kind: kind, Namespace: "alpha", Name: name, Context: contextMode})
		if err != nil {
			t.Fatalf("get_resource %s/%s: %v", kind, name, err)
		}
		return extractText(t, result)
	}

	for _, tc := range []struct{ kind, name string }{
		{"secret", "cart-db"},
		{"pod", "cart-abc123"},
		{"deployment", "cart"},
	} {
		for _, contextMode := range []string{"", "none"} {
			t.Run(tc.kind+"/context="+contextMode, func(t *testing.T) {
				setupFakeCacheForDiagnoseTests(t)
				body := get(t, grant(t, true, true), tc.kind, tc.name, contextMode)
				for _, want := range []string{
					`"staleEnv":{"total":1`,
					`"pod":"cart-abc123"`,
					`"source":"Secret/cart-db"`,
					`"evidenceSource":"managed_fields_mtime"`,
					"Pod cart-abc123: Secret/cart-db had a write at",
					"the write may not have touched data at all",
				} {
					if !strings.Contains(body, want) {
						t.Fatalf("missing %q in %s", want, body)
					}
				}
				// The fallback can't name a key, and never a value.
				_, staleEnv, _ := strings.Cut(body, `"staleEnv"`)
				if strings.Contains(staleEnv, `"key":`) || strings.Contains(body, "diagnose-secret-sentinel") {
					t.Fatalf("managedFields fallback claimed a key or leaked a value: %s", body)
				}
			})
		}
	}

	t.Run("Secret without pods access hides consumers", func(t *testing.T) {
		setupFakeCacheForDiagnoseTests(t)
		body := get(t, grant(t, false, true), "secret", "cart-db", "none")
		if strings.Contains(body, "staleEnv") || strings.Contains(body, "cart-abc123") {
			t.Fatalf("staleEnv named a Pod the caller can't read: %s", body)
		}
	})

	t.Run("Deployment without pods access hides consumers", func(t *testing.T) {
		setupFakeCacheForDiagnoseTests(t)
		if body := get(t, grant(t, false, true), "deployment", "cart", "none"); strings.Contains(body, "staleEnv") {
			t.Fatalf("staleEnv named a Pod the caller can't read: %s", body)
		}
	})

	t.Run("Pod without secrets access hides the Secret row", func(t *testing.T) {
		setupFakeCacheForDiagnoseTests(t)
		if body := get(t, grant(t, true, false), "pod", "cart-abc123", "none"); strings.Contains(body, "staleEnv") || strings.Contains(body, "secret-controller") {
			t.Fatalf("staleEnv named a Secret write the caller can't read: %s", body)
		}
	})
}
