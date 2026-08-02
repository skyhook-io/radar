package server

import "testing"

func TestNamespaceInAllowed(t *testing.T) {
	cases := []struct {
		name    string
		allowed []string
		ns      string
		want    bool
	}{
		{"nil = cluster-wide, all namespaces allowed", nil, "anything", true},
		{"empty non-nil = no access", []string{}, "a", false},
		{"member allowed", []string{"a", "b"}, "b", true},
		{"non-member denied", []string{"a", "b"}, "c", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := namespaceInAllowed(tc.allowed, tc.ns); got != tc.want {
				t.Fatalf("namespaceInAllowed(%v, %q) = %v, want %v", tc.allowed, tc.ns, got, tc.want)
			}
		})
	}
}
