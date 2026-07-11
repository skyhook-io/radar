package audit

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// checkTraefikDanglingRefs flags Traefik resources that reference a Service,
// TraefikService, or Middleware that doesn't exist: routers (spec.routes →
// Service/Middleware), chain middlewares (spec.chain.middlewares → Middleware),
// and errors middlewares (spec.errors.service → Service). Traefik ships no
// validation webhook or linter — a typo'd ref silently drops traffic or skips a
// middleware until someone reads the controller logs — so this is genuinely
// additive (cf. Kubevious' built-in "missing Middleware reference" validator).
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
// Scope is still deliberately partial: nested TraefikService weighted/mirror
// sub-services, ServersTransport refs, and TLS-secret refs can also dangle and
// are not yet covered.
func checkTraefikDanglingRefs(tr *evalTracker, input *CheckInput) []Finding {
	if len(input.IngressRoutes) == 0 && len(input.MiddlewareSubjects) == 0 {
		return nil
	}

	// Core Services for cross-namespace ref resolution (cluster-wide). Group-
	// agnostic (always core/v1). Trust level matches ingressNoMatchingService.
	coreServices := make(map[string]bool, len(input.AllServices))
	for _, svc := range input.AllServices {
		coreServices[svc.Namespace+"/"+svc.Name] = true
	}
	servicesListed := input.AllServices != nil

	// Target inventories are gathered cluster-wide. Keys carry group + (for
	// middlewares) kind so a traefik.io router only resolves against traefik.io
	// targets, never the legacy group.
	traefikServices := make(map[string]bool, len(input.TraefikServices)) // group\x00ns/name
	for _, ts := range input.TraefikServices {
		traefikServices[traefikGroupOf(ts)+"\x00"+ts.GetNamespace()+"/"+ts.GetName()] = true
	}
	middlewares := make(map[string]bool, len(input.Middlewares)) // group\x00kind\x00ns/name
	for _, mw := range input.Middlewares {
		middlewares[traefikGroupOf(mw)+"\x00"+mw.GetKind()+"\x00"+mw.GetNamespace()+"/"+mw.GetName()] = true
	}
	// authoritative[group\x00Kind]: only assert a kind's absence when a synced
	// cluster-wide informer backs it (else the cache may know a subset of ns).
	authoritative := input.TraefikAuthoritativeKinds

	var findings []Finding
	seen := make(map[string]bool)
	add := func(subject *unstructured.Unstructured, checkID, msg string) {
		key := string(subject.GetUID()) + "\x00" + checkID + "\x00" + msg
		if seen[key] {
			return
		}
		seen[key] = true
		// Group is intentionally left empty — the audit backfills group from the
		// builtin table (CRDs resolve to ""), which is what the per-resource
		// drill-down looks up. Setting it would hide these findings there.
		findings = append(findings, Finding{
			Kind:      subject.GetKind(),
			Namespace: subject.GetNamespace(), Name: subject.GetName(),
			CheckID: checkID, Category: CategoryReliability, Severity: SeverityWarning,
			Message: msg,
		})
	}

	// checkServiceRef resolves a Traefik Service reference (core Service or, when
	// kind=="TraefikService", a TraefikService) against the authoritative
	// inventory and reports it as missing via `add`. Shared by router
	// spec.routes[].services and errors-middleware spec.errors.service — both use
	// the same Service ref shape. `refDesc` labels the subject in the message.
	checkServiceRef := func(subject *unstructured.Unstructured, group, defaultNs, refDesc, checkID string, s map[string]any) {
		name, _ := s["name"].(string)
		if name == "" {
			return
		}
		ns, _ := s["namespace"].(string)
		if ns == "" {
			ns = defaultNs
		}
		if kind, _ := s["kind"].(string); kind == "TraefikService" {
			if authoritative[group+"\x00TraefikService"] && !traefikServices[group+"\x00"+ns+"/"+name] {
				add(subject, checkID,
					fmt.Sprintf("%s references TraefikService %q which is not found in the cluster", refDesc, traefikRefLabel(ns, name, defaultNs)))
			}
		} else if servicesListed && !coreServices[ns+"/"+name] {
			add(subject, checkID,
				fmt.Sprintf("%s references Service %q which is not found in the cluster", refDesc, traefikRefLabel(ns, name, defaultNs)))
		}
	}

	// checkMiddlewareRef resolves a Traefik MiddlewareRef (name + optional
	// namespace) against the authoritative inventory for mwKind. Shared by router
	// spec.routes[].middlewares and chain-middleware spec.chain.middlewares[].
	checkMiddlewareRef := func(subject *unstructured.Unstructured, group, mwKind, defaultNs, refDesc, checkID string, m map[string]any) {
		if !authoritative[group+"\x00"+mwKind] {
			return // no synced cluster-wide inventory for this kind → can't assert absence
		}
		name, _ := m["name"].(string)
		if name == "" {
			return
		}
		ns, _ := m["namespace"].(string)
		if ns == "" {
			ns = defaultNs
		}
		if !middlewares[group+"\x00"+mwKind+"\x00"+ns+"/"+name] {
			add(subject, checkID,
				fmt.Sprintf("%s references %s %q which is not found in the cluster", refDesc, mwKind, traefikRefLabel(ns, name, defaultNs)))
		}
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

		// A subject counts as evaluated only when (a) the ref inventory it
		// would be checked against is authoritative AND (b) it actually
		// carries at least one ref of that type — a route with no
		// middlewares isn't "passing" the dangling-middleware check, it's
		// out of scope.
		hasCoreSvcRef, hasTraefikSvcRef, hasMiddlewareRef := false, false, false
		routes, _, _ := unstructured.NestedSlice(route.Object, "spec", "routes")
		for _, r := range routes {
			rm, ok := r.(map[string]any)
			if !ok {
				continue
			}
			for _, svc := range nestedMaps(rm, "services") {
				if kind, _ := svc["kind"].(string); kind == "TraefikService" {
					hasTraefikSvcRef = true
				} else {
					hasCoreSvcRef = true
				}
			}
			if len(nestedMaps(rm, "middlewares")) > 0 {
				hasMiddlewareRef = true
			}
		}
		// Evaluated only when EVERY present ref kind is checkable — with a
		// core-Service ref but only the TraefikService inventory listed, the
		// core ref goes unchecked and "passed" would be a lie.
		svcRefsCheckable := (hasCoreSvcRef || hasTraefikSvcRef) &&
			(!hasCoreSvcRef || servicesListed) &&
			(!hasTraefikSvcRef || authoritative[group+"\x00TraefikService"])
		if svcRefsCheckable {
			tr.record("traefikRouteMissingService", routeNs)
		}
		if hasMiddlewareRef && authoritative[group+"\x00"+mwKind] {
			tr.record("traefikRouteMissingMiddleware", routeNs)
		}
		for _, r := range routes {
			rm, ok := r.(map[string]any)
			if !ok {
				continue
			}
			for _, s := range nestedMaps(rm, "services") {
				checkServiceRef(route, group, routeNs, routeKind, "traefikRouteMissingService", s)
			}
			for _, m := range nestedMaps(rm, "middlewares") {
				checkMiddlewareRef(route, group, mwKind, routeNs, routeKind, "traefikRouteMissingMiddleware", m)
			}
		}
	}

	// Middleware subjects (audited namespaces): a `chain` middleware referencing a
	// missing Middleware, or an `errors` middleware referencing a missing Service.
	// Same silent-failure class as routes — Traefik accepts the bad ref and the
	// chain/error-page simply doesn't work. Only HTTP Middlewares carry chain/
	// errors; a MiddlewareTCP has neither key, so its loops are no-ops.
	for _, mw := range input.MiddlewareSubjects {
		group := traefikGroupOf(mw)
		mwKind := mw.GetKind()
		mwNs := mw.GetNamespace()

		chain, _, _ := unstructured.NestedSlice(mw.Object, "spec", "chain", "middlewares")
		errSvc, hasErrSvc, _ := unstructured.NestedMap(mw.Object, "spec", "errors", "service")
		// Only HTTP Middlewares can carry chain/errors — and a Middleware
		// counts as evaluated only for the check whose ref it actually
		// carries (a middleware without spec.errors.service isn't "passing"
		// the errors check, it's out of scope).
		if mwKind == "Middleware" {
			if len(chain) > 0 && authoritative[group+"\x00"+mwKind] {
				tr.record("traefikChainMissingMiddleware", mwNs)
			}
			if hasErrSvc {
				errIsTraefik, _ := errSvc["kind"].(string)
				checkable := (errIsTraefik == "TraefikService" && authoritative[group+"\x00TraefikService"]) ||
					(errIsTraefik != "TraefikService" && servicesListed)
				if checkable {
					tr.record("traefikErrorsMissingService", mwNs)
				}
			}
		}

		for _, c := range chain {
			cm, ok := c.(map[string]any)
			if !ok {
				continue
			}
			checkMiddlewareRef(mw, group, mwKind, mwNs, mwKind+" chain", "traefikChainMissingMiddleware", cm)
		}

		if hasErrSvc {
			checkServiceRef(mw, group, mwNs, mwKind+" errors", "traefikErrorsMissingService", errSvc)
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
