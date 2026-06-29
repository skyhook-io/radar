package mcp

import (
	"context"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
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

func TestSummarizeWorkloadRolloutRequiresObservedGeneration(t *testing.T) {
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
			"updatedReplicas":   int64(2),
			"availableReplicas": int64(2),
		},
	}}

	got := summarizeWorkloadRollout(dep)
	if got["complete"] != false {
		t.Fatalf("complete = %v, want false without observedGeneration; got %#v", got["complete"], got)
	}
	if _, ok := got["observedCurrentGeneration"]; ok {
		t.Fatalf("observedCurrentGeneration should be absent when controller has not reported observedGeneration: %#v", got)
	}
}

func TestSummarizeWorkloadRolloutStatefulSetAndDaemonSet(t *testing.T) {
	sts := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "StatefulSet",
		"metadata":   map[string]any{"name": "db", "namespace": "prod", "generation": int64(4)},
		"spec":       map[string]any{"replicas": int64(3)},
		"status": map[string]any{
			"observedGeneration": int64(4),
			"readyReplicas":      int64(2),
			"updatedReplicas":    int64(3),
		},
	}}
	if got := summarizeWorkloadRollout(sts); got["complete"] != false || got["readyReplicas"] != int64(2) {
		t.Fatalf("statefulset rollout = %+v, want incomplete with 2 ready", got)
	}

	ds := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "DaemonSet",
		"metadata":   map[string]any{"name": "agent", "namespace": "prod", "generation": int64(2)},
		"status": map[string]any{
			"observedGeneration":     int64(2),
			"desiredNumberScheduled": int64(5),
			"updatedNumberScheduled": int64(5),
			"numberAvailable":        int64(4),
			"numberUnavailable":      int64(1),
		},
	}}
	if got := summarizeWorkloadRollout(ds); got["complete"] != false || got["numberUnavailable"] != int64(1) {
		t.Fatalf("daemonset rollout = %+v, want incomplete with 1 unavailable", got)
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

func TestBuildMutationVerificationFetchesPostState(t *testing.T) {
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "configmaps"}
	dyn := setupMCPDynamicResource(t, gvr, "ConfigMapList", k8s.APIResource{
		Version:    "v1",
		Kind:       "ConfigMap",
		Name:       "configmaps",
		Namespaced: true,
		Verbs:      []string{"get", "list"},
	}, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "ConfigMap",
		"metadata":   map[string]any{"name": "cfg", "namespace": "prod"},
		"data":       map[string]any{"key": "value"},
	}})

	got := buildMutationVerification(context.Background(), dyn, mutationVerificationOptions{
		Kind:      "ConfigMap",
		Namespace: "prod",
		Name:      "cfg",
		GVR:       gvr,
	})
	if got["error"] != nil {
		t.Fatalf("verification error = %v", got["error"])
	}
	if got["resource"] == nil {
		t.Fatalf("resource missing from verification: %+v", got)
	}
}

func TestBuildMutationVerificationReportsMissingPostClient(t *testing.T) {
	got := buildMutationVerification(context.Background(), nil, mutationVerificationOptions{
		Kind:      "ConfigMap",
		Namespace: "prod",
		Name:      "cfg",
	})
	if got["error"] != "post-mutation object unavailable and dynamic client is nil" {
		t.Fatalf("error = %v, want missing dynamic client", got["error"])
	}
}

