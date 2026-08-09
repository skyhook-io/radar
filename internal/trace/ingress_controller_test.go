package trace

import (
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
)

func ingFixture(mod func(*networkingv1.Ingress)) *networkingv1.Ingress {
	ing := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "ing", Namespace: "ns"}}
	if mod != nil {
		mod(ing)
	}
	return ing
}

func TestLivePods_ExcludesTerminalAndDeleting(t *testing.T) {
	now := metav1.Now()
	pod := func(name string, phase corev1.PodPhase, deleting bool) *corev1.Pod {
		p := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name}, Status: corev1.PodStatus{Phase: phase}}
		if deleting {
			p.DeletionTimestamp = &now
		}
		return p
	}
	pods := []*corev1.Pod{
		pod("running", corev1.PodRunning, false),
		pod("pending", corev1.PodPending, false),
		pod("succeeded", corev1.PodSucceeded, false), // a completed Job pod under the selector
		pod("failed", corev1.PodFailed, false),
		pod("terminating", corev1.PodRunning, true), // rolling-update old pod being deleted
		nil,
	}
	live := livePods(pods)
	if len(live) != 2 {
		t.Fatalf("want 2 live pods (running + pending), got %d", len(live))
	}
	for _, p := range live {
		if p.Name != "running" && p.Name != "pending" {
			t.Errorf("unexpected live pod %q (terminal/deleting should be excluded)", p.Name)
		}
	}
}

func TestIngressEntryAddress(t *testing.T) {
	none := ingFixture(nil)
	if got := ingressEntryAddress(none); len(got) != 0 {
		t.Errorf("no status: got %v, want empty", got)
	}
	withHost := ingFixture(func(i *networkingv1.Ingress) {
		i.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{{Hostname: "lb.example.com"}}
	})
	if got := ingressEntryAddress(withHost); len(got) != 1 || got[0] != "lb.example.com" {
		t.Errorf("hostname: got %v", got)
	}
	withIP := ingFixture(func(i *networkingv1.Ingress) {
		i.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{{IP: "203.0.113.4"}}
	})
	if got := ingressEntryAddress(withIP); len(got) != 1 || got[0] != "203.0.113.4" {
		t.Errorf("ip: got %v", got)
	}
}

