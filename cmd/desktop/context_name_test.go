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

		// OpenShift oc login: the server host names the cluster
		{"openshift oc login", "myproject/api-prod-example-com:6443/alice", "api-prod-example-com"},
		{"openshift user with colon", "default/api-crc-testing:6443/kube:admin", "api-crc-testing"},
		{"openshift user with slash", "team-a/api-x-example-com:6443/idp/alice", "api-x-example-com"},
		{"openshift ipv6 host", "default/[2001:db8::1]:6443/alice", "[2001:db8::1]"},
		{"gitlab agent is not openshift", "gitlab-org/infra/platform/k8s-agents:production-eu-west-agent", "gitlab-org/infra/platform/k8s-agents:production-eu-west-agent"},
		{"gitlab agent short path is not openshift", "group/project:agent", "group/project:agent"},
		{"no user is not openshift", "group/project:6443/", "group/project:6443/"},
		{"dotted host is not openshift", "ns/api.prod.example.com:6443/alice", "ns/api.prod.example.com:6443/alice"},
		{"project ending in dash is not openshift", "ns-/api-prod:6443/alice", "ns-/api-prod:6443/alice"},
		{"host ending in dash is not openshift", "ns/api-prod-:6443/alice", "ns/api-prod-:6443/alice"},
		{"port out of range is not openshift", "ns/api-prod:99999/alice", "ns/api-prod:99999/alice"},
		{"port zero is not openshift", "ns/api-prod:0/alice", "ns/api-prod:0/alice"},
		{"eks arn with slash stays eks", "arn:aws:eks:us-east-1:123456789012:cluster/team/prod", "team/prod"},

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
