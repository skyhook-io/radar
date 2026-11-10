package trace

import (
	"context"
	"fmt"
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
	// The in-cluster test settles the prediction only for its own protocol
	// and Service port, which the trace records.
	if podsHop.Meta["policyProtocol"] != "TCP" || podsHop.Meta["policyPort"] == nil {
		t.Errorf("Pods hop meta = %v, want the prediction's protocol and Service port", podsHop.Meta)
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
// Traffic that got through to the pods settles a lone deny-all issue on the
// Service hop, as it does the Pods hop's per-port prediction; a dropped data
// path keeps it.
func TestReconcilePolicyDowngradesServiceDenyAll(t *testing.T) {
	trace := func(dataOK bool) *Trace {
		svc := Hop{Resource: ResourceRef{Kind: "Service", Namespace: "ns", Name: "rec"}, Edge: "entry:Service", Findings: []Finding{
			{Code: k8s.NetworkPolicyDenyAllIngressFingerprint, Severity: SeverityWarning, Message: "NetworkPolicy denies all ingress - Would block all inbound traffic"},
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
		t.Errorf("Service hop findings = %+v, want the info note after traffic got through", got)
	}

	tr = trace(false)
	reconcilePolicyFindings(tr)
	if f := tr.Downstream[0].Findings[0]; f.Severity != SeverityWarning {
		t.Errorf("Service hop finding %+v, want the warning kept when the data path was dropped", f)
	}
}

// A TCP probe that got through on a port says nothing about a UDP prediction
// on the same port number.
func TestReconcilePolicyKeepsUDPPrediction(t *testing.T) {
	pods := wouldDenyHop([]probe.Result{
		{Layer: probe.LayerTCP, Target: "10.0.0.5:53", Port: 53, Path: probe.PathData, OK: true},
	}, false)
	pods.Meta["policyDenyPort"] = int32(53)
	pods.Meta["policyProtocol"] = "UDP"
	tr := &Trace{Downstream: []Hop{pods}}
	reconcilePolicyFindings(tr)
	if f := tr.Downstream[0].Findings[findingIndexByCode(tr.Downstream[0].Findings, codePolicyWouldDeny)]; f.Severity != SeverityWarning {
		t.Errorf("UDP prediction = %s, want it kept after a TCP pass", f.Severity)
	}
}

// The in-cluster verify path settles the Service hop's deny-all policy issue
// with the Pods hop's prediction once real traffic got through, so the
// verdict re-derives off degraded; a dropped in-cluster request keeps both.
func TestApplyInClusterResultsSettlesServiceDenyAll(t *testing.T) {
	trace := func() *Trace {
		return &Trace{
			Subject: ResourceRef{Kind: "Service", Namespace: "shop", Name: "rec"},
			Verdict: VerdictDegraded,
			Downstream: []Hop{
				{Resource: ResourceRef{Kind: "Service", Namespace: "shop", Name: "rec"}, Edge: "entry:Service", Findings: []Finding{{
					Code: k8s.NetworkPolicyDenyAllIngressFingerprint, Severity: SeverityWarning, Message: "NetworkPolicy denies all ingress - Would block all inbound traffic",
				}}},
				{Resource: ResourceRef{Kind: "Pods", Namespace: "shop"}, Edge: "Service->Pods",
					Meta:     map[string]any{"policyVerdict": "would-deny", "policyNames": []string{"deny-all"}, "policyPort": ":80", "policyDenyPort": int32(80)},
					Findings: []Finding{{Code: codePolicyWouldDeny, Severity: SeverityWarning, Message: "A network rule here would block this traffic"}}},
			},
			Routes: []RouteResult{{Route: "rec", Target: "rec:80", Outcome: OutcomeReached, Confidence: ConfidenceIndirect,
				InClusterRequest: &ProbeRequest{Protocol: "http", Scheme: "http", Path: "/"}}},
			Coverage: &Coverage{Tested: 1, Passed: 1},
		}
	}

	tr := trace()
	ApplyInClusterResults(tr, map[string][]probe.Result{InClusterResultKey("rec", "rec:80", ""): cleanInClusterPass()})
	for i, h := range tr.Downstream {
		for _, f := range h.Findings {
			if f.Severity != SeverityInfo {
				t.Errorf("hop %d finding %s = %s, want info after real in-cluster traffic got through", i, f.Code, f.Severity)
			}
		}
	}
	if tr.Verdict == VerdictDegraded {
		t.Errorf("verdict = %s, want it re-derived off degraded", tr.Verdict)
	}

	tr = trace()
	ApplyInClusterResults(tr, map[string][]probe.Result{InClusterResultKey("rec", "rec:80", ""): {{
		Layer: probe.LayerTCP, Path: probe.PathData, Vantage: probe.VantageInCluster, OK: false, Detail: "connection timed out",
	}}})
	if f := tr.Downstream[0].Findings[0]; f.Severity != SeverityWarning {
		t.Errorf("Service hop finding = %s, want the warning kept when in-cluster traffic was dropped", f.Severity)
	}
}

// A real in-cluster pass settles the Service's deny-all issue whatever the
// port. It settles the Pods hop's prediction only when it went through the
// Service port the prediction was evaluated for, and only for TCP, the
// protocol of the in-cluster request.
func TestApplyInClusterResultsSettlesDenyAllAcrossPorts(t *testing.T) {
	type prediction struct {
		servicePort, podPort int32
		protocol             string
	}
	trace := func(pred prediction, passPort int32, ports ...PortMap) *Trace {
		return &Trace{
			Subject: ResourceRef{Kind: "Service", Namespace: "shop", Name: "rec"},
			Verdict: VerdictDegraded,
			Downstream: []Hop{
				{Resource: ResourceRef{Kind: "Service", Namespace: "shop", Name: "rec"}, Edge: "entry:Service",
					Config:   &HopConfig{Ports: ports},
					Findings: []Finding{{Code: k8s.NetworkPolicyDenyAllIngressFingerprint, Severity: SeverityWarning, Message: "NetworkPolicy denies all ingress"}}},
				{Resource: ResourceRef{Kind: "Pods", Namespace: "shop"}, Edge: "Service->Pods",
					Meta: map[string]any{"policyVerdict": "would-deny", "policyNames": []string{"deny-all"},
						"policyPort": fmt.Sprintf(":%d", pred.servicePort), "policyDenyPort": pred.podPort, "policyProtocol": pred.protocol},
					Findings: []Finding{{Code: codePolicyWouldDeny, Severity: SeverityWarning, Message: "A network rule here would block this traffic"}}},
			},
			Routes: []RouteResult{{Route: "rec", Target: fmt.Sprintf("rec:%d", passPort), Outcome: OutcomeReached, Confidence: ConfidenceIndirect,
				InClusterRequest: &ProbeRequest{Protocol: "http", Scheme: "http", Path: "/"}}},
			Coverage: &Coverage{Tested: 1, Passed: 1},
		}
	}
	for _, tt := range []struct {
		name        string
		pred        prediction
		passPort    int32
		ports       []PortMap
		wantPods    string
		wantHealthy bool
	}{
		{name: "Service port 80 forwards to targetPort 8080",
			pred: prediction{80, 8080, "TCP"}, passPort: 80, ports: []PortMap{{Port: 80, TargetPort: "8080"}},
			wantPods: SeverityInfo, wantHealthy: true},
		{name: "named targetPort",
			pred: prediction{80, 8080, "TCP"}, passPort: 80, ports: []PortMap{{Port: 80, TargetPort: "http"}},
			wantPods: SeverityInfo, wantHealthy: true},
		{name: "the prediction is about another Service port",
			pred: prediction{90, 9090, "TCP"}, passPort: 80, ports: []PortMap{{Port: 80, TargetPort: "8080"}, {Port: 90, TargetPort: "9090"}},
			wantPods: SeverityWarning},
		{name: "crossed ports: Service 90 forwards to the denied pod port 80",
			pred: prediction{90, 80, "TCP"}, passPort: 80, ports: []PortMap{{Port: 80, TargetPort: "8080"}, {Port: 90, TargetPort: "80"}},
			wantPods: SeverityWarning},
		{name: "a TCP pass doesn't settle a UDP denial on the same Service port",
			pred: prediction{53, 5353, "UDP"}, passPort: 53, ports: []PortMap{{Port: 53, TargetPort: "5353", Protocol: "UDP"}, {Port: 53, TargetPort: "8080", Protocol: "TCP"}},
			wantPods: SeverityWarning},
		{name: "a TCP pass doesn't settle a UDP denial on the same pod port",
			pred: prediction{53, 5353, "UDP"}, passPort: 53, ports: []PortMap{{Port: 53, TargetPort: "5353", Protocol: "UDP"}, {Port: 53, TargetPort: "5353", Protocol: "TCP"}},
			wantPods: SeverityWarning},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tr := trace(tt.pred, tt.passPort, tt.ports...)
			ApplyInClusterResults(tr, map[string][]probe.Result{
				InClusterResultKey("rec", fmt.Sprintf("rec:%d", tt.passPort), ""): cleanInClusterPass(),
			})
			if f := tr.Downstream[0].Findings[0]; f.Severity != SeverityInfo {
				t.Errorf("Service hop deny-all = %s, want info after a real pass", f.Severity)
			}
			if f := tr.Downstream[1].Findings[0]; f.Severity != tt.wantPods {
				t.Errorf("Pods hop prediction = %s, want %s", f.Severity, tt.wantPods)
			}
			if tt.wantHealthy && tr.Verdict == VerdictDegraded {
				t.Errorf("verdict = %s, want it off degraded", tr.Verdict)
			}
			// A prediction the pass didn't reach still leads the diagnosis.
			if f, _, ok := primaryProblemFinding(tr); tt.wantPods == SeverityWarning && (!ok || f.Code != codePolicyWouldDeny) {
				t.Errorf("primary finding = %+v (found %v), want the standing would-deny", f, ok)
			}
		})
	}
}

// On the Radar-process probe path, a data-path pass settles the Service's
// deny-all issue even when the prediction can't be pinned to one port.
func TestReconcilePolicySettlesDenyAllWithoutAPinnedPort(t *testing.T) {
	pods := wouldDenyHop([]probe.Result{
		{Layer: probe.LayerTCP, Target: "10.0.0.5:443", Port: 443, Path: probe.PathData, OK: true},
	}, false)
	pods.Edge = "Service->Pods"
	delete(pods.Meta, "policyDenyPort")
	tr := &Trace{Downstream: []Hop{
		{Resource: ResourceRef{Kind: "Service", Namespace: "ns", Name: "rec"}, Edge: "entry:Service", Findings: []Finding{{
			Code: k8s.NetworkPolicyDenyAllIngressFingerprint, Severity: SeverityWarning, Message: "NetworkPolicy denies all ingress",
		}}},
		pods,
	}}
	reconcilePolicyFindings(tr)
	if f := tr.Downstream[0].Findings[0]; f.Severity != SeverityInfo {
		t.Errorf("Service hop finding = %s, want info after traffic got through", f.Severity)
	}
	if f := tr.Downstream[1].Findings[0]; f.Severity != SeverityWarning {
		t.Errorf("Pods hop prediction = %s, want it kept: its port isn't pinned", f.Severity)
	}
}

// Policies that split a Service's pods each add a deny-all issue to the
// Service hop. A pass through the Service can't say which pod answered, and
// Radar's probes reach only a sample of the pods, so neither settles them.
func TestDenyAllSplitAcrossPodsKeepsIssues(t *testing.T) {
	svcHop := func() Hop {
		return Hop{Resource: ResourceRef{Kind: "Service", Namespace: "shop", Name: "rec"}, Edge: "entry:Service", Findings: []Finding{
			{Code: k8s.NetworkPolicyDenyAllIngressFingerprint, Severity: SeverityWarning, Message: "NetworkPolicy denies all ingress (deny-a)"},
			{Code: k8s.NetworkPolicyDenyAllIngressFingerprint, Severity: SeverityWarning, Message: "NetworkPolicy denies all ingress (deny-b)"},
		}}
	}
	warnings := func(h Hop) int {
		n := 0
		for _, f := range h.Findings {
			if f.Code == k8s.NetworkPolicyDenyAllIngressFingerprint && f.Severity == SeverityWarning {
				n++
			}
		}
		return n
	}

	// Radar's own probes: one pod reached and the other dropped, then both
	// probed pods reached (other pods may be outside the sample).
	for _, tt := range []struct {
		name   string
		podBOK bool
		want   int
	}{{"one pod dropped", false, 2}, {"both probed pods reached", true, 2}} {
		pods := Hop{Resource: ResourceRef{Kind: "Pods", Namespace: "shop"}, Edge: "Service->Pods", Probes: []probe.Result{
			{Layer: probe.LayerTCP, Target: "10.0.0.5:80", Port: 80, Path: probe.PathData, OK: true},
			{Layer: probe.LayerTCP, Target: "10.0.0.6:80", Port: 80, Path: probe.PathData, OK: tt.podBOK},
		}}
		tr := &Trace{Downstream: []Hop{svcHop(), pods}}
		reconcilePolicyFindings(tr)
		if got := warnings(tr.Downstream[0]); got != tt.want {
			t.Errorf("%s: %d deny-all warnings left, want %d", tt.name, got, tt.want)
		}
	}

	// The in-cluster test goes through the Service: two issues stay.
	tr := &Trace{
		Subject:    ResourceRef{Kind: "Service", Namespace: "shop", Name: "rec"},
		Downstream: []Hop{svcHop(), {Resource: ResourceRef{Kind: "Pods", Namespace: "shop"}, Edge: "Service->Pods"}},
		Routes: []RouteResult{{Route: "rec", Target: "rec:80", Outcome: OutcomeReached, Confidence: ConfidenceIndirect,
			InClusterRequest: &ProbeRequest{Protocol: "http", Scheme: "http", Path: "/"}}},
		Coverage: &Coverage{Tested: 1, Passed: 1},
	}
	ApplyInClusterResults(tr, map[string][]probe.Result{InClusterResultKey("rec", "rec:80", ""): cleanInClusterPass()})
	if got := warnings(tr.Downstream[0]); got != 2 {
		t.Errorf("in-cluster pass through the Service: %d deny-all warnings left, want 2", got)
	}
}

// Only this run's in-cluster request to a backend settles its deny-all issue.
// A backend whose earlier outcome came from the shared front door, and whose
// own in-cluster request didn't succeed, keeps the warning while a sibling
// backend's success folds in.
func TestApplyInClusterResultsSettlesOnlyBackendsItReached(t *testing.T) {
	denyAll := func() []Finding {
		return []Finding{{Code: k8s.NetworkPolicyDenyAllIngressFingerprint, Severity: SeverityWarning, Message: "NetworkPolicy denies all ingress"}}
	}
	tr := &Trace{
		Subject: ResourceRef{Kind: "Ingress", Namespace: "shop", Name: "web"},
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Ingress", Namespace: "shop", Name: "web"}, Edge: "entry:Ingress"},
			{Resource: ResourceRef{Kind: "Service", Namespace: "shop", Name: "svc-a"}, Edge: "Ingress->Service", Findings: denyAll(),
				Config: &HopConfig{Ports: []PortMap{{Port: 80, TargetPort: "8080"}}}},
			{Resource: ResourceRef{Kind: "Pods", Namespace: "shop"}, Edge: "Service->Pods",
				Meta:     map[string]any{"policyVerdict": "would-deny", "policyNames": []string{"deny-a"}, "policyPort": ":80", "policyDenyPort": int32(8080), "policyProtocol": "TCP"},
				Findings: []Finding{{Code: codePolicyWouldDeny, Severity: SeverityWarning, Message: "A network rule here would block this traffic"}}},
			{Resource: ResourceRef{Kind: "Service", Namespace: "shop", Name: "svc-b"}, Edge: "Ingress->Service", Findings: denyAll()},
			{Resource: ResourceRef{Kind: "Pods", Namespace: "shop"}, Edge: "Service->Pods"},
		},
		Routes: []RouteResult{
			// Reached through the front door earlier; no in-cluster result now.
			{Route: "/a", Target: "svc-a:80", TargetNamespace: "shop", Outcome: OutcomeReached, Confidence: ConfidenceReal,
				InClusterRequest: &ProbeRequest{Protocol: "http", Scheme: "http", Path: "/a"}},
			{Route: "/b", Target: "svc-b:80", TargetNamespace: "shop", Outcome: OutcomeReached, Confidence: ConfidenceIndirect,
				InClusterRequest: &ProbeRequest{Protocol: "http", Scheme: "http", Path: "/b"}},
		},
		Coverage: &Coverage{Tested: 2, Passed: 2},
	}
	ApplyInClusterResults(tr, map[string][]probe.Result{InClusterResultKey("/b", "svc-b:80", "shop"): cleanInClusterPass()})
	if f := tr.Downstream[1].Findings[0]; f.Severity != SeverityWarning {
		t.Errorf("svc-a deny-all = %s, want the warning kept: no in-cluster request reached it", f.Severity)
	}
	if f := tr.Downstream[3].Findings[0]; f.Severity != SeverityInfo {
		t.Errorf("svc-b deny-all = %s, want info after its in-cluster request got through", f.Severity)
	}
	if f := tr.Downstream[2].Findings[0]; f.Severity != SeverityWarning {
		t.Errorf("svc-a pods prediction = %s, want the warning kept", f.Severity)
	}
	if f, _, ok := primaryProblemFinding(tr); !ok || f.Severity != SeverityWarning {
		t.Errorf("primary finding = %+v (found %v), want one of svc-a's standing warnings", f, ok)
	}
}

// A probe Job with a mesh sidecar can get a connection accepted by its own
// proxy before the backend does, so its success doesn't settle policy findings.
func TestApplyInClusterResultsIgnoresMeshedProbeForPolicy(t *testing.T) {
	tr := &Trace{
		Subject: ResourceRef{Kind: "Service", Namespace: "shop", Name: "rec"},
		Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Service", Namespace: "shop", Name: "rec"}, Edge: "entry:Service", Findings: []Finding{
				{Code: k8s.NetworkPolicyDenyAllIngressFingerprint, Severity: SeverityWarning, Message: "NetworkPolicy denies all ingress"},
			}},
			{Resource: ResourceRef{Kind: "Pods", Namespace: "shop"}, Edge: "Service->Pods",
				Meta:     map[string]any{"policyVerdict": "would-deny", "policyNames": []string{"deny-all"}, "policyPort": ":80", "policyDenyPort": int32(80), "policyProtocol": "TCP"},
				Findings: []Finding{{Code: codePolicyWouldDeny, Severity: SeverityWarning, Message: "A network rule here would block this traffic"}}},
		},
		Routes: []RouteResult{{Route: "rec", Target: "rec:80", Outcome: OutcomeReached, Confidence: ConfidenceIndirect,
			InClusterRequest: &ProbeRequest{Protocol: "http", Scheme: "http", Path: "/"}}},
		Coverage: &Coverage{Tested: 1, Passed: 1},
	}
	pass := cleanInClusterPass()
	pass[0].Mesh = "Istio"
	ApplyInClusterResults(tr, map[string][]probe.Result{InClusterResultKey("rec", "rec:80", ""): pass})
	for i, h := range tr.Downstream {
		if f := h.Findings[0]; f.Severity != SeverityWarning {
			t.Errorf("hop %d %s = %s, want the warning kept after a meshed probe", i, f.Code, f.Severity)
		}
	}
}

