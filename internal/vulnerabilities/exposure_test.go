package vulnerabilities

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8sruntime "k8s.io/apimachinery/pkg/runtime"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"

	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

func appPod(ns, app string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: app + "-0", Namespace: ns, Labels: map[string]string{"app": app}}}
}

func service(ns, name string, typ corev1.ServiceType, selector map[string]string) *corev1.Service {
	return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Spec: corev1.ServiceSpec{Type: typ, Selector: selector}}
}

func withNodePort(svc *corev1.Service) *corev1.Service {
	svc.Spec.Ports = []corev1.ServicePort{{Port: 80, NodePort: 31000}}
	return svc
}

func withAddress(svc *corev1.Service) *corev1.Service {
	svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{IP: "203.0.113.10"}}
	return svc
}

func ingress(ns, name, backend string, addressed bool) *networkingv1.Ingress {
	ing := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Spec: networkingv1.IngressSpec{
		DefaultBackend: &networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: backend}},
	}}
	if addressed {
		ing.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{{Hostname: "lb.example.test"}}
	}
	return ing
}

func gateway(ns, name string, addressed bool) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1", "kind": "Gateway",
		"metadata": map[string]any{"name": name, "namespace": ns},
	}}
	if addressed {
		u.Object["status"] = map[string]any{"addresses": []any{map[string]any{"type": "IPAddress", "value": "203.0.113.20"}}}
	}
	return u
}

// httpRoute attaches to parentNS/parentName and sends to backends, each
// "namespace/name" or "namespace/name=weight".
func httpRoute(ns, name, parentNS, parentName string, backends ...string) *unstructured.Unstructured {
	refs := []any{}
	for _, b := range backends {
		target, weight, weighted := strings.Cut(b, "=")
		bns, bname, _ := strings.Cut(target, "/")
		ref := map[string]any{"name": bname, "namespace": bns, "port": int64(80)}
		if weighted {
			var w int64
			fmt.Sscan(weight, &w)
			ref["weight"] = w
		}
		refs = append(refs, ref)
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1", "kind": "HTTPRoute",
		"metadata": map[string]any{"name": name, "namespace": ns},
		"spec": map[string]any{
			"parentRefs": []any{map[string]any{"name": parentName, "namespace": parentNS}},
			"rules":      []any{map[string]any{"backendRefs": refs}},
		},
	}}
}

func rejectedBy(route *unstructured.Unstructured, parentNS, parentName string) *unstructured.Unstructured {
	route.Object["status"] = map[string]any{"parents": []any{map[string]any{
		"parentRef":  map[string]any{"name": parentName, "namespace": parentNS},
		"conditions": []any{map[string]any{"type": "Accepted", "status": "False", "reason": "NotAllowedByListeners"}},
	}}}
	return route
}

// Each way in the spec names counts only with evidence that it is a way in:
// an address, node ports, an accepted route. Everything else stays unmarked.
func TestExposureRules(t *testing.T) {
	sel := func(app string) map[string]string { return map[string]string{"app": app} }
	src := routeSources{
		services: []*corev1.Service{
			withAddress(service("shop", "lb", corev1.ServiceTypeLoadBalancer, sel("lb"))),
			withNodePort(service("shop", "lb-pending", corev1.ServiceTypeLoadBalancer, sel("lb-pending"))),
			service("shop", "lb-no-ports", corev1.ServiceTypeLoadBalancer, sel("lb-no-ports")),
			withNodePort(service("shop", "np", corev1.ServiceTypeNodePort, sel("np"))),
			service("shop", "web", corev1.ServiceTypeClusterIP, sel("web")),
			service("shop", "web-noaddr", corev1.ServiceTypeClusterIP, sel("web-noaddr")),
			service("shop", "internal", corev1.ServiceTypeClusterIP, sel("internal")),
			service("shop", "gw", corev1.ServiceTypeClusterIP, sel("gw")),
			service("shop", "gw-noaddr", corev1.ServiceTypeClusterIP, sel("gw-noaddr")),
			service("shop", "canary", corev1.ServiceTypeClusterIP, sel("canary")),
			service("shop", "rejected", corev1.ServiceTypeClusterIP, sel("rejected")),
			withAddress(service("shop", "two-lb", corev1.ServiceTypeLoadBalancer, sel("two"))),
			service("shop", "two", corev1.ServiceTypeClusterIP, sel("two")),
			{ObjectMeta: metav1.ObjectMeta{Name: "external", Namespace: "shop"}, Spec: corev1.ServiceSpec{Type: corev1.ServiceTypeExternalName, Selector: sel("external")}},
			withNodePort(service("shop", "selectorless", corev1.ServiceTypeNodePort, nil)),
		},
		ingresses: []*networkingv1.Ingress{
			ingress("shop", "web", "web", true),
			ingress("shop", "web-noaddr", "web-noaddr", false),
			ingress("shop", "two", "two", true),
		},
		gateways: []*unstructured.Unstructured{gateway("infra", "public", true), gateway("infra", "orphan", false)},
		routes: []*unstructured.Unstructured{
			httpRoute("shop", "gw", "infra", "public", "shop/gw", "shop/canary=0"),
			httpRoute("shop", "gw-noaddr", "infra", "orphan", "shop/gw-noaddr"),
			rejectedBy(httpRoute("shop", "rejected", "infra", "public", "shop/rejected"), "infra", "public"),
		},
	}
	mirrored := httpRoute("shop", "mirrored", "infra", "public", "shop/gw")
	rules := mirrored.Object["spec"].(map[string]any)["rules"].([]any)
	rules[0].(map[string]any)["filters"] = []any{map[string]any{"type": "RequestMirror", "requestMirror": map[string]any{"backendRef": map[string]any{"name": "shadow", "port": int64(80)}}}}
	src.routes = append(src.routes, mirrored)
	off := httpRoute("shop", "mirror-off", "infra", "public", "shop/gw")
	off.Object["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)["filters"] = []any{
		map[string]any{"type": "RequestMirror", "requestMirror": map[string]any{"backendRef": map[string]any{"name": "shadow-off", "port": int64(80)}, "percent": int64(0)}},
	}
	src.routes = append(src.routes, off)
	src.services = append(src.services, service("shop", "shadow-off", corev1.ServiceTypeClusterIP, sel("shadow-off")))
	src.services = append(src.services, service("shop", "shadow", corev1.ServiceTypeClusterIP, sel("shadow")))
	// A stale status entry for a listener the route no longer names doesn't
	// outweigh the current rejection.
	stale := rejectedBy(httpRoute("shop", "moved", "infra", "public", "shop/moved"), "infra", "public")
	stale.Object["spec"].(map[string]any)["parentRefs"] = []any{map[string]any{"name": "public", "namespace": "infra", "sectionName": "https"}}
	parents := stale.Object["status"].(map[string]any)["parents"].([]any)
	parents[0].(map[string]any)["parentRef"].(map[string]any)["sectionName"] = "https"
	stale.Object["status"].(map[string]any)["parents"] = append(parents, map[string]any{
		"parentRef":  map[string]any{"name": "public", "namespace": "infra", "sectionName": "http"},
		"conditions": []any{map[string]any{"type": "Accepted", "status": "True"}},
	})
	src.routes = append(src.routes, stale)
	src.services = append(src.services, service("shop", "moved", corev1.ServiceTypeClusterIP, sel("moved")))
	x := mustIndex(src, nil, nil)
	cases := map[string][]Route{
		"shadow":      {{Via: "HTTPRoute", Namespace: "shop", Name: "mirrored"}},
		"moved":       nil,
		"shadow-off":  nil,
		"lb":          {{Via: "LoadBalancer", Namespace: "shop", Name: "lb"}},
		"lb-pending":  {{Via: "LoadBalancer", Namespace: "shop", Name: "lb-pending"}},
		"lb-no-ports": nil,
		"np":          {{Via: "NodePort", Namespace: "shop", Name: "np"}},
		"web":         {{Via: "Ingress", Namespace: "shop", Name: "web"}},
		"web-noaddr":  nil,
		"internal":    nil,
		"gw":          {{Via: "HTTPRoute", Namespace: "shop", Name: "gw"}, {Via: "HTTPRoute", Namespace: "shop", Name: "mirror-off"}, {Via: "HTTPRoute", Namespace: "shop", Name: "mirrored"}},
		"gw-noaddr":   nil,
		"canary":      nil,
		"rejected":    nil,
		"two":         {{Via: "Ingress", Namespace: "shop", Name: "two"}, {Via: "LoadBalancer", Namespace: "shop", Name: "two-lb"}},
		"external":    nil,
	}
	for app, want := range cases {
		if got := x.mustRoutes(appPod("shop", app)); !slices.Equal(got, want) {
			t.Errorf("%s: routes %v, want %v", app, got, want)
		}
	}
	// A Service selects only in its own namespace.
	if got := x.mustRoutes(appPod("other", "lb")); got != nil {
		t.Errorf("a Pod in another namespace: %v", got)
	}
}

