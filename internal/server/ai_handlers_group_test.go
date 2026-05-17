package server

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/skyhook-io/radar/internal/k8s"
)

// Group-qualified AI GET must route to the dynamic cache so CRDs whose
// plural shadows a core kind (Knative serving.knative.dev/Service vs
// core/v1 Service) resolve to the requested object — not whichever the
// typed cache happens to hold under that kind/name pair.
//
// Without the group-first branch in fetchAIResource, FetchResource(
// "services", ...) returns the core/v1 Service from the typed informer
// and ?group=serving.knative.dev is silently dropped. The bug surfaces
// as wrong-object disclosure on the AI surface: a caller asking for the
// Knative Service receives the core Service's spec + IP + selector
// instead. This pins the fix and would regress if the typed cache is
// consulted before the group qualifier.
//
// Same bug class as T12's group-blind root lookup, but on the single-
// resource GET path; ResourceContext relationship walks already disambig
// by group (see pkg/topology/managedby_test.go), so a regression here is
// the last remaining hot spot for kind/plural collisions on the GET API.
func TestAIGetResource_GroupRoutesToDynamic(t *testing.T) {
	// Seed a Knative Service named "nginx" in "default" — same name+ns as
	// the core Service registered in TestMain. Without ?group routing, the
	// typed cache wins and returns the core Service. With it, the dynamic
	// cache returns the Knative Service.
	knativeGVR := schema.GroupVersionResource{Group: "serving.knative.dev", Version: "v1", Resource: "services"}
	knativeSvc := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "serving.knative.dev/v1",
			"kind":       "Service",
			"metadata": map[string]any{
				"name":      "nginx",
				"namespace": "default",
			},
			"spec": map[string]any{
				"template": map[string]any{
					"spec": map[string]any{
						"containers": []any{
							map[string]any{"image": "gcr.io/example/hello:1"},
						},
					},
				},
			},
		},
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{knativeGVR: "ServiceList"},
		knativeSvc,
	)

	resources := []k8s.APIResource{
		{
			Group:      "serving.knative.dev",
			Version:    "v1",
			Kind:       "Service",
			Name:       "services",
			Namespaced: true,
			IsCRD:      true,
			Verbs:      []string{"get", "list", "watch"},
		},
	}
	if err := k8s.InitTestDynamicResourceCache(dyn, resources); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)

	// Warm the informer so the Get() call below sees the seeded object
	// without racing on initial sync.
	dynCache := k8s.GetDynamicResourceCache()
	if dynCache == nil {
		t.Fatal("dynamic cache not initialized")
	}
	if err := dynCache.EnsureWatching(knativeGVR); err != nil {
		t.Fatalf("EnsureWatching: %v", err)
	}
	if !dynCache.WaitForSync(knativeGVR, 5*time.Second) {
		t.Fatal("timed out waiting for Knative Service informer sync")
	}

	resp, err := http.Get(testServer.URL + "/api/ai/resources/services/default/nginx?group=serving.knative.dev&context=none")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	// context=none returns the minified resource directly (no envelope).
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	apiVersion, _ := body["apiVersion"].(string)
	if apiVersion != "serving.knative.dev/v1" {
		t.Fatalf("apiVersion = %q, want serving.knative.dev/v1 — group qualifier was ignored "+
			"and the typed cache's core Service was returned instead", apiVersion)
	}
	kind, _ := body["kind"].(string)
	if kind != "Service" {
		t.Errorf("kind = %q, want Service", kind)
	}
	// Cross-check: the core Service has a Spec.Selector / ClusterIP shape
	// that the Knative seed does NOT have. A regression that returned the
	// core Service would carry those fields here.
	spec, _ := body["spec"].(map[string]any)
	if _, hasSelector := spec["selector"]; hasSelector {
		t.Errorf("response carries Service.spec.selector — looks like the core Service leaked through "+
			"despite ?group=serving.knative.dev; body=%+v", body)
	}
}

