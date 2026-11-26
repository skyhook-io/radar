package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func TestGetResourceObservedIdentity(t *testing.T) {
	const secretValue = "private-example-secret"
	m := metav1.ObjectMeta{Name: "web", Namespace: "alpha", UID: "web-deployment", ResourceVersion: "v-42", Generation: 12}
	replicas := int32(2)
	deployment := &appsv1.Deployment{ObjectMeta: m, Spec: appsv1.DeploymentSpec{Replicas: &replicas, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}}}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "settings", Namespace: "alpha", UID: "web-secret", ResourceVersion: "v-41"}, Data: map[string][]byte{"token": []byte(secretValue)}}
	missing := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "unversioned", Namespace: "alpha", UID: "pod-without-version"}}
	if err := k8s.InitTestResourceCache(fake.NewSimpleClientset(fixtureNamespaces(deployment, secret, missing)...)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)

	const group = "example.test"
	gvr := schema.GroupVersionResource{Group: group, Version: "v1", Resource: "widgets"}
	widget := &unstructured.Unstructured{Object: map[string]any{"apiVersion": group + "/v1", "kind": "Widget", "metadata": map[string]any{"name": "web", "namespace": "alpha", "uid": "web-widget", "resourceVersion": "opaque-A"}}}
	dynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"}, widget)
	if err := k8s.InitTestDynamicResourceCache(dynamic, []k8s.APIResource{{Name: "widgets", Kind: "Widget", Group: group, Version: "v1", Namespaced: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	if _, err := k8s.GetResourceCache().ListDynamicWithGroup(t.Context(), "Widget", "alpha", group); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct{ kind, group, name, include, context, uid, rv string }{
		{"Deployment", "apps", "web", "identity", "none", "web-deployment", "v-42"},
		{"Deployment", "apps", "web", "", "none", "", ""},
		{"Widget", group, "web", "identity", "basic", "web-widget", "opaque-A"},
		{"Secret", "", "settings", "issues, identity", "none", "web-secret", "v-41"},
		{"Pod", "", "unversioned", "identity", "none", "", ""},
	} {
		result, _, err := handleGetResource(t.Context(), nil, getResourceInput{Kind: tc.kind, Group: tc.group, Namespace: "alpha", Name: tc.name, Include: tc.include, Context: tc.context})
		if err != nil {
			t.Fatalf("%s: %v", tc.kind, err)
		}
		body := extractText(t, result)
		var row struct {
			Identity *struct {
				UID, ResourceVersion string
				Generation           *int64
			}
			IncludeError  string
			IdentityError string
		}
		if err := json.Unmarshal([]byte(body), &row); err != nil {
			t.Fatal(err)
		}
		if row.IncludeError != "" || strings.Contains(body, secretValue) || strings.Contains(body, "cHJpdmF0ZS1leGFtcGxlLXNlY3JldA==") {
			t.Fatalf("%s returned unexpected content: %s", tc.kind, body)
		}
		if (row.IdentityError != "") != (tc.name == "unversioned") {
			t.Fatalf("expected an explanation only for the unversioned observation: %s", body)
		}
		if row.Identity != nil {
			if tc.kind == "Deployment" {
				if row.Identity.Generation == nil || *row.Identity.Generation != 12 {
					t.Fatalf("wrong observed generation: %s", body)
				}
			} else if row.Identity.Generation != nil {
				t.Fatalf("zero generation must be omitted: %s", body)
			}
		}
		if tc.uid == "" {
			if row.Identity != nil || strings.Contains(body, `"resourceVersion"`) {
				t.Fatalf("unexpected identity: %s", body)
			}
		} else if row.Identity == nil || row.Identity.UID != tc.uid || row.Identity.ResourceVersion != tc.rv {
			t.Fatalf("%s wrong identity: %s", tc.kind, body)
		}
	}

	ctx := withRestrictedUser(t, "limited-reader", []string{"alpha"})
	seedSecretGetCanI(t, "limited-reader", nil, []string{"alpha"})
	response, _, err := handleGetResource(ctx, nil, getResourceInput{Kind: "Secret", Namespace: "alpha", Name: "settings", Include: "identity", Context: "none"})
	if response != nil || err == nil || !strings.HasPrefix(err.Error(), "user_forbidden:") {
		t.Fatalf("denied identity read returned %+v, %v", response, err)
	}
}
