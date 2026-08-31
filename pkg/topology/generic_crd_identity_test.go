package topology

import (
	"fmt"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/k8score"
)

type genericIdentityDynamic struct {
	watched   []schema.GroupVersionResource
	kinds     map[schema.GroupVersionResource]string
	resources map[schema.GroupVersionResource][]*unstructured.Unstructured
	listCalls map[schema.GroupVersionResource]int
}

type dynamicProviderWithoutExactCRD struct {
	DynamicProvider
}

func (d *genericIdentityDynamic) List(gvr schema.GroupVersionResource, _ string) ([]*unstructured.Unstructured, error) {
	return d.resources[gvr], nil
}

func (d *genericIdentityDynamic) ListNamespaces(gvr schema.GroupVersionResource, _ []string) ([]*unstructured.Unstructured, error) {
	d.listCalls[gvr]++
	return d.resources[gvr], nil
}

func (d *genericIdentityDynamic) Get(gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	for _, resource := range d.resources[gvr] {
		if resource.GetNamespace() == namespace && resource.GetName() == name {
			return resource, nil
		}
	}
	return nil, fmt.Errorf("not found")
}

func (d *genericIdentityDynamic) GetWatchedResources() []schema.GroupVersionResource {
	return d.watched
}

func (d *genericIdentityDynamic) GetDiscoveryStatus() k8score.CRDDiscoveryStatus {
	return k8score.CRDDiscoveryComplete
}

func (d *genericIdentityDynamic) GetGVR(kindOrName string) (schema.GroupVersionResource, bool) {
	for _, gvr := range d.watched {
		if strings.EqualFold(d.kinds[gvr], kindOrName) || strings.EqualFold(gvr.Resource, kindOrName) {
			return gvr, true
		}
	}
	return schema.GroupVersionResource{}, false
}

func (d *genericIdentityDynamic) GetGVRWithGroup(kindOrName, group string) (schema.GroupVersionResource, bool) {
	for _, gvr := range d.watched {
		if gvr.Group == group && (strings.EqualFold(d.kinds[gvr], kindOrName) || strings.EqualFold(gvr.Resource, kindOrName)) {
			return gvr, true
		}
	}
	return schema.GroupVersionResource{}, false
}

func (d *genericIdentityDynamic) GetKindForGVR(gvr schema.GroupVersionResource) string {
	return d.kinds[gvr]
}

func (d *genericIdentityDynamic) IsCRD(kind string) bool {
	for _, candidate := range d.kinds {
		if candidate == kind {
			return true
		}
	}
	return false
}

func (d *genericIdentityDynamic) IsCRDGVR(gvr schema.GroupVersionResource) bool {
	_, ok := d.kinds[gvr]
	return ok
}

func genericIdentityObject(gvr schema.GroupVersionResource, kind, namespace, name string, owners ...metav1.OwnerReference) *unstructured.Unstructured {
	apiVersion := gvr.Version
	if gvr.Group != "" {
		apiVersion = gvr.Group + "/" + gvr.Version
	}
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata": map[string]any{
			"name":      name,
			"namespace": namespace,
		},
	}}
	obj.SetOwnerReferences(owners)
	return obj
}

func nodeByID(nodes []Node, id string) *Node {
	for i := range nodes {
		if nodes[i].ID == id {
			return &nodes[i]
		}
	}
	return nil
}

