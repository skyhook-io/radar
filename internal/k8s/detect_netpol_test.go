package k8s

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/netpol"
)

func netpolTestPod(name string, lbls map[string]string, ready bool) *corev1.Pod {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", Labels: lbls},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: status}},
		},
	}
}

func netpolTestService(name string, selector map[string]string) *corev1.Service {
	return &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop"},
		Spec: corev1.ServiceSpec{
			Selector: selector,
			Ports:    []corev1.ServicePort{{Port: 8080}},
		},
	}
}

func netpolTestPolicy(name string, selector map[string]string, types []networkingv1.PolicyType) *networkingv1.NetworkPolicy {
	return &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", CreationTimestamp: metav1.NewTime(time.Now().Add(-3 * time.Minute))},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: selector},
			PolicyTypes: types,
		},
	}
}

var (
	ingressOnly      = []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}
	egressOnly       = []networkingv1.PolicyType{networkingv1.PolicyTypeEgress}
	ingressAndEgress = []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress}
)

func allowFromAnywhere(np *networkingv1.NetworkPolicy) *networkingv1.NetworkPolicy {
	port := intstr.FromInt32(8080)
	np.Spec.Ingress = []networkingv1.NetworkPolicyIngressRule{{Ports: []networkingv1.NetworkPolicyPort{{Port: &port}}}}
	return np
}

func TestDenyAllIngressDetections(t *testing.T) {
	recPods := []*corev1.Pod{
		netpolTestPod("rec-1", map[string]string{"app": "rec"}, true),
		netpolTestPod("rec-2", map[string]string{"app": "rec"}, true),
	}
	recSvc := netpolTestService("recommendation", map[string]string{"app": "rec"})

	tests := []struct {
		name      string
		policies  []*networkingv1.NetworkPolicy
		services  []*corev1.Service
		pods      []*corev1.Pod
		wantNames []string
	}{
		{
			name:      "targeted deny-all on a Service's pods",
			policies:  []*networkingv1.NetworkPolicy{netpolTestPolicy("deny-all-rec", map[string]string{"app": "rec"}, ingressAndEgress)},
			services:  []*corev1.Service{recSvc},
			pods:      recPods,
			wantNames: []string{"deny-all-rec"},
		},
		{
			name:      "namespace default-deny with no allow policy",
			policies:  []*networkingv1.NetworkPolicy{netpolTestPolicy("default-deny", nil, ingressOnly)},
			services:  []*corev1.Service{recSvc},
			pods:      recPods,
			wantNames: []string{"default-deny"},
		},
		{
			name: "default-deny plus an allow policy is the intended pattern",
			policies: []*networkingv1.NetworkPolicy{
				netpolTestPolicy("default-deny", nil, ingressOnly),
				allowFromAnywhere(netpolTestPolicy("allow-rec", map[string]string{"app": "rec"}, ingressOnly)),
			},
			services: []*corev1.Service{recSvc},
			pods:     recPods,
		},
		{
			name:     "egress-only isolation is not an inbound block",
			policies: []*networkingv1.NetworkPolicy{netpolTestPolicy("deny-egress", map[string]string{"app": "rec"}, egressOnly)},
			services: []*corev1.Service{recSvc},
			pods:     recPods,
		},
		{
			name:     "policy selecting only some backend pods",
			policies: []*networkingv1.NetworkPolicy{netpolTestPolicy("deny-one", map[string]string{"canary": "true"}, ingressOnly)},
			services: []*corev1.Service{recSvc},
			pods: []*corev1.Pod{
				netpolTestPod("rec-1", map[string]string{"app": "rec", "canary": "true"}, true),
				netpolTestPod("rec-2", map[string]string{"app": "rec"}, true),
			},
		},
		{
			name:     "pods no Service selects",
			policies: []*networkingv1.NetworkPolicy{netpolTestPolicy("deny-workers", map[string]string{"app": "worker"}, ingressOnly)},
			services: []*corev1.Service{recSvc},
			pods:     []*corev1.Pod{netpolTestPod("worker-1", map[string]string{"app": "worker"}, true)},
		},
		{
			name:     "Service with no ready backends is left to the endpoints check",
			policies: []*networkingv1.NetworkPolicy{netpolTestPolicy("deny-all-rec", map[string]string{"app": "rec"}, ingressOnly)},
			services: []*corev1.Service{recSvc},
			pods:     []*corev1.Pod{netpolTestPod("rec-1", map[string]string{"app": "rec"}, false)},
		},
		{
			name: "an unparseable selector might be the allow, so stay silent",
			policies: []*networkingv1.NetworkPolicy{
				netpolTestPolicy("deny-all-rec", map[string]string{"app": "rec"}, ingressOnly),
				func() *networkingv1.NetworkPolicy {
					np := allowFromAnywhere(netpolTestPolicy("broken-allow", nil, ingressOnly))
					np.Spec.PodSelector = metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "app", Operator: "Bogus"}}}
					return np
				}(),
			},
			services: []*corev1.Service{recSvc},
			pods:     recPods,
		},
		{
			name:     "hostNetwork backends",
			policies: []*networkingv1.NetworkPolicy{netpolTestPolicy("deny-all-rec", map[string]string{"app": "rec"}, ingressOnly)},
			services: []*corev1.Service{recSvc},
			pods: func() []*corev1.Pod {
				p := netpolTestPod("rec-1", map[string]string{"app": "rec"}, true)
				p.Spec.HostNetwork = true
				return []*corev1.Pod{p}
			}(),
		},
		{
			name:     "ExternalName Service",
			policies: []*networkingv1.NetworkPolicy{netpolTestPolicy("deny-all-rec", map[string]string{"app": "rec"}, ingressOnly)},
			services: []*corev1.Service{func() *corev1.Service {
				s := netpolTestService("ext", map[string]string{"app": "rec"})
				s.Spec.Type = corev1.ServiceTypeExternalName
				return s
			}()},
			pods: recPods,
		},
		{
			name: "two deny-all policies are both named",
			policies: []*networkingv1.NetworkPolicy{
				netpolTestPolicy("default-deny", nil, ingressOnly),
				netpolTestPolicy("deny-all-rec", map[string]string{"app": "rec"}, ingressOnly),
			},
			services:  []*corev1.Service{recSvc},
			pods:      recPods,
			wantNames: []string{"default-deny", "deny-all-rec"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := denyAllIngressDetections(tt.policies, tt.services, tt.pods, denyAllEnv{}, time.Now())
			var names []string
			for _, d := range got {
				names = append(names, d.Name)
			}
			if strings.Join(names, ",") != strings.Join(tt.wantNames, ",") {
				t.Fatalf("detections = %v, want %v", names, tt.wantNames)
			}
			for _, d := range got {
				if d.Kind != "NetworkPolicy" || d.Group != "networking.k8s.io" || d.Severity != "warning" ||
					d.Reason != NetworkPolicyDenyAllIngressReason || d.Fingerprint != NetworkPolicyDenyAllIngressFingerprint {
					t.Errorf("unexpected detection identity: %+v", d)
				}
				if len(d.RequiredReads) != 3 {
					t.Errorf("RequiredReads = %+v, want networkpolicies, services and pods", d.RequiredReads)
				}
			}
		})
	}
}

