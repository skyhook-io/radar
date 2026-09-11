package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	capacitymodel "github.com/skyhook-io/radar/internal/capacity"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/capacityapi"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/karpenter"
	"github.com/skyhook-io/radar/pkg/subject"
	pkgtimeline "github.com/skyhook-io/radar/pkg/timeline"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
)

var capacityContractNodePoolGVR = schema.GroupVersionResource{
	Group: karpenter.Group, Version: "v1", Resource: "nodepools",
}

func TestCapacityRoutesAreAvailable(t *testing.T) {
	k8s.ResetTestDynamicState()
	t.Cleanup(k8s.ResetTestDynamicState)

	paths := []string{
		"/api/capacity",
		"/api/capacity/pools",
		"/api/capacity/pools/example",
		"/api/capacity/pools/example/members?type=node",
		"/api/capacity/demand",
		"/api/capacity/activity",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			var body map[string]any
			assertOK(t, get(t, path), &body)
			if got := body["state"]; got != string(capacityapi.IntegrationSyncing) {
				t.Fatalf("state = %v, want %q", got, capacityapi.IntegrationSyncing)
			}
			if got := body["schemaVersion"]; got != capacityapi.CurrentSchemaVersion {
				t.Fatalf("schemaVersion = %v, want %q", got, capacityapi.CurrentSchemaVersion)
			}
		})
	}
}

func TestCapacityNotDetectedPrecedesRBACDenial(t *testing.T) {
	initCapacityContractDynamicState(t, false, false)

	env := newAuthTestServer(t)
	permissions := &auth.UserPermissions{AllowedNamespaces: nil}
	permissions.SetCanI("list", karpenter.Group, "nodepools", "", false)
	// Node visibility is the page gate; the premise here is that Karpenter
	// absence (CRD not discovered) is reported as not_detected even though the
	// caller is also denied NodePools — absence precedes that denial.
	permissions.SetCanI("list", "", "nodes", "", true)
	permissions.SetCanI("list", "", "pods", "", false)
	permissions.SetCanI("get", "", "configmaps", "kube-system", false)
	permissions.SetCanI("list", "", "pods", "default", false)
	permissions.SetCanI("list", "", "pods", "broken", false)
	env.srv.permCache.Set("alice", nil, permissions)

	var body capacityapi.OverviewResponse
	assertOK(t, env.authGet(t, "/api/capacity", "alice", ""), &body)
	if body.State != capacityapi.IntegrationNotDetected {
		t.Fatalf("state = %q, want %q", body.State, capacityapi.IntegrationNotDetected)
	}
}

func TestCapacityNodeVisibilityGateDeniesEveryRoute(t *testing.T) {
	// Capacity's page gate is cluster-level node visibility: a caller who cannot
	// list Nodes has nothing honest to see, so every route fails closed with the
	// node-visibility message — even one whose NodePools are listable.
	initCapacityContractDynamicState(t, true, true, capacityContractNodePool("general"))
	env := newAuthTestServer(t)
	permissions := &auth.UserPermissions{AllowedNamespaces: nil}
	permissions.SetCanI("list", karpenter.Group, "nodepools", "", true)
	permissions.SetCanI("list", "", "nodes", "", false)
	env.srv.permCache.Set("alice", nil, permissions)

	for _, path := range []string{"/api/capacity", "/api/capacity/pools", "/api/capacity/demand", "/api/capacity/activity"} {
		resp := env.authGet(t, path, "alice", "")
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s status = %d, want 403; body = %s", path, resp.StatusCode, body)
		}
		var decoded map[string]any
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatalf("%s decode: %v", path, err)
		}
		if message, _ := decoded["error"].(string); message != "Capacity requires cluster-level node visibility (list nodes)" {
			t.Fatalf("%s error = %q, want the node-visibility message", path, message)
		}
	}
}

func TestCapacityOverviewNodePoolsDeniedRendersClusterShape(t *testing.T) {
	// NodePools exist but are unreadable, while the node fleet is visible. The
	// Overview must soften Karpenter denial into the cluster-only shape (200,
	// state denied, NodePools coverage denied, empty pools) rather than 403 —
	// and must not fabricate any pool-spec-derived data or claim Karpenter
	// health it could not read.
	initCapacityContractDynamicState(t, true, true, capacityContractNodePool("general"))
	env := newAuthTestServer(t)
	permissions := &auth.UserPermissions{AllowedNamespaces: nil}
	permissions.SetCanI("list", karpenter.Group, "nodepools", "", false)
	permissions.SetCanI("list", "", "nodes", "", true)
	permissions.SetCanI("list", "", "pods", "", false)
	permissions.SetCanI("list", "", "pods", "default", false)
	permissions.SetCanI("list", "", "pods", "broken", false)
	permissions.SetCanI("get", "", "configmaps", "kube-system", false)
	env.srv.permCache.Set("alice", nil, permissions)

	resp := env.authGet(t, "/api/capacity", "alice", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body = %s", resp.StatusCode, body)
	}
	wire, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	var body capacityapi.OverviewResponse
	if err := json.Unmarshal(wire, &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.State != capacityapi.IntegrationDenied {
		t.Fatalf("state = %q, want %q (wire meaning stays the Karpenter integration)", body.State, capacityapi.IntegrationDenied)
	}
	nodePoolCoverage := body.Coverage[capacityapi.CoverageNodePools]
	if nodePoolCoverage.Status != capacityapi.CoverageDenied {
		t.Fatalf("nodePools coverage = %#v, want denied", nodePoolCoverage)
	}
	if len(body.Pools) != 0 || body.Summary.PoolCount != nil {
		t.Fatalf("denied overview leaked pool-spec data: pools=%d poolCount=%v", len(body.Pools), body.Summary.PoolCount)
	}
	// Every Karpenter-scoped aggregate must be ABSENT on the wire, not zero: a
	// serialized poolCount 0 / exact-empty scheduling ledger / unpooledNodeCount
	// equal to the whole fleet would each describe NodePools nobody read.
	assertCapacitySummaryKeysAbsent(t, wire, "poolCount", "scheduling", "unpooledNodeCount")
	// The group surface is still present (arrays, never null); no Karpenter
	// manager may claim readiness it could not observe.
	if body.Groups == nil || body.Summary.Managers == nil {
		t.Fatalf("cluster-only surface missing: groups=%v managers=%v", body.Groups, body.Summary.Managers)
	}
	for _, manager := range body.Summary.Managers {
		if manager.Manager == capacityapi.ManagerKarpenter && manager.Status != capacityapi.ManagerUnknown {
			t.Fatalf("karpenter rollup claimed readiness under NodePool denial: %#v", manager)
		}
	}
}

func TestCapacityAuthorizedStateEnvelopes(t *testing.T) {
	t.Run("not detected", func(t *testing.T) {
		initCapacityContractDynamicState(t, false, false)
		env := newAuthTestServer(t)
		permissions := &auth.UserPermissions{AllowedNamespaces: nil}
		permissions.SetCanI("list", karpenter.Group, "nodepools", "", true)
		permissions.SetCanI("list", "", "nodes", "", true)
		permissions.SetCanI("list", "", "pods", "", false)
		permissions.SetCanI("get", "", "configmaps", "kube-system", false)
		permissions.SetCanI("list", "", "pods", "default", false)
		permissions.SetCanI("list", "", "pods", "broken", false)
		env.srv.permCache.Set("alice", nil, permissions)

		var body capacityapi.OverviewResponse
		assertOK(t, env.authGet(t, "/api/capacity", "alice", ""), &body)
		if body.State != capacityapi.IntegrationNotDetected {
			t.Fatalf("state = %q, want %q", body.State, capacityapi.IntegrationNotDetected)
		}
		if got := body.Coverage[capacityapi.CoverageNodePools].Status; got != capacityapi.CoverageUnavailable {
			t.Fatalf("nodePools coverage = %q, want %q", got, capacityapi.CoverageUnavailable)
		}
	})

	t.Run("syncing", func(t *testing.T) {
		initCapacityContractDynamicState(t, true, false)
		env := newAuthTestServer(t)
		permissions := &auth.UserPermissions{AllowedNamespaces: nil}
		permissions.SetCanI("list", karpenter.Group, "nodepools", "", true)
		permissions.SetCanI("list", "", "nodes", "", true)
		env.srv.permCache.Set("alice", nil, permissions)

		var body capacityapi.OverviewResponse
		assertOK(t, env.authGet(t, "/api/capacity", "alice", ""), &body)
		if body.State != capacityapi.IntegrationSyncing {
			t.Fatalf("state = %q, want %q", body.State, capacityapi.IntegrationSyncing)
		}
		coverage := body.Coverage[capacityapi.CoverageNodePools]
		if coverage.Status != capacityapi.CoverageSyncing || coverage.ReasonCode != "nodepools_syncing" {
			t.Fatalf("nodePools coverage = %#v", coverage)
		}
	})
}

func TestCapacityNotDetectedStillCarriesGroupSurface(t *testing.T) {
	initCapacityContractDynamicState(t, false, false)

	var body capacityapi.OverviewResponse
	assertOK(t, get(t, "/api/capacity"), &body)
	if body.State != capacityapi.IntegrationNotDetected {
		t.Fatalf("state = %q, want %q", body.State, capacityapi.IntegrationNotDetected)
	}
	// Karpenter absent must not blank the capacity surface: groups, orphans,
	// and managers are present (possibly empty) arrays, never null.
	if body.Groups == nil || body.OrphanAutoscalerGroups == nil || body.Summary.Managers == nil {
		t.Fatalf("group surface missing under not_detected: groups=%v orphans=%v managers=%v",
			body.Groups, body.OrphanAutoscalerGroups, body.Summary.Managers)
	}
}

func TestCapacityPoolMissingIsNotFoundWhenAvailable(t *testing.T) {
	initCapacityContractDynamicState(t, true, true, capacityContractNodePool("general"))

	resp := get(t, "/api/capacity/pools/missing")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 404; body = %s", resp.StatusCode, body)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := body["error"]; got != "NodePool not found" {
		t.Fatalf("error = %v, want NodePool not found", got)
	}
}