func TestGenericCRDIdentitySeparatesCoreAndCrossGroupJobs(t *testing.T) {
	volcano := schema.GroupVersionResource{Group: "batch.volcano.sh", Version: "v1alpha1", Resource: "jobs"}
	other := schema.GroupVersionResource{Group: "jobs.example.io", Version: "v1", Resource: "jobs"}
	owner := metav1.OwnerReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "trainer"}
	dynamic := &genericIdentityDynamic{
		watched: []schema.GroupVersionResource{other, volcano},
		kinds:   map[schema.GroupVersionResource]string{volcano: "Job", other: "Job"},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{
			volcano: {genericIdentityObject(volcano, "Job", "ml", "train", owner)},
			other:   {genericIdentityObject(other, "Job", "ml", "train", owner)},
		},
		listCalls: map[schema.GroupVersionResource]int{},
	}
	nodes := []Node{
		{ID: "deployment/ml/trainer", Kind: KindDeployment, Name: "trainer", Data: map[string]any{"namespace": "ml"}},
		{ID: "job/ml/train", Kind: KindJob, Name: "train", Data: map[string]any{"namespace": "ml"}},
	}

	nodes, edges := (&Builder{dynamic: dynamic}).addGenericCRDNodes(nodes, nil, DefaultBuildOptions())

	for _, id := range []string{"job/ml/train", "job/ml/train/batch.volcano.sh", "job/ml/train/jobs.example.io"} {
		if nodeByID(nodes, id) == nil {
			t.Fatalf("missing distinct Job node %q: %+v", id, nodes)
		}
	}
	for _, target := range []string{"job/ml/train/batch.volcano.sh", "job/ml/train/jobs.example.io"} {
		found := false
		for _, edge := range edges {
			found = found || edge.Source == "deployment/ml/trainer" && edge.Target == target && edge.Type == EdgeManages
		}
		if !found {
			t.Errorf("missing exact owner edge to %s: %+v", target, edges)
		}
	}
}

func TestGenericCRDOwnerResolutionUsesOwnerAPIGroup(t *testing.T) {
	groupA := schema.GroupVersionResource{Group: "a.example.io", Version: "v1", Resource: "widgets"}
	groupB := schema.GroupVersionResource{Group: "b.example.io", Version: "v1", Resource: "widgets"}
	childGVR := schema.GroupVersionResource{Group: "children.example.io", Version: "v1", Resource: "children"}
	deploymentOwner := metav1.OwnerReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "operator"}
	childOwner := metav1.OwnerReference{APIVersion: "b.example.io/v1", Kind: "Widget", Name: "shared"}
	dynamic := &genericIdentityDynamic{
		watched: []schema.GroupVersionResource{childGVR, groupB, groupA},
		kinds: map[schema.GroupVersionResource]string{
			groupA: "Widget", groupB: "Widget", childGVR: "Child",
		},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{
			groupA:   {genericIdentityObject(groupA, "Widget", "demo", "shared", deploymentOwner)},
			groupB:   {genericIdentityObject(groupB, "Widget", "demo", "shared", deploymentOwner)},
			childGVR: {genericIdentityObject(childGVR, "Child", "demo", "leaf", childOwner)},
		},
		listCalls: map[schema.GroupVersionResource]int{},
	}
	nodes := []Node{{ID: "deployment/demo/operator", Kind: KindDeployment, Name: "operator", Data: map[string]any{"namespace": "demo"}}}

	nodes, edges := (&Builder{dynamic: dynamic}).addGenericCRDNodes(nodes, nil, DefaultBuildOptions())
	childID := "child/demo/leaf/children.example.io"
	if nodeByID(nodes, childID) == nil {
		t.Fatalf("missing child node: %+v", nodes)
	}
	var ownerSources []string
	for _, edge := range edges {
		if edge.Target == childID {
			ownerSources = append(ownerSources, edge.Source)
		}
	}
	if len(ownerSources) != 1 || ownerSources[0] != "widget/demo/shared/b.example.io" {
		t.Fatalf("child owners = %v, want only b.example.io Widget", ownerSources)
	}
}

