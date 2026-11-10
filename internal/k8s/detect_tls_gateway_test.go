package k8s

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/issuesapi"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

type gatewayOpt func(*unstructured.Unstructured)

func notProgrammed(gw *unstructured.Unstructured) {
	conds, _, _ := unstructured.NestedSlice(gw.Object, "status", "conditions")
	conds[0].(map[string]any)["status"] = "False"
	_ = unstructured.SetNestedSlice(gw.Object, conds, "status", "conditions")
}

func staleProgrammed(gw *unstructured.Unstructured) {
	gw.SetGeneration(3)
}

func noAddress(gw *unstructured.Unstructured) {
	unstructured.RemoveNestedField(gw.Object, "status", "addresses")
}

// testGateway is a Gateway at generation 2 that is Programmed for it, has an
// address, and has one listener named "https" with the given protocol, TLS
// mode, certificate refs and attached routes.
func testGateway(ns, name, protocol, mode string, attached int64, certRefs []any, opts ...gatewayOpt) *unstructured.Unstructured {
	tls := map[string]any{"certificateRefs": certRefs}
	if mode != "" {
		tls["mode"] = mode
	}
	gw := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "Gateway",
		"metadata":   map[string]any{"name": name, "namespace": ns, "generation": int64(2)},
		"spec": map[string]any{
			"gatewayClassName": "istio",
			"listeners":        []any{map[string]any{"name": "https", "protocol": protocol, "port": int64(443), "tls": tls}},
		},
		"status": map[string]any{
			"addresses":  []any{map[string]any{"type": "Hostname", "value": name + "." + ns + ".lb.example.test"}},
			"conditions": []any{map[string]any{"type": "Programmed", "status": "True", "observedGeneration": int64(2)}},
			"listeners": []any{map[string]any{
				"name": "https", "attachedRoutes": attached,
				"conditions": []any{map[string]any{"type": "Programmed", "status": "True", "observedGeneration": int64(2)}},
			}},
		},
	}}
	for _, opt := range opts {
		opt(gw)
	}
	return gw
}

func listenerCondition(condType, status string) gatewayOpt {
	return func(gw *unstructured.Unstructured) {
		listeners, _, _ := unstructured.NestedSlice(gw.Object, "status", "listeners")
		l := listeners[0].(map[string]any)
		l["conditions"] = append(l["conditions"].([]any), map[string]any{"type": condType, "status": status})
		_ = unstructured.SetNestedSlice(gw.Object, listeners, "status", "listeners")
	}
}

func gatewayClass(name, controller string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1",
		"kind":       "GatewayClass",
		"metadata":   map[string]any{"name": name},
		"spec":       map[string]any{"controllerName": controller},
	}}
}

func withGatewayClass(name string) gatewayOpt {
	return func(gw *unstructured.Unstructured) {
		_ = unstructured.SetNestedField(gw.Object, name, "spec", "gatewayClassName")
	}
}

func gatewayAddress(value string) gatewayOpt {
	return func(gw *unstructured.Unstructured) {
		_ = unstructured.SetNestedSlice(gw.Object, []any{map[string]any{"type": "IPAddress", "value": value}}, "status", "addresses")
	}
}

func listenerHostname(host string) gatewayOpt {
	return func(gw *unstructured.Unstructured) {
		listeners, _, _ := unstructured.NestedSlice(gw.Object, "spec", "listeners")
		listeners[0].(map[string]any)["hostname"] = host
		_ = unstructured.SetNestedSlice(gw.Object, listeners, "spec", "listeners")
	}
}

func listenerTLSOption(key, value string) gatewayOpt {
	return func(gw *unstructured.Unstructured) {
		listeners, _, _ := unstructured.NestedSlice(gw.Object, "spec", "listeners")
		l := listeners[0].(map[string]any)
		l["tls"].(map[string]any)["options"] = map[string]any{key: value}
		_ = unstructured.SetNestedSlice(gw.Object, listeners, "spec", "listeners")
	}
}

func listenerProgrammedStale(gw *unstructured.Unstructured) {
	listeners, _, _ := unstructured.NestedSlice(gw.Object, "status", "listeners")
	l := listeners[0].(map[string]any)
	l["conditions"] = []any{map[string]any{"type": "Programmed", "status": "True", "observedGeneration": int64(1)}}
	_ = unstructured.SetNestedSlice(gw.Object, listeners, "status", "listeners")
}