func TestCapacityDemandPoolFilterRequiresObservedNodePool(t *testing.T) {
	initCapacityContractDynamicState(t, true, true, capacityContractNodePool("general"))

	resp := get(t, "/api/capacity/demand?pool=general")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("existing pool status = %d, want 200; body = %s", resp.StatusCode, body)
	}

	missing := get(t, "/api/capacity/demand?pool=missing")
	defer missing.Body.Close()
	if missing.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(missing.Body)
		t.Fatalf("missing pool status = %d, want 404; body = %s", missing.StatusCode, body)
	}
	var body map[string]any
	if err := json.NewDecoder(missing.Body).Decode(&body); err != nil {
		t.Fatalf("decode missing-pool response: %v", err)
	}
	if got := body["error"]; got != "NodePool not found" {
		t.Fatalf("error = %v, want NodePool not found", got)
	}
}

func TestCapacityDemandPoolSetsClassifyAgainstWholeFleet(t *testing.T) {
	snapshot := &capacitymodel.Snapshot{
		GeneratedAt: time.Now().UTC(),
		NodePools: []*unstructured.Unstructured{
			capacityContractNodePool("pool-a"),
			capacityContractNodePool("pool-b"),
		},
	}
	model := capacitymodel.Build(*snapshot)
	result := capacityLoadResult{model: &model, snapshot: snapshot}

	classification, evaluation := demandPoolSets(result, "pool-a")
	if len(classification) != 2 {
		t.Fatalf("classification pools = %d, want the whole fleet — demand state is a fleet-wide property", len(classification))
	}
	if len(evaluation) != 1 || evaluation[0].NodePool.GetName() != "pool-a" {
		t.Fatalf("evaluation pools = %#v, want only the filtered pool", evaluation)
	}

	classification, evaluation = demandPoolSets(result, "")
	if len(classification) != 2 || len(evaluation) != 2 {
		t.Fatalf("unfiltered pool sets = %d/%d, want 2/2", len(classification), len(evaluation))
	}
}

func TestCapacityProjectsCanonicalNodePoolIssues(t *testing.T) {
	pool := capacityContractNodePool("unhealthy")
	pool.SetGeneration(2)
	pool.Object["status"] = map[string]any{"conditions": []any{map[string]any{
		"type":               "Ready",
		"status":             "False",
		"reason":             "ValidationFailed",
		"message":            "requirements are invalid",
		"observedGeneration": int64(2),
		"lastTransitionTime": time.Now().UTC().Add(-time.Minute).Format(time.RFC3339),
	}}}
	initCapacityContractDynamicState(t, true, true, pool)

	var overview capacityapi.OverviewResponse
	assertOK(t, get(t, "/api/capacity"), &overview)
	if len(overview.Pools) != 1 || overview.Pools[0].IssueCount != 1 {
		t.Fatalf("overview pool issue projection = %+v", overview.Pools)
	}
	var notReadyAction *capacityapi.ActionSummary
	for index := range overview.Summary.Actions {
		if overview.Summary.Actions[index].Code == "pool_not_ready" {
			notReadyAction = &overview.Summary.Actions[index]
			break
		}
	}
	if notReadyAction == nil || notReadyAction.Count != 1 {
		t.Fatalf("overview issue-derived actions = %+v", overview.Summary.Actions)
	}

	var detail capacityapi.PoolDetailResponse
	assertOK(t, get(t, "/api/capacity/pools/unhealthy"), &detail)
	if detail.Pool == nil || len(detail.Pool.Issues) != 1 {
		t.Fatalf("pool detail issues = %+v", detail.Pool)
	}
	issue := detail.Pool.Issues[0]
	if issue.Reason != issues.ReasonKarpenterNodePoolNotReady || issue.Message != "requirements are invalid" || issue.Cause == "" || issue.Action == "" {
		t.Fatalf("structured NodePool issue = %+v", issue)
	}
	for _, fact := range detail.Pool.Facts {
		if fact.Code == "nodepool_not_ready" {
			t.Fatalf("readiness was duplicated as a posture fact: %+v", fact)
		}
	}
}

func TestCapacityDemandSummaryIgnoresStateFilter(t *testing.T) {
	initCapacityContractDynamicState(t, true, true, capacityContractNodePool("general"))

	var all capacityapi.DemandResponse
	assertOK(t, get(t, "/api/capacity/demand"), &all)
	if all.Summary == nil {
		t.Fatal("observed Pod coverage omitted demand summary")
	}
	if len(all.Summary.ByState) != 5 {
		t.Fatalf("summary states = %d, want 5: %#v", len(all.Summary.ByState), all.Summary.ByState)
	}
	var podCount, groupCount int
	for _, counts := range all.Summary.ByState {
		podCount += counts.PodCount
		groupCount += counts.GroupCount
	}
	if all.Summary.Total != (capacityapi.DemandCounts{PodCount: podCount, GroupCount: groupCount}) {
		t.Fatalf("summary total = %#v, bucket sum = pods %d groups %d", all.Summary.Total, podCount, groupCount)
	}

	var blocked capacityapi.DemandResponse
	assertOK(t, get(t, "/api/capacity/demand?state=blocked"), &blocked)
	if blocked.Summary == nil || !reflect.DeepEqual(*blocked.Summary, *all.Summary) {
		t.Fatalf("state filter changed summary: all=%#v blocked=%#v", all.Summary, blocked.Summary)
	}
	for _, group := range blocked.Items {
		if group.State != capacityapi.DemandBlocked {
			t.Fatalf("state-filtered item = %q, want %q", group.State, capacityapi.DemandBlocked)
		}
	}
}

func TestCapacityDemandSummaryOmittedWithoutPodObservations(t *testing.T) {
	initCapacityContractDynamicState(t, true, true, capacityContractNodePool("general"))
	env := newAuthTestServer(t)
	permissions := &auth.UserPermissions{AllowedNamespaces: []string{}}
	permissions.SetCanI("list", karpenter.Group, "nodepools", "", true)
	permissions.SetCanI("list", karpenter.Group, "nodeclaims", "", false)
	permissions.SetCanI("list", "", "nodes", "", true)
	permissions.SetCanI("list", "", "pods", "", false)
	permissions.SetCanI("list", "metrics.k8s.io", "nodes", "", false)
	env.srv.permCache.Set("alice", nil, permissions)

	resp := env.authGet(t, "/api/capacity/demand", "alice", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body = %s", resp.StatusCode, body)
	}
	wire, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	var body capacityapi.DemandResponse
	if err := json.Unmarshal(wire, &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.State != capacityapi.IntegrationAvailable {
		t.Fatalf("state = %q, want %q", body.State, capacityapi.IntegrationAvailable)
	}
	if body.Summary != nil {
		t.Fatalf("summary = %#v, want omitted without Pod observations", body.Summary)
	}
	podCoverage := body.Coverage[capacityapi.CoveragePods]
	if podCoverage.Status != capacityapi.CoverageDenied || !capacityContainsString(podCoverage.ImpactFields, "demand.summary") {
		t.Fatalf("Pod coverage = %#v, want denied with demand.summary impact", podCoverage)
	}
	var raw map[string]any
	if err := json.Unmarshal(wire, &raw); err != nil {
		t.Fatalf("decode raw response: %v", err)
	}
	if _, found := raw["summary"]; found {
		t.Fatalf("summary key must be absent without Pod observations: %s", wire)
	}
}

func TestCapacityDemandSummaryPresentForExplicitNamespaceScope(t *testing.T) {
	initCapacityContractDynamicState(t, true, true, capacityContractNodePool("general"))

	var body capacityapi.DemandResponse
	assertOK(t, get(t, "/api/capacity/demand?namespaces=default"), &body)
	if body.Summary == nil {
		t.Fatal("explicit observed namespace scope omitted demand summary")
	}
	podCoverage := body.Coverage[capacityapi.CoveragePods]
	if podCoverage.Status != capacityapi.CoverageAvailable || podCoverage.Scope != capacityapi.CoverageScopeExplicitNamespaces {
		t.Fatalf("Pod coverage = %#v, want available explicit namespace scope", podCoverage)
	}
}

func TestCapacityDemandPoolFilterDoesNotRevealNodePoolExistenceUnderDenial(t *testing.T) {
	initCapacityContractDynamicState(t, true, true, capacityContractNodePool("general"))
	env := newAuthTestServer(t)
	permissions := &auth.UserPermissions{AllowedNamespaces: nil}
	permissions.SetCanI("list", karpenter.Group, "nodepools", "", false)
	// Node-visible but NodePool-denied: the demand route must fail closed on the
	// NodePool denial (not the node gate), identically for every pool name.
	permissions.SetCanI("list", "", "nodes", "", true)
	env.srv.permCache.Set("alice", nil, permissions)

	var errorMessage string
	for _, pool := range []string{"general", "missing"} {
		resp := env.authGet(t, "/api/capacity/demand?pool="+pool, "alice", "")
		if resp.StatusCode != http.StatusForbidden {
			body, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			t.Fatalf("pool %q status = %d, want 403; body = %s", pool, resp.StatusCode, body)
		}
		var body map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			resp.Body.Close()
			t.Fatalf("decode pool %q response: %v", pool, err)
		}
		resp.Body.Close()
		message, _ := body["error"].(string)
		if message == "" {
			t.Fatalf("pool %q returned no error message: %#v", pool, body)
		}
		if errorMessage == "" {
			errorMessage = message
		} else if message != errorMessage {
			t.Fatalf("RBAC denial differs by pool existence: existing=%q missing=%q", errorMessage, message)
		}
	}
}