func TestHasCloudLBAnnotations(t *testing.T) {
	cases := []struct {
		name string
		anno map[string]string
		want bool
	}{
		{"none", nil, false},
		{"alb", map[string]string{"alb.ingress.kubernetes.io/scheme": "internet-facing"}, true},
		{"gce", map[string]string{"ingress.gcp.kubernetes.io/pre-shared-cert": "x"}, true},
		{"gke", map[string]string{"networking.gke.io/managed-certificates": "x"}, true},
		{"class-alb", map[string]string{"kubernetes.io/ingress.class": "alb"}, true},
		{"plain-nginx-anno", map[string]string{"nginx.ingress.kubernetes.io/rewrite-target": "/"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ing := ingFixture(func(i *networkingv1.Ingress) { i.Annotations = c.anno })
			if got := hasCloudLBAnnotations(ing); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

// The honesty gating: with no cache (Dynamic nil → no class resolves), the finding
// must NEVER condemn unless the POSITIVE no-controller signal holds, and cloud /
// address signals must read as served, not broken.
func TestIngressControllerFinding_Honesty(t *testing.T) {
	t.Run("unwired deps (couldn't read IngressClasses) → soft pill, NEVER condemn", func(t *testing.T) {
		// Deps{} has a nil Dynamic/Discovery - Radar couldn't READ the classes,
		// which is not the same as "none configured". Fail toward silence: a soft
		// "couldn't identify the controller" pill, never the no-controller condemn
		// of a possibly-healthy Ingress.
		st := ingressControllerStatus(Deps{}, ingFixture(nil))
		if st.hasFinding {
			t.Fatalf("unwired deps must NOT condemn; got %+v", st.finding)
		}
		if st.servedBy == "" || !strings.Contains(st.servedByTitle, "IngressClasses") {
			t.Errorf("want the soft couldn't-identify pill; got servedBy=%q title=%q", st.servedBy, st.servedByTitle)
		}
	})

	t.Run("cloud-LB annotations → quiet servedBy pill, NO finding (never broken)", func(t *testing.T) {
		ing := ingFixture(func(i *networkingv1.Ingress) {
			i.Annotations = map[string]string{"alb.ingress.kubernetes.io/scheme": "internet-facing"}
		})
		st := ingressControllerStatus(Deps{}, ing)
		if st.hasFinding {
			t.Errorf("cloud-LB must NOT be a finding; got %+v", st.finding)
		}
		if st.servedBy == "" || !strings.Contains(st.servedByTitle, "cloud load balancer") {
			t.Errorf("want a cloud servedBy pill; got servedBy=%q title=%q", st.servedBy, st.servedByTitle)
		}
	})

	t.Run("has an address (a controller published it) → quiet pill, never no-controller", func(t *testing.T) {
		ing := ingFixture(func(i *networkingv1.Ingress) {
			i.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{{Hostname: "x.elb.amazonaws.com"}}
		})
		st := ingressControllerStatus(Deps{}, ing)
		if st.hasFinding {
			t.Errorf("an address present must NOT be condemned as a finding; got %+v", st.finding)
		}
		if st.servedBy == "" {
			t.Errorf("want a servedBy pill when an address is published")
		}
	})
}

func TestKnownControllers_CloudClassified(t *testing.T) {
	// ALB is cloud DATA plane but its CONTROL plane (the AWS Load Balancer
	// Controller) is an ordinary in-cluster Deployment - it must carry pod
	// labels so a dead LBC is checkable, not invisible.
	alb := knownControllers["ingress.k8s.aws/alb"]
	if !alb.cloud || alb.labels["app.kubernetes.io/name"] != "aws-load-balancer-controller" || alb.podOwnerName() != "aws-load-balancer-controller" {
		t.Errorf("ALB must be cloud WITH the aws-load-balancer-controller pod identity; got %+v", alb)
	}
	// GCLB's controller runs in the cloud provider's managed control plane -
	// genuinely nothing in-cluster to check.
	if gce := knownControllers["k8s.io/ingress-gce"]; !gce.cloud || len(gce.labels) != 0 {
		t.Errorf("ingress-gce must be cloud with no pod labels; got %+v", gce)
	}
	if info := knownControllers["k8s.io/ingress-nginx"]; info.cloud || len(info.labels) == 0 {
		t.Errorf("ingress-nginx must be in-cluster with pod labels; got %+v", info)
	}
}

// --- fixtures for class/pod-backed controller-status tests ---

func ingressClassObj(name, controller string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "networking.k8s.io/v1",
		"kind":       "IngressClass",
		"metadata":   map[string]any{"name": name},
		"spec":       map[string]any{"controller": controller},
	}}
}

// setupIngressClassDeps wires a synced cluster-wide dynamic cache holding the
// given IngressClasses (possibly none - the synced-empty case) and returns
// Deps resolving against it. Typed cache (pods) is wired separately via
// setupControllerPodCache when a test needs controller pods.
func setupIngressClassDeps(t *testing.T, classes ...*unstructured.Unstructured) Deps {
	t.Helper()
	gvr := schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingressclasses"}
	dynClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "IngressClassList"},
	)
	for _, c := range classes {
		if err := dynClient.Tracker().Create(gvr, c, ""); err != nil {
			t.Fatalf("tracker create ingressclass: %v", err)
		}
	}
	if err := k8s.InitTestDynamicResourceCache(dynClient, []k8s.APIResource{
		{Group: "networking.k8s.io", Version: "v1", Kind: "IngressClass", Name: "ingressclasses", Namespaced: false, Verbs: []string{"list", "watch"}},
	}); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	dynCache := k8s.GetDynamicResourceCache()
	if err := dynCache.EnsureWatching(gvr); err != nil {
		t.Fatalf("EnsureWatching ingressclasses: %v", err)
	}
	if !dynCache.WaitForSync(gvr, 2*time.Second) {
		t.Fatalf("dynamic cache did not sync ingressclasses")
	}
	return Deps{Dynamic: dynCache, Discovery: k8s.GetResourceDiscovery()}
}

