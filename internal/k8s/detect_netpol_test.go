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
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
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
		{"Calico global policy selecting the pods", served, fakePolicyLister{objects: objects(gnp, "", policyObject(map[string]any{"selector": "app == 'rec'"}))}, shopNS, true},
		{"Calico global policy selecting other pods", served, fakePolicyLister{objects: objects(gnp, "", policyObject(map[string]any{"selector": "app == 'web'"}))}, shopNS, false},
		{"Calico global policy for another namespace", served, fakePolicyLister{objects: objects(gnp, "", policyObject(map[string]any{"namespaceSelector": "team == 'other'"}))}, shopNS, false},
		{"Calico namespace selector with unknown namespace labels", served, fakePolicyLister{objects: objects(gnp, "", policyObject(map[string]any{"namespaceSelector": "team == 'other'"}))}, nil, true},
		{"Calico service-account selector can't be judged", served, fakePolicyLister{objects: objects(gnp, "", policyObject(map[string]any{"serviceAccountSelector": "role == 'x'"}))}, shopNS, true},
		{"Calico egress-only policy can't allow inbound", served, fakePolicyLister{objects: objects(gnp, "", policyObject(map[string]any{"selector": "app == 'rec'", "types": []any{"Egress"}}))}, shopNS, false},
		{"Calico policy with only egress rules defaults to Egress", served, fakePolicyLister{objects: objects(gnp, "", policyObject(map[string]any{"selector": "app == 'rec'", "egress": []any{map[string]any{"action": "Allow"}}}))}, shopNS, false},
		{"Calico selector on the endpoint service-account label", served, fakePolicyLister{objects: objects(gnp, "", policyObject(map[string]any{"selector": "projectcalico.org/serviceaccount == 'default'"}))}, shopNS, true},
		{"Calico service-account selector plus an excluding namespace selector", served, fakePolicyLister{objects: objects(gnp, "", policyObject(map[string]any{"namespaceSelector": "team == 'other'", "serviceAccountSelector": "role == 'x'"}))}, shopNS, false},
		{"Calico selector Radar can't parse", served, fakePolicyLister{objects: objects(gnp, "", policyObject(map[string]any{"selector": "app ==="}))}, shopNS, true},
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
}

