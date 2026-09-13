package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// The evaluate fixture, all in "shop" unless said otherwise: a default-deny
// ingress on every pod; the web pod admits "api" pods on 8080 and the
// "trusted" namespace; the edge pod admits an office ipBlock; the other pod may
// only egress to a DNS range; a client pod lives in "trusted".
func evaluateFixtureObjects() []runtime.Object {
	pod := func(ns, name string, labels map[string]string, ip string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c"}}},
			Status:     corev1.PodStatus{PodIP: ip, PodIPs: []corev1.PodIP{{IP: ip}}},
		}
	}
	port := intstr.FromInt(8080)
	return []runtime.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop", Labels: map[string]string{"kubernetes.io/metadata.name": "shop"}}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "trusted", Labels: map[string]string{"kubernetes.io/metadata.name": "trusted", "team": "trusted"}}},
		pod("shop", "web-0", map[string]string{"app": "web"}, "10.0.0.10"),
		pod("shop", "api-0", map[string]string{"app": "api"}, "10.0.0.11"),
		pod("shop", "other-0", map[string]string{"app": "other"}, "10.0.0.12"),
		pod("shop", "edge-0", map[string]string{"app": "edge"}, "10.0.0.13"),
		pod("trusted", "client-0", map[string]string{"app": "client"}, "10.0.1.10"),
		&networkingv1.NetworkPolicy{
			// No policyTypes: the pre-admission shape, which still isolates ingress.
			ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "deny-all-ingress"},
			Spec:       networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{}},
		},
		&networkingv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "allow-api-8080"},
			Spec: networkingv1.NetworkPolicySpec{
				PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
				PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
				Ingress: []networkingv1.NetworkPolicyIngressRule{{
					From:  []networkingv1.NetworkPolicyPeer{{PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "api"}}}},
					Ports: []networkingv1.NetworkPolicyPort{{Port: &port}},
				}},
			},
		},
		&networkingv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "allow-trusted-ns"},
			Spec: networkingv1.NetworkPolicySpec{
				PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
				PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
				Ingress: []networkingv1.NetworkPolicyIngressRule{{
					From: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"team": "trusted"}}}},
				}},
			},
		},
		&networkingv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "allow-office-ingress"},
			Spec: networkingv1.NetworkPolicySpec{
				PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "edge"}},
				PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
				Ingress: []networkingv1.NetworkPolicyIngressRule{{
					From: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "203.0.113.0/24"}}},
				}},
			},
		},
		&networkingv1.NetworkPolicy{
			ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "egress-to-dns-only"},
			Spec: networkingv1.NetworkPolicySpec{
				PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "other"}},
				PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeEgress},
				Egress: []networkingv1.NetworkPolicyEgressRule{{
					To: []networkingv1.NetworkPolicyPeer{{IPBlock: &networkingv1.IPBlock{CIDR: "1.1.1.0/24"}}},
				}},
			},
		},
	}
}

// seedEmptyDiscovery gives the handler a cluster with no Cilium CRDs. Without
// discovery it must say so rather than guess, which is not what these cases
// are about.
func seedEmptyDiscovery(t *testing.T) {
	t.Helper()
	if err := k8s.InitTestDynamicResourceCache(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), nil); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
}

