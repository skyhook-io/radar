package k8s

import (
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"
)

func netpolForDiff() *networkingv1.NetworkPolicy {
	port := intstr.FromInt32(8080)
	udp := corev1.ProtocolUDP
	dns := intstr.FromInt32(53)
	return &networkingv1.NetworkPolicy{
		TypeMeta:   metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy"},
		ObjectMeta: metav1.ObjectMeta{Name: "rec", Namespace: "shop"},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "rec"}},
			Ingress: []networkingv1.NetworkPolicyIngressRule{{
				From: []networkingv1.NetworkPolicyPeer{
					{PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "frontend"}}},
					{IPBlock: &networkingv1.IPBlock{CIDR: "10.0.0.0/8", Except: []string{"10.1.0.0/16"}}},
				},
				Ports: []networkingv1.NetworkPolicyPort{{Port: &port}},
			}},
			Egress: []networkingv1.NetworkPolicyEgressRule{{
				To:    []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{}}},
				Ports: []networkingv1.NetworkPolicyPort{{Protocol: &udp, Port: &dns}},
			}},
		},
	}
}

func TestDiffNetworkPolicy(t *testing.T) {
	base := netpolForDiff()

	if changes, _ := diffNetworkPolicy(base, base.DeepCopy()); len(changes) != 0 {
		t.Fatalf("identical policies produced a diff: %+v", changes)
	}

	reordered := base.DeepCopy()
	reordered.Spec.Ingress[0].From[1].IPBlock.Except = []string{"10.2.0.0/16", "10.1.0.0/16"}
	withBoth := base.DeepCopy()
	withBoth.Spec.Ingress[0].From[1].IPBlock.Except = []string{"10.1.0.0/16", "10.2.0.0/16"}
	if changes, _ := diffNetworkPolicy(withBoth, reordered); len(changes) != 0 {
		t.Fatalf("reordering ipBlock.except produced a diff: %+v", changes)
	}

	relabeled := base.DeepCopy()
	relabeled.Labels = map[string]string{"team": "shop"}
	if changes, _ := diffNetworkPolicy(base, relabeled); len(changes) != 0 {
		t.Fatalf("metadata-only change produced a diff: %+v", changes)
	}

	denyAll := base.DeepCopy()
	denyAll.Spec.Ingress = nil
	changes, summary := diffNetworkPolicy(base, denyAll)
	if len(changes) != 1 || changes[0].Path != "spec.ingress" {
		t.Fatalf("changes = %+v, want one spec.ingress change", changes)
	}
	wantOld := []string{"from [10.0.0.0/8 except 10.1.0.0/16; pods app=frontend] ports TCP/8080"}
	if !reflect.DeepEqual(changes[0].OldValue, wantOld) || !reflect.DeepEqual(changes[0].NewValue, []string{}) {
		t.Fatalf("ingress change = %+v, want old %v and no rules", changes[0], wantOld)
	}
	// The summary describes this policy only — other policies may still
	// allow the traffic, so it must not claim inbound is now denied.
	if strings.Join(summary, ", ") != "ingress rules 1→0" {
		t.Fatalf("summary = %v", summary)
	}

	widened := base.DeepCopy()
	widened.Spec.PodSelector = metav1.LabelSelector{}
	widened.Spec.PolicyTypes = []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}
	changes, _ = diffNetworkPolicy(base, widened)
	got := map[string][2]any{}
	for _, c := range changes {
		got[c.Path] = [2]any{c.OldValue, c.NewValue}
	}
	if got["spec.podSelector"] != [2]any{"app=rec", "all pods"} {
		t.Errorf("podSelector change = %v", got["spec.podSelector"])
	}
	// Without explicit policyTypes the egress rule implied Egress isolation;
	// pinning Ingress only drops it.
	if !reflect.DeepEqual(got["spec.policyTypes"], [2]any{[]string{"Ingress", "Egress"}, []string{"Ingress"}}) {
		t.Errorf("policyTypes change = %v", got["spec.policyTypes"])
	}

	egress := base.DeepCopy()
	egress.Spec.Egress[0].To = nil
	changes, _ = diffNetworkPolicy(base, egress)
	if len(changes) != 1 || changes[0].Path != "spec.egress" ||
		!reflect.DeepEqual(changes[0].NewValue, []string{"to anywhere ports UDP/53"}) {
		t.Fatalf("egress change = %+v", changes)
	}
}

// The timeline drops updates with an empty diff for kinds with a registered
// differ, so a rule edit must produce one through ComputeDiff for both typed
// and unstructured inputs.
func TestComputeDiffNetworkPolicy(t *testing.T) {
	base := netpolForDiff()
	denyAll := base.DeepCopy()
	denyAll.Spec.Ingress = nil

	if !KindHasDiffer("NetworkPolicy") {
		t.Fatal("NetworkPolicy has no registered differ")
	}
	if diff := ComputeDiff("NetworkPolicy", base, denyAll); diff == nil || diff.Fields[0].Path != "spec.ingress" {
		t.Fatalf("typed diff = %+v", diff)
	}

	toU := func(np *networkingv1.NetworkPolicy) *unstructured.Unstructured {
		m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(np)
		if err != nil {
			t.Fatalf("ToUnstructured: %v", err)
		}
		return &unstructured.Unstructured{Object: m}
	}
	if diff := ComputeDiff("NetworkPolicy", toU(base), toU(denyAll)); diff == nil || diff.Fields[0].Path != "spec.ingress" {
		t.Fatalf("unstructured diff = %+v", diff)
	}
}

// Pod label edits are recorded: they change which Services and policies
// select the pod.
func TestDiffPodRecordsLabelChanges(t *testing.T) {
	oldPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "rec"}}}
	newPod := oldPod.DeepCopy()
	newPod.Labels = map[string]string{"app": "other"}
	diff := ComputeDiff("Pod", oldPod, newPod)
	if diff == nil || len(diff.Fields) != 1 || diff.Fields[0].Path != "metadata.labels" {
		t.Fatalf("diff = %+v, want one metadata.labels change", diff)
	}
	if ComputeDiff("Pod", oldPod, oldPod.DeepCopy()) != nil {
		t.Fatal("unchanged labels produced a diff")
	}
}
