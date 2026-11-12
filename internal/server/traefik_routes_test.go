package server

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

func tRoute(ns, name string, entryPoints []any, tls map[string]any, rules ...map[string]any) *unstructured.Unstructured {
	rs := make([]any, len(rules))
	for i, r := range rules {
		rs[i] = r
	}
	spec := map[string]any{"routes": rs}
	if entryPoints != nil {
		spec["entryPoints"] = entryPoints
	}
	if tls != nil {
		spec["tls"] = tls
	}
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "traefik.io/v1alpha1", "kind": "IngressRoute", "metadata": map[string]any{"namespace": ns, "name": name}, "spec": spec}}
}

func tRule(match string, services []string, middlewares ...string) map[string]any {
	r := map[string]any{"match": match, "kind": "Rule"}
	var svcs, mws []any
	for _, s := range services {
		svcs = append(svcs, map[string]any{"name": s, "port": int64(80)})
	}
	for _, m := range middlewares {
		mws = append(mws, map[string]any{"name": m})
	}
	if svcs != nil {
		r["services"] = svcs
	}
	if mws != nil {
		r["middlewares"] = mws
	}
	return r
}

func boolp(b bool) *bool { return &b }

type tFixture struct {
	refs     map[string][]k8s.TraefikRuleRefs
	services map[string]*corev1.Service
	pods     map[string][]*corev1.Pod
	cert     map[string]struct {
		state string
		at    *time.Time
	}
	denyRead bool
	hidden   map[string]bool
}

func (f tFixture) input(routes ...*unstructured.Unstructured) traefikRoutesInput {
	return traefikRoutesInput{
		Routes: routes,
		Refs: func(u *unstructured.Unstructured) []k8s.TraefikRuleRefs {
			if r, ok := f.refs[u.GetNamespace()+"/"+u.GetName()]; ok {
				return r
			}
			return k8s.ParseTraefikRuleRefs(u)
		},
		CanRead:   func(issuesapi.EvidenceRead) bool { return !f.denyRead },
		NSVisible: func(ns string) bool { return !f.hidden[ns] },
		Service: func(ns, name string) (*corev1.Service, bool) {
			s, ok := f.services[ns+"/"+name]
			return s, ok
		},
		Pods: func(ns string, sel labels.Selector) ([]*corev1.Pod, error) { return f.pods[ns], nil },
		Cert: func(ns, name string) (string, *time.Time) {
			c, ok := f.cert[ns+"/"+name]
			if !ok {
				return "notFound", nil
			}
			return c.state, c.at
		},
		Now:   time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC),
		Limit: 5000,
	}
}

func readyPod(ready bool) *corev1.Pod {
	s := corev1.ConditionFalse
	if ready {
		s = corev1.ConditionTrue
	}
	return &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodRunning, PodIP: "10.0.0.1", Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: s}}}}
}

func selService(ns, name string) *corev1.Service {
	return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": name}}}
}

func hasProblem(r traefikRouteRow, code string) bool {
	for _, p := range r.Problems {
		if p == code {
			return true
		}
	}
	return false
}

func TestBuildTraefikRoutesRowsAndProblems(t *testing.T) {
	f := tFixture{
		services: map[string]*corev1.Service{"shop/ui": selService("shop", "ui")},
		pods:     map[string][]*corev1.Pod{"shop": {readyPod(false), readyPod(false)}},
		refs: map[string][]k8s.TraefikRuleRefs{"shop/ui": {
			{Services: []k8s.TraefikRefVerdict{{Kind: "Service", Namespace: "shop", Name: "ui", Missing: boolp(false)}}},
			{Services: []k8s.TraefikRefVerdict{{Kind: "Service", Namespace: "shop", Name: "gone", Missing: boolp(true)}}, Middlewares: []k8s.TraefikRefVerdict{{Kind: "Middleware", Namespace: "shop", Name: "auth", Missing: boolp(true)}}},
		}},
	}
	rows, _, truncated := buildTraefikRoutes(f.input(tRoute("shop", "ui", nil, nil,
		tRule("Host(`ui.example.com`)", []string{"ui"}),
		tRule("Host(`api.example.com`)", []string{"gone"}, "auth"))))
	if len(rows) != 2 || rows[0].Rule != 0 || rows[1].Rule != 1 || truncated {
		t.Fatalf("rows = %+v", rows)
	}
	if b := rows[0].Services[0]; b.ReadyPods == nil || *b.ReadyPods != 0 || *b.TotalPods != 2 || !hasProblem(rows[0], "backend_not_ready") {
		t.Errorf("row 0 = %+v", rows[0])
	}
	if rows[0].Hosts[0] != "ui.example.com" {
		t.Errorf("hosts = %v", rows[0].Hosts)
	}
	if m := rows[1].Middlewares[0]; m.Exists == nil || *m.Exists || !hasProblem(rows[1], "middleware_missing") || !hasProblem(rows[1], "backend_missing") {
		t.Errorf("row 1 = %+v", rows[1])
	}
}

