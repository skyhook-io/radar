package server

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/prune"
)

// Summary strip profiles for ?include=summary on the resource list endpoint.
// GitOps CRs carry heavy subtrees (Argo status.resources / history /
// operationState.syncResult, Flux status.inventory) that dominate wire size on
// large fleets but that list consumers (the fleet GitOps board) never read.
// Each profile deletes only those subtrees; every field the board normalizers
// read must survive intact — resource_summary_test.go pins that contract.
// Kinds without a profile pass through unchanged, so summary is best-effort
// and the response stays a bare array of full-shaped objects.
// Profiles are DATA over pkg/prune's shared mechanism — the keep-list
// policy lives here (validated by the contract tests below/in *_test.go);
// the tree surgery, deep-copy discipline, and tail-trim semantics live in
// pkg/prune. pkg/ai/context prunes the same way for a different policy
// (token budget); see that package before inventing a third mechanism.
var summaryStripProfiles = map[string]prune.Profile{
	"argoproj.io/Application": {
		Drop: [][]string{
			{"metadata", "managedFields"},
			{"status", "resources"},
			{"status", "operationState", "syncResult"},
		},
		TailTrims: []prune.TailTrim{{Path: []string{"status", "history"}, KeepField: "deployedAt"}},
	},
	"kustomize.toolkit.fluxcd.io/Kustomization": {
		Drop: [][]string{{"metadata", "managedFields"}, {"status", "inventory"}},
	},
	"helm.toolkit.fluxcd.io/HelmRelease": {
		Drop: [][]string{{"metadata", "managedFields"}, {"status", "inventory"}},
	},
}

// Profiles must target CRD kinds (group contains a dot): the summary strip
// only runs on the dynamic (unstructured) list path — a typed-kind profile
// would be accepted and silently never apply. Fail loudly at init instead.
func init() {
	for key := range summaryStripProfiles {
		if !strings.Contains(key, ".") {
			panic("resource summary profile for a non-CRD kind will silently never apply: " + key)
		}
	}
}

// parseResourcesInclude maps the resource list endpoint's include values.
// Default (absent) is raw; unknown values are a caller bug and 400, matching
// /api/search's validation posture — but NOT its semantics: here summary is
// a same-schema strip (heavy subtrees removed, object shape intact), whereas
// search's summary/raw are transformed ai/context representations. Same
// word, different shape contract; don't assume one from the other.
func parseResourcesInclude(v string) (summary bool, err error) {
	switch v {
	case "", "raw":
		return false, nil
	case "summary":
		return true, nil
	default:
		return false, fmt.Errorf("unknown include=%q (want: summary, raw)", v)
	}
}

// applySummaryStrip summarizes every unstructured item in a dynamic list
// IN PLACE. Its only callers are the two handleListResources dynamic-list
// exits, and every item there is already an owned deep copy — the dynamic
// cache returns StripUnstructuredFields(u) results (List + ListDirect both
// DeepCopy). Mutating in place avoids a redundant second copy of objects
// we're about to shrink, on the heaviest payload path. The informer cache
// is never touched (proven by the handler e2e test); do NOT call this with
// objects you don't own.
//
// Typed-cache lists (the default: arm) bypass summary by construction —
// profiled kinds are all CRDs, guaranteed by the init check above. Dynamic
// informers preserve apiVersion/kind, so each item keys on its own GVK; an
// item that lacks a profile is left untouched (fail open).
func applySummaryStrip(result any) any {
	switch items := result.(type) {
	case []*unstructured.Unstructured:
		for _, item := range items {
			summarizeUnstructuredInPlace(item)
		}
	case []any:
		for _, item := range items {
			if u, ok := item.(*unstructured.Unstructured); ok {
				summarizeUnstructuredInPlace(u)
			}
		}
	}
	return result
}

func summarizeUnstructuredInPlace(obj *unstructured.Unstructured) {
	if obj == nil {
		return
	}
	gvk := obj.GroupVersionKind()
	if profile, ok := summaryStripProfiles[gvk.Group+"/"+gvk.Kind]; ok {
		prune.ApplyInPlace(obj.Object, profile)
	}
}