func TestGenericCRDSelectsOneStableVersionPerGroupKind(t *testing.T) {
	alpha := schema.GroupVersionResource{Group: "batch.volcano.sh", Version: "v1alpha1", Resource: "jobs"}
	stable := schema.GroupVersionResource{Group: "batch.volcano.sh", Version: "v1", Resource: "jobs"}
	owner := metav1.OwnerReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "trainer"}
	for _, watched := range [][]schema.GroupVersionResource{{alpha, stable}, {stable, alpha}} {
		dynamic := &genericIdentityDynamic{
			watched: watched,
			kinds:   map[schema.GroupVersionResource]string{alpha: "Job", stable: "Job"},
			resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{
				alpha:  {genericIdentityObject(alpha, "Job", "ml", "alpha-only", owner)},
				stable: {genericIdentityObject(stable, "Job", "ml", "stable", owner)},
			},
			listCalls: map[schema.GroupVersionResource]int{},
		}
		nodes := []Node{{ID: "deployment/ml/trainer", Kind: KindDeployment, Name: "trainer", Data: map[string]any{"namespace": "ml"}}}

		nodes, _ = (&Builder{dynamic: dynamic}).addGenericCRDNodes(nodes, nil, DefaultBuildOptions())
		if nodeByID(nodes, "job/ml/stable/batch.volcano.sh") == nil || nodeByID(nodes, "job/ml/alpha-only/batch.volcano.sh") != nil {
			t.Fatalf("stable-version selection produced the wrong nodes for order %v: %+v", watched, nodes)
		}
		if dynamic.listCalls[stable] != 1 || dynamic.listCalls[alpha] != 0 {
			t.Fatalf("list calls for order %v = stable:%d alpha:%d, want 1/0", watched, dynamic.listCalls[stable], dynamic.listCalls[alpha])
		}
	}
}

func TestGenericCRDReusesSpecializedResourceIdentityAndPseudoOwner(t *testing.T) {
	bucketGVR := schema.GroupVersionResource{Group: "s3.aws.upbound.io", Version: "v1beta1", Resource: "buckets"}
	childGVR := schema.GroupVersionResource{Group: "infra.example.io", Version: "v1", Resource: "children"}
	childOwner := metav1.OwnerReference{APIVersion: "karpenter.k8s.aws/v1", Kind: "EC2NodeClass", Name: "shared"}
	dynamic := &genericIdentityDynamic{
		watched: []schema.GroupVersionResource{bucketGVR, childGVR},
		kinds:   map[schema.GroupVersionResource]string{bucketGVR: "Bucket", childGVR: "Child"},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{
			bucketGVR: {genericIdentityObject(bucketGVR, "Bucket", "infra", "logs", metav1.OwnerReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "operator"})},
			childGVR:  {genericIdentityObject(childGVR, "Child", "infra", "leaf", childOwner)},
		},
		listCalls: map[schema.GroupVersionResource]int{},
	}
	nodeClassID := "nodeclass//shared/karpenter.k8s.aws/EC2NodeClass"
	nodes := []Node{
		{ID: "deployment/infra/operator", Kind: KindDeployment, Name: "operator", Data: map[string]any{"namespace": "infra"}},
		{ID: "crossplane/infra/logs", Kind: NodeKind("Bucket"), Name: "logs", Data: map[string]any{"namespace": "infra", "apiVersion": "s3.aws.upbound.io/v1beta1"}},
		{ID: nodeClassID, Kind: KindNodeClass, Name: "shared", Data: map[string]any{"apiVersion": "karpenter.k8s.aws/v1", "resourceKind": "EC2NodeClass"}},
	}

	nodes, edges := (&Builder{dynamic: dynamic}).addGenericCRDNodes(nodes, nil, DefaultBuildOptions())
	buckets := 0
	for _, node := range nodes {
		if KubernetesKindForNode(&node) == "Bucket" && node.Name == "logs" {
			buckets++
		}
	}
	if buckets != 1 || nodeByID(nodes, "bucket/infra/logs/s3.aws.upbound.io") != nil {
		t.Fatalf("specialized Bucket was duplicated: %+v", nodes)
	}
	childID := "child/infra/leaf/infra.example.io"
	found := false
	for _, edge := range edges {
		found = found || edge.Source == nodeClassID && edge.Target == childID
	}
	if !found {
		t.Fatalf("generic child did not resolve its pseudo-kind owner: %+v", edges)
	}
}

