package trace

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/probe"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func waitFor(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("condition not met within deadline")
}

func TestBuildTrace_ServiceHealthy(t *testing.T) {
	defer k8s.ResetTestState()

	labelsMap := map[string]string{"app": "api"}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
		Spec: corev1.ServiceSpec{
			Selector: labelsMap,
			Ports:    []corev1.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromString("http")}},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "api-1", Namespace: "prod", Labels: labelsMap},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name:  "main",
			Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}},
			ReadinessProbe: &corev1.Probe{ProbeHandler: corev1.ProbeHandler{
				HTTPGet: &corev1.HTTPGetAction{Port: intstr.FromString("http")},
			}},
		}}},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: metav1.NewTime(time.Now())}},
		},
	}
	client := fake.NewClientset(svc, pod)
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("init: %v", err)
	}

	deps := Deps{
		Cache:     k8s.GetResourceCache(),
		Dynamic:   k8s.GetDynamicResourceCache(),
		Discovery: k8s.GetResourceDiscovery(),
		Issues:    issues.NewCacheProvider(),
	}

	var trace *Trace
	waitFor(t, func() bool {
		tr, err := BuildTraceWithOptions(context.Background(), deps, "Service", "prod", "api", Options{})
		if err != nil {
			return false
		}
		trace = tr
		return trace.Verdict == VerdictHealthy
	})

	if trace.Subject.Kind != "Service" || trace.Subject.Name != "api" {
		t.Errorf("subject = %+v, want Service prod/api", trace.Subject)
	}
	if len(trace.Downstream) != 2 {
		t.Fatalf("downstream len = %d, want 2 (Service, Pods); got %+v", len(trace.Downstream), trace.Downstream)
	}
	if trace.Downstream[0].Resource.Kind != "Service" || trace.Downstream[1].Resource.Kind != "Pods" {
		t.Errorf("downstream order wrong: %+v", trace.Downstream)
	}
	if trace.BrokenAt != -1 {
		t.Errorf("brokenAt = %d, want -1 on healthy trace", trace.BrokenAt)
	}
	if trace.Downstream[1].Meta["selected"] != 1 {
		t.Errorf("pods hop selected=%v, want 1", trace.Downstream[1].Meta["selected"])
	}
	if trace.Downstream[1].Meta["endpointSource"] != "pod-readiness" {
		t.Errorf("endpointSource=%v, want pod-readiness", trace.Downstream[1].Meta["endpointSource"])
	}
}

// TestBuildTrace_ShippedVerdictIsCoverageHonest pins the C3(b)/M4 collapse: the
// shipped t.Verdict is the single coverage-honest value, not the raw probe-mutated
// one. A route reached only via the apiserver proxy (indirect, local vantage) is
// not a confident green, so the stored verdict must read unknown, matching what
// CoverageVerdict and the MCP surface report. Without the collapse, t.Verdict would
// stay healthy while the headline honestly says "reached via API server, not live".
func TestBuildTrace_ShippedVerdictIsCoverageHonest(t *testing.T) {
	defer k8s.ResetTestState()
	t.Setenv("KUBERNETES_SERVICE_HOST", "") // local vantage: only the apiserver path probes
	stubProxyProbes(t)                      // apiserver proxy returns 2xx (indirect success)

	labelsMap := map[string]string{"app": "api"}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
		Spec: corev1.ServiceSpec{
			Selector: labelsMap,
			Ports:    []corev1.ServicePort{{Name: "http", Port: 80, TargetPort: intstr.FromString("http")}},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "api-1", Namespace: "prod", Labels: labelsMap},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name:  "main",
			Ports: []corev1.ContainerPort{{Name: "http", ContainerPort: 8080}},
		}}},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: metav1.NewTime(time.Now())}},
		},
	}
	client := fake.NewClientset(svc, pod)
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("init: %v", err)
	}
	deps := Deps{Cache: k8s.GetResourceCache(), Dynamic: k8s.GetDynamicResourceCache(), Discovery: k8s.GetResourceDiscovery(), Issues: issues.NewCacheProvider()}

	var tr *Trace
	waitFor(t, func() bool {
		x, err := BuildTraceWithOptions(context.Background(), deps, "Service", "prod", "api", Options{Probe: true, ProbeBudget: 2 * time.Second})
		if err != nil {
			return false
		}
		tr = x
		return len(tr.Downstream) == 2
	})

	// The collapse in action: an apiserver-only reach is indirect, so the shipped
	// verdict is unknown, not a confident healthy.
	if tr.Verdict != VerdictUnknown {
		t.Errorf("shipped verdict = %q, want unknown (apiserver-only reach is indirect, not a confident green)", tr.Verdict)
	}
	// Invariant the collapse guarantees: the stored verdict always equals
	// CoverageVerdict, one source of truth for REST, UI, and MCP. If either collapse
	// site is removed, the raw healthy leaks and this fails.
	if cv := CoverageVerdict(tr); tr.Verdict != cv {
		t.Errorf("shipped verdict %q != CoverageVerdict %q, the collapse must keep them identical", tr.Verdict, cv)
	}
}

func TestBuildTrace_ServiceNoSelectorIsSelectorless(t *testing.T) {
	defer k8s.ResetTestState()

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "manual", Namespace: "prod"},
		Spec: corev1.ServiceSpec{
			Ports: []corev1.ServicePort{{Port: 80}},
		},
	}
	client := fake.NewClientset(svc)
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("init: %v", err)
	}
	deps := Deps{
		Cache:     k8s.GetResourceCache(),
		Dynamic:   k8s.GetDynamicResourceCache(),
		Discovery: k8s.GetResourceDiscovery(),
		Issues:    issues.NewCacheProvider(),
	}

	var trace *Trace
	waitFor(t, func() bool {
		tr, _ := BuildTraceWithOptions(context.Background(), deps, "Service", "prod", "manual", Options{})
		if tr == nil {
			return false
		}
		trace = tr
		return len(trace.Downstream) == 1
	})

	if trace.Downstream[0].Meta["selectorless"] != true {
		t.Errorf("expected selectorless=true on Service hop meta, got %+v", trace.Downstream[0].Meta)
	}
	hasInfo := false
	for _, f := range trace.Downstream[0].Findings {
		if f.Code == "svc:selectorless" {
			hasInfo = true
		}
	}
	if !hasInfo {
		t.Errorf("expected svc:selectorless finding, got %+v", trace.Downstream[0].Findings)
	}
	if trace.Verdict != VerdictUnknown {
		t.Errorf("selectorless trace must not claim healthy; got verdict=%q", trace.Verdict)
	}
	if trace.Reason == "" {
		t.Errorf("unknown verdict must carry a reason for the banner")
	}
}

func TestBuildTrace_ServiceExternalNameIsTerminal(t *testing.T) {
	defer k8s.ResetTestState()

	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "ext", Namespace: "prod"},
		Spec: corev1.ServiceSpec{
			Type:         corev1.ServiceTypeExternalName,
			ExternalName: "stripe.example.com",
			Ports:        []corev1.ServicePort{{Name: "https", Port: 443}},
		},
	}
	client := fake.NewClientset(svc)
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("init: %v", err)
	}
	deps := Deps{
		Cache:     k8s.GetResourceCache(),
		Dynamic:   k8s.GetDynamicResourceCache(),
		Discovery: k8s.GetResourceDiscovery(),
		Issues:    issues.NewCacheProvider(),
	}

	var trace *Trace
	waitFor(t, func() bool {
		tr, _ := BuildTraceWithOptions(context.Background(), deps, "Service", "prod", "ext", Options{})
		if tr == nil {
			return false
		}
		trace = tr
		return len(trace.Downstream) == 2
	})
	if trace.Downstream[1].Resource.Kind != "ExternalName" {
		t.Errorf("expected ExternalName hop, got %+v", trace.Downstream[1].Resource)
	}
	// The declared Service ports ride on the terminal hop so the probe layer
	// tests the declared port/protocol instead of assuming HTTP on :80.
	cfg := trace.Downstream[1].Config
	if cfg == nil || len(cfg.Ports) != 1 || cfg.Ports[0].Port != 443 || cfg.Ports[0].Name != "https" {
		t.Errorf("ExternalName hop should carry the declared Service ports, got %+v", cfg)
	}
	if trace.Verdict == VerdictBroken {
		t.Errorf("ExternalName service should not be broken: %+v", trace)
	}
}

