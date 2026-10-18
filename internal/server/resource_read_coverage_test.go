package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"
)

type testResourceListEnvelope struct {
	Items    []map[string]any     `json:"items"`
	Coverage resourceapi.Coverage `json:"coverage"`
	Kind     string               `json:"kind"`
	Group    string               `json:"group"`
	Columns  []printerColumn      `json:"columns"`
	Cells    map[string][]any     `json:"cells"`
}

func assertResourceReadError(t *testing.T, resp *http.Response, status int, reason string) {
	t.Helper()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]json.RawMessage
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("invalid error response: %s (%v)", body, err)
	}
	var code, message string
	if err := json.Unmarshal(result["error_code"], &code); err != nil {
		t.Fatalf("missing error_code: %s", body)
	}
	if err := json.Unmarshal(result["error"], &message); err != nil {
		t.Fatalf("missing error: %s", body)
	}
	if resp.StatusCode != status || code != reason || message == "" {
		t.Fatalf("status/code = %d/%q, want %d/%q: %s", resp.StatusCode, code, status, reason, body)
	}
	for _, field := range []string{"items", "columns", "cells"} {
		if _, exists := result[field]; exists {
			t.Fatalf("failed read exposed %s: %s", field, body)
		}
	}
	// A failed read may say what it covered and why, never that it read any of it.
	if raw, exists := result["coverage"]; exists {
		var coverage resourceapi.Coverage
		if err := json.Unmarshal(raw, &coverage); err != nil || coverage.Read != nil || coverage.Status != resourceapi.CoveragePartial || len(coverage.Gaps) == 0 {
			t.Fatalf("failed read's coverage claims a read: %s", body)
		}
	}
}

func TestResourceListCoverageDistinguishesEmptyAndUnread(t *testing.T) {
	for _, populated := range []bool{false, true} {
		t.Run(fmt.Sprintf("populated=%v", populated), func(t *testing.T) {
			var objects []runtime.Object
			if populated {
				objects = append(objects, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "seen", Namespace: "alpha"}})
			}
			client := fake.NewClientset(objects...)
			useTestResourceCache(t, client)
			k8s.ResetResourceCache()
			if err := k8s.InitScopedTestResourceCacheNamespaces(client, map[string]k8score.ResourceScope{k8score.Pods: {Enabled: true, Namespace: "alpha"}}, map[string][]string{k8score.Pods: {"alpha"}}); err != nil {
				t.Fatal(err)
			}
			for _, tc := range []struct {
				query, requestedType string
				partial              bool
				gapNames             []string
			}{
				{"?namespace=alpha", "namespaces", false, nil},
				{"?namespaces=alpha,beta", "namespaces", true, []string{"beta"}},
				// An unscoped list is narrowed to what Radar collects, and
				// complete within it.
				{"", "namespaces", false, nil},
				{"?namespaces=alpha,beta&table=1", "namespaces", true, []string{"beta"}},
			} {
				t.Run(tc.query, func(t *testing.T) {
					var body testResourceListEnvelope
					assertOK(t, get(t, "/api/resources/pods"+tc.query), &body)
					wantRows := 0
					if populated {
						wantRows = 1
					}
					if body.Items == nil || len(body.Items) != wantRows {
						t.Fatalf("items = %+v, want non-null %d rows", body.Items, wantRows)
					}
					wantStatus := resourceapi.CoverageComplete
					if tc.partial {
						wantStatus = resourceapi.CoveragePartial
					}
					if body.Coverage.Status != wantStatus || body.Coverage.Scope.Type != resourceapi.ScopeType(tc.requestedType) {
						t.Fatalf("coverage = %+v", body.Coverage)
					}
					if narrowed := slices.Equal(body.Coverage.NarrowedBy, []resourceapi.Narrowing{resourceapi.NarrowedByNamespaceCollection}); narrowed != (tc.query == "") {
						t.Fatalf("narrowedBy = %v", body.Coverage.NarrowedBy)
					}
					if body.Coverage.Read.Namespaces == nil || !slices.Equal(body.Coverage.Read.Namespaces.Names, []string{"alpha"}) {
						t.Fatalf("read scope = %+v", body.Coverage.Read)
					}
					if tc.partial {
						if len(body.Coverage.Gaps) != 1 || body.Coverage.Gaps[0].Code != "radar_namespace_not_collected" {
							t.Fatalf("gaps = %+v", body.Coverage.Gaps)
						}
						gap := body.Coverage.Gaps[0].Namespaces
						if tc.gapNames == nil && gap != nil {
							t.Fatalf("invented unread universe: %+v", gap)
						}
						if tc.gapNames != nil && (gap == nil || !slices.Equal(gap.Names, tc.gapNames)) {
							t.Fatalf("missing unread namespace: %+v", gap)
						}
					} else if len(body.Coverage.Gaps) != 0 {
						t.Fatalf("complete read has gaps: %+v", body.Coverage.Gaps)
					}
					if body.Columns != nil || body.Cells != nil {
						t.Fatalf("typed kind unexpectedly has printer metadata: %+v", body)
					}
				})
			}
			resp := get(t, "/api/resources/pods?namespace=beta")
			defer resp.Body.Close()
			assertResourceReadError(t, resp, 409, "radar_namespace_not_collected")
		})
	}
}