func TestBuildMutationVerificationIncludesPodAndIssueSnapshots(t *testing.T) {
	defer k8s.ResetTestState()

	replicas := int32(1)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "prod", CreationTimestamp: metav1.Now(), Generation: 2},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}},
			Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "web"}}},
		},
		Status: appsv1.DeploymentStatus{UnavailableReplicas: 1},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "web-abc", Namespace: "prod", Labels: map[string]string{"app": "web"}},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{{
				Name:         "app",
				RestartCount: 3,
				State:        corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}},
			}},
		},
	}
	if err := k8s.InitTestResourceCache(fake.NewClientset(dep, pod)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}

	post := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]any{"name": "web", "namespace": "prod", "generation": int64(2)},
		"spec": map[string]any{
			"replicas": int64(1),
			"selector": map[string]any{"matchLabels": map[string]any{"app": "web"}},
		},
		"status": map[string]any{"unavailableReplicas": int64(1)},
	}}

	got := buildMutationVerification(context.Background(), nil, mutationVerificationOptions{Post: post})
	pods, ok := got["pods"].(map[string]any)
	if !ok || pods["total"] != 1 || pods["ready"] != 0 {
		t.Fatalf("pods summary = %#v, want total=1 ready=0", got["pods"])
	}
	if pods["restarts"] != int64(3) {
		t.Fatalf("pods restarts = %#v, want int64(3)", pods["restarts"])
	}
	if pods["source"] != "informer_cache" || got["cacheSnapshot"] == nil {
		t.Fatalf("cache snapshot metadata missing: %+v", got)
	}
	issues, ok := got["currentIssues"].([]map[string]any)
	if !ok || len(issues) == 0 {
		t.Fatalf("currentIssues = %#v, want related issue", got["currentIssues"])
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

func TestSubmittedVsLiveDiffIgnoresKubernetesDefaultedFields(t *testing.T) {
	serviceDesired := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Service",
		"metadata":   map[string]any{"name": "api", "namespace": "prod"},
		"spec": map[string]any{
			"ports": []any{map[string]any{"port": int64(80), "targetPort": int64(8080)}},
		},
	}}
	serviceLive := serviceDesired.DeepCopy()
	_ = unstructured.SetNestedSlice(serviceLive.Object, []any{
		map[string]any{"port": int64(80), "targetPort": int64(8080), "protocol": "TCP"},
	}, "spec", "ports")
	if got := submittedVsLiveDiff(serviceDesired, nil, serviceLive); got != nil {
		t.Fatalf("service default diff = %+v, want nil", got)
	}

	deployDesired := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]any{"name": "web", "namespace": "prod"},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"containers": []any{map[string]any{
				"name":  "app",
				"ports": []any{map[string]any{"containerPort": int64(8080)}},
				"readinessProbe": map[string]any{
					"httpGet": map[string]any{"path": "/healthz", "port": int64(8080)},
				},
			}},
		}}},
	}}
	deployLive := deployDesired.DeepCopy()
	_ = unstructured.SetNestedSlice(deployLive.Object, []any{map[string]any{
		"name":  "app",
		"ports": []any{map[string]any{"containerPort": int64(8080), "protocol": "TCP"}},
		"readinessProbe": map[string]any{
			"httpGet":          map[string]any{"path": "/healthz", "port": int64(8080), "scheme": "HTTP"},
			"timeoutSeconds":   int64(1),
			"periodSeconds":    int64(10),
			"successThreshold": int64(1),
			"failureThreshold": int64(3),
		},
	}}, "spec", "template", "spec", "containers")
	if got := submittedVsLiveDiff(deployDesired, nil, deployLive); got != nil {
		t.Fatalf("deployment default diff = %+v, want nil", got)
	}
}

func TestSubmittedVsLiveDiffStillFlagsExtraSubmittedListItems(t *testing.T) {
	desired := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]any{"name": "web", "namespace": "prod"},
		"spec": map[string]any{"template": map[string]any{"spec": map[string]any{
			"containers": []any{map[string]any{
				"name":  "app",
				"ports": []any{map[string]any{"containerPort": int64(8080)}},
			}},
		}}},
	}}
	live := desired.DeepCopy()
	_ = unstructured.SetNestedSlice(live.Object, []any{map[string]any{
		"name": "app",
		"ports": []any{
			map[string]any{"containerPort": int64(8080), "protocol": "TCP"},
			map[string]any{"containerPort": int64(9090), "protocol": "TCP"},
		},
	}}, "spec", "template", "spec", "containers")

	got := submittedVsLiveDiff(desired, nil, live)
	if got == nil {
		t.Fatal("expected diff for extra live port item")
	}
	diffs := got["differences"].([]map[string]any)
	if len(diffs) != 1 || diffs[0]["type"] != "submitted_field_differs" || diffs[0]["path"] != "/spec/template/spec/containers/app/ports" {
		t.Fatalf("diffs = %+v, want submitted_field_differs on container ports", diffs)
	}
}

func TestSubmittedVsLiveDiffPodKindAndEightCap(t *testing.T) {
	desired := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1",
		"kind":       "Pod",
		"metadata":   map[string]any{"name": "pod", "namespace": "prod"},
		"spec": map[string]any{
			"dnsPolicy": "None",
			"dnsConfig": map[string]any{"nameservers": []any{"8.8.8.8"}},
			"containers": []any{
				map[string]any{"name": "c1", "image": "img:v2"},
				map[string]any{"name": "c2", "image": "img:v2"},
				map[string]any{"name": "c3", "image": "img:v2"},
				map[string]any{"name": "c4", "image": "img:v2"},
				map[string]any{"name": "c5", "image": "img:v2"},
				map[string]any{"name": "c6", "image": "img:v2"},
				map[string]any{"name": "c7", "image": "img:v2"},
				map[string]any{"name": "c8", "image": "img:v2"},
				map[string]any{"name": "c9", "image": "img:v2"},
			},
		},
	}}
	live := desired.DeepCopy()
	_ = unstructured.SetNestedField(live.Object, "ClusterFirst", "spec", "dnsPolicy")
	_ = unstructured.SetNestedField(live.Object, map[string]any{"nameservers": []any{"1.1.1.1"}}, "spec", "dnsConfig")
	liveContainers, _, _ := unstructured.NestedSlice(live.Object, "spec", "containers")
	for _, item := range liveContainers {
		item.(map[string]any)["image"] = "img:v1"
	}
	_ = unstructured.SetNestedSlice(live.Object, liveContainers, "spec", "containers")

	got := submittedVsLiveDiff(desired, nil, live)
	if got == nil {
		t.Fatal("expected pod diff")
	}
	diffs := got["differences"].([]map[string]any)
	if len(diffs) != 8 {
		t.Fatalf("diff count = %d, want capped at 8: %+v", len(diffs), diffs)
	}
	if diffs[0]["path"] != "/spec/dnsPolicy" {
		t.Fatalf("first pod diff path = %v, want /spec/dnsPolicy", diffs[0]["path"])
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
