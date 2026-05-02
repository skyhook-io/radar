package server

import (
	"testing"
)

// HasNamespaceAccess is the gitopsRequest method that handlers call before
// running the (potentially expensive) topology+tree build. It mirrors the
// noNamespaceAccess contract (tested in server_auth_test.go) but as a
// method on the request struct, so this guards against the predicate
// drifting out of sync with the underlying helper.
func TestGitopsRequestHasNamespaceAccess(t *testing.T) {
	cases := []struct {
		name string
		ns   []string
		want bool
	}{
		{name: "nil → allowed (no filter)", ns: nil, want: true},
		{name: "empty slice → denied", ns: []string{}, want: false},
		{name: "specific ns → allowed", ns: []string{"argocd"}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := &gitopsRequest{AllowedNamespaces: tc.ns}
			if got := req.HasNamespaceAccess(); got != tc.want {
				t.Fatalf("HasNamespaceAccess() = %v, want %v", got, tc.want)
			}
		})
	}
}
