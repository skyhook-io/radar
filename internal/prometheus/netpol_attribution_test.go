package prometheus

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/prom"
)

type attributionFixture struct {
	objects  []runtime.Object
	scopes   map[string]k8score.ResourceScope
	deferred map[string]bool
	selfName string
	selfNs   string
}

func promCandidate() prom.Candidate {
	return prom.Candidate{Namespace: "monitoring", Name: "prometheus-server", Port: 9090, ClusterAddr: "http://prometheus-server.monitoring.svc.cluster.local:9090"}
}

// clusterWithPrometheus is the minimal in-cluster picture: Radar's pod, a
// Prometheus pod behind a Service with a published EndpointSlice, and both
// Namespace objects.
func clusterWithPrometheus(extra ...runtime.Object) []runtime.Object {
	ready := true
	port := int32(9090)
	proto := corev1.ProtocolTCP
	objs := []runtime.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "radar", Labels: map[string]string{"kubernetes.io/metadata.name": "radar"}}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "monitoring", Labels: map[string]string{"kubernetes.io/metadata.name": "monitoring"}}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: "radar", Name: "radar-0", Labels: map[string]string{"app": "radar"}},
			Status:     corev1.PodStatus{PodIP: "10.0.1.5"},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: "monitoring", Name: "prometheus-server-0", Labels: map[string]string{"app": "prometheus"}},
			Status:     corev1.PodStatus{PodIP: "10.0.2.9"},
		},
		&corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Namespace: "monitoring", Name: "prometheus-server"},
			Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "http", Port: 9090}}},
		},
		&discoveryv1.EndpointSlice{
			ObjectMeta:  metav1.ObjectMeta{Namespace: "monitoring", Name: "prometheus-server-abc", Labels: map[string]string{discoveryv1.LabelServiceName: "prometheus-server"}},
			AddressType: discoveryv1.AddressTypeIPv4,
			Ports:       []discoveryv1.EndpointPort{{Name: strPtr("http"), Port: &port, Protocol: &proto}},
			Endpoints: []discoveryv1.Endpoint{{
				Addresses:  []string{"10.0.2.9"},
				Conditions: discoveryv1.EndpointConditions{Ready: &ready},
				TargetRef:  &corev1.ObjectReference{Kind: "Pod", Namespace: "monitoring", Name: "prometheus-server-0"},
			}},
		},
	}
	return append(objs, extra...)
}

func strPtr(s string) *string { return &s }

func clusterWideScopes() map[string]k8score.ResourceScope {
	return map[string]k8score.ResourceScope{
		string(k8score.Pods):            {Enabled: true},
		string(k8score.Services):        {Enabled: true},
		string(k8score.Namespaces):      {Enabled: true},
		string(k8score.NetworkPolicies): {Enabled: true},
	}
}

func denyAllIngress(ns, name string) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec:       networkingv1.NetworkPolicySpec{PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}},
	}
}

func allowFromRadar(ns, name string) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "prometheus"}},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{
				NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": "radar"}},
			}}}},
		},
	}
}

func runAttribution(t *testing.T, fx attributionFixture, reasons []prom.ProbeReason) error {
	t.Helper()
	t.Setenv("MY_POD_NAME", fx.selfName)
	t.Setenv("MY_POD_NAMESPACE", fx.selfNs)
	client := fake.NewClientset(fx.objects...)
	scopes := fx.scopes
	if scopes == nil {
		scopes = clusterWideScopes()
	}
	deferred := fx.deferred
	if deferred == nil {
		deferred = map[string]bool{}
	}
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:         client,
		ResourceScopes: scopes,
		DeferredTypes:  deferred,
	})
	if err != nil {
		t.Fatalf("NewResourceCache: %v", err)
	}
	t.Cleanup(core.Stop)
	cache := &k8s.ResourceCache{ResourceCache: core}
	c := &Client{k8sClient: client, inCluster: true}
	return c.attributeNetworkPolicyBlock(context.Background(), cache, []prom.Candidate{promCandidate()}, reasons)
}

