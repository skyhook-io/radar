package audit

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// checkTraefikDanglingRefs flags Traefik routers that reference a Service,
// TraefikService, or Middleware that doesn't exist. Traefik ships no validation
// webhook or linter — a typo'd ref silently drops traffic until someone reads
// the controller logs — so this is genuinely additive (cf. Kubevious' built-in
// "missing Middleware reference" validator).
//
// Matching is conservative to avoid false positives:
//   - Only genuinely-absent refs are flagged; an explicit cross-namespace ref
//     that resolves is accepted even though Traefik may reject it without
//     allowCrossNamespace (we can't see the provider config, so we don't guess).
//   - Refs are matched within the SAME Traefik group family — a traefik.io
//     router is not considered satisfied by a traefik.containo.us Middleware.
//   - Service / Middleware presence is only asserted when we actually have the
//     corresponding inventory (nil set = "couldn't list", so skip — never flag).
//
// Scope is deliberately partial: route → Service and route → Middleware. Nested
// TraefikService services, middleware chains, errors.service, and TLS-secret
// refs can also dangle and are not yet covered.
func checkTraefikDanglingRefs(input *CheckInput) []Finding {
	if len(input.IngressRoutes) == 0 {
		return nil
	}

	// Core Services are group-agnostic (always core/v1). nil = RBAC denied → skip.
	var coreServices map[string]bool
	if input.Services != nil {
		coreServices = make(map[string]bool, len(input.Services))
		for _, svc := range input.Services {
			coreServices[svc.Namespace+"/"+svc.Name] = true
		}
	}

	// nil = couldn't list (denied/unsynced) → don't assert absence of TraefikServices.
	traefikServicesListed := input.TraefikServices != nil
	traefikServices := make(map[string]bool, len(input.TraefikServices)) // group\x00ns/name
	for _, ts := range input.TraefikServices {
		traefikServices[traefikGroupOf(ts)+"\x00"+ts.GetNamespace()+"/"+ts.GetName()] = true
	}

	middlewares := make(map[string]bool, len(input.Middlewares)) // group\x00kind\x00ns/name
	for _, mw := range input.Middlewares {
		middlewares[traefikGroupOf(mw)+"\x00"+mw.GetKind()+"\x00"+mw.GetNamespace()+"/"+mw.GetName()] = true
	}

	var findings []Finding
	seen := make(map[string]bool)
	add := func(route *unstructured.Unstructured, checkID, msg string) {
		key := string(route.GetUID()) + "\x00" + checkID + "\x00" + msg
		if seen[key] {
			return
		}
		seen[key] = true
		findings = append(findings, Finding{
			Kind: route.GetKind(), Group: traefikGroupOf(route),
			Namespace: route.GetNamespace(), Name: route.GetName(),
			CheckID: checkID, Category: CategoryReliability, Severity: SeverityWarning,
			Message: msg,
		})
	}

	for _, route := range input.IngressRoutes {
		group := traefikGroupOf(route)
		routeKind := route.GetKind()
		routeNs := route.GetNamespace()

		// IngressRouteTCP chains MiddlewareTCP; the others chain Middleware.
		mwKind := "Middleware"
		if routeKind == "IngressRouteTCP" {
			mwKind = "MiddlewareTCP"
		}

		routes, _, _ := unstructured.NestedSlice(route.Object, "spec", "routes")
		for _, r := range routes {
			rm, ok := r.(map[string]any)
			if !ok {
				continue
			}

			for _, s := range nestedMaps(rm, "services") {
				name, _ := s["name"].(string)
				if name == "" {
					continue
				}
				ns, _ := s["namespace"].(string)
				if ns == "" {
					ns = routeNs
				}
				if kind, _ := s["kind"].(string); kind == "TraefikService" {
					if traefikServicesListed && !traefikServices[group+"\x00"+ns+"/"+name] {
						add(route, "traefikRouteMissingService",
							fmt.Sprintf("%s references non-existent TraefikService %q", routeKind, traefikRefLabel(ns, name, routeNs)))
					}
				} else if coreServices != nil && !coreServices[ns+"/"+name] {
					add(route, "traefikRouteMissingService",
						fmt.Sprintf("%s references non-existent Service %q", routeKind, traefikRefLabel(ns, name, routeNs)))
				}
			}

			if input.Middlewares == nil {
				continue // couldn't list middlewares → don't assert absence
			}
			for _, m := range nestedMaps(rm, "middlewares") {
				name, _ := m["name"].(string)
				if name == "" {
					continue
				}
				ns, _ := m["namespace"].(string)
				if ns == "" {
					ns = routeNs
				}
				if !middlewares[group+"\x00"+mwKind+"\x00"+ns+"/"+name] {
					add(route, "traefikRouteMissingMiddleware",
						fmt.Sprintf("%s references non-existent %s %q", routeKind, mwKind, traefikRefLabel(ns, name, routeNs)))
				}
			}
		}
	}
	return findings
}

// traefikGroupOf returns the API group of an unstructured object (apiVersion
// before the "/"), e.g. "traefik.io" or "traefik.containo.us".
func traefikGroupOf(u *unstructured.Unstructured) string {
	if group, _, ok := strings.Cut(u.GetAPIVersion(), "/"); ok {
		return group
	}
	return u.GetAPIVersion()
}

// traefikRefLabel shows the namespace only when it differs from the router's,
// matching how operators write same-namespace refs (bare name).
func traefikRefLabel(ns, name, routeNs string) string {
	if ns != "" && ns != routeNs {
		return ns + "/" + name
	}
	return name
}

func nestedMaps(m map[string]any, key string) []map[string]any {
	raw, _, _ := unstructured.NestedSlice(m, key)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if mm, ok := item.(map[string]any); ok {
			out = append(out, mm)
		}
	}
	return out
}
