package trace

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestDetectMesh(t *testing.T) {
	sidecar := func(name string) *corev1.Pod {
		return &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}, {Name: name}}}}
	}
	labeled := func(labels, annos map[string]string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Labels: labels, Annotations: annos}}
	}
	cases := []struct {
		name string
		pod  *corev1.Pod
		want string
	}{
		{"istio sidecar container", sidecar("istio-proxy"), "Istio"},
		{"linkerd sidecar container", sidecar("linkerd-proxy"), "Linkerd"},
		{"consul sidecar container", sidecar("consul-dataplane"), "Consul"},
		{"istio rev label", labeled(map[string]string{"istio.io/rev": "stable"}, nil), "Istio"},
		{"istio status annotation", labeled(nil, map[string]string{"sidecar.istio.io/status": "{...}"}), "Istio"},
		{"linkerd inject label", labeled(map[string]string{"linkerd.io/inject": "enabled"}, nil), "Linkerd"},
		{"consul connect-inject annotation", labeled(nil, map[string]string{"consul.hashicorp.com/connect-inject": "true"}), "Consul"},
		{"plain pod, no mesh", &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}}, ""},
		{"linkerd inject disabled is not a mesh", labeled(map[string]string{"linkerd.io/inject": "disabled"}, nil), ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := detectMesh([]*corev1.Pod{c.pod}); got != c.want {
				t.Errorf("detectMesh = %q, want %q", got, c.want)
			}
		})
	}
}

// TestDetectMesh_AdvisoryIsInfoOnly pins the contract that a meshed pod yields an
// INFO advisory finding, never a verdict-escalating severity (a probe failing on
// mTLS must not read as "broken").
func TestDetectMesh_AdvisoryIsInfoOnly(t *testing.T) {
	meshed := &corev1.Pod{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "istio-proxy"}}}}
	hop := buildPodsHop(Deps{}, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "prod"}}, []*corev1.Pod{meshed}, false)
	var found *Finding
	for i := range hop.Findings {
		if hop.Findings[i].Code == "mesh:probe-may-fail-mtls" {
			found = &hop.Findings[i]
		}
	}
	if found == nil {
		t.Fatal("expected a mesh:probe-may-fail-mtls advisory on a meshed pods hop")
	}
	if found.Severity != SeverityInfo {
		t.Errorf("mesh advisory severity = %q, want INFO (it must never escalate the verdict)", found.Severity)
	}
}

// TestBuildPodsHop_PublishNotReadyAddresses pins the dataplane-honest handling
// of publishNotReadyAddresses Services: Kubernetes publishes their endpoints
// regardless of readiness, so (a) not-ready pods ARE probe targets (they
// receive traffic), (b) meta["ready"] counts published endpoints - the value
// the severity layers read - so a by-design 0-kubelet-ready state never renders
// as an outage, and (c) the no-ready-endpoints note is info-tier, not warning.
// A plain Service with the same pods keeps the strict readiness behavior.
func TestBuildPodsHop_PublishNotReadyAddresses(t *testing.T) {
	notReadyPod := func(name, ip string) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "prod", Labels: map[string]string{"app": "db"}},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Name:  "main",
				Ports: []corev1.ContainerPort{{ContainerPort: 5432}},
			}}},
			Status: corev1.PodStatus{
				Phase:      corev1.PodRunning,
				PodIP:      ip,
				Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}},
			},
		}
	}
	svc := func(pnr bool) *corev1.Service {
		return &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "prod"},
			Spec: corev1.ServiceSpec{
				Selector:                 map[string]string{"app": "db"},
				Ports:                    []corev1.ServicePort{{Port: 5432}},
				PublishNotReadyAddresses: pnr,
			},
		}
	}
	pods := []*corev1.Pod{notReadyPod("db-0", "10.0.0.1"), notReadyPod("db-1", "10.0.0.2")}

	cases := []struct {
		name         string
		pnr          bool
		wantReady    int
		wantIPs      int
		wantSeverity string
	}{
		{"publishNotReadyAddresses: endpoints publish regardless of readiness", true, 2, 2, SeverityInfo},
		{"plain Service: only kubelet-ready pods are endpoints", false, 0, 0, SeverityWarning},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hop := buildPodsHop(Deps{}, svc(c.pnr), pods, false)
			if got := hop.Meta["ready"]; got != c.wantReady {
				t.Errorf("meta[ready] = %v, want %d", got, c.wantReady)
			}
			if got, _ := hop.Meta["publishNotReadyAddresses"].(bool); got != c.pnr {
				t.Errorf("meta[publishNotReadyAddresses] = %v, want %v", got, c.pnr)
			}
			gotIPs := 0
			if hop.Config != nil {
				gotIPs = len(hop.Config.PodIPs)
			}
			if gotIPs != c.wantIPs {
				t.Errorf("probe-eligible pod IPs = %d, want %d (config: %+v)", gotIPs, c.wantIPs, hop.Config)
			}
			f := findingByCode(hop.Findings, "pods:no-ready-endpoints")
			if f == nil {
				t.Fatalf("expected the no-ready-endpoints note, got %+v", hop.Findings)
			}
			if f.Severity != c.wantSeverity {
				t.Errorf("no-ready note severity = %q, want %q", f.Severity, c.wantSeverity)
			}
			if c.pnr && !strings.Contains(f.Message, "publishNotReadyAddresses") {
				t.Errorf("PNR note should explain traffic still routes, got %q", f.Message)
			}
		})
	}
}

