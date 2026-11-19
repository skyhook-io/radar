package topology

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
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
	route := projectionCR("gateway.networking.k8s.io", "HTTPRoute", "shop", "entry", map[string]any{"spec": map[string]any{"rules": []any{map[string]any{"backendRefs": []any{
		map[string]any{"name": "hidden", "namespace": "private"},
		map[string]any{"name": "visible"},
	}}}}})
	hidden := projectionObject(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "hidden", Namespace: "private"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "private"}}}, "", "Service")
	visible := projectionObject(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "visible", Namespace: "shop"}}, "", "Service")
	backend := projectionObject(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "backend", Namespace: "private", Labels: map[string]string{"app": "private"}}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "private"}}}}}, "apps", "Deployment")
	opts := NeighborhoodOptions{Hops: 2, MaxNodes: 2, Allow: func(n *Node) bool { return n.Data["namespace"] != "private" }}
	var baseline []byte
	for _, present := range []bool{false, true} {
		objects := []runtime.Object{route, visible, backend}
		if present {
			objects = append(objects, hidden)
		}
		s := newProjectionSession(t, objects...)
		sub := walkTestNeighborhood(t, s, route, opts)
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

// A cluster-scoped owner's Pods in a namespace the caller can't read neither
// join the admitted Pods' group nor spend the budget.
func TestWalkNeighborhoodPodGroupContainsOnlyAdmittedPods(t *testing.T) {
	controller := true
	root := projectionCR("example.io", "Fleet", "", "web", map[string]any{})
	root.SetUID("fleet")
	objects := []runtime.Object{root}
	for i, ns := range []string{"shop", "shop", "shop", "shop", "shop", "shop", "shop", "shop", "shop", "private", "private", "private"} {
		objects = append(objects, projectionObject(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprint(i), Namespace: ns, UID: types.UID(fmt.Sprint(i)), Labels: map[string]string{"app": "web"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "example.io/v1", Kind: "Fleet", Name: "web", UID: "fleet", Controller: &controller}}}}, "", "Pod"))
	}
	sub := walkTestNeighborhood(t, newProjectionSession(t, objects...), root, NeighborhoodOptions{MaxNodes: 2, Allow: func(n *Node) bool { return n.Data["namespace"] != "private" }})
	if len(sub.Nodes) != 2 || sub.Truncated || sub.Nodes[1].Kind != KindPodGroup || sub.Nodes[1].Data["podCount"] != 9 {
		t.Fatalf("wrong authorized group: %+v", sub)
	}
	for _, pod := range sub.Nodes[1].Data["pods"].([]map[string]any) {
		if pod["namespace"] == "private" {
			t.Fatal("forbidden pod serialized")
		}
	}
}

// A batch scheduler's PodGroup custom resource shares its kind name with
// Radar's synthetic Pod groups but carries no member Pods.
func TestWalkNeighborhoodCustomPodGroupIsNotASyntheticGroup(t *testing.T) {
	controller := true
	job := projectionCR("batch.volcano.sh", "Job", "ml", "train", map[string]any{})
	job.SetUID("job")
	group := projectionCR("scheduling.volcano.sh", "PodGroup", "ml", "train-pg", map[string]any{})
	group.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "batch.volcano.sh/v1", Kind: "Job", Name: "train", UID: "job", Controller: &controller}})
	sub := walkTestNeighborhood(t, newProjectionSession(t, job, group), job, NeighborhoodOptions{})
	found := false
	for _, n := range sub.Nodes {
		found = found || n.Name == "train-pg"
	}
	if !found {
		t.Fatalf("custom PodGroup missing from the neighborhood: %+v", sub.Nodes)
	}
}

