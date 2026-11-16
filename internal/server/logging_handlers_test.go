package server

import (
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/logging"
)

func loggingKinds(skip ...string) []k8s.APIResource {
	all := []k8s.APIResource{
		{Group: "logging.banzaicloud.io", Version: "v1beta1", Name: "loggings", Kind: "Logging", Namespaced: false, Verbs: []string{"get", "list", "watch"}},
		{Group: "logging.banzaicloud.io", Version: "v1beta1", Name: "flows", Kind: "Flow", Namespaced: true, Verbs: []string{"get", "list", "watch"}},
		{Group: "logging.banzaicloud.io", Version: "v1beta1", Name: "clusterflows", Kind: "ClusterFlow", Namespaced: true, Verbs: []string{"get", "list", "watch"}},
		{Group: "logging.banzaicloud.io", Version: "v1beta1", Name: "outputs", Kind: "Output", Namespaced: true, Verbs: []string{"get", "list", "watch"}},
		{Group: "logging.banzaicloud.io", Version: "v1beta1", Name: "clusteroutputs", Kind: "ClusterOutput", Namespaced: true, Verbs: []string{"get", "list", "watch"}},
	}
	var out []k8s.APIResource
	for _, k := range all {
		if !slices.Contains(skip, k.Name) {
			out = append(out, k)
		}
	}
	return out
}

func lgObj(kind, ns, name string, spec map[string]any, status map[string]any) *unstructured.Unstructured {
	u := cnpgObj("logging.banzaicloud.io/v1beta1", kind, ns, name, spec, nil)
	if status != nil {
		u.Object["status"] = status
	}
	return u
}

func seedLoggingFixture(t *testing.T, kinds []k8s.APIResource, podLabels map[string]string) {
	t.Helper()
	restoreFixtureCache(t)
	k8s.ResetResourceCache()
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "app-1", Labels: podLabels},
		Spec:       corev1.PodSpec{NodeName: "node-1", Containers: []corev1.Container{{Name: "app"}}},
	}
	if err := k8s.InitTestResourceCache(fake.NewClientset(pod)); err != nil {
		t.Fatal(err)
	}
	objs := []runtime.Object{
		lgObj("Logging", "", "default", map[string]any{"controlNamespace": "logging-system"}, nil),
		lgObj("Logging", "", "second", map[string]any{"loggingRef": "second", "controlNamespace": "other"}, nil),
		lgObj("ClusterFlow", "logging-system", "optin", map[string]any{
			"match":            []any{map[string]any{"select": map[string]any{"labels": map[string]any{"example.com/loki-logs": "true"}}}},
			"filters":          []any{map[string]any{"dedot": map[string]any{}}},
			"globalOutputRefs": []any{"loki"},
		}, map[string]any{"active": true}),
		lgObj("ClusterFlow", "logging-system", "pg", map[string]any{"match": []any{map[string]any{"select": map[string]any{"container_names": []any{"postgres"}}}}, "globalOutputRefs": []any{"loki"}}, nil),
		lgObj("ClusterFlow", "team", "stray", map[string]any{}, nil),
		lgObj("Flow", "team", "second-flow", map[string]any{"loggingRef": "second", "localOutputRefs": []any{"local"}}, nil),
		lgObj("ClusterOutput", "logging-system", "loki", map[string]any{"loki": map[string]any{"url": "https://user:pw@loki.example/push", "password": map[string]any{"value": "s3cret"}}}, map[string]any{"active": true}),
		lgObj("Output", "team", "local", map[string]any{"loggingRef": "second", "loki": map[string]any{}}, map[string]any{"active": false}),
	}
	var keep []runtime.Object
	for _, o := range objs {
		u := o.(*unstructured.Unstructured)
		for _, k := range kinds {
			if k.Kind == u.GetKind() {
				keep = append(keep, o)
			}
		}
	}
	seedCNPGWorkspace(t, kinds, keep...)
}

func getLoggingPod(t *testing.T, resp *http.Response) (logShippingResponse, string) {
	t.Helper()
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	var out logShippingResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out, string(raw)
}

func installed(r logShippingResponse) bool {
	return len(r.Providers) == 1 && r.Providers[0] == "logging-operator"
}

func flowByName(r logShippingResponse, name string) logRoute {
	for _, f := range r.Routes {
		if f.Name == name {
			return f
		}
	}
	return logRoute{}
}