func TestResourceGetCoverageBeforeNotFound(t *testing.T) {
	client := fake.NewClientset()
	useTestResourceCache(t, client)
	k8s.ResetResourceCache()
	if err := k8s.InitScopedTestResourceCache(client, map[string]k8score.ResourceScope{k8score.Pods: {Enabled: true, Namespace: "alpha"}}); err != nil {
		t.Fatal(err)
	}
	resp := get(t, "/api/resources/pods/beta/missing")
	defer resp.Body.Close()
	assertResourceReadError(t, resp, 409, "radar_namespace_not_collected")
	resp = get(t, "/api/resources/pods/alpha/missing")
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("covered missing object should be 404, got %d", resp.StatusCode)
	}
}

func TestResourceMissingCollectorInformerIsNotEmptyOrNotFound(t *testing.T) {
	client := fake.NewClientset()
	useTestResourceCache(t, client)
	k8s.ResetResourceCache()
	if err := k8s.InitScopedTestResourceCache(client, map[string]k8score.ResourceScope{k8score.Services: {Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/resources/pods", "/api/resources/pods/alpha/missing"} {
		t.Run(path, func(t *testing.T) {
			resp := get(t, path)
			defer resp.Body.Close()
			assertResourceReadError(t, resp, 503, "radar_kind_not_collected")
		})
	}
}

func TestAIResourceReadKeepsArrayAndRejectsIncompleteScope(t *testing.T) {
	client := fake.NewClientset()
	useTestResourceCache(t, client)
	k8s.ResetResourceCache()
	if err := k8s.InitScopedTestResourceCache(client, map[string]k8score.ResourceScope{k8score.Pods: {Enabled: true, Namespace: "alpha"}}); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	assertOK(t, get(t, "/api/ai/resources/pods?namespace=alpha"), &rows)
	if rows == nil || len(rows) != 0 {
		t.Fatalf("AI finite complete empty response should remain [], got %+v", rows)
	}
	for _, path := range []string{
		"/api/ai/resources/pods?namespaces=alpha,beta",
		"/api/ai/resources/pods?namespace=beta",
		"/api/ai/resources/pods/beta/missing",
	} {
		t.Run(path, func(t *testing.T) {
			resp := get(t, path)
			defer resp.Body.Close()
			assertResourceReadError(t, resp, 409, "radar_namespace_not_collected")
		})
	}
}

// A namespace Radar collects but the caller can't see reads exactly like one
// that doesn't exist: the caller gets back the name they sent, as a
// namespace_not_visible gap, and nothing that tells the two apart.
func TestResourceListCoverageContainsOnlyCallerVisibleNamespaces(t *testing.T) {
	client := fake.NewClientset()
	useTestResourceCache(t, client)
	k8s.ResetResourceCache()
	if err := k8s.InitScopedTestResourceCacheNamespaces(client, map[string]k8score.ResourceScope{k8score.Pods: {Enabled: true, Namespace: "alpha"}}, map[string][]string{k8score.Pods: {"alpha", "hidden-collector-namespace"}}); err != nil {
		t.Fatal(err)
	}
	env := newAuthTestServer(t)
	env.srv.permCache.Set("reader", nil, &auth.UserPermissions{AllowedNamespaces: []string{"alpha", "beta"}})
	read := func(named string) string {
		t.Helper()
		resp := env.authGet(t, "/api/resources/pods?namespaces=alpha,beta,"+named+"&table=1", "reader", "")
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Fatalf("status = %d: %s", resp.StatusCode, body)
		}
		return string(body)
	}
	collected, missing := read("hidden-collector-namespace"), read("zz-no-such-namespace")
	if strings.ReplaceAll(collected, "hidden-collector-namespace", "zz-no-such-namespace") != missing {
		t.Fatalf("a collected namespace the caller can't see must read like a missing one:\n%s\n%s", collected, missing)
	}
	var result testResourceListEnvelope
	if err := json.Unmarshal([]byte(collected), &result); err != nil {
		t.Fatal(err)
	}
	hidden := slices.IndexFunc(result.Coverage.Gaps, func(gap resourceapi.Gap) bool { return gap.Code == readreason.UserNamespaceNotVisible })
	if result.Coverage.Status != resourceapi.CoveragePartial || result.Coverage.Scope.Namespaces.Count != 3 || result.Coverage.Read.Namespaces.Count != 1 ||
		hidden < 0 || !slices.Equal(result.Coverage.Gaps[hidden].Namespaces.Names, []string{"hidden-collector-namespace"}) {
		t.Fatalf("coverage = %+v", result.Coverage)
	}
	resp := env.authGet(t, "/api/resources/pods?namespace=hidden-collector-namespace&table=1", "reader", "")
	defer resp.Body.Close()
	assertResourceReadError(t, resp, 403, "user_namespace_not_visible")
}

func TestResourceCoverageNamespaceMetadataIsBounded(t *testing.T) {
	names := make([]string, 80)
	for i := range names {
		names[i] = fmt.Sprintf("ns-%03d", i)
	}
	client := fake.NewClientset()
	useTestResourceCache(t, client)
	k8s.ResetResourceCache()
	if err := k8s.InitScopedTestResourceCacheNamespaces(client, map[string]k8score.ResourceScope{k8score.Pods: {Enabled: true, Namespace: names[0]}}, map[string][]string{k8score.Pods: names[:70]}); err != nil {
		t.Fatal(err)
	}
	var body testResourceListEnvelope
	assertOK(t, get(t, "/api/resources/pods?namespaces="+strings.Join(names, ",")), &body)
	// Past the limit a set only counts its members.
	for _, tc := range []struct {
		set   *resourceapi.NameSet
		total int
	}{{body.Coverage.Scope.Namespaces, 80}, {body.Coverage.Read.Namespaces, 70}} {
		if tc.set == nil || tc.set.Names != nil || tc.set.Count != tc.total {
			t.Fatalf("set = %+v, want a count of %d and no names", tc.set, tc.total)
		}
	}
	if body.Coverage.Gaps[0].Namespaces.Count != 10 || len(body.Coverage.Gaps[0].Namespaces.Names) != 10 {
		t.Fatalf("gap = %+v", body.Coverage.Gaps)
	}
}

func TestResourceListRejectsInvalidLimits(t *testing.T) {
	for _, value := range []string{"0", "-1", "50001", "x", "1.5", "99999999999999999999"} {
		t.Run(value, func(t *testing.T) {
			resp := get(t, "/api/resources/pods?maxItems="+value)
			defer resp.Body.Close()
			if resp.StatusCode != 400 {
				t.Fatalf("invalid maxItems returned %d", resp.StatusCode)
			}
		})
	}
}

func TestResourceListChecksCurrentSizeAfterCountGrowth(t *testing.T) {
	client := fake.NewClientset(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "initial", Namespace: "alpha"}})
	useTestResourceCache(t, client)
	var before ResourceCountsResponse
	assertOK(t, get(t, "/api/resource-counts?namespace=alpha"), &before)
	if before.Counts["Pod"] != 1 {
		t.Fatalf("initial count = %d", before.Counts["Pod"])
	}
	for _, name := range []string{"new-1", "new-2"} {
		if _, err := client.CoreV1().Pods("alpha").Create(context.Background(), &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name}}, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	deadline := time.Now().Add(3 * time.Second)
	for typedListCount(k8s.GetResourceCache(), k8score.Pods, []string{"alpha"}) != 3 {
		if time.Now().After(deadline) {
			t.Fatal("new pods did not reach cache")
		}
		time.Sleep(10 * time.Millisecond)
	}
	resp := get(t, "/api/resources/pods?namespace=alpha&maxItems=2")
	defer resp.Body.Close()
	assertResourceReadError(t, resp, 400, "list_too_large")
	var exact testResourceListEnvelope
	assertOK(t, get(t, "/api/resources/pods?namespace=alpha&maxItems=3"), &exact)
	if len(exact.Items) != 3 || exact.Coverage.Status != resourceapi.CoverageComplete {
		t.Fatalf("exact-limit read = %+v", exact)
	}
}

