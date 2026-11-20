package k8s_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
)

var flaggerCanaryGVR = schema.GroupVersionResource{Group: "flagger.app", Version: "v1beta1", Resource: "canaries"}
var flaggerTemplateGVR = schema.GroupVersionResource{Group: "flagger.app", Version: "v1beta1", Resource: "metrictemplates"}

func referenceCanary(name, template string) *unstructured.Unstructured {
	analysis := map[string]any{}
	if template != "" {
		analysis["metrics"] = []any{map[string]any{"name": "latency", "templateRef": map[string]any{"name": template, "namespace": "metrics"}}}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "flagger.app/v1beta1", "kind": "Canary", "metadata": map[string]any{"name": name, "namespace": "release"},
		"spec": map[string]any{"targetRef": map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "app"}, "analysis": analysis},
	}}
}

func referenceTemplate(name, secret string) *unstructured.Unstructured {
	provider := map[string]any{"type": "prometheus", "headers": map[string]any{"token": "private-provider-value"}}
	if secret != "" {
		provider["secretRef"] = map[string]any{"name": secret}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "flagger.app/v1beta1", "kind": "MetricTemplate", "metadata": map[string]any{"name": name, "namespace": "metrics"},
		"spec": map[string]any{"provider": provider, "query": "private-query-value"},
	}}
}

func initFlaggerReferenceCaches(t *testing.T, objects ...runtime.Object) *dynamicfake.FakeDynamicClient {
	t.Helper()
	zero := int32(0)
	client := fake.NewClientset(
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "release"}, Spec: appsv1.DeploymentSpec{Replicas: &zero}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credentials", Namespace: "metrics"}, Data: map[string][]byte{"token": []byte("private-secret-value")}},
	)
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	t.Cleanup(k8s.ResetTestDynamicState)
	dynamicClient := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{flaggerCanaryGVR: "CanaryList", flaggerTemplateGVR: "MetricTemplateList", {Group: "serving.knative.dev", Version: "v1", Resource: "services"}: "ServiceList"}, objects...)
	resources := []k8s.APIResource{
		{Group: "flagger.app", Version: "v1beta1", Kind: "Canary", Name: "canaries", Namespaced: true, Verbs: []string{"get", "list", "watch"}},
		{Group: "flagger.app", Version: "v1beta1", Kind: "MetricTemplate", Name: "metrictemplates", Namespaced: true, Verbs: []string{"get", "list", "watch"}},
		{Group: "serving.knative.dev", Version: "v1", Kind: "Service", Name: "services", Namespaced: true, Verbs: []string{"get", "list", "watch"}},
	}
	if err := k8s.InitTestDynamicResourceCache(dynamicClient, resources); err != nil {
		t.Fatal(err)
	}
	if err := k8s.GetDynamicResourceCache().EnsureWatching(flaggerCanaryGVR); err != nil || !k8s.GetDynamicResourceCache().WaitForSync(flaggerCanaryGVR, 2*time.Second) {
		t.Fatalf("Canary informer not ready: %v", err)
	}
	return dynamicClient
}

func watchFlaggerTemplates(t *testing.T) {
	t.Helper()
	cache := k8s.GetDynamicResourceCache()
	if err := cache.EnsureWatching(flaggerTemplateGVR); err != nil || !cache.WaitForSync(flaggerTemplateGVR, 2*time.Second) {
		t.Fatalf("MetricTemplate informer not ready: %v", err)
	}
}