func TestBuildTraefikRoutesCantTellIsNotMissing(t *testing.T) {
	refs := map[string][]k8s.TraefikRuleRefs{"shop/ui": {{Services: []k8s.TraefikRefVerdict{{Kind: "Service", Namespace: "other", Name: "ui", Missing: boolp(true), Reads: []issuesapi.EvidenceRead{{Resource: "services", Namespace: "other", Verb: "list"}}}}}}}
	route := tRoute("shop", "ui", nil, nil, map[string]any{"match": "Host(`ui`)", "services": []any{map[string]any{"name": "ui", "namespace": "other", "port": int64(80)}}})
	for name, f := range map[string]tFixture{
		"read denied":         {refs: refs, denyRead: true},
		"namespace invisible": {refs: refs, hidden: map[string]bool{"other": true}, services: map[string]*corev1.Service{"other/ui": selService("other", "ui")}},
	} {
		rows, _, _ := buildTraefikRoutes(f.input(route))
		b := rows[0].Services[0]
		if b.Exists != nil || hasProblem(rows[0], "backend_missing") {
			t.Errorf("%s: %+v", name, rows[0])
		}
		if name == "namespace invisible" && (b.ReadyPods != nil || b.TotalPods != nil) {
			t.Errorf("%s: counts must be unknown: %+v", name, b)
		}
	}
}

func TestBuildTraefikRoutesSelectorlessService(t *testing.T) {
	f := tFixture{services: map[string]*corev1.Service{"shop/ext": {ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "ext"}}}}
	rows, _, _ := buildTraefikRoutes(f.input(tRoute("shop", "ui", nil, nil, tRule("Host(`ui`)", []string{"ext"}))))
	if b := rows[0].Services[0]; b.ReadyPods != nil || b.TotalPods != nil || hasProblem(rows[0], "backend_not_ready") {
		t.Errorf("selectorless = %+v", rows[0])
	}
}

func TestBuildTraefikRoutesTLS(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	at := func(d time.Duration) *time.Time { v := now.Add(d); return &v }
	cases := []struct {
		name   string
		tls    map[string]any
		state  string
		notAft *time.Time
		mode   string
		want   string
	}{
		{"expiring", map[string]any{"secretName": "s"}, "read", at(72 * time.Hour), "secret", "cert_expiring"},
		{"expired", map[string]any{"secretName": "s"}, "read", at(-time.Hour), "secret", "cert_expired"},
		{"fine", map[string]any{"secretName": "s"}, "read", at(30 * 24 * time.Hour), "secret", ""},
		{"denied", map[string]any{"secretName": "s"}, "denied", nil, "secret", ""},
		{"resolver", map[string]any{"certResolver": "le"}, "", nil, "resolver", ""},
		{"default", map[string]any{}, "", nil, "default", ""},
		{"none", nil, "", nil, "none", ""},
	}
	for _, c := range cases {
		f := tFixture{cert: map[string]struct {
			state string
			at    *time.Time
		}{"shop/s": {c.state, c.notAft}}}
		rows, _, _ := buildTraefikRoutes(f.input(tRoute("shop", "ui", nil, c.tls, tRule("Host(`ui`)", nil))))
		tls := rows[0].TLS
		if tls.Mode != c.mode || (c.want != "" && !hasProblem(rows[0], c.want)) || (c.want == "" && (hasProblem(rows[0], "cert_expiring") || hasProblem(rows[0], "cert_expired"))) {
			t.Errorf("%s: tls=%+v problems=%v", c.name, tls, rows[0].Problems)
		}
		if c.name == "denied" && (tls.NotAfter != nil || tls.Certificate != "denied") {
			t.Errorf("denied must carry no notAfter: %+v", tls)
		}
		if c.name == "resolver" && tls.Resolver != "le" {
			t.Errorf("resolver = %+v", tls)
		}
	}
}

func TestBuildTraefikRoutesDuplicateHost(t *testing.T) {
	f := tFixture{}
	dup := func(epA, epB []any) []traefikRouteRow {
		rows, _, _ := buildTraefikRoutes(f.input(
			tRoute("shop", "a", epA, nil, tRule("Host(`a`)||Host(`b`)", nil)),
			tRoute("shop", "b", epB, nil, tRule("Host(`b`) || Host(`a`)", nil)),
		))
		return rows
	}
	for _, r := range dup([]any{"websecure"}, []any{"web", "websecure"}) {
		if !hasProblem(r, "duplicate_host") {
			t.Errorf("overlapping entry points: %+v", r)
		}
	}
	for _, r := range dup([]any{"web"}, []any{"websecure"}) {
		if hasProblem(r, "duplicate_host") {
			t.Errorf("disjoint entry points: %+v", r)
		}
	}
	rows, _, _ := buildTraefikRoutes(f.input(tRoute("shop", "a", nil, nil, tRule("Host(`a`)", nil), tRule("Host(`a`)", nil))))
	for _, r := range rows {
		if hasProblem(r, "duplicate_host") {
			t.Errorf("two rules of one route are not duplicates: %+v", r)
		}
	}
}

