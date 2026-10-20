package server

import (
	"encoding/json"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/topology"
	"net/http"
	"testing"
)

func TestCascadePreviewUsesUserTopologyAndRootReadGate(t *testing.T) {
	env := newAuthTestServer(t)
	old := k8s.GetConnectionStatus()
	t.Cleanup(func() { k8s.SetConnectionStatus(old) })
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	env.srv.permCache.Set("preview-reader", nil, &auth.UserPermissions{AllowedNamespaces: []string{"default"}})
	seedServerSecretListCanI(t, env, "preview-reader", nil, []string{"default"})
	shared := &topology.Topology{Nodes: []topology.Node{
		{ID: "deployment/default/root", Kind: topology.KindDeployment, Name: "root", Data: map[string]any{"namespace": "default"}},
		{ID: "configmap/default/visible", Kind: topology.KindConfigMap, Name: "visible", Data: map[string]any{"namespace": "default"}},
		{ID: "configmap/hidden/hidden", Kind: topology.KindConfigMap, Name: "hidden", Data: map[string]any{"namespace": "hidden"}},
		{ID: "secret/default/private", Kind: topology.KindSecret, Name: "private", Data: map[string]any{"namespace": "default"}},
	}, Edges: []topology.Edge{
		{Source: "deployment/default/root", Target: "configmap/default/visible", Type: topology.EdgeManages},
		{Source: "deployment/default/root", Target: "configmap/hidden/hidden", Type: topology.EdgeManages},
		{Source: "deployment/default/root", Target: "secret/default/private", Type: topology.EdgeManages},
	}}
	env.srv.broadcaster.updateCachedTopology(shared, env.srv.broadcaster.topoEpoch.Load())
	resp := env.authGet(t, "/api/resources/deployments/default/root/cascade-preview", "preview-reader", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var preview topology.CascadeDeletePreview
	if err := json.NewDecoder(resp.Body).Decode(&preview); err != nil {
		t.Fatal(err)
	}
	if !preview.RootResolved || len(preview.Dependents) != 1 || preview.Dependents[0].Name != "visible" {
		t.Fatalf("preview=%+v", preview)
	}
	if len(shared.Nodes) != 4 || len(shared.Edges) != 3 {
		t.Fatal("modified shared graph")
	}
	denied := env.authGet(t, "/api/resources/deployments/hidden/root/cascade-preview", "preview-reader", "")
	defer denied.Body.Close()
	if denied.StatusCode != http.StatusForbidden {
		t.Fatalf("denied root status=%d", denied.StatusCode)
	}
}

func TestCascadePreviewNormalizesSensitiveRootKinds(t *testing.T) {
	env := newAuthTestServer(t)
	old := k8s.GetConnectionStatus()
	t.Cleanup(func() { k8s.SetConnectionStatus(old) })
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"default"}}
	perms.SetCanI("get", "", "secrets", "default", false)
	perms.SetCanI("get", "", "limitranges", "default", false)
	perms.SetCanI("get", "", "namespaces", "", false)
	env.srv.permCache.Set("sensitive-preview-reader", nil, perms)
	for _, kind := range []string{"Secret", "secrets", "LimitRange", "Namespace"} {
		resp := env.authGet(t, "/api/resources/"+kind+"/default/private/cascade-preview", "sensitive-preview-reader", "")
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("%s bypassed its root read check: status=%d", kind, resp.StatusCode)
		}
	}
}
