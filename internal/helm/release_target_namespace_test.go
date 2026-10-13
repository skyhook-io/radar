package helm

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"slices"
	"strings"
	"testing"

	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/kube"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage"
	"helm.sh/helm/v3/pkg/storage/driver"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func targetNamespaceManifest(names ...string) string {
	var docs []string
	for _, name := range names {
		namespace := ""
		if name == "explicit" {
			namespace = "  namespace: explicit\n"
		}
		if name == "retained" {
			namespace += "  annotations:\n    helm.sh/resource-policy: keep\n"
		}
		docs = append(docs, fmt.Sprintf("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: %s\n%sdata:\n  message: old\n", name, namespace))
	}
	return strings.Join(docs, "\n---\n")
}

func targetNamespaceActionConfig(t *testing.T) (*action.Configuration, *[]string) {
	t.Helper()
	cfg := memoryActionConfig(t)
	cfg.Releases = storage.Init(driver.NewSecrets(fake.NewClientset().CoreV1().Secrets("store")))
	objects := map[string]*corev1.ConfigMap{}
	for _, name := range []string{"update", "remove", "retained", "explicit"} {
		ns := "app"
		if name == "explicit" {
			ns = "explicit"
		}
		obj := &corev1.ConfigMap{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"}, ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns}, Data: map[string]string{"message": "old"}}
		if name == "retained" {
			obj.Annotations = map[string]string{kube.ResourcePolicyAnno: kube.KeepPolicy}
		}
		objects[ns+"/"+name] = obj
	}
	var writes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/version":
			writeTestJSON(t, w, map[string]string{"gitVersion": "v1.36.2"})
			return
		case "/openapi/v3":
			if _, err := fmt.Fprint(w, `{"paths":{"api/v1":{"serverRelativeURL":"/openapi/v3/api/v1"}}}`); err != nil {
				t.Error(err)
			}
			return
		case "/openapi/v3/api/v1":
			if _, err := fmt.Fprint(w, `{"openapi":"3.0.0","info":{"title":"fixture","version":"v1"},"paths":{"/api/v1/namespaces/{namespace}/configmaps/{name}":{"patch":{"x-kubernetes-group-version-kind":{"group":"","version":"v1","kind":"ConfigMap"},"parameters":[{"name":"fieldValidation","in":"query","schema":{"type":"string"}}],"responses":{"200":{"description":"ok"}}}}}}`); err != nil {
				t.Error(err)
			}
			return
		case "/api/v1/namespaces/app", "/api/v1/namespaces/explicit":
			writeTestJSON(t, w, corev1.Namespace{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"}, ObjectMeta: metav1.ObjectMeta{Name: path.Base(r.URL.Path)}})
			return
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) < 5 || parts[4] != "configmaps" || (parts[3] != "app" && parts[3] != "explicit") {
			t.Errorf("wrong target namespace: %s %s", r.Method, r.URL.Path)
			writeK8sStatus(t, w, 404, "NotFound", "unexpected namespace")
			return
		}
		key := parts[3] + "/" + path.Base(r.URL.Path)
		if r.Method == "POST" {
			obj := &corev1.ConfigMap{}
			if err := json.NewDecoder(r.Body).Decode(obj); err != nil {
				t.Error(err)
			}
			if obj.Namespace != parts[3] {
				t.Errorf("object namespace = %q, request = %q", obj.Namespace, parts[3])
			}
			objects[parts[3]+"/"+obj.Name] = obj
			writes = append(writes, "POST "+parts[3]+"/"+obj.Name)
			writeTestJSON(t, w, obj)
			return
		}
		obj, ok := objects[key]
		if !ok {
			writeK8sStatus(t, w, 404, "NotFound", "absent")
			return
		}
		switch r.Method {
		case "DELETE":
			writes = append(writes, "DELETE "+key)
			delete(objects, key)
			writeTestJSON(t, w, metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: "Success"})
			return
		case "PATCH":
			writes = append(writes, "PATCH "+key)
		}
		writeTestJSON(t, w, obj)
	}))
	t.Cleanup(srv.Close)
	getter := newRESTConfigGetter(&rest.Config{Host: srv.URL}, "store", "", nil)
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{{Version: "v1"}})
	mapper.Add(schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, meta.RESTScopeNamespace)
	getter.mapper = mapper
	cfg.KubeClient = kube.New(getter)
	cfg.KubeClient.(*kube.Client).Log = func(string, ...any) {}
	cfg.RESTClientGetter = getter
	return cfg, &writes
}

