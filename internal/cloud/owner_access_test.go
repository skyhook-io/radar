package cloud

import (
	"reflect"
	"slices"
	"testing"
)

func TestFilterOwnerAccess(t *testing.T) {
	t.Setenv("RADAR_CLOUD_MODE", "true")
	t.Setenv("RADAR_CLOUD_OWNER_ACCESS", "argocd,velero,unknown")
	for _, tc := range []struct {
		name   string
		tunnel bool
		role   string
		want   []string
	}{
		{"approved", true, "radar:owner", []string{"radar:owner", "radar:owner-access:argocd", "radar:idp:ops"}},
		{"legacy owner", true, "cloud:owner", []string{"cloud:owner", "radar:owner-access:argocd", "radar:idp:ops"}},
		{"member", true, "radar:member", []string{"radar:member", "radar:idp:ops"}},
		{"outside tunnel", false, "radar:owner", []string{"radar:owner", "radar:idp:ops"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			groups := []string{tc.role, "radar:owner-access:argocd", "radar:owner-access:velero-delete", "radar:owner-access:unknown", "radar:idp:ops"}
			if got := FilterOwnerAccess(groups, tc.tunnel); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("groups = %v; want %v", got, tc.want)
			}
		})
	}
	if got := OwnerAccess(); !slices.Equal(got, []string{"argocd", "velero"}) {
		t.Fatalf("approved grants = %v", got)
	}
}