// Lists are read once per scan, and a cluster-wide Calico policy compiled for
// known namespace labels isn't reused for a namespace whose labels are unknown.
func TestOtherPolicyViewMemoizes(t *testing.T) {
	gnp := schema.GroupVersionResource{Group: "crd.projectcalico.org", Version: "v1", Resource: "globalnetworkpolicies"}
	calls := 0
	lister := fakePolicyLister{calls: &calls, objects: map[schema.GroupVersionResource]map[string][]*unstructured.Unstructured{
		gnp: {"": {policyObject(map[string]any{"namespaceSelector": "team == 'other'"})}},
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
	known := func(*networkingv1.NetworkPolicy, *denyAllIngressImpact) (time.Time, string, bool) {
		return now.Add(-3 * time.Minute), "started_at_resource_creation", true
	}
	unknown := func(*networkingv1.NetworkPolicy, *denyAllIngressImpact) (time.Time, string, bool) {
		return time.Time{}, "", false
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

// The isolation is dated only when the policy is established as its start:
// watched from before, with nothing it applies to newer, and no other policy
// in the namespace changed afterwards.
func TestNetworkPolicyIsolationOnset(t *testing.T) {
	now := time.Now()
	created := now.Add(-time.Hour)
	appendEvent := func(e timeline.TimelineEvent) {
		t.Helper()
		e.Source, e.ClusterContext, e.Kind, e.APIVersion, e.Namespace = timeline.SourceInformer, ActiveClusterContext(), "NetworkPolicy", "networking.k8s.io/v1", "shop"
		if err := timeline.GetStore().Append(context.Background(), e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	var watchedSince time.Time
	setup := func(t *testing.T, observedFrom time.Time) {
		t.Helper()
		timeline.ResetStore()
		if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 50}); err != nil {
			t.Fatalf("InitStore: %v", err)
		}
		watchedSince = observedFrom
	}
	t.Cleanup(timeline.ResetStore)
	policy := func(generation int64) *networkingv1.NetworkPolicy {
		np := netpolTestPolicy("deny-all", map[string]string{"app": "rec"}, ingressOnly)
		np.CreationTimestamp, np.Generation = metav1.NewTime(created), generation
		return np
	}
	olderObjects := &denyAllIngressImpact{lastCreated: created.Add(-time.Hour)}
	removeRules := &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "spec.ingress", OldValue: []any{"from anywhere ports TCP/80"}, NewValue: []any{}}}}

	t.Run("unchanged policy watched since before it existed", func(t *testing.T) {
		setup(t, created.Add(-time.Hour))
		got, timing, ok := networkPolicyIsolationOnset(watchedSince, policy(1), olderObjects)
		if !ok || !got.Equal(created) || timing != "started_at_resource_creation" {
			t.Fatalf("got %v %q %v, want creation", got, timing, ok)
		}
	})
	t.Run("not watched since before the policy existed", func(t *testing.T) {
		setup(t, created.Add(-30*time.Second))
		if _, _, ok := networkPolicyIsolationOnset(watchedSince, policy(1), olderObjects); ok {
			t.Fatal("claimed an onset without watching the whole window")
		}
	})
	t.Run("a pod or Service newer than the policy", func(t *testing.T) {
		setup(t, created.Add(-time.Hour))
		if _, _, ok := networkPolicyIsolationOnset(watchedSince, policy(1), &denyAllIngressImpact{lastCreated: created.Add(time.Minute)}); ok {
			t.Fatal("claimed an onset earlier than an object it applies to")
		}
	})
	t.Run("another policy deleted afterwards", func(t *testing.T) {
		setup(t, created.Add(-time.Hour))
		appendEvent(timeline.TimelineEvent{ID: "allow-gone", Timestamp: created.Add(10 * time.Minute), Name: "allow-web", EventType: timeline.EventTypeDelete})
		if _, _, ok := networkPolicyIsolationOnset(watchedSince, policy(1), olderObjects); ok {
			t.Fatal("claimed creation as onset although an allow policy was deleted later")
		}
	})
	t.Run("another policy added at the same moment", func(t *testing.T) {
		setup(t, created.Add(-time.Hour))
		appendEvent(timeline.TimelineEvent{ID: "same-time", Timestamp: created, Name: "allow-web", EventType: timeline.EventTypeAdd})
		if _, _, ok := networkPolicyIsolationOnset(watchedSince, policy(1), olderObjects); ok {
			t.Fatal("an equal timestamp can't establish order, but an onset was claimed")
		}
	})
	t.Run("edit that removed the last ingress rules", func(t *testing.T) {
		setup(t, created.Add(-time.Hour))
		edited := created.Add(20 * time.Minute)
		appendEvent(timeline.TimelineEvent{ID: "edit", Timestamp: edited, Name: "deny-all", EventType: timeline.EventTypeUpdate, Diff: removeRules})
		got, timing, ok := networkPolicyIsolationOnset(watchedSince, policy(2), olderObjects)
		if !ok || !got.Equal(edited) || timing != "started_after_resource_was_healthy" {
			t.Fatalf("got %v %q %v, want the edit", got, timing, ok)
		}
	})
	t.Run("newest edit didn't change ingress", func(t *testing.T) {
		setup(t, created.Add(-time.Hour))
		appendEvent(timeline.TimelineEvent{ID: "edit", Timestamp: created.Add(20 * time.Minute), Name: "deny-all", EventType: timeline.EventTypeUpdate, Diff: removeRules})
		appendEvent(timeline.TimelineEvent{ID: "egress", Timestamp: created.Add(30 * time.Minute), Name: "deny-all", EventType: timeline.EventTypeUpdate,
			Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "spec.egress", OldValue: []any{}, NewValue: []any{"to anywhere ports all"}}}}})
		if _, _, ok := networkPolicyIsolationOnset(watchedSince, policy(3), olderObjects); ok {
			t.Fatal("dated the isolation from an edit that didn't start it")
		}
	})
}

// The watched window needs the recorder running and the NetworkPolicy
// informer synced, and starts no earlier than the oldest event the store still
// holds once it has evicted any.
func TestNetworkPolicyWatchedSince(t *testing.T) {
	defer ResetTestState()
	timeline.ResetStore()
	t.Cleanup(func() { timeline.ResetStore(); timeline.SetObservationStartForTest(time.Time{}) })
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 2}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	if err := InitTestResourceCache(fake.NewClientset()); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := GetResourceCache()
	deadline := time.Now().Add(2 * time.Second)
	for !cache.IsKindReady("networkpolicies") && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	start := time.Now().Add(-time.Hour)
	timeline.SetObservationStartForTest(start)
	got := networkPolicyWatchedSince(cache)
	if got.IsZero() || got.Before(start) {
		t.Fatalf("watched since %v, want the later of observation start and informer sync", got)
	}
	for i := range 3 {
		if err := timeline.GetStore().Append(context.Background(), timeline.TimelineEvent{
			ID: fmt.Sprintf("e%d", i), Timestamp: time.Now(), Source: timeline.SourceInformer, ClusterContext: ActiveClusterContext(),
			Kind: "ConfigMap", Namespace: "shop", Name: "x", EventType: timeline.EventTypeUpdate,
		}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	if got := networkPolicyWatchedSince(cache); !got.After(time.Now().Add(-time.Minute)) {
		t.Fatalf("watched since %v after the store evicted the window's start, want it moved to the oldest kept event", got)
	}
}
