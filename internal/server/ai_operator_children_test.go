package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	appsv1 "k8s.io/api/apps/v1"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func TestAIOperatorChildrenApplyGetBeforePreviewLimit(t *testing.T) {
	yes, one := true, int32(1)
	root := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "opentelemetry.io/v1beta1", "kind": "OpenTelemetryCollector",
		"metadata": map[string]any{"namespace": "default", "name": "collector", "uid": "collector"},
	}}
	owner := []metav1.OwnerReference{{APIVersion: "opentelemetry.io/v1beta1", Kind: "OpenTelemetryCollector", Name: "collector", UID: "collector", Controller: &yes}}
	objects := []runtime.Object{&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "readable-config", UID: "config", OwnerReferences: owner}}}
	for i := range 30 {
		name := fmt.Sprintf("denied-runtime-%02d", i)
		objects = append(objects, &appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: name, UID: types.UID(name), OwnerReferences: owner},
			Spec:       appsv1.DeploymentSpec{Replicas: &one, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": name}}},
		})
	}
	useTestResourceCache(t, fake.NewClientset(objects...))
	gvr := schema.GroupVersionResource{Group: "opentelemetry.io", Version: "v1beta1", Resource: "opentelemetrycollectors"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "OpenTelemetryCollectorList"}, root)
	verbs := []string{"get", "list", "watch", "delete"}
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{
		{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: "OpenTelemetryCollector", Namespaced: true, Verbs: verbs},
		{Group: "apps", Version: "v1", Name: "deployments", Kind: "Deployment", Namespaced: true, Verbs: verbs},
		{Version: "v1", Name: "configmaps", Kind: "ConfigMap", Namespaced: true, Verbs: verbs},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	dc := k8s.GetDynamicResourceCache()
	if err := dc.EnsureWatching(gvr); err != nil || !dc.WaitForSync(gvr, 2*time.Second) {
		t.Fatalf("root cache not ready: %v", err)
	}
	fakeSARServer(t, func(authv1.ResourceAttributes) bool { return false })
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"default"}}
	perms.SetCanI("get", gvr.Group, gvr.Resource, "default", true)
	perms.SetCanI("list", gvr.Group, gvr.Resource, "default", true)
	perms.SetCanI("list", "apps", "deployments", "default", true)
	perms.SetCanI("get", "apps", "deployments", "default", false)
	perms.SetCanI("get", "", "configmaps", "default", true)
	env.srv.permCache.Set("preview-reader", nil, perms)
	resp := env.authGet(t, "/api/ai/resources/opentelemetrycollectors/default/collector?group=opentelemetry.io", "preview-reader", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status=%d: %s", resp.StatusCode, body)
	}
	var body struct {
		Context *resourcecontext.ResourceContext `json:"resourceContext"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Context == nil || body.Context.Children == nil {
		t.Fatalf("missing child context: %+v", body.Context)
	}
	children := body.Context.Children
	if len(children.Items) != 1 || children.Items[0].Name != "readable-config" || children.Truncated {
		t.Fatalf("denied workloads consumed readable preview slots: %+v", children)
	}
}