func TestGenericCRDExclusionsAreQualifiedWhereKindsCollide(t *testing.T) {
	tests := []struct {
		name     string
		gvr      schema.GroupVersionResource
		kind     string
		excluded bool
	}{
		{name: "knative service stays specialized", gvr: schema.GroupVersionResource{Group: "serving.knative.dev"}, kind: "Service", excluded: true},
		{name: "foreign service survives builtin collision", gvr: schema.GroupVersionResource{Group: "platform.example.io"}, kind: "Service", excluded: false},
		{name: "argo analysis run stays specialized", gvr: schema.GroupVersionResource{Group: "argoproj.io"}, kind: "AnalysisRun", excluded: true},
		{name: "foreign analysis run survives", gvr: schema.GroupVersionResource{Group: "analysis.example.io"}, kind: "AnalysisRun", excluded: false},
		{name: "trivy report stays excluded", gvr: schema.GroupVersionResource{Group: "aquasecurity.github.io"}, kind: "VulnerabilityReport", excluded: true},
		{name: "foreign report survives", gvr: schema.GroupVersionResource{Group: "security.example.io"}, kind: "VulnerabilityReport", excluded: false},
		{name: "foreign rollout survives", gvr: schema.GroupVersionResource{Group: "rollouts.example.io"}, kind: "Rollout", excluded: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := genericCRDExcluded(tt.gvr, tt.kind); got != tt.excluded {
				t.Fatalf("genericCRDExcluded(%s, %s) = %v, want %v", tt.gvr.Group, tt.kind, got, tt.excluded)
			}
		})
	}
}

func TestGenericCRDAddsForeignKindUsedByDedicatedIntegration(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "rollouts.example.io", Version: "v1", Resource: "rollouts"}
	owner := metav1.OwnerReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "operator"}
	dynamic := &genericIdentityDynamic{
		watched: []schema.GroupVersionResource{gvr},
		kinds:   map[schema.GroupVersionResource]string{gvr: "Rollout"},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{
			gvr: {genericIdentityObject(gvr, "Rollout", "demo", "release", owner)},
		},
		listCalls: map[schema.GroupVersionResource]int{},
	}
	provider := &mockProvider{deployments: []*appsv1.Deployment{{ObjectMeta: metav1.ObjectMeta{Name: "operator", Namespace: "demo"}}}}
	topo, err := NewBuilder(provider).WithDynamic(dynamic).Build(DefaultBuildOptions())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	id := "rollout/demo/release/rollouts.example.io"
	if nodeByID(topo.Nodes, id) == nil {
		t.Fatalf("foreign Rollout was suppressed by the Argo integration: %+v", topo.Nodes)
	}
	if nodeByID(topo.Nodes, "rollout/demo/release") != nil {
		t.Fatalf("foreign Rollout was also rendered as an Argo Rollout: %+v", topo.Nodes)
	}
	foundOwner := false
	for _, edge := range topo.Edges {
		foundOwner = foundOwner || edge.Source == "deployment/demo/operator" && edge.Target == id && edge.Type == EdgeManages
	}
	if !foundOwner {
		t.Fatalf("foreign Rollout owner edge missing: %+v", topo.Edges)
	}
}

func TestBuildDoesNotTreatIstioGatewayAsGatewayAPI(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "networking.istio.io", Version: "v1", Resource: "gateways"}
	dynamic := &genericIdentityDynamic{
		watched: []schema.GroupVersionResource{gvr},
		kinds:   map[schema.GroupVersionResource]string{gvr: "Gateway"},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{
			gvr: {genericIdentityObject(gvr, "Gateway", "demo", "mesh")},
		},
		listCalls: map[schema.GroupVersionResource]int{},
	}

	for _, viewMode := range []ViewMode{ViewModeResources, ViewModeTraffic} {
		t.Run(string(viewMode), func(t *testing.T) {
			opts := DefaultBuildOptions()
			opts.ViewMode = viewMode
			topo, err := NewBuilder(&mockProvider{}).WithDynamic(dynamic).Build(opts)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if nodeByID(topo.Nodes, "istiogateway/demo/mesh") == nil {
				t.Fatalf("Istio Gateway missing from %s topology: %+v", viewMode, topo.Nodes)
			}
			if nodeByID(topo.Nodes, "gateway/demo/mesh") != nil {
				t.Fatalf("Istio Gateway was also rendered as Gateway API in %s topology: %+v", viewMode, topo.Nodes)
			}
		})
	}
}

