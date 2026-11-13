package k8s

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/issuesapi"
)

func traefikAssessor(t *testing.T) *TraefikRouteAssessor {
	t.Helper()
	return NewTraefikRouteAssessor(GetResourceCache(), GetDynamicResourceCache(), GetResourceDiscovery())
}

func missingVerdicts(refs []TraefikRuleRefs) []TraefikRefVerdict {
	var out []TraefikRefVerdict
	for _, r := range refs {
		for _, v := range append(append([]TraefikRefVerdict{}, r.Services...), r.Middlewares...) {
			if v.Missing != nil && *v.Missing {
				out = append(out, v)
			}
		}
	}
	return out
}

// Every route the detector reports has a missing verdict, and no other route does.
func TestTraefikRuleRefsAgreeWithDetector(t *testing.T) {
	defer ResetTestState()
	defer ResetTestDynamicState()
	initTraefikFixture(t, traefikPod("traefik", true, traefikArgs...))

	reported := map[string]bool{}
	for _, d := range DetectLiveTraefikMissingRefs(GetResourceCache(), GetDynamicResourceCache(), GetResourceDiscovery(), "", nil) {
		reported[d.Namespace+"/"+d.Name] = true
	}
	gvr := schema.GroupVersionResource{Group: "traefik.io", Version: "v1alpha1", Resource: "ingressroutes"}
	routes, err := GetDynamicResourceCache().ListWatched(gvr)
	if err != nil {
		t.Fatal(err)
	}
	a := traefikAssessor(t)
	for _, route := range routes {
		key := route.GetNamespace() + "/" + route.GetName()
		missing := missingVerdicts(a.RuleRefs(route))
		if (len(missing) > 0) != reported[key] {
			t.Errorf("%s: missing verdicts %+v, detector reported %v", key, missing, reported[key])
		}
		if key == "shop/via-chain" && (len(missing) != 1 || missing[0].Via != `chain Middleware "shop/secure-chain"`) {
			t.Errorf("via-chain verdict = %+v", missing)
		}
	}
}

func TestTraefikRuleRefsPresent(t *testing.T) {
	defer ResetTestState()
	defer ResetTestDynamicState()
	initTraefikFixture(t, traefikPod("traefik", true, traefikArgs...))
	refs := traefikAssessor(t).RuleRefs(ingressRoute("shop", "ok", nil, append(svcRef("web"), map[string]any{"name": "weighted", "kind": "TraefikService"}), mwRef("strip")))
	if len(refs) != 1 || len(refs[0].Services) != 2 || len(refs[0].Middlewares) != 1 {
		t.Fatalf("refs = %+v", refs)
	}
	for _, v := range append(refs[0].Services, refs[0].Middlewares...) {
		if v.Missing == nil || *v.Missing {
			t.Errorf("%s %s/%s should be present, got %v", v.Kind, v.Namespace, v.Name, v.Missing)
		}
		if !hasRead(v.Reads, issuesapi.EvidenceRead{Resource: "pods", Namespace: "traefik", Verb: "list"}) {
			t.Errorf("%s %s needs the Traefik pods read: %+v", v.Kind, v.Name, v.Reads)
		}
	}
	if refs[0].Services[1].Kind != "TraefikService" {
		t.Errorf("kind = %q", refs[0].Services[1].Kind)
	}
}

func allCantTell(t *testing.T, name string, refs []TraefikRuleRefs) {
	t.Helper()
	if len(refs) == 0 {
		t.Fatalf("%s: no rules", name)
	}
	for _, r := range refs {
		for _, v := range append(append([]TraefikRefVerdict{}, r.Services...), r.Middlewares...) {
			if v.Missing != nil {
				t.Errorf("%s: %s %s/%s = %v, want can't tell", name, v.Kind, v.Namespace, v.Name, *v.Missing)
			}
		}
	}
}

func TestTraefikRuleRefsCantTell(t *testing.T) {
	t.Run("no running Traefik", func(t *testing.T) {
		defer ResetTestState()
		defer ResetTestDynamicState()
		initTraefikFixture(t, nil)
		allCantTell(t, "missing-svc", traefikAssessor(t).RuleRefs(ingressRoute("shop", "missing-svc", nil, svcRef("gone"), nil)))
	})
	t.Run("namespace outside the provider", func(t *testing.T) {
		defer ResetTestState()
		defer ResetTestDynamicState()
		initTraefikFixture(t, traefikPod("traefik", true, traefikArgs...))
		allCantTell(t, "other-namespace", traefikAssessor(t).RuleRefs(ingressRoute("elsewhere", "other-namespace", nil, svcRef("gone"), nil)))
	})
	t.Run("cross-namespace without allowCrossNamespace", func(t *testing.T) {
		defer ResetTestState()
		defer ResetTestDynamicState()
		initTraefikFixture(t, traefikPod("traefik", true, traefikArgs[:len(traefikArgs)-1]...))
		allCantTell(t, "cross-namespace", traefikAssessor(t).RuleRefs(ingressRoute("edge", "cross-namespace", nil, []any{map[string]any{"name": "gone", "namespace": "shop", "port": int64(80)}}, nil)))
	})
	t.Run("provider-qualified name and legacy group", func(t *testing.T) {
		defer ResetTestState()
		defer ResetTestDynamicState()
		initTraefikFixture(t, traefikPod("traefik", true, traefikArgs...))
		a := traefikAssessor(t)
		refs := a.RuleRefs(ingressRoute("shop", "file-provider-mw", nil, svcRef("web"), mwRef("auth@file")))
		if len(refs) != 1 || refs[0].Middlewares[0].Missing != nil {
			t.Errorf("auth@file must be can't tell: %+v", refs)
		}
		legacy := ingressRoute("shop", "missing-svc", nil, svcRef("gone"), nil)
		legacy.SetAPIVersion("traefik.containo.us/v1alpha1")
		allCantTell(t, "legacy", a.RuleRefs(legacy))
	})
}

