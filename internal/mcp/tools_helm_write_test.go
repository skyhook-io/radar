package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/readreason"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/storage/driver"
	authv1 "k8s.io/api/authorization/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
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
		func(x *manageHelmInput) { x.DryRun = true },
		func(x *manageHelmInput) {
			*x = manageHelmInput{Action: "uninstall", Namespace: "team-a", Name: "cache", DryRun: true}
		},
		func(x *manageHelmInput) {
			*x = manageHelmInput{Action: "uninstall", Namespace: "team-a", Name: "cache", ExpectedRevision: 2}
		},
	} {
		in := base
		mutate(&in)
		if err := in.validate(); err == nil {
			t.Fatalf("accepted invalid input: %#v", in)
		}
	}
	rollbackPreview := manageHelmInput{Action: "rollback", Namespace: "team-a", Name: "cache", Revision: 1, DryRun: true, ExpectedRevision: 2}
	if err := rollbackPreview.validate(); err != nil {
		t.Fatalf("rollback must accept dry_run and expected_revision: %v", err)
	}
}

func TestManageHelmErrorsAreRedactedButKeepTheirType(t *testing.T) {
	typed := readErrorf(readreason.ClusterConnecting, "Cluster is still connecting, please retry shortly")
	err := redactedError{err: fmt.Errorf("render failed: password=supersecretpassword123: %w", typed)}
	if strings.Contains(err.Error(), "supersecret") {
		t.Fatalf("credential reached the agent: %v", err)
	}
	var re *readError
	if !errors.As(err, &re) {
		t.Fatal("redaction must keep the chain so typed errors still render as structured results")
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
	if _, fixed := preview["history"]; fixed || !strings.Contains(preview["message"].(string), "history is purged") || preview["historyOnly"] != false || preview["resourcesCount"] != float64(0) || preview["deleteHooks"] == nil || preview["kept"] == nil {
		t.Fatalf("preview must state history, hooks and kept resources: %v", preview)
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

	// A retried confirm must not start a second uninstall while the release
	// still reads deployed (before Helm stores uninstalling, or if that write
	// failed). The fake API fails the test on any write.
	rel.Info.Status = release.StatusDeployed
	if err := storage.Update(key, rel); err != nil {
		t.Fatal(err)
	}
	output, _, err = handleManageHelm(ctx, nil, manageHelmInput{Action: "uninstall", Namespace: "team-a", Name: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	preview = map[string]any{}
	if err := json.Unmarshal([]byte(output.Content[0].(*mcp.TextContent).Text), &preview); err != nil {
		t.Fatal(err)
	}
	token := preview["confirm"].(string)
	if !consumeMutationConfirmation(token) { // the first confirm, still in flight
		t.Fatal("fresh token reported as used")
	}
	_, _, err = handleManageHelm(ctx, nil, manageHelmInput{Action: "uninstall", Namespace: "team-a", Name: "demo", Confirm: token})
	if err == nil || !strings.Contains(err.Error(), "already used") {
		t.Fatalf("a repeated confirm must be refused: %v", err)
	}
}

func TestManageHelmDeniesBeforeFetchingChart(t *testing.T) {
	hits := 0
	repo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++; http.Error(w, "must not fetch", 500) }))
	defer repo.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/apis/authorization.k8s.io/v1/selfsubjectaccessreviews" {
			var review authv1.SelfSubjectAccessReview
			if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
				t.Error(err)
			}
			a := review.Spec.ResourceAttributes
			if a == nil || a.Namespace != "team-a" {
				t.Errorf("no-auth preflight ignored target namespace: %+v", a)
			}
			allowed := a != nil && a.Namespace == "team-probe" && a.Resource == "secrets" && a.Verb == "create"
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(authv1.SelfSubjectAccessReview{TypeMeta: metav1.TypeMeta{APIVersion: "authorization.k8s.io/v1", Kind: "SelfSubjectAccessReview"}, Status: authv1.SubjectAccessReviewStatus{Allowed: allowed}})
			return
		}
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
	oldNamespaces := k8s.RBACNamespaces()
	k8s.SetRBACNamespaces([]string{"team-probe"})
	k8s.InvalidateCapabilitiesCache()
	defer func() {
		k8s.SetTestClient(old)
		k8s.SetRBACNamespaces(oldNamespaces)
		k8s.InvalidateCapabilitiesCache()
	}()
	status := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	defer k8s.SetConnectionStatus(status)
	for _, user := range []*auth.User{nil, {Username: "helm-mcp-denied"}} {
		ctx := auth.ContextWithUser(context.Background(), user)
		_, _, err = handleManageHelm(ctx, nil, manageHelmInput{Action: "install", Namespace: "team-a", Name: "cache", Chart: "redis", Repository: repo.URL, Version: "1.2.3"})
		if err == nil || !strings.Contains(err.Error(), "permission") {
			t.Fatalf("user=%v: want denied, got %v", user, err)
		}
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

// The rollback preview's live reads go through Helm's real kube client: they
// must carry the caller's identity, read the release's namespace, treat a
// denial as unknown rather than absent, and never write.
func TestManageHelmRollbackPreviewReadsLiveObjectsAsCaller(t *testing.T) {
	secrets := fake.NewClientset().CoreV1().Secrets("team-a")
	storage := driver.NewSecrets(secrets)
	cm := func(name string) string {
		return "---\n# Source: fixture/templates/" + name + ".yaml\napiVersion: v1\nkind: ConfigMap\nmetadata:\n  name: " + name + "\n"
	}
	for version, manifest := range map[int]string{1: cm("a") + cm("b"), 2: cm("a") + cm("c")} {
		status := release.StatusSuperseded
		if version == 2 {
			status = release.StatusDeployed
		}
		rel := &release.Release{Name: "demo", Namespace: "team-a", Version: version, Manifest: manifest,
			Chart: &chart.Chart{Metadata: &chart.Metadata{Name: "fixture", Version: fmt.Sprintf("%d.0.0", version)}},
			Info:  &release.Info{Status: status}}
		if err := storage.Create(fmt.Sprintf("sh.helm.release.v1.demo.v%d", version), rel); err != nil {
			t.Fatal(err)
		}
	}
	var liveReads []string
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
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/namespaces/team-a/secrets/"):
			secret, err := secrets.Get(r.Context(), strings.TrimPrefix(r.URL.Path, "/api/v1/namespaces/team-a/secrets/"), metav1.GetOptions{})
			if err != nil {
				t.Error(err)
				return
			}
			secret.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"}
			json.NewEncoder(w).Encode(secret)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/namespaces/team-a":
			json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": "team-a"}})
		case r.Method == http.MethodGet && r.URL.Path == "/api":
			json.NewEncoder(w).Encode(metav1.APIVersions{TypeMeta: metav1.TypeMeta{Kind: "APIVersions"}, Versions: []string{"v1"}})
		case r.Method == http.MethodGet && r.URL.Path == "/apis":
			json.NewEncoder(w).Encode(metav1.APIGroupList{TypeMeta: metav1.TypeMeta{Kind: "APIGroupList", APIVersion: "v1"}})
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1":
			json.NewEncoder(w).Encode(metav1.APIResourceList{TypeMeta: metav1.TypeMeta{Kind: "APIResourceList"}, GroupVersion: "v1",
				APIResources: []metav1.APIResource{{Name: "configmaps", Namespaced: true, Kind: "ConfigMap", Verbs: metav1.Verbs{"get"}}}})
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/namespaces/team-a/configmaps/"):
			name := strings.TrimPrefix(r.URL.Path, "/api/v1/namespaces/team-a/configmaps/")
			liveReads = append(liveReads, name+"@"+r.Header.Get("Impersonate-User"))
			switch name {
			case "a": // ours
				json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "a", "namespace": "team-a",
					"annotations": map[string]string{"meta.helm.sh/release-name": "demo", "meta.helm.sh/release-namespace": "team-a"}}})
			case "b": // the caller may not read it
				w.WriteHeader(http.StatusForbidden)
				json.NewEncoder(w).Encode(metav1.Status{TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"}, Status: "Failure", Reason: metav1.StatusReasonForbidden, Code: http.StatusForbidden})
			default: // c is already gone
				w.WriteHeader(http.StatusNotFound)
				json.NewEncoder(w).Encode(metav1.Status{TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"}, Status: "Failure", Reason: metav1.StatusReasonNotFound, Code: http.StatusNotFound})
			}
		default:
			t.Errorf("unexpected request (a preview must only read): %s %s", r.Method, r.URL.Path)
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
	ctx := auth.ContextWithUser(t.Context(), &auth.User{Username: "rollback-previewer"})

	var logs bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&logs)
	_, _, err = handleManageHelm(ctx, nil, manageHelmInput{Action: "rollback", Namespace: "team-a", Name: "demo", Revision: 1, DryRun: true, ExpectedRevision: 1})
	log.SetOutput(previous)
	if err == nil || !strings.Contains(err.Error(), "expected 1") || !strings.Contains(logs.String(), `outcome="refused"`) {
		t.Fatalf("stale expected_revision must be refused and audited as refused: %v\n%s", err, logs.String())
	}

	output, _, err := handleManageHelm(ctx, nil, manageHelmInput{Action: "rollback", Namespace: "team-a", Name: "demo", Revision: 1, DryRun: true, ExpectedRevision: 2})
	if err != nil {
		t.Fatal(err)
	}
	var preview map[string]any
	if err := json.Unmarshal([]byte(output.Content[0].(*mcp.TextContent).Text), &preview); err != nil {
		t.Fatal(err)
	}
	names := func(key string) string {
		var out []string
		for _, item := range preview[key].([]any) {
			out = append(out, item.(map[string]any)["name"].(string))
		}
		return strings.Join(out, ",")
	}
	if preview["state"] != "preview" || preview["currentRevision"] != float64(2) || preview["targetRevision"] != float64(1) || names("added") != "b" || names("removed") != "c" {
		t.Fatalf("stored comparison: %v", preview)
	}
	if names("liveUnknown") != "b" || names("takeovers") != "" || names("kept") != "" || preview["liveChecked"] != float64(2) {
		t.Fatalf("a denied read must be unknown, ours is no takeover, an absent object is checked: %v", preview)
	}
	if strings.Join(liveReads, ",") != "a@rollback-previewer,b@rollback-previewer,c@rollback-previewer" {
		t.Fatalf("live reads must target the release namespace as the caller: %v", liveReads)
	}
	// After a failed uninstall the agent must learn whether Helm already purged
	// the release: it purges history even when a post-delete hook then fails.
	helmClient := helm.NewStandaloneClient(cfg)
	user := userFromContext(ctx)
	if uninstallPurgedDespiteError(helmClient, manageHelmInput{Namespace: "team-a", Name: "demo"}, user) {
		t.Fatal("a release still in storage must not be reported as purged")
	}
	if !uninstallPurgedDespiteError(helmClient, manageHelmInput{Namespace: "team-a", Name: "gone"}, user) {
		t.Fatal("a release missing from storage after a failed uninstall must be reported as purged")
	}
}

func TestManageHelmRedactsStructuredErrors(t *testing.T) {
	old := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateDisconnected, Error: "dial https://api: password=supersecretpassword123"})
	defer k8s.SetConnectionStatus(old)
	_, _, err := handleManageHelm(t.Context(), nil, manageHelmInput{Action: "uninstall", Namespace: "team-a", Name: "demo"})
	result, ok := readErrorResult(err)
	if !ok {
		t.Fatalf("a typed read error must still render as a structured result: %v", err)
	}
	text := result.Content[0].(*mcp.TextContent).Text
	if strings.Contains(text, "supersecret") || strings.Contains(err.Error(), "supersecret") || !strings.Contains(text, "not connected") {
		t.Fatalf("credential reached the agent: %s", text)
	}
}