func secretRef(name, ns string) map[string]any {
	ref := map[string]any{"name": name}
	if ns != "" {
		ref["namespace"] = ns
	}
	return ref
}

func secretGrant(ns, fromNamespace, secret string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "gateway.networking.k8s.io/v1beta1",
		"kind":       "ReferenceGrant",
		"metadata":   map[string]any{"name": "allow-" + fromNamespace + "-" + secret, "namespace": ns},
		"spec": map[string]any{
			"from": []any{map[string]any{"group": "gateway.networking.k8s.io", "kind": "Gateway", "namespace": fromNamespace}},
			"to":   []any{map[string]any{"group": "", "kind": "Secret", "name": secret}},
		},
	}}
}

func TestDetectLiveTLSCertificatesGatewayListeners(t *testing.T) {
	defer ResetTestState()
	defer ResetTestDynamicState()

	now := time.Now()
	expired := testCertPEM(t, now.Add(-90*24*time.Hour), now.Add(-time.Hour))
	names := []string{"live", "passthrough", "unprogrammed", "stale", "unaddressed", "no-routes", "http", "ungranted", "granted", "foreign-kind", "rejected", "conflicted", "unresolved", "listener-stale", "multi-a", "multi-b", "no-generation", "certmap", "preshared", "acm", "aws", "unknown-class", "sni-a", "sni-same"}
	var objs []runtime.Object
	for _, n := range names {
		objs = append(objs, tlsSecret("shop", n+"-tls", expired))
	}
	objs = append(objs, tlsSecret("certs", "shared-tls", expired), tlsSecret("certs", "private-tls", expired),
		tlsSecret("shop", "sni-b-tls", testCertPEM(t, now.Add(-time.Hour), now.Add(90*24*time.Hour), "api.example.test")),
		tlsSecret("certs", "both-tls", testCertPEM(t, now.Add(-90*24*time.Hour), now.Add(-time.Hour), "both.example.test")),
		// An addressed Ingress in the Secret's own namespace makes both-tls
		// critical without the remote Gateway's evidence.
		&networkingv1.IngressClass{ObjectMeta: metav1.ObjectMeta{Name: "custom"}, Spec: networkingv1.IngressClassSpec{Controller: "example.com/custom"}},
		tlsIngress("certs", "both", []string{"both-tls"}, withClass("custom"), withAddress))
	if err := InitTestResourceCache(fake.NewClientset(objs...)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}

	gatewayGVR := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gateways"}
	classGVR := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gatewayclasses"}
	grantGVR := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1beta1", Resource: "referencegrants"}
	dynClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gatewayGVR: "GatewayList", grantGVR: "ReferenceGrantList", classGVR: "GatewayClassList"},
		secretGrant("certs", "edge", "shared-tls"), secretGrant("certs", "edge", "both-tls"))
	for _, gc := range []*unstructured.Unstructured{
		gatewayClass("istio", "istio.io/gateway-controller"),
		gatewayClass("kong", "konghq.com/kic-gateway-controller"),
		gatewayClass("aws", "gateway.k8s.aws/alb"),
	} {
		if _, err := dynClient.Resource(classGVR).Create(context.Background(), gc, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create GatewayClass %s: %v", gc.GetName(), err)
		}
	}
	// The fake client guesses the resource for objects passed at construction
	// from the kind ("Gateway" -> "gatewaies"), so Gateways are created instead.
	for _, gw := range []*unstructured.Unstructured{
		testGateway("shop", "live", "HTTPS", "", 1, []any{secretRef("live-tls", "")}),
		testGateway("shop", "passthrough", "TLS", "Passthrough", 1, []any{secretRef("passthrough-tls", "")}),
		testGateway("shop", "unprogrammed", "HTTPS", "", 1, []any{secretRef("unprogrammed-tls", "")}, notProgrammed),
		testGateway("shop", "stale", "HTTPS", "", 1, []any{secretRef("stale-tls", "")}, staleProgrammed),
		testGateway("shop", "unaddressed", "HTTPS", "", 1, []any{secretRef("unaddressed-tls", "")}, noAddress),
		testGateway("shop", "no-routes", "HTTPS", "", 0, []any{secretRef("no-routes-tls", "")}),
		testGateway("shop", "http", "HTTP", "", 1, []any{secretRef("http-tls", "")}),
		testGateway("shop", "rejected", "HTTPS", "", 1, []any{secretRef("rejected-tls", "")}, listenerCondition("Accepted", "False")),
		testGateway("shop", "conflicted", "HTTPS", "", 1, []any{secretRef("conflicted-tls", "")}, listenerCondition("Conflicted", "True")),
		testGateway("shop", "unresolved", "HTTPS", "", 1, []any{secretRef("unresolved-tls", "")}, listenerCondition("ResolvedRefs", "False")),
		// A condition that doesn't say which generation it describes proves nothing current.
		testGateway("shop", "no-generation", "HTTPS", "", 1, []any{secretRef("no-generation-tls", "")}, func(gw *unstructured.Unstructured) {
			_ = unstructured.SetNestedSlice(gw.Object, []any{map[string]any{"type": "Programmed", "status": "True"}}, "status", "conditions")
		}),
		testGateway("shop", "listener-stale", "HTTPS", "", 1, []any{secretRef("listener-stale-tls", "")}, listenerProgrammedStale),
		// The controller picks one of several certificates per client.
		testGateway("shop", "multi", "HTTPS", "", 1, []any{secretRef("multi-a-tls", ""), secretRef("multi-b-tls", "")}),
		// Certificates held outside the cluster may be what clients see instead.
		testGateway("shop", "certmap", "HTTPS", "", 1, []any{secretRef("certmap-tls", "")}, func(gw *unstructured.Unstructured) {
			gw.SetAnnotations(map[string]string{"networking.gke.io/certmap": "shop-map"})
		}),
		testGateway("shop", "preshared", "HTTPS", "", 1, []any{secretRef("preshared-tls", "")}, listenerTLSOption("networking.gke.io/pre-shared-certs", "shop-cert")),
		testGateway("shop", "acm", "HTTPS", "", 1, []any{secretRef("acm-tls", "")}, listenerTLSOption("application-networking.k8s.aws/certificate-arn", "arn:aws:acm:us-east-1:111122223333:certificate/x")),
		// AWS's controllers take certificates from ACM, and an unknown class
		// says nothing about how certificates are served.
		testGateway("shop", "aws", "HTTPS", "", 1, []any{secretRef("aws-tls", "")}, withGatewayClass("aws")),
		testGateway("shop", "unknown-class", "HTTPS", "", 1, []any{secretRef("unknown-class-tls", "")}, withGatewayClass("missing")),
		// A valid certificate the same controller serves for the name may be
		// the one presented, so the expired one for that name is left out;
		// one Secret two Gateways present for a name is one row.
		testGateway("shop", "sni-a", "HTTPS", "", 1, []any{secretRef("sni-a-tls", "")}, withGatewayClass("kong"), gatewayAddress("198.51.100.7"), listenerHostname("api.example.test")),
		testGateway("shop", "sni-b", "HTTPS", "", 1, []any{secretRef("sni-b-tls", "")}, withGatewayClass("kong"), gatewayAddress("198.51.100.7"), listenerHostname("API.example.test")),
		testGateway("shop", "sni-same-1", "HTTPS", "", 1, []any{secretRef("sni-same-tls", "")}, withGatewayClass("kong"), gatewayAddress("198.51.100.7"), listenerHostname("www.example.test")),
		testGateway("shop", "sni-same-2", "HTTPS", "", 1, []any{secretRef("sni-same-tls", "")}, withGatewayClass("kong"), gatewayAddress("198.51.100.7"), listenerHostname("www.example.test")),
		testGateway("shop", "foreign-kind", "HTTPS", "", 1, []any{map[string]any{"group": "example.com", "kind": "Vault", "name": "foreign-kind-tls"}}),
		testGateway("edge", "cross", "TLS", "Terminate", 2, []any{secretRef("shared-tls", "certs")}),
		testGateway("edge", "cross-both", "TLS", "Terminate", 2, []any{secretRef("both-tls", "certs")}),
		// No ReferenceGrant lets edge use private-tls.
		testGateway("edge", "cross-ungranted", "TLS", "Terminate", 2, []any{secretRef("private-tls", "certs")}),
	} {
		if _, err := dynClient.Resource(gatewayGVR).Namespace(gw.GetNamespace()).Create(context.Background(), gw, metav1.CreateOptions{}); err != nil {
			t.Fatalf("create Gateway %s: %v", gw.GetName(), err)
		}
	}
	if err := InitTestDynamicResourceCache(dynClient, []APIResource{
		{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "Gateway", Name: "gateways", Verbs: []string{"list", "watch"}},
		{Group: "gateway.networking.k8s.io", Version: "v1beta1", Kind: "ReferenceGrant", Name: "referencegrants", Verbs: []string{"list", "watch"}},
		{Group: "gateway.networking.k8s.io", Version: "v1", Kind: "GatewayClass", Name: "gatewayclasses", Verbs: []string{"list", "watch"}},
	}); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	dynCache := GetDynamicResourceCache()
	for _, gvr := range []schema.GroupVersionResource{gatewayGVR, grantGVR, classGVR} {
		if err := dynCache.EnsureWatching(gvr); err != nil {
			t.Fatalf("EnsureWatching %s: %v", gvr.Resource, err)
		}
		if !dynCache.WaitForSync(gvr, 2*time.Second) {
			t.Fatalf("%s did not sync", gvr.Resource)
		}
	}

	got := detectLiveTLSCertificates(GetResourceCache(), "", nil, now)
	if len(got) != 4 {
		t.Fatalf("detections = %+v, want certs/both-tls, certs/shared-tls, shop/live-tls and shop/sni-same-tls only", got)
	}
	both, shared, live := got[0], got[1], got[2]
	if got[3].Name != "sni-same-tls" || len(got[3].AffectedRefs) != 2 {
		t.Fatalf("same Secret on one SNI name = %+v, want one row linking both Gateways", got[3])
	}
	// The text names only what the row's own evidence shows; the remote
	// Gateway is a link, dropped for callers who can't read it.
	if both.Name != "both-tls" || len(both.RequiredReads) != 0 || len(both.AffectedRefs) != 2 || strings.Contains(both.Message, "Gateway") {
		t.Fatalf("Ingress plus remote Gateway detection = %+v", both)
	}
	if live.Namespace != "shop" || live.Name != "live-tls" || live.Severity != "critical" || len(live.RequiredReads) != 0 ||
		len(live.AffectedRefs) != 1 || live.AffectedRefs[0] != (issuesapi.Ref{Group: gatewayAPIGroup, Kind: "Gateway", Namespace: "shop", Name: "live"}) {
		t.Fatalf("same-namespace Gateway detection = %+v", live)
	}
	if !strings.Contains(live.Message, "Gateway listener") {
		t.Fatalf("message = %q", live.Message)
	}
	// The Secret's namespace can't see why the row is critical unless it can
	// read the Gateway's namespace.
	if shared.Namespace != "certs" || shared.Name != "shared-tls" || shared.Severity != "critical" ||
		len(shared.RequiredReads) != 1 || shared.RequiredReads[0] != (issuesapi.EvidenceRead{Group: gatewayAPIGroup, Resource: "gateways", Namespace: "edge", Verb: "list"}) {
		t.Fatalf("cross-namespace Gateway detection = %+v", shared)
	}

	scoped := detectLiveTLSCertificates(GetResourceCache(), "certs", nil, now)
	if len(scoped) != 2 || scoped[1].Name != "shared-tls" {
		t.Fatalf("namespace-scoped scan = %+v, want the Secret in certs used by a Gateway in edge", scoped)
	}
}

// Traefik serves Ingress and Gateway certificates from one store, so a
// valid one loaded through either can be what clients get for the other.
func TestGatewayCertificatePool(t *testing.T) {
	if got := gatewayCertificatePool(traefikGatewayController); got != traefikIngressController {
		t.Errorf("Traefik Gateway pool = %q, want the Traefik Ingress pool", got)
	}
	if got := gatewayCertificatePool("istio.io/gateway-controller"); got == "istio.io/gateway-controller" || got == traefikIngressController {
		t.Errorf("Istio Gateway pool = %q, want its own gateway pool", got)
	}
}