func setupControllerPodCache(t *testing.T, pods ...*corev1.Pod) {
	t.Helper()
	objs := make([]runtime.Object, 0, len(pods))
	for _, p := range pods {
		objs = append(objs, p)
	}
	if err := k8s.InitTestResourceCache(fake.NewClientset(objs...)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestState)
}

func controllerPod(name, ns string, lbls map[string]string, ready bool) *corev1.Pod {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, Labels: lbls},
		Status: corev1.PodStatus{
			Phase:      corev1.PodRunning,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: status}},
		},
	}
}

func classedIngress(className string, mod func(*networkingv1.Ingress)) *networkingv1.Ingress {
	return ingFixture(func(i *networkingv1.Ingress) {
		i.Spec.IngressClassName = &className
		if mod != nil {
			mod(i)
		}
	})
}

func withLBAddress(i *networkingv1.Ingress) {
	i.Status.LoadBalancer.Ingress = []networkingv1.IngressLoadBalancerIngress{{Hostname: "k8s-abc.elb.amazonaws.com"}}
}

// A cloud-class Ingress with NO assigned address is configured, not served -
// it must warn (an observed, actionable state), and must never claim "Served
// by".
func TestIngressControllerStatus_CloudClassNoAddress_Warns(t *testing.T) {
	deps := setupIngressClassDeps(t, ingressClassObj("alb", "ingress.k8s.aws/alb"))
	ing := classedIngress("alb", nil)

	st := ingressControllerStatus(deps, ing)
	if !st.hasFinding || st.finding.Code != "ingress:no-address" {
		t.Fatalf("cloud class without an address must fire ingress:no-address; got hasFinding=%v %+v", st.hasFinding, st.finding)
	}
	if st.finding.Severity != SeverityWarning {
		t.Errorf("severity = %q, want warning", st.finding.Severity)
	}
	if !strings.Contains(st.finding.Message, "no load balancer address has been assigned") {
		t.Errorf("message must state the observed no-address fact; got %q", st.finding.Message)
	}
	if strings.Contains(st.finding.Message, "Served by") || strings.Contains(st.servedByTitle, "Served by") {
		t.Errorf("configured-only tier must never read as served; got message=%q title=%q", st.finding.Message, st.servedByTitle)
	}
}

// A cloud-class Ingress WITH an assigned address is programmed-tier evidence -
// quiet positive pill, no finding.
func TestIngressControllerStatus_CloudClassWithAddress_Served(t *testing.T) {
	deps := setupIngressClassDeps(t, ingressClassObj("alb", "ingress.k8s.aws/alb"))
	ing := classedIngress("alb", withLBAddress)

	st := ingressControllerStatus(deps, ing)
	if st.hasFinding {
		t.Fatalf("cloud class with an address must not warn; got %+v", st.finding)
	}
	if !strings.Contains(st.servedByTitle, "Served by AWS Application Load Balancer") || !strings.Contains(st.servedByTitle, "address is assigned") {
		t.Errorf("want programmed-tier served wording; got %q", st.servedByTitle)
	}
	// Controller pods are deliberately not scanned on the address-assigned
	// path, so the tooltip must claim no pod facts at all.
	if strings.Contains(st.servedByTitle, "pod") {
		t.Errorf("address-assigned tooltip must not claim pod facts it didn’t gather; got %q", st.servedByTitle)
	}
}

