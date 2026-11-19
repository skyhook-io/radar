package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/release"
	helmtime "helm.sh/helm/v3/pkg/time"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

// get_helm_release, list_helm_releases and get_dashboard for two callers on one
// cache, both able to read the release stored in team-a: one sees team-a only,
// the other everything. The release runs a worker in the shared namespace whose
// image can't be pulled; only the caller who can read shared learns that.
func TestHelmToolsShowEachCallerOnlyTheStatusTheyCanRead(t *testing.T) {
	running := corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{
		Name: "app", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}},
	}}}
	pulling := corev1.PodStatus{Phase: corev1.PodPending, ContainerStatuses: []corev1.ContainerStatus{{
		Name: "worker", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "ImagePullBackOff"}},
	}}}
	client := fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "team-a"}},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "shared"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "payments-app"}, Status: running},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "shared", Name: "payments-worker"}, Status: pulling},
	)
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestState)

	rel := &release.Release{
		Name: "payments", Namespace: "team-a", Version: 1,
		Info:  &release.Info{Status: release.StatusDeployed, Description: "Install complete", LastDeployed: helmtime.Unix(1, 0)},
		Chart: &chart.Chart{Metadata: &chart.Metadata{Name: "platform-app", Version: "0.1.0", AppVersion: "1.0.0"}},
		Manifest: `apiVersion: v1
kind: Pod
metadata:
  name: payments-app
  namespace: team-a
---
apiVersion: v1
kind: Pod
metadata:
  name: payments-worker
  namespace: shared
`,
	}
	payload, err := json.Marshal(rel)
	if err != nil {
		t.Fatal(err)
	}
	secrets, err := json.Marshal(corev1.SecretList{
		TypeMeta: metav1.TypeMeta{Kind: "SecretList", APIVersion: "v1"},
		Items: []corev1.Secret{{
			ObjectMeta: metav1.ObjectMeta{Name: "sh.helm.release.v1.payments.v1", Namespace: "team-a", Labels: map[string]string{
				"owner": "helm", "name": "payments", "status": "deployed", "version": "1",
			}},
			Data: map[string][]byte{"release": []byte(base64.StdEncoding.EncodeToString(payload))},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/version" {
			_, _ = w.Write([]byte(`{"major":"1","minor":"30","gitVersion":"v1.30.0"}`))
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/secrets") {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(secrets)
	}))
	t.Cleanup(storage.Close)
	helm.ResetClient()
	if err := helm.InitializeWithRESTConfig(&rest.Config{Host: storage.URL}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(helm.ResetClient)

	devA := &pkgauth.UserPermissions{AllowedNamespaces: []string{"team-a"}}
	admin := &pkgauth.UserPermissions{}
	admin.SetCanI("list", "", "secrets", "", true)
	getPermCache().Set("dev-a", nil, devA)
	getPermCache().Set("admin", nil, admin)
	t.Cleanup(getPermCache().Invalidate)

	call := func(t *testing.T, user string, run func(context.Context) (string, error)) string {
		t.Helper()
		out, err := run(pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: user}))
		if err != nil {
			t.Fatalf("%s: %v", user, err)
		}
		return out
	}
	extract := func(result *mcpsdk.CallToolResult) string { return extractText(t, result) }
	tools := map[string]func(context.Context) (string, error){
		"get_helm_release": func(ctx context.Context) (string, error) {
			result, _, err := handleGetHelmRelease(ctx, nil, getHelmReleaseInput{Namespace: "team-a", Name: "payments"})
			if err != nil {
				return "", err
			}
			return extract(result), nil
		},
		"list_helm_releases": func(ctx context.Context) (string, error) {
			result, _, err := handleListHelmReleases(ctx, nil, listHelmReleasesInput{})
			if err != nil {
				return "", err
			}
			return extract(result), nil
		},
		"get_dashboard": func(ctx context.Context) (string, error) {
			result, _, err := handleGetDashboard(ctx, nil, dashboardInput{Namespace: "team-a"})
			if err != nil {
				return "", err
			}
			return extract(result), nil
		},
	}
	for name, tool := range tools {
		t.Run(name, func(t *testing.T) {
			// Admin first and last, so a result computed for one caller can't
			// be what the other gets.
			if got := call(t, "admin", tool); !strings.Contains(got, "ImagePullBackOff") {
				t.Fatalf("admin doesn't see the worker's failure: %s", got)
			}
			got := call(t, "dev-a", tool)
			if strings.Contains(got, "ImagePullBackOff") || strings.Contains(got, `"unhealthy"`) {
				t.Fatalf("dev-a sees status from the shared namespace: %s", got)
			}
			if !strings.Contains(got, `"resourceHealth":"unknown"`) || !strings.Contains(got, `"statusUnreadCount":1`) {
				t.Fatalf("dev-a isn't told the health leaves an object out: %s", got)
			}
			if again := call(t, "admin", tool); !strings.Contains(again, "ImagePullBackOff") {
				t.Fatalf("admin after dev-a: %s", again)
			}
		})
	}
}
