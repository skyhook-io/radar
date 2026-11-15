package k8s

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/k8score"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func TestGetClusterPlatform(t *testing.T) {
	tests := []struct {
		name          string
		nodes         corev1.NodeList
		pods          corev1.PodList
		denyNodes     bool
		serverVersion string
		nkp           bool
		want          string
	}{
		{
			name: "EKS compute label from API",
			nodes: corev1.NodeList{Items: []corev1.Node{
				{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"eks.amazonaws.com/compute-type": "auto"}}},
			}},
			want: "eks",
		},
		{
			name: "NKP on AWS infrastructure",
			nodes: corev1.NodeList{Items: []corev1.Node{{
				Spec: corev1.NodeSpec{ProviderID: "aws:///us-east-1a/i-0123"},
			}}},
			serverVersion: "v1.31.7+rke2r1",
			nkp:           true,
			want:          "nkp",
		},
		{
			name: "EKS control plane with an upstream worker",
			nodes: corev1.NodeList{Items: []corev1.Node{{
				Spec:   corev1.NodeSpec{ProviderID: "aws:///us-east-1a/i-0123"},
				Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{KubeletVersion: "v1.32.5"}},
			}}},
			serverVersion: "v1.32.5-eks-5d4a308",
			want:          "eks",
		},
		{
			name: "RKE2 control plane overrides an EKS worker label",
			nodes: corev1.NodeList{Items: []corev1.Node{{
				ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"eks.amazonaws.com/nodegroup": "workers"}},
			}}},
			serverVersion: "v1.24.17+rke2r1",
			want:          "rke2",
		},
		{
			name:          "K3s control plane without node access",
			denyNodes:     true,
			serverVersion: "v1.35.0+k3s3",
			want:          "k3s",
		},
		{
			name:          "GKE control plane without node access",
			denyNodes:     true,
			serverVersion: "v1.30.12-gke.300",
			want:          "gke",
		},
		{
			name: "Unmarked control plane on plain AWS stays generic",
			nodes: corev1.NodeList{Items: []corev1.Node{{
				Spec: corev1.NodeSpec{ProviderID: "aws:///us-east-1a/i-0123"},
			}}},
			serverVersion: "v1.32.5",
			want:          "generic",
		},
		{
			name: "Nutanix node fallback when server version is unavailable",
			nodes: corev1.NodeList{Items: []corev1.Node{{
				Spec: corev1.NodeSpec{ProviderID: "nutanix://99d71c41-e100-4474-601b-5dd604becea6"},
			}}},
			want: "nutanix",
		},
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
			serverVersion: "v1.30.12-gke.300",
			want:          "gke-autopilot",
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
			setClusterDetectionClient(t, tt.nodes, tt.pods, tt.denyNodes, tt.serverVersion, tt.nkp)

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

func TestGetClusterPlatformFromMixedCachedNodes(t *testing.T) {
	setClusterDetectionClient(t, corev1.NodeList{}, corev1.PodList{}, true, "", false)
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client: fake.NewClientset(
			&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "custom"}, Spec: corev1.NodeSpec{ProviderID: "aws:///zone/custom"}},
			&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "eks", Labels: map[string]string{"eks.amazonaws.com/compute-type": "hybrid"}}},
		),
		ResourceTypes: map[string]bool{k8score.Nodes: true},
		DeferredTypes: map[string]bool{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Stop)
	previous := resourceCache.Swap(&ResourceCache{ResourceCache: core})
	t.Cleanup(func() { resourceCache.Store(previous) })
	for range 32 {
		if got, err := GetClusterPlatform(context.Background()); err != nil || got != "eks" {
			t.Fatalf("GetClusterPlatform() = %q, %v; want eks", got, err)
		}
	}
}

func setClusterDetectionClient(t *testing.T, nodes corev1.NodeList, pods corev1.PodList, denyNodes bool, serverVersion string, nkp bool) *atomic.Int32 {
	t.Helper()

	var versionReads atomic.Int32
	nodes.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "NodeList"}
	pods.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/version":
			versionReads.Add(1)
			if serverVersion == "" {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}
			_ = json.NewEncoder(w).Encode(version.Info{GitVersion: serverVersion})
		case "/api/v1/nodes":
			if r.URL.Query().Get("limit") != "1" {
				t.Errorf("node list limit = %q, want 1", r.URL.Query().Get("limit"))
			}
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
		case "/api/v1/namespaces/kommander-flux":
			if !nkp {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(corev1.Namespace{
				TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
				ObjectMeta: metav1.ObjectMeta{Name: "kommander-flux"},
			})
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
	InvalidatePlatformCache()
	previous := SetTestClient(client)
	t.Cleanup(func() {
		SetTestClient(previous)
		ResetResourceCache()
		InvalidateServerVersionCache()
		InvalidatePlatformCache()
	})
	return &versionReads
}

