package k8s

import (
	"context"
	"reflect"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/pkg/k8score"
)

// dynamicCapabilityKinds lists ResourcePermissions fields that are surfaced
// via the dynamic-cache path rather than a typed informer in pkg/k8score.
// Each entry must have a matching candidate in supportedCRDFallbacks.
//
// These kinds are also "optional" from a UI perspective: a false bool means
// "CRD not installed", not "RBAC denied something Radar expected". When
// changing this list, update OPTIONAL_RESOURCE_KINDS in
// packages/k8s-ui/src/types/core.ts so the frontend keeps filtering them
// out of the limited-access banner.
//
// Keep this list small and explicit — if you find yourself adding many
// entries, the typed/dynamic boundary in this codebase has shifted and the
// design needs revisiting, not the allowlist.
var dynamicCapabilityKinds = map[string]bool{
	"gateways":               true,
	"httproutes":             true,
	"verticalpodautoscalers": true,
}

// TestCapabilitiesAlignment_AllFieldsProbed asserts every ResourcePermissions
// field has a matching probe entry. Catches the "added a struct field, forgot
// the probe" failure mode.
func TestCapabilitiesAlignment_AllFieldsProbed(t *testing.T) {
	perms := &ResourcePermissions{}
	probes := resourceProbeTargets(perms)

	// Build a set of field *bool addresses the probes write into.
	probeFields := make(map[uintptr]string, len(probes))
	for _, p := range probes {
		probeFields[reflect.ValueOf(p.field).Pointer()] = p.key
	}

	permsVal := reflect.ValueOf(perms).Elem()
	permsType := permsVal.Type()
	for i := 0; i < permsType.NumField(); i++ {
		field := permsType.Field(i)
		addr := permsVal.Field(i).Addr().Pointer()
		if _, ok := probeFields[addr]; !ok {
			t.Errorf("ResourcePermissions.%s (json:%q) has no probe entry in resourceProbeTargets — "+
				"adding a field requires a matching probe so the bool gets populated.",
				field.Name, field.Tag.Get("json"))
		}
	}

	// Catch a copy-paste hazard: two probes pointing at the same struct
	// field. The map above silently collapses duplicates, so the loop
	// would still pass while one of the two probes ran for nothing.
	if len(probeFields) != len(probes) {
		t.Errorf("duplicate field pointer detected: %d probes but only %d distinct field addresses — "+
			"two probe entries are writing to the same ResourcePermissions field.",
			len(probes), len(probeFields))
	}
}

// TestCapabilitiesAlignment_TypedVsDynamic asserts that every probe key
// either has a typed informer in pkg/k8score OR is in the explicit
// dynamicCapabilityKinds allowlist with a matching supportedCRDFallbacks
// entry. Catches "added a probe but no way to actually surface data".
func TestCapabilitiesAlignment_TypedVsDynamic(t *testing.T) {
	probes := resourceProbeTargets(&ResourcePermissions{})

	typedKeys := make(map[string]bool, len(probes))
	for _, k := range k8score.InformerResourceKeys() {
		typedKeys[k] = true
	}

	dynamicByGVR := make(map[schema.GroupVersionResource]bool, len(supportedCRDFallbacks))
	for _, c := range supportedCRDFallbacks {
		for _, v := range c.Versions {
			dynamicByGVR[schema.GroupVersionResource{Group: c.Group, Version: v, Resource: c.Resource}] = true
		}
	}

	for _, p := range probes {
		if dynamicCapabilityKinds[p.key] {
			if !p.requiresDiscovery {
				t.Errorf("probe %q is in dynamicCapabilityKinds but missing requiresDiscovery: true — "+
					"dynamic CRDs must gate on IsNotFound or capabilities will lie when the CRD isn't installed.", p.key)
			}
			if !dynamicByGVR[p.gvr] {
				t.Errorf("probe %q (dynamic) has GVR %v with no matching supportedCRDFallbacks entry — "+
					"the dynamic cache won't serve this kind even if discovery sees it.", p.key, p.gvr)
			}
			continue
		}
		if !typedKeys[p.key] {
			t.Errorf("probe %q has no typed informer in pkg/k8score and isn't in dynamicCapabilityKinds — "+
				"either add a typed informer in pkg/k8score.buildInformerSetups or add %q to dynamicCapabilityKinds (and supportedCRDFallbacks).",
				p.key, p.key)
		}
		if p.requiresDiscovery {
			t.Errorf("probe %q is a typed informer but has requiresDiscovery: true — "+
				"only dynamic CRDs need the IsNotFound gate.", p.key)
		}
	}
}