// What the caller may not read is never used, so a route is never named to
// someone who couldn't open it.
func TestExposureSkipsWhatTheCallerCannotRead(t *testing.T) {
	src := routeSources{
		services:  []*corev1.Service{service("shop", "web", corev1.ServiceTypeClusterIP, map[string]string{"app": "web"})},
		ingresses: []*networkingv1.Ingress{ingress("shop", "web", "web", true)},
		gateways:  []*unstructured.Unstructured{gateway("infra", "public", true)},
		routes:    []*unstructured.Unstructured{httpRoute("shop", "web", "infra", "public", "shop/web")},
	}
	noIngress := func(group, kind, ns string) bool { return kind != "Ingress" }
	if got := mustIndex(src, noIngress, nil).mustRoutes(appPod("shop", "web")); !slices.Equal(got, []Route{{Via: "HTTPRoute", Namespace: "shop", Name: "web"}}) {
		t.Fatalf("Ingress denied: %v", got)
	}
	noGateway := func(group, kind, ns string) bool { return kind != "Gateway" }
	if got := mustIndex(src, noGateway, nil).mustRoutes(appPod("shop", "web")); !slices.Equal(got, []Route{{Via: "Ingress", Namespace: "shop", Name: "web"}}) {
		t.Fatalf("the route's Gateway denied: %v", got)
	}
}

var (
	frontend = WorkloadRef{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "frontend"}
	lbRoute  = Route{Via: "LoadBalancer", Namespace: "shop", Name: "frontend"}
)

func exposedTarget(t target, routes ...Route) target {
	t.routes = routes
	return t
}

// frontend (exposed) and worker (not) run the same image; checkout runs
// another image with a Critical, unexposed.
func exposureFixture() ([]*unstructured.Unstructured, []target) {
	items := []*unstructured.Unstructured{
		vulnReport(reportSpec{name: "rs-frontend", ns: "shop", ownerKind: "ReplicaSet", ownerName: "frontend-1", container: "app", uid: "u-f",
			repo: "acme/base", tag: "1", digest: "sha256:base", scanned: "2026-11-01T00:00:00Z",
			vulns: []vuln{{"CVE-HIGH", "HIGH", "openssl", "1.0", "1.1"}}}),
		vulnReport(reportSpec{name: "rs-checkout", ns: "shop", ownerKind: "ReplicaSet", ownerName: "checkout-1", container: "app", uid: "u-c",
			repo: "acme/checkout", tag: "1", digest: "sha256:co", scanned: "2026-11-01T00:00:00Z",
			vulns: []vuln{{"CVE-CRIT", "CRITICAL", "libxml", "2.0", "2.1"}}}),
	}
	targets := []target{
		exposedTarget(running(liveTarget("shop", "ReplicaSet", "frontend-1", "app", "acme/base:1", "u-f", frontend), "sha256:base"), lbRoute),
		running(liveTarget("shop", "ReplicaSet", "worker-1", "app", "acme/base:1", "u-w", worker), "sha256:base"),
		running(liveTarget("shop", "ReplicaSet", "checkout-1", "app", "acme/checkout:1", "u-c", checkout), "sha256:co"),
	}
	return items, targets
}

// A row is exposed only through a workload it covers and names only that
// workload. Severity leads: the unexposed Critical sorts above the exposed
// High, and within High the exposed workload leads.
func TestExposedRowsNameOnlyTheirExposedWorkloads(t *testing.T) {
	items, targets := exposureFixture()
	for _, view := range []View{ViewImage, ViewFix, ViewCVE} {
		resp := mustBuild(t, items, targets, view, Filter{})
		var exposed []ExposedWorkload
		var exposedTotal, firstTotal, rows int
		switch view {
		case ViewImage:
			exposed, exposedTotal, firstTotal, rows = resp.Images[1].Exposed, resp.Images[1].ExposedTotal, resp.Images[0].ExposedTotal, len(resp.Images)
		case ViewFix:
			exposed, exposedTotal, firstTotal, rows = resp.Fixes[1].Exposed, resp.Fixes[1].ExposedTotal, resp.Fixes[0].ExposedTotal, len(resp.Fixes)
		case ViewCVE:
			exposed, exposedTotal, firstTotal, rows = resp.CVEs[1].Exposed, resp.CVEs[1].ExposedTotal, resp.CVEs[0].ExposedTotal, len(resp.CVEs)
			if resp.CVEs[0].ID != "CVE-CRIT" {
				t.Fatalf("cve: the unexposed Critical sorts above the exposed High: %+v", resp.CVEs)
			}
		}
		if rows != 2 || firstTotal != 0 || exposedTotal != 1 || len(exposed) != 1 || exposed[0].WorkloadRef != frontend || !slices.Equal(exposed[0].Routes, []Route{lbRoute}) {
			t.Fatalf("%s: second row's exposed %+v (total %d, first row's %d, rows %d)", view, exposed, exposedTotal, firstTotal, rows)
		}
	}
	workloads := mustBuild(t, items, targets, ViewWorkload, Filter{})
	if got := workloads.Workloads; len(got) != 3 || got[0].Name != "checkout" || got[1].Name != "frontend" || !slices.Equal(got[1].Routes, []Route{lbRoute}) || got[2].Routes != nil {
		t.Fatalf("workload: %+v", workloads.Workloads)
	}
	if workloads.Exposure == nil {
		t.Fatal("every answer says it checks exposure")
	}
}

