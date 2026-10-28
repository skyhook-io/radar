package k8s

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func TestAutoInstrumentationContextResolution(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "opentelemetry.io", Version: "v1alpha1", Resource: "instrumentations"}
	for _, tc := range []struct {
		name, podValue, namespaceValue, resolution, refNamespace string
		count                                                    int
		watch, deny, namespaceDenied                             bool
	}{
		{"named present", "shared/java", "", "resolved", "shared", 1, true, false, false},
		{"named absent", "shared/absent", "", "missing", "shared", 1, true, false, false},
		{"not watched", "shared/absent", "", "unknown", "shared", 1, false, false, false},
		{"denied target", "shared/absent", "", "unknown", "shared", 1, true, true, false},
		{"true inherits name", "true", "shared/java", "resolved", "shared", 1, true, false, false},
		{"true hidden namespace", "true", "shared/java", "unknown", "", 1, true, false, true},
		{"false overrides", "false", "shared/java", "disabled", "", 1, true, false, false},
		{"default one", "true", "", "default_resolved", "app", 1, true, false, false},
		{"default inventory denied", "true", "", "unknown", "", 1, true, true, false},
		{"default none", "true", "", "default_missing", "", 0, true, false, false},
		{"default ambiguous", "true", "", "default_ambiguous", "", 2, true, false, false},
		{"invalid named", "shared/a/b", "", "invalid_reference", "", 1, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(ResetTestState)
			t.Cleanup(ResetTestDynamicState)
			ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "app", Annotations: map[string]string{instrumentationAnnotationPrefix + "java": tc.namespaceValue}}}
			if err := InitTestResourceCache(fake.NewClientset(ns)); err != nil {
				t.Fatal(err)
			}
			var objects []runtime.Object
			for n := 0; n < tc.count; n++ {
				name := "java"
				if n > 0 {
					name = "second"
				}
				for _, namespace := range []string{"app", "shared"} {
					objects = append(objects, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "opentelemetry.io/v1alpha1", "kind": "Instrumentation", "metadata": map[string]any{"name": name, "namespace": namespace}}})
				}
			}
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "InstrumentationList"}, objects...)
			if err := InitTestDynamicResourceCache(dyn, []APIResource{{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: "Instrumentation", Namespaced: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
				t.Fatal(err)
			}
			if tc.watch {
				cache := GetDynamicResourceCache()
				if err := cache.EnsureWatching(gvr); err != nil || !cache.WaitForSync(gvr, time.Second) {
					t.Fatalf("not synced: %v", err)
				}
			}
			before := instrumentationReadActions(dyn)
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "app-0", Namespace: "app", Annotations: map[string]string{instrumentationAnnotationPrefix + "java": tc.podValue}}}
			got, unavailable := AutoInstrumentationForObject(pod, func(group, resource, namespace, verb string) bool {
				return !(tc.deny && resource == "instrumentations" || tc.namespaceDenied && resource == "namespaces")
			})
			if unavailable || got == nil || len(got.RequestsIfPodCreatedNow) != 1 || string(got.RequestsIfPodCreatedNow[0].Resolution) != tc.resolution {
				t.Fatalf("wrong resolution: %+v unavailable=%v", got, unavailable)
			}
			r := got.RequestsIfPodCreatedNow[0]
			if tc.refNamespace != "" && (r.Instrumentation == nil || r.Instrumentation.Namespace != tc.refNamespace) {
				t.Fatalf("wrong namespace: %+v", r)
			}
			if tc.name == "default inventory denied" && (!r.AssumesOperatorCRDMode || r.Instrumentation != nil || got.NamespaceDefaultsUnknown) {
				t.Fatalf("unreadable default inventory lost its distinct qualifiers: %+v", got)
			}
			if tc.name == "true inherits name" && r.Source.Kind != "Namespace" {
				t.Fatalf("lost native inheritance: %+v", r)
			}
			if len(tc.resolution) >= 8 && tc.resolution[:8] == "default_" && !r.AssumesOperatorCRDMode {
				t.Fatal("default selection lost inline-config caveat")
			}
			if instrumentationReadActions(dyn) != before {
				t.Fatal("context probe started a watch or fetched objects")
			}
		})
	}
}

func TestAutoInstrumentationWorkloadAnnotationPlacement(t *testing.T) {
	t.Cleanup(ResetTestState)
	if err := InitTestResourceCache(fake.NewClientset()); err != nil {
		t.Fatal(err)
	}
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "app", Annotations: map[string]string{instrumentationAnnotationPrefix + "java": "named"}}}
	got, _ := AutoInstrumentationForObject(deployment, func(string, string, string, string) bool { return false })
	if got == nil || len(got.IgnoredWorkloadMetadataAnnotations) != 1 || len(got.RequestsIfPodCreatedNow) != 0 {
		t.Fatalf("metadata annotation treated as Pod annotation: %+v", got)
	}
}

// Watch setup can finish recording its Watch action after initial list sync;
// count only reads a context computation could incorrectly initiate.
func instrumentationReadActions(client *dynamicfake.FakeDynamicClient) int {
	count := 0
	for _, action := range client.Actions() {
		if action.GetVerb() == "get" || action.GetVerb() == "list" {
			count++
		}
	}
	return count
}