// TestCapabilitiesAlignment_JSONTagMatchesProbeKey asserts the JSON tag,
// lowercased, equals the probe key. This is the actual contract — a future
// field that breaks it (e.g. tag "snake_case") will fail this test.
//
// Without this, a typed field could get a JSON name that frontend consumers
// expect to look up (via key lowercasing) while the probe writes to a
// different map slot — silent miss.
func TestCapabilitiesAlignment_JSONTagMatchesProbeKey(t *testing.T) {
	perms := &ResourcePermissions{}
	probes := resourceProbeTargets(perms)

	keyByField := make(map[uintptr]string, len(probes))
	for _, p := range probes {
		keyByField[reflect.ValueOf(p.field).Pointer()] = p.key
	}

	permsVal := reflect.ValueOf(perms).Elem()
	permsType := permsVal.Type()
	for i := 0; i < permsType.NumField(); i++ {
		field := permsType.Field(i)
		jsonTag := field.Tag.Get("json")
		if jsonTag == "" {
			t.Errorf("ResourcePermissions.%s has no json tag", field.Name)
			continue
		}
		expectedKey := strings.ToLower(jsonTag)
		actualKey, ok := keyByField[permsVal.Field(i).Addr().Pointer()]
		if !ok {
			continue // Reported by TestCapabilitiesAlignment_AllFieldsProbed.
		}
		if actualKey != expectedKey {
			t.Errorf("ResourcePermissions.%s json:%q lowercases to %q but probe key is %q — "+
				"these must match so reflection lookups (informer enabled map, dynamic cache, etc.) work.",
				field.Name, jsonTag, expectedKey, actualKey)
		}
	}
}

// TestCapabilitiesAlignment_FullyAllowedProbeSetsEveryField is the end-to-end
// smoke: run a fully-allowed probe and assert every ResourcePermissions
// field comes back true. If any field is unreachable from the probe pass,
// this fails — the catch-all behind invariants A/B/C.
func TestCapabilitiesAlignment_FullyAllowedProbeSetsEveryField(t *testing.T) {
	dyn := fakeDyn(t, func(_ schema.GroupVersionResource, _ string) bool { return true })

	result, hadErrors := probeResourceAccess(context.Background(), dyn, nil, false)
	if hadErrors {
		t.Fatalf("hadErrors should be false on a fully-allowed run")
	}

	permsVal := reflect.ValueOf(result.Perms).Elem()
	permsType := permsVal.Type()
	for i := 0; i < permsType.NumField(); i++ {
		field := permsType.Field(i)
		if !permsVal.Field(i).Bool() {
			t.Errorf("ResourcePermissions.%s should be true after a fully-allowed probe pass — "+
				"the probe doesn't reach this field. Check resourceProbeTargets and the field address mapping.",
				field.Name)
		}
	}
}