func TestBuildTraefikRoutesMiddlewareUse(t *testing.T) {
	f := tFixture{}
	rule := func(mw map[string]any) map[string]any {
		return map[string]any{"match": "Host(`x`)", "middlewares": []any{mw}}
	}
	_, uses, _ := buildTraefikRoutes(f.input(
		tRoute("shop", "a", nil, nil, rule(map[string]any{"name": "redirect"})),
		tRoute("shop", "b", nil, nil, rule(map[string]any{"name": "redirect", "namespace": "shop"})),
		tRoute("edge", "c", nil, nil, rule(map[string]any{"name": "redirect", "namespace": "shop"})),
		tRoute("shop", "d", nil, nil, rule(map[string]any{"name": "redirect@file"})),
	))
	if len(uses) != 1 || uses[0].Namespace != "shop" || uses[0].Name != "redirect" || uses[0].UsedBy != 3 {
		t.Fatalf("uses = %+v", uses)
	}
}

func TestBuildTraefikRoutesTruncates(t *testing.T) {
	in := tFixture{}.input(tRoute("shop", "a", nil, nil, tRule("Host(`a`)", nil), tRule("Host(`b`)", nil), tRule("Host(`c`)", nil)))
	in.Limit = 2
	rows, _, truncated := buildTraefikRoutes(in)
	if len(rows) != 2 || !truncated {
		t.Fatalf("rows=%d truncated=%v", len(rows), truncated)
	}
}

// Only selected routes are enriched, while use counts and duplicate hosts
// still come from every route the caller can see.
func TestBuildTraefikRoutesSelectEnrichesOnlySelected(t *testing.T) {
	podLists := 0
	f := tFixture{services: map[string]*corev1.Service{"shop/ui": selService("shop", "ui")}, pods: map[string][]*corev1.Pod{"shop": {readyPod(true)}}}
	in := f.input(
		tRoute("shop", "a", nil, nil, tRule("Host(`a`)", []string{"ui"}, "auth")),
		tRoute("shop", "b", nil, nil, tRule("Host(`a`)", []string{"ui"}, "auth"), tRule("Host(`b`)", []string{"ui"}, "auth")),
		tRoute("shop", "c", nil, nil, tRule("Host(`c`)", []string{"ui"})),
	)
	pods := in.Pods
	in.Pods = func(ns string, sel labels.Selector) ([]*corev1.Pod, error) { podLists++; return pods(ns, sel) }
	in.Select = func(u *unstructured.Unstructured) bool { return u.GetName() == "b" }
	rows, uses, _ := buildTraefikRoutes(in)
	if len(rows) != 2 || rows[0].Ref.Name != "b" || rows[1].Ref.Name != "b" {
		t.Fatalf("rows = %+v", rows)
	}
	if !hasProblem(rows[0], "duplicate_host") {
		t.Errorf("duplicate with an unselected route must be marked: %+v", rows[0])
	}
	if len(uses) != 1 || uses[0].UsedBy != 2 {
		t.Errorf("usedBy counts distinct routes over all: %+v", uses)
	}
	if podLists != 1 {
		t.Errorf("pods listed %d times, want once per backend", podLists)
	}
}

func TestBuildTraefikRoutesCountsGatedOnServicesAndRefusal(t *testing.T) {
	f := tFixture{services: map[string]*corev1.Service{"shop/ui": selService("shop", "ui")}, pods: map[string][]*corev1.Pod{"shop": {readyPod(true)}}}
	route := tRoute("shop", "a", nil, nil, tRule("Host(`a`)", []string{"ui"}))
	f.denyRead = true
	rows, _, _ := buildTraefikRoutes(f.input(route))
	if b := rows[0].Services[0]; b.ReadyPods != nil {
		t.Errorf("no counts without reading Services: %+v", b)
	}
	f.denyRead = false
	f.refs = map[string][]k8s.TraefikRuleRefs{"shop/a": {{Services: []k8s.TraefikRefVerdict{{Kind: "Service", Namespace: "shop", Name: "ui", Refused: true}}}}}
	rows, _, _ = buildTraefikRoutes(f.input(route))
	if b := rows[0].Services[0]; b.ReadyPods != nil {
		t.Errorf("no counts for a backend Traefik refuses: %+v", b)
	}
}