func initInstrumentationContextFixture(t *testing.T) {
	t.Helper()
	t.Cleanup(ResetTestState)
	t.Cleanup(ResetTestDynamicState)
	if err := InitTestResourceCache(fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "app"}})); err != nil {
		t.Fatal(err)
	}
	gvr := schema.GroupVersionResource{Group: "opentelemetry.io", Version: "v1alpha1", Resource: "instrumentations"}
	object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "opentelemetry.io/v1alpha1", "kind": "Instrumentation", "metadata": map[string]any{"name": "java", "namespace": "app"}}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "InstrumentationList"}, object)
	if err := InitTestDynamicResourceCache(dyn, []APIResource{{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: "Instrumentation", Namespaced: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	cache := GetDynamicResourceCache()
	if err := cache.EnsureWatching(gvr); err != nil || !cache.WaitForSync(gvr, time.Second) {
		t.Fatalf("instrumentation cache not ready: %v", err)
	}
}

func TestInstrumentationTemplatePathsAndListOnlyDefaultSelection(t *testing.T) {
	initInstrumentationContextFixture(t)
	annotation := map[string]string{instrumentationAnnotationPrefix + "java": "java"}
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "app"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Annotations: annotation}}}}
	cronJob := &batchv1.CronJob{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "app"}, Spec: batchv1.CronJobSpec{JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: deployment.Spec.Template}}}}
	rollout := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Rollout", "metadata": map[string]any{"name": "app", "namespace": "app"}, "spec": map[string]any{"template": map[string]any{"metadata": map[string]any{"annotations": map[string]any{instrumentationAnnotationPrefix + "java": "java"}}, "spec": map[string]any{}}}}}
	for _, tc := range []struct {
		object runtime.Object
		path   string
	}{{deployment, "/spec/template/metadata/annotations/"}, {cronJob, "/spec/jobTemplate/spec/template/metadata/annotations/"}, {rollout, "/spec/template/metadata/annotations/"}} {
		got, unavailable := AutoInstrumentationForObject(tc.object, func(string, string, string, string) bool { return true })
		if unavailable || got == nil || len(got.RequestsIfPodCreatedNow) != 1 || got.RequestsIfPodCreatedNow[0].AnnotationPath != tc.path+"instrumentation.opentelemetry.io~1inject-java" {
			t.Fatalf("wrong prospective annotation source: %+v", got)
		}
	}
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "app", Annotations: map[string]string{instrumentationAnnotationPrefix + "java": "true"}}}
	got, _ := AutoInstrumentationForObject(pod, func(_, resource, _, verb string) bool { return resource != "instrumentations" || verb == "list" })
	if got == nil || len(got.RequestsIfPodCreatedNow) != 1 || got.RequestsIfPodCreatedNow[0].Resolution != "default_resolved" || got.RequestsIfPodCreatedNow[0].Instrumentation == nil {
		t.Fatalf("native List-selected identity was hidden behind an unrelated Get check: %+v", got)
	}
}

func TestInstrumentationHiddenNamespaceAndConnectionReplacement(t *testing.T) {
	initInstrumentationContextFixture(t)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "app"}}
	got, unavailable := AutoInstrumentationForObject(pod, func(string, string, string, string) bool { return false })
	if unavailable || got == nil || !got.NamespaceDefaultsUnknown || len(got.RequestsIfPodCreatedNow) != 0 {
		t.Fatalf("hidden defaults became no instrumentation: %+v unavailable=%v", got, unavailable)
	}
	pod.Annotations = map[string]string{instrumentationAnnotationPrefix + "java": "java"}
	got, unavailable = AutoInstrumentationForObject(pod, func(_, resource, _, _ string) bool {
		if resource == "namespaces" {
			ResetResourceCache()
		}
		return false
	})
	if got != nil || !unavailable {
		t.Fatalf("mixed connection observation survived: %+v unavailable=%v", got, unavailable)
	}
}

func TestInstrumentationNamespaceReadUsesSubjectNamespace(t *testing.T) {
	initInstrumentationContextFixture(t)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "app"}}
	var namespaceChecks int
	canRead := func(group, resource, namespace, verb string) bool {
		if resource == "namespaces" {
			namespaceChecks++
			if group != "" || namespace != "app" || verb != "get" {
				t.Errorf("Namespace GET used wrong request attributes: %q %q %q %q", group, resource, namespace, verb)
				return false
			}
			return true
		}
		return group == "opentelemetry.io" && resource == "instrumentations" && namespace == "app" && verb == "list"
	}
	if got, unavailable := AutoInstrumentationForObject(pod, canRead); unavailable || got != nil {
		t.Fatalf("readable empty defaults added unrelated uncertainty: %+v unavailable=%v", got, unavailable)
	}
	pod.Annotations = map[string]string{instrumentationAnnotationPrefix + "java": "true"}
	got, unavailable := AutoInstrumentationForObject(pod, canRead)
	if unavailable || got == nil || got.NamespaceDefaultsUnknown || len(got.RequestsIfPodCreatedNow) != 1 || got.RequestsIfPodCreatedNow[0].Resolution != "default_resolved" {
		t.Fatalf("Namespace-scoped read did not resolve default selection: %+v unavailable=%v", got, unavailable)
	}
	if namespaceChecks != 2 {
		t.Fatalf("expected a Namespace authorization check per observation, got%d", namespaceChecks)
	}
}

func TestInstrumentationLanguagesResolveReferencesIndependently(t *testing.T) {
	initInstrumentationContextFixture(t)
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "app", Annotations: map[string]string{
		instrumentationAnnotationPrefix + "java":   "java",
		instrumentationAnnotationPrefix + "python": "missing",
	}}}
	got, unavailable := AutoInstrumentationForObject(pod, func(string, string, string, string) bool { return true })
	if unavailable || got == nil || len(got.RequestsIfPodCreatedNow) != 2 {
		t.Fatalf("lost independent reference evidence: %+v unavailable=%v", got, unavailable)
	}
	java, python := got.RequestsIfPodCreatedNow[0], got.RequestsIfPodCreatedNow[1]
	if java.Language != "java" || java.Resolution != "resolved" || python.Language != "python" || python.Resolution != "missing" {
		t.Fatalf("reference lookup was replaced with an admission verdict: %+v", got)
	}
}