func TestLoggingPodShipping(t *testing.T) {
	seedLoggingFixture(t, loggingKinds(), map[string]string{"example.com/loki-logs": "true"})
	resp, _ := http.Get(testServer.URL + "/api/logging/pods/team/app-1")
	got, raw := getLoggingPod(t, resp)
	if !installed(got) || !got.Shipped || got.Uncertain || len(got.ReadErrors) != 0 {
		t.Fatalf("response = %+v", got)
	}
	optin := flowByName(got, "optin")
	if !optin.Selected || !optin.HasFilters || len(optin.Destinations) != 1 || optin.Destinations[0].Name != "loki" || optin.Destinations[0].Type != "loki" || optin.Destinations[0].Active == nil || !*optin.Destinations[0].Active {
		t.Errorf("optin = %+v", optin)
	}
	if pg := flowByName(got, "pg"); pg.Selected || len(pg.Unmet) != 1 || pg.Unmet[0] != "container in [postgres]" {
		t.Errorf("pg = %+v", pg)
	}
	// A ClusterFlow the operator ignores decides nothing, so it isn't sent.
	if stray := flowByName(got, "stray"); stray.Name != "" {
		t.Errorf("stray = %+v", stray)
	}
	if second := flowByName(got, "second-flow"); !second.Selected || second.Ignored {
		t.Errorf("second-flow = %+v", second)
	}
	for _, r := range got.Routes {
		if r.Provider != "logging-operator" || r.Group != logging.Group {
			t.Errorf("route %s tags = %q %q", r.Name, r.Provider, r.Group)
		}
	}
	// The wire names a route and a destination, never an operator's words.
	for _, field := range []string{`"routes"`, `"destinations"`, `"providers"`} {
		if !strings.Contains(raw, field) {
			t.Errorf("body lacks %s", field)
		}
	}
	for _, field := range []string{`"flows"`, `"outputs"`, `"installed"`} {
		if strings.Contains(raw, field) {
			t.Errorf("body still has %s", field)
		}
	}
	if got.Routes[0].Selected != true || got.Routes[len(got.Routes)-1].Name != "pg" {
		t.Errorf("order = %+v", got.Routes)
	}
	for _, leak := range []string{"s3cret", "user:pw", "https://"} {
		if strings.Contains(raw, leak) {
			t.Errorf("body leaks %q: %s", leak, raw)
		}
	}
}

func TestLoggingPodShippingNotSelected(t *testing.T) {
	seedLoggingFixture(t, loggingKinds(), nil)
	resp, _ := http.Get(testServer.URL + "/api/logging/pods/team/app-1")
	got, _ := getLoggingPod(t, resp)
	// second-flow (empty match, Logging "second") selects it, but its only
	// output is inactive, so nothing is delivered.
	if got.Shipped || got.Uncertain {
		t.Fatalf("response = %+v", got)
	}
	if f := flowByName(got, "second-flow"); !f.Selected || f.Delivers == nil || *f.Delivers {
		t.Fatalf("second-flow = %+v", f)
	}
	if optin := flowByName(got, "optin"); optin.Selected || len(optin.Unmet) != 1 || optin.Unmet[0] != "label example.com/loki-logs=true" {
		t.Errorf("optin = %+v", optin)
	}
}

func TestLoggingPodShippingReadErrors(t *testing.T) {
	seedLoggingFixture(t, loggingKinds("clusterflows"), map[string]string{"example.com/loki-logs": "true"})
	resp, _ := http.Get(testServer.URL + "/api/logging/pods/team/app-1")
	got, _ := getLoggingPod(t, resp)
	if len(got.ReadErrors) != 1 || got.ReadErrors[0] != (logReadError{Group: logging.Group, Resource: "clusterflows"}) {
		t.Fatalf("readErrors = %v", got.ReadErrors)
	}
}

func TestLoggingPodShippingNotInstalled(t *testing.T) {
	seedLoggingFixture(t, nil, nil)
	resp, _ := http.Get(testServer.URL + "/api/logging/pods/team/app-1")
	got, raw := getLoggingPod(t, resp)
	if installed(got) || got.Routes == nil || len(got.Routes) != 0 || !strings.Contains(raw, `"providers":[]`) {
		t.Fatalf("response = %+v", got)
	}
}

func TestLoggingPodShippingRequiresPodGet(t *testing.T) {
	seedLoggingFixture(t, loggingKinds(), map[string]string{"example.com/loki-logs": "true"})
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"team"}}
	perms.SetCanI("get", "", "pods", "team", false)
	env.srv.permCache.Set("alice", nil, perms)
	resp := env.authGet(t, "/api/logging/pods/team/app-1", "alice", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("without get pods: %d, want 403", resp.StatusCode)
	}
	perms = &auth.UserPermissions{AllowedNamespaces: []string{"team"}}
	perms.SetCanI("get", "", "pods", "team", true)
	env.srv.permCache.Set("bob", nil, perms)
	got, _ := getLoggingPod(t, env.authGet(t, "/api/logging/pods/team/app-1", "bob", ""))
	if !got.Shipped || flowByName(got, "optin").Name == "" {
		t.Fatalf("a caller who can get the pod sees the flows that decide it: %+v", got)
	}
	resp = env.authGet(t, "/api/logging/pods/other/app-1", "bob", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("another namespace: %d, want 403", resp.StatusCode)
	}
}