// A Node reached from something it owns carries its instance type and
// health, as the root Node does.
func TestWalkNeighborhoodNeighborNodeCarriesItsInstanceTypeAndHealth(t *testing.T) {
	node := projectionObject(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node-a", UID: "node-a", Labels: map[string]string{"node.kubernetes.io/instance-type": "m6a.large"}},
		Status:     corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}},
	}, "", "Node")
	cni := projectionCR("vpcresources.k8s.aws", "CNINode", "", "node-a", map[string]any{})
	cni.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "v1", Kind: "Node", Name: "node-a", UID: "node-a"}})
	sub := walkTestNeighborhood(t, newProjectionSession(t, node, cni), cni, NeighborhoodOptions{})
	for _, n := range sub.Nodes {
		if n.Kind == KindNode && n.Name == "node-a" {
			if n.Data["instanceType"] != "m6a.large" || n.Status != StatusHealthy {
				t.Fatalf("node = %+v", n)
			}
			return
		}
	}
	t.Fatalf("the owning Node is missing: %+v", sub.Nodes)
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

// A built-in kind discovery doesn't list still names the resource its
// per-kind gate checks: the typed caches read it without discovery.
func TestNeighborhoodAccessNodeNamesBuiltinResourceWithoutDiscovery(t *testing.T) {
	a := &neighborhoodAccess{catalog: ownership.NewCatalog(nil, false)}
	for _, tc := range []struct{ group, kind, apiVersion, resource string }{
		{"rbac.authorization.k8s.io", "Role", "rbac.authorization.k8s.io/v1", "roles"},
		{"rbac.authorization.k8s.io", "RoleBinding", "rbac.authorization.k8s.io/v1", "rolebindings"},
		{"", "LimitRange", "v1", "limitranges"},
		{"example.com", "Role", "example.com/v1", ""},
	} {
		n := a.node(resourceid.NewRef(tc.group, tc.kind, "shop", "x"))
		if n.Data["apiVersion"] != tc.apiVersion || n.Data["resource"] != tc.resource {
			t.Errorf("%s/%s: apiVersion %v resource %v, want %s %s", tc.group, tc.kind, n.Data["apiVersion"], n.Data["resource"], tc.apiVersion, tc.resource)
		}
	}
}

// A root Radar can't read yet says why, for a caller who may read it; for one
// who may not, it reads as any absent root. A kind that can't live in the
// root's namespace isn't a candidate at all.
func TestWalkNeighborhoodRootUnavailableSaysWhy(t *testing.T) {
	root := projectionObject(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "shop"}}, "", "ConfigMap")
	s := newProjectionSession(t, root)
	s.gaps = map[resourceid.GroupKind]ownership.Gap{{Kind: "ConfigMap"}: {Kind: resourceid.GroupKind{Kind: "ConfigMap"}, Reason: readreason.KindSyncPending}}
	if sub := walkTestNeighborhood(t, s, root, NeighborhoodOptions{}); len(sub.Nodes) != 0 || sub.RootUnavailable != readreason.KindSyncPending {
		t.Fatalf("unreadable root: %+v", sub)
	}
	if sub := walkTestNeighborhood(t, s, root, NeighborhoodOptions{Allow: func(*Node) bool { return false }}); sub.RootUnavailable != "" {
		t.Fatalf("hidden root said why it couldn't be read: %+v", sub)
	}
	s.gaps[resourceid.GroupKind{Kind: "ConfigMap"}] = ownership.Gap{Kind: resourceid.GroupKind{Kind: "ConfigMap"}, Reason: readreason.RadarKindNotCollected}
	if sub := walkTestNeighborhood(t, s, root, NeighborhoodOptions{}); !slices.Equal(sub.UncollectedRoots, []resourceid.Ref{projectionRef(root)}) {
		t.Fatalf("uncollected root not offered for collection: %+v", sub)
	}
	if sub := walkTestNeighborhood(t, s, root, NeighborhoodOptions{Allow: func(*Node) bool { return false }}); len(sub.UncollectedRoots) != 0 {
		t.Fatalf("a hidden root was offered for collection: %+v", sub)
	}
	node := projectionObject(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n1"}}, "", "Node")
	s = newProjectionSession(t, node)
	sub, err := WalkNeighborhood(context.Background(), s, ResourceRef{Kind: "Node", Namespace: "shop", Name: "n1"}, NeighborhoodOptions{}, nil, nil)
	if err != nil || len(sub.Nodes) != 0 || sub.RootUnavailable != "" || len(s.reads) != 0 {
		t.Fatalf("cluster-scoped kind read in a namespace: %+v %v reads=%v", sub, err, s.reads)
	}
}

