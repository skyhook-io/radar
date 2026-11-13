package server

import (
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
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

func getLoggingPod(t *testing.T, resp *http.Response) (loggingPodResponse, string) {
	t.Helper()
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	var out loggingPodResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out, string(raw)
}

func flowByName(r loggingPodResponse, name string) loggingPodFlow {
	for _, f := range r.Flows {
		if f.Name == name {
			return f
		}
	}
	return loggingPodFlow{}
}

func TestLoggingPodShipping(t *testing.T) {
	seedLoggingFixture(t, loggingKinds(), map[string]string{"example.com/loki-logs": "true"})
	resp, _ := http.Get(testServer.URL + "/api/logging/pods/team/app-1")
	got, raw := getLoggingPod(t, resp)
	if !got.Installed || !got.Shipped || got.Uncertain || len(got.ReadErrors) != 0 {
		t.Fatalf("response = %+v", got)
	}
	optin := flowByName(got, "optin")
	if !optin.Selected || !optin.HasFilters || len(optin.Outputs) != 1 || optin.Outputs[0].Name != "loki" || optin.Outputs[0].Type != "loki" || optin.Outputs[0].Active == nil || !*optin.Outputs[0].Active {
		t.Errorf("optin = %+v", optin)
	}
	if pg := flowByName(got, "pg"); pg.Selected || len(pg.Unmet) != 1 || pg.Unmet[0] != "container in [postgres]" {
		t.Errorf("pg = %+v", pg)
	}
	if stray := flowByName(got, "stray"); !stray.Ignored || stray.Selected {
		t.Errorf("stray = %+v", stray)
	}
	if second := flowByName(got, "second-flow"); !second.Selected || second.Ignored {
		t.Errorf("second-flow = %+v", second)
	}
	if got.Flows[0].Selected != true || got.Flows[len(got.Flows)-1].Name != "stray" {
		t.Errorf("order = %+v", got.Flows)
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
	if !got.Shipped {
		// second-flow (empty match, counted under Logging "second") still selects it.
		t.Fatalf("second-flow selects every pod in team: %+v", got)
	}
	if optin := flowByName(got, "optin"); optin.Selected || len(optin.Unmet) != 1 || optin.Unmet[0] != "label example.com/loki-logs=true" {
		t.Errorf("optin = %+v", optin)
	}
}

func TestLoggingPodShippingReadErrors(t *testing.T) {
	seedLoggingFixture(t, loggingKinds("clusterflows"), map[string]string{"example.com/loki-logs": "true"})
	resp, _ := http.Get(testServer.URL + "/api/logging/pods/team/app-1")
	got, _ := getLoggingPod(t, resp)
	if len(got.ReadErrors) != 1 || got.ReadErrors[0] != "clusterflows" {
		t.Fatalf("readErrors = %v", got.ReadErrors)
	}
}

func TestLoggingPodShippingNotInstalled(t *testing.T) {
	seedLoggingFixture(t, nil, nil)
	resp, _ := http.Get(testServer.URL + "/api/logging/pods/team/app-1")
	got, _ := getLoggingPod(t, resp)
	if got.Installed || got.Flows == nil || len(got.Flows) != 0 {
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