func TestLoggingPodShippingSharedListener(t *testing.T) {
	seedLoggingFixture(t, loggingKinds(), nil)
	req, _ := http.NewRequest("GET", testServer.URL+"/api/logging/pods/team/app-1", nil)
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

// A kind Radar can't list (chart toggle off, or a slow apiserver) never syncs;
// that is a read error, never an empty list that reads as "not shipped".
func TestLoggingPodShippingUnsyncedKindIsAReadError(t *testing.T) {
	seedLoggingFixture(t, loggingKinds(), nil)
	kinds := loggingKinds()
	listKinds := map[schema.GroupVersionResource]string{}
	for _, k := range kinds {
		listKinds[schema.GroupVersionResource{Group: k.Group, Version: k.Version, Resource: k.Name}] = k.Kind + "List"
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
	// The ClusterFlow list never answers within the wait, as on a slow
	// apiserver: the informer stays unsynced and its indexer empty.
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	dyn.PrependReactor("list", "clusterflows", func(k8stesting.Action) (bool, runtime.Object, error) {
		<-release
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "logging.banzaicloud.io", Resource: "clusterflows"}, "", nil)
	})
	oldWait := dynamicSyncWait
	dynamicSyncWait = 300 * time.Millisecond
	t.Cleanup(func() { dynamicSyncWait = oldWait })
	k8s.ResetTestDynamicState()
	if err := k8s.InitTestDynamicResourceCache(dyn, kinds); err != nil {
		t.Fatal(err)
	}
	resp, _ := http.Get(testServer.URL + "/api/logging/pods/team/app-1")
	got, _ := getLoggingPod(t, resp)
	if !slices.Contains(got.ReadErrors, logReadError{Group: logging.Group, Resource: "clusterflows"}) {
		t.Fatalf("an unsynced ClusterFlow read must be a read error: %+v", got)
	}
}

// A Logging that watches other namespaces ignores a Flow in the pod's
// namespace; one with a namespace selector Radar can't evaluate makes it "may".
func TestLoggingPodShippingWatchNamespaces(t *testing.T) {
	for _, c := range []struct {
		name      string
		spec      map[string]any
		ignored   bool
		uncertain bool
	}{
		{"outside watchNamespaces", map[string]any{"controlNamespace": "logging-system", "watchNamespaces": []any{"elsewhere"}}, true, false},
		{"namespace selector", map[string]any{"controlNamespace": "logging-system", "watchNamespaceSelector": map[string]any{"matchLabels": map[string]any{"logs": "on"}}}, false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			restoreFixtureCache(t)
			k8s.ResetResourceCache()
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "app-1"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}}
			if err := k8s.InitTestResourceCache(fake.NewClientset(pod)); err != nil {
				t.Fatal(err)
			}
			seedCNPGWorkspace(t, loggingKinds(),
				lgObj("Logging", "", "default", c.spec, nil),
				lgObj("Flow", "team", "everything", map[string]any{}, nil),
			)
			resp, _ := http.Get(testServer.URL + "/api/logging/pods/team/app-1")
			got, _ := getLoggingPod(t, resp)
			// An ignored Flow decides nothing, so it isn't sent.
			f := flowByName(got, "everything")
			if got.Shipped || (f.Name == "") != c.ignored || f.Uncertain != c.uncertain || f.Selected {
				t.Fatalf("response = %+v", got)
			}
		})
	}
}

func seedLoggingObjects(t *testing.T, podLabels map[string]string, objs ...runtime.Object) {
	t.Helper()
	restoreFixtureCache(t)
	k8s.ResetResourceCache()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "app-1", Labels: podLabels}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}}
	if err := k8s.InitTestResourceCache(fake.NewClientset(pod)); err != nil {
		t.Fatal(err)
	}
	all := append([]runtime.Object{lgObj("Logging", "", "default", map[string]any{"controlNamespace": "logging-system"}, nil)}, objs...)
	seedCNPGWorkspace(t, loggingKinds(), all...)
}

