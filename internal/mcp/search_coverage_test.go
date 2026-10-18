package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"unicode/utf8"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/search"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

func TestSearchNamespacedKindRBAC(t *testing.T) {
	for _, kind := range search.NamespacedSearchKinds {
		t.Run(kind.Kind, func(t *testing.T) {
			username := "search-kind-" + kind.Kind
			ctx := withClusterAdmin(t, username)
			perms := getPermCache().Get(username, nil)
			perms.SetCanI("list", kind.Group, kind.Resource, "", false)
			perms.SetCanI("list", kind.Group, kind.Resource, "a", true)
			perms.SetCanI("list", kind.Group, kind.Resource, "b", false)
			decision, ns := mcpSearchKindRBAC(ctx, nil, kind.Group, kind.Resource)
			if decision != "skip" {
				t.Fatalf("cluster-scope bypass: %s %v", decision, ns)
			}
			decision, ns = mcpSearchKindRBAC(ctx, []string{"a", "b"}, kind.Group, kind.Resource)
			if decision != "override" || len(ns) != 1 || ns[0] != "a" {
				t.Fatalf("kind override: %s %v", decision, ns)
			}
			decision, _ = mcpSearchKindRBAC(ctx, []string{"b"}, kind.Group, kind.Resource)
			if decision != "skip" {
				t.Fatalf("denied namespace bypass: %s", decision)
			}
			decision, _ = mcpSearchKindRBAC(context.Background(), nil, kind.Group, kind.Resource)
			if decision != "" {
				t.Fatalf("no-auth gate: %s", decision)
			}
		})
	}
	t.Cleanup(func() { getPermCache().Invalidate() })
}

func TestHandleSearchExcludedNamespaceCoverage(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	ctx := withRestrictedUser(t, "excluded-search", []string{"alpha"})
	for _, tc := range []struct {
		input searchInput
		hits  int
	}{
		{searchInput{Query: "kind:Pod", Namespace: "beta"}, 0},
		{searchInput{Query: "kind:Pod ns:alpha ns:beta"}, 1},
	} {
		body := mcpSearch(t, ctx, tc.input)
		excluded := slices.ContainsFunc(hintEntries(body.Coverage), func(gap hintEntry) bool {
			return gap.Kind == "*" && gap.Reason == "user_namespace_not_visible" && slices.Equal(gap.Namespaces, []string{"beta"})
		})
		if body.Coverage.Status != resourceapi.CoveragePartial || !excluded || len(body.Hits) != tc.hits || (tc.hits > 0 && body.Hits[0].Namespace != "alpha") {
			t.Fatalf("%+v: scope exclusion: %+v", tc.input, body)
		}
	}
}