// Helm stores a new revision before it touches resources. When another writer
// stores that revision first, the call changed nothing, and the agent must hear
// that rather than a generic failure that may have partially applied.
func TestManageHelmReportsLostRevisionRaceAsNoChange(t *testing.T) {
	secrets := fake.NewClientset().CoreV1().Secrets("team-a")
	storage := driver.NewSecrets(secrets)
	for version, status := range map[int]release.Status{1: release.StatusSuperseded, 2: release.StatusDeployed} {
		rel := &release.Release{Name: "demo", Namespace: "team-a", Version: version,
			Chart:  &chart.Chart{Metadata: &chart.Metadata{Name: "fixture", Version: "1.0.0"}},
			Config: map[string]any{"auth": map[string]any{"password": fmt.Sprintf("Stored9Pass%d", version)}},
			Info:   &release.Info{Status: status}}
		if err := storage.Create(fmt.Sprintf("sh.helm.release.v1.demo.v%d", version), rel); err != nil {
			t.Fatal(err)
		}
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
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v1/namespaces/team-a/secrets/"):
			secret, err := secrets.Get(r.Context(), strings.TrimPrefix(r.URL.Path, "/api/v1/namespaces/team-a/secrets/"), metav1.GetOptions{})
			if err != nil {
				t.Error(err)
				return
			}
			secret.TypeMeta = metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"}
			json.NewEncoder(w).Encode(secret)
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/namespaces/team-a/secrets":
			// The write itself must carry the caller's identity, not Radar's.
			if got := r.Header.Get("Impersonate-User"); got != "helm-mcp-race" {
				t.Errorf("Helm wrote release storage as %q, want the caller helm-mcp-race", got)
			}
			// Another writer stored revision 3 first.
			w.WriteHeader(http.StatusConflict)
			json.NewEncoder(w).Encode(metav1.Status{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Status"}, Status: metav1.StatusFailure, Reason: metav1.StatusReasonAlreadyExists, Code: http.StatusConflict})
		default:
			t.Errorf("unexpected request (must not touch resources): %s %s", r.Method, r.URL.Path)
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
	ctx := auth.ContextWithUser(t.Context(), &auth.User{Username: "helm-mcp-race"})
	_, _, err = handleManageHelm(ctx, nil, manageHelmInput{Action: "rollback", Namespace: "team-a", Name: "demo", Revision: 1, ExpectedRevision: 2})
	if err == nil || !strings.Contains(err.Error(), "refused before applying changes: another operation stored the next revision") {
		t.Fatalf("lost revision race must report no change: %v", err)
	}

	// A template's fail or an API error can echo a stored or supplied secret
	// verbatim; pattern masking alone wouldn't catch these plain values.
	in := manageHelmInput{Namespace: "team-a", Name: "demo", Values: map[string]any{"db": map[string]any{"password": "Supplied7Pass"}}}
	failure := error(&knownValuesError{err: errors.New("template: fail: Stored9Pass1 / Stored9Pass2 / Supplied7Pass"), secrets: storedSensitiveValues(helm.NewStandaloneClient(cfg), in, userFromContext(ctx))})
	masked := errorMask(in, failure)(failure.Error())
	for _, secret := range []string{"Stored9Pass1", "Stored9Pass2", "Supplied7Pass"} {
		if strings.Contains(masked, secret) {
			t.Fatalf("error text keeps known secret %s: %s", secret, masked)
		}
	}
}

// Known values are masked in one pass, longest first, so a short value can't
// match inside an earlier [REDACTED] and values too short to be credentials
// don't mangle the message.
func TestMaskKnownValuesIsSinglePass(t *testing.T) {
	got := maskKnownValues("login admin with adminSecret failed; D", map[string]bool{"admin": true, "adminSecret": true, "D": true, "ED": true})
	if got != "login [REDACTED] with [REDACTED] failed; D" {
		t.Fatalf("masking: %q", got)
	}
}

// Kubernetes validation errors quote values (%q), so a credential with a
// quote, backslash or newline appears escaped and must still be masked.
func TestMaskKnownValuesCoversEscapedForms(t *testing.T) {
	err := field.Invalid(field.NewPath("spec", "auth", "password"), "Stored\"Pass9\\x\nend", "must match pattern")
	got := maskKnownValues(err.Error(), map[string]bool{"Stored\"Pass9\\x\nend": true})
	if strings.Contains(got, "Pass9") {
		t.Fatalf("escaped credential survives masking: %s", got)
	}
}

func TestRefuseFluxManagedRelease(t *testing.T) {
	for _, tc := range []struct {
		name    string
		detail  helm.HelmReleaseDetail
		blocked bool
	}{
		{"hidden controller", helm.HelmReleaseDetail{ManagedByFlux: true}, true},
		{"readable controller", helm.HelmReleaseDetail{ManagedByFlux: true, ManagedByFluxHelmRelease: "shared/private-manager"}, true},
		{"existing ownership reference", helm.HelmReleaseDetail{ManagedByFluxHelmRelease: "shared/private-manager"}, true},
		{"ordinary Helm release", helm.HelmReleaseDetail{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := refuseFluxManagedRelease(&tc.detail)
			if (err != nil) != tc.blocked {
				t.Fatalf("blocked=%v, want %v", err != nil, tc.blocked)
			}
			if err != nil {
				shown := strings.Contains(err.Error(), "shared/private-manager")
				if shown != (tc.detail.ManagedByFluxHelmRelease != "") {
					t.Fatalf("controller reference shown=%v, expected only a caller-readable reference: %v", shown, err)
				}
			}
		})
	}
}