// Selection is not delivery: a Flow the operator reports inactive, a Flow
// without outputs, or an output that is missing or inactive ships nothing; an
// output with no reported state can't confirm delivery.
func TestLoggingPodShippingNeedsAWorkingOutput(t *testing.T) {
	active := map[string]any{"active": true}
	inactive := map[string]any{"active": false}
	flow := func(status map[string]any, outputs ...any) *unstructured.Unstructured {
		return lgObj("ClusterFlow", "logging-system", "all", map[string]any{"globalOutputRefs": outputs}, status)
	}
	out := func(status map[string]any) *unstructured.Unstructured {
		return lgObj("ClusterOutput", "logging-system", "loki", map[string]any{"loki": map[string]any{"url": "https://loki.example/push"}}, status)
	}
	for _, c := range []struct {
		name               string
		objs               []runtime.Object
		shipped, uncertain bool
		delivers           *bool
		ignored            bool
	}{
		{"working output", []runtime.Object{flow(active, "loki"), out(active)}, true, false, boolp(true), false},
		{"inactive flow", []runtime.Object{flow(inactive, "loki"), out(active)}, false, false, boolp(false), false},
		{"no outputs", []runtime.Object{flow(active)}, false, false, boolp(false), false},
		// The operator rejects a Flow naming an output that doesn't exist.
		{"missing output", []runtime.Object{flow(active, "loki")}, false, false, nil, true},
		{"inactive output", []runtime.Object{flow(active, "loki"), out(inactive)}, false, false, boolp(false), false},
		{"output state unknown", []runtime.Object{flow(active, "loki"), out(nil)}, false, true, nil, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			seedLoggingObjects(t, nil, c.objs...)
			resp, _ := http.Get(testServer.URL + "/api/logging/pods/team/app-1")
			got, _ := getLoggingPod(t, resp)
			f := flowByName(got, "all")
			if got.Shipped != c.shipped || got.Uncertain != c.uncertain || f.Selected == c.ignored || f.Ignored != c.ignored || !reflect.DeepEqual(f.Delivers, c.delivers) {
				t.Fatalf("shipped=%v uncertain=%v flow=%+v", got.Shipped, got.Uncertain, f)
			}
		})
	}
}

func TestLoggingPodShippingExcludedAndOutputNamespace(t *testing.T) {
	seedLoggingObjects(t, map[string]string{"tier": "debug"},
		lgObj("ClusterFlow", "logging-system", "no-debug", map[string]any{"match": []any{map[string]any{"exclude": map[string]any{"labels": map[string]any{"tier": "debug"}}}, map[string]any{"select": map[string]any{}}}, "globalOutputRefs": []any{"loki"}}, map[string]any{"active": true}),
		lgObj("ClusterFlow", "logging-system", "all", map[string]any{"globalOutputRefs": []any{"loki"}}, map[string]any{"active": true}),
		lgObj("ClusterOutput", "logging-system", "loki", map[string]any{"loki": map[string]any{}}, map[string]any{"active": true}),
	)
	resp, _ := http.Get(testServer.URL + "/api/logging/pods/team/app-1")
	got, _ := getLoggingPod(t, resp)
	if f := flowByName(got, "no-debug"); !f.Excluded || f.Selected {
		t.Errorf("no-debug = %+v", f)
	}
	if f := flowByName(got, "all"); len(f.Destinations) != 1 || f.Destinations[0].Namespace != "logging-system" {
		t.Errorf("all outputs = %+v", f.Destinations)
	}
}

func boolp(b bool) *bool { return &b }

// Loggings that couldn't be read can't make a Flow "ignored": whether the
// operator counts it is unknown, so a matching Flow may select the pod.
func TestLoggingPodShippingUnreadLoggings(t *testing.T) {
	seedLoggingFixture(t, loggingKinds(), map[string]string{"example.com/loki-logs": "true"})
	kinds := loggingKinds()
	listKinds := map[schema.GroupVersionResource]string{}
	for _, k := range kinds {
		listKinds[schema.GroupVersionResource{Group: k.Group, Version: k.Version, Resource: k.Name}] = k.Kind + "List"
	}
	optin := lgObj("ClusterFlow", "logging-system", "optin", map[string]any{
		"match":            []any{map[string]any{"select": map[string]any{"labels": map[string]any{"example.com/loki-logs": "true"}}}},
		"globalOutputRefs": []any{"loki"},
	}, map[string]any{"active": true})
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, optin)
	dyn.PrependReactor("list", "loggings", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: "logging.banzaicloud.io", Resource: "loggings"}, "", nil)
	})
	oldWait := dynamicSyncWait
	dynamicSyncWait = 300 * time.Millisecond
	t.Cleanup(func() { dynamicSyncWait = oldWait })
	k8s.ResetTestDynamicState()
	if err := k8s.InitTestDynamicResourceCache(dyn, kinds); err != nil {
		t.Fatal(err)
	}
	resp, _ := http.Get(testServer.URL + "/api/logging/pods/team/app-1")
	got, _ := getLoggingPod(t, resp)
	if !slices.Contains(got.ReadErrors, logReadError{Group: logging.Group, Resource: "loggings"}) {
		t.Fatalf("readErrors = %v", got.ReadErrors)
	}
	route := flowByName(got, "optin")
	if route.Ignored || route.Selected || !route.Uncertain || got.Shipped || !got.Uncertain {
		t.Fatalf("optin = %+v, answer = %+v", route, got)
	}
}