func TestResourceCountsKeepPartialScopeCountUsable(t *testing.T) {
	client := fake.NewClientset(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "seen", Namespace: "alpha"}})
	useTestResourceCache(t, client)
	k8s.ResetResourceCache()
	if err := k8s.InitScopedTestResourceCache(client, map[string]k8score.ResourceScope{k8score.Pods: {Enabled: true, Namespace: "alpha"}}); err != nil {
		t.Fatal(err)
	}
	var counts ResourceCountsResponse
	assertOK(t, get(t, "/api/resource-counts?namespaces=alpha,beta"), &counts)
	if counts.Counts["Pod"] != 1 || counts.Coverage["Pod"].Status != resourceapi.CoveragePartial || containsString(counts.Unavailable, "Pod") || containsString(counts.Forbidden, "Pod") {
		t.Fatalf("partial count should remain usable: %+v", counts)
	}
	var unread ResourceCountsResponse
	assertOK(t, get(t, "/api/resource-counts?namespace=beta"), &unread)
	if _, exists := unread.Counts["Pod"]; exists || !containsString(unread.Unavailable, "Pod") || unread.Reasons["Pod"] != "radar_namespace_not_collected" {
		t.Fatalf("unread count became zero: %+v", unread)
	}
}

func TestResourceCountsShareMetadataBudgetAcrossKinds(t *testing.T) {
	useTestResourceCache(t, fake.NewClientset())
	names := make([]string, 80)
	for i := range names {
		names[i] = fmt.Sprintf("ns-%03d", i)
	}
	var counts ResourceCountsResponse
	assertOK(t, get(t, "/api/resource-counts?namespaces="+strings.Join(names, ",")), &counts)
	if len(counts.Counts) < 20 || len(counts.Coverage) != len(counts.Counts) {
		t.Fatalf("expected coverage for every normal count: %+v", counts)
	}
	previewNames := 0
	for key, coverage := range counts.Coverage {
		if coverage.Status != resourceapi.CoverageComplete {
			t.Fatalf("empty complete cache count %s misclassified: %+v", key, coverage)
		}
		sets := []*resourceapi.NameSet{coverage.Scope.Namespaces, coverage.Read.Namespaces}
		for _, gap := range coverage.Gaps {
			sets = append(sets, gap.Namespaces)
		}
		for _, set := range sets {
			if set == nil {
				continue
			}
			if set.Count != 80 || len(set.Names) > 64 {
				t.Fatalf("%s invalid metadata preview: %+v", key, set)
			}
			previewNames += len(set.Names)
		}
	}
	if previewNames > 2048 {
		t.Fatalf("count coverage repeated %d namespace names, want at most 2048 across kinds", previewNames)
	}
	var again ResourceCountsResponse
	assertOK(t, get(t, "/api/resource-counts?namespaces="+strings.Join(names, ",")), &again)
	firstJSON, err := json.Marshal(counts.Coverage)
	if err != nil {
		t.Fatal(err)
	}
	againJSON, err := json.Marshal(again.Coverage)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstJSON) != string(againJSON) {
		t.Fatal("metadata budget allocation varies across identical count snapshots")
	}
}