func evaluateGet(t *testing.T, base string, params map[string]string) (int, PolicyEvaluation) {
	t.Helper()
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	resp, err := http.Get(base + "/api/network-policies/evaluate?" + q.Encode())
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out PolicyEvaluation
	if resp.StatusCode == http.StatusOK {
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return resp.StatusCode, out
}

func effectOf(ev PolicyEvaluation, name string) string {
	for _, m := range ev.SelectingPolicies {
		if m.Name == name {
			return m.Effect
		}
	}
	return "<absent>"
}

func TestEvaluateNetworkPolicies(t *testing.T) {
	prev := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	t.Cleanup(func() { k8s.SetConnectionStatus(prev) })
	useTestResourceCache(t, fake.NewClientset(evaluateFixtureObjects()...))
	seedEmptyDiscovery(t)

	ingress := func(srcNs, srcPod, port string) map[string]string {
		return map[string]string{
			"direction": "ingress", "namespace": "shop", "podName": "web-0",
			"sourceNamespace": srcNs, "sourcePodName": srcPod, "sourceKind": "Pod",
			"port": port, "protocol": "tcp",
		}
	}

	tests := []struct {
		name    string
		params  map[string]string
		verdict string
		effects map[string]string
		reason  string
	}{
		{
			// A policy with no policyTypes still isolates ingress.
			name:    "default-deny with no policyTypes isolates ingress",
			params:  ingress("shop", "other-0", "8080"),
			verdict: verdictDenied,
			effects: map[string]string{"deny-all-ingress": "does_not_admit", "allow-api-8080": "does_not_admit"},
		},
		{
			name:    "the allow admits the api pod on its port",
			params:  ingress("shop", "api-0", "8080"),
			verdict: verdictAdmitted,
			effects: map[string]string{"allow-api-8080": "admits"},
		},
		{
			name:    "port mismatch is not admitted",
			params:  ingress("shop", "api-0", "9090"),
			verdict: verdictDenied,
			effects: map[string]string{"allow-api-8080": "does_not_admit"},
		},
		{
			name:    "namespaceSelector matches the peer's Namespace labels",
			params:  ingress("trusted", "client-0", "8080"),
			verdict: verdictAdmitted,
			effects: map[string]string{"allow-trusted-ns": "admits"},
		},
		{
			name: "an external source inside the ipBlock is admitted",
			params: map[string]string{
				"direction": "ingress", "namespace": "shop", "podName": "edge-0",
				"sourceKind": "External", "sourceIP": "203.0.113.7", "port": "8080", "protocol": "tcp",
			},
			verdict: verdictAdmitted,
			effects: map[string]string{"allow-office-ingress": "admits"},
		},
		{
			// An ingress ipBlock miss says nothing certain: the plugin may
			// have rewritten the source address.
			name: "an external source outside the ipBlock is undecidable",
			params: map[string]string{
				"direction": "ingress", "namespace": "shop", "podName": "edge-0",
				"sourceKind": "External", "sourceIP": "198.51.100.9", "port": "8080", "protocol": "tcp",
			},
			verdict: verdictUndecidable,
			effects: map[string]string{"allow-office-ingress": "undecidable", "deny-all-ingress": "does_not_admit"},
		},
		{
			// Egress to the outside world is not DNATed, so a miss is decisive.
			name: "egress to an external address outside every ipBlock is denied",
			params: map[string]string{
				"direction": "egress", "sourceNamespace": "shop", "sourcePodName": "other-0",
				"destinationKind": "External", "destinationIP": "8.8.8.8", "port": "53", "protocol": "udp",
			},
			verdict: verdictDenied,
			effects: map[string]string{"egress-to-dns-only": "does_not_admit"},
		},
		{
			name: "a host-network peer is undecidable even under an allow-all rule",
			params: map[string]string{
				"direction": "ingress", "namespace": "shop", "podName": "web-0",
				"sourceKind": "Host", "sourceIP": "172.18.0.2", "port": "8080", "protocol": "tcp",
			},
			verdict: verdictUndecidable,
			effects: map[string]string{"allow-trusted-ns": "undecidable"},
		},
		{
			name: "a flow without a direction cannot name the enforcing end",
			params: map[string]string{
				"namespace": "shop", "podName": "web-0", "sourceNamespace": "shop", "sourcePodName": "api-0", "sourceKind": "Pod",
			},
			verdict: verdictUndecidable,
			reason:  "does not say at which end",
		},
		{
			name:    "a pod that no longer exists is undecidable, never no-policy",
			params:  map[string]string{"direction": "ingress", "namespace": "shop", "podName": "gone-0", "sourceNamespace": "shop", "sourcePodName": "api-0", "sourceKind": "Pod", "protocol": "tcp"},
			verdict: verdictUndecidable,
			reason:  "no longer exists",
		},
		{
			name:    "a peer pod that no longer exists leaves selector rules open",
			params:  ingress("shop", "gone-0", "8080"),
			verdict: verdictUndecidable,
			effects: map[string]string{"allow-api-8080": "undecidable"},
		},
		{
			// A pod source is judged against an ingress ipBlock only up to what
			// the plugin might have rewritten, so the miss stays open.
			name:    "a pod source against an ingress ipBlock is undecidable",
			params:  map[string]string{"direction": "ingress", "namespace": "shop", "podName": "edge-0", "sourceNamespace": "shop", "sourcePodName": "api-0", "sourceKind": "Pod", "port": "8080", "protocol": "tcp"},
			verdict: verdictUndecidable,
			effects: map[string]string{"allow-office-ingress": "undecidable", "deny-all-ingress": "does_not_admit"},
		},
		{
			name: "a flow without a protocol cannot be judged against port rules",
			params: map[string]string{
				"direction": "ingress", "namespace": "shop", "podName": "web-0",
				"sourceNamespace": "shop", "sourcePodName": "api-0", "sourceKind": "Pod", "port": "8080",
			},
			verdict: verdictUndecidable,
			reason:  "does not carry a protocol",
		},
		{
			name: "a pod nothing selects has no policy",
			params: map[string]string{
				"direction": "egress", "sourceNamespace": "shop", "sourcePodName": "api-0", "sourceKind": "Pod",
				"namespace": "shop", "podName": "web-0", "destinationKind": "Pod", "port": "8080", "protocol": "tcp",
			},
			verdict: verdictNoPolicy,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, ev := evaluateGet(t, testServer.URL, tt.params)
			if status != http.StatusOK {
				t.Fatalf("status = %d", status)
			}
			if ev.Verdict != tt.verdict {
				t.Fatalf("verdict = %q (%s), want %q; rows %+v", ev.Verdict, ev.Reason, tt.verdict, ev.SelectingPolicies)
			}
			for name, want := range tt.effects {
				if got := effectOf(ev, name); got != want {
					t.Errorf("%s effect = %s, want %s; rows %+v", name, got, want, ev.SelectingPolicies)
				}
			}
			if tt.reason != "" && !strings.Contains(ev.Reason, tt.reason) {
				t.Errorf("reason = %q, want it to mention %q", ev.Reason, tt.reason)
			}
			for _, m := range ev.SelectingPolicies {
				if m.Effect == "denies" || m.Effect == "deny" || m.Effect == "allow" {
					t.Errorf("row %s uses a non-additive effect word %q", m.Name, m.Effect)
				}
			}
		})
	}
}

