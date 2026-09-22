package resourcecontext

import (
	"context"
	"strings"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation"
)

const reflectorPrefix = "reflector.v1.k8s.emberstack.com/"
const maxReflectionMirrors = 20

// ReflectionLookup resolves cross-namespace declarations independently of the
// namespace-scoped resource-context graph. Build authorizes its observations.
type ReflectionLookup interface {
	For(kind, namespace, name string) (*ReflectionFacts, error)
}

type ReflectionFacts struct {
	Source                *ContextRef
	SourceResourceVersion string
	Mirrors               []ContextRef
}

func buildReflection(ctx context.Context, obj runtime.Object, lookup ReflectionLookup, ac RefAccessChecker, omitted *omittedTracker) *ReflectionContext {
	ident, ok := identityOf(obj)
	if !ok || ident.Group != "" || (ident.Kind != "Secret" && ident.Kind != "ConfigMap") {
		return nil
	}
	metadata, err := meta.Accessor(obj)
	if err != nil {
		return nil
	}
	anns := metadata.GetAnnotations()
	recognized := false
	for _, name := range []string{"reflects", "auto-reflects", "reflected-version", "reflected-at", "reflection-allowed", "reflection-auto-enabled", "reflection-allowed-namespaces", "reflection-allowed-namespaces-selector", "reflection-auto-namespaces", "reflection-auto-namespaces-selector"} {
		if _, exists := anns[reflectorPrefix+name]; exists {
			recognized = true
			break
		}
	}
	out := &ReflectionContext{DeclaredSource: anns[reflectorPrefix+"reflects"], RecordedSourceVersion: anns[reflectorPrefix+"reflected-version"], RecordedAt: anns[reflectorPrefix+"reflected-at"]}
	if raw, exists := anns[reflectorPrefix+"auto-reflects"]; exists {
		raw = strings.TrimSpace(raw)
		if strings.EqualFold(raw, "true") || strings.EqualFold(raw, "false") {
			automatic := strings.EqualFold(raw, "true")
			out.Automatic = &automatic
		}
	}
	if lookup == nil {
		if !recognized {
			return nil
		}
		omitted.add("reflection", OmittedCacheCold)
		return out
	}
	facts, err := lookup.For(ident.Kind, ident.Namespace, ident.Name)
	if err != nil {
		if !recognized {
			return nil
		}
		omitted.add("reflection", OmittedCacheCold)
		return out
	}
	pending := newOmittedTracker()
	sourceReadable := false
	if ns, name, valid := strings.Cut(out.DeclaredSource, "/"); valid && len(validation.IsDNS1123Label(ns)) == 0 && len(validation.IsDNS1123Subdomain(name)) == 0 {
		sourceReadable = checkRef(ctx, ac, &ContextRef{Kind: ident.Kind, Namespace: ns, Name: name})
		if !sourceReadable {
			pending.add("reflection.source", OmittedRBACDenied)
		}
	}
	if facts != nil {
		if facts.Source != nil && facts.Source.Namespace+"/"+facts.Source.Name == out.DeclaredSource {
			if sourceReadable {
				source := *facts.Source
				out.Source = &source
				out.SourceResourceVersion = facts.SourceResourceVersion
			}
		}
		mirrors := filterRefs(ctx, ac, facts.Mirrors, "reflection.mirrors", pending)
		if len(mirrors) > maxReflectionMirrors {
			mirrors = mirrors[:maxReflectionMirrors]
			out.Truncated = true
			pending.add("reflection.mirrors", OmittedBudgetExceeded)
		}
		out.VisibleMirrors = mirrors
	}
	if !recognized && out.Source == nil && len(out.VisibleMirrors) == 0 {
		return nil
	}
	for _, item := range pending.collect() {
		omitted.add(item.Field, item.Reason)
	}
	return out
}
