package traffic

import (
	"context"
	"errors"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestDetectCNI(t *testing.T) {
	tests := []struct {
		name       string
		daemonSets []string
		want       string
	}{
		{name: "RKE2 Canal", daemonSets: []string{"rke2-canal"}, want: "canal"},
		{name: "plain Canal", daemonSets: []string{"canal"}, want: "canal"},
		{name: "Canal before Calico and Flannel", daemonSets: []string{"calico-node", "kube-flannel-ds", "rke2-canal"}, want: "canal"},
		{name: "Cilium before Canal", daemonSets: []string{"cilium", "rke2-canal"}, want: "cilium"},
		{name: "Calico remains Calico", daemonSets: []string{"calico-node"}, want: "calico"},
		{name: "Flannel remains Flannel", daemonSets: []string{"kube-flannel-ds"}, want: "flannel"},
		{name: "prefix does not match", daemonSets: []string{"rke2-canal-old"}, want: "unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objects := make([]runtime.Object, 0, len(tt.daemonSets))
			for _, name := range tt.daemonSets {
				objects = append(objects, &appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "kube-system"}})
			}
			m := &Manager{k8sClient: fake.NewSimpleClientset(objects...)}
			got, _ := m.detectCNI(context.Background(), "generic")
			if got != tt.want {
				t.Fatalf("detectCNI() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDetectClusterInfoFallsBackToRKE2ServerVersion(t *testing.T) {
	client := fake.NewSimpleClientset()
	client.Discovery().(*fakediscovery.FakeDiscovery).FakedServerVersion = &version.Info{GitVersion: "v1.31.7+rke2r1"}
	client.PrependReactor("list", "nodes", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "nodes"}, "", errors.New("denied"))
	})

	info, err := (&Manager{k8sClient: client}).detectClusterInfo(context.Background())
	if err != nil {
		t.Fatalf("detectClusterInfo() error = %v", err)
	}
	if info.Platform != "rke2" {
		t.Errorf("platform = %q, want rke2", info.Platform)
	}
}

func TestGenerateRecommendationForCanal(t *testing.T) {
	recommendation := (&Manager{}).generateRecommendation(&ClusterInfo{CNI: "canal"}, nil)
	if recommendation == nil {
		t.Fatal("generateRecommendation() = nil")
	}
	if recommendation.Name != "caretta" {
		t.Errorf("name = %q, want caretta", recommendation.Name)
	}
	if !strings.Contains(recommendation.Reason, "Canal") {
		t.Errorf("reason = %q, want Canal-specific guidance", recommendation.Reason)
	}
}

func TestDetectSources_ReportsRKE2CanalWithoutRecommendingOverAvailableCaretta(t *testing.T) {
	client := fake.NewSimpleClientset(
		&corev1.Node{Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.31.7+rke2r1"}}},
		&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Name: "rke2-canal", Namespace: "kube-system"}},
	)
	m := &Manager{
		k8sClient: client,
		sources: map[string]TrafficSource{
			"caretta": &stubSource{name: "caretta", result: &DetectionResult{Available: true, Present: true}},
		},
	}

	response, err := m.DetectSources(context.Background())
	if err != nil {
		t.Fatalf("DetectSources() error = %v", err)
	}
	if response.Cluster.Platform != "rke2" {
		t.Errorf("platform = %q, want rke2", response.Cluster.Platform)
	}
	if response.Cluster.CNI != "canal" {
		t.Errorf("CNI = %q, want canal", response.Cluster.CNI)
	}
	if response.Recommended != nil {
		t.Errorf("available Caretta must suppress recommendations, got %#v", response.Recommended)
	}
}