func TestDenyAllIngressDetectionMessage(t *testing.T) {
	pods := []*corev1.Pod{netpolTestPod("rec-1", map[string]string{"app": "rec"}, true)}
	services := []*corev1.Service{
		netpolTestService("rec-a", map[string]string{"app": "rec"}),
		netpolTestService("rec-b", map[string]string{"app": "rec"}),
		netpolTestService("rec-c", map[string]string{"app": "rec"}),
		netpolTestService("rec-d", map[string]string{"app": "rec"}),
	}

	both := denyAllIngressDetections([]*networkingv1.NetworkPolicy{
		netpolTestPolicy("deny-all", map[string]string{"app": "rec"}, ingressAndEgress),
	}, services, pods, denyAllEnv{}, time.Now())
	if len(both) != 1 {
		t.Fatalf("got %d detections, want 1", len(both))
	}
	msg := both[0].Message
	for _, want := range []string{"Would block all inbound traffic", "1 pod behind Services rec-a, rec-b, rec-c and 1 more", "outbound traffic, including DNS"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q missing %q", msg, want)
		}
	}
	if !strings.Contains(both[0].Cause, "network plugin") || !strings.Contains(both[0].Action, "kubectl describe networkpolicy deny-all -n shop") {
		t.Errorf("cause/action missing enforcement caveat or command: %q / %q", both[0].Cause, both[0].Action)
	}

	ingress := denyAllIngressDetections([]*networkingv1.NetworkPolicy{
		netpolTestPolicy("deny-in", map[string]string{"app": "rec"}, ingressOnly),
	}, services[:1], pods, denyAllEnv{}, time.Now())
	if len(ingress) != 1 || strings.Contains(ingress[0].Message, "outbound") {
		t.Fatalf("ingress-only deny must not claim outbound is blocked: %+v", ingress)
	}

	pair := denyAllIngressDetections([]*networkingv1.NetworkPolicy{
		netpolTestPolicy("default-deny", nil, ingressOnly),
		netpolTestPolicy("deny-in", map[string]string{"app": "rec"}, ingressOnly),
	}, services[:1], pods, denyAllEnv{}, time.Now())
	if len(pair) != 2 {
		t.Fatalf("got %d detections, want one per contributing policy", len(pair))
	}
	for _, d := range pair {
		if !strings.Contains(d.Message, "the 1 pod behind Service rec-a") {
			t.Errorf("%s message = %q: both policies cover the Service's only pod", d.Name, d.Message)
		}
	}
	for i, other := range []string{"deny-in", "default-deny"} {
		if !strings.Contains(pair[i].Cause, "NetworkPolicy "+other+" also isolates this pod") {
			t.Errorf("%s cause doesn't name %s: %q", pair[i].Name, other, pair[i].Cause)
		}
	}
	// Deleting a namespace default-deny is the wrong first advice; the pods
	// need an allow policy.
	if !strings.Contains(pair[0].Action, "namespace-wide default-deny") || strings.Contains(pair[0].Action, "delete the policy") {
		t.Errorf("default-deny action = %q", pair[0].Action)
	}
	if !strings.Contains(pair[1].Action, "removing this one alone may not restore traffic") {
		t.Errorf("targeted deny next to a default-deny action = %q", pair[1].Action)
	}
	if solo := denyAllIngressDetections([]*networkingv1.NetworkPolicy{
		netpolTestPolicy("deny-in", map[string]string{"app": "rec"}, ingressOnly),
	}, services[:1], pods, denyAllEnv{}, time.Now()); !strings.Contains(solo[0].Action, "delete the policy") {
		t.Errorf("lone targeted deny action = %q", solo[0].Action)
	}

	brokenEgress := netpolTestPolicy("broken-egress", nil, egressOnly)
	brokenEgress.Spec.PodSelector = metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "app", Operator: "Bogus"}}}
	undecided := denyAllIngressDetections([]*networkingv1.NetworkPolicy{
		netpolTestPolicy("deny-all", map[string]string{"app": "rec"}, ingressAndEgress), brokenEgress,
	}, services[:1], pods, denyAllEnv{}, time.Now())
	if len(undecided) != 1 || strings.Contains(undecided[0].Message, "outbound") {
		t.Fatalf("an unparseable egress policy might allow outbound traffic, so no outbound claim: %+v", undecided)
	}
}