func TestFlaggerMissingReferenceScenarios(t *testing.T) {
	missingTarget := referenceCanary("missing-target", "")
	_ = unstructured.SetNestedField(missingTarget.Object, "absent-app", "spec", "targetRef", "name")
	suspended := referenceCanary("suspended", "missing")
	_ = unstructured.SetNestedField(suspended.Object, true, "spec", "suspend")
	skipped := referenceCanary("skipped", "missing")
	_ = unstructured.SetNestedField(skipped.Object, true, "spec", "skipAnalysis")
	deleting := referenceCanary("deleting", "missing")
	now := metav1.Now()
	deleting.SetDeletionTimestamp(&now)
	deleting.SetFinalizers([]string{"test/retain"})
	duplicate := referenceCanary("duplicate", "missing")
	metrics, _, _ := unstructured.NestedSlice(duplicate.Object, "spec", "analysis", "metrics")
	_ = unstructured.SetNestedSlice(duplicate.Object, append(metrics, metrics[0]), "spec", "analysis", "metrics")
	initFlaggerReferenceCaches(t, missingTarget, suspended, skipped, deleting, duplicate,
		referenceCanary("missing-template", "missing"), referenceCanary("missing-secret", "private-provider"),
		referenceCanary("valid", "authenticated"), referenceCanary("zero-replicas", ""),
		referenceTemplate("private-provider", "absent-credentials"), referenceTemplate("authenticated", "credentials"))
	watchFlaggerTemplates(t)
	problems := k8s.DetectMissingCRDRefs(k8s.GetResourceCache(), k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery(), "release")
	want := map[string]string{
		"missing-target": "Missing Flagger target", "missing-template": "Missing Flagger MetricTemplate",
		"missing-secret": "Missing Flagger provider Secret", "duplicate": "Missing Flagger MetricTemplate",
	}
	if len(problems) != len(want) {
		t.Fatalf("got %d detections: %+v", len(problems), problems)
	}
	for _, problem := range problems {
		if problem.Reason != want[problem.Name] || problem.Severity != "warning" || !problem.OnsetUnknown || len(problem.RequiredReads) == 0 {
			t.Fatalf("bad finding: %+v", problem)
		}
		if problem.Name == "missing-secret" {
			if len(problem.RequiredReads) != 2 || problem.RequiredReads[0].Resource != "secrets" || problem.RequiredReads[0].Namespace != "metrics" {
				t.Fatalf("wrong credential evidence scope: %+v", problem.RequiredReads)
			}
		}
	}
	raw, _ := json.Marshal(problems)
	if strings.Contains(string(raw), "private-provider-value") || strings.Contains(string(raw), "private-query-value") || strings.Contains(string(raw), "private-secret-value") {
		t.Fatalf("provider payload leaked: %s", raw)
	}
}

func TestFlaggerUnobservedTemplateDoesNotBecomeMissingOrStartAWatch(t *testing.T) {
	client := initFlaggerReferenceCaches(t, referenceCanary("release", "unobserved"))
	before := templateListActions(client)
	got := k8s.DetectMissingCRDRefs(k8s.GetResourceCache(), k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery(), "release")
	if len(got) != 0 || templateListActions(client) != before {
		t.Fatalf("unobserved target produced findings or API calls: %+v", got)
	}
}

func TestFlaggerEvidenceIsWithheldWhenDependencyCannotBeRead(t *testing.T) {
	initFlaggerReferenceCaches(t, referenceCanary("release", "private-provider"), referenceTemplate("private-provider", "absent-credentials"))
	watchFlaggerTemplates(t)
	provider := issues.NewCacheProvider()
	for _, denied := range []string{"", "secrets", "metrictemplates"} {
		got := issues.Compose(provider, issues.Filters{
			Namespaces:      []string{"release"},
			CanReadSubject:  func(issues.Ref) bool { return true },
			CanReadEvidence: func(read issues.EvidenceRead) bool { return denied == "" || read.Resource != denied },
		})
		found := false
		for _, issue := range got {
			if issue.Reason == "Missing Flagger provider Secret" {
				found = true
			}
		}
		if found != (denied == "") {
			t.Fatalf("denied=%q; finding present=%v; issues=%+v", denied, found, got)
		}
	}
}