// Without exposure, most severe comes before most workloads: a High in one
// workload above a Medium in two.
func TestSeverityBeforeWorkloadCount(t *testing.T) {
	items := []*unstructured.Unstructured{
		vulnReport(reportSpec{name: "r-a", ns: "shop", ownerKind: "ReplicaSet", ownerName: "a-1", container: "app", uid: "u-a",
			repo: "acme/a", tag: "1", digest: "sha256:a", scanned: "2026-11-01T00:00:00Z", vulns: []vuln{{"CVE-MED", "MEDIUM", "zlib", "1", "2"}}}),
		vulnReport(reportSpec{name: "r-b", ns: "shop", ownerKind: "ReplicaSet", ownerName: "b-1", container: "app", uid: "u-b",
			repo: "acme/b", tag: "1", digest: "sha256:b", scanned: "2026-11-01T00:00:00Z", vulns: []vuln{{"CVE-HI", "HIGH", "curl", "1", "2"}}}),
	}
	targets := []target{
		running(liveTarget("shop", "ReplicaSet", "a-1", "app", "acme/a:1", "u-a", checkout), "sha256:a"),
		running(liveTarget("shop", "ReplicaSet", "a-2", "app", "acme/a:1", "u-a2", worker), "sha256:a"),
		running(liveTarget("shop", "ReplicaSet", "b-1", "app", "acme/b:1", "u-b", db), "sha256:b"),
	}
	if got := mustBuild(t, items, targets, ViewCVE, Filter{}).CVEs[0].ID; got != "CVE-HI" {
		t.Fatalf("cve: first %s", got)
	}
	if got := mustBuild(t, items, targets, ViewFix, Filter{}).Fixes[0].Package; got != "curl" {
		t.Fatalf("fix: first %s", got)
	}
	if got := mustBuild(t, items, targets, ViewImage, Filter{}).Images[0].Image; got != "acme/b:1" {
		t.Fatalf("image: first %s", got)
	}
	// Most severe of what the filters keep: with a Critical in a's image too,
	// a Medium and High filter leaves a's Medium, so b's High still leads.
	items[0] = vulnReport(reportSpec{name: "r-a", ns: "shop", ownerKind: "ReplicaSet", ownerName: "a-1", container: "app", uid: "u-a",
		repo: "acme/a", tag: "1", digest: "sha256:a", scanned: "2026-11-01T00:00:00Z", vulns: []vuln{{"CVE-MED", "MEDIUM", "zlib", "1", "2"}, {"CVE-CRIT", "CRITICAL", "openssl", "1", "2"}}})
	filter := Filter{Severities: []Severity{SeverityMedium, SeverityHigh}}
	if got := mustBuild(t, items, targets, ViewImage, filter).Images; len(got) != 2 || got[0].Image != "acme/b:1" {
		t.Fatalf("image, filtered: %+v", got)
	}
	if got := mustBuild(t, items, targets, ViewWorkload, filter).Workloads; len(got) != 3 || got[0].Name != db.Name {
		t.Fatalf("workload, filtered: %+v", got)
	}
}

// The Exposed filter keeps the images an exposed workload runs, ANDs with
// the others, and still lists every workload that runs them.
func TestExposedFilter(t *testing.T) {
	items, targets := exposureFixture()
	images := mustBuild(t, items, targets, ViewImage, Filter{ExposedOnly: true}).Images
	if len(images) != 1 || images[0].Image != "acme/base:1" || images[0].WorkloadsTotal != 2 {
		t.Fatalf("image: %+v", images)
	}
	if got := mustBuild(t, items, targets, ViewCVE, Filter{ExposedOnly: true, Severities: []Severity{SeverityCritical}}).CVEs; len(got) != 0 {
		t.Fatalf("exposed AND critical: %+v", got)
	}
	if got := mustBuild(t, items, targets, ViewWorkload, Filter{ExposedOnly: true}).Workloads; len(got) != 1 || got[0].Name != "frontend" {
		t.Fatalf("workload: %+v", got)
	}
	unscanned := append(slices.Clone(targets), exposedTarget(liveTarget("shop", "ReplicaSet", "frontend-1", "sidecar", "acme/proxy:2", "u-f", frontend), lbRoute),
		liveTarget("shop", "ReplicaSet", "worker-1", "sidecar", "acme/cache:3", "u-w", worker))
	if got := mustBuild(t, items, unscanned, ViewImage, Filter{ExposedOnly: true}).Unscanned; len(got) != 1 || got[0].Image != "acme/proxy:2" || got[0].ExposedTotal != 1 {
		t.Fatalf("not-scanned list: %+v", got)
	}
}

// A capped list keeps the exposed workload and the image it runs, however
// many wider ones there are.
func TestCapsKeepTheExposedEvidence(t *testing.T) {
	var items []*unstructured.Unstructured
	var targets []target
	for i := range MaxAffectedPerCVE + 5 {
		digest := fmt.Sprintf("sha256:%03d", i)
		owner := fmt.Sprintf("rs-%03d", i)
		items = append(items, vulnReport(reportSpec{name: "r-" + owner, ns: "shop", ownerKind: "ReplicaSet", ownerName: owner, container: "app", uid: uidOf(owner),
			repo: "acme/img", tag: fmt.Sprint(i), digest: digest, scanned: "2026-11-01T00:00:00Z", vulns: []vuln{{"CVE-1", "HIGH", "openssl", "1", "2"}}}))
		// Every image but the last runs in many workloads; the last runs in
		// one, the exposed one.
		n := MaxWorkloadsPerAffected + 3
		if i == MaxAffectedPerCVE+4 {
			n = 1
		}
		for k := range n {
			w := WorkloadRef{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: fmt.Sprintf("w-%03d-%02d", i, k)}
			tg := running(liveTarget("shop", "ReplicaSet", owner, "app", fmt.Sprintf("acme/img:%d", i), uidOf(owner), w), digest)
			if n == 1 {
				tg = exposedTarget(tg, lbRoute)
			}
			targets = append(targets, tg)
		}
	}
	cve := mustBuild(t, items, targets, ViewCVE, Filter{}).CVEs[0]
	if len(cve.Affected) != MaxAffectedPerCVE || cve.Affected[0].Image != fmt.Sprintf("acme/img:%d", MaxAffectedPerCVE+4) || cve.ExposedTotal != 1 {
		t.Fatalf("cve keeps the exposed image first: %d images, first %s, exposed %d", len(cve.Affected), cve.Affected[0].Image, cve.ExposedTotal)
	}
	fix := mustBuild(t, items, targets, ViewFix, Filter{}).Fixes[0]
	if fix.Affected[0].Image != fmt.Sprintf("acme/img:%d", MaxAffectedPerCVE+4) {
		t.Fatalf("fix keeps the exposed image first: %s", fix.Affected[0].Image)
	}

	// Within an image, the exposed workload is listed before the cap.
	var many []target
	for k := range MaxWorkloadsPerAffected + 5 {
		w := WorkloadRef{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: fmt.Sprintf("a-%02d", k)}
		tg := running(liveTarget("shop", "ReplicaSet", "base-1", "app", "acme/base:1", "u-base", w), "sha256:base")
		if k == MaxWorkloadsPerAffected+4 {
			tg = exposedTarget(tg, lbRoute)
		}
		many = append(many, tg)
	}
	base := vulnReport(reportSpec{name: "r-base", ns: "shop", ownerKind: "ReplicaSet", ownerName: "base-1", container: "app", uid: "u-base",
		repo: "acme/base", tag: "1", digest: "sha256:base", scanned: "2026-11-01T00:00:00Z", vulns: []vuln{{"CVE-1", "HIGH", "openssl", "1", "2"}}})
	listed := mustBuild(t, []*unstructured.Unstructured{base}, many, ViewCVE, Filter{}).CVEs[0].Affected[0].Workloads
	if len(listed) != MaxWorkloadsPerAffected || listed[0].Name != fmt.Sprintf("a-%02d", MaxWorkloadsPerAffected+4) {
		t.Fatalf("exposed workload listed first: %+v", listed)
	}
}

func uidOf(owner string) k8stypes.UID { return k8stypes.UID("uid-" + owner) }

// On a cluster past the findings budget, the images exposed workloads run
// are read first.
func TestFindingsBudgetReadsExposedImagesFirst(t *testing.T) {
	big := make([]vuln, 0, MaxFindings)
	for i := range MaxFindings {
		big = append(big, vuln{fmt.Sprintf("CVE-%06d", i), "LOW", "pkg", "1", ""})
	}
	items := []*unstructured.Unstructured{
		vulnReport(reportSpec{name: "a-big", ns: "shop", ownerKind: "ReplicaSet", ownerName: "aaa-1", container: "app", uid: "u-a",
			repo: "acme/aaa", tag: "1", digest: "sha256:aaa", scanned: "2026-11-01T00:00:00Z", vulns: big}),
		vulnReport(reportSpec{name: "z-small", ns: "shop", ownerKind: "ReplicaSet", ownerName: "zzz-1", container: "app", uid: "u-z",
			repo: "acme/zzz", tag: "1", digest: "sha256:zzz", scanned: "2026-11-01T00:00:00Z", vulns: []vuln{{"CVE-X", "HIGH", "openssl", "1", "2"}}}),
	}
	targets := []target{
		running(liveTarget("shop", "ReplicaSet", "aaa-1", "app", "acme/aaa:1", "u-a", worker), "sha256:aaa"),
		exposedTarget(running(liveTarget("shop", "ReplicaSet", "zzz-1", "app", "acme/zzz:1", "u-z", frontend), "sha256:zzz"), lbRoute),
	}
	resp := mustBuild(t, items, targets, ViewImage, Filter{})
	for _, img := range resp.Images {
		if img.Image == "acme/zzz:1" && img.FindingsUnread {
			t.Fatal("the exposed image was left unread")
		}
	}
	if !resp.Coverage.Truncated {
		t.Fatal("the budget should still run out on the big image")
	}
}

