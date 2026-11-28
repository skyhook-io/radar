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

func loggingRunningPod(ns, name string, labels map[string]string) *corev1.Pod {
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
	seedPipelineFixtureWith(t, map[string]any{"controlNamespace": "logging-system"}, nil, []runtime.Object{
		&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: "logging-system", Name: "default-fluentbit", OwnerReferences: owned("FluentbitAgent", "default")},
			Status: appsv1.DaemonSetStatus{NumberReady: 2, DesiredNumberScheduled: 3}},
		&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Namespace: "logging-system", Name: "default-fluentd", OwnerReferences: owned("Logging", "default")},
			Status: appsv1.StatefulSetStatus{ReadyReplicas: 1, Replicas: 1}},
	}, nil, pods...)
}

func seedPipelineFixtureWith(t *testing.T, loggingSpec, loggingStatus map[string]any, workloads, loggingObjs []runtime.Object, pods ...runtime.Object) {
	t.Helper()
	restoreFixtureCache(t)
	k8s.ResetResourceCache()
	objs := append([]runtime.Object{
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "logging-system"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "ops"}},
	}, append(workloads, pods...)...)
	if err := k8s.InitTestResourceCache(fake.NewClientset(objs...)); err != nil {
		t.Fatal(err)
	}
	kinds := append(loggingKinds(), k8s.APIResource{Group: logging.Group, Version: "v1beta1", Name: "fluentbitagents", Kind: "FluentbitAgent", Verbs: []string{"get", "list", "watch"}})
	seedCNPGWorkspace(t, kinds, append(loggingObjs,
		lgObj("Logging", "", "default", loggingSpec, loggingStatus),
		lgObj("FluentbitAgent", "", "default", map[string]any{}, nil),
		lgObj("ClusterFlow", "logging-system", "optin", map[string]any{
			"match":            []any{map[string]any{"select": map[string]any{"labels": map[string]any{"logs": "on"}}}},
			"globalOutputRefs": []any{"loki"},
		}, map[string]any{"active": true}),
		lgObj("ClusterOutput", "logging-system", "loki", map[string]any{"loki": map[string]any{"url": "https://user:pw@loki.example/push", "password": map[string]any{"value": "s3cret"}}}, map[string]any{"active": true}),
	)...)
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
	pending := loggingRunningPod("team", "starting", map[string]string{"logs": "on"})
	pending.Status.Phase = corev1.PodPending
	return []runtime.Object{
		loggingRunningPod("team", "a1", map[string]string{"logs": "on"}),
		loggingRunningPod("team", "a2", map[string]string{"logs": "on"}),
		loggingRunningPod("team", "b1", nil),
		loggingRunningPod("ops", "o1", map[string]string{"logs": "on"}),
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
	if len(p.Routes) != 1 || p.Routes[0].Name != "optin" || p.Routes[0].Instance != "default" || p.Routes[0].SelectedPods != (logPodCount{Count: 3}) || len(p.Routes[0].Destinations) != 1 || !p.Routes[0].Destinations[0].Found {
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
	set(logging.Group, "fluentbitagents", "")
	for _, r := range []string{"clusterflows", "clusteroutputs", "flows", "outputs"} {
		set(logging.Group, r, "logging-system")
		for _, ns := range namespaces {
			set(logging.Group, r, ns)
		}
	}
	set("apps", "daemonsets", "logging-system")
	set("apps", "statefulsets", "logging-system")
	for _, ns := range namespaces {
		set("", "pods", ns)
	}
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
	// A withheld ClusterFlow may route any pod: which pods are unrouted is
	// unknown, so no count or pod is offered as unrouted.
	if !hasGap(got, "clusterflows") || !got.Unrouted.LowerBound || got.Unrouted.Count != 0 || len(got.Unrouted.Pods) != 0 {
		t.Fatalf("withheld ClusterFlows must leave unrouted unknown: %+v", got.Unrouted)
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
		pods = append(pods, loggingRunningPod("team", fmt.Sprintf("p%03d", i), nil))
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

// Namespace access can come from a deployments sentinel; pod names still need
// list pods in that namespace.
func TestLoggingPipelineNeedsListPods(t *testing.T) {
	seedPipelineFixture(t, pipelinePods()...)
	env := newAuthTestServer(t)
	env.srv.permCache.Set("dave", nil, allowPipeline([]string{"team", "ops"}, "pods@ops"))
	got, raw := readPipeline(t, env.authGet(t, "/api/logging/pipeline", "dave", ""))
	if strings.Contains(raw, `"o1"`) || !hasGap(got, "pods") {
		t.Fatalf("pods the caller can't list leaked or went unreported: %s", raw)
	}
	if r := got.Providers[0].Routes; len(r) != 1 || r[0].SelectedPods != (logPodCount{Count: 2, LowerBound: true}) {
		t.Fatalf("routes = %+v", r)
	}
}

// A collector the caller can't list is a gap, never "None found".
func TestLoggingPipelineUnlistableWorkloadsAreAGap(t *testing.T) {
	seedPipelineFixture(t, pipelinePods()...)
	env := newAuthTestServer(t)
	env.srv.permCache.Set("erin", nil, allowPipeline([]string{"team", "ops"}, "daemonsets@logging-system"))
	got, raw := readPipeline(t, env.authGet(t, "/api/logging/pipeline", "erin", ""))
	if !hasGap(got, "daemonsets") || strings.Contains(raw, "default-fluentbit") {
		t.Fatalf("response = %s", raw)
	}
}

func TestPipelineCandidateWithoutLoggings(t *testing.T) {
	f := logging.Flow{Namespace: "logging-system", Name: "optin", Cluster: true}
	if c := pipelineCandidate(f, nil, false); !c.Ignored {
		t.Errorf("read, no Logging counts it: %+v, want ignored", c)
	}
	if c := pipelineCandidate(f, nil, true); c.Ignored || !c.OnlyMaybe {
		t.Errorf("Loggings unread: %+v, want not ignored and only maybe", c)
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

// An unread Flow only hides its own namespace's pods: those can't be called
// unrouted, while pods elsewhere still are.
func TestLoggingPipelineUnreadFlowsHideOnlyTheirNamespace(t *testing.T) {
	seedPipelineFixture(t, append(pipelinePods(), loggingRunningPod("ops", "o2", nil))...)
	env := newAuthTestServer(t)
	env.srv.permCache.Set("dave", nil, allowPipeline([]string{"team", "ops"}, "flows@ops"))
	got, _ := readPipeline(t, env.authGet(t, "/api/logging/pipeline", "dave", ""))
	if !got.Unrouted.LowerBound || got.Unrouted.Count == 0 {
		t.Fatalf("unrouted = %+v", got.Unrouted)
	}
	for _, p := range got.Unrouted.Pods {
		if p.Namespace == "ops" {
			t.Fatalf("a pod an unread Flow may route was listed as unrouted: %+v", got.Unrouted)
		}
	}
}

// Whether a Logging's collectors were read is told per Logging: an unread
// namespace of another Logging doesn't make this one's empty list "not read".
func TestLoggingPipelineInstanceSaysWhatItRead(t *testing.T) {
	seedPipelineFixture(t, pipelinePods()...)
	env := newAuthTestServer(t)
	env.srv.permCache.Set("erin", nil, allowPipeline([]string{"team"}, "daemonsets@logging-system"))
	got, _ := readPipeline(t, env.authGet(t, "/api/logging/pipeline", "erin", ""))
	in := got.Providers[0].Instances[0]
	if !in.CollectorsUnread || in.AggregatorsUnread {
		t.Fatalf("instance = %+v", in)
	}
}

// Collectors run in fluentBitAgentNamespace, and a detached FluentdConfig owns
// the aggregator.
func TestLoggingPipelineAgentNamespaceAndDetachedAggregator(t *testing.T) {
	seedPipelineFixtureWith(t,
		map[string]any{"controlNamespace": "logging-system", "fluentBitAgentNamespace": "logging-agents"},
		map[string]any{"fluentdConfigName": "default-fluentd"},
		[]runtime.Object{
			&appsv1.DaemonSet{ObjectMeta: metav1.ObjectMeta{Namespace: "logging-agents", Name: "default-fluentbit", OwnerReferences: owned("FluentbitAgent", "default")}},
			&appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Namespace: "logging-system", Name: "default-fluentd", OwnerReferences: owned("FluentdConfig", "default-fluentd")}},
		}, nil, pipelinePods()...)
	got, _ := readPipeline(t, mustGet(t, testServer.URL+"/api/logging/pipeline"))
	i := got.Providers[0].Instances[0]
	if len(i.Collectors) != 1 || i.Collectors[0].Namespace != "logging-agents" || i.CollectorNamespace != "logging-agents" || len(i.Aggregators) != 1 || i.Aggregators[0].Name != "default-fluentd" {
		t.Fatalf("instance = %+v", i)
	}
}

// The defaultFlow receives what no Flow selects: those pods are routed, and it
// shows as a route with their count.
func TestLoggingPipelineDefaultFlow(t *testing.T) {
	seedPipelineFixtureWith(t, map[string]any{"controlNamespace": "logging-system", "defaultFlow": map[string]any{"globalOutputRefs": []any{"loki"}}}, nil, nil, nil, pipelinePods()...)
	got, _ := readPipeline(t, mustGet(t, testServer.URL+"/api/logging/pipeline"))
	if got.Unrouted.Count != 0 || len(got.Unrouted.Pods) != 0 {
		t.Errorf("unrouted = %+v", got.Unrouted)
	}
	var fb *logPipelineRoute
	for i, r := range got.Providers[0].Routes {
		if r.Fallback {
			fb = &got.Providers[0].Routes[i]
		}
	}
	if fb == nil || fb.Kind != "Logging" || fb.Instance != "default" || fb.SelectedPods != (logPodCount{Count: 1}) || len(fb.Destinations) != 1 || !fb.Destinations[0].Found {
		t.Fatalf("default flow route = %+v", fb)
	}
}

// A Flow naming an output its Logging doesn't have is invalid: the operator
// can't render it, so it routes nothing and its pods stay unrouted. With
// skipInvalidResources off (the default) the operator renders none of that
// Logging's configuration, so its other routes' counts are unknown.
func TestLoggingPipelineMissingOutputRejectsFlow(t *testing.T) {
	broken := lgObj("Flow", "team", "broken", map[string]any{"localOutputRefs": []any{"gone"}}, map[string]any{"active": true})
	for _, c := range []struct {
		name    string
		spec    map[string]any
		blocked bool
		optin   logPodCount
	}{
		{"skipInvalidResources off", map[string]any{"controlNamespace": "logging-system"}, true, logPodCount{Count: 0, LowerBound: true}},
		{"skipInvalidResources on", map[string]any{"controlNamespace": "logging-system", "skipInvalidResources": true}, false, logPodCount{Count: 3}},
	} {
		t.Run(c.name, func(t *testing.T) {
			seedPipelineFixtureWith(t, c.spec, nil, nil, []runtime.Object{broken}, pipelinePods()...)
			got, _ := readPipeline(t, mustGet(t, testServer.URL+"/api/logging/pipeline"))
			var r, optin logPipelineRoute
			for _, x := range got.Providers[0].Routes {
				switch x.Name {
				case "broken":
					r = x
				case "optin":
					optin = x
				}
			}
			if !r.Ignored || !strings.Contains(r.Invalid, "Output gone") || r.RenderBlocked != c.blocked || got.Unrouted.Count != 1 || got.Unrouted.Pods[0].Name != "b1" {
				t.Fatalf("broken=%+v unrouted=%+v", r, got.Unrouted)
			}
			if optin.RenderBlocked != c.blocked || optin.SelectedPods != c.optin {
				t.Fatalf("optin = %+v", optin)
			}
		})
	}
}
