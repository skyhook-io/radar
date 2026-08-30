package server

import (
	"os"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/auth"
)

// The queued-work lookup exists as an endpoint rather than a client-side list
// because the answer's scope is not the subject's: Kyverno records
// UpdateRequests in its own namespace whatever namespace the policy lives in.
// Asking the generic resources endpoint without a namespace does not mean
// cluster-wide — it applies the caller's namespace view filter, a browsing
// preference — so a reader narrowed to their own namespace was answered for the
// wrong scope. Scope belongs to permission, which is what the RBAC reverse
// lookups already do.

// Kyverno permits a namespaced Policy and a ClusterPolicy to share a name, and
// records the first qualified and the second bare. The handler matches exactly
// one form, so a namespaced policy cannot be shown a ClusterPolicy's backlog.
func TestPolicyQueued_MatchesExactlyOneNameForm(t *testing.T) {
	for _, c := range []struct {
		name      string
		policy    string
		namespace string
		recorded  string
		want      bool
	}{
		{"cluster-scoped matches bare", "require-labels", "", "require-labels", true},
		{"cluster-scoped rejects qualified", "require-labels", "", "team-a/require-labels", false},
		{"namespaced matches qualified", "require-labels", "team-a", "team-a/require-labels", true},
		{"namespaced rejects bare", "require-labels", "team-a", "require-labels", false},
		{"namespaced rejects another namespace", "require-labels", "team-a", "team-b/require-labels", false},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := c.recorded == policyRequestName(c.policy, c.namespace); got != c.want {
				t.Errorf("match(%q, ns=%q) against %q = %v, want %v",
					c.policy, c.namespace, c.recorded, got, c.want)
			}
		})
	}
}

// A caller who cannot list the kind is told so, rather than being handed an
// empty queue that reads as "this policy has nothing in flight".
func TestPolicyQueued_DeniesRatherThanReportingAnEmptyQueue(t *testing.T) {
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"app"}}
	allow(perms, "kyverno.io", "updaterequests", "", false)
	env.srv.permCache.Set("nobody", nil, perms)

	resp := env.authGet(t, "/api/policy/policies/require-labels/queued", "nobody", "")
	defer resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Errorf("status = %d, want 403 for a caller who may not list updaterequests", resp.StatusCode)
	}
}

// The input-validation and readiness paths, which the endpoint answers before it
// reads anything.
func TestPolicyQueued_RejectsAMissingPolicyName(t *testing.T) {
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"app"}}
	allow(perms, "kyverno.io", "updaterequests", "", true)
	env.srv.permCache.Set("alice", nil, perms)

	// chi will not route an empty {policy}, so the trailing-slash form is the
	// reachable shape of "no name given".
	resp := env.authGet(t, "/api/policy/policies//queued", "alice", "")
	defer resp.Body.Close()
	if resp.StatusCode == 200 {
		t.Errorf("status = 200 for a request naming no policy; want a 4xx")
	}
}

// A failed read must not render as an empty queue. The handler distinguishes an
// absent CRD (no background controller — genuinely nothing queued) from every
// other failure, which is the distinction certificate.go already draws for the
// same call and the one this whole surface exists to preserve.
func TestPolicyQueued_SeparatesAnAbsentCRDFromAFailedRead(t *testing.T) {
	src, err := os.ReadFile("policy_handlers.go")
	if err != nil {
		t.Fatalf("read handler: %v", err)
	}
	body := string(src)
	i := strings.Index(body, "func (s *Server) handlePolicyQueued")
	if i < 0 {
		t.Fatal("handler moved")
	}
	h := body[i:]
	if !strings.Contains(h, "k8s.ErrUnknownDynamicKind") {
		t.Error("handler does not special-case an absent CRD, so every failure reads as an empty queue")
	}
	if !strings.Contains(h, "StatusServiceUnavailable") {
		t.Error("handler has no failure path — a denied or unready read would answer 200 with nothing queued")
	}
}
