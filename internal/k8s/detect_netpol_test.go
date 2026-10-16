package k8s

import (
	"errors"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/pkg/k8score"
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
			got := denyAllIngressDetections(tt.policies, tt.services, tt.pods, time.Now())
			var names []string
			for _, d := range got {
				names = append(names, d.Name)
			}
			if strings.Join(names, ",") != strings.Join(tt.wantNames, ",") {
				t.Fatalf("detections = %v, want %v", names, tt.wantNames)
			}
			for _, d := range got {
				if d.Kind != "NetworkPolicy" || d.Group != "networking.k8s.io" || d.Severity != "warning" ||
					d.Reason != NetworkPolicyDenyAllIngressReason || d.Fingerprint != networkPolicyDenyAllIngressFingerprint {
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
	}, services, pods, time.Now())
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
	}, services[:1], pods, time.Now())
	if len(ingress) != 1 || strings.Contains(ingress[0].Message, "outbound") {
		t.Fatalf("ingress-only deny must not claim outbound is blocked: %+v", ingress)
	}

	pair := denyAllIngressDetections([]*networkingv1.NetworkPolicy{
		netpolTestPolicy("default-deny", nil, ingressOnly),
		netpolTestPolicy("deny-in", map[string]string{"app": "rec"}, ingressOnly),
	}, services[:1], pods, time.Now())
	if len(pair) != 2 {
		t.Fatalf("got %d detections, want one per contributing policy", len(pair))
	}
	for i, other := range []string{"deny-in", "default-deny"} {
		if !strings.Contains(pair[i].Cause, "NetworkPolicy "+other+" also isolates some of these pods") {
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
	}, services[:1], pods, time.Now()); !strings.Contains(solo[0].Action, "delete the policy") {
		t.Errorf("lone targeted deny action = %q", solo[0].Action)
	}

	brokenEgress := netpolTestPolicy("broken-egress", nil, egressOnly)
	brokenEgress.Spec.PodSelector = metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "app", Operator: "Bogus"}}}
	undecided := denyAllIngressDetections([]*networkingv1.NetworkPolicy{
		netpolTestPolicy("deny-all", map[string]string{"app": "rec"}, ingressAndEgress), brokenEgress,
	}, services[:1], pods, time.Now())
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
	coreDiscovery, err := k8score.NewResourceDiscovery(fakeDisc)
	if err != nil {
		t.Fatalf("NewResourceDiscovery: %v", err)
	}
	ResetResourceDiscovery()
	resourceDiscovery = &ResourceDiscovery{ResourceDiscovery: coreDiscovery}
	defer ResetResourceDiscovery()
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
}