func TestBuildTrace_ServiceWithUpstreamIngressIsParallel(t *testing.T) {
	defer k8s.ResetTestState()

	labelsMap := map[string]string{"app": "api"}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
		Spec:       corev1.ServiceSpec{Selector: labelsMap, Ports: []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromInt(8080)}}},
	}
	ingA := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "ing-a", Namespace: "prod"},
		Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{
			IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{{
				Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "api", Port: networkingv1.ServiceBackendPort{Number: 80}}},
			}}}},
		}}},
	}
	ingB := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "ing-b", Namespace: "prod"},
		Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{
			IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{{
				Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "api", Port: networkingv1.ServiceBackendPort{Number: 80}}},
			}}}},
		}}},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "api-1", Namespace: "prod", Labels: labelsMap},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: metav1.NewTime(time.Now())}},
		},
	}
	client := fake.NewClientset(svc, ingA, ingB, pod)
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("init: %v", err)
	}
	deps := Deps{
		Cache:     k8s.GetResourceCache(),
		Dynamic:   k8s.GetDynamicResourceCache(),
		Discovery: k8s.GetResourceDiscovery(),
		Issues:    issues.NewCacheProvider(),
	}

	var trace *Trace
	waitFor(t, func() bool {
		tr, _ := BuildTraceWithOptions(context.Background(), deps, "Service", "prod", "api", Options{})
		if tr == nil {
			return false
		}
		trace = tr
		return len(trace.Upstreams) == 2
	})

	if trace.Verdict != VerdictHealthy && trace.Verdict != VerdictDegraded {
		t.Errorf("verdict = %q, expected healthy/degraded with two healthy ingresses; trace=%+v", trace.Verdict, trace)
	}
}

func TestBuildTrace_BrokenServiceFlagsBrokenAt(t *testing.T) {
	defer k8s.ResetTestState()

	labelsMap := map[string]string{"app": "api"}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
		Spec: corev1.ServiceSpec{
			Selector: labelsMap,
			Ports:    []corev1.ServicePort{{Port: 80, TargetPort: intstr.FromInt(8080)}},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "api-1", Namespace: "prod", Labels: labelsMap, CreationTimestamp: metav1.NewTime(time.Now().Add(-10 * time.Minute))},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "main"}}},
		Status: corev1.PodStatus{
			Phase:      corev1.PodPending,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse, LastTransitionTime: metav1.NewTime(time.Now().Add(-10 * time.Minute))}},
		},
	}
	client := fake.NewClientset(svc, pod)
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("init: %v", err)
	}
	deps := Deps{
		Cache:     k8s.GetResourceCache(),
		Dynamic:   k8s.GetDynamicResourceCache(),
		Discovery: k8s.GetResourceDiscovery(),
		Issues:    issues.NewCacheProvider(),
	}

	var trace *Trace
	waitFor(t, func() bool {
		tr, _ := BuildTraceWithOptions(context.Background(), deps, "Service", "prod", "api", Options{})
		if tr == nil {
			return false
		}
		trace = tr
		return trace.Verdict == VerdictBroken || trace.Verdict == VerdictDegraded
	})

	if trace.Verdict != VerdictBroken {
		t.Fatalf("expected broken verdict with 0 ready pods; got %q (trace=%+v)", trace.Verdict, trace)
	}
	// The existing detection attaches "0/N selected pods ready" to the
	// Service (not each Pod) - that's where the operator can act. So
	// BrokenAt points at the Service hop (index 0), and the diagnosis
	// localizes correctly: "this Service has no ready endpoints".
	if trace.BrokenAt != 0 {
		t.Errorf("brokenAt=%d, want 0 (Service hop carries the no-ready-endpoints finding)", trace.BrokenAt)
	}
	var noReadyCode string
	for _, f := range trace.Downstream[0].Findings {
		if strings.Contains(f.Code, "no-ready-endpoints") || strings.Contains(f.Message, "0/") {
			noReadyCode = f.Code
		}
	}
	if noReadyCode == "" {
		t.Errorf("expected svc:no-ready-endpoints style finding on Service hop, got %+v", trace.Downstream[0].Findings)
	}
}

func TestBuildTrace_CacheNotReadyReturnsUnknown(t *testing.T) {
	defer k8s.ResetTestState()

	deps := Deps{
		Cache:     nil,
		Dynamic:   nil,
		Discovery: nil,
		Issues:    nil,
	}
	trace, err := BuildTraceWithOptions(context.Background(), deps, "Service", "prod", "api", Options{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if trace.Verdict != VerdictUnknown {
		t.Errorf("verdict=%q, want unknown when cache not ready", trace.Verdict)
	}
	if !strings.Contains(trace.Reason, "cache syncing") {
		t.Errorf("reason=%q, want cache-syncing message", trace.Reason)
	}
	for _, tt := range []struct {
		kind, group string
	}{
		{kind: "Ingress", group: "networking.k8s.io"},
		{kind: "HTTPRoute", group: "gateway.networking.k8s.io"},
		{kind: "Gateway", group: "gateway.networking.k8s.io"},
	} {
		got, err := BuildTraceWithOptions(context.Background(), deps, tt.kind, "prod", "entry", Options{})
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", tt.kind, err)
		}
		if got.Subject.Kind != tt.kind || got.Subject.Group != tt.group {
			t.Errorf("%s cache-cold subject = %+v, want group %q", tt.kind, got.Subject, tt.group)
		}
	}
}

func TestBuildTrace_UnknownKindReturnsUnknown(t *testing.T) {
	defer k8s.ResetTestState()
	client := fake.NewClientset()
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("init: %v", err)
	}
	deps := Deps{
		Cache:     k8s.GetResourceCache(),
		Dynamic:   k8s.GetDynamicResourceCache(),
		Discovery: k8s.GetResourceDiscovery(),
		Issues:    issues.NewCacheProvider(),
	}
	trace, _ := BuildTraceWithOptions(context.Background(), deps, "Pod", "prod", "x", Options{})
	if trace.Verdict != VerdictUnknown {
		t.Errorf("verdict=%q, want unknown for unsupported kind", trace.Verdict)
	}
}

// TestTraceGatewayEntry_RedactsCrossNamespaceRoutes pins the per-request
// scope check on the Gateway-entry fan-out. A Gateway can have Routes from
// many namespaces attach to it (the multi-tenant Gateway API model); a
// caller scoped to a subset must see that out-of-scope Routes exist (as an
// anonymous count) without their identities OR findings leaking out of the
// cluster-wide cache - the name + namespace of a route the caller can't
// read are themselves protected data. This guards the fourth fan-out site
// against the missed-redaction class that the other three sites already
// cover.
func TestTraceGatewayEntry_RedactsCrossNamespaceRoutes(t *testing.T) {
	defer k8s.ResetTestState()
	defer k8s.ResetTestDynamicState()

	// The Gateway entry needs a synced typed cache for the readiness probe;
	// no Services participate in this trace.
	if err := k8s.InitTestResourceCache(fake.NewClientset()); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}

	gwGVR := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"}
	routeGVR := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}
	dynClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			gwGVR:    "GatewayList",
			routeGVR: "HTTPRouteList",
		},
		attachedHTTPRoute("route-b", "team-b", "api-gateway", "gateway-system"),
		attachedHTTPRoute("route-c", "team-c", "api-gateway", "gateway-system"),
		attachedHTTPRoute("route-d", "team-d", "api-gateway", "gateway-system"),
	)
	// Add the Gateway through the tracker with an explicit GVR: the fake
	// client's kind-to-resource guess pluralizes "Gateway" to "gatewaies",
	// so an initial-object Gateway would land under the wrong resource and
	// never list. Routes pluralize correctly and stay as initial objects.
	if err := dynClient.Tracker().Create(gwGVR, gatewayObject("api-gateway", "gateway-system"), "gateway-system"); err != nil {
		t.Fatalf("tracker create gateway: %v", err)
	}
	if err := k8s.InitTestDynamicResourceCache(dynClient, []k8s.APIResource{
		{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "Gateway", Name: "gateways", Namespaced: true, Verbs: []string{"list", "watch"}},
		{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute", Name: "httproutes", Namespaced: true, Verbs: []string{"list", "watch"}},
	}); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}

	dynCache := k8s.GetDynamicResourceCache()
	for _, gvr := range []schema.GroupVersionResource{gwGVR, routeGVR} {
		if err := dynCache.EnsureWatching(gvr); err != nil {
			t.Fatalf("EnsureWatching %s: %v", gvr.Resource, err)
		}
		if !dynCache.WaitForSync(gvr, 2*time.Second) {
			t.Fatalf("dynamic cache did not sync %s", gvr.Resource)
		}
	}

	deps := Deps{
		Cache:             k8s.GetResourceCache(),
		Dynamic:           dynCache,
		Discovery:         k8s.GetResourceDiscovery(),
		Issues:            issues.NewCacheProvider(),
		AllowedNamespaces: []string{"gateway-system", "team-b"}, // team-c + team-d are out of scope
	}
	tr, err := BuildTraceWithOptions(context.Background(), deps, "Gateway", "gateway-system", "api-gateway", Options{})
	if err != nil {
		t.Fatalf("BuildTraceWithOptions: %v", err)
	}

	var inScope, redacted *Hop
	for i := range tr.Downstream {
		if tr.Downstream[i].Resource.Namespace == "team-b" {
			inScope = &tr.Downstream[i]
		}
		if findingCodePresent(tr.Downstream[i].Findings, "rbac:cross-namespace-redacted") {
			redacted = &tr.Downstream[i]
		}
	}
	if inScope == nil {
		t.Fatalf("expected the in-scope attached-route hop, got verdict=%q reason=%q downstream %+v", tr.Verdict, tr.Reason, tr.Downstream)
	}
	// BOTH out-of-scope routes collapse into ONE anonymous aggregate carrying
	// the count - not one identified hop each.
	assertRedactedAggregate(t, redacted, "2 HTTPRoutes")
	assertNoIdentityLeak(t, tr, "route-c", "team-c", "route-d", "team-d")

	if _, marked := inScope.Meta["endpointSource"]; marked {
		t.Errorf("in-scope hop should not carry the redaction marker, got %+v", inScope.Meta)
	}
	if findingCodePresent(inScope.Findings, "rbac:cross-namespace-redacted") {
		t.Errorf("in-scope hop should not be redacted, got %+v", inScope.Findings)
	}
	// The synced cluster-wide test cache is authoritative - the inventory
	// must not carry the incomplete-index marker.
	if findingCodePresent(tr.Downstream[0].Findings, "trace:attached-routes-incomplete") {
		t.Errorf("synced route index must not flag the inventory incomplete, got %+v", tr.Downstream[0].Findings)
	}
}

