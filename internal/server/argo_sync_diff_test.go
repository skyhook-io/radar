package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	authv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestCompareArgoRenders(t *testing.T) {
	workload := func(tag, version, deployID string) string {
		return fmt.Sprintf(`{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"web","annotations":{"deployment-id":%q},"labels":{"app.kubernetes.io/version":%q}},"spec":{"template":{"spec":{"containers":[{"name":"web","image":%q}]}}}}`, deployID, version, tag)
	}
	payload := func(val string) string {
		return fmt.Sprintf(`{"apiVersion":"v1","kind":"Secret","metadata":{"name":"database","annotations":{"token":%q}},"data":{"pass":%q}}`, val, val)
	}
	tmpl := func(val string) string {
		return fmt.Sprintf(`{"apiVersion":"example.dev/v1","kind":"Policy","metadata":{"name":"rotate"},"spec":{"templates":[{"kind":"Secret","apiVersion":"v1","metadata":{"name":"embedded-private-name"},"stringData":{"token":%q}}],"enabled":true}}`, val)
	}
	resolve := func(group, kind, ns, name string) (string, bool) { return "prod", name != "no-access" }
	before := []string{
		workload("web:02", "02", "222"), payload("private-before"), tmpl("private-before"),
		`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"retired"},"data":{"key":"retired value"}}`,
		`{"apiVersion":"example.dev/v1","kind":"Widget","metadata":{"name":"config"},"spec":{"value":null}}`,
	}
	after := []string{
		workload("web:01", "01", "111"), payload("private-after"), tmpl("private-after"),
		`{"apiVersion":"v1","kind":"Service","metadata":{"name":"new"},"spec":{"type":"ClusterIP"}}`,
		`{"apiVersion":"example.dev/v1","kind":"Widget","metadata":{"name":"config"},"spec":{"empty":{}}}`,
		`{"apiVersion":"example.dev/v1","kind":"Widget","metadata":{"name":"no-access"},"spec":{"token":"denied-value"}}`,
	}
	got, err := compareArgoRenders(before, after, resolve)
	if err != nil || !got.Partial || !got.SecretsMasked || len(got.Changes) != 5 {
		t.Fatalf("summary=%+v, err=%v", got, err)
	}
	want := []string{"ConfigMap/retired/removed", "Deployment/web/modified", "Secret/database/modified", "Service/new/added", "Widget/config/modified"}
	for i, row := range got.Changes {
		if row.Kind+"/"+row.Name+"/"+row.Operation != want[i] || row.Namespace != "prod" {
			t.Errorf("row %d = %+v", i, row)
		}
	}
	assertField := func(change renderedChange, path []string, before, after string) {
		t.Helper()
		for _, f := range change.Fields {
			if slices.Equal(path, f.Path) && string(f.Before) == before && string(f.After) == after {
				return
			}
		}
		t.Fatalf("missing %s %s -> %s in %+v", path, before, after, change)
	}
	assertField(got.Changes[0], []string{"data", "key"}, `"retired value"`, "")
	assertField(got.Changes[1], []string{"spec", "template", "spec", "containers", "[name=web]", "image"}, `"web:02"`, `"web:01"`)
	assertField(got.Changes[1], []string{"metadata", "labels", "app.kubernetes.io/version"}, `"02"`, `"01"`)
	assertField(got.Changes[1], []string{"metadata", "annotations", "deployment-id"}, `"222"`, `"111"`)
	assertField(got.Changes[3], []string{"spec", "type"}, "", `"ClusterIP"`)
	assertField(got.Changes[4], []string{"spec", "value"}, "null", "")
	assertField(got.Changes[4], []string{"spec", "empty"}, "", "{}")
	encoded, _ := json.Marshal(got)
	for _, value := range []string{"private-before", "private-after", "embedded-private-name", "no-access", "denied-value"} {
		if strings.Contains(string(encoded), value) {
			t.Errorf("response contains unauthorized/masked value %q", value)
		}
	}
}