// The CSV names exposed workloads with their routes in its last column, says
// "unknown" where Radar couldn't read routes, and under the Exposed filter
// writes those exposure gaps as rows.
func TestExportExposedWorkloads(t *testing.T) {
	items, targets := exposureFixture()
	resp := mustExport(t, items, targets, ViewFix, Filter{})
	if cols := ExportColumns[ViewFix]; cols[len(cols)-1] != "exposed_workloads" {
		t.Fatalf("header: %v", cols)
	}
	// libxml's Critical sorts first, openssl's exposed High second.
	rows := exportRows(t, resp, Filter{})
	if got := rows[1]["exposed_workloads"]; rows[1]["package"] != "openssl" || got != "Deployment shop/frontend (LoadBalancer shop/frontend)" {
		t.Fatalf("exposed cell: %q", got)
	}
	if got := rows[0]["exposed_workloads"]; got != "" {
		t.Fatalf("no route seen: %q", got)
	}

	resp.Exposure = &Exposure{Gaps: []resourceapi.Gap{{Code: readreason.KindSyncPending, Kinds: ingressKind, Namespaces: resourceapi.Names([]string{"shop"}, 64)}}}
	rows = exportRows(t, resp, Filter{})
	if got := rows[0]["exposed_workloads"]; got != "unknown" {
		t.Fatalf("routes unread in shop: %q", got)
	}
	if got := rows[1]["exposed_workloads"]; !strings.HasPrefix(got, "Deployment shop/frontend") {
		t.Fatalf("a route seen stays named: %q", got)
	}
	filtered := exportRows(t, resp, Filter{ExposedOnly: true})
	if last := filtered[len(filtered)-1]; last["status"] != StatusLoading || last["running_in"] != "shop" {
		t.Fatalf("exposure gap row under the Exposed filter: %v", last)
	}
	if got := exportRows(t, resp, Filter{}); got[len(got)-1]["status"] == StatusLoading {
		t.Fatal("without the filter the rows are all there, so no gap row")
	}

	workloads := exportRows(t, mustExport(t, items, targets, ViewWorkload, Filter{}), Filter{})
	if workloads[1]["workload"] != "frontend" || workloads[1]["exposed_workloads"] != "Deployment shop/frontend (LoadBalancer shop/frontend)" {
		t.Fatalf("workload view: %v", workloads[1])
	}
}

// Read from the caches: a kind Radar doesn't collect is an exposure gap,
// never a "no route".
func TestReadExposureGaps(t *testing.T) {
	lb := withAddress(service("shop", "web", corev1.ServiceTypeLoadBalancer, map[string]string{"app": "web"}))
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:        fake.NewClientset(lb),
		ResourceTypes: map[string]bool{k8score.Services: true},
		DeferredTypes: map[string]bool{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Stop)
	cache := &k8s.ResourceCache{ResourceCache: core}
	for deadline := time.Now().Add(5 * time.Second); !cache.IsKindReady("services"); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("cache never synced")
		}
	}
	src, gaps := readExposure(context.Background(), cache, nil, nil, nil)
	if got := mustIndex(src, nil, nil).mustRoutes(appPod("shop", "web")); len(got) != 1 || got[0].Via != "LoadBalancer" {
		t.Fatalf("routes: %v", got)
	}
	if len(gaps) != 1 || gaps[0].Kinds.Names[0].Kind != "Ingress" || gaps[0].Code != readreason.RadarKindNotCollected {
		t.Fatalf("Ingresses not collected: %+v", gaps)
	}
	if _, gaps := readExposure(context.Background(), nil, nil, nil, nil); len(gaps) != 1 || gaps[0].Kinds.Names[0].Kind != "Service" {
		t.Fatalf("no Services: %+v", gaps)
	}
}

// A restricted caller's exposure gaps follow their access alone: the same
// whether or not anything they can't see reaches their workloads, so the
// answer reveals nothing about what's hidden.
func TestRestrictedCallerGapsFollowAccessNotTopology(t *testing.T) {
	services := []*corev1.Service{service("shop", "web", corev1.ServiceTypeClusterIP, map[string]string{"app": "web"}),
		withNodePort(service("shop", "admin", corev1.ServiceTypeNodePort, map[string]string{"app": "admin"}))}
	answer := func(src routeSources, scope []string, canRead func(group, kind, ns string) bool) ([]Route, []Route, []resourceapi.Gap) {
		src.gatewayKinds = []string{"Gateway", "HTTPRoute"}
		x := mustIndex(src, routeVisibility(scope, canRead), unknownRouteKinds(scope, canRead, src.gatewayKinds))
		return x.mustRoutes(appPod("shop", "web")), x.mustRoutes(appPod("shop", "admin")), x.gaps()
	}
	bare := routeSources{services: services}
	withHidden := routeSources{services: services,
		gateways: []*unstructured.Unstructured{gateway("infra", "public", true)},
		routes:   []*unstructured.Unstructured{httpRoute("shop", "web", "infra", "public", "shop/web")},
	}
	// Namespace-restricted (shop only): Gateway API exposure is unknown for
	// shop's workloads either way, and the NodePort it can see still marks.
	webA, adminA, gapsA := answer(bare, []string{"shop"}, nil)
	webB, adminB, gapsB := answer(withHidden, []string{"shop"}, nil)
	if webA != nil || webB != nil || !reflect.DeepEqual(adminA, adminB) || len(adminA) != 1 || !reflect.DeepEqual(gapsA, gapsB) {
		t.Fatalf("hidden routes changed the answer: %v %v %+v / %v %v %+v", webA, adminA, gapsA, webB, adminB, gapsB)
	}
	if len(gapsA) != 2 || gapsA[0].Code != readreason.UserForbidden || !slices.Equal(gapsA[0].Namespaces.Names, []string{"shop"}) {
		t.Fatalf("gaps: %+v", gapsA)
	}
	if !exposureUnknownIn(&Exposure{Gaps: gapsA}, "shop") {
		t.Fatal("shop's workloads read as unknown")
	}

	// A caller who may read Gateway API kinds cluster-wide but not Services
	// in shop: only Services are unknown there.
	noServices := func(group, kind, ns string) bool { return kind != "Service" }
	_, _, gaps := answer(withHidden, nil, noServices)
	if len(gaps) != 1 || gaps[0].Kinds.Names[0].Kind != "Service" {
		t.Fatalf("Services unreadable: %+v", gaps)
	}

	// Full access: no gaps, and the route shows.
	web, _, gaps := answer(withHidden, nil, nil)
	if len(web) != 1 || len(gaps) != 0 {
		t.Fatalf("full access: %v %+v", web, gaps)
	}
}

func referenceGrant(ns, fromNS, toName string) *unstructured.Unstructured {
	to := map[string]any{"group": "", "kind": "Service"}
	if toName != "" {
		to["name"] = toName
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1beta1", "kind": "ReferenceGrant",
		"metadata": map[string]any{"name": "allow", "namespace": ns},
		"spec": map[string]any{
			"from": []any{map[string]any{"group": "gateway.networking.k8s.io", "kind": "HTTPRoute", "namespace": fromNS}},
			"to":   []any{to},
		},
	}}
}

