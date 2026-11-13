package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/logging"
)

func runningPod(ns, name string, labels map[string]string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
}

func owned(kind, name string) []metav1.OwnerReference {
	yes := true
	return []metav1.OwnerReference{{APIVersion: logging.Group + "/v1beta1", Kind: kind, Name: name, Controller: &yes}}
}

// seedPipelineFixture: Logging "default" (control namespace logging-system)
// with a fluent-bit DaemonSet and a fluentd StatefulSet, ClusterFlow optin
// (label logs=on) → ClusterOutput loki, plus the given pods.
func seedPipelineFixture(t *testing.T, pods ...runtime.Object) {
	t.Helper()
	restoreFixtureCache(t)
	k8s.ResetResourceCache()
	objs := append([]runtime.Object{
		&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: "logging-system", Name: "default-fluentbit", OwnerReferences: owned("FluentbitAgent", "default")},
			Status: appsv1.DaemonSetStatus{NumberReady: 2, DesiredNumberScheduled: 3}},
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Namespace: "logging-system", Name: "default-fluentd", OwnerReferences: owned("Logging", "default")},
			Status: appsv1.StatefulSetStatus{ReadyReplicas: 1, Replicas: 1}},
	}, pods...)
	if err := k8s.InitTestResourceCache(fake.NewClientset(objs...)); err != nil {
		t.Fatal(err)
	}
	kinds := append(loggingKinds(), k8s.APIResource{Group: logging.Group, Version: "v1beta1", Name: "fluentbitagents", Kind: "FluentbitAgent", Verbs: []string{"get", "list", "watch"}})
	seedCNPGWorkspace(t, kinds,
		lgObj("Logging", "", "default", map[string]any{"controlNamespace": "logging-system"}, nil),
		lgObj("FluentbitAgent", "", "default", map[string]any{}, nil),
		lgObj("ClusterFlow", "logging-system", "optin", map[string]any{
			"match":            []any{map[string]any{"select": map[string]any{"labels": map[string]any{"logs": "on"}}}},
			"globalOutputRefs": []any{"loki"},
		}, map[string]any{"active": true}),
		lgObj("ClusterOutput", "logging-system", "loki", map[string]any{"loki": map[string]any{"url": "https://user:pw@loki.example/push", "password": map[string]any{"value": "s3cret"}}}, map[string]any{"active": true}),
	)
}

func readPipeline(t *testing.T, resp *http.Response) (logPipelineResponse, string) {
	t.Helper()
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	var out logPipelineResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out, string(raw)
}

func pipelinePods() []runtime.Object {
	pending := runningPod("team", "starting", map[string]string{"logs": "on"})
	pending.Status.Phase = corev1.PodPending
	return []runtime.Object{
		runningPod("team", "a1", map[string]string{"logs": "on"}),
		runningPod("team", "a2", map[string]string{"logs": "on"}),
		runningPod("team", "b1", nil),
		runningPod("ops", "o1", map[string]string{"logs": "on"}),
		pending,
	}
}

func TestLoggingPipeline(t *testing.T) {
	seedPipelineFixture(t, pipelinePods()...)
	got, raw := readPipeline(t, mustGet(t, testServer.URL+"/api/logging/pipeline"))
	if len(got.Providers) != 1 || got.Providers[0].Provider != "logging-operator" {
		t.Fatalf("providers = %+v", got.Providers)
	}
	p := got.Providers[0]
	if len(p.Instances) != 1 || len(p.Instances[0].Collectors) != 1 || p.Instances[0].Collectors[0].Name != "default-fluentbit" || p.Instances[0].Collectors[0].Ready != 2 || p.Instances[0].Collectors[0].Desired != 3 ||
		len(p.Instances[0].Aggregators) != 1 || p.Instances[0].Aggregators[0].Name != "default-fluentd" {
		t.Errorf("instances = %+v", p.Instances)
	}
	if len(p.Routes) != 1 || p.Routes[0].Name != "optin" || p.Routes[0].SelectedPods != (logPodCount{Count: 3}) || len(p.Routes[0].Destinations) != 1 || !p.Routes[0].Destinations[0].Found {
		t.Errorf("routes = %+v", p.Routes)
	}
	if len(p.Destinations) != 1 || p.Destinations[0].Address != "https://loki.example/push" || p.Destinations[0].Type != "loki" {
		t.Errorf("destinations = %+v", p.Destinations)
	}
	if got.Unrouted.Count != 1 || got.Unrouted.LowerBound || len(got.Unrouted.Pods) != 1 || got.Unrouted.Pods[0].Name != "b1" {
		t.Errorf("unrouted = %+v", got.Unrouted)
	}
	for _, leak := range []string{"s3cret", "user:pw"} {
		if strings.Contains(raw, leak) {
			t.Errorf("body leaks %q", leak)
		}
	}
}

