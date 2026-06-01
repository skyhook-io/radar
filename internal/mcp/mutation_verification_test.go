package mcp

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestSummarizeWorkloadRolloutDeployment(t *testing.T) {
	dep := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]any{
			"name":       "frontend",
			"namespace":  "prod",
			"generation": int64(3),
		},
		"spec": map[string]any{"replicas": int64(2)},
		"status": map[string]any{
			"observedGeneration": int64(3),
			"updatedReplicas":    int64(2),
			"availableReplicas":  int64(2),
			"conditions": []any{
				map[string]any{"type": "Available", "status": "True"},
			},
		},
	}}

	got := summarizeWorkloadRollout(dep)
	if got["complete"] != true {
		t.Fatalf("complete = %v, want true; got %#v", got["complete"], got)
	}
	if got["observedCurrentGeneration"] != true {
		t.Fatalf("observedCurrentGeneration = %v, want true", got["observedCurrentGeneration"])
	}
	if got["availableReplicas"] != int64(2) {
		t.Fatalf("availableReplicas = %v, want 2", got["availableReplicas"])
	}
}

func TestApplyDocMutationTarget(t *testing.T) {
	target, err := applyDocMutationTarget(`
apiVersion: apps/v1
kind: Deployment
metadata:
  name: frontend
  namespace: old
`, "prod")
	if err != nil {
		t.Fatalf("applyDocMutationTarget: %v", err)
	}
	if target.Kind != "Deployment" || target.Group != "apps" || target.Namespace != "prod" || target.Name != "frontend" {
		t.Fatalf("target = %+v, want apps Deployment prod/frontend", target)
	}
}

func TestResolveMutationGVRAcceptsBuiltinAliasesWithoutGroup(t *testing.T) {
	cases := []struct {
		kind       string
		wantGVR    schema.GroupVersionResource
		namespaced bool
	}{
		{
			kind:       "deploy",
			wantGVR:    schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"},
			namespaced: true,
		},
		{
			kind:       "sts",
			wantGVR:    schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"},
			namespaced: true,
		},
		{
			kind:       "svc",
			wantGVR:    schema.GroupVersionResource{Version: "v1", Resource: "services"},
			namespaced: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			got, namespaced, err := resolveMutationGVR(tc.kind, "")
			if err != nil {
				t.Fatalf("resolveMutationGVR(%q): %v", tc.kind, err)
			}
			if got != tc.wantGVR || namespaced != tc.namespaced {
				t.Fatalf("resolveMutationGVR(%q) = (%v, %v), want (%v, %v)", tc.kind, got, namespaced, tc.wantGVR, tc.namespaced)
			}
		})
	}
}

func TestSubmittedVsLiveDiffFlagsRetainedContainer(t *testing.T) {
	desired := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]any{"name": "frontend", "namespace": "prod"},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"containers": []any{map[string]any{"name": "app", "image": "frontend:v2"}},
		}}},
	}}
	live := desired.DeepCopy()
	unstructured.SetNestedSlice(live.Object, []any{
		map[string]any{"name": "app", "image": "frontend:v2"},
		map[string]any{"name": "debug", "image": "busybox"},
	}, "spec", "template", "spec", "containers")

	got := submittedVsLiveDiff(desired, nil, live)
	if got == nil {
		t.Fatal("expected desired/live diff")
	}
	diffs := got["differences"].([]map[string]any)
	if len(diffs) != 1 || diffs[0]["type"] != "extra_live_list_items" {
		t.Fatalf("diffs = %+v, want extra_live_list_items", diffs)
	}
}

func TestSubmittedVsLiveDiffFlagsRetainedOmittedPodSpecField(t *testing.T) {
	desired := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]any{"name": "frontend", "namespace": "prod"},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"containers": []any{map[string]any{"name": "app", "image": "frontend:v2"}},
		}}},
	}}
	before := desired.DeepCopy()
	live := desired.DeepCopy()
	for _, obj := range []*unstructured.Unstructured{before, live} {
		_ = unstructured.SetNestedField(obj.Object, "None", "spec", "template", "spec", "dnsPolicy")
	}

	got := submittedVsLiveDiff(desired, before, live)
	if got == nil {
		t.Fatal("expected desired/live diff")
	}
	diffs := got["differences"].([]map[string]any)
	if len(diffs) != 1 || diffs[0]["type"] != "omitted_field_retained" || diffs[0]["path"] != "/spec/template/spec/dnsPolicy" {
		t.Fatalf("diffs = %+v, want retained dnsPolicy", diffs)
	}
}