// When the controller refuses a cross-namespace backend for want of a
// ReferenceGrant, each cross-namespace backend is checked against the grants:
// a permitted one keeps its mark, a forbidden one gets none, and where the
// grants can't be read the backend's exposure is unknown. Own-namespace
// backends count either way.
func TestRefNotPermittedChecksEachBackend(t *testing.T) {
	route := httpRoute("shop", "split", "infra", "public", "shop/web", "billing/api", "ledger/books")
	route.Object["status"] = map[string]any{"parents": []any{map[string]any{
		"parentRef": map[string]any{"name": "public", "namespace": "infra"},
		"conditions": []any{
			map[string]any{"type": "Accepted", "status": "True"},
			map[string]any{"type": "ResolvedRefs", "status": "False", "reason": "RefNotPermitted"},
		},
	}}}
	src := routeSources{
		services: []*corev1.Service{
			service("shop", "web", corev1.ServiceTypeClusterIP, map[string]string{"app": "web"}),
			service("billing", "api", corev1.ServiceTypeClusterIP, map[string]string{"app": "api"}),
			service("ledger", "books", corev1.ServiceTypeClusterIP, map[string]string{"app": "books"}),
		},
		gateways: []*unstructured.Unstructured{gateway("infra", "public", true)},
		routes:   []*unstructured.Unstructured{route},
		// billing allows shop's HTTPRoutes to its api Service; ledger allows
		// nothing.
		grants: []*unstructured.Unstructured{referenceGrant("billing", "shop", "api"), referenceGrant("ledger", "other", "")},
	}
	x := mustIndex(src, nil, nil)
	if x.mustRoutes(appPod("billing", "api")) == nil {
		t.Fatal("permitted backend lost its mark")
	}
	if got := x.mustRoutes(appPod("ledger", "books")); got != nil {
		t.Fatalf("forbidden backend marked: %v", got)
	}
	if x.mustRoutes(appPod("shop", "web")) == nil {
		t.Fatal("own-namespace backend lost its mark")
	}
	if gaps := x.gaps(); len(gaps) != 0 {
		t.Fatalf("grants read: no gap, got %+v", gaps)
	}

	// Grants unreadable: the refused route's cross-namespace backends are
	// unknown, not unmarked.
	src.grants, src.grantsUnread = nil, readreason.RadarForbidden
	x = mustIndex(src, nil, nil)
	if x.mustRoutes(appPod("billing", "api")) != nil || x.mustRoutes(appPod("shop", "web")) == nil {
		t.Fatal("unreadable grants: cross-namespace unmarked, own namespace marked")
	}
	gaps := x.gaps()
	if len(gaps) != 1 || gaps[0].Kinds.Names[0].Kind != "ReferenceGrant" || !slices.Equal(gaps[0].Namespaces.Names, []string{"billing", "ledger"}) {
		t.Fatalf("gaps: %+v", gaps)
	}

	// A caller who may not read ReferenceGrants in a backend's namespace
	// gets the same answer whatever the grants there say: that backend is
	// unknown.
	src.grantsUnread = ""
	noGrants := func(group, kind, ns string) bool { return kind != "ReferenceGrant" }
	restricted := func(grants ...*unstructured.Unstructured) ([]Route, []Route, []resourceapi.Gap) {
		src.grants = grants
		x := mustIndex(src, noGrants, nil)
		return x.mustRoutes(appPod("billing", "api")), x.mustRoutes(appPod("ledger", "books")), x.gaps()
	}
	apiA, booksA, gapsA := restricted()
	apiB, booksB, gapsB := restricted(referenceGrant("billing", "shop", "api"), referenceGrant("ledger", "shop", ""))
	if apiA != nil || apiB != nil || booksA != nil || booksB != nil || !reflect.DeepEqual(gapsA, gapsB) {
		t.Fatalf("grants changed a restricted answer: %v %v %+v / %v %v %+v", apiA, booksA, gapsA, apiB, booksB, gapsB)
	}
	if len(gapsA) != 1 || gapsA[0].Code != readreason.UserForbidden || !slices.Equal(gapsA[0].Namespaces.Names, []string{"billing", "ledger"}) {
		t.Fatalf("restricted gaps: %+v", gapsA)
	}

	// Without the controller's refusal, grants aren't consulted.
	delete(route.Object, "status")
	x = mustIndex(src, nil, nil)
	if x.mustRoutes(appPod("ledger", "books")) == nil || len(x.gaps()) != 0 {
		t.Fatal("no refusal: every backend counts, no gap")
	}
}

// Among exposed rows, the one touching more exposed workloads ranks first at
// the same severity.
func TestMoreExposedWorkloadsRankFirst(t *testing.T) {
	vulns := []vuln{{"CVE-1", "HIGH", "openssl", "1", "2"}}
	items := []*unstructured.Unstructured{
		vulnReport(reportSpec{name: "r-one", ns: "shop", ownerKind: "ReplicaSet", ownerName: "one-1", container: "app", uid: "u-one",
			repo: "acme/one", tag: "1", digest: "sha256:one", scanned: "2026-11-01T00:00:00Z", vulns: vulns}),
		vulnReport(reportSpec{name: "r-three", ns: "shop", ownerKind: "ReplicaSet", ownerName: "three-1", container: "app", uid: "u-three",
			repo: "acme/three", tag: "1", digest: "sha256:three", scanned: "2026-11-01T00:00:00Z", vulns: []vuln{{"CVE-2", "HIGH", "curl", "1", "2"}}}),
	}
	w := func(name string) WorkloadRef {
		return WorkloadRef{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: name}
	}
	// acme/one: one exposed workload and three unexposed (more workloads in
	// all); acme/three: three exposed workloads.
	targets := []target{
		exposedTarget(running(liveTarget("shop", "ReplicaSet", "one-1", "app", "acme/one:1", "u-one", w("a")), "sha256:one"), lbRoute),
		running(liveTarget("shop", "ReplicaSet", "one-2", "app", "acme/one:1", "u-one2", w("b")), "sha256:one"),
		running(liveTarget("shop", "ReplicaSet", "one-3", "app", "acme/one:1", "u-one3", w("c")), "sha256:one"),
		running(liveTarget("shop", "ReplicaSet", "one-4", "app", "acme/one:1", "u-one4", w("d")), "sha256:one"),
	}
	for _, name := range []string{"x", "y", "z"} {
		targets = append(targets, exposedTarget(running(liveTarget("shop", "ReplicaSet", "three-"+name, "app", "acme/three:1", k8stypes.UID("u-"+name), w(name)), "sha256:three"), lbRoute))
	}
	if got := mustBuild(t, items, targets, ViewImage, Filter{}).Images; got[0].Image != "acme/three:1" || got[0].ExposedTotal != 3 || got[1].ExposedTotal != 1 {
		t.Fatalf("image: %+v", got)
	}
	if got := mustBuild(t, items, targets, ViewCVE, Filter{}).CVEs; got[0].ID != "CVE-2" {
		t.Fatalf("cve: %+v", got)
	}
	if got := mustBuild(t, items, targets, ViewFix, Filter{}).Fixes; got[0].Package != "curl" {
		t.Fatalf("fix: %+v", got)
	}
}

