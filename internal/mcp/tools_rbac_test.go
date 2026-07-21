package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/k8s"
)

func TestHandleGetSubjectPermissions_AccessCheckAllowed(t *testing.T) {
	capture := installSubjectAccessReviewClient(t, authv1.SubjectAccessReviewStatus{
		Allowed: true,
		Reason:  "allowed by test authorizer",
	}, 0)

	res, _, err := handleGetSubjectPermissions(context.Background(), nil, subjectPermissionsInput{
		Kind:      "ServiceAccount",
		Namespace: "operators",
		Name:      "controller",
		Verb:      " patch ",
		Resource:  " configmaps ",
	})
	if err != nil {
		t.Fatalf("handleGetSubjectPermissions: %v", err)
	}
	got := decodeToolResult(t, res)
	if len(got) != 2 || got["bindings"] != nil || got["flatRules"] != nil {
		t.Fatalf("check response keys = %#v, want focused subject + accessCheck", got)
	}
	check := objectField(t, got, "accessCheck")
	if check["verb"] != "patch" || check["resource"] != "configmaps" || check["namespace"] != "operators" {
		t.Fatalf("accessCheck target = %#v", check)
	}
	if check["allowed"] != true || check["reason"] != "allowed by test authorizer" {
		t.Fatalf("accessCheck verdict = %#v", check)
	}
	if capture.calls != 1 {
		t.Fatalf("SubjectAccessReview calls = %d, want 1", capture.calls)
	}
	if capture.review.Spec.User != "system:serviceaccount:operators:controller" {
		t.Errorf("SAR user = %q", capture.review.Spec.User)
	}
	wantGroups := []string{"system:authenticated", "system:serviceaccounts", "system:serviceaccounts:operators"}
	if !slices.Equal(capture.review.Spec.Groups, wantGroups) {
		t.Errorf("SAR groups = %v", capture.review.Spec.Groups)
	}
	attrs := capture.review.Spec.ResourceAttributes
	if attrs == nil || attrs.Namespace != "operators" || attrs.Verb != "patch" || attrs.Resource != "configmaps" {
		t.Fatalf("SAR attributes = %#v", attrs)
	}
}

func TestHandleGetSubjectPermissions_AccessCheckDeniedWithFullTarget(t *testing.T) {
	capture := installSubjectAccessReviewClient(t, authv1.SubjectAccessReviewStatus{
		Denied:          true,
		Reason:          "named resource is not granted",
		EvaluationError: "webhook authorizer returned partial data",
	}, 0)
	targetNamespace := "prod"

	res, _, err := handleGetSubjectPermissions(context.Background(), nil, subjectPermissionsInput{
		Kind:              "ServiceAccount",
		Namespace:         "operators",
		Name:              "controller",
		Verb:              "update",
		Resource:          "widgets",
		Group:             "apps.example.io",
		ResourceNamespace: &targetNamespace,
		Subresource:       "status",
		ResourceName:      "frontend",
	})
	if err != nil {
		t.Fatalf("handleGetSubjectPermissions: %v", err)
	}
	check := objectField(t, decodeToolResult(t, res), "accessCheck")
	if check["allowed"] != false || check["denied"] != true {
		t.Fatalf("accessCheck verdict = %#v", check)
	}
	if check["evaluationError"] != "webhook authorizer returned partial data" {
		t.Fatalf("evaluationError = %#v", check["evaluationError"])
	}
	attrs := capture.review.Spec.ResourceAttributes
	if attrs == nil || attrs.Namespace != "prod" || attrs.Group != "apps.example.io" ||
		attrs.Resource != "widgets" || attrs.Subresource != "status" || attrs.Name != "frontend" {
		t.Fatalf("SAR attributes = %#v", attrs)
	}
}

func TestHandleGetSubjectPermissions_AccessCheckDefaultDeny(t *testing.T) {
	installSubjectAccessReviewClient(t, authv1.SubjectAccessReviewStatus{}, 0)

	res, _, err := handleGetSubjectPermissions(context.Background(), nil, subjectPermissionsInput{
		Kind: "ServiceAccount", Namespace: "operators", Name: "controller", Verb: "get", Resource: "secrets",
	})
	if err != nil {
		t.Fatalf("handleGetSubjectPermissions: %v", err)
	}
	check := objectField(t, decodeToolResult(t, res), "accessCheck")
	if check["allowed"] != false {
		t.Fatalf("allowed = %#v, want false", check["allowed"])
	}
	if _, ok := check["denied"]; ok {
		t.Fatalf("default denial should not claim explicit denied: %#v", check)
	}
}