// TestCapabilitiesAlignment_DynamicVersionsCoverage asserts every probe
// marked requiresDiscovery resolves to at least one GVR via
// supportedCRDFallbacks. Catches the failure mode that motivated this:
// a hardcoded v1 probe diverging from the supportedCRDFallbacks version
// list, leading to capabilities reporting `false` on clusters serving
// only v1beta1.
func TestCapabilitiesAlignment_DynamicVersionsCoverage(t *testing.T) {
	probes := resourceProbeTargets(&ResourcePermissions{})
	for _, p := range probes {
		if !p.requiresDiscovery {
			continue
		}
		gvrs := resolveProbeGVRs(p)
		if len(gvrs) == 0 {
			t.Errorf("dynamic probe %q resolved to zero GVRs — supportedCRDFallbacks must list at least one version for (group=%q, resource=%q).",
				p.key, p.gvr.Group, p.gvr.Resource)
			continue
		}
		// Every resolved GVR must match the (group, resource) of the probe.
		for _, gvr := range gvrs {
			if gvr.Group != p.gvr.Group || gvr.Resource != p.gvr.Resource {
				t.Errorf("dynamic probe %q resolved GVR %v whose group/resource doesn't match probe gvr %v — supportedCRDFallbacks lookup is buggy.",
					p.key, gvr, p.gvr)
			}
		}
	}
}