// Allow decides by kind and namespace, so the walk asks it once for each,
// however many objects of the kind it meets.
func TestWalkNeighborhoodAsksAllowOncePerKindAndNamespace(t *testing.T) {
	controller := true
	root := projectionObject(&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "shop", UID: "db"}, Spec: appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "db"}}}}}, "apps", "StatefulSet")
	svc := projectionObject(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "shop"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "db"}}}, "", "Service")
	objects := []runtime.Object{root, svc}
	for i := 0; i < 50; i++ {
		objects = append(objects, projectionObject(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("db-%02d", i), Namespace: "shop", UID: types.UID(fmt.Sprint(i)), Labels: map[string]string{"app": "db"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: "db", UID: "db", Controller: &controller}}}}, "", "Pod"))
	}
	for _, hops := range []int{1, 2} {
		asked := map[string]int{}
		sub := walkTestNeighborhood(t, newProjectionSession(t, objects...), root, NeighborhoodOptions{Hops: hops, Allow: func(n *Node) bool {
			if n.Name != "" {
				t.Errorf("hops=%d: Allow asked about %s by name", hops, n.Name)
			}
			asked[fmt.Sprint(n.Data["apiVersion"], " ", n.Data["resourceKind"], " ", n.Data["namespace"])]++
			return true
		}})
		if !slices.ContainsFunc(sub.Nodes, func(n Node) bool { return n.Kind == KindPodGroup && n.Data["podCount"] == 50 }) {
			t.Fatalf("hops=%d: the fixture no longer reaches the Pods: %+v", hops, sub.Nodes)
		}
		for key, calls := range asked {
			if calls > 1 {
				t.Errorf("hops=%d: Allow asked %d times about %s", hops, calls, key)
			}
		}
	}
}

// Within the budget, what manages the root and what exposes, protects or
// configures it come before what the root manages, and replicas come last.
func TestWalkNeighborhoodBudgetKeepsAttachmentsBeforeOwnedChildren(t *testing.T) {
	keeps := func(t *testing.T, sub *Subgraph, want ...string) {
		t.Helper()
		if !sub.Truncated || len(sub.Nodes) != neighborhoodDefaultMaxNodes {
			t.Fatalf("the fixture no longer fills the budget: truncated=%v nodes=%d", sub.Truncated, len(sub.Nodes))
		}
		for _, w := range want {
			if !slices.ContainsFunc(sub.Nodes, func(n Node) bool { return string(n.Kind)+"/"+n.Name == w }) {
				t.Errorf("owned children crowded out %s", w)
			}
		}
	}
	t.Run("controller", func(t *testing.T) {
		controller := true
		parent := projectionCR("example.io", "Database", "shop", "db", map[string]any{})
		parent.SetUID("database")
		root := projectionObject(&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "shop", UID: "db", OwnerReferences: []metav1.OwnerReference{{APIVersion: "example.io/v1", Kind: "Database", Name: "db", UID: "database", Controller: &controller}}}, Spec: appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "db"}}}}}, "apps", "StatefulSet")
		svc := projectionObject(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "shop"}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": "db"}}}, "", "Service")
		policy := projectionObject(&networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Name: "db-policy", Namespace: "shop"}, Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: map[string]string{"app": "db"}}}}, "networking.k8s.io", "NetworkPolicy")
		objects := []runtime.Object{parent, root, svc, policy}
		for i := 0; i < 30; i++ {
			objects = append(objects, projectionObject(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("cm-%02d", i), Namespace: "shop", UID: types.UID(fmt.Sprint("cm", i)), OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "StatefulSet", Name: "db", UID: "db"}}}}, "", "ConfigMap"))
		}
		keeps(t, walkTestNeighborhood(t, newProjectionSession(t, objects...), root, NeighborhoodOptions{}), "Database/db", "Service/db", "NetworkPolicy/db-policy")
	})
	t.Run("gitops manager", func(t *testing.T) {
		repo := projectionCR("source.toolkit.fluxcd.io", "GitRepository", "flux-system", "repo", map[string]any{})
		var entries []any
		objects := []runtime.Object{repo}
		for i := 0; i < 30; i++ {
			name := fmt.Sprintf("cm-%02d", i)
			entries = append(entries, map[string]any{"id": "shop_" + name + "__ConfigMap", "v": "v1"})
			objects = append(objects, projectionObject(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", UID: types.UID(name)}}, "", "ConfigMap"))
		}
		root := projectionCR("kustomize.toolkit.fluxcd.io", "Kustomization", "flux-system", "apps", map[string]any{
			"spec":   map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "repo"}},
			"status": map[string]any{"inventory": map[string]any{"entries": entries}},
		})
		parent := projectionCR("kustomize.toolkit.fluxcd.io", "Kustomization", "flux-system", "root", map[string]any{
			"status": map[string]any{"inventory": map[string]any{"entries": []any{map[string]any{"id": "flux-system_apps_kustomize.toolkit.fluxcd.io_Kustomization", "v": "v1"}}}},
		})
		objects = append(objects, root, parent)
		keeps(t, walkTestNeighborhood(t, newRecordingProjectionSession(t, objects...), root, NeighborhoodOptions{}), "GitRepository/repo", "Kustomization/root")
	})
}

