package topology

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"k8s.io/apimachinery/pkg/types"
)

func walkTestNeighborhood(t *testing.T, s *projectionSession, root runtime.Object, opts NeighborhoodOptions) *Subgraph {
	t.Helper()
	sub, err := WalkNeighborhood(context.Background(), s, refOf(projectionRef(root)), opts, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

func TestWalkNeighborhoodForbiddenIntermediateNeverBridgesOrSpendsBudget(t *testing.T) {
	ingress := projectionObject(&networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "entry", Namespace: "shop"}, Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{
		{Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "hidden"}}},
		{Backend: networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "visible"}}},
	}}}}}}}, "networking.k8s.io", "Ingress")
	hidden := projectionObject(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "hidden", Namespace: "shop"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "private"}}}, "", "Service")
	visible := projectionObject(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "visible", Namespace: "shop"}}, "", "Service")
	pod := projectionObject(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "backend", Namespace: "shop", Labels: map[string]string{"app": "private"}}}, "", "Pod")
	opts := NeighborhoodOptions{Hops: 2, MaxNodes: 2, Allow: func(n *Node) bool { return n.Name != "hidden" }}
	var baseline []byte
	for _, present := range []bool{false, true} {
		objects := []runtime.Object{ingress, visible, pod}
		if present {
			objects = append(objects, hidden)
		}
		s := newProjectionSession(t, objects...)
		sub := walkTestNeighborhood(t, s, ingress, opts)
		if len(sub.Nodes) != 2 || sub.Truncated || sub.Nodes[1].Name != "visible" {
			t.Fatalf("hidden spent budget or bridged: %+v", sub)
		}
		if slices.Contains(s.reads, projectionRef(hidden)) {
			t.Fatal("borrowed a forbidden node")
		}
		body, _ := json.Marshal(sub)
		if !present {
			baseline = body
		} else if string(body) != string(baseline) {
			t.Fatalf("hidden existence changed answer:\n%s\n%s", baseline, body)
		}
	}
}

func TestWalkNeighborhoodExposureAndPolicyBeforeThousandsOfPods(t *testing.T) {
	controller := true
	root := projectionObject(&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "shop", UID: "db"}, Spec: appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "db"}}}}}, "apps", "StatefulSet")
	svc := projectionObject(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "shop"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "db"}}}, "", "Service")
	policy := projectionObject(&networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "db-policy", Namespace: "shop"}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "db"}}}}, "networking.k8s.io", "NetworkPolicy")
	objects := []runtime.Object{root, svc, policy}
	for i := 0; i < 2000; i++ {
		objects = append(objects, projectionObject(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("db-%04d", i), Namespace: "shop", UID: types.UID(fmt.Sprint(i)), Labels: map[string]string{"app": "db"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: "db", UID: "db", Controller: &controller}}}}, "", "Pod"))
	}
	for _, budget := range []int{0, 3, 4} {
		t.Run(fmt.Sprint(budget), func(t *testing.T) {
			sub := walkTestNeighborhood(t, newProjectionSession(t, objects...), root, NeighborhoodOptions{MaxNodes: budget})
			kinds := map[NodeKind]Node{}
			for _, n := range sub.Nodes {
				kinds[n.Kind] = n
			}
			if kinds[KindService].Name != "db" || kinds[KindNetworkPolicy].Name != "db-policy" {
				t.Fatalf("fanout hid exposure/policy: %+v", sub.Nodes)
			}
			if budget != 3 && kinds[KindPodGroup].Data["podCount"] != 2000 {
				t.Fatalf("missing PodGroup: %+v", sub.Nodes)
			}
			if budget == 3 && (!sub.Truncated || len(sub.Nodes) != 3) {
				t.Fatalf("wrong truncation: %+v", sub)
			}
		})
	}
}

func TestWalkNeighborhoodRootDeniedAndCancelled(t *testing.T) {
	root := projectionObject(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "key", Namespace: "shop"}}, "", "Secret")
	s := newProjectionSession(t, root)
	sub := walkTestNeighborhood(t, s, root, NeighborhoodOptions{Allow: func(*Node) bool { return false }})
	if len(sub.Nodes) != 0 || len(s.reads) != 0 {
		t.Fatalf("denied root read: %+v %+v", sub, s.reads)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := WalkNeighborhood(ctx, s, refOf(projectionRef(root)), NeighborhoodOptions{}, nil, nil)
	if err != context.Canceled {
		t.Fatalf("cancel: %v", err)
	}
}

