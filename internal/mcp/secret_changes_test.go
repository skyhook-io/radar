package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/meaningfulchanges"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func secretChangesMCPFixture(t *testing.T) (*appsv1.Deployment, time.Time) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	controller := true
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "reader", Namespace: "shop", UID: "reader", CreationTimestamp: metav1.NewTime(now.Add(-time.Hour))}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "reader"}}, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Image: "example:v1", Env: []corev1.EnvVar{{Name: "PASSWORD", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "db"}, Key: "password"}}}}}}}}}}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "shop", Annotations: map[string]string{"kubectl.kubernetes.io/last-applied-configuration": "dummy-last-applied-secret"}}, Data: map[string][]byte{"password": []byte("dummy-secret-value")}}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "reader-pod", Namespace: "shop", Labels: map[string]string{"app": "reader"}, OwnerReferences: []metav1.OwnerReference{{Kind: "Deployment", APIVersion: "apps/v1", Name: "reader", UID: "reader", Controller: &controller}}}, Spec: deployment.Spec.Template.Spec, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "app", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: metav1.NewTime(now.Add(-10 * time.Minute))}}}}}}
	client := fake.NewSimpleClientset(deployment, secret, pod, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}})
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	store := initCorrelationStore(t)
	old := secret.DeepCopy()
	old.Data["password"] = []byte("dummy-old-secret-value")
	diff := k8s.ComputeDiff("Secret", old, secret)
	if err := store.Append(context.Background(), timeline.TimelineEvent{ID: "secret-write", Kind: "Secret", APIVersion: "v1", Name: "db", Namespace: "shop", Timestamp: now.Add(-5 * time.Minute), ClusterContext: k8s.ActiveClusterContext(), Source: timeline.SourceInformer, EventType: timeline.EventTypeUpdate, Diff: diff}); err != nil {
		t.Fatal(err)
	}
	return deployment, now
}

func TestSecretChangesMCPSurfacesAndRedaction(t *testing.T) {
	deployment, _ := secretChangesMCPFixture(t)
	ctx := context.Background()
	for _, surface := range []string{"get_changes", "issues", "diagnose", "get_resource"} {
		t.Run(surface, func(t *testing.T) {
			var result *mcp.CallToolResult
			var err error
			switch surface {
			case "get_changes":
				result, _, err = handleGetChanges(ctx, nil, getChangesInput{Namespace: "shop"})
			case "issues":
				result, _, err = handleIssuesTool(ctx, nil, issuesInput{Namespace: "shop"})
			case "diagnose":
				result, _, err = handleDiagnose(ctx, nil, testDiagnoseInput("deployment", "shop", "reader"))
			case "get_resource":
				result, _, err = handleGetResource(ctx, nil, getResourceInput{Kind: "secret", Namespace: "shop", Name: "db", Include: "changes", Context: "none"})
			}
			if err != nil {
				t.Fatal(err)
			}
			body := extractText(t, result)
			for _, sentinel := range []string{"dummy-secret-value", "dummy-old-secret-value", "dummy-last-applied-secret", "kubectl.kubernetes.io/last-applied-configuration", base64.StdEncoding.EncodeToString([]byte("dummy-secret-value"))} {
				if strings.Contains(body, sentinel) {
					t.Fatalf("%s contains forbidden Secret material", surface)
				}
			}
			if !strings.Contains(body, `"running_previous_value"`) || !strings.Contains(body, `"keys":["password"]`) {
				t.Fatalf("%s missing annotated history: %s", surface, body)
			}
		})
	}
	response := issues.ListResponse{Issues: []issuesapi.Issue{criticalIssue("Deployment", deployment.Name)}}
	attachIssueChangeCorrelation(ctx, &response)
	if len(response.Issues) != 1 || response.Issues[0].Severity != issuesapi.SeverityCritical || len(response.Issues[0].CorrelatedChanges) != 1 || response.Issues[0].CorrelatedChanges[0].Kind != "Secret" {
		t.Fatalf("correlation=%+v", response.Issues)
	}
}