// Objects admitted on the last hop are never asked about, but the owner
// references among them are already in hand: a ConfigMap's Deployment,
// ReplicaSet and Pod come back linked, not as unconnected spokes.
func TestWalkNeighborhoodLinksOwnersWithinTheLastHop(t *testing.T) {
	controller := true
	env := []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "cfg"}}}}
	spec := corev1.PodSpec{Containers: []corev1.Container{{Name: "c", EnvFrom: env}}}
	cm := projectionObject(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "shop", UID: "cfg"}}, "", "ConfigMap")
	dep := projectionObject(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", UID: "web"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: spec}}}, "apps", "Deployment")
	rs := projectionObject(&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "web-1", Namespace: "shop", UID: "rs", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "web", UID: "web", Controller: &controller}}}, Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{Spec: spec}}}, "apps", "ReplicaSet")
	pod := projectionObject(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web-1-a", Namespace: "shop", UID: "pod", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-1", UID: "rs", Controller: &controller}}}, Spec: spec}, "", "Pod")
	stale := projectionObject(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web-0-a", Namespace: "shop", UID: "stale", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-1", UID: "an-earlier-rs", Controller: &controller}}}, Spec: spec}, "", "Pod")
	sub := walkTestNeighborhood(t, newProjectionSession(t, cm, dep, rs, pod, stale), cm, NeighborhoodOptions{})
	names := map[string]string{}
	for _, n := range sub.Nodes {
		names[n.ID] = n.Name
	}
	edges := map[string]bool{}
	for _, e := range sub.Edges {
		if e.Type == EdgeManages {
			edges[names[e.Source]+" manages "+names[e.Target]] = true
		}
	}
	for _, want := range []string{"web manages web-1", "web-1 manages web-1-a"} {
		if !edges[want] {
			t.Errorf("missing %q; manages edges %v", want, edges)
		}
	}
	if edges["web-1 manages web-0-a"] {
		t.Error("linked a Pod to a ReplicaSet its owner reference doesn't name by UID")
	}
	if len(sub.Nodes) != 5 {
		t.Fatalf("the fixture no longer reaches every object at one hop: %+v", sub.Nodes)
	}
}

// slowStatusProvider answers a status lookup only once ctx has ended, as a
// provider stalled past the request's deadline would.
type slowStatusProvider struct {
	*mockProvider
	ctx context.Context
}

func (p slowStatusProvider) GetResourceStatus(string, string, string) *ResourceStatus {
	<-p.ctx.Done()
	return nil
}

