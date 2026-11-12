package server

import (
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/trace"
	"github.com/skyhook-io/radar/pkg/certs"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/traefik"
)

const traefikCertExpiringWithin = 14 * 24 * time.Hour

type traefikRoutesResponse struct {
	Installed   bool                                `json:"installed"`
	Coverage    map[string]integration.KindCoverage `json:"coverage"`
	Routes      []traefikRouteRow                   `json:"routes"`
	Middlewares []traefikMiddlewareUse              `json:"middlewares"`
	Truncated   bool                                `json:"truncated"`
}

type traefikRouteRef struct {
	Group     string `json:"group"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

// traefikRouteRow is one spec.routes[i] of an IngressRoute.
type traefikRouteRow struct {
	Ref         traefikRouteRef      `json:"ref"`
	Rule        int                  `json:"rule"`
	Match       string               `json:"match"`
	Hosts       []string             `json:"hosts"`
	EntryPoints []string             `json:"entryPoints"`
	Priority    *int64               `json:"priority,omitempty"`
	Middlewares []traefikRefFact     `json:"middlewares"`
	Services    []traefikBackendFact `json:"services"`
	TLS         traefikTLSFact       `json:"tls"`
	Problems    []string             `json:"problems"`
}

// traefikRefFact: Exists is nil when Radar can't tell, never false for
// something the caller merely can't read.
type traefikRefFact struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Exists    *bool  `json:"exists"`
	Via       string `json:"via,omitempty"`
}

type traefikBackendFact struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Port      string `json:"port,omitempty"`
	Kind      string `json:"kind"`
	Exists    *bool  `json:"exists"`
	ReadyPods *int   `json:"readyPods"`
	TotalPods *int   `json:"totalPods"`
}

// traefikTLSFact: Mode secret|resolver|default|none. Certificate is
// read|denied|notLoaded|notFound|notTLS for a secret; only read carries
// NotAfter.
type traefikTLSFact struct {
	Mode        string     `json:"mode"`
	Secret      string     `json:"secret,omitempty"`
	Resolver    string     `json:"resolver,omitempty"`
	Certificate string     `json:"certificate,omitempty"`
	NotAfter    *time.Time `json:"notAfter,omitempty"`
}

type traefikMiddlewareUse struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	UsedBy    int    `json:"usedBy"`
}

// traefikRoutesInput is everything the builder reads, as functions so the
// handler can bind them to the caller's access and the caches.
type traefikRoutesInput struct {
	Routes    []*unstructured.Unstructured
	Refs      func(*unstructured.Unstructured) []k8s.TraefikRuleRefs
	CanRead   func(issuesapi.EvidenceRead) bool
	NSVisible func(ns string) bool
	Service   func(ns, name string) (*corev1.Service, bool)
	Pods      func(ns string, sel labels.Selector) ([]*corev1.Pod, error)
	Cert      func(ns, name string) (state string, notAfter *time.Time)
	Now       time.Time
	Limit     int
}

// buildTraefikRoutes composes one row per IngressRoute rule from the caches,
// with no per-route API call. Problems: backend_missing, backend_not_ready,
// middleware_missing, cert_expired, cert_expiring, duplicate_host.
func buildTraefikRoutes(in traefikRoutesInput) (rows []traefikRouteRow, uses []traefikMiddlewareUse, truncated bool) {
	routes := append([]*unstructured.Unstructured(nil), in.Routes...)
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].GetNamespace() != routes[j].GetNamespace() {
			return routes[i].GetNamespace() < routes[j].GetNamespace()
		}
		return routes[i].GetName() < routes[j].GetName()
	})
	useCount := map[[2]string]int{}
	for _, route := range routes {
		ns := route.GetNamespace()
		rules, _, _ := unstructured.NestedSlice(route.Object, "spec", "routes")
		var refs []k8s.TraefikRuleRefs
		if in.Refs != nil {
			refs = in.Refs(route)
		}
		entryPoints, _, _ := unstructured.NestedStringSlice(route.Object, "spec", "entryPoints")
		tls := traefikTLS(route, in)
		for i, raw := range rules {
			r, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			if in.Limit > 0 && len(rows) >= in.Limit {
				truncated = true
				break
			}
			var ruleRefs k8s.TraefikRuleRefs
			if i < len(refs) {
				ruleRefs = refs[i]
			}
			match, _ := r["match"].(string)
			row := traefikRouteRow{
				Ref:         traefikRouteRef{Group: resourceid.GroupFromAPIVersion(route.GetAPIVersion()), Namespace: ns, Name: route.GetName()},
				Rule:        i,
				Match:       match,
				Hosts:       traefik.RuleHosts(match),
				EntryPoints: entryPoints,
				Priority:    traefikPriority(r["priority"]),
				TLS:         tls,
				Middlewares: []traefikRefFact{},
				Services:    []traefikBackendFact{},
			}
			for j, mw := range traefikRefMaps(r, "middlewares") {
				name, _ := mw["name"].(string)
				mwNs := ns
				if refNs, _ := mw["namespace"].(string); refNs != "" {
					mwNs = refNs
				}
				fact := traefikRefFact{Namespace: mwNs, Name: name}
				if j < len(ruleRefs.Middlewares) {
					fact.Exists = traefikVerdictExists(ruleRefs.Middlewares[j], mwNs, in)
					if fact.Exists != nil && !*fact.Exists {
						fact.Via = ruleRefs.Middlewares[j].Via
					}
				}
				if name != "" && !strings.Contains(name, "@") {
					useCount[[2]string{mwNs, name}]++
				}
				row.Middlewares = append(row.Middlewares, fact)
			}
			for j, svc := range traefikRefMaps(r, "services") {
				row.Services = append(row.Services, traefikBackend(svc, ns, ruleRefs, j, in))
			}
			row.Problems = traefikRowProblems(row, in.Now)
			rows = append(rows, row)
		}
		if truncated {
			break
		}
	}
	markDuplicateHosts(rows)
	for key, n := range useCount {
		uses = append(uses, traefikMiddlewareUse{Namespace: key[0], Name: key[1], UsedBy: n})
	}
	sort.Slice(uses, func(i, j int) bool {
		if uses[i].Namespace != uses[j].Namespace {
			return uses[i].Namespace < uses[j].Namespace
		}
		return uses[i].Name < uses[j].Name
	})
	return rows, uses, truncated
}

func traefikRefMaps(r map[string]any, key string) []map[string]any {
	raw, _ := r[key].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if m, ok := item.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func traefikPriority(v any) *int64 {
	switch p := v.(type) {
	case int64:
		return &p
	case float64:
		n := int64(p)
		return &n
	case int:
		n := int64(p)
		return &n
	}
	return nil
}

// traefikVerdictExists keeps the detector's answer only when the caller may
// see every read behind it and the target's namespace.
func traefikVerdictExists(v k8s.TraefikRefVerdict, ns string, in traefikRoutesInput) *bool {
	if v.Missing == nil || (in.NSVisible != nil && !in.NSVisible(ns)) {
		return nil
	}
	for _, read := range v.Reads {
		if in.CanRead != nil && !in.CanRead(read) {
			return nil
		}
	}
	exists := !*v.Missing
	return &exists
}

func traefikBackend(svc map[string]any, routeNs string, refs k8s.TraefikRuleRefs, j int, in traefikRoutesInput) traefikBackendFact {
	name, _ := svc["name"].(string)
	kind, _ := svc["kind"].(string)
	if kind == "" {
		kind = "Service"
	}
	ns := routeNs
	if refNs, _ := svc["namespace"].(string); refNs != "" {
		ns = refNs
	}
	fact := traefikBackendFact{Namespace: ns, Name: name, Kind: kind}
	if port, ok := svc["port"]; ok && port != nil {
		fact.Port = fmt.Sprint(port)
	}
	if j < len(refs.Services) {
		fact.Exists = traefikVerdictExists(refs.Services[j], ns, in)
	}
	if kind != "Service" || (in.NSVisible != nil && !in.NSVisible(ns)) || in.Service == nil || in.Pods == nil {
		return fact
	}
	s, ok := in.Service(ns, name)
	if !ok || len(s.Spec.Selector) == 0 || s.Spec.Type == corev1.ServiceTypeExternalName {
		return fact
	}
	pods, err := in.Pods(ns, labels.SelectorFromSet(s.Spec.Selector))
	if err != nil {
		return fact
	}
	ready, total := trace.EndpointPodCounts(s, pods)
	fact.ReadyPods, fact.TotalPods = &ready, &total
	return fact
}

func traefikTLS(route *unstructured.Unstructured, in traefikRoutesInput) traefikTLSFact {
	tls, found, _ := unstructured.NestedMap(route.Object, "spec", "tls")
	if !found || tls == nil {
		return traefikTLSFact{Mode: "none"}
	}
	if secret, _ := tls["secretName"].(string); secret != "" {
		fact := traefikTLSFact{Mode: "secret", Secret: secret, Certificate: "notLoaded"}
		if in.Cert != nil {
			state, notAfter := in.Cert(route.GetNamespace(), secret)
			fact.Certificate = state
			if state == "read" {
				fact.NotAfter = notAfter
			}
		}
		return fact
	}
	if resolver, _ := tls["certResolver"].(string); resolver != "" {
		return traefikTLSFact{Mode: "resolver", Resolver: resolver}
	}
	return traefikTLSFact{Mode: "default"}
}

func traefikRowProblems(row traefikRouteRow, now time.Time) []string {
	problems := []string{}
	add := func(code string, cond bool) {
		if cond {
			problems = append(problems, code)
		}
	}
	missingBackend, notReady, missingMw := false, false, false
	for _, s := range row.Services {
		missingBackend = missingBackend || (s.Exists != nil && !*s.Exists)
		notReady = notReady || (s.TotalPods != nil && s.ReadyPods != nil && *s.ReadyPods == 0)
	}
	for _, m := range row.Middlewares {
		missingMw = missingMw || (m.Exists != nil && !*m.Exists)
	}
	add("backend_missing", missingBackend)
	add("backend_not_ready", notReady)
	add("middleware_missing", missingMw)
	if row.TLS.Certificate == "read" && row.TLS.NotAfter != nil {
		add("cert_expired", row.TLS.NotAfter.Before(now))
		add("cert_expiring", !row.TLS.NotAfter.Before(now) && row.TLS.NotAfter.Sub(now) <= traefikCertExpiringWithin)
	}
	return problems
}

// markDuplicateHosts flags rows of different IngressRoutes whose normalized
// match is equal and whose entry points overlap (none listed means Traefik's
// default, which overlaps everything). It's a hint: "matched by more than
// one route".
func markDuplicateHosts(rows []traefikRouteRow) {
	byMatch := map[string][]int{}
	for i, r := range rows {
		if len(r.Hosts) == 0 {
			continue
		}
		key := traefik.NormalizeMatch(r.Match)
		byMatch[key] = append(byMatch[key], i)
	}
	for _, idx := range byMatch {
		for a := 0; a < len(idx); a++ {
			for b := a + 1; b < len(idx); b++ {
				ra, rb := &rows[idx[a]], &rows[idx[b]]
				if ra.Ref == rb.Ref || !entryPointsOverlap(ra.EntryPoints, rb.EntryPoints) {
					continue
				}
				for _, r := range []*traefikRouteRow{ra, rb} {
					if !slices.Contains(r.Problems, "duplicate_host") {
						r.Problems = append(r.Problems, "duplicate_host")
					}
				}
			}
		}
	}
}

func entryPointsOverlap(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return true
	}
	for _, x := range a {
		for _, y := range b {
			if strings.EqualFold(x, y) {
				return true
			}
		}
	}
	return false
}

var (
	traefikRouteGroups      = []string{"traefik.io", "traefik.containo.us"}
	traefikIngressRouteKind = integration.WorkspaceKind{Key: "ingressroutes", Group: "traefik.io", Kind: "IngressRoute", Resource: "ingressroutes"}
	traefikMiddlewareKind   = integration.WorkspaceKind{Key: "middlewares", Group: "traefik.io", Kind: "Middleware", Resource: "middlewares"}
)

const (
	traefikRouteRowLimit      = 5000
	traefikMiddlewareRowLimit = 200
)

// handleTraefikRoutes serves one row per IngressRoute rule (match, hosts,
// entry points, middlewares, backends with ready pods, TLS and problems),
// composed from the caches in one request, as the caller sees it:
//   - IngressRoutes and Middlewares through readWorkspaceKind, so a row exists
//     only where the caller can list IngressRoutes, with per-kind coverage;
//   - whether a Service or Middleware exists comes from the live
//     missing-reference detector, kept only where the caller may see every
//     read behind it (issueEvidenceAccess) and the target's namespace, else
//     null ("can't tell"), never "missing";
//   - a certificate's expiry only where the caller may list Secrets in the
//     route's namespace; only notAfter leaves the server, never Secret data.
//
// ?namespace=&name= returns one IngressRoute's rows, with usedBy for its
// middlewares counted over every route the caller can see; ?middleware=ns/name
// the rows that use a Middleware (at most 200). On a shared listener without
// auth it reads as Radar's identity; a cross-site request is refused by
// requireSameOrigin.
func (s *Server) handleTraefikRoutes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	routeNs, routeName, middleware := q.Get("namespace"), q.Get("name"), q.Get("middleware")
	if middleware != "" && (routeNs != "" || routeName != "") {
		s.writeError(w, http.StatusBadRequest, "namespace/name and middleware can't be combined")
		return
	}
	if (routeNs == "") != (routeName == "") {
		s.writeError(w, http.StatusBadRequest, "namespace and name go together")
		return
	}
	mwNs, mwName, _ := strings.Cut(middleware, "/")
	if middleware != "" && (mwNs == "" || mwName == "") {
		s.writeError(w, http.StatusBadRequest, "middleware must be namespace/name")
		return
	}
	if !s.requireConnected(w) {
		return
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}
	resp := traefikRoutesResponse{Coverage: map[string]integration.KindCoverage{}, Routes: []traefikRouteRow{}, Middlewares: []traefikMiddlewareUse{}}
	discovery := k8s.GetResourceDiscovery()
	if discovery != nil {
		for _, g := range traefikRouteGroups {
			if _, ok := discovery.GetGVRWithGroup("IngressRoute", g); ok {
				resp.Installed = true
			}
		}
	}
	if !resp.Installed {
		s.writeJSON(w, resp)
		return
	}

	namespaces := s.parseNamespacesForUser(r)
	budget := newSyncBudget(r.Context())
	routeAccess, routes := s.readWorkspaceKind(r, cache, traefikIngressRouteKind, namespaces, traefikRouteGroups, budget)
	mwAccess, _ := s.readWorkspaceKind(r, cache, traefikMiddlewareKind, namespaces, traefikRouteGroups, budget)
	svcAccess, _ := s.typedKindScope(r, cache, namespaces, "", "services")
	resp.Coverage["ingressroutes"] = routeAccess.Coverage()
	resp.Coverage["middlewares"] = mwAccess.Coverage()
	resp.Coverage["services"] = svcAccess.Coverage()
	resp.Coverage["secrets"] = traefikSecretsCoverage(s.secretReadableNamespaces(r, namespaces), namespaces)

	assessor := k8s.NewTraefikRouteAssessor(cache, k8s.GetDynamicResourceCache(), discovery)
	in := traefikRoutesInput{
		Routes:    routes,
		Refs:      assessor.RuleRefs,
		CanRead:   s.issueEvidenceAccess(r),
		NSVisible: s.callerNamespaces(r),
		Service: func(ns, name string) (*corev1.Service, bool) {
			lister := cache.Services()
			if lister == nil {
				return nil, false
			}
			svc, err := lister.Services(ns).Get(name)
			return svc, err == nil
		},
		Pods: func(ns string, sel labels.Selector) ([]*corev1.Pod, error) {
			lister := cache.Pods()
			if lister == nil {
				return nil, fmt.Errorf("pods not cached")
			}
			return lister.Pods(ns).List(sel)
		},
		Cert: func(ns, name string) (string, *time.Time) {
			if allowed := s.secretReadableNamespaces(r, []string{ns}); len(allowed) == 0 {
				return "denied", nil
			}
			obj, found, ok := cache.ReadableObject("secrets", ns, name)
			switch {
			case !ok:
				return "notLoaded", nil
			case !found:
				return "notFound", nil
			}
			sec, isSecret := obj.(*corev1.Secret)
			if !isSecret {
				return "notTLS", nil
			}
			cert, isTLS := certs.FromTLSSecret(sec)
			if !isTLS || cert.NotAfter == nil {
				return "notTLS", nil
			}
			return "read", cert.NotAfter
		},
		Now:   time.Now(),
		Limit: traefikRouteRowLimit,
	}
	rows, uses, truncated := buildTraefikRoutes(in)
	switch {
	case routeName != "":
		used := map[[2]string]bool{}
		for _, row := range rows {
			if row.Ref.Namespace == routeNs && row.Ref.Name == routeName {
				resp.Routes = append(resp.Routes, row)
				for _, m := range row.Middlewares {
					used[[2]string{m.Namespace, m.Name}] = true
				}
			}
		}
		for _, u := range uses {
			if used[[2]string{u.Namespace, u.Name}] {
				resp.Middlewares = append(resp.Middlewares, u)
			}
		}
	case middleware != "":
		for _, row := range rows {
			for _, m := range row.Middlewares {
				if m.Namespace == mwNs && m.Name == mwName {
					if len(resp.Routes) >= traefikMiddlewareRowLimit {
						truncated = true
						break
					}
					resp.Routes = append(resp.Routes, row)
					break
				}
			}
		}
		for _, u := range uses {
			if u.Namespace == mwNs && u.Name == mwName {
				resp.Middlewares = append(resp.Middlewares, u)
			}
		}
	default:
		resp.Routes, resp.Middlewares = rows, uses
	}
	if resp.Routes == nil {
		resp.Routes = []traefikRouteRow{}
	}
	if resp.Middlewares == nil {
		resp.Middlewares = []traefikMiddlewareUse{}
	}
	resp.Truncated = truncated
	s.writeJSON(w, resp)
}

// traefikSecretsCoverage reads secretReadableNamespaces' answer: nil is every
// namespace, empty none.
func traefikSecretsCoverage(readable, requested []string) integration.KindCoverage {
	switch {
	case readable == nil:
		return integration.KindCoverage{State: integration.KindCoverageFull}
	case len(readable) == 0:
		return integration.KindCoverage{State: integration.KindCoverageDenied}
	case requested != nil && len(readable) == len(requested):
		return integration.KindCoverage{State: integration.KindCoverageFull}
	}
	return integration.KindCoverage{State: integration.KindCoveragePartial, AllowedNamespaces: readable}
}