func TestDetectProblemsIncludesNetworkPolicyDenyAll(t *testing.T) {
	defer ResetTestState()
	client := fake.NewClientset(
		netpolTestPod("rec-1", map[string]string{"app": "rec"}, true),
		netpolTestService("recommendation", map[string]string{"app": "rec"}),
		netpolTestPolicy("deny-all-rec", map[string]string{"app": "rec"}, ingressAndEgress),
	)
	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	// Discovery that serves only core APIs, so no other policy API can apply.
	fakeDisc := client.Discovery().(*fakediscovery.FakeDiscovery)
	fakeDisc.Resources = []*metav1.APIResourceList{{
		GroupVersion: "networking.k8s.io/v1",
		APIResources: []metav1.APIResource{{Name: "networkpolicies", Kind: "NetworkPolicy", Namespaced: true, Verbs: metav1.Verbs{"get", "list", "watch"}}},
	}}
	restore, err := SetTestResourceDiscovery(fakeDisc)
	if err != nil {
		t.Fatalf("SetTestResourceDiscovery: %v", err)
	}
	defer restore()
	cache := GetResourceCache()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, d := range DetectProblems(cache, "shop") {
			if d.Kind == "NetworkPolicy" && d.Name == "deny-all-rec" && d.Reason == NetworkPolicyDenyAllIngressReason {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("DetectProblems did not report the deny-all NetworkPolicy")
}

type fakePolicyDiscovery map[string]schema.GroupVersionResource

func (f fakePolicyDiscovery) GetGVRWithGroup(kind, group string) (schema.GroupVersionResource, bool) {
	gvr, ok := f[group+"/"+kind]
	return gvr, ok
}

type fakePolicyLister struct {
	objects  map[schema.GroupVersionResource]map[string][]*unstructured.Unstructured // namespace ("" = cluster-wide)
	unsynced bool
	err      error
	calls    *int
}

func (f fakePolicyLister) List(gvr schema.GroupVersionResource, namespace string) ([]*unstructured.Unstructured, error) {
	if f.calls != nil {
		*f.calls++
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.objects[gvr][namespace], nil
}

func (f fakePolicyLister) IsSynced(schema.GroupVersionResource) bool { return !f.unsynced }

func policyObject(spec map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"spec": spec}}
}

func TestOtherPolicyViewMayApply(t *testing.T) {
	allowIn := []any{map[string]any{"action": "Allow"}}
	cnp := schema.GroupVersionResource{Group: "cilium.io", Version: "v2", Resource: "ciliumnetworkpolicies"}
	ccnp := schema.GroupVersionResource{Group: "cilium.io", Version: "v2", Resource: "ciliumclusterwidenetworkpolicies"}
	gnp := schema.GroupVersionResource{Group: "crd.projectcalico.org", Version: "v1", Resource: "globalnetworkpolicies"}
	clusterNP := schema.GroupVersionResource{Group: "policy.networking.k8s.io", Version: "v1alpha2", Resource: "clusternetworkpolicies"}
	served := fakePolicyDiscovery{
		"cilium.io/CiliumNetworkPolicy":                 cnp,
		"cilium.io/CiliumClusterwideNetworkPolicy":      ccnp,
		"crd.projectcalico.org/GlobalNetworkPolicy":     gnp,
		"policy.networking.k8s.io/ClusterNetworkPolicy": clusterNP,
	}
	shopPods := []*corev1.Pod{netpolTestPod("rec-1", map[string]string{"app": "rec"}, true)}
	shopNS := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop", Labels: map[string]string{"team": "shop"}}}
	objects := func(gvr schema.GroupVersionResource, ns string, items ...*unstructured.Unstructured) map[schema.GroupVersionResource]map[string][]*unstructured.Unstructured {
		return map[schema.GroupVersionResource]map[string][]*unstructured.Unstructured{gvr: {ns: items}}
	}
	cilium := func(selector map[string]any, rules ...string) *unstructured.Unstructured {
		spec := map[string]any{"endpointSelector": selector}
		for _, r := range rules {
			spec[r] = []any{map[string]any{"fromEntities": []any{"cluster"}}}
		}
		return policyObject(spec)
	}
	matchRec := map[string]any{"matchLabels": map[string]any{"app": "rec"}}
	matchWeb := map[string]any{"matchLabels": map[string]any{"app": "web"}}

	tests := []struct {
		name   string
		disc   policyAPIDiscovery
		lister policyObjectLister
		ns     *corev1.Namespace
		want   bool
	}{
		{"no other policy API served", fakePolicyDiscovery{}, nil, shopNS, false},
		{"served but empty", served, fakePolicyLister{}, shopNS, false},
		{"Cilium policy selecting the pods", served, fakePolicyLister{objects: objects(cnp, "shop", cilium(matchRec, "ingress"))}, shopNS, true},
		{"Cilium policy selecting other pods", served, fakePolicyLister{objects: objects(cnp, "shop", cilium(matchWeb, "ingress"))}, shopNS, false},
		{"Cilium deny-only policy can't allow inbound", served, fakePolicyLister{objects: objects(cnp, "shop", cilium(matchRec, "ingressDeny"))}, shopNS, false},
		{"Cilium policy with no rule sections may allow", served, fakePolicyLister{objects: objects(cnp, "shop", cilium(matchRec))}, shopNS, true},
		{"Cilium specs list: deny for these pods, allow for others", served, fakePolicyLister{objects: objects(cnp, "shop", &unstructured.Unstructured{Object: map[string]any{"specs": []any{
			map[string]any{"endpointSelector": matchRec, "ingressDeny": []any{map[string]any{"fromEntities": []any{"world"}}}},
			map[string]any{"endpointSelector": matchWeb, "ingress": []any{map[string]any{"fromEntities": []any{"cluster"}}}},
		}}})}, shopNS, false},
		{"Cilium specs list with an allowing spec for these pods", served, fakePolicyLister{objects: objects(cnp, "shop", &unstructured.Unstructured{Object: map[string]any{"specs": []any{
			map[string]any{"endpointSelector": matchWeb, "ingressDeny": []any{map[string]any{"fromEntities": []any{"world"}}}},
			map[string]any{"endpointSelector": matchRec, "ingress": []any{map[string]any{"fromEntities": []any{"cluster"}}}},
		}}})}, shopNS, true},
		{"Cilium egress-only policy can't allow inbound", served, fakePolicyLister{objects: objects(cnp, "shop", cilium(matchRec, "egress"))}, shopNS, false},
		{"Cilium policy in another namespace", served, fakePolicyLister{objects: objects(cnp, "other", cilium(matchRec, "ingress"))}, shopNS, false},
		{"Cilium cluster-wide policy selecting the pods", served, fakePolicyLister{objects: objects(ccnp, "", cilium(map[string]any{"matchLabels": map[string]any{"k8s:io.kubernetes.pod.namespace": "shop"}}, "ingress"))}, shopNS, true},
		{"Cilium cluster-wide policy for another namespace", served, fakePolicyLister{objects: objects(ccnp, "", cilium(map[string]any{"matchLabels": map[string]any{"k8s:io.kubernetes.pod.namespace": "other"}}, "ingress"))}, shopNS, false},
		{"Cilium selector on namespace labels Radar couldn't read", served, fakePolicyLister{objects: objects(ccnp, "", cilium(map[string]any{"matchLabels": map[string]any{"io.cilium.k8s.namespace.labels.team": "shop"}}, "ingress"))}, nil, true},
		{"Cilium selector on the cluster name", served, fakePolicyLister{objects: objects(ccnp, "", cilium(map[string]any{"matchLabels": map[string]any{"io.cilium.k8s.policy.cluster": "a"}}, "ingress"))}, shopNS, true},
		{"admin-tier cluster policy", served, fakePolicyLister{objects: objects(clusterNP, "", policyObject(map[string]any{"tier": "Admin"}))}, shopNS, true},
		{"baseline-tier cluster policy can't override", served, fakePolicyLister{objects: objects(clusterNP, "", policyObject(map[string]any{"tier": "Baseline"}))}, shopNS, false},
		{"Calico global policy selecting the pods", served, fakePolicyLister{objects: objects(gnp, "", policyObject(map[string]any{"selector": "app == 'rec'", "ingress": allowIn}))}, shopNS, true},
		// Calico's recommended global default-deny: it selects the pods but
		// allows only DNS egress, so it can't allow inbound traffic.
		{"Calico global default-deny with only a DNS egress allow", served, fakePolicyLister{objects: objects(gnp, "", policyObject(map[string]any{
			"selector": "projectcalico.org/namespace not in {'kube-system', 'calico-system'}", "types": []any{"Ingress", "Egress"},
			"egress": []any{map[string]any{"action": "Allow", "protocol": "UDP", "destination": map[string]any{"selector": "k8s-app == 'kube-dns'", "ports": []any{53}}}},
		}))}, shopNS, false},
		{"Calico ingress policy with only Deny and Log rules", served, fakePolicyLister{objects: objects(gnp, "", policyObject(map[string]any{
			"selector": "app == 'rec'", "types": []any{"Ingress"}, "ingress": []any{map[string]any{"action": "Log"}, map[string]any{"action": "Deny"}},
		}))}, shopNS, false},
		{"Calico ingress rule that isn't an object may allow", served, fakePolicyLister{objects: objects(gnp, "", policyObject(map[string]any{
			"selector": "app == 'rec'", "types": []any{"Ingress"}, "ingress": []any{nil},
		}))}, shopNS, true},
		{"Calico ingress Pass rule may fall through to an allow", served, fakePolicyLister{objects: objects(gnp, "", policyObject(map[string]any{
			"selector": "app == 'rec'", "ingress": []any{map[string]any{"action": "Pass"}},
		}))}, shopNS, true},
		{"Calico ingress rule with an action Radar can't read", served, fakePolicyLister{objects: objects(gnp, "", policyObject(map[string]any{
			"selector": "app == 'rec'", "ingress": []any{map[string]any{"action": "Reroute"}},
		}))}, shopNS, true},
		{"Calico global policy selecting other pods", served, fakePolicyLister{objects: objects(gnp, "", policyObject(map[string]any{"selector": "app == 'web'", "ingress": allowIn}))}, shopNS, false},
		{"Calico global policy for another namespace", served, fakePolicyLister{objects: objects(gnp, "", policyObject(map[string]any{"namespaceSelector": "team == 'other'", "ingress": allowIn}))}, shopNS, false},
		{"Calico namespace selector with unknown namespace labels", served, fakePolicyLister{objects: objects(gnp, "", policyObject(map[string]any{"namespaceSelector": "team == 'other'", "ingress": allowIn}))}, nil, true},
		{"Calico service-account selector can't be judged", served, fakePolicyLister{objects: objects(gnp, "", policyObject(map[string]any{"serviceAccountSelector": "role == 'x'", "ingress": allowIn}))}, shopNS, true},
		{"Calico egress-only policy can't allow inbound", served, fakePolicyLister{objects: objects(gnp, "", policyObject(map[string]any{"selector": "app == 'rec'", "types": []any{"Egress"}}))}, shopNS, false},
		{"Calico policy with only egress rules defaults to Egress", served, fakePolicyLister{objects: objects(gnp, "", policyObject(map[string]any{"selector": "app == 'rec'", "egress": []any{map[string]any{"action": "Allow"}}}))}, shopNS, false},
		{"Calico selector on another service account's endpoint label", served, fakePolicyLister{objects: objects(gnp, "", policyObject(map[string]any{"selector": "projectcalico.org/serviceaccount == 'payments-api'", "ingress": allowIn}))}, shopNS, false},
		{"Calico selector on the endpoint service-account label", served, fakePolicyLister{objects: objects(gnp, "", policyObject(map[string]any{"selector": "projectcalico.org/serviceaccount == 'default'", "ingress": allowIn}))}, shopNS, true},
		{"Calico service-account selector plus an excluding namespace selector", served, fakePolicyLister{objects: objects(gnp, "", policyObject(map[string]any{"namespaceSelector": "team == 'other'", "serviceAccountSelector": "role == 'x'", "ingress": allowIn}))}, shopNS, false},
		{"Calico selector Radar can't parse", served, fakePolicyLister{objects: objects(gnp, "", policyObject(map[string]any{"selector": "app ===", "ingress": allowIn}))}, shopNS, true},
		{"unsynced is unknown", served, fakePolicyLister{unsynced: true}, shopNS, true},
		{"list error is unknown", served, fakePolicyLister{err: errors.New("forbidden")}, shopNS, true},
		{"served without a dynamic cache is unknown", served, nil, shopNS, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			view := &otherPolicyView{discovery: tt.disc, lister: tt.lister}
			if got := view.mayApply("shop", shopPods, tt.ns, netpol.DirectionIngress); got != tt.want {
				t.Errorf("mayApply = %v, want %v", got, tt.want)
			}
		})
	}
	var noDiscovery *otherPolicyView
	if !noDiscovery.mayApply("shop", shopPods, shopNS, netpol.DirectionIngress) {
		t.Error("without discovery nothing can be ruled out")
	}
}