func TestResourceNamespaceCountMatchesAuthorizedClusterScopedList(t *testing.T) {
	useTestResourceCache(t, fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "alpha"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "beta"}},
	))
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"alpha"}}
	perms.SetCanI("list", "", "namespaces", "", true)
	env.srv.permCache.Set("namespace-reader", nil, perms)
	var list testResourceListEnvelope
	assertOK(t, env.authGet(t, "/api/resources/namespaces?namespace=alpha", "namespace-reader", ""), &list)
	var counts ResourceCountsResponse
	assertOK(t, env.authGet(t, "/api/resource-counts?namespace=alpha", "namespace-reader", ""), &counts)
	if len(list.Items) != 2 || counts.Counts["Namespace"] != 2 || counts.Coverage["Namespace"].Scope.Type != resourceapi.ScopeCluster || counts.Coverage["Namespace"].Status != resourceapi.CoverageComplete {
		t.Fatalf("authorized Namespace count/list disagree: list=%+v counts=%+v", list, counts)
	}
	denied := &auth.UserPermissions{AllowedNamespaces: []string{"alpha"}}
	denied.SetCanI("list", "", "namespaces", "", false)
	env.srv.permCache.Set("resource-reader", nil, denied)
	var restricted ResourceCountsResponse
	assertOK(t, env.authGet(t, "/api/resource-counts?namespace=alpha", "resource-reader", ""), &restricted)
	if _, exists := restricted.Counts["Namespace"]; exists || restricted.Reasons["Namespace"] != "rbac_denied" {
		t.Fatalf("namespace-level resource grants exposed Namespace objects/count: %+v", restricted)
	}
}

func TestResourceCountsHonorSpecialNamespacedListGates(t *testing.T) {
	useTestResourceCache(t, fake.NewClientset(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "visible", Namespace: "alpha"}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "hidden", Namespace: "beta"}},
		&corev1.LimitRange{ObjectMeta: metav1.ObjectMeta{Name: "rules", Namespace: "alpha"}},
	))
	env := newAuthTestServer(t)
	denied := &auth.UserPermissions{AllowedNamespaces: []string{"alpha", "beta"}}
	for _, namespace := range []string{"alpha", "beta"} {
		denied.SetCanI("list", "", "secrets", namespace, false)
		denied.SetCanI("list", "", "limitranges", namespace, false)
	}
	env.srv.permCache.Set("denied", nil, denied)
	var deniedCounts ResourceCountsResponse
	assertOK(t, env.authGet(t, "/api/resource-counts?namespaces=alpha,beta", "denied", ""), &deniedCounts)
	for _, key := range []string{"Secret", "LimitRange"} {
		if _, exists := deniedCounts.Counts[key]; exists || deniedCounts.Reasons[key] != "rbac_denied" {
			t.Fatalf("denied %s exposed count: %+v", key, deniedCounts)
		}
		if _, exists := deniedCounts.Coverage[key]; exists {
			t.Fatalf("denied %s exposed collector coverage: %+v", key, deniedCounts.Coverage[key])
		}
	}
	partial := &auth.UserPermissions{AllowedNamespaces: []string{"alpha", "beta"}}
	partial.SetCanI("list", "", "secrets", "alpha", true)
	partial.SetCanI("list", "", "secrets", "beta", false)
	partial.SetCanI("list", "", "limitranges", "alpha", true)
	partial.SetCanI("list", "", "limitranges", "beta", false)
	env.srv.permCache.Set("partial", nil, partial)
	var counts ResourceCountsResponse
	assertOK(t, env.authGet(t, "/api/resource-counts?namespaces=alpha,beta", "partial", ""), &counts)
	var list testResourceListEnvelope
	assertOK(t, env.authGet(t, "/api/resources/secrets?namespaces=alpha,beta", "partial", ""), &list)
	coverage := counts.Coverage["Secret"]
	if counts.Counts["Secret"] != len(list.Items) || counts.Counts["Secret"] != 1 || coverage.Status != resourceapi.CoveragePartial || coverage.Scope.Namespaces == nil || !slices.Equal(coverage.Scope.Namespaces.Names, []string{"alpha", "beta"}) || coverage.Read.Namespaces == nil || !slices.Equal(coverage.Read.Namespaces.Names, []string{"alpha"}) || len(coverage.Gaps) != 1 || coverage.Gaps[0].Code != "user_forbidden" || coverage.Gaps[0].Namespaces == nil || !slices.Equal(coverage.Gaps[0].Namespaces.Names, []string{"beta"}) {
		t.Fatalf("Secret count did not use list's authorized scope: counts=%+v list=%+v", counts, list)
	}
	// LimitRanges are narrowed like Secrets: counted where the caller may
	// list them, with the rest a gap.
	limits := counts.Coverage["LimitRange"]
	if counts.Counts["LimitRange"] != 1 || limits.Status != resourceapi.CoveragePartial || len(limits.Gaps) != 1 || limits.Gaps[0].Code != "user_forbidden" || !slices.Equal(limits.Gaps[0].Namespaces.Names, []string{"beta"}) {
		t.Fatalf("LimitRange count did not use list's authorized scope: %+v %+v", counts.Counts["LimitRange"], limits)
	}
}

