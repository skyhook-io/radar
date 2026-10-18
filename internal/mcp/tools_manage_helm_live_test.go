package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/k8s"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/release"
	authv1 "k8s.io/api/authorization/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestManageHelmLivePlanConfirmation(t *testing.T) {
	type response struct {
		Preview *helm.ReleaseActionPreview
		Confirm string
	}
	decode := func(t *testing.T, result *mcp.CallToolResult) response {
		t.Helper()
		var output response
		if err := json.Unmarshal([]byte(extractText(t, result)), &output); err != nil {
			t.Fatal(err)
		}
		return output
	}
	for _, mode := range []string{"changed", "unchanged", "preview error", "execution error", "restore current revision", "adopt", "adopt namespace"} {
		t.Run(mode, func(t *testing.T) {
			setConnectedForTest(t)
			for _, env := range []string{"HELM_CACHE_HOME", "HELM_CONFIG_HOME", "HELM_DATA_HOME"} {
				t.Setenv(env, t.TempDir())
			}
			const manifest = "apiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: config\n  namespace: default\n"
			secrets := map[string]corev1.Secret{}
			objects := map[string]corev1.ConfigMap{
				"config":   {TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"}, ObjectMeta: metav1.ObjectMeta{Name: "config", Namespace: "default"}},
				"retained": {TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"}, ObjectMeta: metav1.ObjectMeta{Name: "retained", Namespace: "default", Annotations: map[string]string{"helm.sh/resource-policy": "keep"}}},
			}
			for revision := 1; revision <= 2; revision++ {
				rel := &release.Release{Name: "demo", Namespace: "default", Version: revision, Info: &release.Info{Status: release.StatusDeployed}, Manifest: manifest, Chart: &chart.Chart{Metadata: &chart.Metadata{Name: "demo", Version: "1.0.0"}}}
				if revision == 1 {
					rel.Info.Status = release.StatusSuperseded
				} else {
					rel.Manifest += "---\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: retained\n  namespace: default\n"
				}
				if mode == "restore current revision" {
					if revision == 1 {
						continue
					}
					rel.Manifest = manifest
				}
				if strings.HasPrefix(mode, "adopt") && revision == 2 {
					rel.Manifest = ""
				}
				raw, err := json.Marshal(rel)
				if err != nil {
					t.Fatal(err)
				}
				n := fmt.Sprintf("sh.helm.release.v1.demo.v%d", revision)
				secrets[n] = corev1.Secret{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"}, ObjectMeta: metav1.ObjectMeta{Name: n, Namespace: "default", Labels: map[string]string{"owner": "helm", "name": "demo", "status": rel.Info.Status.String()}}, Data: map[string][]byte{"release": []byte(base64.StdEncoding.EncodeToString(raw))}}
			}
			if strings.HasPrefix(mode, "adopt") {
				obj := objects["config"]
				ownerName := "other"
				if mode == "adopt namespace" {
					ownerName = "demo"
				}
				obj.Annotations = map[string]string{"meta.helm.sh/release-name": ownerName, "meta.helm.sh/release-namespace": "other-ns"}
				objects["config"] = obj
			}
			writes := 0
			status := func(w http.ResponseWriter, code int, reason, message string) {
				w.WriteHeader(code)
				json.NewEncoder(w).Encode(metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: "Failure", Reason: metav1.StatusReason(reason), Code: int32(code), Message: message})
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/subjectaccessreviews") {
					json.NewEncoder(w).Encode(authv1.SubjectAccessReview{TypeMeta: metav1.TypeMeta{APIVersion: "authorization.k8s.io/v1", Kind: "SubjectAccessReview"}, Status: authv1.SubjectAccessReviewStatus{Allowed: true}})
					return
				}
				switch r.URL.Path {
				case "/version":
					json.NewEncoder(w).Encode(map[string]string{"gitVersion": "v1.34.0"})
					return
				case "/api":
					json.NewEncoder(w).Encode(metav1.APIVersions{Versions: []string{"v1"}})
					return
				case "/apis":
					json.NewEncoder(w).Encode(metav1.APIGroupList{})
					return
				case "/api/v1":
					json.NewEncoder(w).Encode(metav1.APIResourceList{GroupVersion: "v1", APIResources: []metav1.APIResource{{Name: "configmaps", Kind: "ConfigMap", Namespaced: true}, {Name: "secrets", Kind: "Secret", Namespaced: true}, {Name: "namespaces", Kind: "Namespace"}}})
					return
				case "/api/v1/namespaces/default":
					json.NewEncoder(w).Encode(corev1.Namespace{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"}, ObjectMeta: metav1.ObjectMeta{Name: "default"}})
					return
				}
				n := path.Base(r.URL.Path)
				if strings.Contains(r.URL.Path, "/secrets") {
					if mode == "preview error" {
						status(w, 400, "BadRequest", "password=preview-secret-value")
						return
					}
					switch r.Method {
					case "GET":
						if n == "secrets" {
							selector, err := labels.Parse(r.URL.Query().Get("labelSelector"))
							if err != nil {
								t.Error(err)
							}
							list := corev1.SecretList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "SecretList"}, Items: []corev1.Secret{}}
							for _, secret := range secrets {
								if selector.Matches(labels.Set(secret.Labels)) {
									list.Items = append(list.Items, secret)
								}
							}
							json.NewEncoder(w).Encode(list)
							return
						}
						if secret, ok := secrets[n]; ok {
							json.NewEncoder(w).Encode(secret)
							return
						}
					case "POST", "PUT":
						var secret corev1.Secret
						if err := json.NewDecoder(r.Body).Decode(&secret); err != nil {
							t.Error(err)
						}
						secrets[secret.Name] = secret
						writes++
						json.NewEncoder(w).Encode(secret)
						return
					case "DELETE":
						delete(secrets, n)
						writes++
						json.NewEncoder(w).Encode(metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: "Success"})
						return
					}
				} else if strings.Contains(r.URL.Path, "/configmaps") {
					if r.Method != "GET" && mode == "execution error" {
						status(w, 400, "BadRequest", "password=execution-secret-value")
						return
					}
					if r.Method == "POST" {
						var obj corev1.ConfigMap
						json.NewDecoder(r.Body).Decode(&obj)
						objects[obj.Name] = obj
						writes++
						json.NewEncoder(w).Encode(obj)
						return
					}
					if obj, ok := objects[n]; ok {
						if r.Method == "DELETE" {
							delete(objects, n)
							writes++
							json.NewEncoder(w).Encode(metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: "Success"})
							return
						}
						if r.Method != "GET" {
							writes++
						}
						json.NewEncoder(w).Encode(obj)
						return
					}
				} else {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				status(w, 404, "NotFound", "absent")
			}))
			defer srv.Close()
			previousClient := k8s.SetTestClient(kubernetes.NewForConfigOrDie(&rest.Config{Host: srv.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json"}}))
			t.Cleanup(func() { k8s.SetTestClient(previousClient) })
			ctx := auth.ContextWithUser(context.Background(), &auth.User{Username: "helm-live-" + mode})
			helm.ResetClient()
			t.Cleanup(helm.ResetClient)
			if err := helm.InitializeWithRESTConfig(&rest.Config{Host: srv.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json"}}); err != nil {
				t.Fatal(err)
			}
			input := manageHelmReleaseInput{Action: "rollback", Namespace: "default", Name: "demo", Revision: 1, NoHooks: true}
			if mode == "execution error" {
				delete(objects, "config")
			}
			if mode == "restore current revision" {
				input.Action = "uninstall"
				input.Revision = 0
				input.KeepHistory = true
				out, _, err := handleManageHelmRelease(ctx, nil, input)
				if err != nil {
					t.Fatal(err)
				}
				input.Confirm = decode(t, out).Confirm
				no := false
				input.DryRun = &no
				if _, _, err = handleManageHelmRelease(ctx, nil, input); err != nil {
					t.Fatal(err)
				}
				input = manageHelmReleaseInput{Action: "rollback", Namespace: "default", Name: "demo", Revision: 2, NoHooks: true}
			}
			result, _, err := handleManageHelmRelease(ctx, nil, input)
			if mode == "preview error" {
				if err == nil || strings.Contains(err.Error(), "preview-secret-value") || !strings.Contains(err.Error(), "[REDACTED]") {
					t.Fatalf("preview error not redacted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			output := decode(t, result)
			preview := output.Preview
			if strings.HasPrefix(mode, "adopt") {
				owner := "other-ns/other"
				if mode == "adopt namespace" {
					owner = "other-ns/demo"
				}
				if preview.Resources[0].Effect != "adopt (exists live, not in current revision)" || !strings.Contains(preview.Summary, "adopts 1 resource") || !strings.Contains(strings.Join(preview.Warnings, " "), owner) {
					t.Fatalf("adoption hidden: %+v", preview)
				}
			} else if mode == "restore current revision" {
				if preview.Resources[0].Effect != "create" {
					t.Fatalf("restoration not live: %+v", preview)
				}
			} else if !strings.Contains(preview.Summary, "keeps 1 resource") {
				t.Fatalf("not enriched: %+v", preview)
			}
			if mode == "changed" {
				obj := objects["retained"]
				obj.Annotations = nil
				objects["retained"] = obj
			}
			no := false
			input.DryRun = &no
			input.Confirm = output.Confirm
			before := writes
			result, _, err = handleManageHelmRelease(ctx, nil, input)
			if mode == "changed" {
				if err == nil || !strings.Contains(err.Error(), "the live plan changed since the preview:") || !strings.Contains(err.Error(), "run dry_run=true again") || writes != before {
					t.Fatalf("changed live plan executed: writes=%d err=%v", writes-before, err)
				}
				return
			}
			if mode == "execution error" {
				if err == nil || strings.Contains(err.Error(), "execution-secret-value") || !strings.Contains(err.Error(), "[REDACTED]") {
					t.Fatalf("execution error not redacted: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			confirmed := decode(t, result).Preview
			if confirmed.Summary != preview.Summary {
				t.Fatalf("confirm summary %q != preview %q", confirmed.Summary, preview.Summary)
			}
			if mode == "restore current revision" {
				if _, ok := objects["config"]; !ok {
					t.Fatal("rollback did not recreate object")
				}
				if _, ok := secrets["sh.helm.release.v1.demo.v3"]; !ok {
					t.Fatal("rollback did not create revision")
				}
			}
		})
	}
}