func TestWalkNeighborhoodProfilesAndDisplayData(t *testing.T) {
	dep := projectionObject(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", UID: "web"}}, "apps", "Deployment")
	svc := projectionObject(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop"}}, "", "Service")
	// The Service owns the Deployment solely to make a management edge that
	// networking auto must not follow.
	dep.(*appsv1.Deployment).OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "Service", Name: "web", UID: "service"}}
	svc.(*corev1.Service).UID = "service"
	for _, profile := range []Profile{ProfileAuto, ProfileAll} {
		sub := walkTestNeighborhood(t, newProjectionSession(t, dep, svc), svc, NeighborhoodOptions{Profile: profile})
		want := 1
		if profile == ProfileAll {
			want = 2
		}
		if len(sub.Nodes) != want {
			t.Fatalf("%s: %+v", profile, sub)
		}
		if profile == ProfileAll {
			node := sub.Nodes[1]
			if node.ID != "deployment/shop/web" || node.Data["totalReplicas"] != int32(1) || node.Data["apiVersion"] != "apps/v1" {
				t.Fatalf("display fields lost: %+v", node)
			}
		}
	}
}

func TestWalkNeighborhoodClampsBudgetAndDepth(t *testing.T) {
	root := projectionObject(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "root", Namespace: "shop", UID: "root"}}, "", "ConfigMap")
	objects := []runtime.Object{root}
	for i := 0; i < 210; i++ {
		objects = append(objects, projectionObject(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprint(i), Namespace: "shop", UID: types.UID(fmt.Sprint(i)), OwnerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "ConfigMap", Name: "root", UID: "root"}}}}, "", "ConfigMap"))
	}
	sub := walkTestNeighborhood(t, newProjectionSession(t, objects...), root, NeighborhoodOptions{Hops: 99, MaxNodes: 999})
	if len(sub.Nodes) != 200 || !sub.Truncated {
		t.Fatalf("budget not clamped: %d %+v", len(sub.Nodes), sub.Truncated)
	}
}

func TestWalkNeighborhoodCalicoAliasesShareBudgetAndAllowEitherGroup(t *testing.T) {
	root := projectionObject(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}}}}}, "apps", "Deployment")
	var objects = []runtime.Object{root}
	for _, group := range CalicoAPIGroups() {
		objects = append(objects, projectionCR(group, "NetworkPolicy", "shop", "allow", map[string]any{"spec": map[string]any{"selector": "app == 'web'"}}))
	}
	s := newProjectionSession(t, objects...)
	sub := walkTestNeighborhood(t, s, root, NeighborhoodOptions{MaxNodes: 2, Allow: func(n *Node) bool {
		if !IsCalicoPolicyKind(n.Kind) {
			return true
		}
		tuples, ok := CalicoPolicyRBACTuples(n)
		return ok && slices.ContainsFunc(tuples, func(tuple SARTuple) bool { return tuple.Group == "crd.projectcalico.org" })
	}})
	if len(sub.Nodes) != 2 || sub.Truncated || sub.Nodes[1].Kind != KindCalicoNetworkPolicy {
		t.Fatalf("aliases duplicated or hidden: %+v", sub)
	}
	ReadvertiseCalicoPolicyNodes(sub.Nodes, func(tuple SARTuple) bool { return tuple.Group == "crd.projectcalico.org" })
	if nodeAPIGroupFromData(&sub.Nodes[1]) != "crd.projectcalico.org" {
		t.Fatalf("wrong advertised group: %+v", sub.Nodes[1])
	}
}

func TestWalkNeighborhoodPodGroupContainsOnlyAdmittedPods(t *testing.T) {
	controller := true
	root := projectionObject(&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "shop", UID: "db"}}, "apps", "StatefulSet")
	objects := []runtime.Object{root}
	for i := 0; i < 10; i++ {
		objects = append(objects, projectionObject(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprint(i), Namespace: "shop", UID: types.UID(fmt.Sprint(i)), OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: "db", UID: "db", Controller: &controller}}}}, "", "Pod"))
	}
	sub := walkTestNeighborhood(t, newProjectionSession(t, objects...), root, NeighborhoodOptions{MaxNodes: 2, Allow: func(n *Node) bool { return n.Name != "9" }})
	if len(sub.Nodes) != 2 || sub.Truncated || sub.Nodes[1].Kind != KindPodGroup || sub.Nodes[1].Data["podCount"] != 9 {
		t.Fatalf("wrong authorized group: %+v", sub)
	}
	for _, pod := range sub.Nodes[1].Data["pods"].([]map[string]any) {
		if pod["name"] == "9" {
			t.Fatal("forbidden pod serialized")
		}
	}
}

