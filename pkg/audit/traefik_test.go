package audit

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func traefikRoute(group, kind, ns, name string, svcRefs, mwRefs []map[string]any) *unstructured.Unstructured {
	route := map[string]any{}
	if svcRefs != nil {
		route["services"] = toIfaceSlice(svcRefs)
	}
	if mwRefs != nil {
		route["middlewares"] = toIfaceSlice(mwRefs)
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": group + "/v1alpha1",
		"kind":       kind,
		"metadata":   map[string]any{"name": name, "namespace": ns, "uid": group + "/" + ns + "/" + name},
		"spec":       map[string]any{"routes": []any{route}},
	}}
}

func traefikObj(group, kind, ns, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": group + "/v1alpha1",
		"kind":       kind,
		"metadata":   map[string]any{"name": name, "namespace": ns},
	}}
}

func toIfaceSlice(maps []map[string]any) []any {
	out := make([]any, len(maps))
	for i, m := range maps {
		out[i] = m
	}
	return out
}

func svc(ns, name string) *corev1.Service {
	return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
}

// authoritative builds the group\x00Kind set the runner would produce when those
// target kinds are served by a synced cluster-wide informer.
func authoritative(keys ...string) map[string]bool {
	m := map[string]bool{}
	for _, k := range keys {
		m[k] = true
	}
	return m
}

func findingIDs(findings []Finding) map[string]int {
	m := map[string]int{}
	for _, f := range findings {
		m[f.CheckID]++
	}
	return m
}