func TestLoggingPipelineNotInstalled(t *testing.T) {
	seedLoggingFixture(t, nil, nil)
	got, raw := readPipeline(t, mustGet(t, testServer.URL+"/api/logging/pipeline"))
	if len(got.Providers) != 0 || !strings.Contains(raw, `"providers":[]`) {
		t.Fatalf("response = %s", raw)
	}
}

// allowPipeline grants every read the pipeline makes except the denied ones.
func allowPipeline(namespaces []string, deny ...string) *auth.UserPermissions {
	perms := &auth.UserPermissions{AllowedNamespaces: namespaces}
	denied := map[string]bool{}
	for _, d := range deny {
		denied[d] = true
	}
	set := func(group, resource, ns string) {
		perms.SetCanI("list", group, resource, ns, !denied[resource+"@"+ns])
	}
	set(logging.Group, "loggings", "")
	for _, r := range []string{"clusterflows", "clusteroutputs", "flows", "outputs"} {
		set(logging.Group, r, "logging-system")
		for _, ns := range namespaces {
			set(logging.Group, r, ns)
		}
	}
	set("apps", "daemonsets", "logging-system")
	set("apps", "statefulsets", "logging-system")
	return perms
}

func TestLoggingPipelineRequiresListLoggings(t *testing.T) {
	seedPipelineFixture(t, pipelinePods()...)
	env := newAuthTestServer(t)
	env.srv.permCache.Set("alice", nil, allowPipeline([]string{"team"}, "loggings@"))
	resp := env.authGet(t, "/api/logging/pipeline", "alice", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("without list loggings: %d, want 403", resp.StatusCode)
	}
}

func TestLoggingPipelineWithholdsUnlistableFlows(t *testing.T) {
	seedPipelineFixture(t, pipelinePods()...)
	env := newAuthTestServer(t)
	env.srv.permCache.Set("bob", nil, allowPipeline([]string{"team", "ops"}, "clusterflows@logging-system"))
	got, raw := readPipeline(t, env.authGet(t, "/api/logging/pipeline", "bob", ""))
	if len(got.Providers) != 1 || len(got.Providers[0].Routes) != 0 || strings.Contains(raw, "optin") {
		t.Fatalf("routes the caller can't list leaked: %s", raw)
	}
	if !hasGap(got, "clusterflows") || !got.Unrouted.LowerBound {
		t.Fatalf("withheld ClusterFlows must be a gap and make unrouted a lower bound: %+v", got)
	}
}

func TestLoggingPipelinePodCountsFollowCallerNamespaces(t *testing.T) {
	seedPipelineFixture(t, pipelinePods()...)
	env := newAuthTestServer(t)
	env.srv.permCache.Set("carol", nil, allowPipeline([]string{"team"}))
	got, raw := readPipeline(t, env.authGet(t, "/api/logging/pipeline", "carol", ""))
	r := got.Providers[0].Routes
	if len(r) != 1 || r[0].SelectedPods != (logPodCount{Count: 2, LowerBound: true}) || strings.Contains(raw, `"o1"`) {
		t.Fatalf("counts must cover only the caller's namespaces and say so: %s", raw)
	}
	if got.Unrouted.Count != 1 || !got.Unrouted.LowerBound {
		t.Fatalf("unrouted = %+v", got.Unrouted)
	}
}

func TestLoggingPipelineCapsUnrouted(t *testing.T) {
	var pods []runtime.Object
	for i := 0; i < 501; i++ {
		pods = append(pods, runningPod("team", fmt.Sprintf("p%03d", i), nil))
	}
	seedPipelineFixture(t, pods...)
	got, _ := readPipeline(t, mustGet(t, testServer.URL+"/api/logging/pipeline"))
	if got.Unrouted.Count != 501 || len(got.Unrouted.Pods) != 500 || !got.Unrouted.Truncated {
		t.Fatalf("unrouted = count %d, listed %d, truncated %v", got.Unrouted.Count, len(got.Unrouted.Pods), got.Unrouted.Truncated)
	}
}

func TestLoggingPipelineSharedListener(t *testing.T) {
	seedPipelineFixture(t)
	req, _ := http.NewRequest("GET", testServer.URL+"/api/logging/pipeline", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("cross-site GET = %d, want 403", resp.StatusCode)
	}
}

func hasGap(r logPipelineResponse, resource string) bool {
	for _, g := range r.Coverage.Gaps {
		if g.Resource == resource {
			return true
		}
	}
	return false
}

func mustGet(t *testing.T, url string) *http.Response {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}
