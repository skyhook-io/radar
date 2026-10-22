package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/topology"
)

// Each test below seeds a topology memo through one call site while the
// cache holds the cluster the user is leaving, swaps the resource cache the
// way a context switch or namespace rescope does, then asks the same call
// site again. A call site that ignores the cache generation is handed the
// previous cluster's graph.

// clusterWithDeployment is a one-Deployment cluster (plus the ReplicaSet it
// owns), so each side of a switch has workloads that exist only there.
func clusterWithDeployment(name string) *fake.Clientset {
	replicas := int32(1)
	selector := &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}}
	deployUID := types.UID("uid-" + name)
	return fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "demo"}},
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "demo", UID: deployUID},
			Spec:       appsv1.DeploymentSpec{Replicas: &replicas, Selector: selector},
		},
		&appsv1.ReplicaSet{
			ObjectMeta: metav1.ObjectMeta{
				Name: name + "-rs", Namespace: "demo", UID: types.UID("uid-" + name + "-rs"),
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: name, UID: deployUID}},
			},
			Spec:   appsv1.ReplicaSetSpec{Replicas: &replicas, Selector: selector},
			Status: appsv1.ReplicaSetStatus{Replicas: 1, ReadyReplicas: 1},
		},
	)
}

func topologyHasNode(nodes []topology.Node, name string) bool {
	for _, n := range nodes {
		if n.Name == name {
			return true
		}
	}
	return false
}

func newMemoTestServer(t *testing.T) *Server {
	t.Helper()
	s := newTestServer(t)
	s.topoMemo = topology.NewMemoizer(5 * time.Second)
	return s
}

func TestTopologyMemo_AIContextAfterSwitch(t *testing.T) {
	useTestResourceCache(t, clusterWithDeployment("old-cluster-web"))
	s := newMemoTestServer(t)

	before, _, _, _, ok := s.topologyForContext()
	if !ok || !topologyHasNode(before.Nodes, "old-cluster-web") {
		t.Fatalf("seed: expected old-cluster-web in the topology, got ok=%v", ok)
	}

	useTestResourceCache(t, clusterWithDeployment("new-cluster-api"))
	s.finalizePostContextSwitch()

	after, _, _, _, ok := s.topologyForContext()
	if !ok {
		t.Fatal("post-switch topology unavailable")
	}
	if topologyHasNode(after.Nodes, "old-cluster-web") || !topologyHasNode(after.Nodes, "new-cluster-api") {
		t.Errorf("post-switch topology still describes the previous cluster: %+v", after.Nodes)
	}
}

// The GitOps handlers pick up the cache while parsing the request and run
// permission checks before building, so a request can hold the previous
// cache and reach the memo after the switch without unusual timing. Its
// scope must come from the cache it builds from, or the previous cluster's
// graph lands where requests on the new cluster read.
func TestTopologyMemo_GitOpsTreeAfterSwitch(t *testing.T) {
	useTestResourceCache(t, clusterWithDeployment("old-cluster-web"))
	s := newMemoTestServer(t)
	staleReq := &gitopsRequest{Kind: "Deployment", Namespace: "demo", Name: "old-cluster-web", Cache: k8s.GetResourceCache()}
	// The trees themselves fail (a Deployment is not a GitOps root); only
	// the topology they build matters here.
	_, _, _ = s.buildGitOpsTree(httptest.NewRequest(http.MethodGet, "/", nil), staleReq)

	useTestResourceCache(t, clusterWithDeployment("new-cluster-api"))
	s.finalizePostContextSwitch()
	_, _, _ = s.buildGitOpsTree(httptest.NewRequest(http.MethodGet, "/", nil), staleReq)

	freshReq := &gitopsRequest{Kind: "Deployment", Namespace: "demo", Name: "new-cluster-api", Cache: k8s.GetResourceCache()}
	_, _, _ = s.buildGitOpsTree(httptest.NewRequest(http.MethodGet, "/", nil), freshReq)

	// The tree does not expose its topology, so read back what the fresh
	// request stored under its own cache.
	opts := topology.DefaultBuildOptions()
	opts.IncludeReplicaSets = true
	opts.ForRelationshipCache = true
	used, err := s.topoMemo.GetScoped(freshReq.Cache.Generation(), opts, func() (*topology.Topology, error) {
		t.Error("the post-switch GitOps request did not build from its own cache")
		return &topology.Topology{}, nil
	})
	if err != nil {
		t.Fatalf("memo Get: %v", err)
	}
	if topologyHasNode(used.Nodes, "old-cluster-web") || !topologyHasNode(used.Nodes, "new-cluster-api") {
		t.Errorf("post-switch GitOps request used the previous cluster's graph: %+v", used.Nodes)
	}
}

func neighborhoodRequest(t *testing.T, s *Server, name string) (int, neighborhoodResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/ai/neighborhood/Deployment/demo/"+name, nil)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("kind", "Deployment")
	rctx.URLParams.Add("namespace", "demo")
	rctx.URLParams.Add("name", name)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	s.handleAINeighborhood(rec, req)
	var resp neighborhoodResponse
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode neighborhood response: %v", err)
		}
	}
	return rec.Code, resp
}

// The handler reads the topology and its relationships index separately;
// both must come from the new cluster, or the root resolves but its
// neighbors are looked up in the previous cluster's index.
func TestTopologyMemo_NeighborhoodAfterSwitch(t *testing.T) {
	useTestResourceCache(t, clusterWithDeployment("old-cluster-web"))
	s := newMemoTestServer(t)
	if code, resp := neighborhoodRequest(t, s, "old-cluster-web"); code != http.StatusOK || !topologyHasNode(resp.Subgraph.Nodes, "old-cluster-web-rs") {
		t.Fatalf("seed: status %d, nodes %+v", code, resp.Subgraph.Nodes)
	}

	useTestResourceCache(t, clusterWithDeployment("new-cluster-api"))
	s.finalizePostContextSwitch()

	code, resp := neighborhoodRequest(t, s, "new-cluster-api")
	if code != http.StatusOK {
		t.Fatalf("post-switch neighborhood: status %d", code)
	}
	if !topologyHasNode(resp.Subgraph.Nodes, "new-cluster-api-rs") {
		t.Errorf("post-switch neighborhood is missing the new cluster's ReplicaSet: %+v", resp.Subgraph.Nodes)
	}
}