func TestHandleGetSubjectPermissions_AccessCheckValidation(t *testing.T) {
	emptyNamespace := ""
	tests := []struct {
		name    string
		input   subjectPermissionsInput
		wantErr string
	}{
		{
			name:    "verb without resource",
			input:   subjectPermissionsInput{Kind: "ServiceAccount", Namespace: "ns", Name: "sa", Verb: "get"},
			wantErr: "verb and resource are both required",
		},
		{
			name:    "resource without verb",
			input:   subjectPermissionsInput{Kind: "ServiceAccount", Namespace: "ns", Name: "sa", Resource: "pods"},
			wantErr: "verb and resource are both required",
		},
		{
			name:    "optional check field without pair",
			input:   subjectPermissionsInput{Kind: "ServiceAccount", Namespace: "ns", Name: "sa", Group: "apps"},
			wantErr: "verb and resource are both required",
		},
		{
			name:    "explicit empty namespace still opts into validation",
			input:   subjectPermissionsInput{Kind: "ServiceAccount", Namespace: "ns", Name: "sa", ResourceNamespace: &emptyNamespace},
			wantErr: "verb and resource are both required",
		},
		{
			name:    "user check is unsupported",
			input:   subjectPermissionsInput{Kind: "User", Name: "alice", Verb: "get", Resource: "pods"},
			wantErr: "access checks support ServiceAccount subjects only",
		},
		{
			name:    "target namespace whitespace",
			input:   subjectPermissionsInput{Kind: "ServiceAccount", Namespace: "ns", Name: "sa", Verb: "get", Resource: "pods", ResourceNamespace: stringPointer(" prod ")},
			wantErr: "resource_namespace must not contain surrounding whitespace",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := handleGetSubjectPermissions(context.Background(), nil, tt.input)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestHandleGetSubjectPermissions_AccessCheckFailsClosedWithoutClient(t *testing.T) {
	previous := k8s.SetTestClient(nil)
	t.Cleanup(func() { k8s.SetTestClient(previous) })

	res, _, err := handleGetSubjectPermissions(context.Background(), nil, subjectPermissionsInput{
		Kind: "ServiceAccount", Namespace: "operators", Name: "controller", Verb: "get", Resource: "pods",
	})
	if err == nil || !strings.Contains(err.Error(), "Kubernetes client unavailable") {
		t.Fatalf("error = %v, want unavailable client", err)
	}
	if res != nil {
		t.Fatalf("result = %#v, want nil on fail-closed path", res)
	}
}

func TestHandleGetSubjectPermissions_AccessCheckRequiresCallerSARPermission(t *testing.T) {
	installSubjectAccessReviewClient(t, authv1.SubjectAccessReviewStatus{}, http.StatusForbidden)

	res, _, err := handleGetSubjectPermissions(context.Background(), nil, subjectPermissionsInput{
		Kind: "ServiceAccount", Namespace: "operators", Name: "controller", Verb: "get", Resource: "pods",
	})
	if err == nil || !strings.Contains(err.Error(), "caller requires create permission on subjectaccessreviews.authorization.k8s.io") {
		t.Fatalf("error = %v, want caller SAR permission guidance", err)
	}
	if res != nil {
		t.Fatalf("result = %#v, want nil on forbidden path", res)
	}
}

func TestHandleGetSubjectPermissions_NoCheckPreservesPermissionsDump(t *testing.T) {
	t.Cleanup(k8s.ResetTestState)
	objects := []runtime.Object{
		&rbacv1.Role{
			ObjectMeta: metav1.ObjectMeta{Name: "config-reader", Namespace: "operators"},
			Rules:      []rbacv1.PolicyRule{{Verbs: []string{"get"}, APIGroups: []string{""}, Resources: []string{"configmaps"}}},
		},
		&rbacv1.RoleBinding{
			ObjectMeta: metav1.ObjectMeta{Name: "controller-reader", Namespace: "operators"},
			RoleRef:    rbacv1.RoleRef{Kind: "Role", Name: "config-reader"},
			Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Namespace: "operators", Name: "controller"}},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "controller-0", Namespace: "operators"},
			Spec:       corev1.PodSpec{ServiceAccountName: "controller", Containers: []corev1.Container{{Name: "controller", Image: "example.invalid/controller"}}},
		},
	}
	if err := k8s.InitTestResourceCache(fake.NewSimpleClientset(objects...)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}

	res, _, err := handleGetSubjectPermissions(context.Background(), nil, subjectPermissionsInput{
		Kind: "ServiceAccount", Namespace: "operators", Name: "controller",
	})
	if err != nil {
		t.Fatalf("handleGetSubjectPermissions: %v", err)
	}
	got := decodeToolResult(t, res)
	if _, ok := got["accessCheck"]; ok {
		t.Fatalf("legacy dump unexpectedly contains accessCheck: %#v", got)
	}
	for _, field := range []string{"subject", "bindings", "flatRules", "usedByPods"} {
		if _, ok := got[field]; !ok {
			t.Errorf("legacy dump missing %q: %#v", field, got)
		}
	}
	bindings, ok := got["bindings"].([]any)
	if !ok || len(bindings) != 1 {
		t.Fatalf("bindings = %#v, want one legacy binding row", got["bindings"])
	}
	rules, ok := got["flatRules"].([]any)
	if !ok || len(rules) != 1 {
		t.Fatalf("flatRules = %#v, want one legacy rule", got["flatRules"])
	}
}