// Group-qualified AI GET must also route the topology relationship lookup
// to the matching pseudo-kind node. The bug: handleAIGetResource passed the
// URL plural "services" straight into topology.GetRelationshipsWithObject,
// which feeds buildNodeID — and buildNodeID's kindMap resolves "services"
// to "service", landing on the CORE Service's topology node. For a Knative
// Service request, the response then carried the core Service's incoming
// Ingress edge as resourceContext.exposes, which is provably wrong.
//
// Fix: derive a topology-pseudo-kind via topology.KindForGVK(gvk.Kind,
// gvk.Group) — for Knative Service, that yields "knativeservice", whose
// node has no Ingress edge in this fixture and therefore no Exposes.
//
// Differentiator: the TestMain fixture seeds an Ingress backend-ref'd to
// the core Service "nginx" in "default". The Knative Service "nginx" in
// "default" (seeded below into the dynamic cache) is a separate topology
// node with NO incoming Ingress edges. The test asserts that the
// resourceContext returned for the ?group=serving.knative.dev request
// does NOT advertise that Ingress — the same fixture, when queried
// without ?group, DOES surface it (locked down by the trailing sub-test
// to pin the regression's pre-fix shape and prevent a future change that
// silently drops the core-side relationship as well).
func TestAIGetResource_GroupRoutesRelationshipsToKnative(t *testing.T) {
	knativeGVR := schema.GroupVersionResource{Group: "serving.knative.dev", Version: "v1", Resource: "services"}
	knativeSvc := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "serving.knative.dev/v1",
			"kind":       "Service",
			"metadata": map[string]any{
				"name":      "nginx",
				"namespace": "default",
			},
			"spec": map[string]any{
				"template": map[string]any{
					"spec": map[string]any{
						"containers": []any{
							map[string]any{"image": "gcr.io/example/hello:1"},
						},
					},
				},
			},
		},
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{knativeGVR: "ServiceList"},
		knativeSvc,
	)
	resources := []k8s.APIResource{
		{
			Group:      "serving.knative.dev",
			Version:    "v1",
			Kind:       "Service",
			Name:       "services",
			Namespaced: true,
			IsCRD:      true,
			Verbs:      []string{"get", "list", "watch"},
		},
	}
	if err := k8s.InitTestDynamicResourceCache(dyn, resources); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)

	dynCache := k8s.GetDynamicResourceCache()
	if dynCache == nil {
		t.Fatal("dynamic cache not initialized")
	}
	if err := dynCache.EnsureWatching(knativeGVR); err != nil {
		t.Fatalf("EnsureWatching: %v", err)
	}
	if !dynCache.WaitForSync(knativeGVR, 5*time.Second) {
		t.Fatal("timed out waiting for Knative Service informer sync")
	}

	// The Knative Service request MUST NOT inherit the core Service's
	// Ingress in resourceContext.exposes. Pre-fix, the URL "services" was
	// passed into buildNodeID and resolved to "service/default/nginx" —
	// the wrong topology node — so the Ingress leaked.
	resp, err := http.Get(testServer.URL + "/api/ai/resources/services/default/nginx?group=serving.knative.dev")
	if err != nil {
		t.Fatalf("GET (knative): %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var knBody map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&knBody); err != nil {
		t.Fatalf("decode (knative): %v", err)
	}
	knRC, _ := knBody["resourceContext"].(map[string]any)
	if knRC == nil {
		t.Fatal("knative response missing resourceContext")
	}
	exposes, _ := knRC["exposes"].([]any)
	for _, e := range exposes {
		em, _ := e.(map[string]any)
		kind, _ := em["kind"].(string)
		name, _ := em["name"].(string)
		if kind == "Ingress" && name == "nginx-ingress" {
			t.Fatalf("knative-routed request leaked the core Service's Ingress into resourceContext.exposes "+
				"(got %+v) — relationship lookup did NOT remap to the knativeservice topology node; "+
				"check that handleAIGetResource is funneling kind through topology.KindForGVK", exposes)
		}
	}

	// Co-anchored sibling: when no ?group is passed, the same path resolves
	// to the core Service node and MUST still surface the Ingress. This
	// half guards against an over-correction that nukes the relationship
	// lookup for the dominant typed-cache case while fixing the CRD case.
	respCore, err := http.Get(testServer.URL + "/api/ai/resources/services/default/nginx")
	if err != nil {
		t.Fatalf("GET (core): %v", err)
	}
	defer respCore.Body.Close()
	var coreBody map[string]any
	if err := json.NewDecoder(respCore.Body).Decode(&coreBody); err != nil {
		t.Fatalf("decode (core): %v", err)
	}
	coreRC, _ := coreBody["resourceContext"].(map[string]any)
	coreExposes, _ := coreRC["exposes"].([]any)
	foundIngress := false
	for _, e := range coreExposes {
		em, _ := e.(map[string]any)
		if em["kind"] == "Ingress" && em["name"] == "nginx-ingress" {
			foundIngress = true
			break
		}
	}
	if !foundIngress {
		t.Errorf("core Service request lost the Ingress from resourceContext.exposes (got %+v) — "+
			"the fix overshot and broke the typed-cache relationship lookup", coreExposes)
	}
}