// An export charges the exposed workloads its rows keep against the same
// reference budget as their workload lists: many distinct image sets full of
// exposed workloads stop the export early, the same fixture without routes
// fits, and a normal export is unaffected.
func TestExportChargesExposedWorkloads(t *testing.T) {
	fixture := func(exposed bool) ([]*unstructured.Unstructured, []target) {
		var items []*unstructured.Unstructured
		var targets []target
		// CVE-i (in package libi) is in images i and i+1, so every CVE and
		// fix row spans its own pair of images: 99 distinct sets, each with
		// 100 exposed workloads.
		for i := range 100 {
			img := fmt.Sprintf("acme/app%02d", i)
			var vulns []vuln
			for _, c := range []int{i - 1, i} {
				if c >= 0 {
					vulns = append(vulns, vuln{fmt.Sprintf("CVE-%03d", c), "HIGH", fmt.Sprintf("lib%03d", c), "1", "2"})
				}
			}
			items = append(items, vulnReport(reportSpec{name: img, ns: "shop", ownerKind: "ReplicaSet", ownerName: img + "-0", container: "app",
				repo: img, tag: "1", digest: "sha256:" + img, scanned: "2026-11-10T00:00:00Z", vulns: vulns}))
			for k := range 50 {
				ref := WorkloadRef{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: fmt.Sprintf("w%02d-%02d", i, k)}
				tg := running(liveTarget("shop", "ReplicaSet", img+"-0", "app", img+":1", "", ref), "sha256:"+img)
				if exposed {
					tg = exposedTarget(tg, Route{Via: "LoadBalancer", Namespace: "shop", Name: ref.Name})
				}
				targets = append(targets, tg)
			}
		}
		return items, targets
	}
	items, targets := fixture(true)
	alloc := func(view View) (uint64, error) {
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		_, err := build(context.Background(), items, targets, view, Filter{}, true)
		runtime.ReadMemStats(&after)
		return after.TotalAlloc - before.TotalAlloc, err
	}
	full, err := build(context.Background(), items, targets, ViewCVE, Filter{}, true)
	if err != nil || full.CVEs[0].ExposedTotal != 100 {
		t.Fatalf("within the default budget: %v", err)
	}
	fullCost, _ := alloc(ViewCVE)
	saved := maxExportRefs
	t.Cleanup(func() { maxExportRefs = saved })
	// Workload lists alone: 100 rows of 2 images x 50 workloads.
	maxExportRefs = 15_000
	plainItems, plainTargets := fixture(false)
	for _, view := range []View{ViewCVE, ViewFix} {
		if _, err := build(context.Background(), plainItems, plainTargets, view, Filter{}, true); err != nil {
			t.Fatalf("%s without routes fits: %v", view, err)
		}
		used, err := alloc(view)
		if !errors.Is(err, ErrExportTooLarge) {
			t.Fatalf("%s with every workload exposed: %v", view, err)
		}
		if view == ViewCVE && used >= fullCost {
			t.Fatalf("stopping cost %d, a full build %d", used, fullCost)
		}
	}
	// A list read isn't charged.
	if _, err := build(context.Background(), items, targets, ViewCVE, Filter{}, false); err != nil {
		t.Fatalf("list read: %v", err)
	}
}

