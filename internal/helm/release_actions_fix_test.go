package helm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"path"
	"slices"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/kube"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage/driver"
	helmtime "helm.sh/helm/v3/pkg/time"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestReleaseActionEmptyDocumentsAndPolicies(t *testing.T) {
	cfg := memoryActionConfig(t)
	rel := actionTestRelease(t, cfg, 1, release.StatusDeployed)
	rel.Manifest += "\n---\n# only a comment\n---\nnull\n"
	for _, policy := range []string{" KEEP ", "other", ""} {
		rel.Manifest = strings.ReplaceAll(rel.Manifest, "helm.sh/resource-policy: keep", fmt.Sprintf("helm.sh/resource-policy: %q", policy))
		cfg.Releases.Update(rel)
		preview, err := PreviewReleaseAction(cfg, "demo", ReleaseActionOptions{Action: "uninstall"})
		if err != nil || len(preview.Resources) != 2 {
			t.Fatalf("comment/null preview: %+v %v", preview, err)
		}
		if preview.Resources[1].Effect == "delete" {
			t.Fatalf("SDK never deletes an annotated manifest: %+v", preview.Resources[1])
		}
		rel.Manifest = strings.ReplaceAll(rel.Manifest, fmt.Sprintf("helm.sh/resource-policy: %q", policy), "helm.sh/resource-policy: keep")
	}
}

func TestReleaseActionRetryAndPendingGuidance(t *testing.T) {
	cfg := memoryActionConfig(t)
	actionTestRelease(t, cfg, 1, release.StatusSuperseded)
	rel := actionTestRelease(t, cfg, 2, release.StatusUninstalling)
	rel.Info.Deleted = helmtime.Now()
	rel.Hooks[0].LastRun.Phase = release.HookPhaseRunning
	rel.Hooks[0].DeletePolicies = []release.HookDeletePolicy{release.HookSucceeded}
	cfg.Releases.Update(rel)
	preview, err := PreviewReleaseAction(cfg, "demo", ReleaseActionOptions{Action: "uninstall"})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Deleted == nil || preview.LastDeployed == nil || !strings.Contains(preview.Hooks[0].StatusMeaning, "last recorded phase") || !strings.Contains(strings.Join(preview.Warnings, " "), "AlreadyExists") {
		t.Fatalf("retry preview: %+v", preview)
	}
	if err := EnrichReleaseActionPreview(context.Background(), cfg, "store", preview); err != nil {
		t.Fatal(err)
	}
	if len(preview.HookDiagnostics) != 1 {
		t.Fatalf("diagnostics not reused: %+v", preview)
	}
	skipped, err := PreviewReleaseAction(cfg, "demo", ReleaseActionOptions{Action: "uninstall", NoHooks: true})
	if err != nil || strings.Contains(strings.Join(skipped.Warnings, " "), "AlreadyExists") {
		t.Fatalf("skipped hook retry guidance: %+v %v", skipped, err)
	}
	if !strings.Contains(strings.Join(preview.Warnings, " "), "A GitOps controller may reconcile this release back") {
		t.Fatalf("missing generic GitOps guidance: %+v", preview)
	}
	rel.Info.Status = release.StatusPendingUpgrade
	cfg.Releases.Update(rel)
	preview, err = PreviewReleaseAction(cfg, "demo", ReleaseActionOptions{Action: "rollback", Revision: 1})
	if err != nil || !strings.Contains(strings.Join(preview.Warnings, " "), "does not lock pending") {
		t.Fatalf("pending preview: %+v %v", preview, err)
	}
}

func TestReleaseActionGuardSeparatesContextAndStorage(t *testing.T) {
	done, err := BeginReleaseAction("store-a", "demo")
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	if _, err := BeginReleaseAction("store-a", "demo"); !errors.Is(err, ErrReleaseActionInProgress) {
		t.Fatalf("overlap allowed: %v", err)
	}
	other, err := BeginReleaseAction("store-b", "demo")
	if err != nil {
		t.Fatal(err)
	}
	other()
	previous := k8s.SetTestContextName("different-context")
	t.Cleanup(func() { k8s.SetTestContextName(previous) })
	other, err = BeginReleaseAction("store-a", "demo")
	if err != nil {
		t.Fatal(err)
	}
	other()
	k8s.SetTestContextName(previous)
	done()
	other, err = BeginReleaseAction("store-a", "demo")
	if err != nil {
		t.Fatal(err)
	}
	other()
}

