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
		"metadata":   map[string]any{"name": name, "namespace": ns, "uid": name},
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
			Services: []*corev1.Service{svc("app", "present-svc")},
			Middlewares: []*unstructured.Unstructured{
				traefikObj(g, "Middleware", "app", "present-mw"),
			},
			IngressRoutes: []*unstructured.Unstructured{
				traefikRoute(g, "IngressRoute", "app", "r1",
					[]map[string]any{{"name": "present-svc"}, {"name": "missing-svc"}},
					[]map[string]any{{"name": "present-mw"}, {"name": "missing-mw"}},
				),
			},
		}
		ids := findingIDs(checkTraefikDanglingRefs(input))
		if ids["traefikRouteMissingService"] != 1 {
			t.Errorf("want 1 missing-service finding, got %d", ids["traefikRouteMissingService"])
		}
		if ids["traefikRouteMissingMiddleware"] != 1 {
			t.Errorf("want 1 missing-middleware finding, got %d", ids["traefikRouteMissingMiddleware"])
		}
	})

	t.Run("all refs present → no findings", func(t *testing.T) {
		input := &CheckInput{
			Services:    []*corev1.Service{svc("app", "svc")},
			Middlewares: []*unstructured.Unstructured{traefikObj(g, "Middleware", "app", "mw")},
			IngressRoutes: []*unstructured.Unstructured{
				traefikRoute(g, "IngressRoute", "app", "r",
					[]map[string]any{{"name": "svc"}}, []map[string]any{{"name": "mw"}}),
			},
		}
		if got := checkTraefikDanglingRefs(input); len(got) != 0 {
			t.Errorf("want no findings, got %v", got)
		}
	})

	t.Run("cross-group middleware does not satisfy the reference", func(t *testing.T) {
		input := &CheckInput{
			Services:    []*corev1.Service{},
			Middlewares: []*unstructured.Unstructured{traefikObj("traefik.containo.us", "Middleware", "app", "mw")},
			IngressRoutes: []*unstructured.Unstructured{
				traefikRoute(g, "IngressRoute", "app", "r", nil, []map[string]any{{"name": "mw"}}),
			},
		}
		if findingIDs(checkTraefikDanglingRefs(input))["traefikRouteMissingMiddleware"] != 1 {
			t.Error("a traefik.io router should not be satisfied by a traefik.containo.us Middleware")
		}
	})

	t.Run("IngressRouteTCP resolves against MiddlewareTCP, not Middleware", func(t *testing.T) {
		input := &CheckInput{
			Services:    []*corev1.Service{},
			Middlewares: []*unstructured.Unstructured{traefikObj(g, "Middleware", "app", "mw")}, // wrong kind
			IngressRoutes: []*unstructured.Unstructured{
				traefikRoute(g, "IngressRouteTCP", "app", "r", nil, []map[string]any{{"name": "mw"}}),
			},
		}
		if findingIDs(checkTraefikDanglingRefs(input))["traefikRouteMissingMiddleware"] != 1 {
			t.Error("a TCP router references MiddlewareTCP; a same-name Middleware should not satisfy it")
		}
	})

	t.Run("nil middleware inventory is treated as 'cannot verify', not 'missing'", func(t *testing.T) {
		input := &CheckInput{
			Services:    []*corev1.Service{svc("app", "svc")},
			Middlewares: nil, // RBAC denied / not listed
			IngressRoutes: []*unstructured.Unstructured{
				traefikRoute(g, "IngressRoute", "app", "r",
					[]map[string]any{{"name": "svc"}}, []map[string]any{{"name": "anything"}}),
			},
		}
		if got := findingIDs(checkTraefikDanglingRefs(input))["traefikRouteMissingMiddleware"]; got != 0 {
			t.Errorf("nil middleware set must not produce false positives, got %d", got)
		}
	})

	t.Run("no Traefik installed → no-op", func(t *testing.T) {
		if got := checkTraefikDanglingRefs(&CheckInput{}); got != nil {
			t.Errorf("want nil, got %v", got)
		}
	})
}
