package k8s

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestGetClusterPlatform(t *testing.T) {
	tests := []struct {
		name          string
		nodes         corev1.NodeList
		pods          corev1.PodList
		denyNodes     bool
		serverVersion string
		want          string
	}{
		{
			name: "RKE2",
			nodes: corev1.NodeList{Items: []corev1.Node{{
				Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.31.7+rke2r1"}},
			}}},
			want: "rke2",
		},
		{
			name: "GKE Autopilot node label",
			nodes: corev1.NodeList{Items: []corev1.Node{{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"cloud.google.com/gke-autopilot": "true"}},
				Spec:       corev1.NodeSpec{ProviderID: "gce://project/zone/node"},
			}}},
			want: "gke-autopilot",
		},
		{
			name:          "RKE2 server version fallback",
			denyNodes:     true,
			serverVersion: "v1.31.7+rke2r1",
			want:          "rke2",
		},
		{
			name:          "RKE2 server version fallback without nodes",
			serverVersion: "v1.31.7+rke2r1",
			want:          "rke2",
		},
		{
			name:      "GKE Autopilot annotation fallback",
			denyNodes: true,
			pods: corev1.PodList{Items: []corev1.Pod{{
				ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{"autopilot.gke.io/resource-adjustment": "true"}},
			}}},
			want: "gke-autopilot",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setClusterDetectionClient(t, tt.nodes, tt.pods, tt.denyNodes, tt.serverVersion)

			got, err := GetClusterPlatform(context.Background())
			if err != nil {
				t.Fatalf("GetClusterPlatform() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("GetClusterPlatform() = %q, want %q", got, tt.want)
			}
		})
	}
}

func setClusterDetectionClient(t *testing.T, nodes corev1.NodeList, pods corev1.PodList, denyNodes bool, serverVersion string) {
	t.Helper()

	nodes.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "NodeList"}
	pods.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/version":
			_ = json.NewEncoder(w).Encode(version.Info{GitVersion: serverVersion})
		case "/api/v1/nodes":
			if denyNodes {
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(metav1.Status{
					TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"},
					Status:   metav1.StatusFailure,
					Reason:   metav1.StatusReasonForbidden,
					Code:     http.StatusForbidden,
				})
				return
			}
			_ = json.NewEncoder(w).Encode(nodes)
		case "/api/v1/namespaces/kube-system/pods":
			_ = json.NewEncoder(w).Encode(pods)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)

	client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatalf("create Kubernetes client: %v", err)
	}
	ResetResourceCache()
	InvalidateServerVersionCache()
	previous := SetTestClient(client)
	t.Cleanup(func() {
		SetTestClient(previous)
		ResetResourceCache()
		InvalidateServerVersionCache()
	})
}