func TestCapacityOptionalSourceDenialOmitsCounts(t *testing.T) {
	initCapacityContractDynamicState(t, true, true, capacityContractNodePool("general"))
	env := newAuthTestServer(t)
	permissions := &auth.UserPermissions{AllowedNamespaces: nil}
	permissions.SetCanI("list", karpenter.Group, "nodepools", "", true)
	permissions.SetCanI("list", karpenter.Group, "nodeclaims", "", false)
	permissions.SetCanI("list", "", "nodes", "", true)
	permissions.SetCanI("list", "", "pods", "", true)
	permissions.SetCanI("list", "metrics.k8s.io", "nodes", "", false)
	permissions.SetCanI("list", "apps", "replicasets", "default", false)
	permissions.SetCanI("list", "batch", "jobs", "default", false)
	permissions.SetCanI("get", "", "configmaps", "kube-system", false)
	env.srv.permCache.Set("alice", nil, permissions)

	resp := env.authGet(t, "/api/capacity", "alice", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body = %s", resp.StatusCode, body)
	}
	wire, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	var body capacityapi.OverviewResponse
	if err := json.Unmarshal(wire, &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.State != capacityapi.IntegrationAvailable {
		t.Fatalf("state = %q, want %q", body.State, capacityapi.IntegrationAvailable)
	}
	claimCoverage := body.Coverage[capacityapi.CoverageNodeClaims]
	if claimCoverage.Status != capacityapi.CoverageDenied || claimCoverage.ReasonCode != "nodeclaims_list_denied" {
		t.Fatalf("nodeClaims coverage = %#v", claimCoverage)
	}
	if claimCoverage.ItemCount != nil || body.Summary.ClaimCount != nil {
		t.Fatalf("denied claim counts were populated: coverage=%v summary=%v", claimCoverage.ItemCount, body.Summary.ClaimCount)
	}

	var raw map[string]any
	if err := json.Unmarshal(wire, &raw); err != nil {
		t.Fatalf("decode raw response: %v", err)
	}
	summary, ok := raw["summary"].(map[string]any)
	if !ok {
		t.Fatalf("summary is not an object: %s", wire)
	}
	if _, found := summary["claimCount"]; found {
		t.Fatalf("summary.claimCount must be omitted under denial: %s", wire)
	}
	coverageBySource, ok := raw["coverage"].(map[string]any)
	if !ok {
		t.Fatalf("coverage is not an object: %s", wire)
	}
	coverage, ok := coverageBySource[string(capacityapi.CoverageNodeClaims)].(map[string]any)
	if !ok {
		t.Fatalf("coverage.nodeClaims is not an object: %s", wire)
	}
	if _, found := coverage["itemCount"]; found {
		t.Fatalf("coverage.nodeClaims.itemCount must be omitted under denial: %s", wire)
	}
	autoscalerCoverage := body.Coverage[capacityapi.CoverageAutoscalerStatus]
	if autoscalerCoverage.Status != capacityapi.CoverageDenied || autoscalerCoverage.ReasonCode != "autoscaler_status_configmap_denied" {
		t.Fatalf("autoscalerStatus coverage = %#v", autoscalerCoverage)
	}
	for _, manager := range body.Summary.Managers {
		if manager.Manager != capacityapi.ManagerKarpenter {
			t.Fatalf("autoscaler manager fabricated under denied ConfigMap access: %#v", manager)
		}
	}
}

func TestCapacityDemandRejectsInvalidFiltersAndCursors(t *testing.T) {
	k8s.ResetTestDynamicState()
	t.Cleanup(k8s.ResetTestDynamicState)

	blockedRequest := capacityPageRequest{
		scope:             "demand",
		filterFingerprint: capacityFilterFingerprint(url.Values{"state": {string(capacityapi.DemandBlocked)}}),
	}
	blockedCursor, err := encodeCapacityPageCursor(blockedRequest, "demand-group", "snapshot-a")
	if err != nil {
		t.Fatalf("encode cursor: %v", err)
	}
	tests := map[string]struct {
		path           string
		wantCursorCode bool
	}{
		"invalid state":       {path: "/api/capacity/demand?state=impossible"},
		"limit over maximum":  {path: "/api/capacity/demand?limit=26"},
		"repeated pool":       {path: "/api/capacity/demand?pool=one&pool=two"},
		"malformed cursor":    {path: "/api/capacity/demand?cursor=%25%25%25", wantCursorCode: true},
		"cursor filter drift": {path: "/api/capacity/demand?state=unknown&cursor=" + url.QueryEscape(blockedCursor), wantCursorCode: true},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			resp := get(t, test.path)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d, want 400; body = %s", resp.StatusCode, body)
			}
			var body map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if message, ok := body["error"].(string); !ok || message == "" {
				t.Fatalf("missing error body: %#v", body)
			}
			if test.wantCursorCode {
				if got := body["error_code"]; got != capacityCursorInvalidErrorCode {
					t.Fatalf("error_code = %v, want %q", got, capacityCursorInvalidErrorCode)
				}
			} else if got, found := body["error_code"]; found {
				t.Fatalf("ordinary validation error included error_code = %v", got)
			}
		})
	}
}

func TestCapacityActivityRejectsInvalidFiltersAndCursors(t *testing.T) {
	fingerprint := capacityFilterFingerprint(url.Values{})
	validCursor, err := encodeCapacityActivityCursor("epoch", fingerprint, k8s.ActiveClusterContext(), 1, "older")
	if err != nil {
		t.Fatalf("encode cursor: %v", err)
	}
	otherClusterCursor, err := encodeCapacityActivityCursor("epoch", fingerprint, "other-cluster", 1, "older")
	if err != nil {
		t.Fatalf("encode cross-cluster cursor: %v", err)
	}
	tests := map[string]struct {
		path           string
		wantCursorCode bool
	}{
		"invalid since":           {path: "/api/capacity/activity?since=yesterday"},
		"repeated filter":         {path: "/api/capacity/activity?pool=one&pool=two"},
		"unsupported workload":    {path: "/api/capacity/activity?workload=api"},
		"malformed cursor":        {path: "/api/capacity/activity?cursor=%25%25%25", wantCursorCode: true},
		"repeated cursor":         {path: "/api/capacity/activity?cursor=one&cursor=two", wantCursorCode: true},
		"cursor filter drift":     {path: "/api/capacity/activity?pool=general&cursor=" + url.QueryEscape(validCursor), wantCursorCode: true},
		"cursor cluster mismatch": {path: "/api/capacity/activity?cursor=" + url.QueryEscape(otherClusterCursor), wantCursorCode: true},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			resp := get(t, test.path)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d, want 400; body = %s", resp.StatusCode, body)
			}
			var body map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if message, ok := body["error"].(string); !ok || message == "" {
				t.Fatalf("missing error body: %#v", body)
			}
			if test.wantCursorCode {
				if got := body["error_code"]; got != capacityCursorInvalidErrorCode {
					t.Fatalf("error_code = %v, want %q", got, capacityCursorInvalidErrorCode)
				}
			} else if got, found := body["error_code"]; found {
				t.Fatalf("ordinary validation error included error_code = %v", got)
			}
		})
	}
}

func TestCapacityActivityEmptyMemoryObservationStartsAtProcessStart(t *testing.T) {
	initCapacityContractDynamicState(t, true, true, capacityContractNodePool("general"))
	timeline.ResetStore()
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 3}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	t.Cleanup(func() {
		timeline.ResetStore()
		if err := timeline.InitStore(timeline.DefaultStoreConfig()); err != nil {
			t.Fatalf("re-init global store: %v", err)
		}
	})
	processStart := timeline.ObservationStart()

	var body capacityapi.ActivityResponse
	assertOK(t, get(t, "/api/capacity/activity"), &body)
	if !body.Observation.StartedAt.Equal(processStart) {
		t.Fatalf("empty memory observation start = %s, want process start %s", body.Observation.StartedAt, processStart)
	}
	if body.Observation.Retention.Mode != "memory_bounded" || body.Observation.Retention.MaxEvents == nil || *body.Observation.Retention.MaxEvents != 3 {
		t.Fatalf("memory retention = %#v, want bounded maxEvents=3", body.Observation.Retention)
	}
}

func TestCapacityActivityReportsCursorEpochAndEviction(t *testing.T) {
	initCapacityContractDynamicState(t, true, true, capacityContractNodePool("general"))
	timeline.ResetStore()
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 3}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	t.Cleanup(func() {
		timeline.ResetStore()
		if err := timeline.InitStore(timeline.DefaultStoreConfig()); err != nil {
			t.Fatalf("re-init global store: %v", err)
		}
	})

	startedAt := timeline.ObservationStart().Add(-10 * time.Minute)
	for index := 1; index <= 4; index++ {
		id := "config-" + strconv.Itoa(index)
		event := pkgtimeline.TimelineEvent{
			ID: id, Timestamp: startedAt.Add(time.Duration(index) * time.Minute), Source: pkgtimeline.SourceInformer,
			Kind: karpenter.NodePoolKind, APIVersion: karpenter.APIVersionV1, Name: "general", UID: "general-uid",
			EventType: pkgtimeline.EventTypeUpdate, ClusterContext: k8s.ActiveClusterContext(),
			Diff: &k8score.DiffInfo{Summary: id, Fields: []k8score.FieldChange{{Path: "spec.weight"}}},
		}
		if err := timeline.GetStore().Append(context.Background(), event); err != nil {
			t.Fatalf("append %s: %v", id, err)
		}
	}

	fingerprint := capacityFilterFingerprint(url.Values{})
	epoch := strconv.FormatInt(timeline.ObservationStart().UnixNano(), 10)
	tests := []struct {
		name       string
		cursor     string
		wantStatus capacityapi.CursorStatus
		wantReason string
	}{
		{
			name:       "epoch changed",
			cursor:     mustEncodeCapacityActivityCursor(t, "previous-epoch", fingerprint, 0),
			wantStatus: capacityapi.CursorEpochChanged,
			wantReason: "timeline_epoch_changed",
		},
		{
			// MaxSize 3 with 4 appends evicted seq 1; an older cursor at the
			// retained boundary (seq 2) has lost its remaining history.
			name:       "cursor evicted",
			cursor:     mustEncodeCapacityActivityCursor(t, epoch, fingerprint, 2),
			wantStatus: capacityapi.CursorEvicted,
			wantReason: "timeline_cursor_evicted",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var body capacityapi.ActivityResponse
			assertOK(t, get(t, "/api/capacity/activity?cursor="+url.QueryEscape(test.cursor)), &body)
			if body.State != capacityapi.IntegrationAvailable {
				t.Fatalf("state = %q, want %q", body.State, capacityapi.IntegrationAvailable)
			}
			if body.CursorStatus != test.wantStatus {
				t.Fatalf("cursorStatus = %q, want %q", body.CursorStatus, test.wantStatus)
			}
			if body.CursorGap == nil || body.CursorGap.Reason != test.wantReason {
				t.Fatalf("cursorGap = %#v, want reason %q", body.CursorGap, test.wantReason)
			}
			if len(body.Observation.Gaps) != 1 || body.Observation.Gaps[0].Reason != test.wantReason {
				t.Fatalf("observation gaps = %#v", body.Observation.Gaps)
			}
			if body.Page.NextCursor != "" || len(body.Items) != 0 {
				t.Fatalf("gap response next=%q items=%d, want no cursor and no items (client restarts from page 1)", body.Page.NextCursor, len(body.Items))
			}
		})
	}
}