// Ephemeral containers write logs to the node like any other container, so
// a container_names rule can select one.
func TestLoggingPodFactsIncludeEphemeralContainers(t *testing.T) {
	pod := &corev1.Pod{Spec: corev1.PodSpec{
		InitContainers:      []corev1.Container{{Name: "init"}},
		Containers:          []corev1.Container{{Name: "app"}},
		EphemeralContainers: []corev1.EphemeralContainer{{EphemeralContainerCommon: corev1.EphemeralContainerCommon{Name: "debugger"}}},
	}}
	if got := loggingPodFacts(pod).Containers; !slices.Equal(got, []string{"init", "app", "debugger"}) {
		t.Fatalf("containers = %v", got)
	}
}

func seedLoggingWith(t *testing.T, logging *unstructured.Unstructured, objs ...runtime.Object) {
	t.Helper()
	restoreFixtureCache(t)
	k8s.ResetResourceCache()
	pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "app-1"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}}
	if err := k8s.InitTestResourceCache(fake.NewClientset(pod)); err != nil {
		t.Fatal(err)
	}
	seedCNPGWorkspace(t, loggingKinds(), append([]runtime.Object{logging}, objs...)...)
}

// What no Flow selects still ships through the Logging's defaultFlow.
func TestLoggingPodShippingDefaultFlow(t *testing.T) {
	withDefault := lgObj("Logging", "", "default", map[string]any{"controlNamespace": "logging-system", "defaultFlow": map[string]any{"globalOutputRefs": []any{"loki"}}}, nil)
	loki := lgObj("ClusterOutput", "logging-system", "loki", map[string]any{"loki": map[string]any{}}, map[string]any{"active": true})
	optin := lgObj("ClusterFlow", "logging-system", "optin", map[string]any{"match": []any{map[string]any{"select": map[string]any{"labels": map[string]any{"example.com/logs": "true"}}}}, "globalOutputRefs": []any{"loki"}}, nil)
	t.Run("no Flow selects", func(t *testing.T) {
		seedLoggingWith(t, withDefault, loki, optin)
		got, _ := getLoggingPod(t, mustGet(t, testServer.URL+"/api/logging/pods/team/app-1"))
		fb := flowByName(got, "default")
		if !got.Shipped || !fb.Fallback || !fb.Selected || fb.Kind != "Logging" || fb.Delivers == nil || !*fb.Delivers || len(fb.Destinations) != 1 || !fb.Destinations[0].Found {
			t.Fatalf("shipped=%v default=%+v", got.Shipped, fb)
		}
	})
	t.Run("a Flow selects", func(t *testing.T) {
		all := lgObj("ClusterFlow", "logging-system", "all", map[string]any{"globalOutputRefs": []any{"loki"}}, map[string]any{"active": true})
		seedLoggingWith(t, withDefault, loki, all)
		got, _ := getLoggingPod(t, mustGet(t, testServer.URL+"/api/logging/pods/team/app-1"))
		if fb := flowByName(got, "default"); fb.Name != "" {
			t.Fatalf("a selecting Flow leaves nothing for the default flow, got %+v", fb)
		}
	})
}

func TestLoggingPodShippingClusterResourcesFromAllNamespaces(t *testing.T) {
	l := lgObj("Logging", "", "default", map[string]any{"controlNamespace": "logging-system", "allowClusterResourcesFromAllNamespaces": true}, nil)
	seedLoggingWith(t, l,
		lgObj("ClusterFlow", "team", "elsewhere", map[string]any{"globalOutputRefs": []any{"loki"}}, map[string]any{"active": true}),
		lgObj("ClusterOutput", "team", "loki", map[string]any{"loki": map[string]any{}}, map[string]any{"active": true}),
	)
	got, _ := getLoggingPod(t, mustGet(t, testServer.URL+"/api/logging/pods/team/app-1"))
	f := flowByName(got, "elsewhere")
	if !got.Shipped || f.Ignored || len(f.Destinations) != 1 || !f.Destinations[0].Found || f.Destinations[0].Namespace != "team" {
		t.Fatalf("shipped=%v flow=%+v", got.Shipped, f)
	}
}