// A resolved class takes precedence over cloud-looking annotations: stale
// alb.* annotations on an nginx-CLASS Ingress must not skip the nginx
// pod-health check.
func TestIngressControllerStatus_ClassBeatsStaleCloudAnnotations(t *testing.T) {
	deps := setupIngressClassDeps(t, ingressClassObj("nginx", "k8s.io/ingress-nginx"))
	setupControllerPodCache(t,
		controllerPod("nginx-0", "ingress-nginx", map[string]string{"app.kubernetes.io/name": "ingress-nginx", "app.kubernetes.io/component": "controller"}, false),
	)
	deps.Cache = k8s.GetResourceCache()
	ing := classedIngress("nginx", func(i *networkingv1.Ingress) {
		i.Annotations = map[string]string{"alb.ingress.kubernetes.io/scheme": "internet-facing"} // stale leftovers
	})

	var st controllerStatus
	waitFor(t, func() bool {
		st = ingressControllerStatus(deps, ing)
		return st.hasFinding
	})
	if st.finding.Code != "ingress:controller-unready" || !strings.Contains(st.finding.Message, "ingress-nginx") {
		t.Fatalf("nginx-class Ingress must get the nginx pod check despite alb annotations; got %+v", st.finding)
	}
	if strings.Contains(st.servedByTitle, "cloud load balancer") {
		t.Errorf("stale cloud annotations must not win over the resolved class; got %q", st.servedByTitle)
	}
}

// A crashlooping AWS Load Balancer Controller is the most common ALB failure -
// with NO address assigned it must surface, not hide behind "it's a cloud LB".
// With an address assigned the pod scan is skipped entirely (the provisioned
// ALB keeps serving last-applied rules, so a cluster-wide pod list on every
// trace build can't change the diagnosis): the readout is the programmed tier
// and must claim no pod facts.
func TestIngressControllerStatus_ALBControllerCrashlooping(t *testing.T) {
	deps := setupIngressClassDeps(t, ingressClassObj("alb", "ingress.k8s.aws/alb"))
	setupControllerPodCache(t,
		controllerPod("lbc-0", "kube-system", map[string]string{"app.kubernetes.io/name": "aws-load-balancer-controller"}, false),
	)
	deps.Cache = k8s.GetResourceCache()

	// Without an address AND a dead LBC, nothing serves the Ingress - say so.
	noAddr := classedIngress("alb", nil)
	var st controllerStatus
	waitFor(t, func() bool {
		st = ingressControllerStatus(deps, noAddr)
		return st.hasFinding && st.finding.Code == "ingress:controller-unready"
	})
	if !strings.Contains(st.finding.Message, "aws-load-balancer-controller") {
		t.Errorf("finding must name the in-cluster controller component; got %q", st.finding.Message)
	}
	if !strings.Contains(st.finding.Message, "isn’t being served") {
		t.Errorf("dead LBC + no address must read as not served; got %q", st.finding.Message)
	}

	// The pod cache demonstrably holds a 0-ready LBC (the waitFor above proved
	// it's served), so a quiet programmed-tier readout here pins the gate: with
	// an address assigned, controller pods are not scanned and not judged.
	st = ingressControllerStatus(deps, classedIngress("alb", withLBAddress))
	if st.hasFinding {
		t.Fatalf("address-assigned cloud class must not scan or judge controller pods; got %+v", st.finding)
	}
	if !strings.Contains(st.servedByTitle, "address is assigned") {
		t.Errorf("want programmed-tier wording; got %q", st.servedByTitle)
	}
	if strings.Contains(st.servedByTitle, "pods ready") || strings.Contains(st.servedByTitle, "pods to check") {
		t.Errorf("address-assigned tooltip must not claim pod facts it didn’t gather; got %q", st.servedByTitle)
	}
}

// A SYNCED-but-empty IngressClass list is a real observation ("no ingress
// controller class exists in this cluster"), not "couldn't read" - it must
// enable the positive no-controller warning instead of suppressing it.
func TestIngressControllerStatus_SyncedEmptyClasses_FiresNoController(t *testing.T) {
	deps := setupIngressClassDeps(t) // synced cluster-wide informer, zero classes

	if _, _, _, couldRead := resolveIngressClass(deps, ingFixture(nil)); !couldRead {
		t.Fatalf("synced-empty class list must report couldRead=true (a real observation)")
	}

	st := ingressControllerStatus(deps, ingFixture(nil))
	if !st.hasFinding || st.finding.Code != "ingress:no-controller" {
		t.Fatalf("synced-empty classes + no address/annotations must fire no-controller; got hasFinding=%v %+v", st.hasFinding, st.finding)
	}
}