func TestReleaseTargetNamespaceAndLiveRollbackEffects(t *testing.T) {
	cfg := memoryActionConfig(t)
	target := actionTestRelease(t, cfg, 1, release.StatusSuperseded)
	current := actionTestRelease(t, cfg, 2, release.StatusDeployed)
	manifest := func(names ...string) string {
		var docs []string
		for _, n := range names {
			docs = append(docs, fmt.Sprintf("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: %s\ndata:\n  message: restored\n", n))
		}
		return strings.Join(docs, "\n---\n")
	}
	target.Namespace = "app"
	target.Manifest = manifest("update", "create")
	target.Chart = &chart.Chart{Metadata: &chart.Metadata{Name: "demo", Version: "1.0.0"}}
	target.Hooks = nil
	current.Namespace = "app"
	current.Manifest = manifest("update", "remove", "retained", "absent")
	current.Hooks = nil
	if err := cfg.Releases.Create(target); err != nil {
		t.Fatal(err)
	}
	if err := cfg.Releases.Create(current); err != nil {
		t.Fatal(err)
	}
	objects := map[string]*corev1.ConfigMap{}
	for _, n := range []string{"update", "remove", "retained"} {
		objects[n] = &corev1.ConfigMap{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"}, ObjectMeta: metav1.ObjectMeta{Name: n, Namespace: "app"}}
	}
	objects["retained"].Annotations = map[string]string{kube.ResourcePolicyAnno: kube.KeepPolicy}
	var writes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/version" {
			json.NewEncoder(w).Encode(map[string]string{"gitVersion": "v1.37.0"})
			return
		}
		if r.URL.Path == "/api/v1/namespaces/app" {
			json.NewEncoder(w).Encode(corev1.Namespace{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"}, ObjectMeta: metav1.ObjectMeta{Name: "app"}})
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/api/v1/namespaces/app/configmaps") {
			t.Errorf("wrong target namespace: %s", r.URL.Path)
			writeK8sStatus(t, w, 404, "NotFound", "unexpected path")
			return
		}
		n := path.Base(r.URL.Path)
		if r.Method == "POST" {
			obj := &corev1.ConfigMap{}
			json.NewDecoder(r.Body).Decode(obj)
			objects[obj.Name] = obj
			writes = append(writes, "POST "+obj.Name)
			json.NewEncoder(w).Encode(obj)
			return
		}
		obj, ok := objects[n]
		if !ok {
			writeK8sStatus(t, w, 404, "NotFound", "absent")
			return
		}
		if r.Method == "DELETE" {
			writes = append(writes, "DELETE "+n)
			delete(objects, n)
			json.NewEncoder(w).Encode(metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: "Success"})
			return
		}
		if r.Method == "PATCH" {
			writes = append(writes, "PATCH "+n)
		}
		json.NewEncoder(w).Encode(obj)
	}))
	defer srv.Close()
	getter := newRESTConfigGetter(&rest.Config{Host: srv.URL}, "store", "", nil)
	mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{{Version: "v1"}})
	mapper.Add(schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, meta.RESTScopeNamespace)
	getter.mapper = mapper
	cfg.KubeClient = kube.New(getter)
	cfg.KubeClient.(*kube.Client).Log = func(string, ...any) {}
	cfg.RESTClientGetter = getter
	preview, err := PreviewReleaseAction(cfg, "demo", ReleaseActionOptions{Action: "rollback", Revision: 1, NoHooks: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := EnrichReleaseActionPreview(context.Background(), cfg, "store", preview); err != nil {
		t.Fatal(err)
	}
	effects := map[string]string{}
	for _, r := range preview.Resources {
		if r.Namespace != "app" {
			t.Fatalf("preview namespace: %+v", r)
		}
		effects[r.Name] = r.Effect
	}
	for n, want := range map[string]string{"update": "update", "create": "create", "remove": "delete", "retained": "keep (live helm.sh/resource-policy)", "absent": "not deleted (already absent)"} {
		if effects[n] != want {
			t.Fatalf("effect %s=%s, want %s", n, effects[n], want)
		}
	}
	if err := RunReleaseAction(cfg, "demo", preview.Options, preview.Fingerprint); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(writes, "POST create") || !slices.Contains(writes, "DELETE remove") || slices.Contains(writes, "DELETE retained") {
		t.Fatalf("rollback writes: %v", writes)
	}
	// The getter still resolves storage while the object builder targets app.
	ns, _, _ := getter.ToRawKubeConfigLoader().Namespace()
	if ns != "store" {
		t.Fatalf("storage namespace moved to %q", ns)
	}
	writes = nil
	if err := uninstallWithOptions(cfg, "demo", UninstallOptions{NoHooks: true, KeepHistory: true}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(writes, "DELETE create") || !slices.Contains(writes, "DELETE update") {
		t.Fatalf("uninstall writes: %v", writes)
	}
	stored, err := cfg.Releases.Last("demo")
	if err != nil || stored.Info.Status != release.StatusUninstalled {
		t.Fatalf("storage lost: %+v %v", stored, err)
	}
}

func TestUninstallHandlerDryRunAndRefusalStatuses(t *testing.T) {
	cfg := memoryActionConfig(t)
	rel := actionTestRelease(t, cfg, 1, release.StatusDeployed)
	secret := helmReleaseSecret(t, "store", rel, true)
	secret.Labels["name"] = "demo"
	var writes int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "subjectaccessreviews") {
			json.NewEncoder(w).Encode(authv1.SubjectAccessReview{TypeMeta: metav1.TypeMeta{APIVersion: "authorization.k8s.io/v1", Kind: "SubjectAccessReview"}, Status: authv1.SubjectAccessReviewStatus{Allowed: true}})
			return
		}
		if r.Method != "GET" {
			writes++
			writeK8sStatus(t, w, 500, "InternalError", "unexpected write")
			return
		}
		if r.URL.Path == "/api/v1/namespaces/store/secrets" {
			items := []corev1.Secret{}
			if secret.Name != "" {
				items = append(items, *secret)
			}
			json.NewEncoder(w).Encode(corev1.SecretList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "SecretList"}, Items: items})
			return
		}
		writeK8sStatus(t, w, 404, "NotFound", "absent")
	}))
	defer srv.Close()
	rc := &rest.Config{Host: srv.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json"}}
	previous := globalClient
	globalClient = &Client{settings: cli.New(), restConfig: rc}
	t.Cleanup(func() { globalClient = previous })
	client := kubernetes.NewForConfigOrDie(rc)
	previousK8s := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(previousK8s) })
	var audit bytes.Buffer
	previousLog := log.Writer()
	log.SetOutput(&audit)
	t.Cleanup(func() { log.SetOutput(previousLog) })
	router := chi.NewRouter()
	NewHandlers(nil).RegisterRoutes(router)
	request := func(query string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("DELETE", "/helm/releases/store/demo?"+query, nil)
		req = req.WithContext(auth.ContextWithUser(req.Context(), &auth.User{Username: "e2-handler-test"}))
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, req)
		return rec
	}
	rec := request("dry_run=true&no_hooks=true&keep_history=true")
	if rec.Code != 200 || writes != 0 || strings.Contains(rec.Body.String(), `"confirm"`) {
		t.Fatalf("dry run: %d %s writes %d", rec.Code, rec.Body, writes)
	}
	if !strings.Contains(rec.Body.String(), `"dry_run":true`) || !strings.Contains(audit.String(), `outcome="preview"`) || !strings.Contains(audit.String(), "revision=1 no_hooks=true keep_history=true") {
		t.Fatalf("preview/audit: %s %s", rec.Body, audit.String())
	}
	rel.Info.Status = release.StatusUninstalled
	secret = helmReleaseSecret(t, "store", rel, true)
	rec = request("dry_run=true&keep_history=true")
	if rec.Code != 409 {
		t.Fatalf("refusal: %d %s", rec.Code, rec.Body)
	}
	secret = &corev1.Secret{}
	rec = request("dry_run=true")
	if rec.Code != 404 {
		t.Fatalf("missing: %d %s", rec.Code, rec.Body)
	}
	if !strings.Contains(audit.String(), `outcome="preview_failed"`) {
		t.Fatalf("failed preview audit: %s", audit.String())
	}
}

