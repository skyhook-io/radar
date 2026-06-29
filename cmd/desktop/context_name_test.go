package main

import "testing"

// TestClusterShortName mirrors the test cases in
// packages/k8s-ui/src/utils/context-name.test.ts so the Go and TS parsers stay
// in lockstep. If you add a case to one side, add it to the other.
func TestClusterShortName(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// GKE
		{"gke zonal cluster", "gke_my-project_us-east1-b_prod-cluster", "prod-cluster"},
		{"gke regional cluster", "gke_my-project_us-central1_my-cluster", "my-cluster"},
		{"gke cluster name with underscores", "gke_proj_us-east1-b_my_funky_cluster", "my_funky_cluster"},
		{"gke arbitrary 3-underscore string is not GKE", "gke_a_b_c", "gke_a_b_c"},
		{"gke middle segment without digit is not a zone", "gke_proj_notazone_cluster", "gke_proj_notazone_cluster"},

		// EKS ARN
		{"eks arn standard", "arn:aws:eks:us-east-1:123456789012:cluster/my-prod", "my-prod"},
		{"eks arn cluster name containing region-like substring", "arn:aws:eks:eu-central-1:982081053473:cluster/us-east-1-nonprod", "us-east-1-nonprod"},

		// eksctl
		{"eksctl format", "admin@my-cluster.us-east-1.eksctl.io", "my-cluster"},

		// AKS
		{"aks clusterUser", "clusterUser_my-rg_my-cluster", "my-cluster"},
		{"aks clusterAdmin", "clusterAdmin_prod-rg_prod-cluster", "prod-cluster"},
		{"aks substring false-positive guarded: tracks-prod", "tracks-prod", "tracks-prod"},
		{"aks substring false-positive guarded: my-aks-experiment", "my-aks-experiment", "my-aks-experiment"},
		{"aks substring false-positive guarded: aks", "aks", "aks"},

		// Fallthrough — user-named contexts and the in-cluster sentinel pass through.
		{"unknown shape passes through", "my-staging-cluster", "my-staging-cluster"},
		{"in-cluster sentinel", "in-cluster", "in-cluster"},
		{"empty input", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := clusterShortName(tc.in); got != tc.want {
				t.Fatalf("clusterShortName(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