func TestResourceCountsAllowExplicitClusterGrantWithoutNamespaceAccess(t *testing.T) {
	useTestResourceCache(t, fake.NewClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker"}}))
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{}}
	perms.SetCanI("list", "", "nodes", "", true)
	env.srv.permCache.Set("node-reader", nil, perms)
	var list testResourceListEnvelope
	assertOK(t, env.authGet(t, "/api/resources/nodes", "node-reader", ""), &list)
	var counts ResourceCountsResponse
	assertOK(t, env.authGet(t, "/api/resource-counts", "node-reader", ""), &counts)
	if len(list.Items) != 1 || counts.Counts["Node"] != 1 || counts.Coverage["Node"].Status != resourceapi.CoverageComplete {
		t.Fatalf("explicit cluster grant was lost by namespace-count gate: list=%+v counts=%+v", list, counts)
	}
	if _, exists := counts.Counts["Pod"]; exists {
		t.Fatalf("node grant implied namespace access: %+v", counts)
	}
}

func TestResourceDynamicNamespaceCoverageDoesNotBorrowAnotherInformer(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList", {Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}: "EndpointSliceList"})
	dyn.PrependReactor("list", "widgets", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetNamespace() != "alpha" {
			return true, nil, apierrors.NewForbidden(gvr.GroupResource(), "", fmt.Errorf("denied"))
		}
		return true, &unstructured.UnstructuredList{}, nil
	})
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "Widget", Name: gvr.Resource, Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	cache := k8s.GetDynamicResourceCache()
	if err := cache.EnsureWatchingContext(context.Background(), gvr, "alpha"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for !cache.IsNamespaceSynced(gvr, "alpha") {
		if time.Now().After(deadline) {
			t.Fatal("alpha informer did not sync")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var result testResourceListEnvelope
	assertOK(t, get(t, "/api/resources/widgets?group=example.com&namespaces=alpha,beta"), &result)
	if result.Coverage.Status != resourceapi.CoveragePartial || len(result.Items) != 0 || result.Items == nil || len(result.Coverage.Gaps) != 1 || result.Coverage.Gaps[0].Code != "radar_forbidden" {
		t.Fatalf("dynamic empty partial = %+v", result)
	}
	resp := get(t, "/api/resources/widgets/beta/missing?group=example.com")
	defer resp.Body.Close()
	assertResourceReadError(t, resp, 503, "radar_forbidden")
	resp = get(t, "/api/resources/widgets/alpha/missing?group=example.com")
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("covered CRD not found = %d", resp.StatusCode)
	}

	rec := httptest.NewRecorder()
	testServerSrv.handleResourceCounts(rec, httptest.NewRequest("GET", "/api/resource-counts?namespaces=alpha,beta", nil))
	var counts ResourceCountsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &counts); err != nil {
		t.Fatal(err)
	}
	if n, exists := counts.Counts["example.com/Widget"]; !exists || n != 0 || counts.Coverage["example.com/Widget"].Status != resourceapi.CoveragePartial {
		t.Fatalf("partial empty dynamic count = %+v", counts)
	}
}

func TestResourceDynamicPendingEmptyDoesNotBecomeAbsence(t *testing.T) {
	release := make(chan struct{})
	var releaseOnce sync.Once
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apis/example.com/v1/widgets" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("watch") == "true" {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"type": "BOOKMARK", "object": map[string]any{"apiVersion": "example.com/v1", "kind": "Widget", "metadata": map[string]any{"resourceVersion": "1", "annotations": map[string]any{"k8s.io/initial-events-end": "true"}}}})
			w.(http.Flusher).Flush()
			<-r.Context().Done()
			return
		}
		if r.URL.Query().Get("limit") != "1" {
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"apiVersion": "example.com/v1", "kind": "WidgetList", "metadata": map[string]any{"resourceVersion": "1"}, "items": []any{}})
	}))
	t.Cleanup(api.Close)
	dyn, err := dynamic.NewForConfig(&rest.Config{Host: api.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: "example.com", Version: "v1", Kind: "Widget", Name: "widgets", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	for _, path := range []string{"/api/resources/widgets?group=example.com&namespace=alpha", "/api/resources/widgets/alpha/missing?group=example.com"} {
		resp := get(t, path)
		assertResourceReadError(t, resp, 503, "kind_sync_pending")
		resp.Body.Close()
	}
	releaseOnce.Do(func() { close(release) })
	deadline := time.Now().Add(3 * time.Second)
	for {
		status, _ := gateProbe(t, "/api/resources/widgets?group=example.com&namespace=alpha")
		if status == 200 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("released empty informer did not become readable")
		}
		time.Sleep(10 * time.Millisecond)
	}
	var result testResourceListEnvelope
	assertOK(t, get(t, "/api/resources/widgets?group=example.com&namespace=alpha"), &result)
	if result.Items == nil || len(result.Items) != 0 || result.Coverage.Status != resourceapi.CoverageComplete {
		t.Fatalf("synced empty scope should be complete: %+v", result)
	}
	resp := get(t, "/api/resources/widgets/alpha/missing?group=example.com")
	defer resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("synced empty GET must establish absence: %d", resp.StatusCode)
	}
}