func TestCompareArgoRendersEmptyAndInvalid(t *testing.T) {
	allow := func(_, _, ns, _ string) (string, bool) { return ns, true }
	row := `{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"demo"}}`
	for _, c := range []struct {
		name            string
		before, after   []string
		diffCount       int
		partial, failed bool
	}{
		{"empty renders", nil, nil, 0, false, false},
		{"removal", []string{row}, nil, 1, false, false},
		{"addition", nil, []string{row}, 1, false, false},
		{"identical", []string{row}, []string{row}, 0, false, false},
		{"duplicate identities", nil, []string{row, row}, 0, false, true},
		{"malformed", nil, []string{"{"}, 0, false, true},
		{"list omitted", nil, []string{`{"apiVersion":"v1","kind":"List","items":[]}`}, 0, true, false},
		{"custom items really changed", nil, []string{`{"apiVersion":"inventory.test/v1","kind":"Stock","metadata":{"name":"aisle"},"items":["a"]}`}, 1, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := compareArgoRenders(c.before, c.after, allow)
			if (err != nil) != c.failed || got.Partial != c.partial || len(got.Changes) != c.diffCount {
				t.Fatalf("got=%+v err=%v", got, err)
			}
		})
	}
}

func TestCompareArgoRendersTransientDenialOmitsBothSides(t *testing.T) {
	checked := 0
	resolve := argoRenderResolver("prod", func(kind, group string) (k8s.APIResource, bool) {
		return k8s.APIResource{Kind: kind, Group: group, Namespaced: true}, true
	}, func(_ k8s.APIResource, ns, name string) bool {
		if ns != "prod" || name != "site" {
			t.Fatalf("permission check used %q/%q", ns, name)
		}
		checked++
		return checked > 1
	})
	before := []string{`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"site"},"data":{"tag":"v1"}}`}
	after := []string{`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"site","namespace":"prod"},"data":{"tag":"v2"}}`}
	got, err := compareArgoRenders(before, after, resolve)
	if err != nil || checked != 1 || !got.Partial || len(got.Changes) != 0 {
		t.Fatalf("got %+v, checks=%d, err=%v", got, checked, err)
	}
}