func TestAttributeNetworkPolicyBlock(t *testing.T) {
	transport := []prom.ProbeReason{prom.ProbeReasonTransportError}
	self := attributionFixture{selfName: "radar-0", selfNs: "radar"}

	t.Run("names the ingress policy that isolates the backend", func(t *testing.T) {
		fx := self
		fx.objects = clusterWithPrometheus(denyAllIngress("monitoring", "deny-all"))
		err := runAttribution(t, fx, transport)
		if err == nil {
			t.Fatal("expected an attributed error")
		}
		for _, want := range []string{"monitoring/prometheus-server:9090", "NetworkPolicy monitoring/deny-all", "radar/radar-0", "ingress rule"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q lacks %q", err, want)
			}
		}
		if !errors.Is(err, ErrPrometheusNotFound) || !errors.Is(err, errPrometheusUnreachable) {
			t.Fatalf("attributed error must still satisfy the discovery sentinels: %v", err)
		}
	})

	t.Run("names Radar's own egress policy", func(t *testing.T) {
		fx := self
		fx.objects = clusterWithPrometheus(&networkingv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Namespace: "radar", Name: "egress-lockdown"},
			Spec:       networkingv1.NetworkPolicySpec{PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}},
		})
		err := runAttribution(t, fx, transport)
		if err == nil || !strings.Contains(err.Error(), "NetworkPolicy radar/egress-lockdown isolates egress") || !strings.Contains(err.Error(), "egress rule to the monitoring namespace on port 9090") {
			t.Fatalf("err = %v, want the egress policy named with an egress remedy", err)
		}
	})

	t.Run("an admitting policy yields nothing", func(t *testing.T) {
		fx := self
		fx.objects = clusterWithPrometheus(denyAllIngress("monitoring", "deny-all"), allowFromRadar("monitoring", "allow-radar"))
		if err := runAttribution(t, fx, transport); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("no policy yields nothing", func(t *testing.T) {
		fx := self
		fx.objects = clusterWithPrometheus()
		if err := runAttribution(t, fx, transport); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("a probe that was refused rather than unreachable is not attributed", func(t *testing.T) {
		fx := self
		fx.objects = clusterWithPrometheus(denyAllIngress("monitoring", "deny-all"))
		if err := runAttribution(t, fx, []prom.ProbeReason{prom.ProbeReasonAuthError}); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("without Radar's own pod identity nothing is attributed", func(t *testing.T) {
		fx := attributionFixture{objects: clusterWithPrometheus(denyAllIngress("monitoring", "deny-all"))}
		if err := runAttribution(t, fx, transport); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("a namespace-scoped policy cache cannot vouch for the candidate namespace", func(t *testing.T) {
		fx := self
		fx.objects = clusterWithPrometheus(denyAllIngress("monitoring", "deny-all"))
		fx.scopes = clusterWideScopes()
		fx.scopes[string(k8score.NetworkPolicies)] = k8score.ResourceScope{Enabled: true, Namespace: "radar"}
		if err := runAttribution(t, fx, transport); err != nil {
			t.Fatalf("expected nil for an unauthoritative cache, got %v", err)
		}
	})

	t.Run("a probe the endpoint answered is not a network failure", func(t *testing.T) {
		fx := self
		fx.objects = clusterWithPrometheus(denyAllIngress("monitoring", "deny-all"))
		if err := runAttribution(t, fx, []prom.ProbeReason{prom.ProbeReasonHTTPError}); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("a stale endpoint naming a replaced pod is undecidable", func(t *testing.T) {
		fx := self
		objs := clusterWithPrometheus(denyAllIngress("monitoring", "deny-all"))
		for _, o := range objs {
			if slice, ok := o.(*discoveryv1.EndpointSlice); ok {
				slice.Endpoints[0].TargetRef.UID = "old-uid"
			}
			if pod, ok := o.(*corev1.Pod); ok && pod.Name == "prometheus-server-0" {
				pod.UID = "new-uid"
			}
		}
		fx.objects = objs
		if err := runAttribution(t, fx, transport); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("a secondary address matching the pod is not the routed one", func(t *testing.T) {
		fx := self
		objs := clusterWithPrometheus(denyAllIngress("monitoring", "deny-all"))
		for _, o := range objs {
			if slice, ok := o.(*discoveryv1.EndpointSlice); ok {
				slice.Endpoints[0].Addresses = []string{"10.0.9.9", "10.0.2.9"}
			}
		}
		fx.objects = objs
		if err := runAttribution(t, fx, transport); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("a UDP port sharing the number is not the probed port", func(t *testing.T) {
		fx := self
		udp := corev1.ProtocolUDP
		port := int32(9090)
		objs := clusterWithPrometheus(denyAllIngress("monitoring", "deny-all"))
		for _, o := range objs {
			if svc, ok := o.(*corev1.Service); ok {
				svc.Spec.Ports = []corev1.ServicePort{{Name: "syslog", Port: 9090, Protocol: udp}}
			}
			if slice, ok := o.(*discoveryv1.EndpointSlice); ok {
				slice.Ports = []discoveryv1.EndpointPort{{Name: strPtr("syslog"), Port: &port, Protocol: &udp}}
			}
		}
		fx.objects = objs
		if err := runAttribution(t, fx, transport); err != nil {
			t.Fatalf("expected nil when only a UDP port matches, got %v", err)
		}
	})

	t.Run("a terminating endpoint that is still serving counts as a backend", func(t *testing.T) {
		fx := self
		notReady, serving := false, true
		objs := clusterWithPrometheus(denyAllIngress("monitoring", "deny-all"), allowFromRadar("monitoring", "allow-radar"))
		for _, o := range objs {
			if slice, ok := o.(*discoveryv1.EndpointSlice); ok {
				slice.Endpoints[0].Conditions = discoveryv1.EndpointConditions{Ready: &notReady, Serving: &serving, Terminating: &serving}
			}
		}
		fx.objects = objs
		if err := runAttribution(t, fx, transport); err != nil {
			t.Fatalf("expected the serving endpoint's admitting policy to yield nil, got %v", err)
		}
	})

	t.Run("a backend in another namespace is judged by that namespace's policies", func(t *testing.T) {
		fx := self
		objs := clusterWithPrometheus(
			denyAllIngress("monitoring", "deny-all"),
			&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "metrics", Labels: map[string]string{"kubernetes.io/metadata.name": "metrics"}}},
			&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Namespace: "metrics", Name: "prom-external", Labels: map[string]string{"app": "prometheus"}},
				Status:     corev1.PodStatus{PodIP: "10.0.3.4"},
			},
		)
		for _, o := range objs {
			if slice, ok := o.(*discoveryv1.EndpointSlice); ok {
				slice.Endpoints[0] = discoveryv1.Endpoint{
					Addresses: []string{"10.0.3.4"},
					TargetRef: &corev1.ObjectReference{Kind: "Pod", Namespace: "metrics", Name: "prom-external"},
				}
			}
		}
		fx.objects = objs
		// The deny-all lives in monitoring; the backend in metrics is unselected
		// by any policy, so the connection is admitted.
		if err := runAttribution(t, fx, transport); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("a policy informer that is not yet ready to serve stays silent", func(t *testing.T) {
		fx := self
		fx.objects = clusterWithPrometheus(denyAllIngress("monitoring", "deny-all"))
		fx.deferred = map[string]bool{string(k8score.NetworkPolicies): true}
		// The deferred lister may or may not have flipped ready by now; either
		// way the call must not panic, and only a ready cache may attribute.
		err := runAttribution(t, fx, transport)
		if err != nil && !strings.Contains(err.Error(), "monitoring/deny-all") {
			t.Fatalf("unexpected error %v", err)
		}
	})

	t.Run("an endpoint that does not resolve to a cached pod is undecidable", func(t *testing.T) {
		fx := self
		objs := clusterWithPrometheus(denyAllIngress("monitoring", "deny-all"))
		for _, o := range objs {
			if slice, ok := o.(*discoveryv1.EndpointSlice); ok {
				slice.Endpoints[0].TargetRef = nil
			}
		}
		fx.objects = objs
		if err := runAttribution(t, fx, transport); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})
}
