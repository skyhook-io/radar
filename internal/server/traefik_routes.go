package server

import (
	"fmt"
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
