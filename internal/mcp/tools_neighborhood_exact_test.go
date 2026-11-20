package mcp

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
)

// setupExactKindNeighborhood seeds a ReplicaSet in "shop" that owns a
// VulnerabilityReport, a Role and a LimitRange: kinds a user needs their own
// permission for beyond namespace access.
func setupExactKindNeighborhood(t *testing.T) {
	t.Helper()
	const uid = types.UID("uid-shop-web")
	owner := []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web", UID: uid}}
	replicas := int32(1)
	client := fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}, Status: corev1.NamespaceStatus{Phase: corev1.NamespaceActive}},
		&appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", UID: uid},
			Spec: appsv1.ReplicaSetSpec{
				Replicas: &replicas,
				Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
				Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}}},
			},
		},
		&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "web-role", Namespace: "shop", OwnerReferences: owner}},
		&corev1.LimitRange{ObjectMeta: metav1.ObjectMeta{Name: "web-limits", Namespace: "shop", OwnerReferences: owner}},
	)
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatal(err)
	}
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected, Context: "fake-test"})
	k8s.ResetTestDynamicState()
	t.Cleanup(func() {
		k8s.ResetTestDynamicState()
		k8s.ResetTestState()
		getPermCache().Invalidate()
	})

	report := &unstructured.Unstructured{}
	report.SetAPIVersion("aquasecurity.github.io/v1alpha1")
	report.SetKind("VulnerabilityReport")
	report.SetNamespace("shop")
	report.SetName("replicaset-web-app")
	report.SetOwnerReferences(owner)
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{vulnReportGVR: "VulnerabilityReportList"}, report)
	verbs := []string{"delete", "get", "list", "watch"}
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{
		{Version: "v1", Kind: "Pod", Name: "pods", Namespaced: true, Verbs: verbs},
		{Version: "v1", Kind: "LimitRange", Name: "limitranges", Namespaced: true, Verbs: verbs},
		{Group: "apps", Version: "v1", Kind: "ReplicaSet", Name: "replicasets", Namespaced: true, Verbs: verbs},
		{Group: "rbac.authorization.k8s.io", Version: "v1", Kind: "Role", Name: "roles", Namespaced: true, Verbs: verbs},
		{Group: vulnReportGVR.Group, Version: vulnReportGVR.Version, Kind: "VulnerabilityReport", Name: vulnReportGVR.Resource, Namespaced: true, IsCRD: true, Verbs: verbs},
	}); err != nil {
		t.Fatal(err)
	}
	dc := k8s.GetDynamicResourceCache()
	if err := dc.EnsureWatching(vulnReportGVR); err != nil {
		t.Fatal(err)
	}
	if !dc.WaitForSync(vulnReportGVR, 2*time.Second) {
		t.Fatal("VulnerabilityReport cache did not sync")
	}
}

// Namespace access lets a user see a ReplicaSet, but its Trivy report, Role
// and LimitRange each need the user's own permission on that kind, as a
// resource read does.
func TestNeighborhoodMCP_ExactKindsNeedTheirOwnPermission(t *testing.T) {
	setupExactKindNeighborhood(t)
	exact := []string{"VulnerabilityReport/replicaset-web-app", "Role/web-role", "LimitRange/web-limits"}
	neighbors := func(user string, grant bool) []string {
		t.Helper()
		ctx := withRestrictedUser(t, user, []string{"shop"})
		perms := getPermCache().Get(user, nil)
		for _, gr := range []schema.GroupResource{vulnReportGVR.GroupResource(), {Group: "rbac.authorization.k8s.io", Resource: "roles"}, {Resource: "limitranges"}} {
			for _, verb := range []string{"get", "list"} {
				perms.SetCanI(verb, gr.Group, gr.Resource, "shop", grant)
			}
		}
		call, _, err := handleGetNeighborhood(ctx, nil, getNeighborhoodInput{Kind: "replicaset", Group: "apps", Namespace: "shop", Name: "web", Profile: "all"})
		if err != nil {
			t.Fatal(err)
		}
		var result neighborhoodResult
		if err := json.Unmarshal([]byte(call.Content[0].(*mcp.TextContent).Text), &result); err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, n := range result.Subgraph.Nodes {
			out = append(out, string(n.Kind)+"/"+n.Name)
		}
		return out
	}
	granted := neighbors("granted", true)
	for _, want := range exact {
		if !slices.Contains(granted, want) {
			t.Fatalf("granted user: neighborhood %v lacks %s; the fixture no longer reaches it", granted, want)
		}
	}
	denied := neighbors("denied", false)
	for _, hidden := range exact {
		if slices.Contains(denied, hidden) {
			t.Errorf("user without permission on the kind sees %s in %v", hidden, denied)
		}
	}
}