func TestClusterPlatformReusesServerVersion(t *testing.T) {
	reads := setClusterDetectionClient(t, corev1.NodeList{}, corev1.PodList{}, true, "v1.32.5-eks-5d4a308", false)
	for range 2 {
		info, err := GetClusterInfo(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if info.Platform != "eks" || info.KubernetesVersion != "v1.32.5-eks-5d4a308" {
			t.Fatalf("unexpected cluster info: platform=%q, version=%q", info.Platform, info.KubernetesVersion)
		}
	}
	if got := reads.Load(); got != 1 {
		t.Fatalf("server version requests = %d, want 1", got)
	}
}

func TestGetServerVersionRetriesUntilSuccess(t *testing.T) {
	var reads atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch reads.Add(1) {
		case 1:
			w.WriteHeader(http.StatusServiceUnavailable)
		case 2:
			_ = json.NewEncoder(w).Encode(version.Info{})
		default:
			_ = json.NewEncoder(w).Encode(version.Info{GitVersion: "v1.32.5-eks-5d4a308"})
		}
	}))
	t.Cleanup(server.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	InvalidateServerVersionCache()
	previous := SetTestClient(client)
	t.Cleanup(func() {
		SetTestClient(previous)
		InvalidateServerVersionCache()
	})
	for attempt, want := range []string{"", "", "v1.32.5-eks-5d4a308"} {
		for range 2 {
			if got := GetServerVersion(); got != want {
				t.Fatalf("GetServerVersion() = %q, want %q", got, want)
			}
		}
		if got := reads.Load(); got != int32(attempt+1) {
			t.Fatalf("server version requests = %d, want %d", got, attempt+1)
		}
		if want == "" {
			serverVersionMu.Lock()
			lastServerVersionFailure = time.Now().Add(-31 * time.Second)
			serverVersionMu.Unlock()
		}
	}
}

// setNKPLookupClient serves one Nutanix node and answers the NKP namespace
// read through namespaceGet, counting the reads.
func setNKPLookupClient(t *testing.T, namespaceGet func(w http.ResponseWriter, r *http.Request)) *atomic.Int32 {
	t.Helper()
	var reads atomic.Int32
	nodes := corev1.NodeList{
		TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "NodeList"},
		Items:    []corev1.Node{{Spec: corev1.NodeSpec{ProviderID: "nutanix://uuid"}}},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/nodes":
			_ = json.NewEncoder(w).Encode(nodes)
		case "/api/v1/namespaces/kommander-flux":
			reads.Add(1)
			namespaceGet(w, r)
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
	InvalidatePlatformCache()
	previous := SetTestClient(client)
	t.Cleanup(func() {
		SetTestClient(previous)
		ResetResourceCache()
		InvalidateServerVersionCache()
		InvalidatePlatformCache()
	})
	return &reads
}

func TestGetClusterPlatformAsksForNKPOncePerCluster(t *testing.T) {
	reads := setNKPLookupClient(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(corev1.Namespace{
			TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"},
			ObjectMeta: metav1.ObjectMeta{Name: "kommander-flux"},
		})
	})
	for range 3 {
		if got, _ := GetClusterPlatform(context.Background()); got != "nkp" {
			t.Fatalf("GetClusterPlatform() = %q, want nkp", got)
		}
	}
	if n := reads.Load(); n != 1 {
		t.Fatalf("namespace reads = %d, want 1", n)
	}

	InvalidatePlatformCache()
	if got, _ := GetClusterPlatform(context.Background()); got != "nkp" {
		t.Fatalf("after invalidation GetClusterPlatform() = %q, want nkp", got)
	}
	if n := reads.Load(); n != 2 {
		t.Fatalf("namespace reads after invalidation = %d, want 2", n)
	}
}

func TestGetClusterPlatformDoesNotWaitOnSlowNKPLookup(t *testing.T) {
	previousTimeout := nkpLookupTimeout
	nkpLookupTimeout = 50 * time.Millisecond
	t.Cleanup(func() { nkpLookupTimeout = previousTimeout })

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	reads := setNKPLookupClient(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})

	start := time.Now()
	got, _ := GetClusterPlatform(context.Background())
	if got != "nutanix" {
		t.Fatalf("GetClusterPlatform() = %q, want nutanix from the node", got)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("GetClusterPlatform() took %s, want it bounded by the lookup limit", elapsed)
	}
	_, _ = GetClusterPlatform(context.Background())
	if n := reads.Load(); n != 2 {
		t.Fatalf("namespace reads = %d, want 2: a timed-out lookup is not remembered", n)
	}
}