// Pods that declare no containerPort get no per-pod probes. Radar's data probe
// through the Service on the prediction's Service port then settles both the
// prediction and a lone deny-all issue, and the diagnosis doesn't lead with
// them; a failed probe keeps both.
func TestReconcilePolicySettlesFromServiceProbeWithoutContainerPort(t *testing.T) {
	trace := func(ok bool) *Trace {
		return &Trace{Downstream: []Hop{
			{Resource: ResourceRef{Kind: "Service", Namespace: "shop", Name: "rec"}, Edge: "entry:Service",
				Findings: []Finding{{Code: k8s.NetworkPolicyDenyAllIngressFingerprint, Severity: SeverityWarning, Message: "NetworkPolicy denies all ingress"}},
				Probes:   []probe.Result{{Layer: probe.LayerTCP, Target: "10.96.0.10:80", Port: 80, Path: probe.PathData, Vantage: probe.VantageInCluster, OK: ok}}},
			{Resource: ResourceRef{Kind: "Pods", Namespace: "shop"}, Edge: "Service->Pods",
				Meta:     map[string]any{"policyVerdict": "would-deny", "policyNames": []string{"deny-all"}, "policyPort": ":80", "policyDenyPort": int32(80), "policyProtocol": "TCP"},
				Findings: []Finding{{Code: codePolicyWouldDeny, Severity: SeverityWarning, Message: "A network rule here would block this traffic"}}},
		}, BrokenAt: -1, radarUnmeshed: true}
	}
	tr := trace(true)
	reconcilePolicyFindings(tr)
	for i, h := range tr.Downstream {
		if f := h.Findings[0]; f.Severity != SeverityInfo {
			t.Errorf("hop %d %s = %s, want info after the Service probe got through", i, f.Code, f.Severity)
		}
	}
	if f, _, ok := primaryProblemFinding(tr); ok {
		t.Errorf("primary finding = %+v, want none after the probe got through", f)
	}

	tr = trace(false)
	reconcilePolicyFindings(tr)
	for i, h := range tr.Downstream {
		if f := h.Findings[0]; f.Severity != SeverityWarning {
			t.Errorf("hop %d %s = %s, want the warning kept when the probe was dropped", i, f.Code, f.Severity)
		}
	}

	// Radar couldn't read its own pod, so a sidecar there can't be ruled out.
	tr = trace(true)
	tr.radarUnmeshed = false
	reconcilePolicyFindings(tr)
	for i, h := range tr.Downstream {
		if f := h.Findings[0]; f.Severity != SeverityWarning {
			t.Errorf("hop %d %s = %s, want the warning kept when Radar's own mesh status is unknown", i, f.Code, f.Severity)
		}
	}
}