func TestCapacityActivityEvictedCursorStillDeliversRetainedEvents(t *testing.T) {
	initCapacityContractDynamicState(t, true, true, capacityContractNodePool("general"))
	timeline.ResetStore()
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 3}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	t.Cleanup(func() {
		timeline.ResetStore()
		if err := timeline.InitStore(timeline.DefaultStoreConfig()); err != nil {
			t.Fatalf("re-init global store: %v", err)
		}
	})

	startedAt := timeline.ObservationStart().Add(-10 * time.Minute)
	for index := 1; index <= 4; index++ {
		id := "config-" + strconv.Itoa(index)
		event := pkgtimeline.TimelineEvent{
			ID: id, Timestamp: startedAt.Add(time.Duration(index) * time.Minute), Source: pkgtimeline.SourceInformer,
			Kind: karpenter.NodePoolKind, APIVersion: karpenter.APIVersionV1, Name: "general", UID: "general-uid",
			EventType: pkgtimeline.EventTypeUpdate, ClusterContext: k8s.ActiveClusterContext(),
			Diff: &k8score.DiffInfo{Summary: id, Fields: []k8score.FieldChange{{Path: "spec.weight"}}},
		}
		if err := timeline.GetStore().Append(context.Background(), event); err != nil {
			t.Fatalf("append %s: %v", id, err)
		}
	}

	epoch := strconv.FormatInt(timeline.ObservationStart().UnixNano(), 10)
	// Paging down from the newest record still works after eviction as long as
	// older retained records remain: seq 4 -> the two retained older records.
	pageCursor := mustEncodeCapacityActivityCursor(t, epoch, capacityFilterFingerprint(url.Values{}), 4)
	var body capacityapi.ActivityResponse
	assertOK(t, get(t, "/api/capacity/activity?cursor="+url.QueryEscape(pageCursor)), &body)
	if body.CursorGap != nil {
		t.Fatalf("older page within retained history reported a gap: %#v", body.CursorGap)
	}
	if len(body.Items) != 2 || body.Items[0].Evidence[0].RawMessage != "config-3" || body.Items[1].Evidence[0].RawMessage != "config-2" {
		t.Fatalf("retained older page = %#v, want config-3 then config-2", body.Items)
	}
	// One step further (older than the oldest retained record) is a real gap,
	// never a silent "end of history".
	evictedCursor := mustEncodeCapacityActivityCursor(t, epoch, capacityFilterFingerprint(url.Values{}), 2)
	var evicted capacityapi.ActivityResponse
	assertOK(t, get(t, "/api/capacity/activity?cursor="+url.QueryEscape(evictedCursor)), &evicted)
	if evicted.CursorStatus != capacityapi.CursorEvicted || evicted.CursorGap == nil || evicted.CursorGap.Reason != "timeline_cursor_evicted" {
		t.Fatalf("evicted older cursor = %q/%#v, want an explicit eviction gap", evicted.CursorStatus, evicted.CursorGap)
	}
	wantObservationStart := timeline.ObservationStart()
	if !body.Observation.StartedAt.Equal(wantObservationStart) || body.Observation.Retention.MaxEvents == nil || *body.Observation.Retention.MaxEvents != 3 {
		t.Fatalf("evicted observation = %#v, want process-clamped start %s and maxEvents=3", body.Observation, wantObservationStart)
	}
}

func TestCapacityActivityOlderPagePreservesCorrelatedEpisodes(t *testing.T) {
	initCapacityContractDynamicState(t, true, true, capacityContractNodePool("general"))
	timeline.ResetStore()
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 100}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	t.Cleanup(func() {
		timeline.ResetStore()
		if err := timeline.InitStore(timeline.DefaultStoreConfig()); err != nil {
			t.Fatalf("re-init global store: %v", err)
		}
	})

	startedAt := timeline.ObservationStart().Add(time.Second)
	events := []pkgtimeline.TimelineEvent{
		{
			ID: "claim-created", Timestamp: startedAt, Source: pkgtimeline.SourceInformer,
			Kind: karpenter.NodeClaimKind, APIVersion: karpenter.APIVersionV1, Name: "claim-a", UID: "claim-a-uid",
			EventType: pkgtimeline.EventTypeAdd, ClusterContext: k8s.ActiveClusterContext(),
			Owner: &k8score.OwnerInfo{Kind: karpenter.NodePoolKind, Name: "general"},
		},
		{
			ID: "pool-updated", Timestamp: startedAt.Add(time.Minute), Source: pkgtimeline.SourceInformer,
			Kind: karpenter.NodePoolKind, APIVersion: karpenter.APIVersionV1, Name: "general", UID: "general-uid",
			EventType: pkgtimeline.EventTypeUpdate, ClusterContext: k8s.ActiveClusterContext(),
			Diff: &k8score.DiffInfo{Summary: "spec.weight changed", Fields: []k8score.FieldChange{{Path: "spec.weight"}}},
		},
		{
			ID: "claim-ready", Timestamp: startedAt.Add(2 * time.Minute), Source: pkgtimeline.SourceK8sEvent,
			Kind: karpenter.NodeClaimKind, APIVersion: karpenter.APIVersionV1, Name: "claim-a", UID: "claim-a-uid",
			Reason: "Ready", EventType: pkgtimeline.EventTypeNormal, ClusterContext: k8s.ActiveClusterContext(),
		},
	}
	for _, event := range events {
		if err := timeline.GetStore().Append(context.Background(), event); err != nil {
			t.Fatalf("append %s: %v", event.ID, err)
		}
	}

	var first capacityapi.ActivityResponse
	assertOK(t, get(t, "/api/capacity/activity?limit=1"), &first)
	if len(first.Items) != 1 || first.Items[0].Type != capacityapi.ActivityProvision || first.Items[0].State != capacityapi.ActivityCompleted {
		t.Fatalf("first activity page = %#v", first.Items)
	}
	if first.Page.NextCursor == "" {
		t.Fatal("first activity page omitted older cursor")
	}

	var older capacityapi.ActivityResponse
	assertOK(t, get(t, "/api/capacity/activity?cursor="+url.QueryEscape(first.Page.NextCursor)), &older)
	if len(older.Items) != 1 || older.Items[0].Type != capacityapi.ActivityConfigChange {
		t.Fatalf("older activity page = %#v, want only the independent config change", older.Items)
	}
	if older.Page.HasMore || older.Page.NextCursor != "" {
		t.Fatalf("older page pagination = %#v, want exhausted stable record window", older.Page)
	}
}

func TestCapacityActivityIncludesPersistedHistoryBeforeProcessStart(t *testing.T) {
	initCapacityContractDynamicState(t, true, true, capacityContractNodePool("general"))
	timeline.ResetStore()
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeSQLite, Path: filepath.Join(t.TempDir(), "timeline.db")}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	t.Cleanup(func() {
		timeline.ResetStore()
		if err := timeline.InitStore(timeline.DefaultStoreConfig()); err != nil {
			t.Fatalf("re-init global store: %v", err)
		}
	})
	processStart := timeline.ObservationStart()
	oldTimestamp := processStart.Add(-24 * time.Hour)
	event := pkgtimeline.TimelineEvent{
		ID: "persisted-config", Timestamp: oldTimestamp, Source: pkgtimeline.SourceInformer,
		Kind: karpenter.NodePoolKind, APIVersion: karpenter.APIVersionV1, Name: "general", UID: "general-uid",
		EventType: pkgtimeline.EventTypeUpdate, ClusterContext: k8s.ActiveClusterContext(),
		Diff: &k8score.DiffInfo{Summary: "spec.limits changed", Fields: []k8score.FieldChange{{Path: "spec.limits"}}},
	}
	if err := timeline.GetStore().Append(context.Background(), event); err != nil {
		t.Fatalf("append persisted event: %v", err)
	}

	var body capacityapi.ActivityResponse
	assertOK(t, get(t, "/api/capacity/activity"), &body)
	if len(body.Items) != 1 || body.Items[0].Type != capacityapi.ActivityConfigChange {
		t.Fatalf("persisted activity = %#v", body.Items)
	}
	if !body.Observation.StartedAt.Equal(oldTimestamp) || !body.Observation.StartedAt.Before(processStart) {
		t.Fatalf("observation start = %s, want persisted boundary %s before process start %s", body.Observation.StartedAt, oldTimestamp, processStart)
	}
	if body.Observation.Retention.Mode != "persistent_unbounded" {
		t.Fatalf("retention mode = %q", body.Observation.Retention.Mode)
	}
}

