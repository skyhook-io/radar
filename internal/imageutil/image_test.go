package imageutil

import "testing"

func TestRepository(t *testing.T) {
	cases := map[string]string{
		"nginx:1.27":                "nginx",
		"ghcr.io/acme/api:1.2.0":    "ghcr.io/acme/api",
		"registry:5000/team/img:v1": "registry:5000/team/img", // registry port colon is not the tag separator
		"repo/img@sha256:abc":       "repo/img",
		"registry:5000/team/img":    "registry:5000/team/img", // no tag
		"":                          "",
	}
	for in, want := range cases {
		if got := Repository(in); got != want {
			t.Errorf("Repository(%q) = %q, want %q", in, got, want)
		}
	}
}