func TestResourceListGroupCollisionKeepsRowsAndCoverageTogether(t *testing.T) {
	client := fake.NewClientset(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "core-service", Namespace: "alpha"}})
	useTestResourceCache(t, client)
	k8s.ResetResourceCache()
	if err := k8s.InitScopedTestResourceCache(client, map[string]k8score.ResourceScope{k8score.Services: {Enabled: true, Namespace: "alpha"}}); err != nil {
		t.Fatal(err)
	}
	gvr := schema.GroupVersionResource{Group: "serving.knative.dev", Version: "v1", Resource: "services"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "ServiceList"}, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "serving.knative.dev/v1", "kind": "Service", "metadata": map[string]any{"name": "knative-service", "namespace": "beta"},
	}})
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "Service", Name: gvr.Resource, Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	cache := k8s.GetDynamicResourceCache()
	if err := cache.EnsureWatching(gvr); err != nil {
		t.Fatal(err)
	}
	if !cache.WaitForSync(gvr, 3*time.Second) {
		t.Fatal("Knative Service informer did not sync")
	}
	var core testResourceListEnvelope
	assertOK(t, get(t, "/api/resources/Service?namespace=alpha"), &core)
	if len(core.Items) != 1 || mustNested(t, core.Items[0], "metadata", "name") != "core-service" || core.Coverage.Read.Namespaces.Count != 1 || core.Coverage.Status != resourceapi.CoverageComplete {
		t.Fatalf("core Service read = %+v", core)
	}
	var knative testResourceListEnvelope
	assertOK(t, get(t, "/api/resources/Services?group=serving.knative.dev&namespace=beta&table=1"), &knative)
	if len(knative.Items) != 1 || mustNested(t, knative.Items[0], "metadata", "name") != "knative-service" || knative.Items[0]["apiVersion"] != "serving.knative.dev/v1" || knative.Kind != "services" || knative.Group != gvr.Group || knative.Coverage.Status != resourceapi.CoverageComplete || !slices.Equal(knative.Coverage.Read.Namespaces.Names, []string{"beta"}) {
		t.Fatalf("group-qualified Service read borrowed core data/scope: %+v", knative)
	}
}

func TestDirectResourceListFollowsSmallPagesWithinBudget(t *testing.T) {
	for _, limit := range []int{2, 3} {
		t.Run(fmt.Sprintf("maxItems=%d", limit), func(t *testing.T) {
			var mu sync.Mutex
			var continuations []string
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/apis/discovery.k8s.io/v1/namespaces/alpha/endpointslices" {
					t.Errorf("unexpected API path: %s", r.URL.Path)
					http.NotFound(w, r)
					return
				}
				if r.URL.Query().Get("limit") == "" {
					t.Error("direct list must bound each API page")
				}
				continuation := r.URL.Query().Get("continue")
				mu.Lock()
				continuations = append(continuations, continuation)
				mu.Unlock()
				index := 0
				switch continuation {
				case "":
					index = 0
				case "page-2":
					index = 1
				case "page-3":
					index = 2
				default:
					t.Errorf("unexpected continue token %q", continuation)
				}
				next := ""
				if index < 2 {
					next = fmt.Sprintf("page-%d", index+2)
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{
					"apiVersion": "discovery.k8s.io/v1", "kind": "EndpointSliceList",
					"metadata": map[string]any{"continue": next, "resourceVersion": "123"},
					"items":    []any{map[string]any{"apiVersion": "discovery.k8s.io/v1", "kind": "EndpointSlice", "metadata": map[string]any{"name": fmt.Sprintf("work-%d", index), "namespace": "alpha"}}},
				})
			}))
			t.Cleanup(api.Close)
			dyn, err := dynamic.NewForConfig(&rest.Config{Host: api.URL})
			if err != nil {
				t.Fatal(err)
			}
			if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: "discovery.k8s.io", Version: "v1", Kind: "EndpointSlice", Name: "endpointslices", Namespaced: true, Verbs: []string{"get", "list"}}}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(k8s.ResetTestDynamicState)
			resp := get(t, fmt.Sprintf("/api/resources/endpointslices?group=discovery.k8s.io&namespace=alpha&maxItems=%d", limit))
			defer resp.Body.Close()
			if limit == 2 {
				assertResourceReadError(t, resp, 400, "list_too_large")
			} else {
				var result testResourceListEnvelope
				if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
					t.Fatal(err)
				}
				if resp.StatusCode != 200 || len(result.Items) != 3 || result.Coverage.Status != resourceapi.CoverageComplete {
					t.Fatalf("under-budget paginated list = %d %+v", resp.StatusCode, result)
				}
			}
			mu.Lock()
			defer mu.Unlock()
			if !slices.Equal(continuations, []string{"", "page-2", "page-3"}) {
				t.Fatalf("continue tokens = %v; small pages must be read until the budget or end", continuations)
			}
		})
	}
}