// A prediction without a recorded Service port isn't settled by an in-cluster
// pass: its pod port can't be compared with the Service port of a route.
func TestInClusterPassNeedsThePredictionsServicePort(t *testing.T) {
	meta := map[string]any{"policyVerdict": "would-deny", "policyDenyPort": int32(80), "policyProtocol": "TCP"}
	if inClusterPassCoversPrediction(map[int32]bool{80: true}, meta) {
		t.Error("a prediction without policyPort was settled by a pass on Service port 80")
	}
	meta["policyPort"] = ":80"
	if !inClusterPassCoversPrediction(map[int32]bool{80: true}, meta) {
		t.Error("a pass on the prediction's Service port didn't settle it")
	}
}

// Radar's own probes through a sidecar proxy in its pod don't settle policy
// findings either: the proxy can accept before the backend does.
func TestReconcilePolicyIgnoresMeshedRadarProbes(t *testing.T) {
	tr := &Trace{Downstream: []Hop{
		{Resource: ResourceRef{Kind: "Service", Namespace: "shop", Name: "rec"}, Edge: "entry:Service",
			Findings: []Finding{{Code: k8s.NetworkPolicyDenyAllIngressFingerprint, Severity: SeverityWarning, Message: "NetworkPolicy denies all ingress"}},
			Probes:   []probe.Result{{Layer: probe.LayerTCP, Target: "10.96.0.10:80", Port: 80, Path: probe.PathData, Vantage: probe.VantageInCluster, OK: true, Mesh: "Istio"}}},
		{Resource: ResourceRef{Kind: "Pods", Namespace: "shop"}, Edge: "Service->Pods",
			Meta:     map[string]any{"policyVerdict": "would-deny", "policyNames": []string{"deny-all"}, "policyPort": ":80", "policyDenyPort": int32(80), "policyProtocol": "TCP"},
			Findings: []Finding{{Code: codePolicyWouldDeny, Severity: SeverityWarning, Message: "A network rule here would block this traffic"}}},
	}}
	reconcilePolicyFindings(tr)
	for i, h := range tr.Downstream {
		if f := h.Findings[0]; f.Severity != SeverityWarning {
			t.Errorf("hop %d %s = %s, want the warning kept after a meshed probe", i, f.Code, f.Severity)
		}
	}
}

// selfPodMesh finds Radar's own pod in the cache by the downward-API env.
func TestSelfPodMesh(t *testing.T) {
	defer k8s.ResetTestState()
	pod := func(name string, containers ...string) *corev1.Pod {
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "radar"}}
		for _, c := range containers {
			p.Spec.Containers = append(p.Spec.Containers, corev1.Container{Name: c})
		}
		return p
	}
	if err := k8s.InitTestResourceCache(fake.NewClientset(pod("radar-meshed", "radar", "istio-proxy"), pod("radar-plain", "radar"))); err != nil {
		t.Fatalf("init: %v", err)
	}
	t.Setenv("MY_POD_NAMESPACE", "radar")
	for _, tt := range []struct {
		name, mesh string
		known      bool
	}{{"radar-meshed", "Istio", true}, {"radar-plain", "", true}, {"radar-missing", "", false}} {
		t.Setenv("MY_POD_NAME", tt.name)
		if mesh, known := selfPodMesh(); mesh != tt.mesh || known != tt.known {
			t.Errorf("%s: selfPodMesh = %q, %v; want %q, %v", tt.name, mesh, known, tt.mesh, tt.known)
		}
	}
}
