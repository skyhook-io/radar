package tree

import (
	"testing"
)

func TestParseFluxInventoryID(t *testing.T) {
	cases := []struct {
		name string
		id   string
		want ResourceRef
		ok   bool
	}{
		{
			name: "typical Deployment ID",
			id:   "flux-system_podinfo_apps_Deployment",
			want: ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "flux-system", Name: "podinfo"},
			ok:   true,
		},
		{
			name: "name with underscores rejoined correctly",
			id:   "ns_my_weird_name_apps_Deployment",
			want: ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "ns", Name: "my_weird_name"},
			ok:   true,
		},
		{
			name: "core group string normalized to empty",
			id:   "default_my-cm_core_ConfigMap",
			want: ResourceRef{Group: "", Kind: "ConfigMap", Namespace: "default", Name: "my-cm"},
			ok:   true,
		},
		{
			name: "non-core group preserved",
			id:   "monitoring_alerts_monitoring.coreos.com_PrometheusRule",
			want: ResourceRef{Group: "monitoring.coreos.com", Kind: "PrometheusRule", Namespace: "monitoring", Name: "alerts"},
			ok:   true,
		},
		{
			name: "fewer than 4 parts → invalid",
			id:   "only_three_parts",
			ok:   false,
		},
		{
			name: "exactly 4 parts (minimum valid)",
			id:   "ns_name_g_K",
			want: ResourceRef{Group: "g", Kind: "K", Namespace: "ns", Name: "name"},
			ok:   true,
		},
		{
			name: "empty string is invalid",
			id:   "",
			ok:   false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseFluxInventoryID(tc.id)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if !tc.ok {
				return
			}
			if got != tc.want {
				t.Fatalf("ref = %+v, want %+v", got, tc.want)
			}
		})
	}
}