func TestReleaseExecutionErrors(t *testing.T) {
	for _, action := range []string{"uninstall", "rollback"} {
		for _, tc := range []struct {
			name   string
			err    error
			status int
		}{
			{"typed denial", apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "demo", errors.New("denied")), 403},
			{"partial uninstall denial", errors.New(`uninstall failed: uninstallation completed with 1 error(s): configmaps "demo" is forbidden: User "alice" cannot delete resource "configmaps"`), 403},
			{"missing", fmt.Errorf("read release: %w", driver.ErrReleaseNotFound), 404},
			{"overlap", ErrReleaseActionInProgress, 409},
			{"refused", ErrReleaseActionRefused, 409},
			{"execution failure", errors.New("hook failed"), 500},
		} {
			t.Run(action+"/"+tc.name, func(t *testing.T) {
				var logs bytes.Buffer
				previous := log.Writer()
				log.SetOutput(&logs)
				t.Cleanup(func() { log.SetOutput(previous) })
				rec := httptest.NewRecorder()
				writeReleaseExecutionError(rec, action, "store", "demo", tc.err)
				if rec.Code != tc.status || strings.Contains(rec.Body.String(), "permissions to read") {
					t.Fatalf("execution response: %d %s", rec.Code, rec.Body)
				}
				if tc.status == 403 && !strings.Contains(rec.Body.String(), "insufficient permissions to "+action) {
					t.Fatalf("wrong denial: %s", rec.Body)
				}
				if tc.status == 500 && !strings.Contains(logs.String(), "[helm] Failed to "+action+" store/demo: hook failed") {
					t.Fatalf("missing execution log: %s", &logs)
				}
			})
		}
	}
}

