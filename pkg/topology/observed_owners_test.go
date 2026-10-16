package topology

import (
	"encoding/json"
	"fmt"
	"github.com/skyhook-io/radar/pkg/resourceid"
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

func TestObservedOwnershipDoesNotEraseIndependentManagesEvidence(t *testing.T) {
	for _, controller := range []bool{false, true} {
		nodes := []Node{
			{ID: "certificate/team/cert", Kind: KindCertificate, Name: "cert", uid: "cert-now", Data: map[string]any{"namespace": "team", "apiVersion": "cert-manager.io/v1"}},
			{ID: "secret/team/tls", Kind: KindSecret, Name: "tls", observed: true, Data: map[string]any{"namespace": "team"}, ownerReferences: []metav1.OwnerReference{{APIVersion: "cert-manager.io/v1", Kind: "Certificate", Name: "cert", UID: "cert-deleted", Controller: &controller}}},
		}
		edges := addObservedOwnerEdges(nodes, []Edge{
			{ID: "declared-secret", Source: nodes[0].ID, Target: nodes[1].ID, Type: EdgeManages},
			{ID: "old-metadata", Source: nodes[0].ID, Target: nodes[1].ID, Type: EdgeManages, metadataOwner: true},
		})
		if len(edges) != 1 || edges[0].ID != "declared-secret" || edges[0].OwnerController != nil {
			t.Fatalf("independent declaration lost or falsely classified: %+v", edges)
		}
	}
}

func TestCalicoProducerRetainsAliasIncarnationsAndGenericChildren(t *testing.T) {
	primary := schema.GroupVersionResource{Group: calicoProjectGroup, Version: "v3", Resource: "networkpolicies"}
	alias := schema.GroupVersionResource{Group: calicoCRDGroup, Version: "v1", Resource: "networkpolicies"}
	widgets := schema.GroupVersionResource{Group: "relationships.example.io", Version: "v1", Resource: "widgets"}
	policy := calicoTestObject(primary.Group, primary.Version, "NetworkPolicy", "team", "parent", map[string]any{"selector": "all()"})
	policy.SetUID("primary-now")
	second := calicoTestObject(alias.Group, alias.Version, "NetworkPolicy", "team", "parent", map[string]any{"selector": "all()"})
	second.SetUID("alias-now")
	for _, uid := range []types.UID{"alias-now", "alias-deleted"} {
		child := genericIdentityObject(widgets, "Widget", "team", "child", metav1.OwnerReference{APIVersion: alias.Group + "/" + alias.Version, Kind: "NetworkPolicy", Name: "parent", UID: uid})
		dynamic := &calicoTestDynamic{gvrs: map[string]schema.GroupVersionResource{primary.Group + "\x00NetworkPolicy": primary, alias.Group + "\x00NetworkPolicy": alias, widgets.Group + "\x00Widget": widgets}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{primary: {policy}, alias: {second}, widgets: {child}}, watched: []schema.GroupVersionResource{primary, alias, widgets}}
		builder := NewBuilder(&mockProvider{})
		builder.WithDynamic(dynamic)
		topo, err := builder.Build(DefaultBuildOptions())
		if err != nil {
			t.Fatal(err)
		}
		parent, found := IndexByResource(topo).ResolveObservedOwner(resourceid.OwnerReference(alias.Group+"/"+alias.Version, "NetworkPolicy", "parent", string(uid), "team"))
		if parent == nil || !parent.observed || found != (uid == "alias-now") {
			t.Fatalf("producer alias identity: %+v, %v", parent, found)
		}
		linked := false
		for _, edge := range topo.Edges {
			if edge.Source == parent.ID && strings.Contains(edge.Target, "child") && edge.Type == EdgeManages {
				linked = true
			}
		}
		if linked != (uid == "alias-now") {
			t.Fatalf("generic child enrollment uid %s: %+v", uid, topo.Edges)
		}
	}
}

func TestBuilderKeepsCertificateDeclaredSecretAcrossOwnerReplacement(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "cert-manager.io", Version: "v1", Resource: "certificates"}
	cert := genericIdentityObject(gvr, "Certificate", "team", "cert")
	cert.SetUID("cert-now")
	cert.Object["spec"] = map[string]any{"secretName": "tls"}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "tls", Namespace: "team", UID: "secret-now", OwnerReferences: []metav1.OwnerReference{{APIVersion: "cert-manager.io/v1", Kind: "Certificate", Name: "cert", UID: "cert-deleted"}}}}
	dynamic := &genericIdentityDynamic{watched: []schema.GroupVersionResource{gvr}, kinds: map[schema.GroupVersionResource]string{gvr: "Certificate"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {cert}}, listCalls: map[schema.GroupVersionResource]int{}}
	opts := DefaultBuildOptions()
	opts.IncludeSecrets = true
	topo, err := NewBuilder(&mockProvider{secrets: []*corev1.Secret{secret}, deployments: []*appsv1.Deployment{{ObjectMeta: metav1.ObjectMeta{Name: "consumer", Namespace: "team"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "demo", EnvFrom: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "tls"}}}}}}}}}}}}).WithDynamic(dynamic).Build(opts)
	if err != nil {
		t.Fatal(err)
	}
	edge := findEdge(topo, "certificate/team/cert", "secret/team/tls")
	if edge == nil || edge.OwnerController != nil || edge.metadataOwner {
		t.Fatalf("declared Secret link erased or falsely classified: %+v", topo.Edges)
	}
}

func TestObservedOwnerJoinIncludesReflectionMaterializedNodes(t *testing.T) {
	parent := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "root", Namespace: "edge", UID: "root-now"}}
	source := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "source", Namespace: "app", UID: "source-now"}}
	mirror := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "mirror", Namespace: "edge", UID: "mirror-now", Annotations: map[string]string{"reflector.v1.k8s.emberstack.com/reflects": "app/source"}, OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "root", UID: "root-now"}}}}
	opts := DefaultBuildOptions()
	opts.IncludeSecrets = true
	topo, err := NewBuilder(&mockProvider{deployments: []*appsv1.Deployment{parent}, secrets: []*corev1.Secret{source, mirror}}).Build(opts)
	if err != nil {
		t.Fatal(err)
	}
	if edge := findEdge(topo, "deployment/edge/root", "secret/edge/mirror"); edge == nil || edge.OwnerController == nil || *edge.OwnerController {
		t.Fatalf("late observed ownership missing: %+v", topo.Edges)
	}
	if edge := findEdge(topo, "secret/app/source", "secret/edge/mirror"); edge == nil || edge.Type != EdgeConfigures {
		t.Fatalf("independent reflection missing: %+v", topo.Edges)
	}
	topo.StripSecretsExcept(map[SARTuple]bool{{Resource: "secrets", Namespace: "app"}: true})
	for _, edge := range topo.Edges {
		if edge.Target == "secret/edge/mirror" {
			t.Fatalf("denied reflected Secret edge retained: %+v", edge)
		}
	}
}