func TestSecretCorrelationHistoryComplete(t *testing.T) {
	deployment, now := secretChangesMCPFixture(t)
	deployment.CreationTimestamp = metav1.NewTime(now)
	coverage := issuesapi.HistoryCoverage{Incomplete: true, CompleteSince: now.Add(-time.Minute)}
	if correlationHistoryComplete(deployment, "shop", coverage) {
		t.Fatal("old referenced Secret incorrectly treated as complete")
	}
	secret, _ := k8s.GetResourceCache().Secrets().Secrets("shop").Get("db")
	copy := secret.DeepCopy()
	copy.CreationTimestamp = metav1.NewTime(now)
	k8s.ResetTestState()
	if err := k8s.InitTestResourceCache(fake.NewSimpleClientset(deployment, copy)); err != nil {
		t.Fatal(err)
	}
	if !correlationHistoryComplete(deployment, "shop", coverage) {
		t.Fatal("new referenced Secret should cover its whole lifetime")
	}
}

func TestSecretChangesRBACAcrossMCPSurfaces(t *testing.T) {
	secretChangesMCPFixture(t)
	ctx := withClusterAdmin(t, "secret-context-reader")
	perms := getPermCache().Get("secret-context-reader", nil)
	for _, resource := range []string{"secrets", "configmaps"} {
		perms.SetCanI("list", "", resource, "shop", true)
		perms.SetCanI("get", "", resource, "shop", true)
	}
	perms.SetCanI("list", "apps", "deployments", "shop", true)
	perms.SetCanI("get", "apps", "deployments", "shop", true)
	perms.SetCanI("get", "", "pods", "shop", false)
	for _, surface := range []string{"get_changes", "issues", "diagnose", "get_resource", "correlation"} {
		t.Run(surface, func(t *testing.T) {
			var result *mcp.CallToolResult
			var err error
			var body string
			switch surface {
			case "get_changes":
				result, _, err = handleGetChanges(ctx, nil, getChangesInput{Namespace: "shop"})
			case "issues":
				result, _, err = handleIssuesTool(ctx, nil, issuesInput{Namespace: "shop"})
			case "diagnose":
				result, _, err = handleDiagnose(ctx, nil, testDiagnoseInput("deployment", "shop", "reader"))
			case "get_resource":
				result, _, err = handleGetResource(ctx, nil, getResourceInput{Kind: "secret", Namespace: "shop", Name: "db", Include: "changes", Context: "none"})
			case "correlation":
				res, _, _, e := correlationChangesForIssue(ctx, &issuesapi.Issue{Kind: "Deployment", Namespace: "shop", Name: "reader"}, time.Hour, 10)
				err = e
				data, _ := json.Marshal(res.Changes)
				body = string(data)
			}
			if err != nil {
				t.Fatal(err)
			}
			if result != nil {
				body = extractText(t, result)
			}
			if strings.Contains(body, `"running_previous_value"`) {
				t.Fatalf("%s leaked Pod annotation: %s", surface, body)
			}
			if !strings.Contains(body, `"kind":"Secret"`) {
				t.Fatalf("%s lost permitted Secret history: %s", surface, body)
			}
		})
	}
	// Without Deployment read, the referencing Deployment is counted, not named.
	perms.SetCanI("list", "apps", "deployments", "shop", false)
	perms.SetCanI("get", "apps", "deployments", "shop", false)
	result, _, err := handleGetChanges(ctx, nil, getChangesInput{Namespace: "shop", Kind: "Secret"})
	if err != nil {
		t.Fatal(err)
	}
	body := extractText(t, result)
	if strings.Contains(body, "Deployment/reader") || !strings.Contains(body, `"consumed_by_withheld":{"count":1,"reason":"user_forbidden"}`) {
		t.Fatalf("hidden Deployment not withheld: %s", body)
	}
	perms.SetCanI("list", "", "secrets", "shop", false)
	filter := configSourceFilter(ctx, "shop")
	if filter("Secret", "db") || !filter("ConfigMap", "config") {
		t.Fatal("config source filter did not gate Secret independently")
	}
	if meaningfulchanges.TrackedKindForGroup("Secret", "") {
		t.Fatal("Secret issues must stay ineligible for no_recent_changes")
	}
	for _, tc := range []struct {
		group string
		want  bool
	}{{"", true}, {"example.io", false}} {
		if got := recentChangesSourceTracked(schema.GroupVersionKind{Kind: "Secret", Version: "v1", Group: tc.group}); got != tc.want {
			t.Fatalf("Secret group %q tracked=%v", tc.group, got)
		}
	}
}