func gatewayObject(name, namespace string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "Gateway",
		"metadata":   map[string]any{"name": name, "namespace": namespace},
		"spec": map[string]any{
			"listeners": []any{
				map[string]any{"name": "http", "port": int64(80), "protocol": "HTTP"},
			},
		},
	}}
}

func attachedHTTPRoute(name, namespace, gwName, gwNamespace string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "HTTPRoute",
		"metadata":   map[string]any{"name": name, "namespace": namespace},
		"spec": map[string]any{
			"parentRefs": []any{
				map[string]any{"name": gwName, "namespace": gwNamespace},
			},
		},
	}}
}

func findingCodePresent(findings []Finding, code string) bool {
	for _, f := range findings {
		if f.Code == code {
			return true
		}
	}
	return false
}

// TestClassifyHopProbes_HTTPAuthoritative pins that a genuine HTTP 5xx on an
// Ingress hop is NOT diluted by its own DNS/TCP prerequisites. Before the fix,
// DNS-ok + TCP-ok + HTTP-500 counted real=3 degraded=1 -> no escalation, so the
// banner read "healthy" while the chip said "server error". HTTP is
// authoritative: the hop must count only the HTTP result (real=1 degraded=1).
func TestClassifyHopProbes_HTTPAuthoritative(t *testing.T) {
	hop := Hop{Probes: []probe.Result{
		{Layer: probe.LayerDNS, OK: true},
		{Layer: probe.LayerTCP, OK: true},
		{Layer: probe.LayerHTTP, OK: true, Tone: probe.ToneDegraded}, // HTTP 500
	}}
	failed, degraded, real := classifyHopProbes(hop)
	if real != 1 || degraded != 1 || failed != 0 {
		t.Fatalf("HTTP 5xx must dominate its DNS/TCP prerequisites: got failed=%d degraded=%d real=%d, want 0/1/1", failed, degraded, real)
	}
	tr := &Trace{Verdict: VerdictHealthy, Downstream: []Hop{{Resource: ResourceRef{Kind: "Ingress", Name: "ing"}, Probes: hop.Probes}}}
	if v, _ := reviseVerdictWithProbes(tr); v != VerdictDegraded {
		t.Errorf("DNS+TCP ok + HTTP 500 -> verdict = %q, want %q (banner must not say healthy)", v, VerdictDegraded)
	}
}

// TestClassifyHopProbes_TransportFallback: with no HTTP probe (e.g. a non-HTTP
// port where HTTP was skipped), the hop still counts the highest transport
// layer actually run, so a TCP failure is real evidence.
func TestClassifyHopProbes_TransportFallback(t *testing.T) {
	hop := Hop{Probes: []probe.Result{
		{Layer: probe.LayerDNS, OK: true},
		{Layer: probe.LayerTCP, OK: false, Error: "connection refused"},
	}}
	failed, _, real := classifyHopProbes(hop)
	if real != 1 || failed != 1 {
		t.Errorf("no-HTTP hop should fall back to TCP: got failed=%d real=%d, want 1/1", failed, real)
	}
}

// TestClassifyHopProbes_ReplicaDilutionPreserved: the layer-aware change must
// NOT break replica dilution - 1 of 3 pods failing its HTTP probe is still
// transient (real=3, failed=1), not a whole-hop failure.
func TestClassifyHopProbes_ReplicaDilutionPreserved(t *testing.T) {
	hop := Hop{Probes: []probe.Result{
		{Layer: probe.LayerHTTP, Target: "pod-a port 80", OK: true, Tone: probe.ToneHealthy},
		{Layer: probe.LayerHTTP, Target: "pod-b port 80", OK: true, Tone: probe.ToneHealthy},
		{Layer: probe.LayerHTTP, Target: "pod-c port 80", OK: false, Error: "i/o timeout"},
	}}
	failed, _, real := classifyHopProbes(hop)
	if real != 3 || failed != 1 {
		t.Errorf("replica dilution: got failed=%d real=%d, want 1/3 (1 of 3 pods failing is transient)", failed, real)
	}
	if hopVotesDegraded(failed, 0, real) {
		t.Error("1 of 3 replicas failing should NOT escalate the whole-hop verdict")
	}
}

// TestReviseVerdict_UpstreamBreakDoesNotBlameSubject: when every entry path
// (upstream) is broken but the subject Service + its Pods are healthy, the
// verdict is broken but must NOT anchor brokenAt to downstream[0] - that would
// render "Service api is broken" and blame a healthy resource. brokenAt stays
// unanchored and the reason names the real situation.
func TestReviseVerdict_UpstreamBreakDoesNotBlameSubject(t *testing.T) {
	tr := &Trace{
		Verdict:  VerdictHealthy,
		BrokenAt: -1,
		Downstream: []Hop{{
			Resource: ResourceRef{Kind: "Service", Name: "api"},
			Probes:   []probe.Result{{Layer: probe.LayerHTTP, OK: true, Tone: probe.ToneHealthy}},
		}},
		Upstreams: []Hop{{
			Resource: ResourceRef{Kind: "Ingress", Name: "ing"},
			Probes:   []probe.Result{{Layer: probe.LayerHTTP, OK: false, Error: "connection refused"}},
		}},
	}
	v, brokenAt := reviseVerdictWithProbes(tr)
	if v != VerdictBroken {
		t.Errorf("all-upstreams-broken: verdict=%q, want broken", v)
	}
	if brokenAt == 0 {
		t.Errorf("brokenAt must not anchor to downstream[0] (blames the healthy subject); got %d", brokenAt)
	}
	if !strings.Contains(tr.Reason, "entry paths") {
		t.Errorf("reason should name the entry-path break, got %q", tr.Reason)
	}
}

// --- Multi-backend Ingress/Route verdict (cross-functional design) ---

// TestComputeVerdict_PartialBackendBreakIsDegraded: one backend route broken
// among healthy ones is DEGRADED (other routes still deliver), not the
// confident-wrong "broken - traffic can't pass". brokenAt points at the broken
// backend; the reason names it + the route count.
func TestComputeVerdict_PartialBackendBreakIsDegraded(t *testing.T) {
	tr := &Trace{Downstream: []Hop{
		{Resource: ResourceRef{Kind: "Ingress", Name: "app"}, Edge: "entry:Ingress"},
		{Resource: ResourceRef{Kind: "Service", Name: "svc-a"}, Edge: "Ingress->Service", Findings: []Finding{{Severity: SeverityCritical, Code: "ref:missing"}}},
		{Resource: ResourceRef{Kind: "Pods"}, Edge: "Service->Pods"},
		{Resource: ResourceRef{Kind: "Service", Name: "svc-b"}, Edge: "Ingress->Service"},
		{Resource: ResourceRef{Kind: "Pods"}, Edge: "Service->Pods"},
	}}
	v, brokenAt := computeVerdict(tr)
	if v != VerdictDegraded {
		t.Errorf("partial backend break: verdict=%q, want degraded (other routes deliver)", v)
	}
	if brokenAt != 1 {
		t.Errorf("brokenAt=%d, want 1 (the broken backend svc-a, not the healthy entry)", brokenAt)
	}
	if !strings.Contains(tr.Reason, "1 of 2 routes broken") {
		t.Errorf("reason should state the route count, got %q", tr.Reason)
	}
}