// A deadline that passes while the walk is still working, whether reading the
// root or assembling the answer, is the deadline, never an absent root or a
// finished answer.
func TestWalkNeighborhoodDeadlineDuringTheWork(t *testing.T) {
	root := projectionObject(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", UID: "web"}}, "apps", "Deployment")
	t.Run("authorizing the root", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		opts := NeighborhoodOptions{Allow: func(*Node) bool { <-ctx.Done(); return true }}
		if sub, err := WalkNeighborhood(ctx, newProjectionSession(t, root), refOf(projectionRef(root)), opts, nil, nil); err != context.DeadlineExceeded {
			t.Fatalf("got %+v, %v; want %v", sub, err, context.DeadlineExceeded)
		}
	})
	t.Run("assembling the answer", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		provider := slowStatusProvider{mockProvider: &mockProvider{}, ctx: ctx}
		if sub, err := WalkNeighborhood(ctx, newProjectionSession(t, root), refOf(projectionRef(root)), NeighborhoodOptions{}, provider, nil); err != context.DeadlineExceeded {
			t.Fatalf("got %+v, %v; want %v", sub, err, context.DeadlineExceeded)
		}
	})
}

// The last hop's owner edges resolve owner references as ownership does: a
// version the cluster doesn't serve names no owner, even with a matching UID,
// and a kind spelled in another case still names its object.
func TestWalkNeighborhoodLastHopOwnersResolveAsOwnershipDoes(t *testing.T) {
	controller := true
	env := []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "cfg"}}}}
	spec := corev1.PodSpec{Containers: []corev1.Container{{Name: "c", EnvFrom: env}}}
	cm := projectionObject(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "cfg", Namespace: "shop", UID: "cfg"}}, "", "ConfigMap")
	dep := projectionObject(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", UID: "web"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: spec}}}, "apps", "Deployment")
	rs := projectionObject(&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "web-1", Namespace: "shop", UID: "rs", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v99", Kind: "Deployment", Name: "web", UID: "web", Controller: &controller}}}, Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{Spec: spec}}}, "apps", "ReplicaSet")
	pod := projectionObject(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web-1-a", Namespace: "shop", UID: "pod", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "replicaset", Name: "web-1", UID: "rs", Controller: &controller}}}, Spec: spec}, "", "Pod")
	sub := walkTestNeighborhood(t, newProjectionSession(t, cm, dep, rs, pod), cm, NeighborhoodOptions{})
	names := map[string]string{}
	for _, n := range sub.Nodes {
		names[n.ID] = n.Name
	}
	edges := map[string]bool{}
	for _, e := range sub.Edges {
		if e.Type == EdgeManages {
			edges[names[e.Source]+" manages "+names[e.Target]] = true
		}
	}
	if len(sub.Nodes) != 4 {
		t.Fatalf("the fixture no longer reaches every object at one hop: %+v", sub.Nodes)
	}
	if edges["web manages web-1"] {
		t.Error("linked an owner through an apiVersion the cluster doesn't serve")
	}
	if !edges["web-1 manages web-1-a"] {
		t.Errorf("missed the owner a differently cased kind names; manages edges %v", edges)
	}
}

// Objects admitted on the last hop are never answered, but what they declare
// of one another is in hand: a Kustomization's source and a Certificate's
// issuer come back linked when both ends are admitted, and never to an object
// the caller may not read.
func TestWalkNeighborhoodLinksLastHopDeclarations(t *testing.T) {
	entry := func(ns, name, group, kind string) map[string]any {
		return map[string]any{"id": ns + "_" + name + "_" + group + "_" + kind, "v": "v1"}
	}
	root := projectionCR("kustomize.toolkit.fluxcd.io", "Kustomization", "flux-system", "root", map[string]any{"status": map[string]any{"inventory": map[string]any{"entries": []any{
		entry("flux-system", "apps", "kustomize.toolkit.fluxcd.io", "Kustomization"),
		entry("flux-system", "repo", "source.toolkit.fluxcd.io", "GitRepository"),
		entry("shop", "web-tls", "cert-manager.io", "Certificate"),
		entry("", "letsencrypt", "cert-manager.io", "ClusterIssuer"),
	}}}})
	apps := projectionCR("kustomize.toolkit.fluxcd.io", "Kustomization", "flux-system", "apps", map[string]any{"spec": map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "repo"}}})
	repo := projectionCR("source.toolkit.fluxcd.io", "GitRepository", "flux-system", "repo", map[string]any{})
	cert := projectionCR("cert-manager.io", "Certificate", "shop", "web-tls", map[string]any{"spec": map[string]any{"issuerRef": map[string]any{"kind": "ClusterIssuer", "name": "letsencrypt", "group": "cert-manager.io"}}})
	issuer := projectionCR("cert-manager.io", "ClusterIssuer", "", "letsencrypt", map[string]any{})
	edgesOf := func(sub *Subgraph) map[string]bool {
		names := map[string]string{}
		for _, n := range sub.Nodes {
			names[n.ID] = n.Name
		}
		out := map[string]bool{}
		for _, e := range sub.Edges {
			out[names[e.Source]+" "+string(e.Type)+" "+names[e.Target]] = true
		}
		return out
	}
	sub := walkTestNeighborhood(t, newRecordingProjectionSession(t, root, apps, repo, cert, issuer), root, NeighborhoodOptions{Profile: ProfileAll})
	if len(sub.Nodes) != 5 {
		t.Fatalf("the fixture no longer admits every member at one hop: %+v", sub.Nodes)
	}
	edges := edgesOf(sub)
	for _, want := range []string{"repo configures apps", "web-tls uses letsencrypt"} {
		if !edges[want] {
			t.Errorf("missing %q; edges %v", want, edges)
		}
	}
	hidden := walkTestNeighborhood(t, newRecordingProjectionSession(t, root, apps, repo, cert, issuer), root, NeighborhoodOptions{Profile: ProfileAll, Allow: func(n *Node) bool { return n.Data["resourceKind"] != "ClusterIssuer" }})
	ids := map[string]bool{}
	for _, n := range hidden.Nodes {
		ids[n.ID] = true
	}
	for _, e := range hidden.Edges {
		if !ids[e.Source] || !ids[e.Target] || strings.Contains(e.ID, "letsencrypt") {
			t.Errorf("linked an object the caller may not read: %s", e.ID)
		}
	}
	if len(hidden.Nodes) != 4 || !edgesOf(hidden)["repo configures apps"] {
		t.Errorf("withholding the issuer changed the rest: %+v", hidden)
	}
}