func TestWalkNeighborhoodOwnerChainStopsBeforeForbiddenReplicaSet(t *testing.T) {
	controller := true
	dep := projectionObject(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "dep", Namespace: "shop", UID: "dep"}}, "apps", "Deployment")
	rs := projectionObject(&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "rs", Namespace: "shop", UID: "rs", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "dep", UID: "dep", Controller: &controller}}}}, "apps", "ReplicaSet")
	pod := projectionObject(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "pod", Namespace: "shop", UID: "pod", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "rs", UID: "rs", Controller: &controller}}}}, "", "Pod")
	s := newProjectionSession(t, pod, rs, dep)
	sub := walkTestNeighborhood(t, s, pod, NeighborhoodOptions{Hops: 2, Allow: func(n *Node) bool { return n.Kind != KindReplicaSet }})
	if len(sub.Nodes) != 1 || slices.Contains(s.reads, projectionRef(rs)) || slices.Contains(s.reads, projectionRef(dep)) {
		t.Fatalf("forbidden owner bridged: %+v; reads=%+v", sub, s.reads)
	}
}

func TestWalkNeighborhoodGroupAwareAndAmbiguousRoots(t *testing.T) {
	core := projectionObject(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "same", Namespace: "shop"}}, "", "Service")
	knative := projectionCR("serving.knative.dev", "Service", "shop", "same", map[string]any{})
	capi := projectionCR("cluster.x-k8s.io", "Cluster", "shop", "same", map[string]any{})
	cnpg := projectionCR("postgresql.cnpg.io", "Cluster", "shop", "same", map[string]any{})
	s := newProjectionSession(t, core, knative, capi, cnpg)
	for _, tc := range []struct {
		root      ResourceRef
		kind      string
		ambiguous bool
	}{
		{ResourceRef{Kind: "services", Namespace: "shop", Name: "same"}, "Service", false},
		{ResourceRef{Kind: "Service", Group: "serving.knative.dev", Namespace: "shop", Name: "same"}, "KnativeService", false},
		{ResourceRef{Kind: "Cluster", Namespace: "shop", Name: "same"}, "", true},
		{ResourceRef{Kind: "Cluster", Group: "cluster.x-k8s.io", Namespace: "shop", Name: "same"}, "CAPICluster", false},
	} {
		sub, err := WalkNeighborhood(context.Background(), s, tc.root, NeighborhoodOptions{MaxNodes: 1}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if sub.AmbiguousRoot != tc.ambiguous {
			t.Fatalf("wrong ambiguity for %+v: %+v", tc.root, sub)
		}
		if !tc.ambiguous && (len(sub.Nodes) != 1 || string(sub.Nodes[0].Kind) != tc.kind) {
			t.Fatalf("wrong root for %+v: %+v", tc.root, sub)
		}
	}
}

func TestWalkNeighborhoodGitOpsSourcesRemainContext(t *testing.T) {
	repo := projectionCR("source.toolkit.fluxcd.io", "GitRepository", "shop", "repo", map[string]any{})
	source := map[string]any{"spec": map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "repo"}}}
	root := projectionCR("kustomize.toolkit.fluxcd.io", "Kustomization", "shop", "root", source)
	sibling := projectionCR("kustomize.toolkit.fluxcd.io", "Kustomization", "shop", "sibling", map[string]any{"spec": map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "repo"}}})
	for _, profile := range []Profile{ProfileAuto, ProfileAll} {
		sub := walkTestNeighborhood(t, newProjectionSession(t, root, repo, sibling), root, NeighborhoodOptions{Hops: 2, Profile: profile})
		want := 2
		if profile == ProfileAll {
			want = 3
		}
		if len(sub.Nodes) != want {
			t.Fatalf("%s crossed shared source incorrectly: %+v", profile, sub)
		}
	}
}

func TestWalkNeighborhoodNeverExpandsThirdHop(t *testing.T) {
	objects := []runtime.Object{}
	for i := 0; i < 4; i++ {
		cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprint(i), Namespace: "shop", UID: types.UID(fmt.Sprint(i))}}
		if i > 0 {
			cm.OwnerReferences = []metav1.OwnerReference{{APIVersion: "v1", Kind: "ConfigMap", Name: fmt.Sprint(i - 1), UID: types.UID(fmt.Sprint(i - 1))}}
		}
		objects = append(objects, projectionObject(cm, "", "ConfigMap"))
	}
	s := newProjectionSession(t, objects...)
	sub := walkTestNeighborhood(t, s, objects[0], NeighborhoodOptions{Hops: 99})
	if len(sub.Nodes) != 3 || slices.ContainsFunc(sub.Nodes, func(n Node) bool { return n.Name == "3" }) {
		t.Fatalf("third hop expanded: %+v", sub)
	}
}