// TestComputeVerdict_AllBackendsBrokenIsBroken: when EVERY route is broken,
// traffic genuinely can't pass - that stays loud-red broken.
func TestComputeVerdict_AllBackendsBrokenIsBroken(t *testing.T) {
	tr := &Trace{Downstream: []Hop{
		{Resource: ResourceRef{Kind: "Ingress"}, Edge: "entry:Ingress"},
		{Resource: ResourceRef{Kind: "Service", Name: "a"}, Edge: "Ingress->Service", Findings: []Finding{{Severity: SeverityCritical}}},
		{Resource: ResourceRef{Kind: "Service", Name: "b"}, Edge: "Ingress->Service", Findings: []Finding{{Severity: SeverityCritical}}},
	}}
	if v, _ := computeVerdict(tr); v != VerdictBroken {
		t.Errorf("all backends broken: verdict=%q, want broken", v)
	}
}

// TestComputeVerdict_EntryCriticalIsBroken: a critical on the entry hop (no
// controller address, host unresolvable) means nothing can enter - broken,
// anchored on the entry row.
func TestComputeVerdict_EntryCriticalIsBroken(t *testing.T) {
	tr := &Trace{Downstream: []Hop{
		{Resource: ResourceRef{Kind: "Ingress", Name: "app"}, Edge: "entry:Ingress", Findings: []Finding{{Severity: SeverityCritical, Code: "no-address"}}},
		{Resource: ResourceRef{Kind: "Service", Name: "a"}, Edge: "Ingress->Service"},
	}}
	v, brokenAt := computeVerdict(tr)
	if v != VerdictBroken || brokenAt != 0 {
		t.Errorf("entry critical: verdict=%q brokenAt=%d, want broken/0", v, brokenAt)
	}
}

// TestComputeVerdict_SingleChainStillBreaks: a Service subject (no backend
// branches) keeps the flat semantics - a broken Pods hop breaks the path.
func TestComputeVerdict_SingleChainStillBreaks(t *testing.T) {
	tr := &Trace{Downstream: []Hop{
		{Resource: ResourceRef{Kind: "Service", Name: "svc"}},
		{Resource: ResourceRef{Kind: "Pods"}, Edge: "Service->Pods", Findings: []Finding{{Severity: SeverityCritical}}},
	}}
	if v, _ := computeVerdict(tr); v != VerdictBroken {
		t.Errorf("single chain with broken Pods: verdict=%q, want broken", v)
	}
}

// TestReviseVerdict_PartialBackendProbeBreakIsDegraded: same rule on the probe
// path - one route's probes failing while another succeeds is degraded.
func TestReviseVerdict_PartialBackendProbeBreakIsDegraded(t *testing.T) {
	tr := &Trace{Verdict: VerdictHealthy, BrokenAt: -1, Downstream: []Hop{
		{Resource: ResourceRef{Kind: "Ingress"}, Edge: "entry:Ingress"},
		{Resource: ResourceRef{Kind: "Service", Name: "a"}, Edge: "Ingress->Service", Probes: []probe.Result{{Layer: probe.LayerHTTP, OK: false, Error: "connection refused"}}},
		{Resource: ResourceRef{Kind: "Service", Name: "b"}, Edge: "Ingress->Service", Probes: []probe.Result{{Layer: probe.LayerHTTP, OK: true, Tone: probe.ToneHealthy}}},
	}}
	v, brokenAt := reviseVerdictWithProbes(tr)
	if v != VerdictDegraded {
		t.Errorf("partial probe break: verdict=%q, want degraded", v)
	}
	if brokenAt != 1 {
		t.Errorf("brokenAt=%d, want 1 (broken backend a)", brokenAt)
	}
}

// TestComputeVerdict_EntryMissingRefIsPartialDegrade: a missing-backend ref is
// reported as a critical on the ENTRY hop, but it only breaks that one route.
// With another healthy route, the verdict is degraded - never a confident
// "Ingress is broken" claim that one dead ref does not earn.
func TestComputeVerdict_EntryMissingRefIsPartialDegrade(t *testing.T) {
	tr := &Trace{Downstream: []Hop{
		{Resource: ResourceRef{Kind: "Ingress", Name: "app"}, Edge: "entry:Ingress", Findings: []Finding{{Code: "missing_ref:Missing backend Service", Severity: SeverityCritical, Message: "/api references ghost which does not exist"}}},
		{Resource: ResourceRef{Kind: "Service", Name: "echo"}, Edge: "Ingress->Service"},
		{Resource: ResourceRef{Kind: "Pods"}, Edge: "Service->Pods"},
		{Resource: ResourceRef{Kind: "Service", Name: "ghost"}, Edge: "Ingress->Service"},
	}}
	v, _ := computeVerdict(tr)
	if v != VerdictDegraded {
		t.Errorf("entry missing-ref + a healthy route: verdict=%q, want degraded", v)
	}
	if !strings.Contains(tr.Reason, "1 of 2 routes broken") {
		t.Errorf("reason should state the route count, got %q", tr.Reason)
	}
}

// TestComputeVerdict_EntryMissingRefAllRoutesBroken: when the missing ref is the
// ONLY route, traffic genuinely can't pass - stays broken.
func TestComputeVerdict_EntryMissingRefAllRoutesBroken(t *testing.T) {
	tr := &Trace{Downstream: []Hop{
		{Resource: ResourceRef{Kind: "Ingress"}, Edge: "entry:Ingress", Findings: []Finding{{Code: "missing_ref:x", Severity: SeverityCritical}}},
		{Resource: ResourceRef{Kind: "Service", Name: "ghost"}, Edge: "Ingress->Service"},
	}}
	if v, _ := computeVerdict(tr); v != VerdictBroken {
		t.Errorf("single missing route: verdict=%q, want broken", v)
	}
}

// TestClassifyHopProbes_DataPathWinsOverApiserver: in-cluster, the pod-to-pod
// data path succeeds while the apiserver proxy fails (RBAC denied / non-HTTP).
// The hop is reachable via the REAL path - an apiserver-proxy failure must not
// condemn it (that divergence is an info finding, not a broken verdict).
func TestClassifyHopProbes_DataPathWinsOverApiserver(t *testing.T) {
	hop := Hop{Probes: []probe.Result{
		{Layer: probe.LayerTCP, Path: probe.PathData, OK: true},
		{Layer: probe.LayerHTTP, Path: probe.PathAPIServer, OK: false, Error: "permission denied"},
	}}
	failed, _, real := classifyHopProbes(hop)
	if real != 1 || failed != 0 {
		t.Errorf("data-path success must not be condemned by apiserver failure: failed=%d real=%d, want 0/1", failed, real)
	}
}

// TestAnyProbeReached_FailedProbeIsNotReached: the "reached a server but didn't
// verify the exact route" reason must NOT fire when every probe FAILED (e.g. the
// apiserver-proxy got connection-refused because the cluster API is down). A
// failed probe has OK==false and reached nothing - claiming "reached a server"
// there is a false-heal. Only a probe that actually answered (OK) counts.
func TestAnyProbeReached_FailedProbeIsNotReached(t *testing.T) {
	failedOnly := &Trace{Downstream: []Hop{{Probes: []probe.Result{
		{Layer: probe.LayerHTTP, Path: probe.PathAPIServer, OK: false, Error: "Connection refused. Nothing is listening on the port."},
	}}}}
	if anyProbeReached(failedOnly) {
		t.Error("a failed-only probe set must not count as 'reached a server'")
	}

	skippedOnly := &Trace{Downstream: []Hop{{Probes: []probe.Result{
		{Layer: probe.LayerHTTP, Path: probe.PathAPIServer, Skipped: true, Reason: "couldn't reach the cluster API from here"},
	}}}}
	if anyProbeReached(skippedOnly) {
		t.Error("a skipped-only probe set must not count as 'reached a server'")
	}

	// A TCP port that actually answered DID reach a server - the reason is honest.
	reached := &Trace{Downstream: []Hop{{Probes: []probe.Result{
		{Layer: probe.LayerTCP, Path: probe.PathData, OK: true},
	}}}}
	if !anyProbeReached(reached) {
		t.Error("a TCP probe that answered must count as 'reached a server'")
	}
}