// A relation the profile doesn't follow from a node can't make the answer
// incomplete: a GitOps root's managed Deployment mounting a Secret the caller
// may not read gives the same answer, and no omission, as when the caller may.
func TestWalkNeighborhoodExcludedRelationReportsNoOmission(t *testing.T) {
	root := projectionCR("kustomize.toolkit.fluxcd.io", "Kustomization", "flux-system", "apps", map[string]any{"status": map[string]any{"inventory": map[string]any{"entries": []any{
		map[string]any{"id": "shop_web_apps_Deployment", "v": "v1"},
	}}}})
	volumes := []corev1.Volume{{Name: "key", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "key"}}}}
	dep := projectionObject(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", UID: "web"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Volumes: volumes}}}}, "apps", "Deployment")
	secret := projectionObject(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "key", Namespace: "shop"}}, "", "Secret")
	var answers []string
	for _, readable := range []bool{true, false} {
		sub := walkTestNeighborhood(t, newRecordingProjectionSession(t, root, dep, secret), root, NeighborhoodOptions{Hops: 2, Allow: func(n *Node) bool { return readable || n.Data["resourceKind"] != "Secret" }})
		if sub.RBACDenied != 0 {
			t.Errorf("secrets readable=%v: an excluded relation reported an omission", readable)
		}
		body, _ := json.Marshal(sub)
		answers = append(answers, string(body))
	}
	if answers[0] != answers[1] {
		t.Errorf("the Secret's readability changed the answer:\n%s\n%s", answers[0], answers[1])
	}
	if !strings.Contains(answers[0], `"name":"web"`) {
		t.Fatalf("the fixture no longer reaches the Deployment: %s", answers[0])
	}
	// When the Secret is admitted on its own, as a member, the Deployment's
	// mount of it is still drawn.
	root.Object["status"] = map[string]any{"inventory": map[string]any{"entries": []any{
		map[string]any{"id": "shop_web_apps_Deployment", "v": "v1"},
		map[string]any{"id": "shop_key__Secret", "v": "v1"},
	}}}
	sub := walkTestNeighborhood(t, newRecordingProjectionSession(t, root, dep, secret), root, NeighborhoodOptions{Hops: 2})
	names := map[string]string{}
	for _, n := range sub.Nodes {
		names[n.ID] = n.Name
	}
	mounted := slices.ContainsFunc(sub.Edges, func(e Edge) bool {
		return e.Type == EdgeConfigures && names[e.Source] == "key" && names[e.Target] == "web"
	})
	if !mounted {
		t.Errorf("an admitted Secret's mount was not drawn: %+v", sub.Edges)
	}
}

