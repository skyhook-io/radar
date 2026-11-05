package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage/driver"
	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
)

func TestManageHelmRejectsAmbiguousAndRedactedInput(t *testing.T) {
	base := manageHelmInput{Action: "install", Namespace: "team-a", Name: "cache", Chart: "redis", Repository: "https://charts.example", Version: "1.2.3"}
	if err := base.validate(); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*manageHelmInput){
		func(x *manageHelmInput) { x.Namespace = "../other" },
		func(x *manageHelmInput) { x.Version = "" },
		func(x *manageHelmInput) { x.Version = "1" },
		func(x *manageHelmInput) { x.Version = "1.2" },
		func(x *manageHelmInput) { x.Version = ">=1.2.3" },
		func(x *manageHelmInput) { x.Action = "remove" },
		func(x *manageHelmInput) { x.Confirm = "true" },
		func(x *manageHelmInput) { x.Values = map[string]any{"auth": map[string]any{"password": "[REDACTED]"}} },
		func(x *manageHelmInput) { x.Action = "rollback"; x.Revision = 0 },
	} {
		in := base
		mutate(&in)
		if err := in.validate(); err == nil {
			t.Fatalf("accepted invalid input: %#v", in)
		}
	}
}

func TestManageHelmRefusesBusyReleaseAndChangedPreviewStatus(t *testing.T) {
	secrets := fake.NewClientset().CoreV1().Secrets("team-a")
	storage := driver.NewSecrets(secrets)
	rel := &release.Release{Name: "demo", Namespace: "team-a", Version: 1,
		Chart: &chart.Chart{Metadata: &chart.Metadata{Name: "fixture", Version: "1.0.0"}},
		Info:  &release.Info{Status: release.StatusDeployed}}
	const key = "sh.helm.release.v1.demo.v1"
	if err := storage.Create(key, rel); err != nil {
		t.Fatal(err)
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/version":
			json.NewEncoder(w).Encode(map[string]string{"gitVersion": "v1.34.0"})
		case r.Method == http.MethodPost && r.URL.Path == "/apis/authorization.k8s.io/v1/subjectaccessreviews":
			json.NewEncoder(w).Encode(authv1.SubjectAccessReview{TypeMeta: metav1.TypeMeta{APIVersion: "authorization.k8s.io/v1", Kind: "SubjectAccessReview"}, Status: authv1.SubjectAccessReviewStatus{Allowed: true}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/namespaces/team-a/secrets":
			list, err := secrets.List(r.Context(), metav1.ListOptions{LabelSelector: r.URL.Query().Get("labelSelector")})
			if err != nil {
				t.Error(err)
				return
			}
			list.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "SecretList"}
			json.NewEncoder(w).Encode(list)
		default:
			t.Errorf("unexpected request (must not mutate release): %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer api.Close()
	cfg := &rest.Config{Host: api.URL, QPS: -1, ContentConfig: rest.ContentConfig{ContentType: "application/json"}}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	oldClient, oldConfig, oldStatus := k8s.SetTestClient(client), k8s.SetTestConfig(cfg), k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	defer func() {
		k8s.SetTestClient(oldClient)
		k8s.SetTestConfig(oldConfig)
		k8s.SetConnectionStatus(oldStatus)
	}()
	ctx := auth.ContextWithUser(t.Context(), &auth.User{Username: "helm-mcp-busy"})
	for _, status := range []release.Status{release.StatusPendingInstall, release.StatusPendingUpgrade, release.StatusPendingRollback, release.StatusUninstalling} {
		rel.Info.Status = status
		if err := storage.Update(key, rel); err != nil {
			t.Fatal(err)
		}
		for _, action := range []string{"upgrade", "rollback", "uninstall"} {
			in := manageHelmInput{Action: action, Namespace: "team-a", Name: "demo"}
			if action == "rollback" {
				in.Revision = 1
			} else if action == "upgrade" {
				in.Version = "1.1.0"
			}
			_, _, err := handleManageHelm(ctx, nil, in)
			if err == nil || !strings.Contains(err.Error(), "release is "+string(status)) {
				t.Fatalf("%s %s: want busy refusal, got %v", status, action, err)
			}
		}
	}
	rel.Info.Status = release.StatusDeployed
	if err := storage.Update(key, rel); err != nil {
		t.Fatal(err)
	}
	in := manageHelmInput{Action: "uninstall", Namespace: "team-a", Name: "demo"}
	output, _, err := handleManageHelm(ctx, nil, in)
	if err != nil {
		t.Fatal(err)
	}
	var preview map[string]any
	if err := json.Unmarshal([]byte(output.Content[0].(*mcp.TextContent).Text), &preview); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(preview["message"].(string), "deployed") {
		t.Fatalf("preview omitted status: %v", preview)
	}
	in.Confirm = preview["confirm"].(string)
	// Helm can change status without changing revision or LastDeployed.
	rel.Info.Status = release.StatusFailed
	if err := storage.Update(key, rel); err != nil {
		t.Fatal(err)
	}
	_, _, err = handleManageHelm(ctx, nil, in)
	if err == nil || !strings.Contains(err.Error(), "release changed since the preview") {
		t.Fatalf("status-only change accepted: %v", err)
	}
}

func TestManageHelmDeniesBeforeFetchingChart(t *testing.T) {
	hits := 0
	repo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; http.Error(w, "must not fetch", 500) }))
	defer repo.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/apis/authorization.k8s.io/v1/subjectaccessreviews" {
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		var review authv1.SubjectAccessReview
		if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
			t.Error(err)
		}
		if review.Spec.User != "helm-mcp-denied" || review.Spec.ResourceAttributes.Namespace != "team-a" {
			t.Errorf("wrong caller/scope: %+v", review.Spec)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(authv1.SubjectAccessReview{TypeMeta: metav1.TypeMeta{APIVersion: "authorization.k8s.io/v1", Kind: "SubjectAccessReview"}, Status: authv1.SubjectAccessReviewStatus{Allowed: false}})
	}))
	defer api.Close()
	client, err := kubernetes.NewForConfig(&rest.Config{Host: api.URL, QPS: -1, ContentConfig: rest.ContentConfig{ContentType: "application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	old := k8s.SetTestClient(client)
	defer k8s.SetTestClient(old)
	status := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	defer k8s.SetConnectionStatus(status)
	ctx := auth.ContextWithUser(context.Background(), &auth.User{Username: "helm-mcp-denied"})
	_, _, err = handleManageHelm(ctx, nil, manageHelmInput{Action: "install", Namespace: "team-a", Name: "cache", Chart: "redis", Repository: repo.URL, Version: "1.2.3"})
	if err == nil || !strings.Contains(err.Error(), "permission") {
		t.Fatalf("want denied, got %v", err)
	}
	if hits != 0 {
		t.Fatalf("denied caller fetched repository %d times", hits)
	}
}

func TestManageHelmAuditsInvalidInput(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previous)
	_, _, err := handleManageHelm(t.Context(), nil, manageHelmInput{Action: "install", Namespace: "team-a", Name: "demo"})
	if err == nil || !strings.Contains(output.String(), `action="helm_install"`) || !strings.Contains(output.String(), `outcome="refused"`) {
		t.Fatalf("missing refusal audit: %v %s", err, output.String())
	}
}

func TestManageHelmDisabledCatalogAndDispatch(t *testing.T) {
	previous := k8s.ForceDisableHelmWrite
	k8s.ForceDisableHelmWrite = true
	t.Cleanup(func() { k8s.ForceDisableHelmWrite = previous })
	names := make(map[string]bool)
	for _, tool := range listRegisteredTools(t) {
		names[tool.Name] = true
	}
	if names["manage_helm"] || !names["get_helm_release"] || !names["apply_resource"] {
		t.Fatalf("disabled Helm writes changed the wrong tools: %v", names)
	}
	// A stale or direct dispatch must also refuse before any API or chart request.
	status := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	t.Cleanup(func() { k8s.SetConnectionStatus(status) })
	_, _, err := handleManageHelm(t.Context(), nil, manageHelmInput{Action: "install", Namespace: "team-a", Name: "cache", Chart: "redis", Repository: "https://charts.example", Version: "1.2.3"})
	if err == nil || !strings.Contains(err.Error(), "--disable-helm-write") {
		t.Fatalf("want operator-disabled refusal, got %v", err)
	}
}