func TestCapacityActivityUsesActiveClusterTimelineBounds(t *testing.T) {
	previousContext := k8s.SetTestContextName("capacity-active")
	t.Cleanup(func() { k8s.SetTestContextName(previousContext) })
	initCapacityContractDynamicState(t, true, true, capacityContractNodePool("general"))
	timeline.ResetStore()
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeSQLite, Path: filepath.Join(t.TempDir(), "timeline.db")}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	t.Cleanup(func() {
		timeline.ResetStore()
		if err := timeline.InitStore(timeline.DefaultStoreConfig()); err != nil {
			t.Fatalf("re-init global store: %v", err)
		}
	})

	processStart := timeline.ObservationStart().UTC()
	activeAt := processStart.Add(-2 * time.Hour)
	events := []pkgtimeline.TimelineEvent{
		capacityActivityConfigEvent("foreign-oldest", "capacity-foreign", processStart.Add(-30*24*time.Hour)),
		capacityActivityConfigEvent("foreign-second", "capacity-foreign", processStart.Add(-20*24*time.Hour)),
		capacityActivityConfigEvent("foreign-third", "capacity-foreign", processStart.Add(-10*24*time.Hour)),
		capacityActivityConfigEvent("active-event", "capacity-active", activeAt),
		capacityActivityConfigEvent("foreign-newest", "capacity-foreign", processStart.Add(-time.Hour)),
	}
	if err := timeline.GetStore().AppendBatch(context.Background(), events); err != nil {
		t.Fatalf("append mixed-cluster activity: %v", err)
	}

	var body capacityapi.ActivityResponse
	assertOK(t, get(t, "/api/capacity/activity"), &body)
	if len(body.Items) != 1 || body.Items[0].Evidence[0].RawMessage != "active-event" {
		t.Fatalf("active-cluster activity = %#v", body.Items)
	}
	if !body.Observation.StartedAt.Equal(activeAt) {
		t.Fatalf("observation start = %s, want active-cluster boundary %s", body.Observation.StartedAt, activeAt)
	}
	// Nothing was ever evicted from this persistent store, so an older cursor
	// that exhausts history is a genuine end — never a fabricated gap.
	epoch := strconv.FormatInt(timeline.ObservationStart().UnixNano(), 10)
	endCursor := mustEncodeCapacityActivityCursor(t, epoch, capacityFilterFingerprint(url.Values{}), 4)
	var exhausted capacityapi.ActivityResponse
	assertOK(t, get(t, "/api/capacity/activity?cursor="+url.QueryEscape(endCursor)), &exhausted)
	if exhausted.CursorGap != nil || len(exhausted.Items) != 0 || exhausted.Page.HasMore {
		t.Fatalf("end-of-history response = gap %#v items %d hasMore %v, want a clean empty page", exhausted.CursorGap, len(exhausted.Items), exhausted.Page.HasMore)
	}

	k8s.SetTestContextName("capacity-empty")
	requestStarted := time.Now().UTC()
	var empty capacityapi.ActivityResponse
	assertOK(t, get(t, "/api/capacity/activity"), &empty)
	if len(empty.Items) != 0 || empty.Observation.StartedAt.Before(requestStarted) {
		t.Fatalf("empty active-cluster response leaked foreign history: start=%s items=%#v", empty.Observation.StartedAt, empty.Items)
	}
}

func TestCapacityActivityRBACMetadataUsesOnlyAuthorizedRelevantEvents(t *testing.T) {
	initCapacityContractDynamicState(t, true, true, capacityContractNodePool("general"))
	timeline.ResetStore()
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 100}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	t.Cleanup(func() {
		timeline.ResetStore()
		if err := timeline.InitStore(timeline.DefaultStoreConfig()); err != nil {
			t.Fatalf("re-init global store: %v", err)
		}
	})

	base := timeline.ObservationStart().UTC()
	visibleAt := base.Add(-time.Minute)
	events := []pkgtimeline.TimelineEvent{
		capacityActivityConfigEvent("visible-config", k8s.ActiveClusterContext(), visibleAt),
		{
			ID: "denied-claim", Timestamp: base.Add(-24 * time.Hour), Source: pkgtimeline.SourceK8sEvent,
			Kind: karpenter.NodeClaimKind, APIVersion: karpenter.APIVersionV1, Name: "private-claim", UID: "private-claim-uid",
			Reason: "Ready", EventType: pkgtimeline.EventTypeNormal, Namespace: "private", ClusterContext: k8s.ActiveClusterContext(),
		},
		{
			ID: "unrelated-node", Timestamp: base.Add(-48 * time.Hour), Source: pkgtimeline.SourceInformer,
			Kind: "Node", APIVersion: "v1", Name: "unrelated-node", EventType: pkgtimeline.EventTypeDelete, ClusterContext: k8s.ActiveClusterContext(),
		},
		{
			ID: "denied-k8s-event", Timestamp: base.Add(2 * time.Minute), Source: pkgtimeline.SourceK8sEvent,
			Kind: karpenter.NodePoolKind, APIVersion: karpenter.APIVersionV1, Name: "general", UID: "general-uid",
			Reason: "DisruptionBlocked", EventType: pkgtimeline.EventTypeNormal, Namespace: "private", ClusterContext: k8s.ActiveClusterContext(),
		},
	}
	if err := timeline.GetStore().AppendBatch(context.Background(), events); err != nil {
		t.Fatalf("append mixed-visibility activity: %v", err)
	}

	env := newAuthTestServer(t)
	permissions := &auth.UserPermissions{AllowedNamespaces: nil}
	permissions.SetCanI("list", karpenter.Group, "nodepools", "", true)
	permissions.SetCanI("list", karpenter.Group, "nodeclaims", "", false)
	// Node visibility is the page gate for Activity, so a caller who reaches it
	// can always see node events; per-event denial here is exercised through the
	// claim and namespace-scoped event fixtures instead.
	permissions.SetCanI("list", "", "nodes", "", true)
	permissions.SetCanI("list", "", "events", "", false)
	permissions.SetCanI("list", "", "events", "private", false)
	env.srv.permCache.Set("alice", nil, permissions)

	var body capacityapi.ActivityResponse
	assertOK(t, env.authGet(t, "/api/capacity/activity", "alice", ""), &body)
	if len(body.Items) != 1 || len(body.Items[0].Evidence) != 1 || body.Items[0].Evidence[0].RawMessage != "visible-config" {
		t.Fatalf("visible activity = %#v", body.Items)
	}
	timelineCoverage := body.Coverage[capacityapi.CoverageTimeline]
	if timelineCoverage.ItemCount == nil || *timelineCoverage.ItemCount != 1 || !body.Observation.StartedAt.Equal(base) {
		t.Fatalf("denied activity affected timeline metadata: coverage=%#v observation=%#v", timelineCoverage, body.Observation)
	}
	if body.Coverage[capacityapi.CoverageKarpenterObjectEvents].Status != capacityapi.CoveragePartial {
		t.Fatalf("event coverage = %#v, want permission-derived partial coverage", body.Coverage[capacityapi.CoverageKarpenterObjectEvents])
	}

	// Paging older from the single visible record: invisible (denied) events
	// must not surface as items, extra pages, or fabricated gaps.
	epoch := strconv.FormatInt(timeline.ObservationStart().UnixNano(), 10)
	olderCursor := mustEncodeCapacityActivityCursor(t, epoch, capacityFilterFingerprint(url.Values{}), 1)
	var older capacityapi.ActivityResponse
	assertOK(t, env.authGet(t, "/api/capacity/activity?cursor="+url.QueryEscape(olderCursor), "alice", ""), &older)
	if len(older.Items) != 0 || older.Page.HasMore || older.CursorGap != nil {
		t.Fatalf("denied events affected older paging: %#v", older)
	}
}

func TestCapacityActivityEvictedMemoryWithoutVisibleEventsStartsAtRequest(t *testing.T) {
	initCapacityContractDynamicState(t, true, true, capacityContractNodePool("general"))
	timeline.ResetStore()
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 2}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	t.Cleanup(func() {
		timeline.ResetStore()
		if err := timeline.InitStore(timeline.DefaultStoreConfig()); err != nil {
			t.Fatalf("re-init global store: %v", err)
		}
	})
	base := timeline.ObservationStart().UTC()
	for index := range 3 {
		event := pkgtimeline.TimelineEvent{
			ID: "unrelated-node-" + strconv.Itoa(index), Timestamp: base.Add(time.Duration(index-3) * time.Hour), Source: pkgtimeline.SourceInformer,
			Kind: "Node", APIVersion: "v1", Name: "unrelated-node-" + strconv.Itoa(index), EventType: pkgtimeline.EventTypeUpdate,
			ClusterContext: k8s.ActiveClusterContext(),
		}
		if err := timeline.GetStore().Append(context.Background(), event); err != nil {
			t.Fatalf("append unrelated event: %v", err)
		}
	}

	requestStarted := time.Now().UTC()
	var body capacityapi.ActivityResponse
	assertOK(t, get(t, "/api/capacity/activity"), &body)
	if len(body.Items) != 0 || body.Observation.StartedAt.Before(requestStarted) {
		t.Fatalf("unrelated evicted history shaped empty activity: start=%s items=%#v", body.Observation.StartedAt, body.Items)
	}
	if body.Observation.Retention.MaxEvents == nil || *body.Observation.Retention.MaxEvents != 2 {
		t.Fatalf("retention = %#v, want maxEvents=2", body.Observation.Retention)
	}
}

func capacityActivityConfigEvent(id, clusterContext string, timestamp time.Time) pkgtimeline.TimelineEvent {
	return pkgtimeline.TimelineEvent{
		ID: id, Timestamp: timestamp, Source: pkgtimeline.SourceInformer,
		Kind: karpenter.NodePoolKind, APIVersion: karpenter.APIVersionV1, Name: "general", UID: "general-uid",
		EventType: pkgtimeline.EventTypeUpdate, ClusterContext: clusterContext,
		Diff: &k8score.DiffInfo{Summary: id, Fields: []k8score.FieldChange{{Path: "spec.weight"}}},
	}
}

