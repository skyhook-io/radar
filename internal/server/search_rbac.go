package server

import (
	"context"
	"net/http"
	"sync"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

// sensitiveSearchKinds enumerates cluster-scoped kinds whose default presence
// in /api/search results would leak information beyond what the calling user
// can fetch via /api/resources/{kind} under their own RBAC. Read access to a
// namespace doesn't imply read access to Node, PersistentVolume,
// StorageClass, or Namespace metadata — these need their own cluster-scope
// SAR. Secrets are namespaced and handled separately by
// computeSearchKindRBAC, which supports per-namespace permission.
//
// The walker in internal/search consults Options.SkipKinds, populated by SAR
// per (user, kind) at cluster scope. Users without `list X` at cluster scope
// have X dropped from the scan — including for explicit `kind:X` queries,
// which report the omitted kinds as coverage gaps.
var sensitiveSearchKinds = []struct {
	Kind     string // singular Kind for SkipKinds map
	Resource string // plural for SAR ResourceAttributes
	Group    string // API group; empty for core
}{
	{"Node", "nodes", ""},
	{"PersistentVolume", "persistentvolumes", ""},
	{"StorageClass", "storageclasses", "storage.k8s.io"},
	{"Namespace", "namespaces", ""},
}

// computeSearchSkipKinds runs SARs for each sensitive kind and
// returns a SkipKinds map suitable for search.Options. Returns nil
// when there's no user identity (auth-mode=none) — the SA's own
// permissions apply via the cache layer, no extra gating needed.
//
// SARs run in parallel; a single failure (k8s API blip) is treated
// as "deny that kind" rather than failing the whole search — fail-
// closed is the safer default for sensitive data.
func (s *Server) computeSearchSkipKinds(r *http.Request) map[string]bool {
	user := auth.UserFromContext(r.Context())
	if user == nil {
		// auth.mode=none — no end-user identity, SA RBAC is the only
		// authorization layer. Cache lister already returns "forbidden"
		// when the SA can't list. Nothing for us to add.
		return nil
	}
	client := k8s.GetClient()
	if client == nil {
		// Defensive: cache client not initialized. Fail closed on all
		// sensitive kinds rather than silently leaking through.
		out := make(map[string]bool, len(sensitiveSearchKinds))
		for _, k := range sensitiveSearchKinds {
			out[k.Kind] = true
		}
		return out
	}

	type result struct {
		kind    string
		allowed bool
	}
	results := make(chan result, len(sensitiveSearchKinds))
	var wg sync.WaitGroup
	for _, k := range sensitiveSearchKinds {
		wg.Add(1)
		go func(kind, resource, group string) {
			defer wg.Done()
			// Cluster-scope SAR: namespace="" means "any namespace"
			// for namespaced resources and "the resource itself" for
			// cluster-scoped — both are the right shape for "can the
			// user enumerate this kind at all."
			allowed, err := pkgauth.SubjectCanI(r.Context(), client, user.Username, user.Groups, "", group, resource, "list")
			if err != nil {
				// SAR API itself failed (rare). Treat as deny.
				results <- result{kind: kind, allowed: false}
				return
			}
			results <- result{kind: kind, allowed: allowed}
		}(k.Kind, k.Resource, k.Group)
	}
	wg.Wait()
	close(results)

	skip := make(map[string]bool, len(sensitiveSearchKinds))
	for r := range results {
		if !r.allowed {
			skip[r.kind] = true
		}
	}
	return skip
}

// computeSearchKindRBAC narrows namespaced scans using the caller's exact
// list permission; namespace visibility alone does not authorize these kinds.
func (s *Server) computeSearchKindRBAC(r *http.Request, scanNamespaces []string, group, resource string) (decision string, scopedNamespaces []string) {
	if auth.UserFromContext(r.Context()) == nil {
		return "", nil
	}

	if scanNamespaces == nil {
		if s.canRead(r, group, resource, "", "list") {
			return "", nil
		}
		return "skip", nil
	}

	if len(scanNamespaces) == 0 {
		return "skip", nil
	}

	scoped := make([]string, 0, len(scanNamespaces))
	for _, ns := range scanNamespaces {
		if s.canRead(r, group, resource, ns, "list") {
			scoped = append(scoped, ns)
		}
	}
	if len(scoped) == 0 {
		return "skip", nil
	}
	return "override", scoped
}

// _ context.Context — kept to make ctx threading explicit if a future
// caller passes one in without an http.Request.
var _ context.Context
