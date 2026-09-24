package k8s

import "testing"

func TestSameAPIServer(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"https://ABC.gr7.eu-central-1.eks.amazonaws.com", "https://abc.gr7.eu-central-1.eks.amazonaws.com/", true},
		{"https://34.73.119.131", "https://34.73.119.131:443", true},
		{"https://[2001:db8::1]:6443", "https://[2001:db8::1]:6443/", true},
		{"https://rancher.example.com/k8s/clusters/c-prod", "https://rancher.example.com/k8s/clusters/c-prod/", true},
		{"https://prod.example.com?x=1", "https://prod.example.com", true},
		// Different clusters.
		{"https://rancher.example.com/k8s/clusters/c-prod", "https://rancher.example.com/k8s/clusters/c-staging", false},
		{"https://rancher.example.com/k8s/clusters/c-prod", "https://rancher.example.com", false},
		{"https://34.73.119.131", "https://35.243.244.193", false},
		{"https://prod.example.com:6443", "https://prod.example.com", false},
		{"http://prod.example.com", "https://prod.example.com", false},
		// Unusable input never matches.
		{"", "", false},
		{"not a url", "not a url", false},
		{"kubernetes.default.svc", "kubernetes.default.svc", false},
	}
	for _, tc := range cases {
		if got := SameAPIServer(tc.a, tc.b); got != tc.want {
			t.Errorf("SameAPIServer(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}
