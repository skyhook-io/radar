package k8score

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
)

func TestDetectNodePlatform(t *testing.T) {
	tests := []struct {
		name string
		node corev1.Node
		want string
	}{
		{name: "RKE2 kubelet", node: nodeWithKubeletVersion("v1.31.7+rke2r1"), want: "rke2"},
		{name: "RKE2 before Rancher", node: nodeWithSignals("v1.31.7+rke2r1", "", map[string]string{"rke.cattle.io/machine": "worker"}), want: "rke2"},
		{name: "RKE2 before provider ID", node: nodeWithSignals("v1.31.7+rke2r1", "aws:///zone/node", nil), want: "rke2"},
		{name: "not an RKE2 suffix", node: nodeWithKubeletVersion("v1.31.7-rke2r1"), want: "unknown"},
		{name: "GCE provider", node: nodeWithProviderID("gce://project/zone/node"), want: "gke"},
		{name: "GKE provider", node: nodeWithProviderID("gke://project/zone/node"), want: "gke"},
		{name: "AWS provider", node: nodeWithProviderID("aws:///zone/node"), want: "eks"},
		{name: "Azure provider", node: nodeWithProviderID("azure:///subscriptions/node"), want: "aks"},
		{name: "kind provider", node: nodeWithProviderID("kind://docker/radar/control-plane"), want: "kind"},
		{name: "GKE label", node: nodeWithLabels(map[string]string{"cloud.google.com/gke-nodepool": "default"}), want: "gke"},
		{name: "EKS nodegroup label", node: nodeWithLabels(map[string]string{"eks.amazonaws.com/nodegroup": "workers"}), want: "eks"},
		{name: "EKS capacity label", node: nodeWithLabels(map[string]string{"eks.amazonaws.com/capacityType": "ON_DEMAND"}), want: "eks"},
		{name: "Azure label", node: nodeWithLabels(map[string]string{"kubernetes.azure.com/cluster": "prod"}), want: "aks"},
		{name: "OpenShift label", node: nodeWithLabels(map[string]string{"node.openshift.io/os_id": "rhcos"}), want: "openshift"},
		{name: "Rancher label", node: nodeWithLabels(map[string]string{"rke.cattle.io/machine": "worker"}), want: "rancher"},
		{name: "kind node name", node: corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "kind-control-plane"}}, want: "kind"},
		{name: "minikube node name", node: corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "minikube-m02"}}, want: "minikube"},
		{name: "Docker Desktop node name", node: corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "docker-desktop"}}, want: "docker-desktop"},
		{name: "unknown", node: corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}}, want: "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DetectNodePlatform(tt.node); got != tt.want {
				t.Fatalf("DetectNodePlatform() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDetectPlatformFromVersion(t *testing.T) {
	tests := []struct {
		version string
		want    string
	}{
		{version: "v1.31.7+rke2r1", want: "rke2"},
		{version: "v1.31.7-rke2r1", want: "unknown"},
		{version: "v1.31.7", want: "unknown"},
	}

	for _, tt := range tests {
		if got := DetectPlatformFromVersion(tt.version); got != tt.want {
			t.Errorf("DetectPlatformFromVersion(%q) = %q, want %q", tt.version, got, tt.want)
		}
	}
}

func TestDetectClusterPlatform(t *testing.T) {
	tests := []struct {
		name   string
		client kubernetes.Interface
		want   string
	}{
		{name: "nil client", client: nil, want: "unknown"},
		{name: "no nodes", client: fake.NewSimpleClientset(), want: "unknown"},
		{name: "generic node", client: fake.NewSimpleClientset(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-1"}}), want: "generic"},
		{name: "RKE2 node", client: fake.NewSimpleClientset(nodePtr(nodeWithKubeletVersion("v1.31.7+rke2r1"))), want: "rke2"},
		{name: "GKE Autopilot", client: fake.NewSimpleClientset(nodePtr(nodeWithSignals("", "gce://project/zone/node", map[string]string{"cloud.google.com/gke-autopilot": "true"}))), want: "gke-autopilot"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DetectClusterPlatform(context.Background(), tt.client)
			if err != nil {
				t.Fatalf("DetectClusterPlatform() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("DetectClusterPlatform() = %q, want %q", got, tt.want)
			}
		})
	}
}

func nodeWithKubeletVersion(version string) corev1.Node {
	return nodeWithSignals(version, "", nil)
}

func nodeWithProviderID(providerID string) corev1.Node {
	return nodeWithSignals("", providerID, nil)
}

func nodeWithLabels(labels map[string]string) corev1.Node {
	return nodeWithSignals("", "", labels)
}

func nodeWithSignals(version, providerID string, labels map[string]string) corev1.Node {
	return corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-1", Labels: labels},
		Spec:       corev1.NodeSpec{ProviderID: providerID},
		Status:     corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: version}},
	}
}

func nodePtr(node corev1.Node) *corev1.Node {
	return &node
}