func TestEvaluateNetworkPolicies_RequiresNamedPod(t *testing.T) {
	prev := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	t.Cleanup(func() { k8s.SetConnectionStatus(prev) })
	useTestResourceCache(t, fake.NewClientset(evaluateFixtureObjects()...))
	seedEmptyDiscovery(t)

	status, _ := evaluateGet(t, testServer.URL, map[string]string{"direction": "ingress", "namespace": "shop"})
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", status)
	}
	status, _ = evaluateGet(t, testServer.URL, map[string]string{"direction": "ingress", "namespace": "shop", "podName": "web-0", "port": "http", "protocol": "tcp"})
	if status != http.StatusBadRequest {
		t.Fatalf("malformed port: status = %d, want 400", status)
	}
}

func TestEvaluateNetworkPolicies_GatedPerCaller(t *testing.T) {
	prev := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	t.Cleanup(func() { k8s.SetConnectionStatus(prev) })
	useTestResourceCache(t, fake.NewClientset(evaluateFixtureObjects()...))
	seedEmptyDiscovery(t)

	const path = "/api/network-policies/evaluate?direction=ingress&namespace=shop&podName=web-0&sourceNamespace=trusted&sourcePodName=client-0&sourceKind=Pod&port=8080&protocol=tcp"
	grant := func(perms *auth.UserPermissions, verb, group, resource, ns string, ok bool) {
		perms.SetCanI(verb, group, resource, ns, ok)
	}
	for _, tt := range []struct {
		name    string
		setup   func(*auth.UserPermissions)
		want    int
		verdict string
		peer    string
	}{
		{"cannot list policies", func(p *auth.UserPermissions) {
			grant(p, "list", "networking.k8s.io", "networkpolicies", "shop", false)
		}, http.StatusForbidden, "", ""},
		{"cannot read the evaluated pod", func(p *auth.UserPermissions) {
			grant(p, "list", "networking.k8s.io", "networkpolicies", "shop", true)
			grant(p, "get", "", "pods", "shop", false)
		}, http.StatusForbidden, "", ""},
		// The peer lives in a namespace this caller cannot read: the rule that
		// would admit it stays undecidable instead of being answered from the
		// shared cache on the caller's behalf.
		{"cannot read the peer pod", func(p *auth.UserPermissions) {
			grant(p, "list", "networking.k8s.io", "networkpolicies", "shop", true)
			grant(p, "get", "", "pods", "shop", true)
			grant(p, "get", "", "pods", "trusted", false)
			grant(p, "get", "", "namespaces", "", true)
		}, http.StatusOK, verdictUndecidable, "trusted/client-0 (not visible to you)"},
		{"cannot read namespaces: namespaceSelector stays open", func(p *auth.UserPermissions) {
			grant(p, "list", "networking.k8s.io", "networkpolicies", "shop", true)
			grant(p, "get", "", "pods", "shop", true)
			grant(p, "get", "", "pods", "trusted", true)
			grant(p, "get", "", "namespaces", "", false)
		}, http.StatusOK, verdictUndecidable, "trusted/client-0"},
		{"fully granted", func(p *auth.UserPermissions) {
			grant(p, "list", "networking.k8s.io", "networkpolicies", "shop", true)
			grant(p, "get", "", "pods", "shop", true)
			grant(p, "get", "", "pods", "trusted", true)
			grant(p, "get", "", "namespaces", "", true)
		}, http.StatusOK, verdictAdmitted, "trusted/client-0"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			env := newAuthTestServer(t)
			perms := &auth.UserPermissions{AllowedNamespaces: []string{"shop", "trusted"}}
			tt.setup(perms)
			env.srv.permCache.Set("reader", nil, perms)
			resp := env.authGet(t, path, "reader", "")
			defer resp.Body.Close()
			if resp.StatusCode != tt.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tt.want)
			}
			if tt.want != http.StatusOK {
				return
			}
			var ev PolicyEvaluation
			if err := json.NewDecoder(resp.Body).Decode(&ev); err != nil {
				t.Fatal(err)
			}
			if ev.Verdict != tt.verdict || ev.Evaluated.Peer != tt.peer {
				t.Fatalf("verdict = %q peer = %q (%s), want %q / %q; rows %+v", ev.Verdict, ev.Evaluated.Peer, ev.Reason, tt.verdict, tt.peer, ev.SelectingPolicies)
			}
		})
	}
}