func assertTargetNamespaceStorage(t *testing.T, cfg *action.Configuration) {
	t.Helper()
	ns, _, err := cfg.RESTClientGetter.(*restConfigGetter).ToRawKubeConfigLoader().Namespace()
	if err != nil || ns != "store" {
		t.Fatalf("storage getter moved: %q %v", ns, err)
	}
	if cfg.KubeClient.(*kube.Client).Namespace != "app" {
		t.Fatal("object builder does not target app")
	}
}

func TestReleaseTargetNamespaceAndLiveRollbackEffects(t *testing.T) {
	for _, operation := range []string{"rollback", "rollback with progress", "uninstall"} {
		t.Run(operation, func(t *testing.T) {
			cfg, writes := targetNamespaceActionConfig(t)
			seedRelease(t, cfg, "demo", release.StatusSuperseded, 1)
			target, _ := cfg.Releases.Get("demo", 1)
			target.Namespace = "app"
			target.Manifest = targetNamespaceManifest("update", "create", "explicit")
			target.Chart = &chart.Chart{Metadata: &chart.Metadata{Name: "radar", Version: "1.0.0"}}
			for _, hook := range []string{"hook", "explicit-hook"} {
				manifest := fmt.Sprintf("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: %s\ndata:\n  message: hook\n", hook)
				if hook == "explicit-hook" {
					manifest = strings.Replace(manifest, "  name: explicit-hook\n", "  name: explicit-hook\n  namespace: explicit\n", 1)
				}
				target.Hooks = append(target.Hooks, &release.Hook{Name: hook, Kind: "ConfigMap", Path: "templates/" + hook + ".yaml", Manifest: manifest, Events: []release.HookEvent{release.HookPreRollback}})
			}
			if err := cfg.Releases.Update(target); err != nil {
				t.Fatal(err)
			}
			seedRelease(t, cfg, "demo", release.StatusDeployed, 2)
			current, _ := cfg.Releases.Get("demo", 2)
			current.Namespace = "app"
			current.Manifest = targetNamespaceManifest("update", "remove", "retained", "absent", "explicit")
			current.Chart = target.Chart
			if err := cfg.Releases.Update(current); err != nil {
				t.Fatal(err)
			}
			client := &Client{}
			var err error
			switch operation {
			case "rollback":
				err = rollbackWith(cfg, "demo", 1, false)
			case "rollback with progress":
				ch := make(chan InstallProgress, 10)
				err = client.rollbackWithProgressUsing(cfg, "demo", 1, ch)
				if err == nil && len(ch) != 3 {
					t.Fatalf("progress events: %d", len(ch))
				}
			case "uninstall":
				err = uninstallWithOptions(cfg, "demo", UninstallOptions{})
			}
			if err != nil {
				t.Fatal(err)
			}
			assertTargetNamespaceStorage(t, cfg)
			expected := []string{"PATCH app/update", "POST app/create", "PATCH explicit/explicit", "DELETE app/remove", "POST app/hook", "POST explicit/explicit-hook"}
			if operation == "uninstall" {
				expected = []string{"DELETE app/update", "DELETE app/remove", "DELETE explicit/explicit"}
			}
			for _, want := range expected {
				if !slices.Contains(*writes, want) {
					t.Fatalf("missing %q: %v", want, *writes)
				}
			}
			if slices.Contains(*writes, "DELETE app/retained") {
				t.Fatalf("deleted retained resource: %v", *writes)
			}
			stored, err := cfg.Releases.Last("demo")
			if operation == "uninstall" {
				if err == nil {
					t.Fatal("uninstall did not purge history")
				}
			} else if err != nil || stored.Version != 3 || stored.Namespace != "app" {
				t.Fatalf("stored rollback: %+v %v", stored, err)
			}
		})
	}
}

