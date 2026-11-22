package mcp

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// A kagent RemoteMCPServer or ModelConfig is read only with the caller's own
// access to its kind; the issues tool withholds their findings without it,
// as the drawer and REST Issues do.
func TestMCPIssuesToolGatesKagentCredentialKinds(t *testing.T) {
	if err := k8s.InitTestResourceCache(fake.NewClientset()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetResourceCache)
	k8s.ResetTestDynamicState()
	t.Cleanup(k8s.ResetTestDynamicState)

	servers := schema.GroupVersionResource{Group: "api.kagent.dev", Version: "v1alpha3", Resource: "remotemcpservers"}
	models := schema.GroupVersionResource{Group: "api.kagent.dev", Version: "v1alpha3", Resource: "modelconfigs"}
	failing := func(kind, name string, condition map[string]any) *unstructured.Unstructured {
		u := &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{"observedGeneration": int64(1), "conditions": []any{condition}}}}
		u.SetAPIVersion("api.kagent.dev/v1alpha3")
		u.SetKind(kind)
		u.SetNamespace("agents")
		u.SetName(name)
		u.SetGeneration(1)
		u.SetCreationTimestamp(metav1.NewTime(time.Now().Add(-time.Hour)))
		return u
	}
	since := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	server := failing("RemoteMCPServer", "github", map[string]any{"type": "Accepted", "status": "False", "reason": "DiscoveryFailed", "message": "no such host", "observedGeneration": int64(1), "lastTransitionTime": since})
	model := failing("ModelConfig", "gpt", map[string]any{"type": "ResolvedRefs", "status": "False", "reason": "APIKeySecretNotFound", "message": "secret openai not found", "observedGeneration": int64(1), "lastTransitionTime": since})
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{servers: "RemoteMCPServerList", models: "ModelConfigList"}, server, model)
	if err := k8s.InitTestDynamicResourceCache(client, []k8s.APIResource{
		{Group: servers.Group, Version: servers.Version, Kind: "RemoteMCPServer", Name: servers.Resource, Namespaced: true, IsCRD: true, Verbs: []string{"list", "watch"}},
		{Group: models.Group, Version: models.Version, Kind: "ModelConfig", Name: models.Resource, Namespaced: true, IsCRD: true, Verbs: []string{"list", "watch"}},
	}); err != nil {
		t.Fatal(err)
	}
	dc := k8s.GetDynamicResourceCache()
	for _, gvr := range []schema.GroupVersionResource{servers, models} {
		if err := dc.EnsureWatching(gvr); err != nil {
			t.Fatal(err)
		}
		if !dc.WaitForSync(gvr, 2*time.Second) {
			t.Fatalf("%s cache did not sync", gvr.Resource)
		}
	}

	ctx := pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: "kagent-reader"})
	perms := &pkgauth.UserPermissions{AllowedNamespaces: []string{"agents"}}
	getPermCache().Set("kagent-reader", nil, perms)
	t.Cleanup(func() { getPermCache().Invalidate() })

	for _, tt := range []struct {
		kind     string
		resource string
	}{{"RemoteMCPServer", "remotemcpservers"}, {"ModelConfig", "modelconfigs"}} {
		for _, granted := range []bool{false, true} {
			perms.SetCanI("get", "api.kagent.dev", tt.resource, "agents", granted)
			result, _, err := handleIssuesTool(ctx, nil, issuesInput{Namespace: "agents", Kind: tt.kind})
			if err != nil {
				t.Fatal(err)
			}
			var response issuesapi.Response
			if err := json.Unmarshal([]byte(extractText(t, result)), &response); err != nil {
				t.Fatal(err)
			}
			found := 0
			for _, iss := range response.Issues {
				if iss.Kind == tt.kind && iss.Group == "api.kagent.dev" {
					found++
				}
			}
			if want := map[bool]int{false: 0, true: 1}[granted]; found != want {
				t.Errorf("%s with get=%v: %d issues, want %d (%+v)", tt.kind, granted, found, want, response.Issues)
			}
		}
	}
}