func TestUnsupportedDynamicListKeepsItsCollectionCause(t *testing.T) {
	useTestResourceCache(t, fake.NewClientset())
	gvr := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "summaries"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "SummaryList"})
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "Summary", Name: gvr.Resource, Namespaced: true, Verbs: []string{"get", "list"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	before := len(dyn.Actions())
	resp := get(t, "/api/resources/summaries?group=example.com&namespace=alpha")
	defer resp.Body.Close()
	assertResourceReadError(t, resp, 400, "kind_not_watchable")
	if len(dyn.Actions()) != before {
		t.Fatal("unsupported collection attempted a Kubernetes read")
	}
}

func TestCancelledDirectNamespaceReadIsNotInformerLoading(t *testing.T) {
	useTestResourceCache(t, fake.NewClientset())
	gvr := schema.GroupVersionResource{Group: "coordination.k8s.io", Version: "v1", Resource: "leases"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "LeaseList"})
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "Lease", Name: gvr.Resource, Namespaced: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest("GET", "/api/resources/leases?namespace=alpha", nil).WithContext(ctx)
	_, err := readDynamicResourceList(req, k8s.GetResourceCache(), "leases", gvr.Group, []string{"alpha"}, true, 10)
	var failure *resourceReadFailure
	if !errors.As(err, &failure) || failure.code != readreason.ReadError || readStatusFor(failure.code) != 500 {
		t.Fatalf("cancelled direct read = %v", err)
	}
	if len(dyn.Actions()) != 0 {
		t.Fatal("cancelled read reached Kubernetes")
	}
}

func TestResourceReadPreservesKnownExclusionBeforeInitialSync(t *testing.T) {
	core, err := k8score.NewResourceCache(k8score.CacheConfig{Client: fake.NewClientset(), ResourceScopes: map[string]k8score.ResourceScope{"services": {Enabled: true}, "pods": {Enabled: true, Namespace: "alpha"}}, DeferredTypes: map[string]bool{"pods": true}, DebugSyncDelays: map[string]time.Duration{"pods": time.Hour}, DeferredSyncTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Stop)
	cache := &k8s.ResourceCache{ResourceCache: core}
	for _, tc := range []struct {
		namespaces []string
		reason     readreason.Code
	}{
		{[]string{"beta"}, readreason.RadarNamespaceNotCollected},
		{[]string{"alpha"}, readreason.KindSyncPending},
		{[]string{"alpha", "beta"}, readreason.KindSyncPending},
		{nil, readreason.KindSyncPending},
	} {
		plan, err := typedReadCoverage(cache, "pods", tc.namespaces, false, tc.namespaces != nil)
		var failure *resourceReadFailure
		if !errors.As(err, &failure) || failure.code != tc.reason || plan.read != nil {
			t.Fatalf("%v: plan=%+v error=%v; want %s", tc.namespaces, plan, err, tc.reason)
		}
		if len(tc.namespaces) == 2 {
			var scope *resourceapi.Gap
			for i := range failure.gaps {
				if failure.gaps[i].Code == readreason.RadarNamespaceNotCollected {
					scope = &failure.gaps[i]
				}
			}
			if len(failure.gaps) != 2 || scope == nil || !slices.Equal(scope.Namespaces.Names, []string{"beta"}) {
				t.Fatalf("mixed target lost known exclusion: %+v", failure.gaps)
			}
		}
	}
}

func TestPendingResourceCountsRetainKnownScopeGaps(t *testing.T) {
	core, err := k8score.NewResourceCache(k8score.CacheConfig{Client: fake.NewClientset(), ResourceScopes: map[string]k8score.ResourceScope{"services": {Enabled: true}, "pods": {Enabled: true, Namespace: "alpha"}}, DeferredTypes: map[string]bool{"pods": true}, DebugSyncDelays: map[string]time.Duration{"pods": time.Hour}, DeferredSyncTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Stop)
	cache := k8s.GetResourceCache()
	prior := cache.ResourceCache
	cache.ResourceCache = core
	defer func() { cache.ResourceCache = prior }()
	w := httptest.NewRecorder()
	(&Server{}).handleResourceCounts(w, httptest.NewRequest(http.MethodGet, "/api/resource-counts?namespaces=alpha,beta", nil))
	var response ResourceCountsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	coverage := response.Coverage["Pod"]
	scopeGap := slices.IndexFunc(coverage.Gaps, func(gap resourceapi.Gap) bool { return gap.Code == readreason.RadarNamespaceNotCollected })
	if w.Code != 200 || !slices.Contains(response.Unavailable, "Pod") || response.Reasons["Pod"] != string(readreason.KindSyncPending) || coverage.Status != resourceapi.CoveragePartial || len(coverage.Gaps) != 2 || scopeGap < 0 || !slices.Equal(coverage.Gaps[scopeGap].Namespaces.Names, []string{"beta"}) {
		t.Fatalf("counts lost unread scope facts: status=%d %+v", w.Code, response)
	}
	if _, exists := response.Counts["Pod"]; exists {
		t.Fatal("pending Pod count became an observation")
	}
}

// A namespace whose access check Kubernetes answered with a credential
// rejection is a gap with that cause, never a limit of what Radar collects.
func TestTypedReadKeepsRecordedCredentialRejections(t *testing.T) {
	client := fake.NewClientset(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "alpha", Name: "alpha-cm"}})
	t.Cleanup(func() {
		k8s.ResetResourceCache()
		if err := k8s.InitTestResourceCache(testFakeClient); err != nil {
			t.Fatalf("restore package fixture cache: %v", err)
		}
	})
	k8s.ResetResourceCache()
	if err := k8s.InitTestResourceCacheFromConfig(k8score.CacheConfig{
		Client:         client,
		ResourceTypes:  map[string]bool{"configmaps": true},
		ResourceScopes: map[string]k8score.ResourceScope{"configmaps": {Enabled: true, Namespace: "alpha"}},
	}); err != nil {
		t.Fatal(err)
	}
	restore := k8s.SetTestPermissionResult(&k8s.PermissionCheckResult{
		Perms:                &k8s.ResourcePermissions{},
		Scopes:               map[string]k8score.ResourceScope{"configmaps": {Enabled: true, Namespace: "alpha"}},
		NamespaceProbeErrors: map[string]map[string]error{"configmaps": {"beta": apierrors.NewUnauthorized("token expired")}},
	})
	t.Cleanup(restore)
	cache := k8s.GetResourceCache()
	rejected := func(gaps []resourceapi.Gap) bool {
		return slices.ContainsFunc(gaps, func(gap resourceapi.Gap) bool {
			return gap.Code == readreason.RadarCredentialsRejected && gap.Namespaces != nil && slices.Equal(gap.Namespaces.Names, []string{"beta"})
		}) && !slices.ContainsFunc(gaps, func(gap resourceapi.Gap) bool { return gap.Code == readreason.RadarNamespaceNotCollected })
	}

	_, err := typedReadCoverage(cache, "configmaps", []string{"beta"}, false, true)
	var failure *resourceReadFailure
	if !errors.As(err, &failure) || failure.code != readreason.RadarCredentialsRejected || !rejected(failure.gaps) {
		t.Fatalf("named rejected namespace: %v", err)
	}
	plan, err := typedReadCoverage(cache, "configmaps", []string{"alpha", "beta"}, false, true)
	if err != nil || !slices.Equal(plan.read, []string{"alpha"}) || !rejected(plan.gaps) {
		t.Fatalf("mixed request: %+v, %v", plan, err)
	}
	plan, err = typedReadCoverage(cache, "configmaps", nil, false, false)
	if err != nil || !slices.Contains(plan.collected, "beta") || !rejected(plan.gaps) {
		t.Fatalf("every namespace: %+v, %v", plan, err)
	}
}

