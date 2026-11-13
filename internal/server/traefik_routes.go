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
	// Routes is every IngressRoute the caller can see; usedBy and
	// duplicate_host are computed over all of them.
	Routes []*unstructured.Unstructured
	// Select picks the routes that get full rows; nil selects all.
	Select    func(*unstructured.Unstructured) bool
	Refs      func(*unstructured.Unstructured) []k8s.TraefikRuleRefs
	CanRead   func(issuesapi.EvidenceRead) bool
	NSVisible func(ns string) bool
	Service   func(ns, name string) (*corev1.Service, bool)
	Pods      func(ns string, sel labels.Selector) ([]*corev1.Pod, error)
	Cert      func(ns, name string) (state string, notAfter *time.Time)
	Now       time.Time
	// Limit caps the full rows; 0 is no cap.
	Limit int
}

// buildTraefikRoutes composes one row per rule of the selected IngressRoutes
// from the caches, with no per-route API call. Problems: backend_missing,
// backend_not_ready, middleware_missing, cert_expired, cert_expiring,
// duplicate_host. usedBy counts distinct routes.
func buildTraefikRoutes(in traefikRoutesInput) (rows []traefikRouteRow, uses []traefikMiddlewareUse, truncated bool) {
	routes := append([]*unstructured.Unstructured(nil), in.Routes...)
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].GetNamespace() != routes[j].GetNamespace() {
			return routes[i].GetNamespace() < routes[j].GetNamespace()
		}
		return routes[i].GetName() < routes[j].GetName()
	})
	refsOf := in.Refs
	if refsOf == nil {
		refsOf = k8s.ParseTraefikRuleRefs
	}
	users := map[[2]string]map[traefikRouteRef]bool{}
	counts := map[[2]string]traefikPodCounts{}
	// all holds a light row for every rule (for duplicate_host); full marks
	// the ones that were enriched.
	var all []traefikRouteRow
	var full []int
	for _, route := range routes {
		ref := traefikRouteRef{Group: resourceid.GroupFromAPIVersion(route.GetAPIVersion()), Namespace: route.GetNamespace(), Name: route.GetName()}
		for _, rule := range k8s.ParseTraefikRuleRefs(route) {
			for _, m := range rule.Middlewares {
				if m.Name == "" || strings.Contains(m.Name, "@") {
					continue
				}
				key := [2]string{m.Namespace, m.Name}
				if users[key] == nil {
					users[key] = map[traefikRouteRef]bool{}
				}
				users[key][ref] = true
			}
		}
		selected := !truncated && (in.Select == nil || in.Select(route))
		var refs []k8s.TraefikRuleRefs
		var tls traefikTLSFact
		if selected {
			refs = refsOf(route)
			tls = traefikTLS(route, in)
		}
		rules, _, _ := unstructured.NestedSlice(route.Object, "spec", "routes")
		entryPoints, _, _ := unstructured.NestedStringSlice(route.Object, "spec", "entryPoints")
		for i, raw := range rules {
			r, ok := raw.(map[string]any)
			if !ok {
				continue
			}
			match, _ := r["match"].(string)
			row := traefikRouteRow{Ref: ref, Rule: i, Match: match, Hosts: traefik.RuleHosts(match), EntryPoints: entryPoints}
			if selected && in.Limit > 0 && len(full) >= in.Limit {
				truncated, selected = true, false
			}
			if selected {
				var ruleRefs k8s.TraefikRuleRefs
				if i < len(refs) {
					ruleRefs = refs[i]
				}
				row.Priority = traefikPriority(r["priority"])
				row.TLS = tls
				row.Middlewares = []traefikRefFact{}
				row.Services = []traefikBackendFact{}
				for _, v := range ruleRefs.Middlewares {
					fact := traefikRefFact{Namespace: v.Namespace, Name: v.Name, Exists: traefikVerdictExists(v, in)}
					if fact.Exists != nil && !*fact.Exists {
						fact.Via = v.Via
					}
					row.Middlewares = append(row.Middlewares, fact)
				}
				for _, v := range ruleRefs.Services {
					row.Services = append(row.Services, traefikBackend(v, in, counts))
				}
				row.Problems = traefikRowProblems(row, in.Now)
				full = append(full, len(all))
			}
			all = append(all, row)
		}
	}
	markDuplicateHosts(all)
	rows = make([]traefikRouteRow, 0, len(full))
	for _, i := range full {
		rows = append(rows, all[i])
	}
	for key, refs := range users {
		uses = append(uses, traefikMiddlewareUse{Namespace: key[0], Name: key[1], UsedBy: len(refs)})
	}
	sort.Slice(uses, func(i, j int) bool {
		if uses[i].Namespace != uses[j].Namespace {
			return uses[i].Namespace < uses[j].Namespace
		}
		return uses[i].Name < uses[j].Name
	})
	return rows, uses, truncated
}