// What a last-hop object selects among the other admitted objects is drawn
// too, from the objects already read: a controller's Service and the
// Deployment it exposes, both owned and admitted at one hop, come back linked,
// and drawing the link reads nothing.
func TestWalkNeighborhoodLinksLastHopSelections(t *testing.T) {
	controller := true
	owner := []metav1.OwnerReference{{APIVersion: "example.io/v1", Kind: "App", Name: "web", UID: "app", Controller: &controller}}
	root := projectionCR("example.io", "App", "shop", "web", map[string]any{})
	root.SetUID("app")
	dep := projectionObject(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", UID: "dep", OwnerReferences: owner}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}}}}}, "apps", "Deployment")
	walk := func(app string) (*Subgraph, int) {
		svc := projectionObject(&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", UID: "svc", OwnerReferences: owner}, Spec: corev1.ServiceSpec{Selector: map[string]string{"app": app}}}, "", "Service")
		s := newProjectionSession(t, root, svc, dep)
		return walkTestNeighborhood(t, s, root, NeighborhoodOptions{}), len(s.reads)
	}
	sub, reads := walk("web")
	_, unmatchedReads := walk("other")
	if len(sub.Nodes) != 3 {
		t.Fatalf("the fixture no longer admits the Service and Deployment at one hop: %+v", sub.Nodes)
	}
	names := map[string]string{}
	for _, n := range sub.Nodes {
		names[n.ID] = string(n.Kind) + "/" + n.Name
	}
	if !slices.ContainsFunc(sub.Edges, func(e Edge) bool {
		return e.Type == EdgeExposes && names[e.Source] == "Service/web" && names[e.Target] == "Deployment/web"
	}) {
		t.Errorf("missing Service exposes Deployment; edges %+v", sub.Edges)
	}
	if reads != unmatchedReads {
		t.Errorf("drawing the selection read %d objects", reads-unmatchedReads)
	}
}

// A Node and a Karpenter NodeClass carry the IDs the topology gives them,
// whatever else the answer holds, so a node can be matched across answers.
func TestWalkNeighborhoodNodesAndNodeClassesKeepTheirTopologyIDs(t *testing.T) {
	node := projectionObject(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node-a", UID: "node-a"}}, "", "Node")
	cilium := projectionCR("cilium.io", "CiliumNode", "", "node-a", map[string]any{})
	cilium.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "v1", Kind: "Node", Name: "node-a", UID: "node-a"}})
	nodeClass := projectionCR("karpenter.k8s.aws", "EC2NodeClass", "", "default", map[string]any{})
	ids := map[string]bool{}
	for _, sub := range []*Subgraph{
		walkTestNeighborhood(t, newProjectionSession(t, node, cilium), cilium, NeighborhoodOptions{}),
		walkTestNeighborhood(t, newProjectionSession(t, nodeClass), nodeClass, NeighborhoodOptions{}),
	} {
		for _, n := range sub.Nodes {
			ids[n.ID] = true
		}
	}
	for _, want := range []string{"node//node-a", "nodeclass//default/karpenter.k8s.aws/ec2nodeclass"} {
		if !ids[want] {
			t.Errorf("missing node %s; ids %v", want, ids)
		}
	}
}

