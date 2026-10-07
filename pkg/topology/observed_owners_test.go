package topology

import (
	"encoding/json"
	"fmt"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"reflect"
	"strings"
	"testing"
)

func TestObservedOwnershipAcrossTypedAndCuratedNodes(t *testing.T) {
	controller := true
	nodes := []Node{
		{ID: "deployment/team/controller", Kind: KindDeployment, Name: "controller", uid: "controller-now", Data: map[string]any{"namespace": "team"}},
		{ID: "configmap/team/other", Kind: KindConfigMap, Name: "other", uid: "other-now", Data: map[string]any{"namespace": "team"}},
		{ID: "certificate/team/cert", Kind: KindCertificate, Name: "cert", uid: "child-now", Data: map[string]any{"namespace": "team"}, ownerReferences: []metav1.OwnerReference{
			{APIVersion: "v1", Kind: "ConfigMap", Name: "other", UID: "other-now"},
			{APIVersion: "apps/v1", Kind: "Deployment", Name: "controller", UID: "controller-now", Controller: &controller},
			{APIVersion: "cert-manager.io/v1", Kind: "Certificate", Name: "cert", UID: "child-now"},
			{APIVersion: "apps/v1", Kind: "Deployment", Name: "missing", UID: "missing-now"},
		}},
	}
	edges := addObservedOwnerEdges(nodes, nil)
	if len(edges) != 2 {
		t.Fatalf("observed parents: %+v", edges)
	}
	topo := &Topology{Nodes: nodes, Edges: edges}
	for _, idx := range []*RelationshipsIndex{nil, IndexByResource(topo)} {
		ref := walkTopmostOwnerFromNodeID(nodes[2].ID, topo, nil, idx)
		if ref == nil || ref.Name != "controller" || ref.Kind != "Deployment" {
			t.Fatalf("controller displaced: %+v", ref)
		}
		rel := GetRelationshipsWithObject("Certificate", "team", "cert", nil, topo, nil, nil, idx)
		if rel == nil || rel.Owner == nil || rel.Owner.Name != "controller" {
			t.Fatalf("controller relationship: %+v", rel)
		}
	}
	again := addObservedOwnerEdges(nodes, append([]Edge(nil), edges...))
	if !reflect.DeepEqual(edges, again) {
		t.Fatalf("repeat joins: %+v", again)
	}
	raw, err := json.Marshal(topo)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "ownerReferences") || strings.Contains(string(raw), "child-now") || !strings.Contains(string(raw), `"ownerController":true`) {
		t.Fatalf("wire provenance: %s", raw)
	}
	// A known replacement contradicts just metadata ownership, not other edges.
	nodes[2].ownerReferences[1].UID = "controller-deleted"
	edges = append(edges, Edge{ID: "dependency", Source: nodes[0].ID, Target: nodes[2].ID, Type: EdgeUses})
	edges = addObservedOwnerEdges(nodes, edges)
	if len(edges) != 2 || edges[1].Type != EdgeUses {
		t.Fatalf("replacement pruning: %+v", edges)
	}
}
func TestObservedOwnershipRequiresExactGroupAndScope(t *testing.T) {
	nodes := []Node{
		{ID: "deployment/other/root", Kind: KindDeployment, Name: "root", uid: "foreign", Data: map[string]any{"namespace": "other"}},
		{ID: "deployment/team/root/custom.example", Kind: "Deployment", Name: "root", uid: "custom", Data: map[string]any{"namespace": "team", "apiVersion": "custom.example/v1"}},
		{ID: "node//worker", Kind: KindNode, Name: "worker", uid: "node-now", Data: map[string]any{}},
		{ID: "secret/team/child", Kind: KindSecret, Name: "child", Data: map[string]any{"namespace": "team"}, ownerReferences: []metav1.OwnerReference{
			{APIVersion: "apps/v1", Kind: "Deployment", Name: "root", UID: "foreign"},
			{APIVersion: "v1", Kind: "Node", Name: "worker", UID: "node-now"},
		}},
	}
	edges := addObservedOwnerEdges(nodes, nil)
	if len(edges) != 1 || edges[0].Source != nodes[2].ID {
		t.Fatalf("scope/group collision: %+v", edges)
	}
}
func TestBuilderRetainsObservedTypedOwnerMetadata(t *testing.T) {
	controller := true
	parent := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "root", Namespace: "team", UID: "root-now"}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credentials", Namespace: "team", UID: "secret-now", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "root", UID: "root-now", Controller: &controller}}}}
	parent.Spec.Template.Spec.Containers = []corev1.Container{{Name: "app", Image: "demo", EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: secret.Name}}}}}}
	before := secret.DeepCopy()
	for _, uid := range []types.UID{"root-now", "deleted"} {
		secret.OwnerReferences[0].UID = uid
		opts := DefaultBuildOptions()
		opts.IncludeSecrets = true
		topo, err := NewBuilder(&mockProvider{deployments: []*appsv1.Deployment{parent}, secrets: []*corev1.Secret{secret}}).Build(opts)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, edge := range topo.Edges {
			if edge.Type == EdgeManages && edge.Source == "deployment/team/root" && edge.Target == "secret/team/credentials" {
				found = true
				if edge.OwnerController == nil || !*edge.OwnerController {
					t.Fatal("controller metadata absent")
				}
			}
		}
		if found != (uid == "root-now") {
			t.Fatalf("typed owner UID %s: %+v", uid, topo.Edges)
		}
	}
	secret.OwnerReferences[0].UID = "root-now"
	if !reflect.DeepEqual(secret, before) {
		t.Fatal("builder mutated source metadata")
	}
}