func TestCapacityActivitySQLiteIgnoresUnrelatedNodeNoiseAcrossRawQueryBounds(t *testing.T) {
	initCapacityContractDynamicState(t, true, true, capacityContractNodePool("general"))
	timeline.ResetStore()
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeSQLite, Path: filepath.Join(t.TempDir(), "timeline.db")}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	t.Cleanup(func() {
		timeline.ResetStore()
		if err := timeline.InitStore(timeline.DefaultStoreConfig()); err != nil {
			t.Fatalf("re-init global store: %v", err)
		}
	})

	base := timeline.ObservationStart().UTC()
	events := make([]pkgtimeline.TimelineEvent, 0, capacityActivityQueryLimit+3)
	events = append(events, pkgtimeline.TimelineEvent{
		ID: "claim-created", Timestamp: base, Source: pkgtimeline.SourceInformer,
		Kind: karpenter.NodeClaimKind, APIVersion: karpenter.APIVersionV1, Name: "claim-a", UID: "claim-a-uid",
		EventType: pkgtimeline.EventTypeAdd, ClusterContext: k8s.ActiveClusterContext(),
		Owner: &k8score.OwnerInfo{Kind: karpenter.NodePoolKind, Name: "general"},
	})
	events = append(events, pkgtimeline.TimelineEvent{
		ID: "earliest-sequence-activity", Timestamp: base.Add(time.Second), Source: pkgtimeline.SourceInformer,
		Kind: karpenter.NodePoolKind, APIVersion: karpenter.APIVersionV1, Name: "general", UID: "general-uid",
		EventType: pkgtimeline.EventTypeUpdate, ClusterContext: k8s.ActiveClusterContext(),
		Diff: &k8score.DiffInfo{Summary: "earliest-sequence-activity", Fields: []k8score.FieldChange{{Path: "spec.weight"}}},
	})
	for index := range capacityActivityQueryLimit - 1 {
		events = append(events, pkgtimeline.TimelineEvent{
			ID: "noise-" + strconv.Itoa(index), Timestamp: base.Add(time.Duration(index+2) * time.Second), Source: pkgtimeline.SourceInformer,
			Kind: "Node", APIVersion: "v1", Name: "noise-" + strconv.Itoa(index),
			EventType: pkgtimeline.EventTypeUpdate, ClusterContext: k8s.ActiveClusterContext(),
		})
	}
	events = append(events, pkgtimeline.TimelineEvent{
		ID: "late-sequence-arrival", Timestamp: base.Add(-48 * time.Hour), Source: pkgtimeline.SourceInformer,
		Kind: karpenter.NodeClaimKind, APIVersion: karpenter.APIVersionV1, Name: "claim-a", UID: "claim-a-uid",
		Reason: "Ready", Message: "late-sequence-arrival", EventType: pkgtimeline.EventTypeNormal, ClusterContext: k8s.ActiveClusterContext(),
	})
	events = append(events, pkgtimeline.TimelineEvent{
		ID: "subjectless-controller-finalized", Timestamp: base.Add(time.Hour), Source: pkgtimeline.SourceK8sEvent,
		Kind: karpenter.NodeClaimKind, APIVersion: karpenter.APIVersionV1,
		Reason: "Finalized", Message: "Finalized karpenter.sh/termination", EventType: pkgtimeline.EventTypeNormal, ClusterContext: k8s.ActiveClusterContext(),
	})
	if err := timeline.GetStore().AppendBatch(context.Background(), events); err != nil {
		t.Fatalf("append bounded activity fixture: %v", err)
	}

	var body capacityapi.ActivityResponse
	assertOK(t, get(t, "/api/capacity/activity"), &body)
	if len(body.Items) != 2 || body.Items[0].Type != capacityapi.ActivityProvision || body.Items[0].State != capacityapi.ActivityCompleted || body.Items[0].PrimaryReasonCode != "nodeclaim_ready" || len(body.Items[0].Evidence) != 2 {
		t.Fatalf("activity did not correlate across unrelated raw events: %#v", body.Items)
	}
	if body.Items[1].Type != capacityapi.ActivityConfigChange || body.Items[1].Evidence[0].RawMessage != "earliest-sequence-activity" {
		t.Fatalf("independent config activity = %#v", body.Items[1])
	}
	timelineCoverage := body.Coverage[capacityapi.CoverageTimeline]
	if timelineCoverage.Status != capacityapi.CoverageAvailable || timelineCoverage.ReasonCode != "" || timelineCoverage.ItemCount == nil || *timelineCoverage.ItemCount != 3 {
		t.Fatalf("timeline coverage counted unrelated Node events: %#v", timelineCoverage)
	}
	if body.Page.HasMore || body.Page.NextCursor != "" {
		t.Fatalf("unrelated Node events created a false continuation: %#v", body.Page)
	}
}

func initCapacityContractDynamicState(t *testing.T, discover, waitForSync bool, objects ...runtime.Object) {
	t.Helper()
	k8s.ResetTestDynamicState()
	previousClient := k8s.SetTestClient(&kubernetes.Clientset{})
	t.Cleanup(func() { k8s.SetTestClient(previousClient) })
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{capacityContractNodePoolGVR: "NodePoolList"},
		objects...,
	)
	resources := []k8s.APIResource{}
	if discover {
		resources = append(resources, k8s.APIResource{
			Group: karpenter.Group, Version: "v1", Kind: karpenter.NodePoolKind,
			Name: "nodepools", Namespaced: false, IsCRD: true, Verbs: []string{"get", "list", "watch"},
		})
	}
	if err := k8s.InitTestDynamicResourceCache(dynamicClient, resources); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	if !waitForSync {
		return
	}
	dynamicCache := k8s.GetDynamicResourceCache()
	if err := dynamicCache.EnsureWatching(capacityContractNodePoolGVR); err != nil {
		t.Fatalf("EnsureWatching(nodepools): %v", err)
	}
	if !dynamicCache.WaitForSync(capacityContractNodePoolGVR, 2*time.Second) {
		t.Fatal("NodePool informer did not sync")
	}
}

func capacityContractNodePool(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": karpenter.APIVersionV1,
		"kind":       karpenter.NodePoolKind,
		"metadata":   map[string]any{"name": name, "uid": name + "-uid"},
	}}
}

func mustEncodeCapacityActivityCursor(t *testing.T, epoch, fingerprint string, seq int64) string {
	t.Helper()
	cursor, err := encodeCapacityActivityCursor(epoch, fingerprint, k8s.ActiveClusterContext(), seq, "older")
	if err != nil {
		t.Fatalf("encode activity cursor: %v", err)
	}
	return cursor
}

func TestCapacityActivitySQLitePruningReportsCursorEviction(t *testing.T) {
	initCapacityContractDynamicState(t, true, true, capacityContractNodePool("general"))
	timeline.ResetStore()
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeSQLite, Path: filepath.Join(t.TempDir(), "timeline.db")}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	t.Cleanup(func() {
		timeline.ResetStore()
		if err := timeline.InitStore(timeline.DefaultStoreConfig()); err != nil {
			t.Fatalf("re-init global store: %v", err)
		}
	})

	// Two old events (beyond retention) + one fresh. Prune deletes the old
	// pair; an older cursor pointing into the pruned range must be a gap.
	base := timeline.ObservationStart().Add(-10 * time.Minute)
	events := []pkgtimeline.TimelineEvent{
		capacityActivityConfigEvent("pruned-1", k8s.ActiveClusterContext(), base.Add(-3*time.Hour)),
		capacityActivityConfigEvent("pruned-2", k8s.ActiveClusterContext(), base.Add(-2*time.Hour)),
		capacityActivityConfigEvent("fresh", k8s.ActiveClusterContext(), base),
	}
	if err := timeline.GetStore().AppendBatch(context.Background(), events); err != nil {
		t.Fatalf("append: %v", err)
	}
	pruner, ok := timeline.GetStore().(interface {
		Cleanup(ctx context.Context, maxAge time.Duration) (int64, error)
	})
	if !ok {
		t.Fatal("SQLite store does not expose Cleanup")
	}
	if pruned, err := pruner.Cleanup(context.Background(), time.Hour); err != nil || pruned != 2 {
		t.Fatalf("cleanup pruned %d, err %v; want 2 old events removed", pruned, err)
	}
	if !timeline.GetStore().Stats().EventsEvicted {
		t.Fatal("SQLite pruning did not surface EventsEvicted")
	}

	epoch := strconv.FormatInt(timeline.ObservationStart().UnixNano(), 10)
	evictedCursor := mustEncodeCapacityActivityCursor(t, epoch, capacityFilterFingerprint(url.Values{}), 2)
	var evicted capacityapi.ActivityResponse
	assertOK(t, get(t, "/api/capacity/activity?cursor="+url.QueryEscape(evictedCursor)), &evicted)
	if evicted.CursorStatus != capacityapi.CursorEvicted || evicted.CursorGap == nil {
		t.Fatalf("pruned-history cursor = %q/%#v, want eviction gap", evicted.CursorStatus, evicted.CursorGap)
	}
}

func TestCapacityOverviewAggregateDemandCertaintyFollowsPodScope(t *testing.T) {
	initCapacityContractDynamicState(t, true, true, capacityContractNodePool("general"))

	// Cluster-wide observed pods -> the aggregate is an exact reading.
	var clusterWide capacityapi.OverviewResponse
	assertOK(t, get(t, "/api/capacity"), &clusterWide)
	if clusterWide.Summary.AggregateDemand == nil || clusterWide.Summary.AggregateDemand.Certainty != capacityapi.CertaintyExact {
		t.Fatalf("cluster-wide aggregate demand = %#v, want exact", clusterWide.Summary.AggregateDemand)
	}

	// Namespace-scoped pods -> the aggregate must hedge as a lower bound;
	// presenting it as '=' would fabricate an exact cluster total.
	var scoped capacityapi.OverviewResponse
	assertOK(t, get(t, "/api/capacity?namespaces=default"), &scoped)
	if scoped.Summary.AggregateDemand == nil || scoped.Summary.AggregateDemand.Certainty != capacityapi.CertaintyLowerBound {
		t.Fatalf("namespace-scoped aggregate demand = %#v, want lower_bound", scoped.Summary.AggregateDemand)
	}
}