// TestComputeVerdict_PartialReadyBackendIsDegradedNotBroken: a Service whose
// backend has SOME ready endpoints (1 of 2 pods, the other crashlooping) still
// SERVES traffic - it must read degraded, never the false-condemn "broken"
// (which the UI paints ✗ unreachable). A per-pod crash among serving replicas is
// a degradation, not an outage. The 0-ready case stays broken (genuinely down).
func TestComputeVerdict_PartialReadyBackendIsDegradedNotBroken(t *testing.T) {
	withReady := func(ready, selected int) *Trace {
		return &Trace{Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Service", Name: "dual"}, Edge: "entry:Service"},
			{Resource: ResourceRef{Kind: "Pods"}, Edge: "Service->Pods",
				Meta:     map[string]any{"ready": ready, "selected": selected},
				Findings: []Finding{{Code: "problem:CrashLoopBackOff", Severity: SeverityCritical, Message: "CrashLoopBackOff"}}},
		}}
	}
	if v, _ := computeVerdict(withReady(1, 2)); v != VerdictDegraded {
		t.Errorf("1-of-2-ready backend with a crashing pod: verdict=%q, want degraded (it still serves)", v)
	}
	if v, _ := computeVerdict(withReady(0, 1)); v != VerdictBroken {
		t.Errorf("0-ready backend: verdict=%q, want broken (genuinely no live endpoint)", v)
	}
}

// TestComputeVerdict_GatewayPartialRouteBreakIsDegraded: a Gateway with one
// healthy and one broken attached route is degraded (1 of 2 routes), not the
// whole-Gateway "broken" - same parallel-branch honesty as Ingress backends.
func TestComputeVerdict_GatewayPartialRouteBreakIsDegraded(t *testing.T) {
	tr := &Trace{Downstream: []Hop{
		{Resource: ResourceRef{Kind: "Gateway", Name: "gw"}, Edge: "entry:Gateway"},
		{Resource: ResourceRef{Kind: "HTTPRoute", Name: "r-ok"}, Edge: "Gateway->HTTPRoute"},
		{Resource: ResourceRef{Kind: "HTTPRoute", Name: "r-bad"}, Edge: "Gateway->HTTPRoute", Findings: []Finding{{Severity: SeverityCritical}}},
	}}
	v, _ := computeVerdict(tr)
	if v != VerdictDegraded {
		t.Errorf("gateway 1 of 2 routes broken: verdict=%q, want degraded", v)
	}
	if !strings.Contains(tr.Reason, "1 of 2 routes broken") {
		t.Errorf("reason should state route count, got %q", tr.Reason)
	}
}

// TestComputeVerdict_UpstreamMissingRefDoesNotDegradeBackend: a healthy Service
// that is a backend of an Ingress whose OTHER route points at a missing Service
// must stay healthy - the broken sibling route doesn't reach this subject, so
// it can't degrade it.
func TestComputeVerdict_UpstreamMissingRefDoesNotDegradeBackend(t *testing.T) {
	tr := &Trace{
		Downstream: []Hop{{Resource: ResourceRef{Kind: "Service", Name: "echo"}}},
		Upstreams: []Hop{{
			Resource: ResourceRef{Kind: "Ingress", Name: "multi"},
			Findings: []Finding{{Code: "missing_ref:Missing backend Service", Severity: SeverityCritical, Message: "/api references ghost"}},
		}},
	}
	if v, _ := computeVerdict(tr); v != VerdictHealthy {
		t.Errorf("backend with an upstream whose OTHER route is broken: verdict=%q, want healthy", v)
	}
}

// TestReviseVerdict_MultiBackendEntryProbeFailBreaks: when the entry hop's own
// probes fail (front door unreachable) but a backend looks healthy, the
// multi-backend probe fold must still report broken - a reachable backend
// behind an unreachable entry is not "healthy".
func TestReviseVerdict_MultiBackendEntryProbeFailBreaks(t *testing.T) {
	tr := &Trace{
		Verdict: VerdictHealthy, BrokenAt: -1,
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Ingress", Name: "ing"}, Edge: "entry:Ingress", Probes: []probe.Result{{Layer: probe.LayerTCP, OK: false, Error: "connection refused"}}},
			{Resource: ResourceRef{Kind: "Service", Name: "echo"}, Edge: "Ingress->Service", Probes: []probe.Result{{Layer: probe.LayerHTTP, Path: probe.PathAPIServer, OK: true, Tone: probe.ToneHealthy}}},
		},
	}
	v, brokenAt := reviseVerdictWithProbes(tr)
	if v != VerdictBroken || brokenAt != 0 {
		t.Errorf("entry unreachable + healthy backend: verdict=%q brokenAt=%d, want broken/0", v, brokenAt)
	}
}

// TestReviseVerdict_MultiBackendEntryDegradedUpgrades: an entry hop returning
// 5xx (degraded), with healthy backends, must not read "healthy".
func TestReviseVerdict_MultiBackendEntryDegradedUpgrades(t *testing.T) {
	tr := &Trace{
		Verdict: VerdictHealthy, BrokenAt: -1,
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Ingress", Name: "ing"}, Edge: "entry:Ingress", Probes: []probe.Result{{Layer: probe.LayerHTTP, OK: true, Tone: probe.ToneDegraded}}},
			{Resource: ResourceRef{Kind: "Service", Name: "echo"}, Edge: "Ingress->Service", Probes: []probe.Result{{Layer: probe.LayerHTTP, Path: probe.PathAPIServer, OK: true, Tone: probe.ToneHealthy}}},
		},
	}
	if v, _ := reviseVerdictWithProbes(tr); v != VerdictDegraded {
		t.Errorf("degraded entry + healthy backend: verdict=%q, want degraded", v)
	}
}

// TestReproducerForRef_PodTargetsTheSpecificPod pins the state-blind fallback: a
// pod-level finding must point at the specific pod (describe pod <name>), not the
// Pods collection's "get pods" or a Service's "describe service". The fallback is
// a single clean command - podDiagnosis overrides it with the state-true variant
// (e.g. logs --previous) when the live container state is known.
func TestReproducerForRef_PodTargetsTheSpecificPod(t *testing.T) {
	cmd := reproducerForRef(ResourceRef{Kind: "Pod", Namespace: "prod", Name: "web-abc123"})
	if cmd != "kubectl describe pod web-abc123 -n prod" {
		t.Errorf("pod reproducer = %q, want the clean state-blind describe of the specific pod", cmd)
	}
	if strings.Contains(cmd, "#") {
		t.Errorf("pod reproducer must be a single runnable command, not a comment-jammed double, got %q", cmd)
	}
}

// TestPodDiagnosis_StateTrueCommand pins the per-state pod command + cause: a
// crash loop gets a clean `logs --previous` (the prior container's crash output);
// a never-started pod (ImagePull / Pending) gets `describe` and NEVER --previous
// (there is no previous instance to read). Every string is a single runnable line.
func TestPodDiagnosis_StateTrueCommand(t *testing.T) {
	waiting := func(reason string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "prod"},
			Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{
				{Name: "app", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: reason}}},
			}},
		}
	}
	crash := waiting("CrashLoopBackOff")
	cause, cmd := podDiagnosis(crash)
	if cmd != "kubectl logs p -n prod --previous" {
		t.Errorf("crashloop command = %q, want a clean logs --previous", cmd)
	}
	if !strings.Contains(cause, "CrashLoopBackOff") {
		t.Errorf("crashloop cause = %q, want it to name CrashLoopBackOff", cause)
	}
	for _, reason := range []string{"ImagePullBackOff", "ErrImagePull"} {
		c, cmd := podDiagnosis(waiting(reason))
		if strings.Contains(cmd, "--previous") {
			t.Errorf("%s command = %q, must NOT use --previous (container never started)", reason, cmd)
		}
		if cmd != "kubectl describe pod p -n prod" {
			t.Errorf("%s command = %q, want describe", reason, cmd)
		}
		if !strings.Contains(c, "image") {
			t.Errorf("%s cause = %q, want it to mention the image", reason, c)
		}
	}
	pending := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Namespace: "prod"}, Status: corev1.PodStatus{Phase: corev1.PodPending}}
	if c, cmd := podDiagnosis(pending); strings.Contains(cmd, "--previous") || !strings.Contains(c, "Pending") {
		t.Errorf("pending diagnosis = (%q,%q), want describe + a Pending cause", c, cmd)
	}
}