func TestObservedOwnersDoNotPromoteDeclarationStubs(t *testing.T) {
	nodes := []Node{
		{ID: "secret/team/absent", Kind: KindSecret, Name: "absent", Data: map[string]any{"namespace": "team"}},
		{ID: "configmap/team/child", Kind: KindConfigMap, Name: "child", uid: "child-now", Data: map[string]any{"namespace": "team"}, ownerReferences: []metav1.OwnerReference{{APIVersion: "v1", Kind: "Secret", Name: "absent", UID: "missing-object"}}},
	}
	if edges := addObservedOwnerEdges(nodes, nil); len(edges) != 0 {
		t.Fatalf("declaration promoted to observed owner: %+v", edges)
	}
}

func TestGenericEnrollmentRejectsDeclarationOnlyOwner(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "relationships.example.io", Version: "v1", Resource: "widgets"}
	child := genericIdentityObject(gvr, "Widget", "team", "child", metav1.OwnerReference{APIVersion: "v1", Kind: "Secret", Name: "absent", UID: "absent-now"})
	dp := &genericIdentityDynamic{watched: []schema.GroupVersionResource{gvr}, kinds: map[schema.GroupVersionResource]string{gvr: "Widget"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {child}}, listCalls: map[schema.GroupVersionResource]int{}}
	nodes, edges := (&Builder{dynamic: dp}).addGenericCRDNodes([]Node{{ID: "secret/team/absent", Kind: KindSecret, Name: "absent", Data: map[string]any{"namespace": "team"}}}, nil, DefaultBuildOptions())
	if len(nodes) != 1 || len(edges) != 0 {
		t.Fatalf("declaration stub enrolled a child: %+v %+v", nodes, edges)
	}
}

func BenchmarkObservedOwnerJoin(b *testing.B) {
	nodes := []Node{{ID: "deployment/team/root", Kind: KindDeployment, Name: "root", uid: "root-now", Data: map[string]any{"namespace": "team"}}}
	for i := 0; i < 5000; i++ {
		nodes = append(nodes, Node{ID: fmt.Sprintf("widget/team/item-%d", i), Kind: "Widget", Name: fmt.Sprintf("item-%d", i), observed: true, Data: map[string]any{"namespace": "team", "apiVersion": "example.io/v1"}, ownerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "root", UID: "root-now"}}})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if len(addObservedOwnerEdges(nodes, nil)) != 5000 {
			b.Fatal("owner edges lost")
		}
	}
}