func TestCapacityDemandOwnerFilterSemantics(t *testing.T) {
	// Parse: well-formed, malformed, and cursor binding via the filters map.
	filters, _, _, owner, _, err := parseCapacityDemandFilters(url.Values{"owner": {"shop/Deployment/web"}})
	if err != nil || owner == nil || *owner != (demandOwnerFilter{Namespace: "shop", Kind: "Deployment", Name: "web"}) {
		t.Fatalf("parsed owner = %#v, err %v", owner, err)
	}
	if filters.Get("owner") != "shop/Deployment/web" {
		t.Fatalf("owner missing from cursor-bound filters: %#v", filters)
	}
	for _, malformed := range []string{"not-a-ref", "a/b", "//x", "a//c"} {
		if _, _, _, _, _, err := parseCapacityDemandFilters(url.Values{"owner": {malformed}}); err == nil {
			t.Fatalf("owner %q parsed without error", malformed)
		}
	}

	// Match: only the subject's groups; kind case-insensitive; ownerless
	// groups never match.
	group := func(namespace, kind, name string) capacitymodel.DemandGroupModel {
		value := capacityapi.NewDemandGroup(time.Unix(0, 0).UTC())
		value.Namespace = namespace
		if kind != "" {
			value.Owner = &subject.Ref{Kind: kind, Namespace: namespace, Name: name}
		}
		return capacitymodel.DemandGroupModel{Group: value}
	}
	want := demandOwnerFilter{Namespace: "shop", Kind: "Deployment", Name: "web"}
	if !demandGroupMatchesOwner(group("shop", "deployment", "web"), want) {
		t.Fatal("kind match must be case-insensitive")
	}
	for _, other := range []capacitymodel.DemandGroupModel{
		group("shop", "Deployment", "api"),
		group("media", "Deployment", "web"),
		group("shop", "Job", "web"),
		group("shop", "", ""),
	} {
		if demandGroupMatchesOwner(other, want) {
			t.Fatalf("unrelated group matched the owner filter: %#v", other.Group)
		}
	}
}

func TestCapacityDemandRejectsMalformedOwnerFilter(t *testing.T) {
	initCapacityContractDynamicState(t, true, true, capacityContractNodePool("general"))
	resp := get(t, "/api/capacity/demand?owner=not-a-ref")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed owner status = %d, want 400", resp.StatusCode)
	}
}

func TestCapacityDemandPodFilterSemantics(t *testing.T) {
	// Parse: well-formed, cursor binding, malformed, and owner exclusivity.
	filters, _, _, _, pod, err := parseCapacityDemandFilters(url.Values{"pod": {"shop/web-1"}})
	if err != nil || pod == nil || *pod != (demandPodFilter{Namespace: "shop", Name: "web-1"}) {
		t.Fatalf("parsed pod = %#v, err %v", pod, err)
	}
	if filters.Get("pod") != "shop/web-1" {
		t.Fatalf("pod missing from cursor-bound filters: %#v", filters)
	}
	for _, malformed := range []string{"just-a-name", "a/b/c", "/x", "x/"} {
		if _, _, _, _, _, err := parseCapacityDemandFilters(url.Values{"pod": {malformed}}); err == nil {
			t.Fatalf("pod %q parsed without error", malformed)
		}
	}
	if _, _, _, _, _, err := parseCapacityDemandFilters(url.Values{
		"pod":   {"shop/web-1"},
		"owner": {"shop/Deployment/web"},
	}); err == nil {
		t.Fatal("pod and owner together must be rejected — they are competing subject filters")
	}

	// Resolution: the filter must key on the SAME owner the grouping computes.
	controller := true
	owned := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Namespace: "shop", Name: "web-1",
		OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-abc123", Controller: &controller}},
	}}
	bare := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "solo"}}
	snapshot := &capacitymodel.Snapshot{
		Pods: []*corev1.Pod{owned, bare},
		ResolvePodOwner: func(p *corev1.Pod) *subject.Ref {
			if p.Name == "web-1" {
				return &subject.Ref{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "web"}
			}
			return nil
		},
	}
	resolved := demandOwnerForPodFilter(snapshot, demandPodFilter{Namespace: "shop", Name: "web-1"})
	if resolved == nil || *resolved != (demandOwnerFilter{Namespace: "shop", Kind: "Deployment", Name: "web"}) {
		t.Fatalf("owned pod resolved to %#v, want its TOP owner (Deployment web), not the ReplicaSet", resolved)
	}
	if bareOwner := demandOwnerForPodFilter(snapshot, demandPodFilter{Namespace: "shop", Name: "solo"}); bareOwner == nil ||
		*bareOwner != (demandOwnerFilter{Namespace: "shop", Kind: "Pod", Name: "solo"}) {
		t.Fatalf("bare pod resolved to %#v, want the pod itself as its own group subject", bareOwner)
	}
	if ghost := demandOwnerForPodFilter(snapshot, demandPodFilter{Namespace: "shop", Name: "ghost"}); ghost != nil {
		t.Fatalf("unseen pod must resolve to nil (matches nothing), got %#v", ghost)
	}
}