// A caller under a namespace allow-list must not receive cluster-scoped
// IngressClass content - the controller string and the product identity
// derived from it (names, glosses, pod label selectors in commands) - in ANY
// branch, findings included. Pod detail (counts) IS disclosed when every
// matched pod's namespace is inside the allow-list (podsChecked), and withheld
// otherwise.
func TestIngressControllerStatus_ScopedCallerRedaction(t *testing.T) {
	t.Run("controller pods outside scope → no counts, no identity", func(t *testing.T) {
		deps := setupIngressClassDeps(t, ingressClassObj("nginx", "k8s.io/ingress-nginx"))
		setupControllerPodCache(t,
			controllerPod("nginx-0", "ingress-nginx", map[string]string{"app.kubernetes.io/name": "ingress-nginx", "app.kubernetes.io/component": "controller"}, true),
			controllerPod("nginx-1", "ingress-nginx", map[string]string{"app.kubernetes.io/name": "ingress-nginx", "app.kubernetes.io/component": "controller"}, true),
		)
		deps.Cache = k8s.GetResourceCache()
		deps.AllowedNamespaces = []string{"ns"} // the Ingress's own namespace only

		// Wait until the pod cache serves the pods, so the test pins the
		// REDACTION (pods visible to radar, withheld from the caller), not a
		// cold cache.
		waitFor(t, func() bool {
			unscoped := deps
			unscoped.AllowedNamespaces = nil
			return strings.Contains(ingressControllerStatus(unscoped, classedIngress("nginx", nil)).servedByTitle, "2/2")
		})

		st := ingressControllerStatus(deps, classedIngress("nginx", nil))
		if st.hasFinding {
			t.Fatalf("redaction must not condemn; got %+v", st.finding)
		}
		if strings.Contains(st.servedByTitle, "ingress-nginx") || strings.Contains(st.servedByTitle, "2/2") {
			t.Errorf("scoped caller must not see controller identity or replica counts; got %q", st.servedByTitle)
		}
		if !strings.Contains(st.servedByTitle, "outside your namespace access") {
			t.Errorf("scoped caller must be told the controller status was not checked; got %q", st.servedByTitle)
		}
	})

	t.Run("unknown controller string omitted for scoped caller", func(t *testing.T) {
		deps := setupIngressClassDeps(t, ingressClassObj("custom", "example.com/custom-controller"))
		ing := classedIngress("custom", nil)

		open := ingressControllerStatus(deps, ing)
		if !strings.Contains(open.servedByTitle, "example.com/custom-controller") {
			t.Fatalf("unscoped caller should see the controller string; got %q", open.servedByTitle)
		}

		deps.AllowedNamespaces = []string{"ns"}
		scoped := ingressControllerStatus(deps, ing)
		if strings.Contains(scoped.servedByTitle, "example.com/custom-controller") {
			t.Errorf("scoped caller must not see the cluster-scoped controller string; got %q", scoped.servedByTitle)
		}
		if scoped.hasFinding {
			t.Errorf("redaction must not condemn; got %+v", scoped.finding)
		}
	})

	t.Run("cloud product identity omitted for scoped caller", func(t *testing.T) {
		deps := setupIngressClassDeps(t, ingressClassObj("alb", "ingress.k8s.aws/alb"))
		deps.AllowedNamespaces = []string{"ns"}
		st := ingressControllerStatus(deps, classedIngress("alb", withLBAddress))
		if st.hasFinding {
			t.Fatalf("address assigned must not warn; got %+v", st.finding)
		}
		if strings.Contains(st.servedByTitle, "AWS") {
			t.Errorf("scoped caller must not see the cloud product identity; got %q", st.servedByTitle)
		}
		if !strings.Contains(st.servedByTitle, "cloud load balancer") || !strings.Contains(st.servedByTitle, "address is assigned") {
			t.Errorf("scoped caller still gets the honest tier; got %q", st.servedByTitle)
		}
	})

	nginxLabels := map[string]string{"app.kubernetes.io/name": "ingress-nginx", "app.kubernetes.io/component": "controller"}

	t.Run("controller pods within scope → counts kept, class identity dropped", func(t *testing.T) {
		deps := setupIngressClassDeps(t, ingressClassObj("nginx", "k8s.io/ingress-nginx"))
		setupControllerPodCache(t,
			controllerPod("nginx-0", "ingress-nginx", nginxLabels, true),
			controllerPod("nginx-1", "ingress-nginx", nginxLabels, true),
			controllerPod("nginx-2", "ingress-nginx", nginxLabels, false),
		)
		deps.Cache = k8s.GetResourceCache()
		ing := classedIngress("nginx", nil)

		var open controllerStatus
		waitFor(t, func() bool {
			open = ingressControllerStatus(deps, ing)
			return strings.Contains(open.servedByTitle, "2/3")
		})
		if open.servedBy != "ingress-nginx" || !strings.Contains(open.servedByTitle, "ingress-nginx") {
			t.Fatalf("unscoped caller gets the controller identity; got servedBy=%q title=%q", open.servedBy, open.servedByTitle)
		}

		deps.AllowedNamespaces = []string{"ns", "ingress-nginx"} // every matched pod in scope
		st := ingressControllerStatus(deps, ing)
		if st.hasFinding {
			t.Fatalf("healthy controller must not warn; got %+v", st.finding)
		}
		if st.servedBy != "via a controller" || strings.Contains(st.servedByTitle, "ingress-nginx") {
			t.Errorf("scoped caller must not see the class-derived identity; got servedBy=%q title=%q", st.servedBy, st.servedByTitle)
		}
		if !strings.Contains(st.servedByTitle, "2/3") {
			t.Errorf("pod counts are authorized (every matched pod in scope) and must be kept; got %q", st.servedByTitle)
		}
	})

	t.Run("unready controller pods within scope → warning keeps counts, drops identity", func(t *testing.T) {
		deps := setupIngressClassDeps(t, ingressClassObj("nginx", "k8s.io/ingress-nginx"))
		setupControllerPodCache(t,
			controllerPod("nginx-0", "ingress-nginx", nginxLabels, false),
			controllerPod("nginx-1", "ingress-nginx", nginxLabels, false),
		)
		deps.Cache = k8s.GetResourceCache()
		ing := classedIngress("nginx", nil)

		var open controllerStatus
		waitFor(t, func() bool {
			open = ingressControllerStatus(deps, ing)
			return open.hasFinding && open.finding.Code == "ingress:controller-unready"
		})
		if !strings.Contains(open.finding.Message, "ingress-nginx") || !strings.Contains(open.finding.Command, "ingress-nginx") {
			t.Fatalf("unscoped caller gets the controller identity; got %+v", open.finding)
		}

		deps.AllowedNamespaces = []string{"ns", "ingress-nginx"}
		st := ingressControllerStatus(deps, ing)
		if !st.hasFinding || st.finding.Code != "ingress:controller-unready" {
			t.Fatalf("scoped caller still gets the unready warning; got hasFinding=%v %+v", st.hasFinding, st.finding)
		}
		for _, text := range []string{st.finding.Message, st.finding.Cause, st.finding.Action, st.finding.Command} {
			if strings.Contains(text, "ingress-nginx") {
				t.Errorf("scoped caller must not see the class-derived identity in the finding; got %+v", st.finding)
			}
		}
		if st.finding.Command != "" {
			t.Errorf("the label-selector command reveals the identity - must be omitted for scoped callers; got %q", st.finding.Command)
		}
		if !strings.Contains(st.finding.Cause, "0 of 2") {
			t.Errorf("pod counts are authorized and must be kept; got %q", st.finding.Cause)
		}
	})

	t.Run("cloud class, no address, dead controller within scope → warning keeps pod facts, drops identity", func(t *testing.T) {
		deps := setupIngressClassDeps(t, ingressClassObj("alb", "ingress.k8s.aws/alb"))
		setupControllerPodCache(t,
			controllerPod("lbc-0", "kube-system", map[string]string{"app.kubernetes.io/name": "aws-load-balancer-controller"}, false),
		)
		deps.Cache = k8s.GetResourceCache()
		ing := classedIngress("alb", nil)

		var open controllerStatus
		waitFor(t, func() bool {
			open = ingressControllerStatus(deps, ing)
			return open.hasFinding && open.finding.Code == "ingress:controller-unready"
		})
		if !strings.Contains(open.finding.Message, "aws-load-balancer-controller") || !strings.Contains(open.finding.Command, "aws-load-balancer-controller") {
			t.Fatalf("unscoped caller gets the controller identity; got %+v", open.finding)
		}

		deps.AllowedNamespaces = []string{"ns", "kube-system"}
		st := ingressControllerStatus(deps, ing)
		if !st.hasFinding || st.finding.Code != "ingress:controller-unready" {
			t.Fatalf("scoped caller still gets the not-served warning; got hasFinding=%v %+v", st.hasFinding, st.finding)
		}
		for _, text := range []string{st.finding.Message, st.finding.Cause, st.finding.Action, st.finding.Command} {
			if strings.Contains(text, "aws-load-balancer-controller") || strings.Contains(text, "AWS") {
				t.Errorf("scoped caller must not see the class-derived identity in the finding; got %+v", st.finding)
			}
		}
		if st.finding.Command != "" {
			t.Errorf("the label-selector command reveals the identity - must be omitted for scoped callers; got %q", st.finding.Command)
		}
		if !strings.Contains(st.finding.Message, "isn’t being served") {
			t.Errorf("scoped caller still gets the honest not-served claim; got %q", st.finding.Message)
		}
		if !strings.Contains(st.finding.Cause, "0 of 1") {
			t.Errorf("pod counts are authorized and must be kept in the cause; got %q", st.finding.Cause)
		}
	})

	t.Run("cloud class, no address, running controller within scope → cause keeps counts, drops identity", func(t *testing.T) {
		deps := setupIngressClassDeps(t, ingressClassObj("alb", "ingress.k8s.aws/alb"))
		setupControllerPodCache(t,
			controllerPod("lbc-0", "kube-system", map[string]string{"app.kubernetes.io/name": "aws-load-balancer-controller"}, true),
		)
		deps.Cache = k8s.GetResourceCache()
		ing := classedIngress("alb", nil)

		var open controllerStatus
		waitFor(t, func() bool {
			open = ingressControllerStatus(deps, ing)
			return open.hasFinding && strings.Contains(open.finding.Cause, "1/1 pods ready")
		})
		if open.finding.Code != "ingress:no-address" || !strings.Contains(open.finding.Cause, "aws-load-balancer-controller") {
			t.Fatalf("unscoped caller gets the identity in the no-address cause; got %+v", open.finding)
		}

		deps.AllowedNamespaces = []string{"ns", "kube-system"}
		st := ingressControllerStatus(deps, ing)
		if !st.hasFinding || st.finding.Code != "ingress:no-address" {
			t.Fatalf("scoped caller still gets the no-address warning; got hasFinding=%v %+v", st.hasFinding, st.finding)
		}
		for _, text := range []string{st.finding.Message, st.finding.Cause} {
			if strings.Contains(text, "aws-load-balancer-controller") || strings.Contains(text, "AWS") {
				t.Errorf("scoped caller must not see the class-derived identity; got %+v", st.finding)
			}
		}
		if !strings.Contains(st.finding.Cause, "1/1 pods ready") {
			t.Errorf("pod counts are authorized and must be kept in the cause; got %q", st.finding.Cause)
		}
	})
}