// A policy Radar doesn't evaluate silences only the Services whose backends it
// may select, not the whole namespace.
func TestDenyAllIngressSkipsOnlyServicesOtherPoliciesSelect(t *testing.T) {
	pods := []*corev1.Pod{
		netpolTestPod("rec-1", map[string]string{"app": "rec"}, true),
		netpolTestPod("web-1", map[string]string{"app": "web"}, true),
	}
	services := []*corev1.Service{
		netpolTestService("recommendation", map[string]string{"app": "rec"}),
		netpolTestService("web", map[string]string{"app": "web"}),
	}
	selectsWeb := func(backends []*corev1.Pod, _ netpol.Direction) bool {
		for _, p := range backends {
			if p.Labels["app"] == "web" {
				return true
			}
		}
		return false
	}
	got := denyAllIngressDetections([]*networkingv1.NetworkPolicy{netpolTestPolicy("default-deny", nil, ingressOnly)}, services, pods, denyAllEnv{otherMayApply: selectsWeb}, time.Now())
	if len(got) != 1 || !strings.Contains(got[0].Message, "Service recommendation") || strings.Contains(got[0].Message, "web") {
		t.Fatalf("detections = %+v, want only Service recommendation", got)
	}
}

// Another engine's outbound allow makes the outbound note false, while the
// inbound finding stands.
func TestDenyAllIngressOutboundNoteRespectsOtherPolicies(t *testing.T) {
	pods := []*corev1.Pod{netpolTestPod("rec-1", map[string]string{"app": "rec"}, true)}
	services := []*corev1.Service{netpolTestService("recommendation", map[string]string{"app": "rec"})}
	policies := []*networkingv1.NetworkPolicy{netpolTestPolicy("deny-all", map[string]string{"app": "rec"}, ingressAndEgress)}
	egressOnly := func(_ []*corev1.Pod, dir netpol.Direction) bool { return dir == netpol.DirectionEgress }
	got := denyAllIngressDetections(policies, services, pods, denyAllEnv{otherMayApply: egressOnly}, time.Now())
	if len(got) != 1 || strings.Contains(got[0].Message, "outbound") {
		t.Fatalf("detections = %+v, want the inbound finding without the outbound note", got)
	}
}

func TestOtherPolicyViewDirections(t *testing.T) {
	cnp := schema.GroupVersionResource{Group: "cilium.io", Version: "v2", Resource: "ciliumnetworkpolicies"}
	gnp := schema.GroupVersionResource{Group: "crd.projectcalico.org", Version: "v1", Resource: "globalnetworkpolicies"}
	served := fakePolicyDiscovery{"cilium.io/CiliumNetworkPolicy": cnp, "crd.projectcalico.org/GlobalNetworkPolicy": gnp}
	pods := []*corev1.Pod{netpolTestPod("rec-1", map[string]string{"app": "rec"}, true)}
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}}
	ciliumEgress := policyObject(map[string]any{"endpointSelector": map[string]any{"matchLabels": map[string]any{"app": "rec"}}, "egress": []any{map[string]any{"toEntities": []any{"world"}}}})
	calicoEgress := policyObject(map[string]any{"selector": "app == 'rec'", "egress": []any{map[string]any{"action": "Allow"}}})
	for name, lister := range map[string]fakePolicyLister{
		"Cilium": {objects: map[schema.GroupVersionResource]map[string][]*unstructured.Unstructured{cnp: {"shop": {ciliumEgress}}}},
		"Calico": {objects: map[schema.GroupVersionResource]map[string][]*unstructured.Unstructured{gnp: {"": {calicoEgress}}}},
	} {
		view := &otherPolicyView{discovery: served, lister: lister}
		if view.mayApply("shop", pods, ns, netpol.DirectionIngress) {
			t.Errorf("%s egress policy counted for inbound", name)
		}
		if !view.mayApply("shop", pods, ns, netpol.DirectionEgress) {
			t.Errorf("%s egress policy not counted for outbound", name)
		}
	}

	// Only Deny and Log egress rules can't allow outbound traffic either.
	calicoEgressDeny := policyObject(map[string]any{"selector": "app == 'rec'", "egress": []any{map[string]any{"action": "Log"}, map[string]any{"action": "Deny"}}})
	view := &otherPolicyView{discovery: served, lister: fakePolicyLister{objects: map[schema.GroupVersionResource]map[string][]*unstructured.Unstructured{gnp: {"": {calicoEgressDeny}}}}}
	if view.mayApply("shop", pods, ns, netpol.DirectionEgress) {
		t.Error("Calico egress policy with only Deny and Log rules counted for outbound")
	}
}