// Pin Finding 1: the AI GET handler used to pass the URL-plural kind
// ("deployments") into computeIssueSummaryForResource, which forwards
// it to issues.Compose via Filters.Kinds. The composer's applyFilters
// case-folds both sides (strings.ToLower) but does NOT plural-to-singular
// convert — and Issue.Kind is the canonical Pascal singular ("Deployment").
// So the filter set {"deployments"} never matched lower("Deployment") =
// "deployment", every issue got dropped, and IssueSummary.Count silently
// collapsed to 0 (Build then omits the field entirely).
//
// Fix: pass canonicalKind (derived from obj.GVK) into
// computeIssueSummaryForResource so the filter is "Deployment" → matched.
//
// Fixture: TestMain seeds Deployment broken/stuck-app with
// UnavailableReplicas=3. DetectProblems emits a Pascal-singular
// "Deployment" problem for it. Hitting /api/ai/resources/deployments/...
// (URL plural) must surface the issue in resourceContext.issueSummary
// with count > 0 — pre-fix this came back as null.
func TestAIGetResource_IssueSummaryCountsURLPluralKind(t *testing.T) {
	resp, err := http.Get(testServer.URL + "/api/ai/resources/deployments/broken/stuck-app")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	rc, _ := body["resourceContext"].(map[string]any)
	if rc == nil {
		t.Fatal("response missing resourceContext")
	}
	issueSum, _ := rc["issueSummary"].(map[string]any)
	if issueSum == nil {
		t.Fatalf("resourceContext.issueSummary is nil — composer filter dropped every issue. "+
			"Likely the handler is still passing URL-plural kind ('deployments') into "+
			"computeIssueSummaryForResource instead of canonical Pascal singular ('Deployment'). "+
			"Got: %+v", rc)
	}
	count, _ := issueSum["count"].(float64)
	if count < 1 {
		t.Fatalf("issueSummary.count = %v, want >= 1 — DetectProblems should have flagged "+
			"the broken/stuck-app Deployment (UnavailableReplicas=3)", count)
	}
}

// Happy-path sibling for the test above: when no group is passed, the
// typed-cache-first path is correct (and must continue to be — the v1
// core Service is the dominant case and must not pay a dynamic-cache
// detour just because the group-qualified branch was added).
func TestAIGetResource_NoGroupHitsTypedCache(t *testing.T) {
	resp, err := http.Get(testServer.URL + "/api/ai/resources/services/default/nginx?context=none")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	apiVersion, _ := body["apiVersion"].(string)
	if apiVersion != "v1" {
		t.Fatalf("apiVersion = %q, want v1 (core Service) on no-group request", apiVersion)
	}
}