func TestCheckTraefikDanglingRefs(t *testing.T) {
	const g = "traefik.io"

	t.Run("flags missing service and middleware, accepts present ones", func(t *testing.T) {
		input := &CheckInput{
			AllServices:               []*corev1.Service{svc("app", "present-svc")},
			Middlewares:               []*unstructured.Unstructured{traefikObj(g, "Middleware", "app", "present-mw")},
			TraefikAuthoritativeKinds: authoritative(g + "\x00Middleware"),
			IngressRoutes: []*unstructured.Unstructured{
				traefikRoute(g, "IngressRoute", "app", "r1",
					[]map[string]any{{"name": "present-svc"}, {"name": "missing-svc"}},
					[]map[string]any{{"name": "present-mw"}, {"name": "missing-mw"}},
				),
			},
		}
		ids := findingIDs(checkTraefikDanglingRefs(newEvalTracker(), input))
		if ids["traefikRouteMissingService"] != 1 {
			t.Errorf("want 1 missing-service, got %d", ids["traefikRouteMissingService"])
		}
		if ids["traefikRouteMissingMiddleware"] != 1 {
			t.Errorf("want 1 missing-middleware, got %d", ids["traefikRouteMissingMiddleware"])
		}
	})

	t.Run("all refs present → no findings", func(t *testing.T) {
		input := &CheckInput{
			AllServices:               []*corev1.Service{svc("app", "s")},
			Middlewares:               []*unstructured.Unstructured{traefikObj(g, "Middleware", "app", "mw")},
			TraefikAuthoritativeKinds: authoritative(g + "\x00Middleware"),
			IngressRoutes: []*unstructured.Unstructured{
				traefikRoute(g, "IngressRoute", "app", "r",
					[]map[string]any{{"name": "s"}}, []map[string]any{{"name": "mw"}}),
			},
		}
		if got := checkTraefikDanglingRefs(newEvalTracker(), input); len(got) != 0 {
			t.Errorf("want no findings, got %v", got)
		}
	})

	t.Run("cross-namespace middleware ref RESOLVES against cluster-wide inventory", func(t *testing.T) {
		// Route in ns "app" references a Middleware explicitly in ns "shared".
		// The middleware exists there; since the inventory is cluster-wide and
		// authoritative, this must NOT be flagged.
		input := &CheckInput{
			AllServices:               []*corev1.Service{},
			Middlewares:               []*unstructured.Unstructured{traefikObj(g, "Middleware", "shared", "auth")},
			TraefikAuthoritativeKinds: authoritative(g + "\x00Middleware"),
			IngressRoutes: []*unstructured.Unstructured{
				traefikRoute(g, "IngressRoute", "app", "r", nil,
					[]map[string]any{{"name": "auth", "namespace": "shared"}}),
			},
		}
		if n := findingIDs(checkTraefikDanglingRefs(newEvalTracker(), input))["traefikRouteMissingMiddleware"]; n != 0 {
			t.Errorf("cross-namespace ref to an existing middleware must not be flagged, got %d", n)
		}
	})

	t.Run("not authoritative → skip (no false positive)", func(t *testing.T) {
		// Middleware kind NOT in the authoritative set (e.g. namespace-scoped
		// fallback). Even though the referenced mw is absent, do not assert.
		input := &CheckInput{
			AllServices:               []*corev1.Service{},
			Middlewares:               []*unstructured.Unstructured{},
			TraefikAuthoritativeKinds: authoritative(), // empty → nothing authoritative
			IngressRoutes: []*unstructured.Unstructured{
				traefikRoute(g, "IngressRoute", "app", "r", nil, []map[string]any{{"name": "ghost"}}),
			},
		}
		if n := findingIDs(checkTraefikDanglingRefs(newEvalTracker(), input))["traefikRouteMissingMiddleware"]; n != 0 {
			t.Errorf("non-authoritative middleware kind must not be asserted, got %d", n)
		}
	})

	t.Run("cross-group middleware does not satisfy the reference", func(t *testing.T) {
		input := &CheckInput{
			AllServices:               []*corev1.Service{},
			Middlewares:               []*unstructured.Unstructured{traefikObj("traefik.containo.us", "Middleware", "app", "mw")},
			TraefikAuthoritativeKinds: authoritative(g + "\x00Middleware"),
			IngressRoutes: []*unstructured.Unstructured{
				traefikRoute(g, "IngressRoute", "app", "r", nil, []map[string]any{{"name": "mw"}}),
			},
		}
		if n := findingIDs(checkTraefikDanglingRefs(newEvalTracker(), input))["traefikRouteMissingMiddleware"]; n != 1 {
			t.Errorf("traefik.io router must not be satisfied by a traefik.containo.us Middleware, got %d", n)
		}
	})

	t.Run("IngressRouteTCP resolves against MiddlewareTCP, per-kind independent", func(t *testing.T) {
		// Only Middleware (not MiddlewareTCP) is authoritative + present. A TCP
		// router's MiddlewareTCP ref must be skipped (its kind isn't authoritative),
		// NOT fabricated as missing.
		input := &CheckInput{
			AllServices:               []*corev1.Service{},
			Middlewares:               []*unstructured.Unstructured{traefikObj(g, "Middleware", "app", "mw")},
			TraefikAuthoritativeKinds: authoritative(g + "\x00Middleware"), // NOT MiddlewareTCP
			IngressRoutes: []*unstructured.Unstructured{
				traefikRoute(g, "IngressRouteTCP", "app", "r", nil, []map[string]any{{"name": "mw"}}),
			},
		}
		if n := findingIDs(checkTraefikDanglingRefs(newEvalTracker(), input))["traefikRouteMissingMiddleware"]; n != 0 {
			t.Errorf("MiddlewareTCP kind not authoritative → must skip, got %d (regression: CR-3)", n)
		}

		// Now MiddlewareTCP IS authoritative and the ref is absent → flag.
		input.TraefikAuthoritativeKinds = authoritative(g + "\x00MiddlewareTCP")
		if n := findingIDs(checkTraefikDanglingRefs(newEvalTracker(), input))["traefikRouteMissingMiddleware"]; n != 1 {
			t.Errorf("authoritative MiddlewareTCP with absent ref → want 1, got %d", n)
		}
	})

	t.Run("service check independent of middleware authority", func(t *testing.T) {
		// Middlewares not authoritative, but services are listed → service ref
		// still checked (per-kind independence; CR-5).
		input := &CheckInput{
			AllServices:               []*corev1.Service{}, // non-nil, empty → listed
			TraefikAuthoritativeKinds: authoritative(),
			IngressRoutes: []*unstructured.Unstructured{
				traefikRoute(g, "IngressRoute", "app", "r",
					[]map[string]any{{"name": "missing"}}, []map[string]any{{"name": "ghost"}}),
			},
		}
		ids := findingIDs(checkTraefikDanglingRefs(newEvalTracker(), input))
		if ids["traefikRouteMissingService"] != 1 {
			t.Errorf("service ref should be checked independently, got %d", ids["traefikRouteMissingService"])
		}
		if ids["traefikRouteMissingMiddleware"] != 0 {
			t.Errorf("middleware not authoritative → skip, got %d", ids["traefikRouteMissingMiddleware"])
		}
	})

	t.Run("findings carry empty Group (visible in per-resource drill-down)", func(t *testing.T) {
		input := &CheckInput{
			AllServices:               []*corev1.Service{},
			TraefikAuthoritativeKinds: authoritative(),
			IngressRoutes: []*unstructured.Unstructured{
				traefikRoute(g, "IngressRoute", "app", "r", []map[string]any{{"name": "missing"}}, nil),
			},
		}
		got := checkTraefikDanglingRefs(newEvalTracker(), input)
		if len(got) != 1 {
			t.Fatalf("want 1 finding, got %d", len(got))
		}
		if got[0].Group != "" {
			t.Errorf("Traefik findings must leave Group empty for drill-down lookup, got %q", got[0].Group)
		}
	})

	t.Run("no Traefik installed → no-op", func(t *testing.T) {
		if got := checkTraefikDanglingRefs(newEvalTracker(), &CheckInput{}); got != nil {
			t.Errorf("want nil, got %v", got)
		}
	})
}

func traefikChainMw(group, ns, name string, chainRefs []map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": group + "/v1alpha1",
		"kind":       "Middleware",
		"metadata":   map[string]any{"name": name, "namespace": ns, "uid": group + "/chain/" + ns + "/" + name},
		"spec":       map[string]any{"chain": map[string]any{"middlewares": toIfaceSlice(chainRefs)}},
	}}
}