func TestFlaggerCrossNamespaceCacheCoverage(t *testing.T) {
	client := initFlaggerReferenceCaches(t, referenceCanary("release", "absent"))
	client.PrependReactor("list", "metrictemplates", func(action clienttesting.Action) (bool, runtime.Object, error) {
		if action.GetNamespace() == "release" {
			return false, nil, nil
		}
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "flagger.app", Resource: "metrictemplates"}, "", nil)
	})
	cache := k8s.GetDynamicResourceCache()
	if err := cache.EnsureWatchingContext(context.Background(), flaggerTemplateGVR, "release"); err != nil || !cache.WaitForSync(flaggerTemplateGVR, 2*time.Second) {
		t.Fatalf("namespace-scoped informer not ready: %v", err)
	}
	if !cache.HasCoveringInformer(flaggerTemplateGVR, "release") || cache.HasCoveringInformer(flaggerTemplateGVR, "metrics") {
		t.Fatal("fixture did not establish divergent namespace coverage")
	}
	before := templateListActions(client)
	got := k8s.DetectMissingCRDRefs(k8s.GetResourceCache(), k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery(), "release")
	if len(got) != 0 || templateListActions(client) != before {
		t.Fatalf("uncovered namespace was called absent or probed: %+v", got)
	}
}

func templateListActions(client *dynamicfake.FakeDynamicClient) int {
	count := 0
	for _, action := range client.Actions() {
		if action.GetVerb() == "list" && action.GetResource() == flaggerTemplateGVR {
			count++
		}
	}
	return count
}

func TestFlaggerNativeTargetKindsAndDeprecatedAnalysis(t *testing.T) {
	ds := referenceCanary("daemonset", "")
	_ = unstructured.SetNestedField(ds.Object, "DaemonSet", "spec", "targetRef", "kind")
	service := referenceCanary("core-service", "")
	_ = unstructured.SetNestedField(service.Object, "Service", "spec", "targetRef", "kind")
	_ = unstructured.SetNestedField(service.Object, "v1", "spec", "targetRef", "apiVersion")
	knative := service.DeepCopy()
	knative.SetName("knative")
	_ = unstructured.SetNestedField(knative.Object, "serving.knative.dev/v1", "spec", "targetRef", "apiVersion")
	deprecated := referenceCanary("deprecated-analysis", "absent")
	analysis, _, _ := unstructured.NestedMap(deprecated.Object, "spec", "analysis")
	unstructured.RemoveNestedField(deprecated.Object, "spec", "analysis")
	_ = unstructured.SetNestedMap(deprecated.Object, analysis, "spec", "canaryAnalysis")
	deleting := referenceTemplate("deleting", "absent-credentials")
	now := metav1.Now()
	deleting.SetDeletionTimestamp(&now)
	deleting.SetFinalizers([]string{"test/retain"})
	initFlaggerReferenceCaches(t, ds, service, knative, deprecated, deleting, referenceCanary("deleting-template", "deleting"))
	watchFlaggerTemplates(t)
	before := k8s.DetectMissingCRDRefs(k8s.GetResourceCache(), k8s.GetDynamicResourceCache(), k8s.GetResourceDiscovery(), "release")
	if len(before) != 3 {
		t.Fatalf("unobserved Knative/deleting template became missing: %+v", before)
	}
	gvr := schema.GroupVersionResource{Group: "serving.knative.dev", Version: "v1", Resource: "services"}
	cache := k8s.GetDynamicResourceCache()
	if err := cache.EnsureWatching(gvr); err != nil || !cache.WaitForSync(gvr, 2*time.Second) {
		t.Fatalf("Knative informer not ready: %v", err)
	}
	got := k8s.DetectMissingCRDRefs(k8s.GetResourceCache(), cache, k8s.GetResourceDiscovery(), "release")
	if len(got) != 4 {
		t.Fatalf("target kinds/deprecated refs not checked: %+v", got)
	}
	for _, finding := range got {
		if finding.Name == "knative" && (!strings.Contains(finding.Message, "Knative Service") || !strings.Contains(finding.Action, "Knative Service")) {
			t.Fatalf("core/Knative identity ambiguous: %+v", finding)
		}
	}
}