func TestBuildPrefersModernTraefikGroupWithoutDoubleRendering(t *testing.T) {
	modern := schema.GroupVersionResource{Group: "traefik.io", Version: "v1alpha1", Resource: "ingressroutes"}
	legacy := schema.GroupVersionResource{Group: "traefik.containo.us", Version: "v1alpha1", Resource: "ingressroutes"}

	for _, viewMode := range []ViewMode{ViewModeResources, ViewModeTraffic} {
		t.Run(string(viewMode), func(t *testing.T) {
			dynamic := &genericIdentityDynamic{
				watched: []schema.GroupVersionResource{legacy, modern},
				kinds:   map[schema.GroupVersionResource]string{modern: "IngressRoute", legacy: "IngressRoute"},
				resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{
					modern: {genericIdentityObject(modern, "IngressRoute", "demo", "route")},
					legacy: {genericIdentityObject(legacy, "IngressRoute", "demo", "route")},
				},
				listCalls: map[schema.GroupVersionResource]int{},
			}
			opts := DefaultBuildOptions()
			opts.ViewMode = viewMode
			topo, err := NewBuilder(&mockProvider{}).WithDynamic(dynamic).Build(opts)
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			selected, ok := preferredTraefikGVR(dynamic, "IngressRoute")
			if !ok || selected != modern {
				t.Fatalf("preferred Traefik GVR = %s (%v), want %s", selected, ok, modern)
			}
			var matches []Node
			for _, node := range topo.Nodes {
				if node.ID == "ingressroute/demo/route" {
					matches = append(matches, node)
				}
			}
			if len(matches) != 1 || matches[0].Data["apiVersion"] != "traefik.io/v1alpha1" {
				t.Fatalf("Traefik nodes = %+v, want one node from the modern API group", matches)
			}
		})
	}

	legacyOnly := &genericIdentityDynamic{
		watched: []schema.GroupVersionResource{legacy},
		kinds:   map[schema.GroupVersionResource]string{legacy: "IngressRoute"},
	}
	selected, ok := preferredTraefikGVR(legacyOnly, "IngressRoute")
	if !ok || selected != legacy {
		t.Fatalf("legacy-only Traefik GVR = %s (%v), want %s", selected, ok, legacy)
	}
}

func TestGenericCRDSkipsBuiltInGroupsBeforeProviderFallback(t *testing.T) {
	tests := []struct {
		name string
		gvr  schema.GroupVersionResource
		kind string
	}{
		{name: "typed apps API", gvr: schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, kind: "Deployment"},
		{name: "dynamically watched DRA API", gvr: schema.GroupVersionResource{Group: "resource.k8s.io", Version: "v1", Resource: "resourceclaims"}, kind: "ResourceClaim"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dynamic := &genericIdentityDynamic{
				watched: []schema.GroupVersionResource{tt.gvr},
				kinds:   map[schema.GroupVersionResource]string{tt.gvr: tt.kind},
				resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{
					tt.gvr: {genericIdentityObject(tt.gvr, tt.kind, "demo", "duplicate", metav1.OwnerReference{APIVersion: "apps/v1", Kind: "Deployment", Name: "operator"})},
				},
				listCalls: map[schema.GroupVersionResource]int{},
			}
			nodes := []Node{{ID: "deployment/demo/operator", Kind: KindDeployment, Name: "operator", Data: map[string]any{"namespace": "demo"}}}
			fallbackProvider := &dynamicProviderWithoutExactCRD{DynamicProvider: dynamic}

			nodes, _ = (&Builder{dynamic: fallbackProvider}).addGenericCRDNodes(nodes, nil, DefaultBuildOptions())
			if dynamic.listCalls[tt.gvr] != 0 {
				t.Fatalf("generic CRD fallback listed built-in %s %d times", tt.gvr, dynamic.listCalls[tt.gvr])
			}
			if len(nodes) != 1 {
				t.Fatalf("built-in %s was rendered as a generic CRD: %+v", tt.kind, nodes)
			}
		})
	}
}