func TestWalkNeighborhoodIsolatedRootKeepsDisplayFields(t *testing.T) {
	labels := map[string]string{"app": "web"}
	meta := metav1.ObjectMeta{Name: "config", Namespace: "shop", ResourceVersion: "42", Labels: labels}
	class := "fast"
	for _, tc := range []struct {
		object    runtime.Object
		id, field string
		value     any
	}{
		{projectionObject(&corev1.ConfigMap{ObjectMeta: meta, Data: map[string]string{"a": "value"}}, "", "ConfigMap"), "configmap/shop/config", "keys", 1},
		{projectionObject(&corev1.Secret{ObjectMeta: meta, Type: corev1.SecretTypeTLS, Data: map[string][]byte{"tls.key": []byte("private")}}, "", "Secret"), "secret/shop/config", "type", "kubernetes.io/tls"},
		{projectionObject(&corev1.ServiceAccount{ObjectMeta: meta}, "", "ServiceAccount"), "serviceaccount/shop/config", "namespace", "shop"},
		{projectionObject(&corev1.PersistentVolumeClaim{ObjectMeta: meta, Spec: corev1.PersistentVolumeClaimSpec{StorageClassName: &class, Resources: corev1.VolumeResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse("2Gi")}}}}, "", "PersistentVolumeClaim"), "persistentvolumeclaim/shop/config", "storage", "2Gi"},
	} {
		sub := walkTestNeighborhood(t, newProjectionSession(t, tc.object), tc.object, NeighborhoodOptions{MaxNodes: 1})
		if len(sub.Nodes) != 1 {
			t.Fatalf("root missing: %+v", sub)
		}
		n := sub.Nodes[0]
		if n.ID != tc.id || n.Data[tc.field] != tc.value || n.Data["auditKey"] == nil {
			t.Fatalf("display fields changed: %+v", n)
		}
		if n.Data["labels"].(map[string]string)["app"] != "web" {
			t.Fatalf("labels lost: %+v", n)
		}
		body, _ := json.Marshal(sub)
		if strings.Contains(string(body), "private") {
			t.Fatal("secret value serialized")
		}
	}
}

func TestWalkNeighborhoodAuxiliaryLabelsUseRadarIdentity(t *testing.T) {
	root := projectionObject(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}}}}}, "apps", "Deployment")
	ns := projectionObject(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop", Labels: map[string]string{"team": "store"}}}, "", "Namespace")
	policy := projectionCR("projectcalico.org", "GlobalNetworkPolicy", "", "allow-store", map[string]any{"spec": map[string]any{"selector": "app == 'web'", "namespaceSelector": "team == 'store'"}})
	sub := walkTestNeighborhood(t, newProjectionSession(t, root, ns, policy), root, NeighborhoodOptions{Allow: func(n *Node) bool { return n.Kind != KindNamespace }})
	if len(sub.Nodes) != 2 || sub.Nodes[1].Kind != KindCalicoGlobalNetworkPolicy {
		t.Fatalf("auxiliary namespace labels were withheld: %+v", sub)
	}
	for _, n := range sub.Nodes {
		if n.Kind == KindNamespace {
			t.Fatal("auxiliary Namespace emitted")
		}
	}
}

func TestWalkNeighborhoodCalicoCRDOnly(t *testing.T) {
	root := projectionObject(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}}}}}, "apps", "Deployment")
	policy := projectionCR("crd.projectcalico.org", "NetworkPolicy", "shop", "allow", map[string]any{"spec": map[string]any{"selector": "app == 'web'"}})
	sub := walkTestNeighborhood(t, newProjectionSession(t, root, policy), root, NeighborhoodOptions{MaxNodes: 2})
	if len(sub.Nodes) != 2 || sub.Nodes[1].Kind != KindCalicoNetworkPolicy || nodeAPIGroupFromData(&sub.Nodes[1]) != "crd.projectcalico.org" {
		t.Fatalf("CRD-only policy lost: %+v", sub)
	}
}

// The walk says it withheld something only when a related object is hidden:
// an unrelated object the caller may not read changes nothing.
func TestWalkNeighborhoodWithholdsOnlyRelatedObjects(t *testing.T) {
	web := projectionObject(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}}}}}, "apps", "Deployment")
	related := projectionObject(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "web"}}}, "", "Service")
	unrelated := projectionObject(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "other", Namespace: "shop"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "other"}}}, "", "Service")
	opts := NeighborhoodOptions{Profile: ProfileAll, Allow: func(n *Node) bool { return n.Kind != KindService }}
	for _, tc := range []struct {
		name     string
		objects  []runtime.Object
		withheld bool
	}{
		{"no Service", []runtime.Object{web}, false},
		{"an unrelated Service", []runtime.Object{web, unrelated}, false},
		{"a related Service", []runtime.Object{web, unrelated, related}, true},
	} {
		sub := walkTestNeighborhood(t, newProjectionSession(t, tc.objects...), web, opts)
		if (sub.RBACDenied > 0) != tc.withheld {
			t.Errorf("%s: RBACDenied=%d, want withheld=%v", tc.name, sub.RBACDenied, tc.withheld)
		}
		for _, n := range sub.Nodes {
			if n.Kind == KindService {
				t.Errorf("%s: surfaced a Service the caller may not read: %s", tc.name, n.ID)
			}
		}
	}
}