// While Radar is still retrying an access check that got no answer, a kind
// with no informer yet is still loading, not a failed read.
func TestDynamicListWhileTheAccessCheckRetries(t *testing.T) {
	useTestResourceCache(t, fake.NewClientset())
	gvr := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"})
	dyn.PrependReactor("list", "widgets", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("client rate limiter Wait returned an error: rate: Wait(n=1) would exceed context deadline")
	})
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "Widget", Name: gvr.Resource, Namespaced: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	req := httptest.NewRequest("GET", "/api/resources/widgets?group=example.com", nil)
	_, err := readDynamicResourceList(req, k8s.GetResourceCache(), "widgets", gvr.Group, nil, false, 10)
	var failure *resourceReadFailure
	if !k8s.GetDynamicResourceCache().Observation(gvr).ProbeRetrying {
		t.Fatalf("the access check is not retrying: %+v", k8s.GetDynamicResourceCache().Observation(gvr))
	}
	if !errors.As(err, &failure) || failure.code != readreason.KindSyncPending {
		t.Fatalf("list while the check retries = %v", err)
	}
}

// A context switch stops the old dynamic cache before replacing it; a get or
// a count answered in between is from the previous connection.
func TestStoppedDynamicCacheAnswersNothing(t *testing.T) {
	useTestResourceCache(t, fake.NewClientset())
	gvr := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	widget := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "example.com/v1", "kind": "Widget", "metadata": map[string]any{"name": "w", "namespace": "alpha"}}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		gvr: "WidgetList",
		{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}: "EndpointSliceList",
	}, widget)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "Widget", Name: gvr.Resource, Namespaced: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	cache := k8s.GetResourceCache()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := readDynamicResourceGet(context.Background(), cache, "widgets", gvr.Group, "alpha", "w"); err == nil {
			break
		} else if time.Now().After(deadline) {
			t.Fatalf("widget never read: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	k8s.GetDynamicResourceCache().Stop()

	if _, err := readDynamicResourceGet(context.Background(), cache, "widgets", gvr.Group, "alpha", "w"); !errors.Is(err, errReadHandlesChanged) {
		t.Fatalf("get from a stopped cache = %v", err)
	}
	w := httptest.NewRecorder()
	(&Server{}).handleResourceCounts(w, httptest.NewRequest(http.MethodGet, "/api/resource-counts", nil))
	var body resourceapi.ReadError
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.ErrorCode != readreason.ConnectionChanged {
		t.Fatalf("counts from a stopped cache: %d %s", w.Code, w.Body.String())
	}
}