func TestRolloutIssueCorrelatesReferencedSecret(t *testing.T) {
	client := fake.NewSimpleClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}})
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}
	rollout := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Rollout", "metadata": map[string]any{"name": "roll", "namespace": "shop"}, "spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "app", "env": []any{map[string]any{"name": "PASSWORD", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "db", "key": "password"}}}}}}}}}}}
	dynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "RolloutList"}, rollout)
	if err := k8s.InitTestDynamicResourceCache(dynamic, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "Rollout", Name: gvr.Resource, Namespaced: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	if err := k8s.GetDynamicResourceCache().EnsureWatching(gvr); err != nil {
		t.Fatal(err)
	}
	if !k8s.GetDynamicResourceCache().WaitForSync(gvr, 2*time.Second) {
		t.Fatal("Rollout cache did not sync")
	}
	store := initCorrelationStore(t)
	if err := store.Append(context.Background(), timeline.TimelineEvent{ID: "secret-write", Kind: "Secret", APIVersion: "v1", Name: "db", Namespace: "shop", Timestamp: time.Now().Add(-5 * time.Minute), ClusterContext: k8s.ActiveClusterContext(), Source: timeline.SourceInformer, EventType: timeline.EventTypeUpdate, Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "data (modified keys)", OldValue: []string{"password"}, NewValue: []string{"password"}}}}}); err != nil {
		t.Fatal(err)
	}
	result, _, obj, err := correlationChangesForIssue(context.Background(), &issuesapi.Issue{Kind: "Rollout", Namespace: "shop", Name: "roll"}, time.Hour, 10)
	if err != nil || obj == nil {
		t.Fatalf("Rollout object not resolved: obj=%v err=%v", obj, err)
	}
	if len(result.Changes) != 1 || result.Changes[0].Kind != "Secret" || result.Changes[0].Name != "db" {
		t.Fatalf("Rollout correlation changes = %+v", result.Changes)
	}
}

func TestRolloutWorkloadRefCorrelatesReferencedSecret(t *testing.T) {
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "api", Namespace: "shop"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Env: []corev1.EnvVar{{Name: "PASSWORD", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "db"}, Key: "password"}}}}}}}}}}
	client := fake.NewSimpleClientset(deployment, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shop"}})
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}
	rollout := func(name, target string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Rollout", "metadata": map[string]any{"name": name, "namespace": "shop"}, "spec": map[string]any{"workloadRef": map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": target}}}}
	}
	dynamic := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "RolloutList"}, rollout("ref", "api"), rollout("dangling", "missing"))
	if err := k8s.InitTestDynamicResourceCache(dynamic, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "Rollout", Name: gvr.Resource, Namespaced: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	if err := k8s.GetDynamicResourceCache().EnsureWatching(gvr); err != nil {
		t.Fatal(err)
	}
	if !k8s.GetDynamicResourceCache().WaitForSync(gvr, 2*time.Second) {
		t.Fatal("Rollout cache did not sync")
	}
	store := initCorrelationStore(t)
	if err := store.Append(context.Background(), timeline.TimelineEvent{ID: "secret-write", Kind: "Secret", APIVersion: "v1", Name: "db", Namespace: "shop", Timestamp: time.Now().Add(-5 * time.Minute), ClusterContext: k8s.ActiveClusterContext(), Source: timeline.SourceInformer, EventType: timeline.EventTypeUpdate, Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "data (modified keys)", OldValue: []string{"password"}, NewValue: []string{"password"}}}}}); err != nil {
		t.Fatal(err)
	}
	result, withheld, _, err := correlationChangesForIssue(context.Background(), &issuesapi.Issue{Kind: "Rollout", Namespace: "shop", Name: "ref"}, time.Hour, 10)
	if err != nil || withheld || len(result.Changes) != 1 || result.Changes[0].Name != "db" {
		t.Fatalf("workloadRef Rollout: changes=%+v withheld=%v err=%v", result.Changes, withheld, err)
	}
	// An unresolvable target leaves the sources unknown, so no absence claim.
	result, withheld, _, err = correlationChangesForIssue(context.Background(), &issuesapi.Issue{Kind: "Rollout", Namespace: "shop", Name: "dangling"}, time.Hour, 10)
	if err != nil || !withheld || len(result.Changes) != 0 {
		t.Fatalf("dangling workloadRef Rollout: changes=%+v withheld=%v err=%v", result.Changes, withheld, err)
	}
}