// The operator rejects a Flow with both match and selectors, or select and
// exclude in one entry, and deprecated outputRefs are never rendered.
// skipInvalidResources keeps the invalid Flow from blocking the valid one.
func TestLoggingPodShippingInvalidFlowAndDeprecatedOutputRefs(t *testing.T) {
	l := lgObj("Logging", "", "default", map[string]any{"controlNamespace": "logging-system", "skipInvalidResources": true}, nil)
	seedLoggingWith(t, l,
		lgObj("Flow", "team", "both", map[string]any{"match": []any{map[string]any{"select": map[string]any{}, "exclude": map[string]any{"labels": map[string]any{"a": "b"}}}}, "localOutputRefs": []any{"out"}}, nil),
		lgObj("Flow", "team", "legacy", map[string]any{"outputRefs": []any{"out"}}, nil),
		lgObj("Output", "team", "out", map[string]any{"loki": map[string]any{}}, map[string]any{"active": true}),
	)
	got, _ := getLoggingPod(t, mustGet(t, testServer.URL+"/api/logging/pods/team/app-1"))
	if f := flowByName(got, "both"); !f.Ignored || f.Selected || f.Invalid == "" {
		t.Errorf("both = %+v", f)
	}
	if f := flowByName(got, "legacy"); len(f.Destinations) != 0 || f.Delivers == nil || *f.Delivers {
		t.Errorf("legacy = %+v", f)
	}
	if got.Shipped {
		t.Errorf("shipped = true")
	}
}

// A Flow naming an output the operator can't resolve (missing, or with another
// loggingRef) is rejected, but only when Radar read that output kind.
func TestLoggingPodShippingMissingOutputRejectsFlow(t *testing.T) {
	l := lgObj("Logging", "", "default", map[string]any{"controlNamespace": "logging-system"}, nil)
	flow := lgObj("Flow", "team", "app", map[string]any{"localOutputRefs": []any{"out", "gone"}}, map[string]any{"active": true})
	out := lgObj("Output", "team", "out", map[string]any{"loki": map[string]any{}}, map[string]any{"active": true})
	t.Run("outputs read", func(t *testing.T) {
		seedLoggingWith(t, l, flow, out)
		got, _ := getLoggingPod(t, mustGet(t, testServer.URL+"/api/logging/pods/team/app-1"))
		if f := flowByName(got, "app"); !f.Ignored || f.Selected || !strings.Contains(f.Invalid, "Output gone") || got.Shipped {
			t.Fatalf("shipped=%v app=%+v", got.Shipped, f)
		}
	})
	t.Run("output of another Logging", func(t *testing.T) {
		other := lgObj("Output", "team", "gone", map[string]any{"loggingRef": "second", "loki": map[string]any{}}, map[string]any{"active": true})
		seedLoggingWith(t, l, flow, out, other)
		got, _ := getLoggingPod(t, mustGet(t, testServer.URL+"/api/logging/pods/team/app-1"))
		if f := flowByName(got, "app"); !f.Ignored || f.Invalid == "" {
			t.Fatalf("app = %+v", f)
		}
	})
	t.Run("outputs unread", func(t *testing.T) {
		restoreFixtureCache(t)
		k8s.ResetResourceCache()
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "app-1"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}}
		if err := k8s.InitTestResourceCache(fake.NewClientset(pod)); err != nil {
			t.Fatal(err)
		}
		seedCNPGWorkspace(t, loggingKinds("outputs"), l, flow)
		got, _ := getLoggingPod(t, mustGet(t, testServer.URL+"/api/logging/pods/team/app-1"))
		if f := flowByName(got, "app"); f.Ignored || f.Invalid != "" || !f.Selected || f.Delivers != nil || !got.Uncertain {
			t.Fatalf("uncertain=%v app=%+v", got.Uncertain, f)
		}
	})
}

// "match: []" leaves the operator a route with no match rules: nothing is selected.
func TestLoggingPodShippingEmptyMatchSelectsNothing(t *testing.T) {
	l := lgObj("Logging", "", "default", map[string]any{"controlNamespace": "logging-system"}, nil)
	seedLoggingWith(t, l, lgObj("ClusterFlow", "logging-system", "empty", map[string]any{"match": []any{}}, nil))
	got, _ := getLoggingPod(t, mustGet(t, testServer.URL+"/api/logging/pods/team/app-1"))
	if f := flowByName(got, "empty"); f.Selected || f.Uncertain {
		t.Fatalf("empty = %+v", f)
	}
}

func seedLoggingPod(t *testing.T, pod *corev1.Pod, objs ...runtime.Object) {
	t.Helper()
	restoreFixtureCache(t)
	k8s.ResetResourceCache()
	if err := k8s.InitTestResourceCache(fake.NewClientset(pod)); err != nil {
		t.Fatal(err)
	}
	seedCNPGWorkspace(t, loggingKinds(), objs...)
}