func TestUpgradeAndValuesUseTargetNamespace(t *testing.T) {
	for _, operation := range []string{"upgrade", "upgrade with progress", "upgrade with values", "apply values", "preview values", "install recovery"} {
		t.Run(operation, func(t *testing.T) {
			cfg, writes := targetNamespaceActionConfig(t)
			seedRelease(t, cfg, "demo", release.StatusDeployed, 1)
			rel, _ := cfg.Releases.Get("demo", 1)
			rel.Namespace = "app"
			rel.Config = map[string]any{"message": "old"}
			rel.Manifest = targetNamespaceManifest("update", "explicit")
			template := strings.ReplaceAll(rel.Manifest, "message: old", "message: {{ .Values.message }}\n  releaseNamespace: {{ .Release.Namespace }}")
			rel.Chart = &chart.Chart{Metadata: &chart.Metadata{Name: "radar", Version: "1.0.0", APIVersion: "v2"}, Templates: []*chart.File{{Name: "templates/configmap.yaml", Data: []byte(template)}}}
			if operation == "install recovery" {
				rel.Info.Status = release.StatusFailed
			}
			if err := cfg.Releases.Update(rel); err != nil {
				t.Fatal(err)
			}
			next := *rel.Chart
			metadata := *rel.Chart.Metadata
			metadata.Version = "2.0.0"
			next.Metadata = &metadata
			srv := preparedChartServer(t, &next)
			defer srv.Close()
			client := newChartSourceTestClient(t)
			repoName, err := client.ensureClassicRepository(srv.URL, "fixture")
			if err != nil {
				t.Fatal(err)
			}
			values := map[string]any{"message": "new"}
			noop := func(string, string, string) {}
			var rendered *release.Release
			switch operation {
			case "upgrade":
				err = client.upgradeWith(cfg, "demo", "2.0.0", repoName, noop)
			case "upgrade with progress":
				ch := make(chan InstallProgress, 10)
				err = client.upgradeWith(cfg, "demo", "2.0.0", repoName, progressSender(ch))
				if err == nil && len(ch) == 0 {
					t.Fatal("no progress")
				}
			case "upgrade with values":
				err = client.upgradeWithValues(cfg, "demo", "2.0.0", repoName, values, noop)
			case "apply values":
				err = client.applyValuesWith(cfg, "demo", values)
			case "preview values":
				_, err = client.previewValuesChangeWith(cfg, "demo", values, "", "")
			case "install recovery":
				rendered, err = runInstallOrUpgrade(cfg, &InstallRequest{Namespace: "store", ReleaseName: "demo", Values: values}, &next, installUpgrade)
			}
			if err != nil {
				t.Fatal(err)
			}
			assertTargetNamespaceStorage(t, cfg)
			if rendered != nil && (rendered.Namespace != "app" || strings.Count(rendered.Manifest, "releaseNamespace: app") != 2) {
				t.Fatalf("rendered namespace differs from object target: namespace=%q manifest=%s", rendered.Namespace, rendered.Manifest)
			}
			dryRun := operation == "preview values"
			if dryRun {
				if len(*writes) != 0 {
					t.Fatalf("preview wrote: %v", *writes)
				}
			} else {
				for _, want := range []string{"PATCH app/update", "PATCH explicit/explicit"} {
					if !slices.Contains(*writes, want) {
						t.Fatalf("missing %s: %v", want, *writes)
					}
				}
				if len(*writes) != 2 {
					t.Fatalf("unexpected writes: %v", *writes)
				}
			}
			stored, err := cfg.Releases.Last("demo")
			revision := 2
			if dryRun {
				revision = 1
			}
			if err != nil || stored.Version != revision || stored.Namespace != "app" {
				t.Fatalf("stored release: %+v %v", stored, err)
			}
			if !dryRun && strings.Count(stored.Manifest, "releaseNamespace: app") != 2 {
				t.Fatalf("stored manifest rendered a different release namespace: %s", stored.Manifest)
			}
		})
	}
}