func TestEvaluateNetworkPolicies_NamespaceOutsideCacheIsUndecidable(t *testing.T) {
	prev := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	t.Cleanup(func() { k8s.SetConnectionStatus(prev) })
	t.Cleanup(func() {
		k8s.ResetResourceCache()
		if err := k8s.InitTestResourceCache(testFakeClient); err != nil {
			t.Fatalf("restore package fixture cache: %v", err)
		}
	})
	k8s.ResetResourceCache()
	if err := k8s.InitScopedTestResourceCache(fake.NewClientset(evaluateFixtureObjects()...), map[string]k8score.ResourceScope{
		string(k8score.Pods):            {Enabled: true, Namespace: "trusted"},
		string(k8score.Namespaces):      {Enabled: true},
		string(k8score.NetworkPolicies): {Enabled: true, Namespace: "trusted"},
	}); err != nil {
		t.Fatal(err)
	}
	seedEmptyDiscovery(t)

	status, ev := evaluateGet(t, testServer.URL, map[string]string{
		"direction": "ingress", "namespace": "shop", "podName": "web-0", "sourceNamespace": "trusted", "sourcePodName": "client-0", "sourceKind": "Pod", "port": "8080", "protocol": "tcp",
	})
	if status != http.StatusOK || ev.Verdict != verdictUndecidable || !strings.Contains(ev.Reason, "can't see") {
		t.Fatalf("status %d verdict %q reason %q; want undecidable because shop is not watched", status, ev.Verdict, ev.Reason)
	}
}