func TestSubjectPermissionsToolSchemaIncludesOptionalAccessCheckFields(t *testing.T) {
	var raw []byte
	for _, tool := range listRegisteredTools(t) {
		if tool.Name == "get_subject_permissions" {
			var err error
			raw, err = json.Marshal(tool.InputSchema)
			if err != nil {
				t.Fatalf("marshal input schema: %v", err)
			}
			break
		}
	}
	if raw == nil {
		t.Fatal("get_subject_permissions is not registered")
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("unmarshal input schema: %v", err)
	}
	for _, field := range []string{"verb", "resource", "group", "resource_namespace", "subresource", "resource_name"} {
		if _, ok := schema.Properties[field]; !ok {
			t.Errorf("schema missing optional access-check field %q: %s", field, raw)
		}
		if slices.Contains(schema.Required, field) {
			t.Errorf("access-check field %q must remain optional in the shared dump/check tool", field)
		}
	}
}

type subjectAccessReviewCapture struct {
	calls  int
	review authv1.SubjectAccessReview
}

func installSubjectAccessReviewClient(t *testing.T, status authv1.SubjectAccessReviewStatus, responseCode int) *subjectAccessReviewCapture {
	t.Helper()
	capture := &subjectAccessReviewCapture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost || r.URL.Path != "/apis/authorization.k8s.io/v1/subjectaccessreviews" {
			writeRBACJSON(t, w, http.StatusNotFound, metav1.Status{Status: metav1.StatusFailure, Reason: metav1.StatusReasonNotFound})
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&capture.review); err != nil {
			t.Fatalf("decode SubjectAccessReview: %v", err)
		}
		capture.calls++
		if responseCode != 0 {
			writeRBACJSON(t, w, responseCode, metav1.Status{
				Status:  metav1.StatusFailure,
				Reason:  metav1.StatusReasonForbidden,
				Message: "subjectaccessreviews.authorization.k8s.io is forbidden",
				Code:    int32(responseCode),
			})
			return
		}
		writeRBACJSON(t, w, http.StatusOK, authv1.SubjectAccessReview{
			TypeMeta: metav1.TypeMeta{APIVersion: "authorization.k8s.io/v1", Kind: "SubjectAccessReview"},
			Status:   status,
		})
	}))
	t.Cleanup(server.Close)

	client, err := kubernetes.NewForConfig(&rest.Config{
		Host: server.URL,
		ContentConfig: rest.ContentConfig{
			ContentType: "application/json",
		},
	})
	if err != nil {
		t.Fatalf("create test Kubernetes client: %v", err)
	}
	previous := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(previous) })
	return capture
}

func writeRBACJSON(t *testing.T, w http.ResponseWriter, status int, value any) {
	t.Helper()
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("write test response: %v", err)
	}
}

func objectField(t *testing.T, object map[string]any, name string) map[string]any {
	t.Helper()
	value, ok := object[name].(map[string]any)
	if !ok {
		t.Fatalf("%s = %#v, want object", name, object[name])
	}
	return value
}

func stringPointer(value string) *string {
	return &value
}