func TestCapacityDemandPodFilterHandlerPaths(t *testing.T) {
	initCapacityContractDynamicState(t, true, true, capacityContractNodePool("general"))
	for _, path := range []string{
		"/api/capacity/demand?pod=just-a-name",
		"/api/capacity/demand?pod=shop/web-1&owner=shop/Deployment/web",
	} {
		resp := get(t, path)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400", path, resp.StatusCode)
		}
	}
	// A pod the model never observed matches nothing: an honest empty 200,
	// byte-identical in shape to any other unmatched subject filter — the
	// response must not reveal whether the pod exists.
	resp := get(t, "/api/capacity/demand?pod=nowhere/ghost")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("unseen pod status = %d, want 200", resp.StatusCode)
	}
	var decoded struct {
		Items []json.RawMessage `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(decoded.Items) != 0 {
		t.Fatalf("unseen pod returned %d groups, want 0", len(decoded.Items))
	}
}

// assertCapacitySummaryKeysAbsent pins omission on the WIRE, not on the decoded
// struct: a *int nil and a serialized 0 decode identically for a consumer that
// reads `summary.poolCount ?? 0`, so only the raw key proves "unobserved".
func assertCapacitySummaryKeysAbsent(t *testing.T, wire []byte, keys ...string) {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal(wire, &raw); err != nil {
		t.Fatalf("decode raw response: %v", err)
	}
	summary, ok := raw["summary"].(map[string]any)
	if !ok {
		t.Fatalf("summary is not an object: %s", wire)
	}
	for _, key := range keys {
		if _, found := summary[key]; found {
			t.Fatalf("summary.%s must be omitted when its source was not observed: %s", key, wire)
		}
	}
}

func TestCapacityOverviewObservedNodePoolsKeepKarpenterScopedAggregates(t *testing.T) {
	// The mirror of the denied case: once NodePools are actually listed, the
	// Karpenter-scoped aggregates are real observations and must be present —
	// including poolCount 0 on an installed-but-empty fleet, which is a true zero.
	initCapacityContractDynamicState(t, true, true, capacityContractNodePool("general"))

	var body capacityapi.OverviewResponse
	assertOK(t, get(t, "/api/capacity"), &body)
	if body.State != capacityapi.IntegrationAvailable {
		t.Fatalf("state = %q, want %q", body.State, capacityapi.IntegrationAvailable)
	}
	if body.Summary.PoolCount == nil || *body.Summary.PoolCount != 1 {
		t.Fatalf("observed poolCount = %v, want 1", body.Summary.PoolCount)
	}
	if body.Summary.Scheduling == nil {
		t.Fatal("observed NodePools must carry the Karpenter-scoped scheduling ledger")
	}
	if body.Summary.UnpooledNodeCount == nil {
		t.Fatal("observed NodePools must carry unpooledNodeCount")
	}
}

func TestCapacityOverviewOmitsPodDerivedCountsUnderPodDenial(t *testing.T) {
	// The unpinned half of the omission family: pendingPodCount and
	// aggregateDemand are pod-derived, so a pods denial must drop the keys
	// entirely rather than serialize "0 pending, no demand".
	initCapacityContractDynamicState(t, true, true, capacityContractNodePool("general"))
	env := newAuthTestServer(t)
	permissions := &auth.UserPermissions{AllowedNamespaces: []string{}}
	permissions.SetCanI("list", karpenter.Group, "nodepools", "", true)
	permissions.SetCanI("list", karpenter.Group, "nodeclaims", "", false)
	permissions.SetCanI("list", "", "nodes", "", true)
	permissions.SetCanI("list", "", "pods", "", false)
	permissions.SetCanI("list", "metrics.k8s.io", "nodes", "", false)
	permissions.SetCanI("get", "", "configmaps", "kube-system", false)
	env.srv.permCache.Set("alice", nil, permissions)

	resp := env.authGet(t, "/api/capacity", "alice", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 200; body = %s", resp.StatusCode, body)
	}
	wire, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	var body capacityapi.OverviewResponse
	if err := json.Unmarshal(wire, &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Coverage[capacityapi.CoveragePods].Status != capacityapi.CoverageDenied {
		t.Fatalf("pods coverage = %#v, want denied", body.Coverage[capacityapi.CoveragePods])
	}
	if body.Summary.PendingPodCount != nil || body.Summary.AggregateDemand != nil {
		t.Fatalf("pod-derived counts survived denial: pending=%v demand=%v", body.Summary.PendingPodCount, body.Summary.AggregateDemand)
	}
	assertCapacitySummaryKeysAbsent(t, wire, "pendingPodCount", "aggregateDemand")
	// NodePools were listable, so the Karpenter-scoped aggregates stay.
	if body.Summary.PoolCount == nil {
		t.Fatal("poolCount must survive a pods denial — NodePools were observed")
	}
}

func TestCapacityProjectsNodeRegistrationHealthIssue(t *testing.T) {
	// NodeRegistrationHealthy sits OUTSIDE the pool's aggregate Ready and the
	// failing NodeClaims are deleted at the registration timeout, so this
	// condition is the only durable trace of "nodes are not joining". Without an
	// action code the projection filter dropped it from Capacity entirely.
	pool := capacityContractNodePool("registration-broken")
	pool.SetGeneration(3)
	pool.Object["status"] = map[string]any{"conditions": []any{
		map[string]any{
			"type": "Ready", "status": "True", "observedGeneration": int64(3),
			"lastTransitionTime": time.Now().UTC().Add(-time.Hour).Format(time.RFC3339),
		},
		map[string]any{
			"type": "NodeRegistrationHealthy", "status": "False",
			"reason": "RegistrationFailed", "message": "nodes failed to register",
			"observedGeneration": int64(3),
			"lastTransitionTime": time.Now().UTC().Add(-time.Minute).Format(time.RFC3339),
		},
	}}
	initCapacityContractDynamicState(t, true, true, pool)

	var overview capacityapi.OverviewResponse
	assertOK(t, get(t, "/api/capacity"), &overview)
	var signal *capacityapi.ActionSummary
	for index := range overview.Summary.Actions {
		if overview.Summary.Actions[index].Code == "node_registration_unhealthy" {
			signal = &overview.Summary.Actions[index]
			break
		}
	}
	if signal == nil {
		t.Fatalf("registration-health signal missing from overview actions: %+v", overview.Summary.Actions)
	}
	if signal.Count != 1 || len(signal.Pools) != 1 || signal.Pools[0].Ref.Name != "registration-broken" {
		t.Fatalf("registration-health signal = %+v", signal)
	}

	var detail capacityapi.PoolDetailResponse
	assertOK(t, get(t, "/api/capacity/pools/registration-broken"), &detail)
	if detail.Pool == nil {
		t.Fatal("pool detail missing")
	}
	found := false
	for _, issue := range detail.Pool.Issues {
		if issue.Reason == issues.ReasonKarpenterNodeRegistrationUnhealthy {
			found = true
		}
	}
	if !found {
		t.Fatalf("pool detail dropped the registration-health issue: %+v", detail.Pool.Issues)
	}
}

// TestCapacityZeroNodePoolsClassifiesIdenticallyOnBothLayers pins the ONE
// owner of zero-pool semantics. Overview and Demand build the same demand
// groups from the same pods; when Overview guarded classification on
// len(pools)>0 while Demand ran it unconditionally, a Karpenter cluster with no
// NodePools reported scheduler-level states on Overview and "blocked" on
// Demand — the same fleet, two contradictory answers.
func TestCapacityZeroNodePoolsClassifiesIdenticallyOnBothLayers(t *testing.T) {
	now := time.Now().UTC()
	pending := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "web-0", UID: "web-0-uid"},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name:      "app",
			Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m")}},
		}}},
		Status: corev1.PodStatus{
			Phase: corev1.PodPending,
			Conditions: []corev1.PodCondition{{
				Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable,
				Message: "0/0 nodes are available: 1 Insufficient cpu.",
			}},
		},
	}
	// Karpenter observed (state available) with ZERO NodePools.
	snapshot := &capacitymodel.Snapshot{GeneratedAt: now, Pods: []*corev1.Pod{pending}}
	model := capacitymodel.Build(*snapshot)
	meta := newCapacityResponseMeta(now, currentCapacityClusterIdentity())
	meta.Coverage[capacityapi.CoverageNodePools] = availableClusterCoverage(now, 0, []string{"summary", "pools"})
	result := capacityLoadResult{state: capacityapi.IntegrationAvailable, meta: meta, model: &model, snapshot: snapshot}

	// Overview's path — the handler calls exactly this.
	actions := capacityDemandActions(capacityOverviewDemandGroups(result))

	// Demand's path.
	classificationPools, _ := demandPoolSets(result, "")
	demandGroups := capacitymodel.BuildDemandGroupModels(capacitymodel.DemandInput{GeneratedAt: now, Pods: snapshot.Pods})
	capacitymodel.ClassifyDemandGroupModels(demandGroups, classificationPools)
	summary := capacitymodel.SummarizeDemandGroupModels(demandGroups)

	blocked := summary.ByState[capacityapi.DemandBlocked].GroupCount
	if blocked != 1 {
		t.Fatalf("demand blocked groups = %d, want 1 — with Karpenter observed, no NodePool can take this demand", blocked)
	}
	var overviewBlocked int
	for _, action := range actions {
		if action.Code == "pending_demand_blocked" {
			overviewBlocked = action.Count
		}
	}
	if overviewBlocked != blocked {
		t.Fatalf("overview reports %d blocked groups, demand reports %d — one layer classified against pools the other did not", overviewBlocked, blocked)
	}
}

// TestCapacityZeroNodePoolsStillReportsObservedPoolCount pins the companion
// wire fact: zero pools on a listable Karpenter install is a TRUE zero, not the
// unobserved omission the denied path produces.
func TestCapacityZeroNodePoolsStillReportsObservedPoolCount(t *testing.T) {
	initCapacityContractDynamicState(t, true, true)

	var overview capacityapi.OverviewResponse
	assertOK(t, get(t, "/api/capacity"), &overview)
	if overview.State != capacityapi.IntegrationAvailable {
		t.Fatalf("state = %q, want %q — Karpenter is installed and listable, it just has no pools", overview.State, capacityapi.IntegrationAvailable)
	}
	if overview.Summary.PoolCount == nil || *overview.Summary.PoolCount != 0 {
		t.Fatalf("poolCount = %v, want an observed zero", overview.Summary.PoolCount)
	}
}

// TestCapacityActivityAggregateSpansWindowAndFirstPageOnly pins the two rules
// that make the type-rollup strip readable: the aggregate describes the whole
// filtered window (so the type pills stay stable while the list narrows,
// mirroring the demand-state rollup), and it appears only on first-page
// responses — a cursor page sees a window bounded near the cursor, and an
// aggregate over that slice would misread as the whole window.
func TestCapacityActivityAggregateSpansWindowAndFirstPageOnly(t *testing.T) {
	initCapacityContractDynamicState(t, true, true, capacityContractNodePool("general"))
	timeline.ResetStore()
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 100}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	t.Cleanup(func() {
		timeline.ResetStore()
		if err := timeline.InitStore(timeline.DefaultStoreConfig()); err != nil {
			t.Fatalf("re-init global store: %v", err)
		}
	})

	startedAt := timeline.ObservationStart().Add(time.Second)
	events := []pkgtimeline.TimelineEvent{
		{
			ID: "claim-created", Timestamp: startedAt, Source: pkgtimeline.SourceInformer,
			Kind: karpenter.NodeClaimKind, APIVersion: karpenter.APIVersionV1, Name: "claim-a", UID: "claim-a-uid",
			EventType: pkgtimeline.EventTypeAdd, ClusterContext: k8s.ActiveClusterContext(),
			Owner: &k8score.OwnerInfo{Kind: karpenter.NodePoolKind, Name: "general"},
		},
		{
			ID: "claim-ready", Timestamp: startedAt.Add(time.Minute), Source: pkgtimeline.SourceK8sEvent,
			Kind: karpenter.NodeClaimKind, APIVersion: karpenter.APIVersionV1, Name: "claim-a", UID: "claim-a-uid",
			Reason: "Ready", EventType: pkgtimeline.EventTypeNormal, ClusterContext: k8s.ActiveClusterContext(),
		},
		capacityActivityConfigEvent("pool-updated", k8s.ActiveClusterContext(), startedAt.Add(2*time.Minute)),
	}
	if err := timeline.GetStore().AppendBatch(context.Background(), events); err != nil {
		t.Fatalf("append activity: %v", err)
	}

	var all capacityapi.ActivityResponse
	assertOK(t, get(t, "/api/capacity/activity"), &all)
	if all.Aggregate == nil {
		t.Fatal("first page omitted the aggregate rollup")
	}
	if all.Aggregate.Total != 2 {
		t.Fatalf("aggregate total = %d, want 2 (one provision episode + one config change)", all.Aggregate.Total)
	}
	if all.Aggregate.ByType[capacityapi.ActivityProvision].Total != 1 || all.Aggregate.ByType[capacityapi.ActivityConfigChange].Total != 1 {
		t.Fatalf("aggregate byType = %#v", all.Aggregate.ByType)
	}

	// The type filter narrows the ITEMS, never the rollup.
	var filtered capacityapi.ActivityResponse
	assertOK(t, get(t, "/api/capacity/activity?type=config_change"), &filtered)
	if len(filtered.Items) != 1 || filtered.Items[0].Type != capacityapi.ActivityConfigChange {
		t.Fatalf("type-filtered items = %#v, want only the config change", filtered.Items)
	}
	if filtered.Aggregate == nil || !reflect.DeepEqual(*filtered.Aggregate, *all.Aggregate) {
		t.Fatalf("type filter narrowed the aggregate: all=%#v filtered=%#v", all.Aggregate, filtered.Aggregate)
	}

	// Cursor pages carry no aggregate at all.
	var first capacityapi.ActivityResponse
	assertOK(t, get(t, "/api/capacity/activity?limit=1"), &first)
	if first.Page.NextCursor == "" {
		t.Fatal("first page omitted the older cursor")
	}
	resp := get(t, "/api/capacity/activity?cursor="+url.QueryEscape(first.Page.NextCursor))
	defer resp.Body.Close()
	wire, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read cursor page: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(wire, &raw); err != nil {
		t.Fatalf("decode cursor page: %v", err)
	}
	if _, found := raw["aggregate"]; found {
		t.Fatalf("cursor page carried an aggregate over its bounded slice: %s", wire)
	}
}