// b2ReadyPod builds a ready pod declaring the given container ports.
func b2ReadyPod(name string, ports ...int32) *corev1.Pod {
	cps := make([]corev1.ContainerPort, len(ports))
	for i, p := range ports {
		cps[i] = corev1.ContainerPort{ContainerPort: p}
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "prod"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Ports: cps}}},
		Status: corev1.PodStatus{Conditions: []corev1.PodCondition{
			{Type: corev1.PodReady, Status: corev1.ConditionTrue, LastTransitionTime: metav1.NewTime(time.Now())},
		}},
	}
}

func b2Service(port, targetPort int32) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "prod"},
		Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{
			{Port: port, TargetPort: intstr.FromInt32(targetPort)},
		}},
	}
}

// TestTargetPortAdvisory_FiresOnPositiveEvidence: pods declare a port, none
// matches the numeric targetPort -> advisory warning on the Service hop.
func TestTargetPortAdvisory_FiresOnPositiveEvidence(t *testing.T) {
	f, suspects, ok := targetPortAdvisory(b2Service(80, 9999), []*corev1.Pod{b2ReadyPod("p", 8080)})
	if !ok {
		t.Fatal("expected advisory to fire when pods declare :8080 but Service targets :9999")
	}
	if len(suspects) != 1 || suspects[0] != 80 {
		t.Errorf("suspect Service ports = %v, want [80] (so the probe reconcile can clear it)", suspects)
	}
	if f.Severity != SeverityWarning {
		t.Errorf("severity = %q, want warning (never broken - containerPort is informational)", f.Severity)
	}
	if f.Code != "svc:targetport-no-listener" {
		t.Errorf("code = %q", f.Code)
	}
	if !strings.Contains(f.Message, "9999") || !strings.Contains(f.Message, ":8080") {
		t.Errorf("message should name both the target and the declared ports, got %q", f.Message)
	}
	if !strings.Contains(f.Command, "kubectl get svc api -n prod -o yaml") {
		t.Errorf("command should be the Service yaml reproducer, got %q", f.Command)
	}
}

// TestTargetPortAdvisory_SilentWhenNoPortsDeclared: pods declare NO container
// ports (the common case - ports are optional) -> no advisory, no false degrade.
func TestTargetPortAdvisory_SilentWhenNoPortsDeclared(t *testing.T) {
	if _, _, ok := targetPortAdvisory(b2Service(80, 9999), []*corev1.Pod{b2ReadyPod("p")}); ok {
		t.Error("must not fire when pods declare no container ports - a mismatch means nothing there")
	}
}

// TestTargetPortAdvisory_SilentWhenMatched: targetPort matches a declared
// container port -> healthy, no advisory.
func TestTargetPortAdvisory_SilentWhenMatched(t *testing.T) {
	if _, _, ok := targetPortAdvisory(b2Service(80, 8080), []*corev1.Pod{b2ReadyPod("p", 8080)}); ok {
		t.Error("must not fire when the targetPort matches a declared container port")
	}
}

// TestTargetPortAdvisory_DegradesNotBreaks: the warning degrades the verdict,
// never broken (we can't see container-internal listeners).
func TestTargetPortAdvisory_DegradesNotBreaks(t *testing.T) {
	adv, _, ok := targetPortAdvisory(b2Service(80, 9999), []*corev1.Pod{b2ReadyPod("p", 8080)})
	if !ok {
		t.Fatal("setup: advisory should fire")
	}
	tr := &Trace{Downstream: []Hop{
		{Resource: ResourceRef{Kind: "Service", Name: "api"}, Edge: "entry:Service", Findings: []Finding{adv}},
		{Resource: ResourceRef{Kind: "Pods"}, Edge: "Service->Pods"},
	}}
	if v, _ := computeVerdict(tr); v != VerdictDegraded {
		t.Errorf("targetPort advisory should degrade, got %q", v)
	}
}

// httpRouteWithBackend builds an HTTPRoute whose single rule references one
// backend Service, optionally in another namespace (a cross-namespace
// backendRef). Used by the CR5 redaction tests.
func httpRouteWithBackend(name, ns, backendName, backendNS string) *unstructured.Unstructured {
	backend := map[string]any{"name": backendName}
	if backendNS != "" {
		backend["namespace"] = backendNS
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "HTTPRoute",
		"metadata":   map[string]any{"name": name, "namespace": ns},
		"spec": map[string]any{
			"rules": []any{map[string]any{"backendRefs": []any{backend}}},
		},
	}}
}

// assertRedactedAggregate fails unless hop is the ANONYMOUS aggregate for
// out-of-scope matches: no name, no namespace, endpointSource:"unknown",
// exactly the single rbac finding (no leaked real findings), and the
// out-of-scope count phrase in its message. Shared by the four per-site
// redaction tests.
func assertRedactedAggregate(t *testing.T, hop *Hop, wantCountPhrase string) {
	t.Helper()
	if hop == nil {
		t.Fatal("expected an anonymous aggregate hop for out-of-scope matches, got none")
	}
	if hop.Resource.Name != "" || hop.Resource.Namespace != "" {
		t.Errorf("aggregate hop must carry no identity, got %+v", hop.Resource)
	}
	if got := hop.Meta["endpointSource"]; got != "unknown" {
		t.Errorf("redacted hop endpointSource = %v, want \"unknown\"", got)
	}
	if len(hop.Findings) != 1 || hop.Findings[0].Code != "rbac:cross-namespace-redacted" {
		t.Fatalf("redacted hop should carry exactly the redaction finding, got %+v", hop.Findings)
	}
	if !strings.Contains(hop.Findings[0].Message, wantCountPhrase) {
		t.Errorf("aggregate message = %q, want the count phrase %q", hop.Findings[0].Message, wantCountPhrase)
	}
}

// assertNoIdentityLeak marshals the whole trace and asserts the hidden
// identifiers appear nowhere in the wire output. The serialized form is what
// the caller actually receives, so a redaction that leaks through any other
// field (Message, Command, Meta, Config) still fails here.
func assertNoIdentityLeak(t *testing.T, tr *Trace, hidden ...string) {
	t.Helper()
	raw, err := json.Marshal(tr)
	if err != nil {
		t.Fatalf("marshal trace: %v", err)
	}
	for _, s := range hidden {
		if strings.Contains(string(raw), s) {
			t.Errorf("serialized trace leaks %q", s)
		}
	}
}

// findRedactedHop returns the hop carrying the cross-namespace redaction
// finding, if any.
func findRedactedHop(hops []Hop) *Hop {
	for i := range hops {
		if findingCodePresent(hops[i].Findings, "rbac:cross-namespace-redacted") {
			return &hops[i]
		}
	}
	return nil
}

func cr5Setup(t *testing.T, typed *fake.Clientset, objs ...runtime.Object) Deps {
	t.Helper()
	if err := k8s.InitTestResourceCache(typed); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	routeGVR := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}
	gwGVR := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"}
	dynClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gwGVR: "GatewayList", routeGVR: "HTTPRouteList"},
		objs...,
	)
	if err := k8s.InitTestDynamicResourceCache(dynClient, []k8s.APIResource{
		{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "Gateway", Name: "gateways", Namespaced: true, Verbs: []string{"list", "watch"}},
		{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute", Name: "httproutes", Namespaced: true, Verbs: []string{"list", "watch"}},
	}); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	dynCache := k8s.GetDynamicResourceCache()
	for _, gvr := range []schema.GroupVersionResource{gwGVR, routeGVR} {
		if err := dynCache.EnsureWatching(gvr); err != nil {
			t.Fatalf("EnsureWatching %s: %v", gvr.Resource, err)
		}
		if !dynCache.WaitForSync(gvr, 2*time.Second) {
			t.Fatalf("dynamic cache did not sync %s", gvr.Resource)
		}
	}
	return Deps{Cache: k8s.GetResourceCache(), Dynamic: dynCache, Discovery: k8s.GetResourceDiscovery(), Issues: issues.NewCacheProvider()}
}

// TestRouteUpstreamsForService_RedactsCrossNamespace covers the Service-entry
// upstream fan-out (entries.go routeUpstreamsForService): a Route in a
// namespace the caller can't read that references this Service must appear
// only as the anonymous aggregate - its name and namespace must not reach the
// wire anywhere.
func TestRouteUpstreamsForService_RedactsCrossNamespaceRoutes(t *testing.T) {
	defer k8s.ResetTestState()
	defer k8s.ResetTestDynamicState()

	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "team-a"}, Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}}}
	deps := cr5Setup(t, fake.NewClientset(svc),
		httpRouteWithBackend("route-x", "team-x", "api", "team-a"), // cross-ns reference into team-a
	)
	deps.AllowedNamespaces = []string{"team-a"} // team-x out of scope

	tr, err := BuildTraceWithOptions(context.Background(), deps, "Service", "team-a", "api", Options{})
	if err != nil {
		t.Fatalf("BuildTraceWithOptions: %v", err)
	}
	assertRedactedAggregate(t, findRedactedHop(tr.Upstreams), "1 HTTPRoute")
	assertNoIdentityLeak(t, tr, "route-x", "team-x")
	// The synced cluster-wide test cache is authoritative - no incomplete-index
	// marker on the entry hop.
	if findingCodePresent(tr.Downstream[0].Findings, "trace:upstreams-incomplete") {
		t.Errorf("synced route index must not flag the upstream walk incomplete, got %+v", tr.Downstream[0].Findings)
	}
}

