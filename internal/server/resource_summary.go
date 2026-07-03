package server

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"strings"

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

var summaryKindAliases = func() map[string]string {
	aliases := map[string]string{}
	for key := range summaryStripProfiles {
		_, kind, ok := strings.Cut(key, "/")
		if !ok {
			continue
		}
		aliases[strings.ToLower(kind)] = kind
		aliases[strings.ToLower(kind)+"s"] = kind
	}
	return aliases
}()

// summaryFallbackKey builds the profile key from request params when list
// items carry no TypeMeta.
func summaryFallbackKey(group, kind string) string {
	if k, ok := summaryKindAliases[strings.ToLower(kind)]; ok {
		return group + "/" + k
	}
	return group + "/" + kind
}

func applySummaryStrip(result any, fallbackKey string) any {
	switch items := result.(type) {
	case []*unstructured.Unstructured:
		out := make([]*unstructured.Unstructured, len(items))
		for i, item := range items {
			out[i] = summarizeUnstructured(item, fallbackKey)
		}
		return out
	case []any:
		out := make([]any, len(items))
		for i, item := range items {
			if u, ok := item.(*unstructured.Unstructured); ok {
				out[i] = summarizeUnstructured(u, fallbackKey)
			} else {
				out[i] = item
			}
		}
		return out
	default:
		return result
	}
}

func summarizeUnstructured(obj *unstructured.Unstructured, fallbackKey string) *unstructured.Unstructured {
	if obj == nil {
		return nil
	}
	gvk := obj.GroupVersionKind()
	key := gvk.Group + "/" + gvk.Kind
	if gvk.Kind == "" {
		key = fallbackKey
	}
	profile, ok := summaryStripProfiles[key]
	if !ok {
		return obj
	}
	return prune.Apply(obj, profile)
}