func TestSearchKindRBACClusterFirstBoundsSARs(t *testing.T) {
	client, err := kubernetes.NewForConfig(&rest.Config{Host: "https://example.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	previous := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(previous); getPermCache().Invalidate() })
	ctx := withTestUserPerms(t, "search-count", nil, nil)
	checks := 0
	stubSubjectCanI(t, func(_ context.Context, _ kubernetes.Interface, _ string, _ []string, namespace, group, resource, verb string) (bool, error) {
		checks++
		if namespace != "" {
			t.Errorf("cluster-allowed kind rechecked in %q", namespace)
		}
		return true, nil
	})
	namespaces := []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j"}
	for _, kind := range search.NamespacedSearchKinds {
		decision, scoped := mcpSearchKindRBAC(ctx, namespaces, kind.Group, kind.Resource)
		if decision != "" || scoped != nil {
			t.Fatalf("allowed kind: %s %v", decision, scoped)
		}
	}
	if checks != len(search.NamespacedSearchKinds) {
		t.Fatalf("SAR calls = %d, want one per sensitive kind (%d)", checks, len(search.NamespacedSearchKinds))
	}
}

func TestSearchKindRBACNamespaceCheckFailureRetainsAllowedScope(t *testing.T) {
	client, err := kubernetes.NewForConfig(&rest.Config{Host: "https://example.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	previous := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(previous); getPermCache().Invalidate() })
	ctx := withTestUserPerms(t, "search-check-error", nil, []string{"a", "b"})
	stubSubjectCanI(t, func(_ context.Context, _ kubernetes.Interface, _ string, _ []string, namespace, group, resource, verb string) (bool, error) {
		if namespace == "b" {
			return false, fmt.Errorf("SAR unavailable")
		}
		return namespace == "a", nil
	})
	decision, scoped := mcpSearchKindRBAC(ctx, []string{"a", "b"}, "", "secrets")
	if decision != "permission_check_failed" || len(scoped) != 1 || scoped[0] != "a" {
		t.Fatalf("SAR failure: %s %v", decision, scoped)
	}
	decision, _ = mcpSearchKindRBAC(ctx, []string{"a"}, "", "unavailable")
	if decision != "" {
		t.Fatalf("fully allowed narrower scope: %s", decision)
	}
}

func TestSearchKindRBACDeniedClusterCountsAndCachesNamespaceChecks(t *testing.T) {
	client, err := kubernetes.NewForConfig(&rest.Config{Host: "https://example.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	previous := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(previous); getPermCache().Invalidate() })
	namespaces := []string{"a", "b", "c", "denied"}
	ctx := withTestUserPerms(t, "search-denied-counts", nil, namespaces)
	var checks atomic.Int32
	stubSubjectCanI(t, func(_ context.Context, _ kubernetes.Interface, _ string, _ []string, namespace, group, resource, verb string) (bool, error) {
		checks.Add(1)
		return namespace != "" && namespace != "denied", nil
	})
	for range 2 {
		for _, kind := range search.NamespacedSearchKinds {
			decision, scoped := mcpSearchKindRBAC(ctx, namespaces, kind.Group, kind.Resource)
			if decision != "override" || len(scoped) != 3 {
				t.Fatalf("%s denied-cluster scope: %s %v", kind.Kind, decision, scoped)
			}
		}
		if got, want := checks.Load(), int32(len(search.NamespacedSearchKinds)*(len(namespaces)+1)); got != want {
			t.Fatalf("SAR calls = %d, want %d (one cluster check plus visible namespaces per sensitive kind, cached on repeat)", got, want)
		}
	}
}

func mcpSearch(t *testing.T, ctx context.Context, input searchInput) search.Result {
	t.Helper()
	input.Context, input.Include = "none", "none"
	result, _, err := handleSearch(ctx, nil, input)
	if err != nil {
		t.Fatal(err)
	}
	var body search.Result
	if err := json.Unmarshal([]byte(extractText(t, result)), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestHandleSearchClusterScopedForNamespaceRestrictedCaller(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	ctx := withRestrictedUser(t, "search-node-reader", []string{"alpha"})
	grantClusterRead(t, "search-node-reader", "/nodes")
	for _, input := range []searchInput{
		{Query: "kind:Node node"},
		{Query: "node"},
		{Query: "kind:Node node", Namespace: "alpha"},
	} {
		body := mcpSearch(t, ctx, input)
		nodes := 0
		for _, hit := range body.Hits {
			if hit.Kind == "Node" {
				nodes++
			}
		}
		if nodes != 2 {
			t.Fatalf("%+v: restricted caller with list nodes got %d nodes: %+v", input, nodes, body)
		}
	}
	body := mcpSearch(t, ctx, searchInput{Query: "node", Namespace: "alpha"})
	for _, hit := range body.Hits {
		if hit.Kind == "Node" {
			t.Fatalf("broad namespace-scoped search returned a Node: %+v", body.Hits)
		}
	}

	ctx = withRestrictedUser(t, "search-no-nodes", []string{"alpha"})
	denyClusterRead(t, "search-no-nodes", "/nodes")
	body = mcpSearch(t, ctx, searchInput{Query: "kind:Node node"})
	if len(body.Hits) != 0 || !slices.ContainsFunc(hintEntries(body.Coverage), func(gap hintEntry) bool {
		return gap.Kind == "Node" && gap.Reason == "user_forbidden"
	}) {
		t.Fatalf("caller without list nodes: %+v", body)
	}
}

func TestHandleSearchNamesExcludedNamespacesOnce(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	ctx := withRestrictedUser(t, "search-collapse", []string{"alpha"})
	body := mcpSearch(t, ctx, searchInput{Query: "pod ns:beta ns:gamma"})
	excluded := 0
	for _, gap := range hintEntries(body.Coverage) {
		if gap.Reason == "user_namespace_not_visible" {
			excluded++
			if gap.Kind != "*" || !slices.Equal(gap.Namespaces, []string{"beta", "gamma"}) {
				t.Fatalf("collapsed exclusion: %+v", gap)
			}
		}
	}
	if excluded != 1 || len(body.Hits) != 0 {
		t.Fatalf("broad exclusion entries = %d: %+v", excluded, body)
	}
}

// This fixture has no discovery, so a kind: term naming nothing typed may just
// not be discovered yet: the answer is "still loading", not "no such kind".
func TestHandleSearchWithoutDiscoveryIsPendingNotUnknown(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	ctx := withRestrictedUser(t, "search-loading", []string{"alpha"})
	body := mcpSearch(t, ctx, searchInput{Query: "kind:NoSuchKindXyz"})
	want := []hintEntry{{Kind: "*", Reason: "kind_sync_pending"}}
	if body.Coverage.Status != resourceapi.CoveragePartial || !reflect.DeepEqual(hintEntries(body.Coverage), want) {
		t.Fatalf("coverage before discovery = %+v, want %+v", hintEntries(body.Coverage), want)
	}
}

// coverageHint rides on every partial result, independently of narrowHint.
// (This fixture has no discovery, so every result here is partial; the REST/MCP
// parity test covers a complete result carrying no hint.)
func TestHandleSearchCoverageHintIsIndependentOfNarrowHint(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	for _, tc := range []struct {
		ctx        context.Context
		input      searchInput
		wantNarrow bool
	}{
		{withRestrictedUser(t, "search-hint", []string{"alpha"}), searchInput{Query: "pod ns:beta"}, false},
		{withClusterAdmin(t, "search-hint-admin"), searchInput{Query: "pod", Limit: 1}, true},
	} {
		input := tc.input
		input.Context, input.Include = "none", "none"
		result, _, err := handleSearch(tc.ctx, nil, input)
		if err != nil {
			t.Fatal(err)
		}
		var body searchResponseMCP
		if err := json.Unmarshal([]byte(extractText(t, result)), &body); err != nil {
			t.Fatal(err)
		}
		if body.Coverage.Status != resourceapi.CoveragePartial || body.CoverageHint == "" || (body.NarrowHint != "") != tc.wantNarrow {
			t.Fatalf("%+v: partial=%v coverageHint=%q narrowHint=%q", tc.input, (body.Coverage.Status == resourceapi.CoveragePartial), body.CoverageHint, body.NarrowHint)
		}
	}
	t.Cleanup(func() { getPermCache().Invalidate() })
}

func TestCoverageHintTemplates(t *testing.T) {
	gap := func(kind, group string, reason readreason.Code) hintEntry {
		return hintEntry{Kind: kind, Group: group, Reason: reason}
	}
	for _, tc := range []struct {
		name string
		res  hintResult
		want string
	}{
		{"pending fold", hintResult{Unsearched: []hintEntry{{Kind: "*", Reason: readreason.KindSyncPending, Count: 37}}},
			"37 kinds were still loading; retry in a few seconds before concluding absence."},
		{"pending catalog", hintResult{Unsearched: []hintEntry{{Kind: "*", Reason: readreason.KindSyncPending}}},
			"Some kinds were still loading; retry in a few seconds before concluding absence."},
		{"one failure", hintResult{Unsearched: []hintEntry{gap("Certificate", "cert-manager.io", readreason.KindSyncTimedOut)}},
			"Couldn't read Certificate (cert-manager.io); matches of that kind may be missing."},
		{"failures fold", hintResult{Unsearched: []hintEntry{
			{Kind: "*", Reason: readreason.ReadError, Count: 1}, gap("Certificate", "cert-manager.io", readreason.KindSyncTimedOut), gap("Issuer", "cert-manager.io", readreason.ReadError),
		}}, "Couldn't read 3 kinds (Certificate, Issuer, …); matches of those kinds may be missing. Retry with kind:Issuer for a kind you need before concluding absence."},
		// Kinds too many to name still get the way to re-check one.
		{"failures counted", hintResult{Unsearched: []hintEntry{{Kind: "*", Reason: readreason.ReadError, Count: 10}}},
			"Couldn't read 10 kinds; matches of those kinds may be missing. Retry with kind:<Kind> for a kind you need before concluding absence."},
		{"rbac", hintResult{Unsearched: []hintEntry{gap("Secret", "", readreason.UserForbidden)}},
			"Your access doesn't include all Secrets; this result says nothing about the rest."},
		{"rbac cluster-scoped", hintResult{Unsearched: []hintEntry{gap("Node", "", readreason.UserForbidden), gap("ClusterRole", "rbac.authorization.k8s.io", readreason.UserForbidden)}},
			"Your access doesn't include all Nodes or ClusterRoles; this result says nothing about the rest."},
		{"unknown", hintResult{Unsearched: []hintEntry{gap("Deploymnet", "", readreason.KindUnknown)}},
			"Kind 'Deploymnet' isn't recognized on this cluster: it may not be installed here, or the name may be misspelled."},
		{"unknown with undiscovered groups", hintResult{Unsearched: []hintEntry{{Kind: "*", Reason: readreason.ReadError}, gap("Certificate", "", readreason.KindUnknown)}},
			"Kind 'Certificate' wasn't recognized, but some API groups couldn't be discovered: it may be served by one of them; retry."},
		{"read directly", hintResult{Unsearched: []hintEntry{gap("Lease", "coordination.k8s.io", readreason.KindNotSearchable)}}, "Search doesn't cover Leases; use list_resources kind=Lease or get_resource."},
		{"unsupported", hintResult{Unsearched: []hintEntry{gap("PodMetrics", "metrics.k8s.io", readreason.KindNotWatchable)}}, "Radar can't collect PodMetrics: the API doesn't support both list and watch, so search can't cover them; read them from the Kubernetes API directly."},
		{"rbac namespace", hintResult{Unsearched: []hintEntry{{Kind: "*", Reason: readreason.UserNamespaceNotVisible, Namespaces: []string{"payments-prd"}}}},
			"Namespace payments-prd wasn't searched: it isn't among the namespaces you can see, or doesn't exist."},
		{"missing namespace", hintResult{Unsearched: []hintEntry{{Kind: "*", Reason: readreason.NamespaceNotFound, Namespaces: []string{"payments-prd"}}}},
			"Namespace 'payments-prd' doesn't exist on this cluster."},
		{"unknown kinds", hintResult{Unsearched: []hintEntry{gap("CiliumNetworkPolicy", "", readreason.KindUnknown), gap("Deploymnet", "", readreason.KindUnknown)}},
			"Kinds 'CiliumNetworkPolicy' and 'Deploymnet' aren't recognized on this cluster: they may not be installed here, or the names may be misspelled."},
		{"scope namespace", hintResult{Unsearched: []hintEntry{{Kind: "*", Reason: readreason.RadarNamespaceNotCollected, Namespaces: []string{"billing"}}}},
			"Namespace billing wasn't searched: Radar here watches only some namespaces."},
		{"check failed", hintResult{Unsearched: []hintEntry{gap("Secret", "", readreason.UserPermissionCheckFailed)}},
			"Radar couldn't check your access to Secret; retry."},
		{"collector", hintResult{Unsearched: []hintEntry{gap("Node", "", readreason.RadarForbidden)}},
			"Radar isn't allowed to read Nodes on this cluster; granting you access won't help."},
		{"not watched", hintResult{Unsearched: []hintEntry{gap("Role", "rbac.authorization.k8s.io", readreason.RadarKindNotCollected)}},
			"Radar doesn't watch Roles on this cluster, so they weren't searched."},
		{"filter", hintResult{FilterErrors: 143, FilterErrorSample: "no such key: readyReplicas"},
			"143 objects failed the filter (no such key: readyReplicas): Kubernetes omits fields that are 0 or empty, so guard them with has() and match a zero with kind == \"K\" && (!has(x) || x == 0)."},
		{"ordered and capped", hintResult{FilterErrors: 1, FilterErrorSample: "no such key: x", Unsearched: []hintEntry{
			gap("Secret", "", readreason.UserForbidden), {Kind: "*", Reason: readreason.KindSyncPending, Count: 2}, gap("Deploymnet", "", readreason.KindUnknown), gap("Node", "", readreason.RadarForbidden),
		}}, "Kind 'Deploymnet' isn't recognized on this cluster: it may not be installed here, or the name may be misspelled. 1 object failed the filter (no such key: x): Kubernetes omits fields that are 0 or empty, so guard them with has() and match a zero with kind == \"K\" && (!has(x) || x == 0). 2 kinds were still loading; retry in a few seconds before concluding absence. +2 more reasons."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := hintOf(tc.res, false); got != tc.want {
				t.Fatalf("coverageHint = %q\nwant           %q", got, tc.want)
			}
		})
	}
}

// Every reason search can report, and any it may report later, yields a
// sentence, so a partial result always carries a hint.
func TestCoverageHintCoversEveryReason(t *testing.T) {
	for _, reason := range append(readreason.Codes(), "reason_from_the_future") {
		res := hintResult{Partial: true, Unsearched: []hintEntry{{Kind: "Widget", Group: "example.io", Reason: reason, Namespaces: []string{"a"}}}}
		if hint := hintOf(res, false); hint == "" || strings.Contains(hint, "%!") {
			t.Fatalf("%s: hint %q", reason, hint)
		}
	}
}

// Without a Kubernetes client the caller's namespaces can't be discovered: the
// search fails closed and says the check failed rather than blaming the
// caller's RBAC.
func TestHandleSearchReportsFailedNamespaceDiscovery(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	getPermCache().Invalidate()
	t.Cleanup(func() { getPermCache().Invalidate() })
	ctx := pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: "search-undiscovered"})
	body := mcpSearch(t, ctx, searchInput{Query: "kind:Pod", Namespace: "alpha"})
	failed := slices.ContainsFunc(hintEntries(body.Coverage), func(gap hintEntry) bool {
		return gap.Kind == "Pod" && gap.Reason == "user_permission_check_failed"
	})
	excluded := slices.ContainsFunc(hintEntries(body.Coverage), func(gap hintEntry) bool { return gap.Reason == "namespace_excluded" })
	if body.Coverage.Status != resourceapi.CoveragePartial || !failed || excluded || len(body.Hits) != 0 {
		t.Fatalf("failed namespace discovery: %+v", body)
	}
}

// The --namespace-scope pin is Radar's setup, so a namespace outside it is
// namespace_scope_limited, never blamed on the caller's RBAC.
func TestHandleSearchAttributesNamedNamespacesToThePin(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	k8s.ForceNamespaceScope = true
	k8s.SetFallbackNamespace("alpha")
	t.Cleanup(func() {
		k8s.ForceNamespaceScope = false
		k8s.SetFallbackNamespace("")
		getPermCache().Invalidate()
	})
	ctx := withClusterAdmin(t, "search-pinned")
	body := mcpSearch(t, ctx, searchInput{Query: "kind:Pod", Namespace: "beta"})
	pinned := slices.ContainsFunc(hintEntries(body.Coverage), func(gap hintEntry) bool {
		return gap.Kind == "*" && gap.Reason == "outside_namespace_pin" && slices.Equal(gap.Namespaces, []string{"beta"})
	})
	if body.Coverage.Status != resourceapi.CoveragePartial || !pinned || len(body.Hits) != 0 {
		t.Fatalf("pinned namespace: %+v", body)
	}
}

// For a caller who can see every namespace, a named namespace that doesn't
// exist is reported, and the hint says exactly that; under the pin it is not
// blamed on the pin.
func TestHandleSearchNamedNamespaceThatDoesNotExist(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	t.Cleanup(func() { getPermCache().Invalidate() })
	for _, pinned := range []bool{false, true} {
		if pinned {
			k8s.ForceNamespaceScope = true
			k8s.SetFallbackNamespace("alpha")
			t.Cleanup(func() {
				k8s.ForceNamespaceScope = false
				k8s.SetFallbackNamespace("")
			})
		}
		ctx := withClusterAdmin(t, "search-missing-ns")
		result, _, err := handleSearch(ctx, nil, searchInput{Query: "pod", Namespace: "payments-prd", Context: "none", Include: "none"})
		if err != nil {
			t.Fatal(err)
		}
		var body searchResponseMCP
		if err := json.Unmarshal([]byte(extractText(t, result)), &body); err != nil {
			t.Fatal(err)
		}
		var namespaceEntries []hintEntry
		for _, gap := range hintEntries(body.Coverage) {
			if len(gap.Namespaces) > 0 {
				namespaceEntries = append(namespaceEntries, gap)
			}
		}
		want := []hintEntry{{Kind: "*", Reason: "namespace_not_found", Namespaces: []string{"payments-prd"}}}
		if !reflect.DeepEqual(namespaceEntries, want) || !strings.Contains(body.CoverageHint, "Namespace 'payments-prd' doesn't exist on this cluster.") {
			t.Fatalf("pinned=%v: %+v hint %q", pinned, namespaceEntries, body.CoverageHint)
		}
	}
}

func TestCoverageHintReadRecovery(t *testing.T) {
	for _, tc := range []struct {
		name, query string
		reason      readreason.Code
		want        string
	}{
		{"broad read error", "renewal", readreason.ReadError, "Couldn't read Certificate (cert-manager.io); matches of that kind may be missing. Retry with kind:Certificate before concluding absence."},
		{"named read error", "kind:Certificate renewal", readreason.ReadError, "Couldn't read Certificate (cert-manager.io); matches of that kind may be missing."},
		{"plural alias", "K:certificates renewal", readreason.ReadError, "Couldn't read Certificate (cert-manager.io); matches of that kind may be missing."},
		{"stalled watch", "renewal", readreason.KindSyncTimedOut, "Couldn't read Certificate (cert-manager.io); matches of that kind may be missing."},
		{"credentials rejected", "renewal", readreason.RadarCredentialsRejected, "Couldn't read Certificate (cert-manager.io); matches of that kind may be missing."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := hintResult{Partial: true, Unsearched: []hintEntry{{Kind: "Certificate", Group: "cert-manager.io", Reason: tc.reason}}}
			if got := hintOf(res, len(search.Parse(tc.query).KindFilter) > 0); got != tc.want {
				t.Fatalf("coverageHint = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestCoverageHintReadRecoveryFoldAndBudget(t *testing.T) {
	res := hintResult{Partial: true, Unsearched: []hintEntry{
		{Kind: "*", Reason: readreason.ReadError, Count: 4},
		{Kind: "Certificate", Group: "cert-manager.io", Reason: readreason.ReadError},
		{Kind: "Issuer", Group: "cert-manager.io", Reason: readreason.ReadError},
	}}
	want := "Couldn't read 6 kinds (Certificate, Issuer, …); matches of those kinds may be missing. Retry with kind:Certificate for a kind you need before concluding absence."
	if got := hintOf(res, false); got != want {
		t.Fatalf("fold: %q, want %q", got, want)
	}
	res.Unsearched = append(res.Unsearched,
		hintEntry{Kind: "*", Reason: readreason.KindSyncPending, Count: 1},
		hintEntry{Kind: "Secret", Reason: readreason.UserPermissionCheckFailed},
		hintEntry{Kind: "Node", Reason: readreason.RadarForbidden},
	)
	want = "1 kind was still loading; retry in a few seconds before concluding absence. Radar couldn't check your access to Secret; retry. Couldn't read 6 kinds (Certificate, Issuer, …); matches of those kinds may be missing; retry with kind:Certificate for a kind you need before concluding absence. +1 more reason."
	if got := hintOf(res, false); got != want {
		t.Fatalf("cap/order: %q, want %q", got, want)
	}
	res.Unsearched = res.Unsearched[:3]
	res.FilterErrors = 1
	want = filterSentence(1, "") + " Couldn't read 6 kinds (Certificate, Issuer, …); matches of those kinds may be missing; retry with kind:Certificate for a kind you need before concluding absence."
	if got := hintOf(res, false); got != want {
		t.Fatalf("filter advice budget: %q, want %q", got, want)
	}
	res.FilterErrors = 0
	res.Unsearched = []hintEntry{{Kind: "*", Reason: readreason.ReadError}}
	if got := hintOf(res, false); got != "Couldn't read some kinds; matches of those kinds may be missing." {
		t.Fatalf("catalog invented a kind: %q", got)
	}
}

func TestCoverageHintCapsNamedTargets(t *testing.T) {
	names := make([]string, 5000)
	entries := make([]hintEntry, len(names))
	for i := range names {
		names[i] = fmt.Sprintf("target-%04d", i)
		entries[i] = hintEntry{Kind: names[i], Reason: readreason.KindUnknown}
	}
	var cases []hintResult
	for _, reason := range []readreason.Code{readreason.KindUnknown, readreason.UserForbidden, readreason.RadarForbidden, readreason.KindNotWatchable} {
		gaps := slices.Clone(entries)
		for i := range gaps {
			gaps[i].Reason = reason
		}
		cases = append(cases, hintResult{Partial: true, Unsearched: gaps})
	}
	for _, reason := range []readreason.Code{readreason.UserNamespaceNotVisible, readreason.OutsideNamespaceSelection, readreason.OutsideNamespacePin, readreason.RadarNamespaceNotCollected} {
		cases = append(cases, hintResult{Partial: true, Unsearched: []hintEntry{{Kind: "*", Reason: reason, Namespaces: names}}})
	}
	for _, res := range cases {
		hint := hintOf(res, true)
		if !strings.Contains(hint, "and 4995 more") || strings.Contains(hint, names[5]) {
			t.Fatalf("unbounded hint: %s", hint)
		}
		if len(hint) > 500 {
			t.Fatalf("hint length: %d", len(hint))
		}
	}
	if len(entries) != 5000 || len(names) != 5000 || entries[4999].Kind != names[4999] {
		t.Fatal("structured identities changed")
	}
}

func TestCoverageHintCapsFilterDiagnostic(t *testing.T) {
	for _, sample := range []string{strings.Repeat("x", 10000), strings.Repeat("界", 10000), "no such key: " + strings.Repeat("x", 10000)} {
		res := hintResult{Partial: true, FilterErrors: 1, FilterErrorSample: sample}
		hint := hintOf(res, false)
		if !utf8.ValidString(hint) || !strings.Contains(hint, "…") || len(hint) > 350 {
			t.Fatalf("diagnostic excerpt (%d bytes): %q", len(hint), hint)
		}
		if res.FilterErrorSample != sample {
			t.Fatal("full diagnostic changed")
		}
	}
}

// hintResult is a search result's coverage as the hint reads it, so the
// sentence templates can be tested entry by entry.
type hintResult struct {
	Partial           bool
	Unsearched        []hintEntry
	FilterErrors      int
	FilterErrorSample string
}

func hintOf(r hintResult, namedKinds bool) string {
	return entriesHint(r.Unsearched, r.FilterErrors, r.FilterErrorSample, namedKinds)
}