// TestProbeKindAccess covers both single-version typed probes and
// multi-version dynamic probes. The dynamic-CRD cases are the load-bearing
// ones — they validate that we don't false-negative on clusters serving
// only a beta version of Gateway API or VPA.
func TestProbeKindAccess(t *testing.T) {
	gateways := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Resource: "gateways"}

	// Find the live Gateway probe so we use the real probe configuration.
	var gatewayProbe resourceProbe
	for _, p := range resourceProbeTargets(&ResourcePermissions{}) {
		if p.key == "gateways" {
			gatewayProbe = p
			break
		}
	}
	if gatewayProbe.field == nil {
		t.Fatal("gateway probe not found — alignment with resourceProbeTargets broken")
	}
	if !gatewayProbe.requiresDiscovery {
		t.Fatal("gateway probe should have requiresDiscovery=true")
	}

	// Sanity: supportedCRDFallbacks should have multiple versions for gateways.
	gvrs := resolveProbeGVRs(gatewayProbe)
	if len(gvrs) < 2 {
		t.Fatalf("expected multiple version GVRs for gateways probe, got %v — test premise no longer holds", gvrs)
	}

	// --- Dynamic probe: any version available → allowed.
	// Simulates "v1beta1 installed, v1 not yet" — the load-bearing case
	// this whole multi-version walk exists to handle.
	t.Run("dynamic_anyVersionAvailable_allowed", func(t *testing.T) {
		dyn := fakeDynForGVRs(t, func(gvr schema.GroupVersionResource, _ string) (allow bool, notFound bool) {
			if gvr.Group == gateways.Group && gvr.Resource == gateways.Resource && gvr.Version == "v1beta1" {
				return true, false
			}
			if gvr.Group == gateways.Group && gvr.Resource == gateways.Resource {
				return false, true // v1 not installed
			}
			return true, false
		})
		allowed, forbidden, transient := probeKindAccess(context.Background(), dyn, gatewayProbe, "")
		if !allowed || forbidden || transient != nil {
			t.Errorf("v1 NotFound but v1beta1 allowed should report allowed=true, got allowed=%v forbidden=%v transient=%v", allowed, forbidden, transient)
		}
	})

	// --- Dynamic probe: all versions NotFound → denied (CRD not installed).
	t.Run("dynamic_allVersionsNotFound_denied", func(t *testing.T) {
		dyn := fakeDynForGVRs(t, func(gvr schema.GroupVersionResource, _ string) (allow bool, notFound bool) {
			if gvr.Group == gateways.Group && gvr.Resource == gateways.Resource {
				return false, true
			}
			return true, false
		})
		allowed, forbidden, transient := probeKindAccess(context.Background(), dyn, gatewayProbe, "")
		if allowed || forbidden || transient != nil {
			t.Errorf("CRD not installed at any version should deny cleanly, got allowed=%v forbidden=%v transient=%v", allowed, forbidden, transient)
		}
	})

	// --- Dynamic probe: any version Forbidden → denied (RBAC). RBAC rules
	// are version-agnostic in K8s so we short-circuit on the first 403.
	t.Run("dynamic_anyVersionForbidden_denied", func(t *testing.T) {
		dyn := fakeDyn(t, func(gvr schema.GroupVersionResource, _ string) bool {
			return !(gvr.Group == gateways.Group && gvr.Resource == gateways.Resource)
		})
		allowed, forbidden, transient := probeKindAccess(context.Background(), dyn, gatewayProbe, "")
		if allowed || !forbidden || transient != nil {
			t.Errorf("forbidden on any version should report forbidden=true, got allowed=%v forbidden=%v transient=%v", allowed, forbidden, transient)
		}
	})

	// --- Typed probe: behaves like probeListAccessWith.
	t.Run("typed_singleGVRBehavior", func(t *testing.T) {
		var podsProbe resourceProbe
		for _, p := range resourceProbeTargets(&ResourcePermissions{}) {
			if p.key == "pods" {
				podsProbe = p
				break
			}
		}
		if podsProbe.requiresDiscovery {
			t.Fatal("pods probe should have requiresDiscovery=false")
		}
		dyn := fakeDyn(t, func(_ schema.GroupVersionResource, _ string) bool { return true })
		allowed, forbidden, transient := probeKindAccess(context.Background(), dyn, podsProbe, "")
		if !allowed || forbidden || transient != nil {
			t.Errorf("typed pods probe with allow-all should report allowed, got allowed=%v forbidden=%v transient=%v", allowed, forbidden, transient)
		}
	})

	// --- Mixed Forbidden + NotFound across versions: must report forbidden
	// regardless of iteration order. RBAC denial on any version short-
	// circuits, so this exercises that the NotFound on the other version
	// doesn't fool the loop into ignoring the 403.
	t.Run("dynamic_mixedForbiddenAndNotFound_denies", func(t *testing.T) {
		dyn := fakeDynForGVRs(t, func(gvr schema.GroupVersionResource, _ string) (allow bool, notFound bool) {
			if gvr.Group != gateways.Group || gvr.Resource != gateways.Resource {
				return true, false
			}
			if gvr.Version == "v1" {
				return false, true // NotFound on v1
			}
			return false, false // Forbidden on v1beta1
		})
		allowed, forbidden, transient := probeKindAccess(context.Background(), dyn, gatewayProbe, "")
		if allowed || !forbidden || transient != nil {
			t.Errorf("mixed Forbidden+NotFound should deny via forbidden=true, got allowed=%v forbidden=%v transient=%v", allowed, forbidden, transient)
		}
	})

	// --- Non-NotFound transient + NotFound: must preserve optimistic-allow
	// with the transient bubbled up so the cache TTL shortens (5s, not 60s).
	// Without this, a momentary apiserver hiccup on one version would
	// permanently disable Gateway API for the session.
	t.Run("dynamic_transientAndNotFound_optimisticAllow", func(t *testing.T) {
		dyn := fakeDynListReactor(t, func(gvr schema.GroupVersionResource, _ string) (runtime.Object, error) {
			if gvr.Group != gateways.Group || gvr.Resource != gateways.Resource {
				return nil, nil // allow
			}
			if gvr.Version == "v1" {
				// 503 service-unavailable — a real transient.
				return nil, apierrors.NewServiceUnavailable("apiserver hiccup")
			}
			// NotFound on v1beta1.
			return nil, apierrors.NewNotFound(schema.GroupResource{Group: gvr.Group, Resource: gvr.Resource}, "")
		})
		allowed, forbidden, transient := probeKindAccess(context.Background(), dyn, gatewayProbe, "")
		if !allowed || forbidden {
			t.Errorf("transient+NotFound should optimistic-allow, got allowed=%v forbidden=%v", allowed, forbidden)
		}
		if transient == nil {
			t.Errorf("transient+NotFound should preserve the transient error so the cache TTL shortens")
		}
	})

	// --- resolveProbeGVRs empty-fallback branch: a dynamic probe whose GVR
	// isn't registered in supportedCRDFallbacks should still return its
	// configured GVR. The alignment test catches this for real probes, but
	// guard against a future simplification that drops the len(out)>0
	// safety net and returns nil.
	t.Run("resolveProbeGVRs_emptyFallback", func(t *testing.T) {
		var sentinel bool
		syntheticGVR := schema.GroupVersionResource{Group: "synthetic.example.com", Version: "v1", Resource: "widgets"}
		p := resourceProbe{
			key:               "synthetic-widgets",
			gvr:               syntheticGVR,
			field:             &sentinel,
			requiresDiscovery: true,
		}
		got := resolveProbeGVRs(p)
		if len(got) != 1 || got[0] != syntheticGVR {
			t.Errorf("dynamic probe without supportedCRDFallbacks entry should fall back to [p.gvr], got %v", got)
		}
	})
}