// A caller who can get the pod learns which routes decide it, not every
// ClusterFlow in the cluster: only routes that select, may select or exclude
// it, invalid ones that would select it, and the single closest miss, whose
// namespace and node conditions, own namespace and destination namespaces are
// withheld unless the caller may list that route's kind.
func TestLoggingPodShippingWithholdsUnrelatedRoutes(t *testing.T) {
	l := lgObj("Logging", "", "default", map[string]any{"controlNamespace": "logging-system"}, nil)
	seedLoggingWith(t, l,
		lgObj("ClusterFlow", "logging-system", "near", map[string]any{"match": []any{map[string]any{"select": map[string]any{"namespaces": []any{"secret-a"}, "hosts": []any{"node-9"}}}}, "globalOutputRefs": []any{"loki"}}, nil),
		lgObj("ClusterFlow", "logging-system", "far", map[string]any{"match": []any{map[string]any{"select": map[string]any{"labels": map[string]any{"a": "b", "c": "d"}, "namespaces": []any{"secret-b"}}}}, "globalOutputRefs": []any{"loki"}}, nil),
		lgObj("ClusterFlow", "elsewhere", "stray", map[string]any{"globalOutputRefs": []any{"loki"}}, nil),
		lgObj("ClusterOutput", "logging-system", "loki", map[string]any{"loki": map[string]any{}}, map[string]any{"active": true}),
	)
	env := newAuthTestServer(t)
	for _, c := range []struct {
		user     string
		canList  bool
		unmetHas string
	}{
		{"bob", false, "a namespace or node condition"},
		{"carol", true, "namespace in [secret-a]"},
	} {
		perms := &auth.UserPermissions{AllowedNamespaces: []string{"team"}}
		perms.SetCanI("get", "", "pods", "team", true)
		perms.SetCanI("list", logging.Group, "clusterflows", "logging-system", c.canList)
		env.srv.permCache.Set(c.user, nil, perms)
		got, raw := getLoggingPod(t, env.authGet(t, "/api/logging/pods/team/app-1", c.user, ""))
		if len(got.Routes) != 1 || got.Routes[0].Name != "near" || !slices.Contains(got.Routes[0].Unmet, c.unmetHas) {
			t.Fatalf("%s: routes = %+v", c.user, got.Routes)
		}
		for _, leak := range []string{"far", "stray", "secret-b"} {
			if strings.Contains(raw, leak) {
				t.Errorf("%s: body names %q: %s", c.user, leak, raw)
			}
		}
		if !c.canList && (strings.Contains(raw, "secret-a") || strings.Contains(raw, "node-9") || strings.Contains(raw, "logging-system")) {
			t.Errorf("%s: body leaks namespace or node conditions, or the route's namespace: %s", c.user, raw)
		}
		if c.canList && (got.Routes[0].Namespace != "logging-system" || got.Routes[0].Destinations[0].Namespace != "logging-system") {
			t.Errorf("%s: route namespaces withheld from a caller who may list them: %+v", c.user, got.Routes[0])
		}
	}
}

// Only a ClusterFlow may use a protected ClusterOutput; the operator refuses a
// Flow naming one.
func TestLoggingPodShippingProtectedClusterOutput(t *testing.T) {
	l := lgObj("Logging", "", "default", map[string]any{"controlNamespace": "logging-system", "skipInvalidResources": true}, nil)
	audit := lgObj("ClusterOutput", "logging-system", "audit", map[string]any{"protected": true, "loki": map[string]any{}}, map[string]any{"active": true})
	t.Run("Flow", func(t *testing.T) {
		seedLoggingWith(t, l, audit, lgObj("Flow", "team", "app", map[string]any{"globalOutputRefs": []any{"audit"}}, map[string]any{"active": true}))
		got, _ := getLoggingPod(t, mustGet(t, testServer.URL+"/api/logging/pods/team/app-1"))
		if f := flowByName(got, "app"); !f.Ignored || !strings.Contains(f.Invalid, "protected") || got.Shipped {
			t.Fatalf("shipped=%v app=%+v", got.Shipped, f)
		}
	})
	t.Run("ClusterFlow", func(t *testing.T) {
		seedLoggingWith(t, l, audit, lgObj("ClusterFlow", "logging-system", "all", map[string]any{"globalOutputRefs": []any{"audit"}}, map[string]any{"active": true}))
		got, _ := getLoggingPod(t, mustGet(t, testServer.URL+"/api/logging/pods/team/app-1"))
		if f := flowByName(got, "all"); f.Ignored || !got.Shipped {
			t.Fatalf("shipped=%v all=%+v", got.Shipped, f)
		}
	})
}

