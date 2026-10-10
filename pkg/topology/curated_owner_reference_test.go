package topology

import (
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"testing"
)

func TestCuratedOwnerReferencesRequireExactAPIGroup(t *testing.T) {
	for _, tc := range []struct {
		childKind, childGroup, childPlural, childID, ownerKind, ownerGroup, ownerPlural, ownerID string
		reverse                                                                                  bool
	}{
		{"KubeadmControlPlane", "controlplane.cluster.x-k8s.io", "kubeadmcontrolplanes", "kubeadmcontrolplane", "Cluster", "cluster.x-k8s.io", "clusters", "capicluster", false},
		{"MachineDeployment", "cluster.x-k8s.io", "machinedeployments", "machinedeployment", "Cluster", "cluster.x-k8s.io", "clusters", "capicluster", false},
		{"MachinePool", "cluster.x-k8s.io", "machinepools", "machinepool", "Cluster", "cluster.x-k8s.io", "clusters", "capicluster", false},
		{"MachineHealthCheck", "cluster.x-k8s.io", "machinehealthchecks", "machinehealthcheck", "Cluster", "cluster.x-k8s.io", "clusters", "capicluster", true},
		{"MachineSet", "cluster.x-k8s.io", "machinesets", "machineset", "MachineDeployment", "cluster.x-k8s.io", "machinedeployments", "machinedeployment", false},
		{"Machine", "cluster.x-k8s.io", "machines", "machine", "MachineSet", "cluster.x-k8s.io", "machinesets", "machineset", false},
		{"Machine", "cluster.x-k8s.io", "machines", "machine", "KubeadmControlPlane", "controlplane.cluster.x-k8s.io", "kubeadmcontrolplanes", "kubeadmcontrolplane", false},
		{"Machine", "cluster.x-k8s.io", "machines", "machine", "MachinePool", "cluster.x-k8s.io", "machinepools", "machinepool", false},
		{"Configuration", "serving.knative.dev", "configurations", "knativeconfiguration", "Service", "serving.knative.dev", "services", "knativeservice", false},
		{"Route", "serving.knative.dev", "routes", "knativeroute", "Service", "serving.knative.dev", "services", "knativeservice", false},
		{"Revision", "serving.knative.dev", "revisions", "knativerevision", "Configuration", "serving.knative.dev", "configurations", "knativeconfiguration", false},
		{"Deployment", "apps", "deployments", "deployment", "Revision", "serving.knative.dev", "revisions", "knativerevision", false},
		{"Workflow", "argoproj.io", "workflows", "workflow", "CronWorkflow", "argoproj.io", "cronworkflows", "cronworkflow", false},
	} {
		for _, ownerAPI := range []string{tc.ownerGroup + "/v1beta1", "unrelated.example/v1", "prefix." + tc.ownerGroup + "/v1", "postgresql.cnpg.io/v1", ""} {
			t.Run(tc.childKind+"/"+tc.ownerKind+"/"+ownerAPI, func(t *testing.T) {
				childGVR := schema.GroupVersionResource{Group: tc.childGroup, Version: "v1beta1", Resource: tc.childPlural}
				ownerGVR := schema.GroupVersionResource{Group: tc.ownerGroup, Version: "v1beta1", Resource: tc.ownerPlural}
				controller := true
				ref := metav1.OwnerReference{APIVersion: ownerAPI, Kind: tc.ownerKind, Name: "parent", Controller: &controller}
				child := genericIdentityObject(childGVR, tc.childKind, "app", "child", ref)
				owner := genericIdentityObject(ownerGVR, tc.ownerKind, "app", "parent")
				d := &genericIdentityDynamic{watched: []schema.GroupVersionResource{childGVR, ownerGVR}, kinds: map[schema.GroupVersionResource]string{childGVR: tc.childKind, ownerGVR: tc.ownerKind}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{childGVR: {child}, ownerGVR: {owner}}, listCalls: map[schema.GroupVersionResource]int{}}
				provider := &mockProvider{}
				if tc.childKind == "Deployment" {
					provider.deployments = []*appsv1.Deployment{{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "child", OwnerReferences: []metav1.OwnerReference{ref}}}}
					d.resources[childGVR] = nil
				}
				topo, err := NewBuilder(provider).WithDynamic(d).Build(DefaultBuildOptions())
				if err != nil {
					t.Fatal(err)
				}
				source, target := tc.ownerID+"/app/parent", tc.childID+"/app/child"
				typ := EdgeManages
				if tc.reverse {
					source, target = target, source
					typ = EdgeProtects
				}
				if nodeByID(topo.Nodes, source) == nil || nodeByID(topo.Nodes, target) == nil {
					t.Fatalf("missing fixture nodes %s %s", source, target)
				}
				found := false
				for _, e := range topo.Edges {
					found = found || e.Source == source && e.Target == target && e.Type == typ
				}
				if found != (ownerAPI == tc.ownerGroup+"/v1beta1") {
					t.Errorf("owner apiVersion %q joined=%v", ownerAPI, found)
				}
			})
		}
	}
}