// Lists are read once per scan, and a cluster-wide Calico policy compiled for
// known namespace labels isn't reused for a namespace whose labels are unknown.
func TestOtherPolicyViewMemoizes(t *testing.T) {
	gnp := schema.GroupVersionResource{Group: "crd.projectcalico.org", Version: "v1", Resource: "globalnetworkpolicies"}
	calls := 0
	lister := fakePolicyLister{calls: &calls, objects: map[schema.GroupVersionResource]map[string][]*unstructured.Unstructured{
		gnp: {"": {policyObject(map[string]any{"namespaceSelector": "team == 'other'", "ingress": []any{map[string]any{"action": "Allow"}}})}},
	}}
	view := &otherPolicyView{discovery: fakePolicyDiscovery{"crd.projectcalico.org/GlobalNetworkPolicy": gnp}, lister: lister}
	pods := []*corev1.Pod{netpolTestPod("rec-1", map[string]string{"app": "rec"}, true)}
	known := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop", Labels: map[string]string{"team": "shop"}}}

	steps := []struct {
		ns   *corev1.Namespace
		want bool
	}{{known, false}, {nil, true}, {known, false}}
	for i, step := range steps {
		if got := view.mayApply("shop", pods, step.ns, netpol.DirectionIngress); got != step.want {
			t.Errorf("step %d: mayApply = %v, want %v", i, got, step.want)
		}
	}
	if calls != 1 {
		t.Errorf("List called %d times, want 1 for the whole scan", calls)
	}
}

// The detection links the isolated Services, their pods' owners and the pods,
// so the issue also shows on those resources.
func TestDenyAllIngressAffectedRefs(t *testing.T) {
	pods := []*corev1.Pod{
		netpolTestPod("rec-b", map[string]string{"app": "rec"}, true),
		netpolTestPod("rec-a", map[string]string{"app": "rec"}, true),
	}
	env := denyAllEnv{ownerOf: func(*corev1.Pod) (string, string, string) { return "apps", "Deployment", "recommendation" }}
	got := denyAllIngressDetections([]*networkingv1.NetworkPolicy{netpolTestPolicy("deny-all", map[string]string{"app": "rec"}, ingressOnly)},
		[]*corev1.Service{netpolTestService("recommendation", map[string]string{"app": "rec"})}, pods, env, time.Now())
	if len(got) != 1 {
		t.Fatalf("got %d detections", len(got))
	}
	want := []issuesapi.Ref{
		{Kind: "Service", Namespace: "shop", Name: "recommendation"},
		{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "recommendation"},
		{Kind: "Pod", Namespace: "shop", Name: "rec-a"},
		{Kind: "Pod", Namespace: "shop", Name: "rec-b"},
	}
	if !reflect.DeepEqual(got[0].AffectedRefs, want) {
		t.Fatalf("AffectedRefs = %+v, want %+v", got[0].AffectedRefs, want)
	}
}

// The cause names the Service, the pod count and the outbound effect on its
// own, because the UI shows it instead of the message.
func TestDenyAllIngressCauseStandsAlone(t *testing.T) {
	pods := []*corev1.Pod{netpolTestPod("rec-1", map[string]string{"app": "rec"}, true), netpolTestPod("rec-2", map[string]string{"app": "rec"}, true)}
	got := denyAllIngressDetections([]*networkingv1.NetworkPolicy{netpolTestPolicy("deny-all", map[string]string{"app": "rec"}, ingressAndEgress)},
		[]*corev1.Service{netpolTestService("recommendation", map[string]string{"app": "rec"})}, pods, denyAllEnv{}, time.Now())
	if len(got) != 1 {
		t.Fatalf("got %d detections", len(got))
	}
	for _, want := range []string{"leaves the 2 pods behind Service recommendation with no allowed inbound traffic", "outbound traffic, including DNS, would be blocked too"} {
		if !strings.Contains(got[0].Cause, want) {
			t.Errorf("cause %q missing %q", got[0].Cause, want)
		}
	}
	if got[0].MessageSource != issuesapi.MessageSourceRadar {
		t.Errorf("MessageSource = %q, want radar", got[0].MessageSource)
	}
}

func TestDenyAllIngressOnsetIsApplied(t *testing.T) {
	now := time.Now()
	pods := []*corev1.Pod{netpolTestPod("rec-1", map[string]string{"app": "rec"}, true)}
	services := []*corev1.Service{netpolTestService("recommendation", map[string]string{"app": "rec"})}
	policies := []*networkingv1.NetworkPolicy{netpolTestPolicy("deny-all", map[string]string{"app": "rec"}, ingressOnly)}
	known := func(*networkingv1.NetworkPolicy, *denyAllIngressImpact) (time.Time, bool) {
		return now.Add(-3 * time.Minute), true
	}
	unknown := func(*networkingv1.NetworkPolicy, *denyAllIngressImpact) (time.Time, bool) {
		return time.Time{}, false
	}

	got := denyAllIngressDetections(policies, services, pods, denyAllEnv{isolationOnset: known}, now)
	if d := got[0]; d.OnsetUnknown || !d.OnsetAt.Equal(now.Add(-3*time.Minute).UTC()) || d.IssueTiming != "started_at_resource_creation" || d.IssueTimingBasis != "spec" {
		t.Fatalf("known onset not applied: %+v", d)
	}
	got = denyAllIngressDetections(policies, services, pods, denyAllEnv{isolationOnset: unknown}, now)
	if d := got[0]; !d.OnsetUnknown || d.IssueTiming != "" {
		t.Fatalf("unknown onset was claimed: %+v", d)
	}
}