// With skipInvalidResources off (the default), one invalid Flow anywhere the
// Logging watches stops the operator rendering that Logging's configuration,
// so whether its other routes ship the pod can't be told.
func TestLoggingPodShippingInvalidFlowBlocksItsLogging(t *testing.T) {
	all := lgObj("ClusterFlow", "logging-system", "all", map[string]any{"globalOutputRefs": []any{"loki"}}, map[string]any{"active": true})
	loki := lgObj("ClusterOutput", "logging-system", "loki", map[string]any{"loki": map[string]any{}}, map[string]any{"active": true})
	broken := lgObj("Flow", "other", "broken", map[string]any{"localOutputRefs": []any{"gone"}}, nil)
	for _, c := range []struct {
		name     string
		spec     map[string]any
		objs     []runtime.Object
		blocked  bool
		selected bool
	}{
		{"invalid Flow in another namespace", map[string]any{"controlNamespace": "logging-system"}, []runtime.Object{all, loki, broken}, true, false},
		{"skipInvalidResources", map[string]any{"controlNamespace": "logging-system", "skipInvalidResources": true}, []runtime.Object{all, loki, broken}, false, true},
		{"defaultFlow names a missing output", map[string]any{"controlNamespace": "logging-system", "skipInvalidResources": true, "defaultFlow": map[string]any{"globalOutputRefs": []any{"gone"}}}, []runtime.Object{all, loki}, true, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			seedLoggingWith(t, lgObj("Logging", "", "default", c.spec, nil), c.objs...)
			got, raw := getLoggingPod(t, mustGet(t, testServer.URL+"/api/logging/pods/team/app-1"))
			f := flowByName(got, "all")
			if f.RenderBlocked != c.blocked || f.Selected != c.selected || f.Uncertain != c.blocked || got.Shipped == c.blocked || got.Uncertain != c.blocked {
				t.Fatalf("shipped=%v uncertain=%v all=%+v", got.Shipped, got.Uncertain, f)
			}
			if strings.Contains(raw, "broken") {
				t.Errorf("body names a Flow in another namespace: %s", raw)
			}
		})
	}
}

// fluent-bit drops the logs of a pod annotated fluentbit.io/exclude: "true"
// (K8S-Logging.Exclude, on by default), whatever the Flows select.
func TestLoggingPodShippingExcludeAnnotation(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "app-1", Annotations: map[string]string{"fluentbit.io/exclude": "true"}},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
	}
	seedLoggingPod(t, pod,
		lgObj("Logging", "", "default", map[string]any{"controlNamespace": "logging-system"}, nil),
		lgObj("ClusterFlow", "logging-system", "all", map[string]any{"globalOutputRefs": []any{"loki"}}, map[string]any{"active": true}),
		lgObj("ClusterOutput", "logging-system", "loki", map[string]any{"loki": map[string]any{}}, map[string]any{"active": true}),
	)
	got, _ := getLoggingPod(t, mustGet(t, testServer.URL+"/api/logging/pods/team/app-1"))
	if got.Shipped || !got.Uncertain || got.ExcludeAnnotation != "fluentbit.io/exclude" {
		t.Fatalf("response = %+v", got)
	}
}

// Without a cluster-wide read of Outputs, a Flow elsewhere naming an Output
// can't be called invalid, so it never blocks its Logging.
func TestLoggingPodShippingOutputsReadOnlyInPodNamespace(t *testing.T) {
	seedLoggingFixture(t, loggingKinds(), nil)
	kinds := loggingKinds()
	listKinds := map[schema.GroupVersionResource]string{}
	for _, k := range kinds {
		listKinds[schema.GroupVersionResource{Group: k.Group, Version: k.Version, Resource: k.Name}] = k.Kind + "List"
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds,
		lgObj("Logging", "", "default", map[string]any{"controlNamespace": "logging-system"}, nil),
		lgObj("ClusterFlow", "logging-system", "all", map[string]any{"globalOutputRefs": []any{"loki"}}, map[string]any{"active": true}),
		lgObj("ClusterOutput", "logging-system", "loki", map[string]any{"loki": map[string]any{}}, map[string]any{"active": true}),
		lgObj("Flow", "other", "ok", map[string]any{"localOutputRefs": []any{"out"}}, nil),
		lgObj("Output", "other", "out", map[string]any{"loki": map[string]any{}}, map[string]any{"active": true}),
	)
	dyn.PrependReactor("list", "outputs", func(a k8stesting.Action) (bool, runtime.Object, error) {
		if a.GetNamespace() == "" {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: logging.Group, Resource: "outputs"}, "", nil)
		}
		return false, nil, nil
	})
	k8s.ResetTestDynamicState()
	if err := k8s.InitTestDynamicResourceCache(dyn, kinds); err != nil {
		t.Fatal(err)
	}
	got, _ := getLoggingPod(t, mustGet(t, testServer.URL+"/api/logging/pods/team/app-1"))
	if f := flowByName(got, "all"); f.RenderBlocked || !got.Shipped || len(got.ReadErrors) != 0 {
		t.Fatalf("shipped=%v readErrors=%v all=%+v", got.Shipped, got.ReadErrors, f)
	}
}