// TestTraceRouteEntry_RedactsCrossNamespaceBackend covers the Route-entry
// backendRef fan-out (entries.go traceRouteEntry): a backend Service in a
// namespace the caller can't read appears only as the anonymous aggregate hop.
// (The route's own spec - which the caller can read - still names the
// backendRef in the entry hop's Config; the redaction is about not presenting
// the out-of-scope Service as an identified hop with state of its own.)
func TestTraceRouteEntry_RedactsCrossNamespaceBackend(t *testing.T) {
	defer k8s.ResetTestState()
	defer k8s.ResetTestDynamicState()

	deps := cr5Setup(t, fake.NewClientset(),
		httpRouteWithBackend("route-a", "team-a", "svc-b", "team-b"), // backend lives in team-b
	)
	deps.AllowedNamespaces = []string{"team-a"} // team-b out of scope

	tr, err := BuildTraceWithOptions(context.Background(), deps, "HTTPRoute", "team-a", "route-a", Options{})
	if err != nil {
		t.Fatalf("BuildTraceWithOptions: %v", err)
	}
	redacted := findRedactedHop(tr.Downstream)
	assertRedactedAggregate(t, redacted, "1 backend Service")
	// The aggregate hop itself must not carry the backend's identity in any
	// field. (A whole-trace leak check doesn't apply at this site: the entry
	// hop's Config honestly echoes the route's own caller-readable spec,
	// which names the backendRef.)
	if redacted != nil {
		raw, err := json.Marshal(redacted)
		if err != nil {
			t.Fatalf("marshal redacted hop: %v", err)
		}
		for _, s := range []string{"svc-b", "team-b"} {
			if strings.Contains(string(raw), s) {
				t.Errorf("serialized redacted hop leaks %q", s)
			}
		}
	}
}

// TestRouteParentGateways_RedactsCrossNamespace covers the Route-entry parent
// fan-out (entries.go routeParentGateways): a parent Gateway in a namespace
// the caller can't read appears only as the anonymous aggregate - its name
// and namespace must not reach the wire anywhere.
func TestRouteParentGateways_RedactsCrossNamespaceGateway(t *testing.T) {
	defer k8s.ResetTestState()
	defer k8s.ResetTestDynamicState()

	deps := cr5Setup(t, fake.NewClientset(),
		attachedHTTPRoute("route-a", "team-a", "shared-gw", "gateway-system"), // parent gateway elsewhere
	)
	deps.AllowedNamespaces = []string{"team-a"} // gateway-system out of scope

	tr, err := BuildTraceWithOptions(context.Background(), deps, "HTTPRoute", "team-a", "route-a", Options{})
	if err != nil {
		t.Fatalf("BuildTraceWithOptions: %v", err)
	}
	assertRedactedAggregate(t, findRedactedHop(tr.Upstreams), "1 parent Gateway")
	assertNoIdentityLeak(t, tr, "shared-gw", "gateway-system")
}

// TestServiceUpstreams_UnreadableRouteIndexFlagsIncomplete: when the route
// kinds exist in discovery but the dynamic cache can't serve them, "no
// upstream routes found" must NOT read as "nothing references this Service" -
// the entry hop carries an explicit incomplete-enumeration marker.
func TestServiceUpstreams_UnreadableRouteIndexFlagsIncomplete(t *testing.T) {
	defer k8s.ResetTestState()
	defer k8s.ResetTestDynamicState()

	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "team-a"}, Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}}}
	deps := cr5Setup(t, fake.NewClientset(svc),
		httpRouteWithBackend("route-x", "team-x", "api", "team-a"),
	)
	deps.Dynamic = nil // HTTPRoute is discovered but its index is unreadable

	tr, err := BuildTraceWithOptions(context.Background(), deps, "Service", "team-a", "api", Options{})
	if err != nil {
		t.Fatalf("BuildTraceWithOptions: %v", err)
	}
	if len(tr.Upstreams) != 0 {
		t.Errorf("unreadable index should yield no route upstreams, got %+v", tr.Upstreams)
	}
	if !findingCodePresent(tr.Downstream[0].Findings, "trace:upstreams-incomplete") {
		t.Errorf("entry hop must flag the upstream enumeration as incomplete, got %+v", tr.Downstream[0].Findings)
	}
}

// TestAttachedRoutes_UnreadableIndexNotComplete pins the same absence-honesty
// on the Gateway inventory: a discovered-but-unreadable route kind means the
// enumeration is NOT authoritative.
func TestAttachedRoutes_UnreadableIndexNotComplete(t *testing.T) {
	defer k8s.ResetTestState()
	defer k8s.ResetTestDynamicState()

	deps := cr5Setup(t, fake.NewClientset())
	deps.Dynamic = nil

	refs, total, complete := attachedRoutes(deps, gatewayObject("gw", "ns"))
	if len(refs) != 0 || total != 0 {
		t.Errorf("unreadable index should list nothing, got refs=%v total=%d", refs, total)
	}
	if complete {
		t.Error("a discovered-but-unreadable route kind must not claim a complete inventory")
	}
}

// TestTraceGatewayEntry_ListsL4RoutesStaticOnly: TCPRoute/TLSRoute attachments
// appear in the Gateway inventory (so it never claims HTTP-only completeness)
// with an explicit not-traced note - static presence, no reachability claim.
func TestTraceGatewayEntry_ListsL4RoutesStaticOnly(t *testing.T) {
	defer k8s.ResetTestState()
	defer k8s.ResetTestDynamicState()

	if err := k8s.InitTestResourceCache(fake.NewClientset()); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	gwGVR := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"}
	httpGVR := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "httproutes"}
	tcpGVR := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1alpha2", Resource: "tcproutes"}
	dynClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{
			gwGVR:   "GatewayList",
			httpGVR: "HTTPRouteList",
			tcpGVR:  "TCPRouteList",
		},
		attachedHTTPRoute("web", "ns", "gw", "ns"),
		attachedL4Route("TCPRoute", "db-route", "ns", "gw", "ns"),
	)
	if err := dynClient.Tracker().Create(gwGVR, gatewayObject("gw", "ns"), "ns"); err != nil {
		t.Fatalf("tracker create gateway: %v", err)
	}
	if err := k8s.InitTestDynamicResourceCache(dynClient, []k8s.APIResource{
		{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "Gateway", Name: "gateways", Namespaced: true, Verbs: []string{"list", "watch"}},
		{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "HTTPRoute", Name: "httproutes", Namespaced: true, Verbs: []string{"list", "watch"}},
		{Group: "gateway.networking.k8s.io", Version: "v1alpha2", Kind: "TCPRoute", Name: "tcproutes", Namespaced: true, Verbs: []string{"list", "watch"}},
	}); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	dynCache := k8s.GetDynamicResourceCache()
	for _, gvr := range []schema.GroupVersionResource{gwGVR, httpGVR, tcpGVR} {
		if err := dynCache.EnsureWatching(gvr); err != nil {
			t.Fatalf("EnsureWatching %s: %v", gvr.Resource, err)
		}
		if !dynCache.WaitForSync(gvr, 2*time.Second) {
			t.Fatalf("dynamic cache did not sync %s", gvr.Resource)
		}
	}

	deps := Deps{Cache: k8s.GetResourceCache(), Dynamic: dynCache, Discovery: k8s.GetResourceDiscovery(), Issues: issues.NewCacheProvider()}
	tr, err := BuildTraceWithOptions(context.Background(), deps, "Gateway", "ns", "gw", Options{})
	if err != nil {
		t.Fatalf("BuildTraceWithOptions: %v", err)
	}

	var l4, http *Hop
	for i := range tr.Downstream {
		switch tr.Downstream[i].Resource.Kind {
		case "TCPRoute":
			l4 = &tr.Downstream[i]
		case "HTTPRoute":
			http = &tr.Downstream[i]
		}
	}
	if http == nil {
		t.Fatalf("expected the HTTPRoute hop, got %+v", tr.Downstream)
	}
	if l4 == nil {
		t.Fatalf("TCPRoute attachment missing from the Gateway inventory - the list claims false completeness; got %+v", tr.Downstream)
	}
	if l4.Resource.Name != "db-route" {
		t.Errorf("l4 hop name = %q, want db-route", l4.Resource.Name)
	}
	f := findingByCode(l4.Findings, "gateway:l4-route-not-traced")
	if f == nil {
		t.Fatalf("l4 hop must carry the not-traced note, got %+v", l4.Findings)
	}
	if f.Severity != SeverityInfo {
		t.Errorf("l4 not-traced note severity = %q, want info (presence is not a problem)", f.Severity)
	}
	if findingCodePresent(http.Findings, "gateway:l4-route-not-traced") {
		t.Errorf("HTTPRoute hop must not carry the l4 note, got %+v", http.Findings)
	}
}