func TestBuildMutationVerificationReportsPreReadFailure(t *testing.T) {
	post := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "cfg", "namespace": "prod"},
		"data":       map[string]any{"key": "value"},
	}}

	got := buildMutationVerification(nil, nil, mutationVerificationOptions{
		Post:      post,
		BeforeErr: "forbidden",
	})
	preRead, ok := got["preMutationRead"].(map[string]any)
	if !ok {
		t.Fatalf("preMutationRead = %#v, want map", got["preMutationRead"])
	}
	if preRead["status"] != "failed" || preRead["error"] != "forbidden" {
		t.Fatalf("preMutationRead = %#v, want failed forbidden", preRead)
	}
}

func TestBuildMutationVerificationIncludesSubmittedLiveDiff(t *testing.T) {
	desired := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]any{"name": "frontend", "namespace": "prod"},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"containers": []any{map[string]any{"name": "app", "image": "frontend:v2"}},
		}}},
	}}
	before := desired.DeepCopy()
	post := desired.DeepCopy()
	_ = unstructured.SetNestedField(before.Object, "None", "spec", "template", "spec", "dnsPolicy")
	_ = unstructured.SetNestedField(post.Object, "None", "spec", "template", "spec", "dnsPolicy")

	got := buildMutationVerification(nil, nil, mutationVerificationOptions{
		Post:    post,
		Before:  before,
		Desired: desired,
	})
	if got["mode"] != "post_mutation_state" {
		t.Fatalf("mode = %v, want post_mutation_state", got["mode"])
	}
	if got["resource"] == nil {
		t.Fatalf("resource missing from verification: %+v", got)
	}
	diff, ok := got["desiredLiveDiff"].(map[string]any)
	if !ok {
		t.Fatalf("desiredLiveDiff = %#v, want map", got["desiredLiveDiff"])
	}
	diffs := diff["differences"].([]map[string]any)
	if len(diffs) != 1 || diffs[0]["type"] != "omitted_field_retained" || diffs[0]["path"] != "/spec/template/spec/dnsPolicy" {
		t.Fatalf("diffs = %+v, want retained dnsPolicy", diffs)
	}
}

func TestSubmittedVsLiveDiffCleanWhenSubmittedFieldsMatch(t *testing.T) {
	desired := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Service",
		"metadata":   map[string]any{"name": "api", "namespace": "prod"},
		"spec": map[string]any{
			"selector": map[string]any{"app": "api"},
			"ports":    []any{map[string]any{"name": "http", "port": int64(80), "targetPort": int64(8080)}},
		},
	}}
	live := desired.DeepCopy()
	_ = unstructured.SetNestedSlice(live.Object, []any{
		map[string]any{"name": "http", "port": float64(80), "targetPort": float64(8080)},
	}, "spec", "ports")

	if got := submittedVsLiveDiff(desired, nil, live); got != nil {
		t.Fatalf("diff = %+v, want nil", got)
	}
}

func TestSubmittedVsLiveDiffFieldOrderDeterministic(t *testing.T) {
	desired := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]any{"name": "frontend", "namespace": "prod"},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"containers": []any{
				map[string]any{"name": "b", "image": "b:v2"},
				map[string]any{"name": "a", "image": "a:v2"},
			},
		}}},
	}}
	live := desired.DeepCopy()
	unstructured.SetNestedSlice(live.Object, []any{
		map[string]any{"name": "a", "image": "a:v1"},
		map[string]any{"name": "b", "image": "b:v1"},
	}, "spec", "template", "spec", "containers")

	got := submittedVsLiveDiff(desired, nil, live)
	if got == nil {
		t.Fatal("expected desired/live diff")
	}
	diffs := got["differences"].([]map[string]any)
	if len(diffs) != 2 {
		t.Fatalf("diffs = %+v, want 2 field diffs", diffs)
	}
	if diffs[0]["path"] != "/spec/template/spec/containers/a/image" || diffs[1]["path"] != "/spec/template/spec/containers/b/image" {
		t.Fatalf("diff order = %+v, want sorted by container name", diffs)
	}
}
