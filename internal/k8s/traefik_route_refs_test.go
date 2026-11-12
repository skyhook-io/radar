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