// The isolation is dated from the policy's creation only when creating it is
// established as the start: spec never edited, watched from before, nothing
// it applies to newer, and no other policy or affected Service changed since.
func TestNetworkPolicyIsolationOnset(t *testing.T) {
	now := time.Now()
	created := now.Add(-time.Hour)
	window := isolationWindow{since: created.Add(-time.Hour), policyKinds: []string{"NetworkPolicy", "CiliumNetworkPolicy"}}
	appendEvent := func(e timeline.TimelineEvent) {
		t.Helper()
		e.Source, e.ClusterContext, e.Namespace = timeline.SourceInformer, ActiveClusterContext(), "shop"
		if err := timeline.GetStore().Append(context.Background(), e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	setup := func(t *testing.T) {
		t.Helper()
		timeline.ResetStore()
		if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 50}); err != nil {
			t.Fatalf("InitStore: %v", err)
		}
	}
	t.Cleanup(timeline.ResetStore)
	policy := func(generation int64) *networkingv1.NetworkPolicy {
		np := netpolTestPolicy("deny-all", map[string]string{"app": "rec"}, ingressOnly)
		np.CreationTimestamp, np.Generation = metav1.NewTime(created), generation
		return np
	}
	recSelector := labels.SelectorFromSet(map[string]string{"app": "rec"})
	olderObjects := &denyAllIngressImpact{services: []string{"recommendation"}, selectors: []labels.Selector{recSelector}, lastCreated: created.Add(-time.Hour)}

	t.Run("unchanged policy, watched since before it existed", func(t *testing.T) {
		setup(t)
		appendEvent(timeline.TimelineEvent{ID: "own-add", Timestamp: created, Kind: "NetworkPolicy", APIVersion: "networking.k8s.io/v1", Name: "deny-all", EventType: timeline.EventTypeAdd})
		// A pod the Services don't select is irrelevant.
		appendEvent(timeline.TimelineEvent{ID: "other-pod", Timestamp: created.Add(time.Minute), Kind: "Pod", APIVersion: "v1", Name: "web-1", EventType: timeline.EventTypeUpdate, Labels: map[string]string{"app": "web"}})
		if got, ok := networkPolicyIsolationOnset(window, policy(1), olderObjects); !ok || !got.Equal(created) {
			t.Fatalf("got %v %v, want creation", got, ok)
		}
	})
	for _, tt := range []struct {
		name       string
		window     isolationWindow
		generation int64
		impact     *denyAllIngressImpact
		event      *timeline.TimelineEvent
	}{
		{name: "not watched since before the policy existed", window: isolationWindow{since: created.Add(-30 * time.Second), policyKinds: window.policyKinds}, generation: 1, impact: olderObjects},
		{name: "no known watched window", generation: 1, impact: olderObjects},
		{name: "spec edited since creation", window: window, generation: 2, impact: olderObjects},
		{name: "a pod or Service newer than the policy", window: window, generation: 1,
			impact: &denyAllIngressImpact{services: []string{"recommendation"}, lastCreated: created.Add(time.Minute)}},
		{name: "another policy deleted afterwards", window: window, generation: 1, impact: olderObjects,
			event: &timeline.TimelineEvent{ID: "allow-gone", Timestamp: created.Add(10 * time.Minute), Kind: "NetworkPolicy", APIVersion: "networking.k8s.io/v1", Name: "allow-web", EventType: timeline.EventTypeDelete}},
		{name: "another policy added at the same moment", window: window, generation: 1, impact: olderObjects,
			event: &timeline.TimelineEvent{ID: "same-time", Timestamp: created, Kind: "NetworkPolicy", APIVersion: "networking.k8s.io/v1", Name: "allow-web", EventType: timeline.EventTypeAdd}},
		{name: "a Cilium allow policy deleted afterwards", window: window, generation: 1, impact: olderObjects,
			event: &timeline.TimelineEvent{ID: "cnp-gone", Timestamp: created.Add(10 * time.Minute), Kind: "CiliumNetworkPolicy", APIVersion: "cilium.io/v2", Name: "allow-rec", EventType: timeline.EventTypeDelete}},
		{name: "the namespace relabeled afterwards", window: window, generation: 1, impact: olderObjects,
			event: &timeline.TimelineEvent{ID: "ns-relabel", Timestamp: created.Add(10 * time.Minute), Kind: "Namespace", APIVersion: "v1", Name: "shop", EventType: timeline.EventTypeUpdate}},
		{name: "a backend pod went away afterwards", window: window, generation: 1, impact: olderObjects,
			event: &timeline.TimelineEvent{ID: "pod-gone", Timestamp: created.Add(5 * time.Minute), Kind: "Pod", APIVersion: "v1", Name: "rec-old", EventType: timeline.EventTypeDelete, Labels: map[string]string{"app": "rec"}}},
		{name: "a selected pod no policy isolates (readiness could have started it)", window: window, generation: 1,
			impact: &denyAllIngressImpact{services: []string{"recommendation"}, selectors: []labels.Selector{recSelector}, lastCreated: created.Add(-time.Hour), readinessDependent: true}},
		{name: "affected Service changed afterwards", window: window, generation: 1, impact: olderObjects,
			event: &timeline.TimelineEvent{ID: "svc-edit", Timestamp: created.Add(5 * time.Minute), Kind: "Service", APIVersion: "v1", Name: "recommendation", EventType: timeline.EventTypeUpdate}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			setup(t)
			if tt.event != nil {
				appendEvent(*tt.event)
			}
			if got, ok := networkPolicyIsolationOnset(tt.window, policy(tt.generation), tt.impact); ok {
				t.Fatalf("claimed onset %v", got)
			}
		})
	}
}