func traefikErrorsMw(group, ns, name string, svcRef map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": group + "/v1alpha1",
		"kind":       "Middleware",
		"metadata":   map[string]any{"name": name, "namespace": ns, "uid": group + "/errors/" + ns + "/" + name},
		"spec":       map[string]any{"errors": map[string]any{"service": svcRef}},
	}}
}

func TestCheckTraefikMiddlewareSubjectRefs(t *testing.T) {
	const g = "traefik.io"

	t.Run("chain flags missing middleware, accepts present", func(t *testing.T) {
		input := &CheckInput{
			Middlewares:               []*unstructured.Unstructured{traefikObj(g, "Middleware", "app", "present")},
			TraefikAuthoritativeKinds: authoritative(g + "\x00Middleware"),
			MiddlewareSubjects: []*unstructured.Unstructured{
				traefikChainMw(g, "app", "c", []map[string]any{{"name": "present"}, {"name": "missing"}}),
			},
		}
		if n := findingIDs(checkTraefikDanglingRefs(newEvalTracker(), input))["traefikChainMissingMiddleware"]; n != 1 {
			t.Errorf("want 1 missing chain middleware, got %d", n)
		}
	})

	t.Run("chain cross-namespace ref resolves against cluster-wide inventory", func(t *testing.T) {
		input := &CheckInput{
			Middlewares:               []*unstructured.Unstructured{traefikObj(g, "Middleware", "shared", "auth")},
			TraefikAuthoritativeKinds: authoritative(g + "\x00Middleware"),
			MiddlewareSubjects: []*unstructured.Unstructured{
				traefikChainMw(g, "app", "c", []map[string]any{{"name": "auth", "namespace": "shared"}}),
			},
		}
		if n := findingIDs(checkTraefikDanglingRefs(newEvalTracker(), input))["traefikChainMissingMiddleware"]; n != 0 {
			t.Errorf("cross-namespace ref to an existing middleware must not be flagged, got %d", n)
		}
	})

	t.Run("chain not authoritative → skip (no false positive)", func(t *testing.T) {
		input := &CheckInput{
			TraefikAuthoritativeKinds: authoritative(), // nothing authoritative
			MiddlewareSubjects: []*unstructured.Unstructured{
				traefikChainMw(g, "app", "c", []map[string]any{{"name": "ghost"}}),
			},
		}
		if n := findingIDs(checkTraefikDanglingRefs(newEvalTracker(), input))["traefikChainMissingMiddleware"]; n != 0 {
			t.Errorf("non-authoritative middleware kind must not be asserted, got %d", n)
		}
	})

	t.Run("errors flags missing service, accepts present", func(t *testing.T) {
		input := &CheckInput{
			AllServices: []*corev1.Service{svc("app", "present")},
			MiddlewareSubjects: []*unstructured.Unstructured{
				traefikErrorsMw(g, "app", "e-missing", map[string]any{"name": "missing"}),
				traefikErrorsMw(g, "app", "e-present", map[string]any{"name": "present"}),
			},
		}
		if n := findingIDs(checkTraefikDanglingRefs(newEvalTracker(), input))["traefikErrorsMissingService"]; n != 1 {
			t.Errorf("want 1 missing errors service, got %d", n)
		}
	})

	t.Run("errors.service kind=TraefikService resolves against TraefikService inventory", func(t *testing.T) {
		input := &CheckInput{
			AllServices:               []*corev1.Service{}, // listed, empty
			TraefikServices:           []*unstructured.Unstructured{traefikObj(g, "TraefikService", "app", "weighted")},
			TraefikAuthoritativeKinds: authoritative(g + "\x00TraefikService"),
			MiddlewareSubjects: []*unstructured.Unstructured{
				traefikErrorsMw(g, "app", "e", map[string]any{"name": "weighted", "kind": "TraefikService"}),
			},
		}
		if n := findingIDs(checkTraefikDanglingRefs(newEvalTracker(), input))["traefikErrorsMissingService"]; n != 0 {
			t.Errorf("present TraefikService ref must not be flagged, got %d", n)
		}
	})

	t.Run("middleware-only namespace (no routes) still runs the checks", func(t *testing.T) {
		// Regression guard: the early-return must not bail when there are only
		// middleware subjects and zero IngressRoutes.
		input := &CheckInput{
			AllServices: []*corev1.Service{},
			MiddlewareSubjects: []*unstructured.Unstructured{
				traefikErrorsMw(g, "app", "e", map[string]any{"name": "missing"}),
			},
		}
		got := checkTraefikDanglingRefs(newEvalTracker(), input)
		if len(got) != 1 {
			t.Fatalf("want 1 finding from a middleware-only namespace, got %d", len(got))
		}
		if got[0].Kind != "Middleware" || got[0].Group != "" {
			t.Errorf("finding must be on the Middleware with empty Group, got kind=%q group=%q", got[0].Kind, got[0].Group)
		}
	})
}