func TestRollbackAuditsGateDenialsAndConflicts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var review authv1.SubjectAccessReview
		if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(authv1.SubjectAccessReview{TypeMeta: metav1.TypeMeta{APIVersion: "authorization.k8s.io/v1", Kind: "SubjectAccessReview"}, Status: authv1.SubjectAccessReviewStatus{Allowed: review.Spec.User == "rollback-allowed"}})
	}))
	defer srv.Close()
	previousClient := k8s.SetTestClient(kubernetes.NewForConfigOrDie(&rest.Config{Host: srv.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json"}}))
	t.Cleanup(func() { k8s.SetTestClient(previousClient) })
	previousStatus := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	t.Cleanup(func() { k8s.SetConnectionStatus(previousStatus) })
	previousHelm := globalClient
	globalClient = &Client{}
	t.Cleanup(func() { globalClient = previousHelm })
	done, err := BeginReleaseAction("audit-store", "demo")
	if err != nil {
		t.Fatal(err)
	}
	defer done()
	router := chi.NewRouter()
	NewHandlers(nil).RegisterRoutes(router)
	for _, route := range []string{"rollback", "rollback-stream"} {
		for _, tc := range []struct {
			user   string
			status int
		}{{"rollback-denied", 403}, {"rollback-allowed", 409}} {
			t.Run(route+"/"+tc.user, func(t *testing.T) {
				var logs bytes.Buffer
				previous := log.Writer()
				log.SetOutput(&logs)
				t.Cleanup(func() { log.SetOutput(previous) })
				req := httptest.NewRequest("POST", "/helm/releases/audit-store/demo/"+route+"?revision=2", nil)
				req = req.WithContext(auth.ContextWithUser(req.Context(), &auth.User{Username: tc.user}))
				rec := httptest.NewRecorder()
				router.ServeHTTP(rec, req)
				if rec.Code != tc.status {
					t.Fatalf("response: %d %s", rec.Code, rec.Body)
				}
				for _, want := range []string{`action="helm_rollback"`, `source="rest" outcome="failed" revision=2 no_hooks=false keep_history=false`, `ns="audit-store" name="demo"`} {
					if !strings.Contains(logs.String(), want) {
						t.Fatalf("missing %s: %s", want, &logs)
					}
				}
				if strings.Count(logs.String(), "[audit]") != 1 {
					t.Fatalf("duplicate audit: %s", &logs)
				}
				if tc.status == 403 && !strings.Contains(logs.String(), "[helm] Helm write denied") {
					t.Fatalf("missing gate log: %s", &logs)
				}
			})
		}
	}
}

func TestUpgradeAndValuesUseTargetNamespace(t *testing.T) {
	for _, operation := range []string{"upgrade", "upgrade with values", "apply values", "preview values"} {
		t.Run(operation, func(t *testing.T) {
			cfg := memoryActionConfig(t)
			rel := actionTestRelease(t, cfg, 1, release.StatusDeployed)
			rel.Namespace = "app"
			rel.Config = map[string]any{"message": "old"}
			rel.Hooks = nil
			rel.Manifest = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\ndata:\n  message: old\n"
			rel.Chart = &chart.Chart{Metadata: &chart.Metadata{Name: "demo", Version: "1.0.0"}, Templates: []*chart.File{{Name: "templates/configmap.yaml", Data: []byte("apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: demo\n  labels:\n    upgraded: \"true\"\ndata:\n  message: {{ .Values.message }}\n")}}}
			if err := cfg.Releases.Create(rel); err != nil {
				t.Fatal(err)
			}
			obj := corev1.ConfigMap{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"}, ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "app"}, Data: map[string]string{"message": "old"}}
			var writes []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if r.URL.Path == "/openapi/v3" {
					w.Write([]byte(`{"paths":{"api/v1":{"serverRelativeURL":"/openapi/v3/api/v1"}}}`))
					return
				}
				if r.URL.Path == "/openapi/v3/api/v1" {
					w.Write([]byte(`{"openapi":"3.0.0","info":{"title":"fixture","version":"v1"},"paths":{"/api/v1/namespaces/{namespace}/configmaps/{name}":{"patch":{"x-kubernetes-group-version-kind":{"group":"","version":"v1","kind":"ConfigMap"},"parameters":[{"name":"fieldValidation","in":"query","schema":{"type":"string"}}],"responses":{"200":{"description":"ok"}}}}}}`))
					return
				}
				if r.URL.Path == "/version" {
					json.NewEncoder(w).Encode(map[string]string{"gitVersion": "v1.37.0"})
					return
				}
				if r.URL.Path != "/api/v1/namespaces/app/configmaps/demo" {
					t.Errorf("wrong target: %s %s", r.Method, r.URL.Path)
					writeK8sStatus(t, w, 404, "NotFound", "unexpected target")
					return
				}
				if r.Method == "PATCH" {
					writes = append(writes, r.Method+" "+r.URL.Path)
				}
				json.NewEncoder(w).Encode(obj)
			}))
			defer srv.Close()
			getter := newRESTConfigGetter(&rest.Config{Host: srv.URL}, "store", "", nil)
			mapper := meta.NewDefaultRESTMapper([]schema.GroupVersion{{Version: "v1"}})
			mapper.Add(schema.GroupVersionKind{Version: "v1", Kind: "ConfigMap"}, meta.RESTScopeNamespace)
			getter.mapper = mapper
			cfg.KubeClient = kube.New(getter)
			cfg.KubeClient.(*kube.Client).Log = func(string, ...any) {}
			cfg.RESTClientGetter = getter
			client := &Client{}
			var err error
			values := map[string]any{"message": "new"}
			noop := func(string, string, string) {}
			switch operation {
			case "upgrade":
				err = client.upgradeWith(cfg, "demo", "1.0.0", "", noop)
			case "upgrade with values":
				err = client.upgradeWithValues(cfg, "demo", "1.0.0", "", values, noop)
			case "apply values":
				err = client.applyValuesWith(cfg, "demo", values)
			case "preview values":
				_, err = client.previewValuesChangeWith(cfg, "demo", values, "", "")
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.KubeClient.(*kube.Client).Namespace != "app" {
				t.Fatal("object builder does not target app")
			}
			if operation == "preview values" {
				if len(writes) != 0 {
					t.Fatalf("preview wrote: %v", writes)
				}
			} else if len(writes) != 1 {
				t.Fatalf("upgrade writes: %v", writes)
			}
			ns, _, _ := getter.ToRawKubeConfigLoader().Namespace()
			if ns != "store" {
				t.Fatalf("storage moved: %s", ns)
			}
			stored, err := cfg.Releases.Last("demo")
			wantRevision := 2
			if operation == "preview values" {
				wantRevision = 1
			}
			if err != nil || stored.Version != wantRevision || stored.Namespace != "app" {
				t.Fatalf("release: %+v %v", stored, err)
			}
		})
	}
}