// The watch window needs the recorder running and the NetworkPolicy informer
// synced, and is gone once the store has evicted anything or another served
// policy API isn't being watched.
func TestNetworkPolicyWatchWindow(t *testing.T) {
	defer ResetTestState()
	timeline.ResetStore()
	t.Cleanup(func() { timeline.ResetStore(); timeline.SetObservationStartForTest(time.Time{}) })
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 2}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	client := fake.NewClientset()
	if err := InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()
	deadline := time.Now().Add(2 * time.Second)
	for !cache.IsKindReady("networkpolicies") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	setDiscovery := func(lists ...*metav1.APIResourceList) {
		t.Helper()
		fakeDisc := client.Discovery().(*fakediscovery.FakeDiscovery)
		fakeDisc.Resources = lists
		restore, err := SetTestResourceDiscovery(fakeDisc)
		if err != nil {
			t.Fatalf("SetTestResourceDiscovery: %v", err)
		}
		t.Cleanup(restore)
	}
	core := &metav1.APIResourceList{GroupVersion: "networking.k8s.io/v1", APIResources: []metav1.APIResource{
		{Name: "networkpolicies", Kind: "NetworkPolicy", Namespaced: true, Verbs: metav1.Verbs{"get", "list", "watch"}},
	}}
	start := time.Now().Add(-time.Hour)
	timeline.SetObservationStartForTest(start)

	setDiscovery(core)
	got := networkPolicyWatchWindow(cache)
	if got.since.IsZero() || got.since.Before(start) || len(got.policyKinds) != 1 {
		t.Fatalf("window = %+v, want the later of observation start and informer sync, core kind only", got)
	}

	setDiscovery(core, &metav1.APIResourceList{GroupVersion: "cilium.io/v2", APIResources: []metav1.APIResource{
		{Name: "ciliumnetworkpolicies", Kind: "CiliumNetworkPolicy", Namespaced: true, Verbs: metav1.Verbs{"get", "list", "watch"}},
	}})
	if got := networkPolicyWatchWindow(cache); !got.since.IsZero() {
		t.Fatalf("window = %+v while a served Cilium policy API isn't watched", got)
	}

	setDiscovery(core)
	for i := range 3 {
		if err := timeline.GetStore().Append(context.Background(), timeline.TimelineEvent{
			ID: fmt.Sprintf("e%d", i), Timestamp: time.Now(), Source: timeline.SourceInformer, ClusterContext: ActiveClusterContext(),
			Kind: "ConfigMap", Namespace: "shop", Name: "x", EventType: timeline.EventTypeUpdate,
		}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if got := networkPolicyWatchWindow(cache); !got.since.IsZero() {
		t.Fatalf("window = %+v after the store evicted events", got)
	}
}

// With publishNotReadyAddresses, only not-ready pods that still get an
// endpoint count as backends: a failed or IP-less pod doesn't.
func TestDenyAllIngressPublishNotReadyBackends(t *testing.T) {
	svc := netpolTestService("headless", map[string]string{"app": "rec"})
	svc.Spec.PublishNotReadyAddresses = true
	policies := []*networkingv1.NetworkPolicy{netpolTestPolicy("deny-all", map[string]string{"app": "rec"}, ingressOnly)}
	pod := func(phase corev1.PodPhase, ip string) *corev1.Pod {
		p := netpolTestPod("rec-1", map[string]string{"app": "rec"}, false)
		p.Status.Phase, p.Status.PodIP = phase, ip
		return p
	}
	if got := denyAllIngressDetections(policies, []*corev1.Service{svc}, []*corev1.Pod{pod(corev1.PodRunning, "10.0.0.1")}, denyAllEnv{}, time.Now()); len(got) != 1 {
		t.Fatalf("not-ready pod with an IP: got %d detections, want 1", len(got))
	}
	for name, p := range map[string]*corev1.Pod{"failed": pod(corev1.PodFailed, "10.0.0.1"), "no IP": pod(corev1.PodPending, "")} {
		if got := denyAllIngressDetections(policies, []*corev1.Service{svc}, []*corev1.Pod{p}, denyAllEnv{}, time.Now()); len(got) != 0 {
			t.Errorf("%s pod counted as a backend: %+v", name, got)
		}
	}
}

// A not-ready pod that no policy isolates makes the isolation depend on
// readiness, which leaves no recorded event, so the onset can't be dated.
func TestDenyAllIngressReadinessDependent(t *testing.T) {
	svc := netpolTestService("rec", map[string]string{"app": "rec"})
	ready := netpolTestPod("rec-a", map[string]string{"app": "rec", "tier": "a"}, true)
	notReady := netpolTestPod("rec-b", map[string]string{"app": "rec", "tier": "b"}, false)
	for _, tt := range []struct {
		name     string
		selector map[string]string
		want     bool
	}{
		{name: "the not-ready pod is allowed", selector: map[string]string{"tier": "a"}, want: true},
		{name: "every selected pod is isolated", selector: map[string]string{"app": "rec"}, want: false},
	} {
		var got []bool
		env := denyAllEnv{isolationOnset: func(_ *networkingv1.NetworkPolicy, impact *denyAllIngressImpact) (time.Time, bool) {
			got = append(got, impact.readinessDependent)
			return time.Time{}, false
		}}
		policies := []*networkingv1.NetworkPolicy{netpolTestPolicy("deny", tt.selector, ingressOnly)}
		if d := denyAllIngressDetections(policies, []*corev1.Service{svc}, []*corev1.Pod{ready, notReady}, env, time.Now()); len(d) != 1 {
			t.Fatalf("%s: got %d detections, want 1", tt.name, len(d))
		}
		if len(got) != 1 || got[0] != tt.want {
			t.Errorf("%s: readinessDependent = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// The copy agrees in number with the isolated pods.
func TestDenyAllIngressCopyAgreesInNumber(t *testing.T) {
	policies := []*networkingv1.NetworkPolicy{netpolTestPolicy("default-deny", nil, ingressAndEgress)}
	services := []*corev1.Service{netpolTestService("rec", map[string]string{"app": "rec"})}
	for _, tt := range []struct {
		pods  int
		want  []string
		avoid []string
	}{
		{pods: 1,
			want:  []string{"the 1 pod behind Service rec: no NetworkPolicy selecting it allows any ingress; its outbound traffic", "it isolates it for ingress", "New connections to it would be dropped", "so this pod needs a NetworkPolicy that allows its expected clients"},
			avoid: []string{"them", "their", "these pods"}},
		{pods: 2,
			want: []string{"the 2 pods behind Service rec: no NetworkPolicy selecting them allows any ingress; their outbound traffic", "it isolates them for ingress", "so these pods need a NetworkPolicy that allows their expected clients"}},
	} {
		var pods []*corev1.Pod
		for i := 0; i < tt.pods; i++ {
			pods = append(pods, netpolTestPod(fmt.Sprintf("rec-%d", i), map[string]string{"app": "rec"}, true))
		}
		got := denyAllIngressDetections(policies, services, pods, denyAllEnv{}, time.Now())
		if len(got) != 1 {
			t.Fatalf("%d pods: got %d detections, want 1", tt.pods, len(got))
		}
		text := got[0].Message + " | " + got[0].Cause + " | " + got[0].Action
		for _, w := range tt.want {
			if !strings.Contains(text, w) {
				t.Errorf("%d pods: missing %q in %q", tt.pods, w, text)
			}
		}
		for _, w := range tt.avoid {
			if strings.Contains(text, " "+w+" ") {
				t.Errorf("%d pods: %q should not appear in %q", tt.pods, w, text)
			}
		}
	}
}

// Two deny-all policies that split a Service's pods each say they cover only
// part of them, and removing one alone frees only some backends.
func TestDenyAllIngressSplitSelectors(t *testing.T) {
	pods := []*corev1.Pod{
		netpolTestPod("rec-a", map[string]string{"app": "rec", "tier": "a"}, true),
		netpolTestPod("rec-b", map[string]string{"app": "rec", "tier": "b"}, true),
	}
	got := denyAllIngressDetections([]*networkingv1.NetworkPolicy{
		netpolTestPolicy("deny-a", map[string]string{"tier": "a"}, ingressOnly),
		netpolTestPolicy("deny-b", map[string]string{"tier": "b"}, ingressOnly),
	}, []*corev1.Service{netpolTestService("rec", map[string]string{"app": "rec"})}, pods, denyAllEnv{}, time.Now())
	if len(got) != 2 {
		t.Fatalf("got %d detections, want one per policy", len(got))
	}
	for _, d := range got {
		if !strings.Contains(d.Message, "1 of the 2 pods behind Service rec") || !strings.Contains(d.Action, "Other policies isolate the remaining backend pods, so removing this one alone restores only some backends") {
			t.Errorf("%s: message %q / action %q", d.Name, d.Message, d.Action)
		}
	}
}

// A capped event query can't show the window is clean, so no onset.
func TestNetworkPolicyIsolationOnsetQueryCap(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 1000}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	created := time.Now().Add(-time.Hour)
	for i := range 500 {
		if err := timeline.GetStore().Append(context.Background(), timeline.TimelineEvent{
			ID: fmt.Sprintf("np-%d", i), Timestamp: created.Add(-time.Duration(i+1) * time.Second).Add(time.Hour), Source: timeline.SourceInformer,
			ClusterContext: ActiveClusterContext(), Kind: "NetworkPolicy", APIVersion: "networking.k8s.io/v1", Namespace: "other", Name: fmt.Sprintf("p%d", i), EventType: timeline.EventTypeUpdate,
		}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	np := netpolTestPolicy("deny-all", map[string]string{"app": "rec"}, ingressOnly)
	np.CreationTimestamp, np.Generation = metav1.NewTime(created), 1
	window := isolationWindow{since: created.Add(-time.Hour), policyKinds: []string{"NetworkPolicy"}}
	if _, ok := networkPolicyIsolationOnset(window, np, &denyAllIngressImpact{lastCreated: created.Add(-time.Hour)}); ok {
		t.Fatal("claimed an onset although the event query hit its cap")
	}
}

// A pod relabeled into or out of a Service's selector after the policy was
// created moves the isolation's start, so the onset is withheld. Driven
// through the real recording path: label-only Pod edits must be recorded.
func TestNetworkPolicyIsolationOnsetSeesPodRelabels(t *testing.T) {
	created := time.Now().Add(-time.Hour)
	np := netpolTestPolicy("deny-all", map[string]string{"app": "rec"}, ingressOnly)
	np.CreationTimestamp, np.Generation = metav1.NewTime(created), 1
	window := isolationWindow{since: created.Add(-time.Hour), policyKinds: []string{"NetworkPolicy"}}
	impact := &denyAllIngressImpact{services: []string{"recommendation"}, selectors: []labels.Selector{labels.SelectorFromSet(map[string]string{"app": "rec"})}, lastCreated: created.Add(-time.Hour)}

	relabel := func(t *testing.T, from, to map[string]string) {
		t.Helper()
		timeline.ResetStore()
		if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 10}); err != nil {
			t.Fatalf("InitStore: %v", err)
		}
		oldPod := netpolTestPod("pod-1", from, true)
		newPod := oldPod.DeepCopy()
		newPod.Labels = to
		recordToTimelineStore(ActiveClusterContext(), "Pod", "shop", "pod-1", "uid-1", "update", oldPod, newPod, nil, false)
	}
	t.Cleanup(timeline.ResetStore)

	t.Run("relabeled into the selector", func(t *testing.T) {
		relabel(t, map[string]string{"app": "other"}, map[string]string{"app": "rec"})
		if _, ok := networkPolicyIsolationOnset(window, np, impact); ok {
			t.Fatal("claimed an onset although a pod joined the Service afterwards")
		}
	})
	t.Run("relabeled out of the selector", func(t *testing.T) {
		relabel(t, map[string]string{"app": "rec"}, map[string]string{"app": "other"})
		if _, ok := networkPolicyIsolationOnset(window, np, impact); ok {
			t.Fatal("claimed an onset although a backend left the Service afterwards")
		}
	})
	t.Run("unrelated pod relabeled", func(t *testing.T) {
		relabel(t, map[string]string{"app": "web"}, map[string]string{"app": "web", "tier": "front"})
		if got, ok := networkPolicyIsolationOnset(window, np, impact); !ok || !got.Equal(created) {
			t.Fatalf("got %v %v, want creation: the relabeled pod never matched", got, ok)
		}
	})
}

func netpolTestObjects() (*corev1.Pod, *corev1.Service, *networkingv1.NetworkPolicy) {
	return netpolTestPod("rec-1", map[string]string{"app": "rec"}, true),
		netpolTestService("recommendation", map[string]string{"app": "rec"}),
		netpolTestPolicy("deny-all-rec", map[string]string{"app": "rec"}, ingressAndEgress)
}

func netpolDetections(cache *ResourceCache) []Detection {
	var out []Detection
	for _, d := range DetectProblems(cache, "shop") {
		if d.Reason == NetworkPolicyDenyAllIngressReason {
			out = append(out, d)
		}
	}
	return out
}

func installCoreOnlyDiscovery(t *testing.T, client *fake.Clientset) {
	t.Helper()
	fakeDisc := client.Discovery().(*fakediscovery.FakeDiscovery)
	fakeDisc.Resources = []*metav1.APIResourceList{{GroupVersion: "networking.k8s.io/v1", APIResources: []metav1.APIResource{
		{Name: "networkpolicies", Kind: "NetworkPolicy", Namespaced: true, Verbs: metav1.Verbs{"get", "list", "watch"}},
	}}}
	restore, err := SetTestResourceDiscovery(fakeDisc)
	if err != nil {
		t.Fatalf("SetTestResourceDiscovery: %v", err)
	}
	t.Cleanup(restore)
}

// Without permission to list NetworkPolicies there is no informer, and the
// detector says nothing rather than guessing.
func TestDetectNetworkPolicyDenyAllWithoutPolicyAccess(t *testing.T) {
	defer ResetTestState()
	pod, svc, np := netpolTestObjects()
	client := fake.NewClientset(pod, svc, np)
	scopes := map[string]k8score.ResourceScope{}
	for kind := range allTestResourceTypes() {
		if kind != "networkpolicies" {
			scopes[kind] = k8score.ResourceScope{Enabled: true}
		}
	}
	if err := InitScopedTestResourceCache(client, scopes); err != nil {
		t.Fatalf("InitScopedTestResourceCache: %v", err)
	}
	installCoreOnlyDiscovery(t, client)
	cache := GetResourceCache()
	if cache.NetworkPolicies() != nil {
		t.Fatal("NetworkPolicy informer exists although access was denied")
	}
	time.Sleep(200 * time.Millisecond)
	if got := netpolDetections(cache); len(got) != 0 {
		t.Fatalf("detections without NetworkPolicy access: %+v", got)
	}
}

// While the NetworkPolicy informer hasn't synced, a partial policy list could
// hold a deny-all without its allow, so the detector waits.
func TestDetectNetworkPolicyDenyAllWaitsForSync(t *testing.T) {
	defer ResetTestState()
	pod, svc, np := netpolTestObjects()
	client := fake.NewClientset(pod, svc, np)
	if err := InitTestPromotedSyncingCache(client, 5*time.Second, 5*time.Second, map[string]time.Duration{"networkpolicies": 2 * time.Second}); err != nil {
		t.Fatalf("InitTestPromotedSyncingCache: %v", err)
	}
	installCoreOnlyDiscovery(t, client)
	cache := GetResourceCache()
	if cache.IsKindReady("networkpolicies") {
		t.Skip("NetworkPolicy informer synced before the check; the delay didn't hold")
	}
	if got := netpolDetections(cache); len(got) != 0 {
		t.Fatalf("detections before the NetworkPolicy informer synced: %+v", got)
	}
	deadline := time.Now().Add(6 * time.Second)
	for time.Now().Before(deadline) {
		if len(netpolDetections(cache)) == 1 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("no detection after the NetworkPolicy informer synced")
}

// A namespace with Services but no policies yields nothing.
func TestDenyAllIngressNoPolicies(t *testing.T) {
	pod, svc, _ := netpolTestObjects()
	if got := denyAllIngressDetections(nil, []*corev1.Service{svc}, []*corev1.Pod{pod}, denyAllEnv{}, time.Now()); len(got) != 0 {
		t.Fatalf("detections without policies: %+v", got)
	}
}
