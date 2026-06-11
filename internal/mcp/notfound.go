package mcp

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/k8s"
)

// Not-found errors are prompts: a wrong kind or namespace guess should cost
// the agent one corrected call, not a search expedition. On not-found we do a
// cheap cached-lister sweep for the same name under a different kind (same
// namespace) and the same kind in another namespace, and say exactly how to
// retry. Cross-namespace suggestions are RBAC-gated — we never reveal a
// resource in a namespace the caller can't access. Secrets are excluded from
// suggestions entirely.
const maxNotFoundSuggestions = 3

// notFoundError wraps a not-found error with retry suggestions when the
// cache knows a likely match. Falls back to the bare error otherwise.
func notFoundError(ctx context.Context, baseErr error, kind, namespace, name string) error {
	if s := notFoundSuggestion(ctx, kind, namespace, name); s != "" {
		return fmt.Errorf("resource not found: %w — %s", baseErr, s)
	}
	return fmt.Errorf("resource not found: %w", baseErr)
}

type notFoundProbe struct {
	kind       string
	existsIn   func(namespace, name string) bool
	namespaces func(name string) []string
}

func notFoundProbes(cache *k8s.ResourceCache) []notFoundProbe {
	var probes []notFoundProbe
	everything := labels.Everything()
	if l := cache.Deployments(); l != nil {
		probes = append(probes, notFoundProbe{
			kind: "Deployment",
			existsIn: func(ns, name string) bool {
				_, err := l.Deployments(ns).Get(name)
				return err == nil
			},
			namespaces: func(name string) []string {
				items, _ := l.List(everything)
				var out []string
				for _, item := range items {
					if item.Name == name {
						out = append(out, item.Namespace)
					}
				}
				return out
			},
		})
	}
	if l := cache.StatefulSets(); l != nil {
		probes = append(probes, notFoundProbe{
			kind: "StatefulSet",
			existsIn: func(ns, name string) bool {
				_, err := l.StatefulSets(ns).Get(name)
				return err == nil
			},
			namespaces: func(name string) []string {
				items, _ := l.List(everything)
				var out []string
				for _, item := range items {
					if item.Name == name {
						out = append(out, item.Namespace)
					}
				}
				return out
			},
		})
	}
	if l := cache.DaemonSets(); l != nil {
		probes = append(probes, notFoundProbe{
			kind: "DaemonSet",
			existsIn: func(ns, name string) bool {
				_, err := l.DaemonSets(ns).Get(name)
				return err == nil
			},
			namespaces: func(name string) []string {
				items, _ := l.List(everything)
				var out []string
				for _, item := range items {
					if item.Name == name {
						out = append(out, item.Namespace)
					}
				}
				return out
			},
		})
	}
	if l := cache.Services(); l != nil {
		probes = append(probes, notFoundProbe{
			kind: "Service",
			existsIn: func(ns, name string) bool {
				_, err := l.Services(ns).Get(name)
				return err == nil
			},
			namespaces: func(name string) []string {
				items, _ := l.List(everything)
				var out []string
				for _, item := range items {
					if item.Name == name {
						out = append(out, item.Namespace)
					}
				}
				return out
			},
		})
	}
	if l := cache.ConfigMaps(); l != nil {
		probes = append(probes, notFoundProbe{
			kind: "ConfigMap",
			existsIn: func(ns, name string) bool {
				_, err := l.ConfigMaps(ns).Get(name)
				return err == nil
			},
			namespaces: func(name string) []string {
				items, _ := l.List(everything)
				var out []string
				for _, item := range items {
					if item.Name == name {
						out = append(out, item.Namespace)
					}
				}
				return out
			},
		})
	}
	if l := cache.CronJobs(); l != nil {
		probes = append(probes, notFoundProbe{
			kind: "CronJob",
			existsIn: func(ns, name string) bool {
				_, err := l.CronJobs(ns).Get(name)
				return err == nil
			},
			namespaces: func(name string) []string {
				items, _ := l.List(everything)
				var out []string
				for _, item := range items {
					if item.Name == name {
						out = append(out, item.Namespace)
					}
				}
				return out
			},
		})
	}
	return probes
}

func kindMatchesProbe(requested, probeKind string) bool {
	r := strings.ToLower(strings.TrimSpace(requested))
	p := strings.ToLower(probeKind)
	return r == p || r == p+"s" || strings.TrimSuffix(r, "es") == p
}

func notFoundSuggestion(ctx context.Context, kind, namespace, name string) string {
	cache := k8s.GetResourceCache()
	if cache == nil || name == "" {
		return ""
	}
	probes := notFoundProbes(cache)
	var parts []string

	// Same name under a different kind in the same namespace — the classic
	// "diagnose StatefulSet postgresql" when it's a Deployment.
	if namespace != "" {
		for _, p := range probes {
			if kindMatchesProbe(kind, p.kind) {
				continue
			}
			if p.existsIn(namespace, name) {
				parts = append(parts, fmt.Sprintf("found %s %s/%s — retry with kind=%s", p.kind, namespace, name, strings.ToLower(p.kind)))
				if len(parts) >= maxNotFoundSuggestions {
					break
				}
			}
		}
	}

	// Same kind in another namespace the caller can access.
	if len(parts) < maxNotFoundSuggestions {
		for _, p := range probes {
			if !kindMatchesProbe(kind, p.kind) {
				continue
			}
			for _, ns := range p.namespaces(name) {
				if ns == namespace || !checkNamespaceAccess(ctx, ns) {
					continue
				}
				parts = append(parts, fmt.Sprintf("found %s %s/%s — retry with namespace=%s", p.kind, ns, name, ns))
				if len(parts) >= maxNotFoundSuggestions {
					break
				}
			}
			break
		}
	}

	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "; ") + "; or use search"
}