func TestGenericCRDOwnerResolutionUsesIndexedCalicoAlias(t *testing.T) {
	childGVR := schema.GroupVersionResource{Group: "children.example.io", Version: "v1", Resource: "children"}
	dynamic := &genericIdentityDynamic{
		watched: []schema.GroupVersionResource{childGVR},
		kinds:   map[schema.GroupVersionResource]string{childGVR: "Child"},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{
			childGVR: {genericIdentityObject(childGVR, "Child", "demo", "leaf", metav1.OwnerReference{
				APIVersion: "crd.projectcalico.org/v1", Kind: "NetworkPolicy", Name: "allow",
			})},
		},
		listCalls: map[schema.GroupVersionResource]int{},
	}
	calicoID := "caliconetworkpolicy/demo/allow"
	nodes := []Node{{
		ID: calicoID, Kind: KindCalicoNetworkPolicy, Name: "allow",
		Data: map[string]any{
			"namespace":   "demo",
			"apiVersion":  "projectcalico.org/v3",
			"apiVersions": []string{"projectcalico.org/v3", "crd.projectcalico.org/v1"},
		},
	}}

	nodes, edges := (&Builder{dynamic: dynamic}).addGenericCRDNodes(nodes, nil, DefaultBuildOptions())
	childID := "child/demo/leaf/children.example.io"
	if nodeByID(nodes, childID) == nil {
		t.Fatalf("generic child missing: %+v", nodes)
	}
	for _, edge := range edges {
		if edge.Source == calicoID && edge.Target == childID && edge.Type == EdgeManages {
			return
		}
	}
	t.Fatalf("generic child did not resolve the Calico CRD-group alias: %+v", edges)
}

func TestRelationshipsWithObjectUsesExactGroupWhenDirectIDCollides(t *testing.T) {
	volcanoGVR := schema.GroupVersionResource{Group: "batch.volcano.sh", Version: "v1alpha1", Resource: "jobs"}
	jobSetGVR := schema.GroupVersionResource{Group: "jobset.x-k8s.io", Version: "v1alpha2", Resource: "jobsets"}
	dynamic := &genericIdentityDynamic{
		watched:   []schema.GroupVersionResource{volcanoGVR, jobSetGVR},
		kinds:     map[schema.GroupVersionResource]string{volcanoGVR: "Job", jobSetGVR: "JobSet"},
		resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{},
		listCalls: map[schema.GroupVersionResource]int{},
	}
	volcano := genericIdentityObject(volcanoGVR, "Job", "ml", "train")
	topo := &Topology{
		Nodes: []Node{
			{ID: "cronjob/ml/train", Kind: KindCronJob, Name: "train", Data: map[string]any{"namespace": "ml"}},
			{ID: "jobset/ml/train/jobset.x-k8s.io", Kind: NodeKind("JobSet"), Name: "train", Data: map[string]any{"namespace": "ml", "apiVersion": "jobset.x-k8s.io/v1alpha2"}},
			{ID: "job/ml/train", Kind: KindJob, Name: "train", Data: map[string]any{"namespace": "ml"}},
			{ID: "job/ml/train/batch.volcano.sh", Kind: NodeKind("Job"), Name: "train", Data: map[string]any{"namespace": "ml", "apiVersion": "batch.volcano.sh/v1alpha1"}},
		},
		Edges: []Edge{
			{ID: "cron-core", Source: "cronjob/ml/train", Target: "job/ml/train", Type: EdgeManages},
			{ID: "jobset-volcano", Source: "jobset/ml/train/jobset.x-k8s.io", Target: "job/ml/train/batch.volcano.sh", Type: EdgeManages},
		},
	}

	rel := GetRelationshipsWithObject("Job", "ml", "train", volcano, topo, nil, dynamic, IndexByResource(topo))
	if rel == nil || rel.Owner == nil {
		t.Fatalf("exact-group relationships missing: %+v", rel)
	}
	if *rel.Owner != (ResourceRef{Group: "jobset.x-k8s.io", Kind: "JobSet", Namespace: "ml", Name: "train"}) {
		t.Fatalf("owner = %+v, want JobSet rather than core CronJob", rel.Owner)
	}
	if len(rel.ManagedBy) != 1 || rel.ManagedBy[0] != *rel.Owner {
		t.Fatalf("managed-by = %+v, want exact JobSet owner %+v", rel.ManagedBy, rel.Owner)
	}
}