func (f fakePolicyLister) List(gvr schema.GroupVersionResource, namespace string) ([]*unstructured.Unstructured, error) {
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
	cnp := schema.GroupVersionResource{Group: "cilium.io", Version: "v2", Resource: "ciliumnetworkpolicies"}
	gnp := schema.GroupVersionResource{Group: "crd.projectcalico.org", Version: "v1", Resource: "globalnetworkpolicies"}
	clusterNP := schema.GroupVersionResource{Group: "policy.networking.k8s.io", Version: "v1alpha2", Resource: "clusternetworkpolicies"}
	served := fakePolicyDiscovery{
		"cilium.io/CiliumNetworkPolicy":                 cnp,
		"crd.projectcalico.org/GlobalNetworkPolicy":     gnp,
		"policy.networking.k8s.io/ClusterNetworkPolicy": clusterNP,
	}
	shopPods := []*corev1.Pod{netpolTestPod("rec-1", map[string]string{"app": "rec"}, true)}
	shopLabels := map[string]string{"projectcalico.org/name": "shop", "team": "shop"}
	objects := func(gvr schema.GroupVersionResource, ns string, items ...*unstructured.Unstructured) map[schema.GroupVersionResource]map[string][]*unstructured.Unstructured {
		return map[schema.GroupVersionResource]map[string][]*unstructured.Unstructured{gvr: {ns: items}}
	}

	tests := []struct {
		name   string
		disc   policyAPIDiscovery
		lister policyObjectLister
		labels map[string]string
		want   bool
	}{
		{"no other policy API served", fakePolicyDiscovery{}, nil, shopLabels, false},
		{"served but empty", served, fakePolicyLister{}, shopLabels, false},
		{"Cilium policy in the namespace", served, fakePolicyLister{objects: objects(cnp, "shop", policyObject(nil))}, shopLabels, true},
		{"Cilium policy in another namespace", served, fakePolicyLister{objects: objects(cnp, "other", policyObject(nil))}, shopLabels, false},
		{"admin-tier cluster policy", served, fakePolicyLister{objects: objects(clusterNP, "", policyObject(map[string]any{"tier": "Admin"}))}, shopLabels, true},
		{"baseline-tier cluster policy can't override", served, fakePolicyLister{objects: objects(clusterNP, "", policyObject(map[string]any{"tier": "Baseline"}))}, shopLabels, false},
		{"Calico global policy selecting the pods", served, fakePolicyLister{objects: objects(gnp, "", policyObject(map[string]any{"selector": "app == 'rec'"}))}, shopLabels, true},
		{"Calico global policy selecting other pods", served, fakePolicyLister{objects: objects(gnp, "", policyObject(map[string]any{"selector": "app == 'web'"}))}, shopLabels, false},
		{"Calico global policy for another namespace", served, fakePolicyLister{objects: objects(gnp, "", policyObject(map[string]any{"namespaceSelector": "team == 'other'"}))}, shopLabels, false},
		{"Calico namespace selector with unknown namespace labels", served, fakePolicyLister{objects: objects(gnp, "", policyObject(map[string]any{"namespaceSelector": "team == 'other'"}))}, nil, true},
		{"Calico service-account selector can't be judged", served, fakePolicyLister{objects: objects(gnp, "", policyObject(map[string]any{"serviceAccountSelector": "role == 'x'"}))}, shopLabels, true},
		{"Calico egress-only policy can't allow inbound", served, fakePolicyLister{objects: objects(gnp, "", policyObject(map[string]any{"selector": "app == 'rec'", "types": []any{"Egress"}}))}, shopLabels, false},
		{"Calico policy with only egress rules defaults to Egress", served, fakePolicyLister{objects: objects(gnp, "", policyObject(map[string]any{"selector": "app == 'rec'", "egress": []any{map[string]any{"action": "Allow"}}}))}, shopLabels, false},
		{"Calico selector on the endpoint service-account label", served, fakePolicyLister{objects: objects(gnp, "", policyObject(map[string]any{"selector": "projectcalico.org/serviceaccount == 'default'"}))}, shopLabels, true},
		{"Calico service-account selector plus an excluding namespace selector", served, fakePolicyLister{objects: objects(gnp, "", policyObject(map[string]any{"namespaceSelector": "team == 'other'", "serviceAccountSelector": "role == 'x'"}))}, shopLabels, false},
		{"Calico selector Radar can't parse", served, fakePolicyLister{objects: objects(gnp, "", policyObject(map[string]any{"selector": "app ==="}))}, shopLabels, true},
		{"unsynced is unknown", served, fakePolicyLister{unsynced: true}, shopLabels, true},
		{"list error is unknown", served, fakePolicyLister{err: errors.New("forbidden")}, shopLabels, true},
		{"served without a dynamic cache is unknown", served, nil, shopLabels, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			view := &otherPolicyView{discovery: tt.disc, lister: tt.lister}
			if got := view.mayApply("shop", shopPods, tt.labels); got != tt.want {
				t.Errorf("mayApply = %v, want %v", got, tt.want)
			}
		})
	}
	var noDiscovery *otherPolicyView
	if !noDiscovery.mayApply("shop", shopPods, shopLabels) {
		t.Error("without discovery nothing can be ruled out")
	}
}