// The exposed_workloads cell is built within what the file has left and
// stops as soon as it would pass it, or once the request is gone: many
// workloads sharing one large route list cost no more than the cap.
func TestExposureCellStopsEarly(t *testing.T) {
	routes := make([]Route, 200)
	for i := range routes {
		routes[i] = Route{Via: "HTTPRoute", Namespace: "edge", Name: fmt.Sprintf("%s-%03d", strings.Repeat("r", 60), i)}
	}
	exposed := make([]ExposedWorkload, 20_000)
	for i := range exposed {
		exposed[i] = ExposedWorkload{WorkloadRef: WorkloadRef{Kind: "Deployment", Namespace: "shop", Name: fmt.Sprintf("web-%05d", i)}, Routes: routes}
	}
	// Rendered whole this cell would be about 20,000 x 200 x 80 bytes, 320 MB.
	const left = 1 << 20
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	cell, err := exposureCell(context.Background(), exposed, nil, &Exposure{}, left)
	runtime.ReadMemStats(&after)
	if !errors.Is(err, ErrExportTooLarge) || cell != "" {
		t.Fatalf("past the budget: %q %v", cell[:min(len(cell), 40)], err)
	}
	// Refused before any of it is built.
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 64<<10 {
		t.Fatalf("allocated %d bytes for a %d-byte budget", allocated, left)
	}

	// A small list fits and reads as the CSV writes it.
	cell, err = exposureCell(context.Background(), exposed[:1], nil, &Exposure{}, left)
	if err != nil || !strings.HasPrefix(cell, "Deployment shop/web-00000 (HTTPRoute edge/") || !strings.HasSuffix(cell, "-199)") {
		t.Fatalf("one workload: %q %v", cell[:min(len(cell), 60)], err)
	}

	// A cancelled request stops construction.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := exposureCell(ctx, exposed, nil, &Exposure{}, 1<<40); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}

	// Through WriteCSV: the export fails with the size error, not a file.
	items, targets := exposureFixture()
	resp := mustExport(t, items, targets, ViewFix, Filter{})
	resp.Fixes[0].Exposed = exposed
	saved := maxExportBytes
	maxExportBytes = left
	t.Cleanup(func() { maxExportBytes = saved })
	runtime.GC()
	runtime.ReadMemStats(&before)
	_, err = WriteCSV(context.Background(), resp, Filter{}, "prod")
	runtime.ReadMemStats(&after)
	if !errors.Is(err, ErrExportTooLarge) {
		t.Fatalf("WriteCSV: %v", err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 8*left {
		t.Fatalf("WriteCSV allocated %d bytes for a %d-byte cap", allocated, left)
	}
}

// Merging replicas' routes is linear in what they bring: 1,000 replicas
// each with its own copy of 2,000 routes, and 1,000 sharing one list,
// finish quickly; a cancelled request stops the merge.
func TestRouteMergingScales(t *testing.T) {
	shared := make([]Route, 2000)
	for i := range shared {
		shared[i] = Route{Via: "HTTPRoute", Namespace: "edge", Name: fmt.Sprintf("r-%04d", i)}
	}
	w := WorkloadRef{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "web"}
	var targets []target
	for i := range 1000 {
		tg := liveTarget("shop", "ReplicaSet", fmt.Sprintf("web-%04d", i), "app", "acme/web:1", "", w)
		routes := shared
		if i%2 == 0 {
			routes = slices.Clone(shared) // a copy: its routes are compared, once each
		}
		targets = append(targets, exposedTarget(tg, routes...))
	}
	start := time.Now()
	exposure, err := exposureOf(context.Background(), targets)
	if err != nil || len(exposure[w]) != len(shared) {
		t.Fatalf("routes %d, %v", len(exposure[w]), err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("merging took %s", took)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := exposureOf(ctx, targets); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
}

// Every replica of one workload shares one route list; merging them keeps
// it once, and a cancelled request stops the Pod walk.
func TestLiveTargetsScalesWithReplicasAndStops(t *testing.T) {
	routes := make([]Route, 2000)
	for i := range routes {
		routes[i] = Route{Via: "HTTPRoute", Namespace: "edge", Name: fmt.Sprintf("r-%04d", i)}
	}
	owners := owned("apps/v1", "ReplicaSet", "web-1")
	var objs []k8sruntime.Object
	for i := range 1000 {
		p := pod(fmt.Sprintf("web-1-%04d", i), corev1.PodRunning, owners, "acme/web:1")
		p.Labels = map[string]string{"app": "web"}
		objs = append(objs, p)
	}
	x := mustIndex(routeSources{services: []*corev1.Service{withNodePort(service("shop", "web", corev1.ServiceTypeNodePort, map[string]string{"app": "web"}))}}, nil, nil)
	// The NodePort plus 2,000 more routes on the same Service.
	for _, s := range x.byNamespace["shop"] {
		s.routes = append(s.routes, routes...)
	}
	cache := syncedCache(t, objs...)
	start := time.Now()
	live, ok, err := liveTargets(context.Background(), cache, []string{"shop"}, nil, nil, x)
	if err != nil || !ok || len(live.targets) != 1 || len(live.targets[0].routes) != len(routes)+1 {
		t.Fatalf("targets %d, %v %v", len(live.targets), ok, err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("1,000 replicas took %s", took)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := liveTargets(ctx, cache, []string{"shop"}, nil, nil, x); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled: %v", err)
	}
}

// mustIndex is indexExposure without a request to cancel.
func mustIndex(src routeSources, may func(group, kind, namespace string) bool, unknown func(namespace string) []resourceapi.KindRef) exposureIndex {
	x, err := indexExposure(context.Background(), src, may, unknown)
	if err != nil {
		panic(err)
	}
	return x
}

// mustRoutes is routesFor without a request to cancel.
func (x exposureIndex) mustRoutes(pod *corev1.Pod) []Route {
	routes, err := x.routesFor(context.Background(), pod)
	if err != nil {
		panic(err)
	}
	return routes
}

// A StatefulSet's Pods each carry a unique label; Pods selected by the same
// Services still share one route list, and building it is linear: 1,000 such
// Pods behind 2,000 routes finish quickly, and a cancelled request stops
// both the indexing and the matching.
func TestExposureIndexScalesAndStops(t *testing.T) {
	src := routeSources{
		services: []*corev1.Service{service("shop", "db", corev1.ServiceTypeClusterIP, map[string]string{"app": "db"})},
		gateways: []*unstructured.Unstructured{gateway("infra", "public", true)},
	}
	for i := range 2000 {
		src.routes = append(src.routes, httpRoute("shop", fmt.Sprintf("db-%04d", i), "infra", "public", "shop/db"))
	}
	pods := make([]*corev1.Pod, 1000)
	for i := range pods {
		pods[i] = appPod("shop", "db")
		pods[i].Name = fmt.Sprintf("db-%d", i)
		pods[i].Labels["statefulset.kubernetes.io/pod-name"] = pods[i].Name
	}
	start := time.Now()
	x, err := indexExposure(context.Background(), src, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	var first []Route
	for i, p := range pods {
		routes, err := x.routesFor(context.Background(), p)
		if err != nil || len(routes) != 2000 {
			t.Fatalf("pod %d: %d routes, %v", i, len(routes), err)
		}
		if i == 0 {
			first = routes
		} else if &routes[0] != &first[0] {
			t.Fatal("Pods selected by the same Service don't share one list")
		}
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Fatalf("took %s", took)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := indexExposure(ctx, src, nil, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("indexing, cancelled: %v", err)
	}
	if _, err := x.routesFor(ctx, pods[0]); !errors.Is(err, context.Canceled) {
		t.Fatalf("matching, cancelled: %v", err)
	}
}

// A list read names at most MaxRoutesPerWorkload routes per exposed
// workload, with the total, since every row the workload is in repeats
// them; an export keeps every route.
func TestListReadsCapRoutesPerWorkload(t *testing.T) {
	items, targets := exposureFixture()
	many := make([]Route, 200)
	for i := range many {
		many[i] = Route{Via: "HTTPRoute", Namespace: "edge", Name: fmt.Sprintf("r-%03d", i)}
	}
	targets[0] = exposedTarget(targets[0], many...)
	// The exposed High is the second row, below checkout's Critical.
	cve := mustBuild(t, items, targets, ViewCVE, Filter{}).CVEs[1]
	if len(cve.Exposed[0].Routes) != MaxRoutesPerWorkload || cve.Exposed[0].RoutesTotal != 200 {
		t.Fatalf("list read: %d routes of %d", len(cve.Exposed[0].Routes), cve.Exposed[0].RoutesTotal)
	}
	wl := mustBuild(t, items, targets, ViewWorkload, Filter{}).Workloads[1]
	if len(wl.Routes) != MaxRoutesPerWorkload || wl.RoutesTotal != 200 {
		t.Fatalf("workload row: %d routes of %d", len(wl.Routes), wl.RoutesTotal)
	}
	export := mustExport(t, items, targets, ViewCVE, Filter{})
	if len(export.CVEs[1].Exposed[0].Routes) != 200 {
		t.Fatalf("export: %d routes", len(export.CVEs[1].Exposed[0].Routes))
	}
	if cell := exportRows(t, export, Filter{})[1]["exposed_workloads"]; strings.Count(cell, "HTTPRoute edge/") != 200 {
		t.Fatalf("CSV cell names %d routes", strings.Count(cell, "HTTPRoute edge/"))
	}
}

// Every row charges before it keeps workloads and their exposure, so a
// request that ends mid-build (the export's timeout) stops at the next row
// with the request's error, not a "too large" one.
func TestChargeStopsWhenTheRequestEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	j := &rollup{ctx: ctx, budgeted: true, refsLeft: 10}
	if !j.charge(1) {
		t.Fatal("charge refused a live request")
	}
	cancel()
	if j.charge(1) || !errors.Is(j.stopped(), context.Canceled) {
		t.Fatalf("after cancel: stopped %v", j.stopped())
	}
	list := &rollup{ctx: ctx}
	if list.charge(1) {
		t.Fatal("a cancelled list read kept building")
	}
}

// Thousands of workloads behind the same thousands of routes borrow the one
// shared list instead of each copying it; workloads whose replicas bring the
// same two lists share one merged list.
func TestSharedRoutesAreBorrowedAcrossWorkloads(t *testing.T) {
	routeList := func(prefix string) []Route {
		out := make([]Route, 2000)
		for i := range out {
			out[i] = Route{Via: "HTTPRoute", Namespace: "edge", Name: fmt.Sprintf("%s-%04d", prefix, i)}
		}
		return out
	}
	shared, other := routeList("a"), routeList("b")
	var targets []target
	for i := range 5000 {
		w := WorkloadRef{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: fmt.Sprintf("web-%04d", i)}
		for r := range 2 {
			tg := liveTarget("shop", "ReplicaSet", fmt.Sprintf("web-%04d-%d", i, r), "app", "acme/web:1", "", w)
			tg.routes = shared
			if i%2 == 1 && r == 1 {
				tg.routes = other // a replica another Service also selects
			}
			targets = append(targets, tg)
		}
	}
	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	exposure, err := exposureOf(context.Background(), targets)
	runtime.ReadMemStats(&after)
	if err != nil {
		t.Fatal(err)
	}
	// Copying per workload would be over 1 GB; borrowing is a few MB.
	if alloc := after.TotalAlloc - before.TotalAlloc; alloc > 64<<20 {
		t.Fatalf("allocated %d MB", alloc>>20)
	}
	one, two := exposure[targets[0].workload], exposure[targets[2].workload]
	if &one[0] != &shared[0] || len(one) != 2000 {
		t.Fatal("a workload with one shared list doesn't borrow it")
	}
	if len(two) != 4000 || &exposure[targets[6].workload][0] != &two[0] {
		t.Fatalf("merged workloads: %d routes, shared %v", len(two), &exposure[targets[6].workload][0] == &two[0])
	}
	if !slices.IsSortedFunc(two, compareRoutes) {
		t.Fatal("merged routes aren't sorted")
	}
}

// A not-read row a search couldn't rule out still names its exposed
// workloads, in every view.
func TestExportNotReadRowsCarryExposure(t *testing.T) {
	vs := make([]vuln, MaxFindings+1)
	for i := range vs {
		vs[i] = vuln{"CVE-X", "LOW", "p", "1", "2"}
	}
	items, targets := filterFixture()
	items = append(items, vulnReport(reportSpec{name: "big", ns: "shop", ownerKind: "ReplicaSet", ownerName: "big-1", container: "app", repo: "acme/big", tag: "1", digest: "sha256:big", scanned: "2026-11-10T00:00:00Z", vulns: vs}))
	big := WorkloadRef{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "big"}
	targets = append(targets, exposedTarget(liveTarget("shop", "ReplicaSet", "big-1", "app", "acme/big:1", "", big), Route{Via: "LoadBalancer", Namespace: "shop", Name: "big"}))
	for _, view := range []View{ViewImage, ViewFix, ViewCVE, ViewWorkload} {
		f := Filter{Query: "CVE-A"}
		rows := withStatus(exportRows(t, mustExport(t, items, targets, view, f), f), StatusNotRead)
		if len(rows) != 1 || rows[0]["exposed_workloads"] != "Deployment shop/big (LoadBalancer shop/big)" {
			t.Fatalf("%s: %v", view, rows)
		}
	}
}

// Severity leads in every view; within a severity, a row with an exposed
// workload comes first, then one whose exposure is unknown (a workload in a
// namespace an exposure gap covers, flagged exposureUnknown), then the rest.
// By workload, an exposed workload with no scan sorts after every workload
// with findings and first among those without.
func TestExposureTierWithinSeverity(t *testing.T) {
	w := func(ns, name string) WorkloadRef {
		return WorkloadRef{Kind: "Deployment", Group: "apps", Namespace: ns, Name: name}
	}
	front, api, work, chk, edge, idle, batch := w("shop", "front"), w("dmz", "api"), w("shop", "work"), w("shop", "chk"), w("shop", "edge"), w("dmz", "idle"), w("dmz", "batch")
	report := func(name, ns, sev, pkg string) *unstructured.Unstructured {
		var vulns []vuln
		if sev != "" {
			vulns = []vuln{{"CVE-" + name, sev, pkg, "1", "2"}}
		}
		return vulnReport(reportSpec{name: "r-" + name, ns: ns, ownerKind: "ReplicaSet", ownerName: name + "-1", container: "app", uid: uidOf(name),
			repo: "acme/" + name, tag: "1", digest: "sha256:" + name, scanned: "2026-11-01T00:00:00Z", vulns: vulns})
	}
	items := []*unstructured.Unstructured{
		report("front", "shop", "HIGH", "pf"), report("api", "dmz", "HIGH", "pa"), report("work", "shop", "HIGH", "pw"),
		report("chk", "shop", "CRITICAL", "pc"), report("batch", "dmz", "", ""),
	}
	scanned := func(name string, ref WorkloadRef) target {
		return running(liveTarget(ref.Namespace, "ReplicaSet", name+"-1", "app", "acme/"+name+":1", uidOf(name), ref), "sha256:"+name)
	}
	targets := []target{
		exposedTarget(scanned("front", front), lbRoute), scanned("api", api), scanned("work", work), scanned("chk", chk), scanned("batch", batch),
		exposedTarget(liveTarget("shop", "ReplicaSet", "edge-1", "app", "acme/edge:1", "u-edge", edge), lbRoute),
		liveTarget("dmz", "ReplicaSet", "idle-1", "app", "acme/idle:1", "u-idle", idle),
	}
	dmzGap := &Exposure{Gaps: []resourceapi.Gap{{Code: readreason.UserForbidden, Kinds: ingressKind, Namespaces: resourceapi.Names([]string{"dmz"}, 64)}}}
	read := func(view View, exposure *Exposure) Response {
		resp, err := buildWithExposure(context.Background(), items, targets, view, Filter{}, false, exposure)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	images := read(ViewImage, dmzGap).Images
	var order []string
	for _, img := range images {
		order = append(order, img.Image)
		if img.ExposureUnknown != (img.Image == "acme/api:1") {
			t.Fatalf("image %s: exposureUnknown %v", img.Image, img.ExposureUnknown)
		}
	}
	if want := []string{"acme/chk:1", "acme/front:1", "acme/api:1", "acme/work:1"}; !slices.Equal(order, want) {
		t.Fatalf("image order %v, want %v", order, want)
	}
	order = nil
	for _, c := range read(ViewCVE, dmzGap).CVEs {
		order = append(order, c.ID)
		if c.ExposureUnknown != (c.ID == "CVE-api") {
			t.Fatalf("cve %s: exposureUnknown %v", c.ID, c.ExposureUnknown)
		}
	}
	if want := []string{"CVE-chk", "CVE-front", "CVE-api", "CVE-work"}; !slices.Equal(order, want) {
		t.Fatalf("cve order %v, want %v", order, want)
	}
	order = nil
	for _, fx := range read(ViewFix, dmzGap).Fixes {
		order = append(order, fx.Package)
		if fx.ExposureUnknown != (fx.Package == "pa") {
			t.Fatalf("fix %s: exposureUnknown %v", fx.Package, fx.ExposureUnknown)
		}
	}
	if want := []string{"pc", "pf", "pa", "pw"}; !slices.Equal(order, want) {
		t.Fatalf("fix order %v, want %v", order, want)
	}
	byWorkload := read(ViewWorkload, dmzGap)
	order = nil
	for _, wl := range byWorkload.Workloads {
		order = append(order, wl.Name)
		if wl.ExposureUnknown != (wl.Namespace == "dmz") {
			t.Fatalf("workload %s: exposureUnknown %v", wl.Name, wl.ExposureUnknown)
		}
	}
	if want := []string{"chk", "front", "api", "work", "edge", "idle"}; !slices.Equal(order, want) {
		t.Fatalf("workload order %v, want %v", order, want)
	}

	resp := read(ViewImage, dmzGap)
	for _, u := range resp.Unscanned {
		if u.ExposureUnknown != (u.Image == "acme/idle:1") {
			t.Fatalf("unscanned %s: exposureUnknown %v", u.Image, u.ExposureUnknown)
		}
	}
	if len(resp.Clean) != 1 || !resp.Clean[0].ExposureUnknown {
		t.Fatalf("clean image in dmz: %+v", resp.Clean)
	}

	// No gap: nothing is unknown, and api is just another High.
	for _, img := range read(ViewImage, nil).Images {
		if img.ExposureUnknown {
			t.Fatalf("no gap, yet %s is unknown", img.Image)
		}
	}
	// A gap over every namespace makes every row with no route unknown.
	all := &Exposure{Gaps: []resourceapi.Gap{{Code: readreason.KindSyncPending, Kinds: kinds(resourceapi.KindRef{Kind: "HTTPRoute", Group: "gateway.networking.k8s.io"}), Namespaces: resourceapi.AllNames()}}}
	for _, img := range read(ViewImage, all).Images {
		if img.ExposureUnknown != (img.ExposedTotal == 0) {
			t.Fatalf("all-namespace gap: %s exposureUnknown %v", img.Image, img.ExposureUnknown)
		}
	}
}

// Route kinds Radar doesn't follow are listed when, and only when, the
// cluster serves them.
func TestUncheckedRouteKinds(t *testing.T) {
	core, err := k8score.NewResourceDiscovery(fake.NewClientset().Discovery())
	if err != nil {
		t.Fatal(err)
	}
	core.AddAPIResource(k8score.APIResource{Group: "networking.istio.io", Version: "v1", Kind: "VirtualService", Name: "virtualservices", Namespaced: true, IsCRD: true, Verbs: []string{"list", "watch"}})
	core.AddAPIResource(k8score.APIResource{Group: "traefik.io", Version: "v1alpha1", Kind: "IngressRoute", Name: "ingressroutes", Namespaced: true, IsCRD: true, Verbs: []string{"list", "watch"}})
	core.AddAPIResource(k8score.APIResource{Group: "example.com", Version: "v1", Kind: "HTTPProxy", Name: "httpproxies", Namespaced: true, IsCRD: true, Verbs: []string{"list", "watch"}})
	src, _ := readExposure(context.Background(), nil, nil, &k8s.ResourceDiscovery{ResourceDiscovery: core}, nil)
	want := []resourceapi.KindRef{{Kind: "VirtualService", Group: "networking.istio.io"}, {Kind: "IngressRoute", Group: "traefik.io"}}
	if !slices.Equal(src.unchecked, want) {
		t.Fatalf("unchecked: %+v, want %+v", src.unchecked, want)
	}
	if src, _ := readExposure(context.Background(), nil, nil, nil, nil); src.unchecked != nil {
		t.Fatalf("no discovery, nothing served: %+v", src.unchecked)
	}
	every := servedUncheckedKinds(func(string, string) bool { return true })
	if len(every) != 10 || !slices.Contains(every, resourceapi.KindRef{Kind: "Service", Group: "serving.knative.dev"}) ||
		!slices.Contains(every, resourceapi.KindRef{Kind: "IngressRouteUDP", Group: "traefik.containo.us"}) || !slices.Contains(every, resourceapi.KindRef{Kind: "HTTPProxy", Group: "projectcontour.io"}) {
		t.Fatalf("every kind: %+v", every)
	}
}