func traefikPriority(v any) *int64 {
	if p, ok := v.(int64); ok {
		return &p
	}
	return nil
}

// traefikVerdictExists keeps the detector's answer only when the caller may
// see every read behind it and the target's namespace.
func traefikVerdictExists(v k8s.TraefikRefVerdict, in traefikRoutesInput) *bool {
	if v.Missing == nil || (in.NSVisible != nil && !in.NSVisible(v.Namespace)) {
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

type traefikPodCounts struct{ ready, total *int }

// traefikBackend counts ready pods only for a Service Traefik routes to,
// where the caller may list Services and Pods, once per Service per request.
func traefikBackend(v k8s.TraefikRefVerdict, in traefikRoutesInput, counts map[[2]string]traefikPodCounts) traefikBackendFact {
	fact := traefikBackendFact{Namespace: v.Namespace, Name: v.Name, Port: v.Port, Kind: v.Kind, Exists: traefikVerdictExists(v, in)}
	ns := v.Namespace
	if v.Kind != "Service" || v.Refused || in.Service == nil || in.Pods == nil ||
		(in.NSVisible != nil && !in.NSVisible(ns)) ||
		(in.CanRead != nil && (!in.CanRead(issuesapi.EvidenceRead{Resource: "services", Namespace: ns, Verb: "list"}) ||
			!in.CanRead(issuesapi.EvidenceRead{Resource: "pods", Namespace: ns, Verb: "list"}))) {
		return fact
	}
	key := [2]string{ns, v.Name}
	c, seen := counts[key]
	if !seen {
		if s, ok := in.Service(ns, v.Name); ok && len(s.Spec.Selector) > 0 && s.Spec.Type != corev1.ServiceTypeExternalName {
			if pods, err := in.Pods(ns, labels.SelectorFromSet(s.Spec.Selector)); err == nil {
				ready, total := trace.EndpointPodCounts(s, pods)
				c = traefikPodCounts{&ready, &total}
			}
		}
		counts[key] = c
	}
	fact.ReadyPods, fact.TotalPods = c.ready, c.total
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
	// Per normalized match, which routes listed each entry point (and which
	// listed none), keeping only whether more than one route did: enough to
	// tell whether a route other than this one overlaps it.
	type owners struct {
		first traefikRouteRef
		many  bool
	}
	add := func(m map[string]*owners, key string, ref traefikRouteRef) {
		if o := m[key]; o == nil {
			m[key] = &owners{first: ref}
		} else if o.first != ref {
			o.many = true
		}
	}
	other := func(o *owners, ref traefikRouteRef) bool { return o != nil && (o.many || o.first != ref) }
	const anyEntryPoint = "\x00any"
	const noEntryPoints = "\x00none"
	groups := map[string]map[string]*owners{}
	for _, r := range rows {
		if len(r.Hosts) == 0 {
			continue
		}
		key := traefik.NormalizeMatch(r.Match)
		g := groups[key]
		if g == nil {
			g = map[string]*owners{}
			groups[key] = g
		}
		add(g, anyEntryPoint, r.Ref)
		if len(r.EntryPoints) == 0 {
			add(g, noEntryPoints, r.Ref)
		}
		for _, ep := range r.EntryPoints {
			add(g, strings.ToLower(ep), r.Ref)
		}
	}
	for i := range rows {
		r := &rows[i]
		if len(r.Hosts) == 0 {
			continue
		}
		g := groups[traefik.NormalizeMatch(r.Match)]
		dup := other(g[noEntryPoints], r.Ref) || (len(r.EntryPoints) == 0 && other(g[anyEntryPoint], r.Ref))
		for _, ep := range r.EntryPoints {
			dup = dup || other(g[strings.ToLower(ep)], r.Ref)
		}
		if dup && !slices.Contains(r.Problems, "duplicate_host") {
			r.Problems = append(r.Problems, "duplicate_host")
		}
	}
}

var (
	traefikRouteGroups      = []string{"traefik.io", "traefik.containo.us"}
	traefikIngressRouteKind = integration.WorkspaceKind{Key: "ingressroutes", Group: "traefik.io", Kind: "IngressRoute", Resource: "ingressroutes"}
	traefikMiddlewareKind   = integration.WorkspaceKind{Key: "middlewares", Group: "traefik.io", Kind: "Middleware", Resource: "middlewares"}
)

// traefikRouteRowLimit caps the list; traefikMiddlewareRouteLimit caps the
// distinct routes a Middleware's "used by" returns.
var (
	traefikRouteRowLimit        = 5000
	traefikMiddlewareRouteLimit = 200
)

// handleTraefikRoutes serves one row per IngressRoute rule (match, hosts,
// entry points, middlewares, backends with ready pods, TLS and problems),
// composed from the caches in one request, as the caller sees it:
//   - IngressRoutes and Middlewares through readWorkspaceKind, so a row exists
//     only where the caller can list IngressRoutes, with per-kind coverage;
//   - whether a Service or Middleware exists comes from the live
//     missing-reference detector, kept only where the caller may see every
//     read behind it, by kind and namespace (traefikReadGate), and the target's namespace, else
//     null ("can't tell"), never "missing";
//   - a certificate's expiry only where the caller may list Secrets in the
//     route's namespace; only notAfter leaves the server, never Secret data.
//
// ?namespace=&name=[&group=] returns one IngressRoute's rows (?namespace
// names the route; only ?namespaces narrows the read), with usedBy for its
// middlewares counted over every route the caller can see; ?middleware=ns/name
// the rows that use a Middleware (at most 200 routes). Only the routes a mode
// returns get full rows; the plain list stops at traefikRouteRowLimit rows.
// On a shared listener without auth it reads as Radar's identity; a
// cross-site request is refused by requireSameOrigin.
func (s *Server) handleTraefikRoutes(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	routeNs, routeName, routeGroup, middleware := q.Get("namespace"), q.Get("name"), q.Get("group"), q.Get("middleware")
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
	var groups []string
	if discovery != nil {
		for _, g := range traefikRouteGroups {
			if _, ok := discovery.GetGVRWithGroup("IngressRoute", g); ok {
				groups = append(groups, g)
			}
		}
	}
	resp.Installed = len(groups) > 0
	if !resp.Installed {
		s.writeJSON(w, resp)
		return
	}

	// ?namespace= names the drawer's route; only ?namespaces narrows the
	// read, so usedBy and duplicate_host see every namespace.
	scoped := r.Clone(r.Context())
	sq := scoped.URL.Query()
	sq.Del("namespace")
	scoped.URL.RawQuery = sq.Encode()
	namespaces := s.parseNamespacesForUser(scoped)
	budget := newSyncBudget(r.Context())
	routeAccess, routes := s.readTraefikKind(scoped, cache, traefikIngressRouteKind, groups, namespaces, budget)
	mwAccess, _ := s.readTraefikKind(scoped, cache, traefikMiddlewareKind, groups, namespaces, budget)
	svcAccess, _ := s.typedKindScope(scoped, cache, namespaces, "", "services")
	resp.Coverage["ingressroutes"] = routeAccess.Coverage()
	resp.Coverage["middlewares"] = mwAccess.Coverage()
	resp.Coverage["services"] = svcAccess.Coverage()
	resp.Coverage["secrets"] = traefikSecretsCoverage(s.secretReadableNamespaces(r, namespaces), namespaces)

	assessor := k8s.NewTraefikRouteAssessor(cache, k8s.GetDynamicResourceCache(), discovery)
	certMemo := map[[2]string]traefikCertAnswer{}
	in := traefikRoutesInput{
		Routes: routes,
		Refs:   assessor.RuleRefs,
		CanRead: traefikReadGate(s.issueEvidenceAccess(r), func(group, resource, ns, verb string) bool {
			return s.canRead(r, group, resource, ns, verb)
		}),
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
			key := [2]string{ns, name}
			a, ok := certMemo[key]
			if !ok {
				a = readTraefikCert(s, r, cache, ns, name)
				certMemo[key] = a
			}
			return a.state, a.notAfter
		},
		Now: time.Now(),
	}
	mwTruncated := false
	switch {
	case routeName != "":
		in.Select = func(u *unstructured.Unstructured) bool {
			return u.GetNamespace() == routeNs && u.GetName() == routeName &&
				(routeGroup == "" || resourceid.GroupFromAPIVersion(u.GetAPIVersion()) == routeGroup)
		}
	case middleware != "":
		// Selection stops at the cap so routes past it aren't enriched.
		selected := 0
		in.Select = func(u *unstructured.Unstructured) bool {
			for _, rule := range k8s.ParseTraefikRuleRefs(u) {
				for _, m := range rule.Middlewares {
					if m.Namespace == mwNs && m.Name == mwName {
						if selected >= traefikMiddlewareRouteLimit {
							mwTruncated = true
							return false
						}
						selected++
						return true
					}
				}
			}
			return false
		}
	default:
		in.Limit = traefikRouteRowLimit
	}
	rows, uses, truncated := buildTraefikRoutes(in)
	switch {
	case routeName != "":
		used := map[[2]string]bool{}
		for _, row := range rows {
			for _, m := range row.Middlewares {
				used[[2]string{m.Namespace, m.Name}] = true
			}
		}
		resp.Routes = rows
		for _, u := range uses {
			if used[[2]string{u.Namespace, u.Name}] {
				resp.Middlewares = append(resp.Middlewares, u)
			}
		}
	case middleware != "":
		truncated = truncated || mwTruncated
		for _, row := range rows {
			if rowUsesMiddleware(row, mwNs, mwName) {
				resp.Routes = append(resp.Routes, row)
			}
		}
		for _, u := range uses {
			if u.Namespace == mwNs && u.Name == mwName {
				resp.Middlewares = append(resp.Middlewares, u)
			}
		}
		// "Used by" asks about the whole cluster: a caller who sees only
		// some namespaces can't be told that nothing else uses it.
		if cov := resp.Coverage["ingressroutes"]; cov.State == integration.KindCoverageFull {
			if visible := s.getUserNamespaces(r, nil); visible != nil {
				resp.Coverage["ingressroutes"] = integration.KindCoverage{State: integration.KindCoveragePartial, AllowedNamespaces: visible}
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

func rowUsesMiddleware(row traefikRouteRow, ns, name string) bool {
	for _, m := range row.Middlewares {
		if m.Namespace == ns && m.Name == name {
			return true
		}
	}
	return false
}

type traefikCertAnswer struct {
	state    string
	notAfter *time.Time
}

// readTraefikCert reads only a TLS Secret's expiry, where the caller may
// list Secrets in its namespace.
func readTraefikCert(s *Server, r *http.Request, cache *k8s.ResourceCache, ns, name string) traefikCertAnswer {
	if allowed := s.secretReadableNamespaces(r, []string{ns}); len(allowed) == 0 {
		return traefikCertAnswer{state: "denied"}
	}
	obj, found, ok := cache.ReadableObject("secrets", ns, name)
	switch {
	case !ok:
		return traefikCertAnswer{state: "notLoaded"}
	case !found:
		return traefikCertAnswer{state: "notFound"}
	}
	sec, isSecret := obj.(*corev1.Secret)
	if !isSecret {
		return traefikCertAnswer{state: "notTLS"}
	}
	cert, isTLS := certs.FromTLSSecret(sec)
	if !isTLS || cert.NotAfter == nil {
		return traefikCertAnswer{state: "notTLS"}
	}
	return traefikCertAnswer{state: "read", notAfter: cert.NotAfter}
}

// readTraefikKind reads one Traefik kind in each installed API group as the
// caller; coverage is the first group's that isn't full.
func (s *Server) readTraefikKind(r *http.Request, cache *k8s.ResourceCache, k integration.WorkspaceKind, groups, namespaces []string, budget *syncBudget) (integration.KindAccess, []*unstructured.Unstructured) {
	var access integration.KindAccess
	var out []*unstructured.Unstructured
	for i, g := range groups {
		kg := k
		kg.Group = g
		acc, list := s.readWorkspaceKind(r, cache, kg, namespaces, []string{g}, budget)
		if i == 0 || (access.State == integration.KindCoverageFull && acc.State != integration.KindCoverageFull) {
			access = acc
		}
		out = append(out, list...)
	}
	return access, out
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

// traefikReadGate requires, beyond the issue evidence gate (which takes a
// visible namespace as access to its ordinary kinds), the caller's own
// permission for the exact kind in each namespace, since a verdict or a
// count here answers about one named object.
func traefikReadGate(evidence func(issuesapi.EvidenceRead) bool, canRead func(group, resource, ns, verb string) bool) func(issuesapi.EvidenceRead) bool {
	return func(read issuesapi.EvidenceRead) bool {
		if !evidence(read) {
			return false
		}
		return read.Namespace == "" || canRead(read.Group, read.Resource, read.Namespace, read.Verb)
	}
}