func TestEvaluateNetworkPolicies_CiliumPresenceCapsVerdict(t *testing.T) {
	prev := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	t.Cleanup(func() { k8s.SetConnectionStatus(prev) })
	useTestResourceCache(t, fake.NewClientset(evaluateFixtureObjects()...))

	cnpGVR := schema.GroupVersionResource{Group: "cilium.io", Version: "v2", Resource: "ciliumnetworkpolicies"}
	ccnpGVR := schema.GroupVersionResource{Group: "cilium.io", Version: "v2", Resource: "ciliumclusterwidenetworkpolicies"}
	policy := func(kind, name string, spec map[string]any, specs []any) *unstructured.Unstructured {
		meta := map[string]any{"name": name}
		if kind == "CiliumNetworkPolicy" {
			meta["namespace"] = "shop"
		}
		obj := map[string]any{"apiVersion": "cilium.io/v2", "kind": kind, "metadata": meta}
		if spec != nil {
			obj["spec"] = spec
		}
		if specs != nil {
			obj["specs"] = specs
		}
		return &unstructured.Unstructured{Object: obj}
	}
	cnp := func(name string, spec map[string]any, specs []any) *unstructured.Unstructured {
		return policy("CiliumNetworkPolicy", name, spec, specs)
	}
	seed := func(t *testing.T, objs ...runtime.Object) {
		t.Helper()
		dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{cnpGVR: "CiliumNetworkPolicyList", ccnpGVR: "CiliumClusterwideNetworkPolicyList"}, objs...)
		if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{
			{Group: "cilium.io", Version: "v2", Kind: "CiliumNetworkPolicy", Name: "ciliumnetworkpolicies", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}},
			{Group: "cilium.io", Version: "v2", Kind: "CiliumClusterwideNetworkPolicy", Name: "ciliumclusterwidenetworkpolicies", Namespaced: false, IsCRD: true, Verbs: []string{"get", "list", "watch"}},
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(k8s.ResetTestDynamicState)
	}
	selectsWeb := map[string]any{"endpointSelector": map[string]any{"matchLabels": map[string]any{"k8s:app": "web"}}, "ingress": []any{map[string]any{}}}
	admitted := map[string]string{
		"direction": "ingress", "namespace": "shop", "podName": "web-0",
		"sourceNamespace": "shop", "sourcePodName": "api-0", "sourceKind": "Pod", "port": "8080", "protocol": "tcp",
	}
	denied := map[string]string{
		"direction": "ingress", "namespace": "shop", "podName": "web-0",
		"sourceNamespace": "shop", "sourcePodName": "other-0", "sourceKind": "Pod", "port": "8080", "protocol": "tcp",
	}
	expect := func(t *testing.T, params map[string]string, verdict string, row string) {
		t.Helper()
		_, ev := evaluateGet(t, testServer.URL, params)
		if ev.Verdict != verdict {
			t.Fatalf("verdict = %q (%s), want %q; rows %+v", ev.Verdict, ev.Reason, verdict, ev.SelectingPolicies)
		}
		if row != "" && effectOf(ev, row) != "undecidable" {
			t.Fatalf("row %s = %s, want undecidable; rows %+v", row, effectOf(ev, row), ev.SelectingPolicies)
		}
	}

	t.Run("no Cilium policy selects the pod: core verdict stands", func(t *testing.T) {
		seed(t, cnp("api-only", map[string]any{"endpointSelector": map[string]any{"matchLabels": map[string]any{"app": "api"}}, "ingress": []any{map[string]any{}}}, nil))
		expect(t, admitted, verdictAdmitted, "")
		expect(t, denied, verdictDenied, "")
	})
	t.Run("a Cilium policy on the pod caps admitted to undecidable and appears as a row", func(t *testing.T) {
		seed(t, cnp("web-l7", selectsWeb, nil))
		expect(t, admitted, verdictUndecidable, "web-l7")
	})
	// Cilium unions its allow rules with core ones: a CNP allow-all admits
	// what a core default-deny would refuse, so the core denial is no verdict.
	t.Run("a Cilium allow can rescue a core denial, so the denial is capped too", func(t *testing.T) {
		seed(t, cnp("web-allow-all", selectsWeb, nil))
		expect(t, denied, verdictUndecidable, "web-allow-all")
	})
	t.Run("an egress-only Cilium policy does not obscure an ingress verdict", func(t *testing.T) {
		seed(t, cnp("web-egress", map[string]any{"endpointSelector": map[string]any{"matchLabels": map[string]any{"app": "web"}}, "egress": []any{map[string]any{}}}, nil))
		expect(t, admitted, verdictAdmitted, "")
	})
	t.Run("a cluster-wide Cilium policy selecting the pod caps the verdict", func(t *testing.T) {
		seed(t, policy("CiliumClusterwideNetworkPolicy", "all-web", selectsWeb, nil))
		expect(t, admitted, verdictUndecidable, "all-web")
	})
	t.Run("cluster-wide policies are consulted even when the namespaced kind is not served", func(t *testing.T) {
		dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{ccnpGVR: "CiliumClusterwideNetworkPolicyList"},
			policy("CiliumClusterwideNetworkPolicy", "all-web", selectsWeb, nil))
		if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{
			{Group: "cilium.io", Version: "v2", Kind: "CiliumClusterwideNetworkPolicy", Name: "ciliumclusterwidenetworkpolicies", Namespaced: false, IsCRD: true, Verbs: []string{"get", "list", "watch"}},
		}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(k8s.ResetTestDynamicState)
		expect(t, admitted, verdictUndecidable, "all-web")
	})
	t.Run("specs[] and matchExpressions select too", func(t *testing.T) {
		seed(t, cnp("multi", nil, []any{
			map[string]any{"endpointSelector": map[string]any{"matchLabels": map[string]any{"app": "nothing"}}, "ingress": []any{}},
			map[string]any{"endpointSelector": map[string]any{"matchExpressions": []any{map[string]any{"key": "app", "operator": "In", "values": []any{"web", "api"}}}}, "ingressDeny": []any{}},
		}))
		expect(t, admitted, verdictUndecidable, "multi")
	})
	// Cilium matches selectors against the endpoint identity, which carries
	// labels the pod's metadata does not: the namespace, the service account.
	t.Run("a serviceaccount selector selects through the Cilium identity", func(t *testing.T) {
		seed(t, cnp("by-sa", map[string]any{"endpointSelector": map[string]any{"matchLabels": map[string]any{"io.cilium.k8s.policy.serviceaccount": "default", "k8s:io.kubernetes.pod.namespace": "shop"}}, "ingress": []any{}}, nil))
		expect(t, admitted, verdictUndecidable, "by-sa")
	})
	t.Run("a namespace-label selector selects through the Namespace object", func(t *testing.T) {
		seed(t, cnp("by-ns-label", map[string]any{"endpointSelector": map[string]any{"matchLabels": map[string]any{"k8s:io.cilium.k8s.namespace.labels.kubernetes.io/metadata.name": "shop"}}, "ingress": []any{}}, nil))
		expect(t, admitted, verdictUndecidable, "by-ns-label")
	})
	t.Run("a selector on the cluster name is undecidable, not a non-match", func(t *testing.T) {
		seed(t, cnp("by-cluster", map[string]any{"endpointSelector": map[string]any{"matchLabels": map[string]any{"io.cilium.k8s.policy.cluster": "other"}}, "ingress": []any{}}, nil))
		_, ev := evaluateGet(t, testServer.URL, admitted)
		if ev.Verdict != verdictUndecidable || !strings.Contains(ev.Reason, "cluster name") {
			t.Fatalf("verdict = %q (%s)", ev.Verdict, ev.Reason)
		}
	})
	t.Run("a nodeSelector-only policy selects no pod", func(t *testing.T) {
		seed(t, cnp("nodes", map[string]any{"nodeSelector": map[string]any{"matchLabels": map[string]any{"role": "infra"}}, "ingress": []any{}}, nil))
		expect(t, admitted, verdictAdmitted, "")
	})
}
