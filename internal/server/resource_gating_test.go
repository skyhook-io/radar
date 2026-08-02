package server

import "testing"

func TestInformerKeyForKind_AliasesResolveToInformerKeys(t *testing.T) {
	cases := map[string]string{
		"pods": "pods", "pod": "pods",
		"hpa": "horizontalpodautoscalers", "hpas": "horizontalpodautoscalers",
		"horizontalpodautoscaler": "horizontalpodautoscalers",
		"pvc": "persistentvolumeclaims", "pvcs": "persistentvolumeclaims",
		"pv": "persistentvolumes", "sc": "storageclasses",
		"pdb": "poddisruptionbudgets", "netpol": "networkpolicies",
		"deployment": "deployments", "namespaces": "namespaces",
		// Outside the typed set: CRDs (and EndpointSlice, which has no typed
		// informer) fall through to dynamic handling.
		"endpointslice": "", "endpointslices": "",
		"applications": "", "kustomizations": "", "no-such-kind": "",
	}
	for in, want := range cases {
		if got := informerKeyForKind(in); got != want {
			t.Errorf("informerKeyForKind(%q) = %q, want %q", in, got, want)
		}
	}
}