// SelectedPods must distinguish "genuinely nothing to read" (no svc /
// no selector → readable-empty) from "can't read pod state" (Pods lister
// unavailable, e.g. Pods kind RBAC-disabled at startup → unreadable). Lumping the
// uncertain case in with the empty case stamps a confident "0 ready pods" under
// uncertainty, contradicting the function's documented contract.
func TestSelectedPods_NilListerIsUnreadable(t *testing.T) {
	withSelector := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "shop"},
		Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "shop"}},
	}
	// A selector exists but Deps.Cache is nil → can't read pods → unreadable.
	if pods, unreadable := selectedPods(Deps{}, withSelector); !unreadable || pods != nil {
		t.Errorf("selector + nil cache: got pods=%v unreadable=%v, want nil/true (uncertain, not a confident empty)", pods, unreadable)
	}
	// No selector → headless/selectorless Service, genuinely nothing to enumerate.
	noSelector := &corev1.Service{ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "db"}}
	if pods, unreadable := selectedPods(Deps{}, noSelector); unreadable || pods != nil {
		t.Errorf("no selector: got pods=%v unreadable=%v, want nil/false (nothing to read)", pods, unreadable)
	}
	// nil Service → nothing to read.
	if pods, unreadable := selectedPods(Deps{}, nil); unreadable || pods != nil {
		t.Errorf("nil svc: got pods=%v unreadable=%v, want nil/false", pods, unreadable)
	}
}

// A publishNotReadyAddresses Service publishes only what the EndpointSlice
// reconciler would: PNR waives the READINESS condition, not the terminal-phase
// or has-an-IP rules. Succeeded/Failed and IP-less pods are never endpoints;
// a terminating (DeletionTimestamp) running pod with an IP stays published.
// Counting len(pods) would overstate meta["ready"] with endpoints Kubernetes
// never publishes.
func TestBuildPodsHop_PNRCountsOnlyPublishedEndpoints(t *testing.T) {
	now := metav1.Now()
	pod := func(name, ip string, phase corev1.PodPhase, deleted bool) *corev1.Pod {
		p := &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "prod", Labels: map[string]string{"app": "db"}},
			Status: corev1.PodStatus{
				Phase:      phase,
				PodIP:      ip,
				Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}},
			},
		}
		if deleted {
			p.ObjectMeta.DeletionTimestamp = &now
		}
		return p
	}
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "prod"},
		Spec: corev1.ServiceSpec{
			Selector:                 map[string]string{"app": "db"},
			Ports:                    []corev1.ServicePort{{Port: 5432}},
			PublishNotReadyAddresses: true,
		},
	}
	pods := []*corev1.Pod{
		pod("running-not-ready", "10.0.0.1", corev1.PodRunning, false), // published
		pod("terminating", "10.0.0.2", corev1.PodRunning, true),        // published: termination doesn't unpublish
		pod("succeeded", "10.0.0.3", corev1.PodSucceeded, false),       // terminal: never published
		pod("failed", "10.0.0.4", corev1.PodFailed, false),             // terminal: never published
		pod("no-ip", "", corev1.PodPending, false),                     // no IP: nothing to publish
	}
	hop := buildPodsHop(Deps{}, svc, pods, false)
	if got := hop.Meta["ready"]; got != 2 {
		t.Errorf("meta[ready] = %v, want 2 (running + terminating; terminal/IP-less pods are never published, even with PNR)", got)
	}
	if got := hop.Meta["selected"]; got != 5 {
		t.Errorf("meta[selected] = %v, want 5", got)
	}
}

// The Deployment is not a network object, which is why it was missing from a
// path view for so long - but it is what an operator calls the thing behind a
// Service, and every consumer had to hold that link in its head.
func TestPodsWorkloadResolvesThroughTheReplicaSet(t *testing.T) {
	ctrl := true
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "web-abc-1", Namespace: "prod",
			OwnerReferences: []metav1.OwnerReference{{Kind: "ReplicaSet", Name: "web-abc", Controller: &ctrl}},
		},
	}
	// No cache: the ReplicaSet can't be read, so the honest answer is the
	// ReplicaSet itself rather than "no owner".
	got := ownerWorkload(Deps{}, pod)
	if got == nil || got.Kind != "ReplicaSet" || got.Name != "web-abc" {
		t.Errorf("ownerWorkload = %+v, want the ReplicaSet when its parent is unreadable", got)
	}
}

// A StatefulSet/DaemonSet owns its Pods directly - there is no intermediate to
// resolve through.
func TestPodsWorkloadTakesADirectOwnerAsIs(t *testing.T) {
	ctrl := true
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "db-0", Namespace: "prod",
			OwnerReferences: []metav1.OwnerReference{{Kind: "StatefulSet", Name: "db", Controller: &ctrl}},
		},
	}
	got := ownerWorkload(Deps{}, pod)
	if got == nil || got.Kind != "StatefulSet" || got.Name != "db" {
		t.Errorf("ownerWorkload = %+v, want the StatefulSet itself", got)
	}
}

// Two workloads behind one Service have no single answer, and naming one of
// them would be a guess in a view whose whole point is not guessing.
func TestPodsWorkloadDeclinesWhenOwnersDisagree(t *testing.T) {
	ctrl := true
	mk := func(name, owner string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "prod",
			OwnerReferences: []metav1.OwnerReference{{Kind: "StatefulSet", Name: owner, Controller: &ctrl}},
		}}
	}
	if got := podsWorkload(Deps{}, []*corev1.Pod{mk("a", "one"), mk("b", "two")}); got != nil {
		t.Errorf("podsWorkload = %+v, want nil when the Pods disagree on their owner", got)
	}
}

// A bare Pod has no controller, so there is nothing to name.
func TestPodsWorkloadIsNilForAnUnownedPod(t *testing.T) {
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "solo", Namespace: "prod"}}
	if got := podsWorkload(Deps{}, []*corev1.Pod{pod}); got != nil {
		t.Errorf("podsWorkload = %+v, want nil for a Pod with no controller", got)
	}
}