// fakeDynListReactor builds a dynamic.Interface whose list calls run an
// arbitrary reactor returning (object, error). Use when the predicate-based
// fakeDyn helpers aren't expressive enough — e.g. tests that need to inject
// specific error types like ServiceUnavailable.
func fakeDynListReactor(t *testing.T, react func(gvr schema.GroupVersionResource, namespace string) (runtime.Object, error)) dynamic.Interface {
	t.Helper()
	scheme := runtime.NewScheme()
	gvrToListKind := map[schema.GroupVersionResource]string{}
	perms := &ResourcePermissions{}
	for _, p := range resourceProbeTargets(perms) {
		for _, gvr := range resolveProbeGVRs(p) {
			gvrToListKind[gvr] = gvr.Resource + "List"
		}
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind)
	client.PrependReactor("list", "*", func(action clienttesting.Action) (bool, runtime.Object, error) {
		la, ok := action.(clienttesting.ListAction)
		if !ok {
			return false, nil, nil
		}
		obj, err := react(la.GetResource(), la.GetNamespace())
		if err != nil {
			return true, nil, err
		}
		if obj != nil {
			return true, obj, nil
		}
		return false, nil, nil // fall through (empty list)
	})
	return client
}

// fakeDynForGVRs is like fakeDyn but the predicate returns (allow, notFound)
// so tests can distinguish "denied by RBAC" (allow=false, notFound=false →
// 403 Forbidden) from "version not installed" (allow=false, notFound=true →
// 404 NotFound) — the load-bearing distinction probeKindAccess depends on
// for the discovery gate.
func fakeDynForGVRs(t *testing.T, gate func(gvr schema.GroupVersionResource, namespace string) (allow bool, notFound bool)) dynamic.Interface {
	t.Helper()
	scheme := runtime.NewScheme()
	gvrToListKind := map[schema.GroupVersionResource]string{}
	perms := &ResourcePermissions{}
	for _, p := range resourceProbeTargets(perms) {
		for _, gvr := range resolveProbeGVRs(p) {
			gvrToListKind[gvr] = gvr.Resource + "List"
		}
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrToListKind)
	client.PrependReactor("list", "*", func(action clienttesting.Action) (bool, runtime.Object, error) {
		la, ok := action.(clienttesting.ListAction)
		if !ok {
			return false, nil, nil
		}
		gvr := la.GetResource()
		ns := la.GetNamespace()
		allow, notFound := gate(gvr, ns)
		if allow {
			return false, nil, nil
		}
		if notFound {
			return true, nil, apierrors.NewNotFound(
				schema.GroupResource{Group: gvr.Group, Resource: gvr.Resource},
				"",
			)
		}
		return true, nil, apierrors.NewForbidden(
			schema.GroupResource{Group: gvr.Group, Resource: gvr.Resource},
			"",
			nil,
		)
	})
	return client
}