func findingByCode(findings []Finding, code string) *Finding {
	for i := range findings {
		if findings[i].Code == code {
			return &findings[i]
		}
	}
	return nil
}

// attachedL4Route builds a TCPRoute/TLSRoute (v1alpha2) attached to a Gateway.
func attachedL4Route(kind, name, namespace, gwName, gwNamespace string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1alpha2",
		"kind":       kind,
		"metadata":   map[string]any{"name": name, "namespace": namespace},
		"spec": map[string]any{
			"parentRefs": []any{
				map[string]any{"name": gwName, "namespace": gwNamespace},
			},
		},
	}}
}

// A DOWN drained (weight-0) backend must not drag the verdict to
// broken/degraded - it carries zero traffic by design.
func TestComputeVerdict_DrainedBackendExcluded(t *testing.T) {
	tr := &Trace{
		Subject: ResourceRef{Group: "gateway.networking.k8s.io", Kind: "HTTPRoute", Namespace: "prod", Name: "r"},
		Verdict: VerdictHealthy,
		Downstream: []Hop{
			{Resource: ResourceRef{Group: "gateway.networking.k8s.io", Kind: "HTTPRoute", Namespace: "prod", Name: "r"}, Edge: "entry:HTTPRoute"},
			{Resource: ResourceRef{Kind: "Service", Namespace: "prod", Name: "stable"}, Edge: "HTTPRoute->Service"},
			{Resource: ResourceRef{Kind: "Pods", Namespace: "prod"}, Edge: "Service->Pods"},
			{Resource: ResourceRef{Kind: "Service", Namespace: "prod", Name: "canary"}, Edge: "HTTPRoute->Service",
				Meta:     map[string]any{"drained": true},
				Findings: []Finding{{Code: "route:drained-weight-zero", Severity: SeverityInfo}}},
			{Resource: ResourceRef{Kind: "Pods", Namespace: "prod"}, Edge: "Service->Pods",
				Findings: []Finding{{Code: "svc:no-ready-endpoints", Severity: SeverityCritical, Message: "0/0 ready"}}},
		},
	}
	if v, _ := computeVerdict(tr); v != VerdictHealthy {
		t.Errorf("verdict = %q, want healthy - a down DRAINED backend must not condemn the path", v)
	}
}

// An upstream missing_ref critical (about a sibling backend route)
// must NOT block the broken→degraded benign softening for a scale-to-0 here.
func TestHasNonBenignCriticalFinding_ScopesUpstreamMissingRef(t *testing.T) {
	tr := &Trace{
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Service", Namespace: "prod", Name: "api"}, Edge: "entry:Service"},
		},
		Upstreams: []Hop{
			{Resource: ResourceRef{Kind: "Ingress", Namespace: "prod", Name: "shop"}, Edge: "entry:Ingress",
				Findings: []Finding{{Code: "missing_ref:ghost", Severity: SeverityCritical, Message: "/other references ghost"}}},
		},
	}
	if hasNonBenignCriticalFinding(tr) {
		t.Error("upstream missing_ref critical must be scoped out (sibling route), not block softening")
	}
	// A downstream critical IS scanned unconditionally.
	tr.Downstream[0].Findings = []Finding{{Code: "svc:no-controller", Severity: SeverityCritical}}
	if !hasNonBenignCriticalFinding(tr) {
		t.Error("a downstream critical must still register")
	}
}

// TestIngressUpstreamsForService_UnavailableCacheNotAuthoritative (S10): when the
// Ingress lister can't be read, an empty enumeration must NOT read as "no Ingress
// references this Service" - the walk reports itself non-authoritative and
// serviceUpstreams folds that into the completeness signal (mirrors the route
// reverse-walk's cluster-wide-synced check).
func TestIngressUpstreamsForService_UnavailableCacheNotAuthoritative(t *testing.T) {
	svc := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "team-a"}}
	hops, authoritative := ingressUpstreamsForService(Deps{}, svc)
	if len(hops) != 0 {
		t.Errorf("unavailable cache should yield no ingress upstreams, got %+v", hops)
	}
	if authoritative {
		t.Error("unavailable Ingress cache must not claim an authoritative enumeration")
	}
	if _, complete := serviceUpstreams(Deps{}, svc); complete {
		t.Error("serviceUpstreams must report incomplete when the Ingress walk isn't authoritative")
	}
}

// TestIngressUpstreamsForService_SyncedCacheAuthoritative (S10): a synced,
// namespace-covering cache backs the claim "these are ALL the Ingresses" - the
// referencing Ingress surfaces AND the walk reports authoritative, so no false
// incomplete marker is raised.
func TestIngressUpstreamsForService_SyncedCacheAuthoritative(t *testing.T) {
	defer k8s.ResetTestState()
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "team-a"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}},
	}
	ing := &networkingv1.Ingress{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "team-a"},
		Spec: networkingv1.IngressSpec{
			Rules: []networkingv1.IngressRule{{
				IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{
					Paths: []networkingv1.HTTPIngressPath{{
						Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "api"}},
					}},
				}},
			}},
		},
	}
	if err := k8s.InitTestResourceCache(fake.NewClientset(svc, ing)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	deps := Deps{Cache: k8s.GetResourceCache(), Issues: issues.NewCacheProvider()}
	hops, authoritative := ingressUpstreamsForService(deps, svc)
	if !authoritative {
		t.Error("a synced cluster-wide cache must be authoritative for the Ingress walk")
	}
	if len(hops) != 1 || hops[0].Resource.Name != "web" {
		t.Fatalf("want the referencing Ingress hop, got %+v", hops)
	}
}

// TestBuildTrace_GatewaySubjectNilDiscoveryDegrades (nil-Discovery): a Gateway
// trace when Discovery failed to init must degrade honestly to Unknown, never
// nil-deref panic on deps.Discovery.GetGVRWithGroup.
func TestBuildTrace_GatewaySubjectNilDiscoveryDegrades(t *testing.T) {
	defer k8s.ResetTestState()
	if err := k8s.InitTestResourceCache(fake.NewClientset()); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	deps := Deps{Cache: k8s.GetResourceCache(), Issues: issues.NewCacheProvider()} // Discovery + Dynamic nil
	tr, err := BuildTraceWithOptions(context.Background(), deps, "Gateway", "ns", "gw", Options{})
	if err != nil {
		t.Fatalf("BuildTraceWithOptions: %v", err)
	}
	if tr.Verdict != VerdictUnknown {
		t.Errorf("nil Discovery on a Gateway trace must degrade to Unknown, got %q (%s)", tr.Verdict, tr.Reason)
	}
}

// TestBuildTrace_ServiceSubjectNilDiscoveryNoPanic (nil-Discovery): the Service
// reverse-walk touches Discovery for the route index. With Discovery nil it must
// not panic and must flag the upstream enumeration incomplete rather than assert
// "nothing references this Service".
func TestBuildTrace_ServiceSubjectNilDiscoveryNoPanic(t *testing.T) {
	defer k8s.ResetTestState()
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "ns"},
		Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}, Selector: map[string]string{"app": "api"}},
	}
	if err := k8s.InitTestResourceCache(fake.NewClientset(svc)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	deps := Deps{Cache: k8s.GetResourceCache(), Issues: issues.NewCacheProvider()} // Discovery nil
	tr, err := BuildTraceWithOptions(context.Background(), deps, "Service", "ns", "api", Options{})
	if err != nil {
		t.Fatalf("BuildTraceWithOptions: %v", err)
	}
	if len(tr.Downstream) == 0 {
		t.Fatal("expected a downstream service hop")
	}
	if !findingCodePresent(tr.Downstream[0].Findings, "trace:upstreams-incomplete") {
		t.Errorf("nil Discovery must flag upstreams incomplete, got %+v", tr.Downstream[0].Findings)
	}
}