func TestArgoSyncDiffIdentityAndReadScope(t *testing.T) {
	previous, selected := strings.Repeat("a", 40), strings.Repeat("b", 40)
	source := map[string]any{"repoURL": "https://git.example/demo", "path": "deploy"}
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": "demo", "namespace": "default", "uid": "local-app", "generation": int64(3)},
		"spec":     map[string]any{"source": source, "project": "sample", "destination": map[string]any{"name": "in-cluster", "namespace": "default"}},
		"status":   map[string]any{"history": []any{map[string]any{"id": int64(1), "revision": previous, "source": source}, map[string]any{"id": int64(2), "revision": selected, "source": source}}},
	}}
	kube := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var review authv1.SelfSubjectAccessReview
		if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		a := review.Spec.ResourceAttributes
		review.Status.Allowed = a != nil && a.Verb == "get" && (a.Resource == "configmaps" || (a.Resource == "applications" && a.Namespace == "default"))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(review)
	}))
	t.Cleanup(kube.Close)
	// Local Radar has a kubeconfig identity even with no proxy user. Its
	// access must still gate Argo's typically more privileged token.
	client, err := kubernetes.NewForConfig(&rest.Config{Host: kube.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	restore := k8s.SetTestInformerClient(client, testFakeClient)
	t.Cleanup(restore)
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{argoApplicationGVR: "ApplicationList"}, app)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{
		{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application", Name: "applications", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}},
		{Version: "v1", Kind: "ConfigMap", Name: "configmaps", Namespaced: true, Verbs: []string{"get", "list", "watch"}},
		{Version: "v1", Kind: "Secret", Name: "secrets", Namespaced: true, Verbs: []string{"get", "list", "watch"}},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)

	var wrongApp, changeDuringRender, readmitDuringRender atomic.Bool
	var identityReads, renderReads atomic.Int32
	argo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/version":
			fmt.Fprint(w, `{"Version":"v3.3.0"}`)
		case "/api/v1/session/userinfo":
			fmt.Fprint(w, `{"loggedIn":true,"username":"example"}`)
		case "/api/v1/applications/demo":
			read := identityReads.Add(1)
			uid := "local-app"
			if wrongApp.Load() {
				uid = "different-app"
			}
			gen := 3
			if changeDuringRender.Load() && read == 2 {
				gen = 4
			}
			fmt.Fprintf(w, `{"metadata":{"uid":%q,"generation":%d}}`, uid, gen)
		case "/api/v1/applications/demo/manifests":
			read := renderReads.Add(1)
			sha := r.URL.Query().Get("revision")
			want := previous
			if read == 2 {
				want = selected
				if readmitDuringRender.Load() {
					status := k8s.GetConnectionStatus()
					k8s.RetireServingConnection()
					k8s.PublishTestConnectionStatus(status)
				}
			}
			if sha != want {
				t.Errorf("render revision=%s want %s", sha, want)
			}
			json.NewEncoder(w).Encode(map[string][]string{"manifests": {
				fmt.Sprintf(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"site"},"data":{"version":%q}}`, sha),
				fmt.Sprintf(`{"apiVersion":"v1","kind":"Secret","metadata":{"name":"restricted-thing"},"data":{"private":%q}}`, sha),
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(argo.Close)
	connectArgo(t, argo.URL)
	request := func(query string) *httptest.ResponseRecorder {
		t.Helper()
		identityReads.Store(0)
		renderReads.Store(0)
		w := httptest.NewRecorder()
		(&Server{}).handleArgoSyncDiff(w, newDiffRequest("default", "demo", query))
		return w
	}
	query := "syncID=2&revision=" + selected + "&previous=" + previous
	denied := httptest.NewRecorder()
	(&Server{}).handleArgoSyncDiff(denied, newDiffRequest("broken", "demo", query))
	if denied.Code != 403 || identityReads.Load() != 0 || renderReads.Load() != 0 {
		t.Fatalf("root access denial: status=%d upstreamReads=%d/%d", denied.Code, identityReads.Load(), renderReads.Load())
	}
	w := request(query)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"partial":true`) || !strings.Contains(w.Body.String(), `"name":"site"`) || strings.Contains(w.Body.String(), "restricted-thing") || renderReads.Load() != 2 {
		t.Fatalf("status=%d body=%s renders=%d", w.Code, w.Body, renderReads.Load())
	}
	if w := request("syncID=99&revision=" + selected + "&previous=" + previous); w.Code != 409 || identityReads.Load() != 0 {
		t.Fatalf("unrecorded pair returned %d after %d upstream identity reads", w.Code, identityReads.Load())
	}
	wrongApp.Store(true)
	if w := request(query); w.Code != 409 || renderReads.Load() != 0 {
		t.Fatalf("wrong instance returned %d after %d renders", w.Code, renderReads.Load())
	}
	wrongApp.Store(false)
	changeDuringRender.Store(true)
	if w := request(query); w.Code != 409 || strings.Contains(w.Body.String(), "site") {
		t.Fatalf("changed identity: %d %s", w.Code, w.Body)
	}
	changeDuringRender.Store(false)
	readmitDuringRender.Store(true)
	if w := request(query); w.Code != 409 || strings.Contains(w.Body.String(), "site") {
		t.Fatalf("changed admission: %d %s", w.Code, w.Body)
	}
}

func TestCompareArgoRendersNamedWorkloadLists(t *testing.T) {
	wrap := func(group, kind, containers string) string {
		return fmt.Sprintf(`{"apiVersion":%q,"kind":%q,"metadata":{"name":"web"},"spec":{"template":{"spec":{"containers":%s}}}}`, group, kind, containers)
	}
	allow := func(_, _, ns, _ string) (string, bool) { return ns, true }
	old := `[{"name":"main","image":"main:v1","env":[{"name":"A","value":"a"},{"name":"B","value":"b"}],"volumeMounts":[{"name":"cache","mountPath":"/a"},{"name":"cache","mountPath":"/b"}]}]`
	newer := `[{"name":"sidecar","image":"side:v1"},{"name":"main","image":"main:v1","env":[{"name":"A","value":"a"},{"name":"NEW","value":"x"},{"name":"B","value":"b"}],"volumeMounts":[{"name":"cache","mountPath":"/b"},{"name":"cache","mountPath":"/a","readOnly":true}]}]`
	compare := func(group, kind, left, right string) []renderedField {
		t.Helper()
		got, err := compareArgoRenders([]string{wrap(group, kind, left)}, []string{wrap(group, kind, right)}, allow)
		if err != nil || len(got.Changes) != 1 {
			t.Fatalf("%v: %+v", err, got)
		}
		return got.Changes[0].Fields
	}
	fields := compare("apps/v1", "Deployment", old, newer)
	field := func(fields []renderedField, relpath ...string) *renderedField {
		path := append([]string{"spec", "template", "spec", "containers"}, relpath...)
		for _, f := range fields {
			if slices.Equal(f.Path, path) {
				return &f
			}
		}
		return nil
	}
	assertValues := func(f *renderedField, before, after string) {
		t.Helper()
		if f == nil || string(f.Before) != before || string(f.After) != after {
			t.Fatalf("field=%+v want %s → %s", f, before, after)
		}
	}
	if field(fields, "[name=main]", "image") != nil {
		t.Fatal("sidecar insertion manufactured a main image update")
	}
	assertValues(field(fields, "[name=sidecar]", "image"), "", `"side:v1"`)
	assertValues(field(fields, "[order]"), `["main"]`, `["sidecar","main"]`)
	assertValues(field(fields, "[name=main]", "env", "[order]"), `["A","B"]`, `["A","NEW","B"]`)
	assertValues(field(fields, "[name=main]", "volumeMounts", "[mountPath=/a]", "readOnly"), "", `true`)
	assertValues(field(fields, "[name=main]", "volumeMounts", "[order]"), `["/a","/b"]`, `["/b","/a"]`)
	one, two := `{"name":"one","image":"v1"}`, `{"name":"two","image":"v2"}`
	fields = compare("argoproj.io/v1alpha1", "Rollout", "["+one+","+two+"]", "["+two+","+one+"]")
	if len(fields) != 1 {
		t.Fatalf("reorded Rollout containers produced %+v", fields)
	}
	assertValues(field(fields, "[order]"), `["one","two"]`, `["two","one"]`)
	for _, tc := range []struct{ group, kind, a, b string }{
		{"example.io/v1", "Deployment", one, two},
		{"batch/v1", "Deployment", one, two},
		{"apps/v1", "Deployment", `{"name":"same","image":"v1"}`, `{"name":"same","image":"v2"}`},
		{"apps/v1", "Deployment", `{"image":"v1"}`, `{"image":"v2"}`},
	} {
		fields := compare(tc.group, tc.kind, "["+tc.a+","+tc.b+"]", "["+tc.b+","+tc.a+"]")
		if field(fields, "0", "image") == nil || field(fields, "[order]") != nil {
			t.Fatalf("%s/%s ambiguous or custom list should use indexes: %+v", tc.group, tc.kind, fields)
		}
	}
}

func TestCompareArgoRendersClusterScopeStampedNamespace(t *testing.T) {
	readNS := "unchecked"
	resolve := argoRenderResolver("default", func(kind, group string) (k8s.APIResource, bool) {
		return k8s.APIResource{Kind: kind, Group: group, Namespaced: false}, true
	}, func(_ k8s.APIResource, ns, name string) bool { readNS = ns; return ns == "" && name == "demo" })
	before := []string{`{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"demo","namespace":"one"}}`}
	after := []string{`{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"demo","namespace":"two"}}`}
	diff, err := compareArgoRenders(before, after, resolve)
	if err != nil || readNS != "" || diff.Partial || len(diff.Changes) != 1 || diff.Changes[0].Namespace != "" || diff.Changes[0].Operation != "modified" || !slices.Equal(diff.Changes[0].Fields[0].Path, []string{"metadata", "namespace"}) {
		t.Fatalf("scope=%q diff=%+v err=%v", readNS, diff, err)
	}
}