// A present chain answers for its members too, so its reads include theirs.
func TestTraefikRuleRefsPresentChainCarriesMemberReads(t *testing.T) {
	defer ResetTestState()
	defer ResetTestDynamicState()
	initTraefikFixture(t, traefikPod("traefik", true, traefikArgs...),
		traefikObject("Middleware", "edge", "edge-chain", map[string]any{"chain": map[string]any{"middlewares": []any{map[string]any{"name": "strip", "namespace": "shop"}}}}))
	refs := traefikAssessor(t).RuleRefs(ingressRoute("edge", "uses-chain", nil, svcRef("web"), mwRef("edge-chain")))
	v := refs[0].Middlewares[0]
	if v.Missing == nil || *v.Missing {
		t.Fatalf("edge-chain should be present: %+v", v)
	}
	if !hasRead(v.Reads, issuesapi.EvidenceRead{Group: "traefik.io", Resource: "middlewares", Namespace: "shop", Verb: "list"}) {
		t.Errorf("reads must cover the member in shop: %+v", v.Reads)
	}
}

func TestParseTraefikRuleRefs(t *testing.T) {
	route := ingressRoute("shop", "p", nil, []any{map[string]any{"name": "web", "port": int64(8080)}, map[string]any{"name": "w", "kind": "TraefikService", "namespace": "edge"}}, mwRef("strip"))
	refs := ParseTraefikRuleRefs(route)
	if len(refs) != 1 || len(refs[0].Services) != 2 || len(refs[0].Middlewares) != 1 {
		t.Fatalf("refs = %+v", refs)
	}
	if s := refs[0].Services[0]; s.Kind != "Service" || s.Namespace != "shop" || s.Port != "8080" || s.Missing != nil {
		t.Errorf("service = %+v", s)
	}
	if s := refs[0].Services[1]; s.Kind != "TraefikService" || s.Namespace != "edge" {
		t.Errorf("traefik service = %+v", s)
	}
	if m := refs[0].Middlewares[0]; m.Namespace != "shop" || m.Name != "strip" {
		t.Errorf("middleware = %+v", m)
	}
}

func TestTraefikRuleRefsRefusedCrossNamespace(t *testing.T) {
	defer ResetTestState()
	defer ResetTestDynamicState()
	initTraefikFixture(t, traefikPod("traefik", true, traefikArgs[:len(traefikArgs)-1]...))
	refs := traefikAssessor(t).RuleRefs(ingressRoute("edge", "cross", nil, []any{map[string]any{"name": "web", "namespace": "shop", "port": int64(80)}}, nil))
	if !refs[0].Services[0].Refused {
		t.Errorf("a cross-namespace ref Traefik refuses must say so: %+v", refs[0].Services[0])
	}
}

// Two chain members reaching the same present Middleware don't turn the
// chain into can't-tell.
func TestTraefikRuleRefsDiamondChainIsPresent(t *testing.T) {
	defer ResetTestState()
	defer ResetTestDynamicState()
	initTraefikFixture(t, traefikPod("traefik", true, traefikArgs...),
		traefikObject("Middleware", "shop", "left", map[string]any{"chain": map[string]any{"middlewares": mwRef("strip")}}),
		traefikObject("Middleware", "shop", "right", map[string]any{"chain": map[string]any{"middlewares": mwRef("strip")}}),
		traefikObject("Middleware", "shop", "diamond", map[string]any{"chain": map[string]any{"middlewares": mwRef("left", "right")}}))
	refs := traefikAssessor(t).RuleRefs(ingressRoute("shop", "uses-diamond", nil, svcRef("web"), mwRef("diamond")))
	if v := refs[0].Middlewares[0]; v.Missing == nil || *v.Missing {
		t.Errorf("diamond chain should be present: %+v", v)
	}
}

// A real cycle stays can't-tell.
func TestTraefikRuleRefsChainCycleCantTell(t *testing.T) {
	defer ResetTestState()
	defer ResetTestDynamicState()
	initTraefikFixture(t, traefikPod("traefik", true, traefikArgs...),
		traefikObject("Middleware", "shop", "loop-a", map[string]any{"chain": map[string]any{"middlewares": mwRef("loop-b")}}),
		traefikObject("Middleware", "shop", "loop-b", map[string]any{"chain": map[string]any{"middlewares": mwRef("loop-a")}}))
	refs := traefikAssessor(t).RuleRefs(ingressRoute("shop", "uses-loop", nil, svcRef("web"), mwRef("loop-a")))
	if v := refs[0].Middlewares[0]; v.Missing != nil {
		t.Errorf("a cycle can't be decided: %+v", v)
	}
}
