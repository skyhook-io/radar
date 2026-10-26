package server

import (
	"encoding/json"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/topology"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubefake "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestCascadePreviewUsesCallerAndOwnershipWithoutTopology(t *testing.T) {
	env := newAuthTestServer(t)
	old := k8s.GetConnectionStatus()
	t.Cleanup(func() { k8s.SetConnectionStatus(old) })
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	env.srv.permCache.Set("preview-reader", nil, &auth.UserPermissions{AllowedNamespaces: []string{"default"}})
	seedServerSecretListCanI(t, env, "preview-reader", nil, []string{"default"})
	useTestResourceCache(t, kubefake.NewClientset(
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "nginx", Namespace: "default", UID: "deploy-uid-1234"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "visible", Namespace: "default", UID: "visible", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "nginx", UID: "deploy-uid-1234"}}}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "private", Namespace: "default", UID: "private", OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "nginx", UID: "deploy-uid-1234"}}}},
	))
	withOwnershipDiscovery(t)
	var switchDuringRead atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Impersonate-User") != "preview-reader" {
			t.Errorf("missing caller identity: %v", r.Header)
		}
		if switchDuringRead.Load() {
			k8s.CancelOngoingOperations()
			http.Error(w, "connection switched", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"nginx","namespace":"default","uid":"deploy-uid-1234"}}`))
	}))
	defer api.Close()
	previous := k8s.SetTestConfig(&rest.Config{Host: api.URL})
	t.Cleanup(func() { k8s.SetTestConfig(previous) })
	resp := env.authGet(t, "/api/resources/deployments/default/nginx/cascade-preview", "preview-reader", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	var preview topology.CascadeDeletePreview
	if err := json.NewDecoder(resp.Body).Decode(&preview); err != nil {
		t.Fatal(err)
	}
	if !preview.RootResolved || len(preview.Dependents) != 1 || preview.Dependents[0].Name != "visible" || preview.Basis != "ownerReferences" || preview.Coverage == nil || !preview.Coverage.Narrowed {
		t.Fatalf("preview=%+v", preview)
	}
	denied := env.authGet(t, "/api/resources/deployments/hidden/root/cascade-preview", "preview-reader", "")
	defer denied.Body.Close()
	if denied.StatusCode != http.StatusForbidden {
		t.Fatalf("denied root status=%d", denied.StatusCode)
	}
	switchDuringRead.Store(true)
	switched := env.authGet(t, "/api/resources/deployments/default/nginx/cascade-preview", "preview-reader", "")
	defer switched.Body.Close()
	if switched.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("switched preview status=%d", switched.StatusCode)
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
