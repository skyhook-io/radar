package trace

import (
	"context"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/probe"
)

// A deny-all NetworkPolicy issue shows on the Service hop it isolates. The
// Pods hop keeps only its own per-port policy finding, so the same isolation
// isn't reported twice there.
func TestServiceTraceShowsNetworkPolicyIssueOnce(t *testing.T) {
	defer k8s.ResetTestState()
	labels := map[string]string{"app": "rec"}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "recommendation", Namespace: "shop"},
		Spec:       corev1.ServiceSpec{Selector: labels, Ports: []corev1.ServicePort{{Port: 80}}},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "rec-1", Namespace: "shop", Labels: labels},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
		},
	}
	policy := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "deny-all", Namespace: "shop", CreationTimestamp: metav1.NewTime(time.Now().Add(-time.Minute))},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{MatchLabels: labels},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
		},
	}
	client := fake.NewClientset(svc, pod, policy)
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("init: %v", err)
	}
	fakeDisc := client.Discovery().(*fakediscovery.FakeDiscovery)
	fakeDisc.Resources = []*metav1.APIResourceList{{GroupVersion: "networking.k8s.io/v1", APIResources: []metav1.APIResource{
		{Name: "networkpolicies", Kind: "NetworkPolicy", Namespaced: true, Verbs: metav1.Verbs{"get", "list", "watch"}},
	}}}
	restore, err := k8s.SetTestResourceDiscovery(fakeDisc)
	if err != nil {
		t.Fatalf("SetTestResourceDiscovery: %v", err)
	}
	defer restore()

	allow := IssueAccess{
		CanReadClusterScoped: func(string, string) bool { return true },
		CanReadRelated:       func(issues.Ref) bool { return true },
		CanReadSubject:       func(issues.Ref) bool { return true },
		CanReadEvidence:      func(issues.EvidenceRead) bool { return true },
	}
	deps := Deps{Cache: k8s.GetResourceCache(), Dynamic: k8s.GetDynamicResourceCache(), Discovery: k8s.GetResourceDiscovery(), Issues: issues.NewCacheProvider(), IssueAccess: allow}

	codes := func(h Hop) map[string]int {
		out := map[string]int{}
		for _, f := range h.Findings {
			out[f.Code]++
		}
		return out
	}
	var svcHop, podsHop Hop
	waitFor(t, func() bool {
		tr, err := BuildTraceWithOptions(context.Background(), deps, "Service", "shop", "recommendation", Options{})
		if err != nil || len(tr.Downstream) < 2 {
			return false
		}
		svcHop, podsHop = tr.Downstream[0], tr.Downstream[1]
		return codes(svcHop)[k8s.NetworkPolicyDenyAllIngressFingerprint] == 1
	})
	if c := codes(podsHop); c[k8s.NetworkPolicyDenyAllIngressFingerprint] != 0 || c[codePolicyWouldDeny] != 1 {
		t.Fatalf("Pods hop findings = %v, want only netpol:would-deny", c)
	}

	deps.IssueAccess.CanReadRelated = nil
	tr, err := BuildTraceWithOptions(context.Background(), deps, "Service", "shop", "recommendation", Options{})
	if err != nil {
		t.Fatal(err)
	}
	if codes(tr.Downstream[0])[k8s.NetworkPolicyDenyAllIngressFingerprint] != 0 {
		t.Fatal("the policy issue reached the Service hop without a related-resource authorizer")
	}
}

// Traffic that got through to the pods settles the Service hop's policy issues
// (one per policy) into a single note, as it does the Pods hop's per-port
// prediction; a dropped data path keeps them.
func TestReconcilePolicyDowngradesServiceDenyAll(t *testing.T) {
	trace := func(dataOK bool) *Trace {
		svc := Hop{Resource: ResourceRef{Kind: "Service", Namespace: "ns", Name: "rec"}, Edge: "entry:Service", Findings: []Finding{
			{Code: k8s.NetworkPolicyDenyAllIngressFingerprint, Severity: SeverityWarning, Message: "NetworkPolicy denies all ingress - Would block all inbound traffic (default-deny)"},
			{Code: k8s.NetworkPolicyDenyAllIngressFingerprint, Severity: SeverityWarning, Message: "NetworkPolicy denies all ingress - Would block all inbound traffic (deny-rec)"},
		}}
		pods := wouldDenyHop([]probe.Result{
			{Layer: probe.LayerTCP, Target: "10.0.0.5:80", Port: 80, Path: probe.PathData, OK: dataOK},
		}, false)
		pods.Edge = "Service->Pods"
		return &Trace{Downstream: []Hop{svc, pods}}
	}

	tr := trace(true)
	reconcilePolicyFindings(tr)
	if got := tr.Downstream[0].Findings; len(got) != 1 || got[0].Severity != SeverityInfo || !strings.Contains(got[0].Message, "Traffic got through") {
		t.Errorf("Service hop findings = %+v, want one info note after traffic got through", got)
	}

	tr = trace(false)
	reconcilePolicyFindings(tr)
	for _, f := range tr.Downstream[0].Findings {
		if f.Severity != SeverityWarning {
			t.Errorf("Service hop finding %+v, want both warnings kept when the data path was dropped", f)
		}
	}
}