// A node names the version Radar reads its object at, the most stable one
// served, not the first in the catalog's sorted list.
func TestWalkNeighborhoodNodeNamesTheVersionRead(t *testing.T) {
	workload := projectionCR("kueue.x-k8s.io", "Workload", "shop", "job-1", map[string]any{})
	workload.SetAPIVersion("kueue.x-k8s.io/v1beta2")
	served := ownership.ServedResource{Group: "kueue.x-k8s.io", Version: "v1beta1", Kind: "Workload", Resource: "Workloads", Namespaced: true, Verbs: []string{"delete", "get", "list", "watch"}}
	s := projectionSessionOf(t, false, []runtime.Object{workload}, served)
	if k, _ := s.Catalog().Kind(resourceid.GroupKind{Group: "kueue.x-k8s.io", Kind: "Workload"}); len(k.Versions) != 2 {
		t.Fatalf("the fixture no longer serves both versions: %+v", k)
	}
	sub := walkTestNeighborhood(t, s, workload, NeighborhoodOptions{})
	if len(sub.Nodes) != 1 || sub.Nodes[0].Data["apiVersion"] != "kueue.x-k8s.io/v1beta2" {
		t.Fatalf("got %+v, want apiVersion kueue.x-k8s.io/v1beta2", sub.Nodes)
	}
	a := &neighborhoodAccess{catalog: s.Catalog()}
	if got := a.node(projectionRef(workload)).Data["apiVersion"]; got != "kueue.x-k8s.io/v1beta2" {
		t.Errorf("the walk's own node for the kind names %v", got)
	}
}

// The root's own descendants come before what a neighbor manages: a
// Deployment whose operator manages dozens of objects still shows its own
// ReplicaSet's Pod within the budget.
func TestWalkNeighborhoodBudgetKeepsTheRootsOwnPods(t *testing.T) {
	controller := true
	install := projectionCR("operator.example.io", "Installation", "", "default", map[string]any{})
	install.SetUID("install")
	byInstall := []metav1.OwnerReference{{APIVersion: "operator.example.io/v1", Kind: "Installation", Name: "default", UID: "install", Controller: &controller}}
	dep := projectionObject(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "controllers", Namespace: "calico-system", UID: "dep", OwnerReferences: byInstall}}, "apps", "Deployment")
	rs := projectionObject(&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "controllers-1", Namespace: "calico-system", UID: "rs", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "controllers", UID: "dep", Controller: &controller}}}}, "apps", "ReplicaSet")
	pod := projectionObject(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "controllers-1-a", Namespace: "calico-system", UID: "pod", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "controllers-1", UID: "rs", Controller: &controller}}}}, "", "Pod")
	objects := []runtime.Object{install, dep, rs, pod}
	for i := 0; i < 30; i++ {
		objects = append(objects, projectionObject(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("cm-%02d", i), Namespace: "calico-system", UID: types.UID(fmt.Sprint("cm", i)), OwnerReferences: byInstall}}, "", "ConfigMap"))
	}
	sub := walkTestNeighborhood(t, newProjectionSession(t, objects...), dep, NeighborhoodOptions{Hops: 2})
	if !sub.Truncated || len(sub.Nodes) != neighborhoodDefaultMaxNodes {
		t.Fatalf("the fixture no longer fills the budget: truncated=%v nodes=%d", sub.Truncated, len(sub.Nodes))
	}
	for _, want := range []string{"Installation/default", "ReplicaSet/controllers-1", "Pod/controllers-1-a"} {
		if !slices.ContainsFunc(sub.Nodes, func(n Node) bool { return string(n.Kind)+"/"+n.Name == want }) {
			t.Errorf("the operator's objects crowded out %s", want)
		}
	}
}
