package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/k8score"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
)

func resourceIssuesRequest(t *testing.T, s *Server, path string) *httptest.ResponseRecorder {
	t.Helper()
	rt := chi.NewRouter()
	rt.Get("/api/issues/resource/{kind}/{namespace}/{name}", s.handleResourceIssues)
	rt.Get("/api/issues", s.handleIssues)
	w := httptest.NewRecorder()
	rt.ServeHTTP(w, requestWithUser(http.MethodGet, path, &auth.User{Username: "pg-user"}))
	return w
}

func TestIssuesAPICompletedPodsRemainQuiet(t *testing.T) {
	completed := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "completed", Namespace: "batch"},
		Status: corev1.PodStatus{Phase: corev1.PodSucceeded, Conditions: []corev1.PodCondition{{
			Type: corev1.PodReady, Status: corev1.ConditionFalse, Reason: "PodCompleted",
		}}},
	}
	failed := completed.DeepCopy()
	failed.Name = "failed"
	failed.Status.Phase = corev1.PodFailed
	failed.Status.Conditions[0].Reason = "PodFailed"
	useTestResourceCache(t, fake.NewClientset(completed, failed))
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	dyn := dynamicfake.NewSimpleDynamicClient(scheme, completed, failed)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Version: "v1", Kind: "Pod", Name: "pods", Namespaced: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	gvr := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	if _, err := k8s.GetDynamicResourceCache().ListBlocking(gvr, "batch", 2*time.Second); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"none", "proxy"} {
		t.Run(mode, func(t *testing.T) {
			s := newAuthServer(auth.Config{Mode: mode})
			var user *auth.User
			if mode == "proxy" {
				user = &auth.User{Username: "batch-user"}
				s.permCache.Set(user.Username, nil, &auth.UserPermissions{AllowedNamespaces: []string{"batch"}})
			}
			for range 2 {
				w := httptest.NewRecorder()
				s.handleIssues(w, requestWithUser(http.MethodGet, "/api/issues?namespaces=batch&kind=Pod", user))
				if w.Code != http.StatusOK {
					t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
				}
				var response issuesapi.Response
				if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				failedReported := false
				for _, issue := range response.Issues {
					if issue.Name != failed.Name {
						t.Errorf("completed Pod must not appear in Issues: %s", w.Body.String())
					}
					failedReported = failedReported || (issue.Name == failed.Name && issue.Severity == issues.SeverityCritical)
				}
				if !failedReported {
					t.Fatalf("failed Pod must remain critical: %s", w.Body.String())
				}
			}
		})
	}
}

// Namespaced issues follow the drawer: namespace access opens a Deployment,
// so it opens the Deployment's issues too, with no per-kind check.
// Fixture: TestMain seeds Deployment broken/stuck-app with no available replicas.
func TestResourceIssuesFollowTheDrawersNamespaceAccess(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	s.permCache.Set("pg-user", nil, &auth.UserPermissions{AllowedNamespaces: []string{"broken"}})

	w := resourceIssuesRequest(t, s, "/api/issues/resource/Deployment/broken/stuck-app?group=apps&coverage=1")
	if w.Code != http.StatusOK {
		t.Fatalf("namespace access: status %d (body %s)", w.Code, w.Body.String())
	}
	var resp ResourceIssuesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Issues) == 0 {
		t.Fatal("a caller with namespace access got no issues for the broken Deployment")
	}
	if resp.Coverage != resourceIssuesCoverageOK {
		t.Fatalf("coverage %q, want ok for a synced typed kind", resp.Coverage)
	}
	if w := resourceIssuesRequest(t, s, "/api/issues/resource/deployments/broken/stuck-app"); w.Code != http.StatusOK {
		t.Fatalf("plural kind: status %d (body %s)", w.Code, w.Body.String())
	}

	s.permCache.Set("pg-user", nil, &auth.UserPermissions{AllowedNamespaces: []string{"other"}})
	if w := resourceIssuesRequest(t, s, "/api/issues/resource/Deployment/broken/stuck-app?group=apps"); w.Code != http.StatusForbidden {
		t.Fatalf("no namespace access: status %d, want 403", w.Code)
	}
}

func TestIssueSubjectAccessFollowsTheDrawer(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"team-a"}}
	perms.SetCanI("get", "", "secrets", "team-a", false)
	s.permCache.Set("pg-user", nil, perms)
	access := s.issueSubjectAccess(requestWithUser(http.MethodGet, "/", &auth.User{Username: "pg-user"}))

	if !access(issues.Ref{Group: "apps", Kind: "Deployment", Namespace: "team-a", Name: "web"}) {
		t.Fatal("namespace access must open a Deployment's issues")
	}
	if access(issues.Ref{Group: "apps", Kind: "Deployment", Namespace: "team-b", Name: "web"}) {
		t.Fatal("another namespace's row was authorized")
	}
	if access(issues.Ref{Kind: "Secret", Namespace: "team-a", Name: "creds"}) {
		t.Fatal("a Secret's issue was authorized without Secret access")
	}
	perms.SetCanI("get", "", "secrets", "team-a", true)
	if !access(issues.Ref{Kind: "Secret", Namespace: "team-a", Name: "creds"}) {
		t.Fatal("Secret access did not open the Secret's issue")
	}

	open := newAuthServer(auth.Config{Mode: "none"}).issueSubjectAccess(httptest.NewRequest(http.MethodGet, "/", nil))
	if !open(issues.Ref{Kind: "Secret", Namespace: "team-b", Name: "creds"}) {
		t.Fatal("auth off must not gate rows")
	}
}

func TestIssueEvidenceAccessFollowsReadGranularity(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"team-a"}}
	perms.SetCanI("list", "", "secrets", "team-a", false)
	perms.SetCanI("list", "", "nodes", "", false)
	s.permCache.Set("pg-user", nil, perms)
	access := s.issueEvidenceAccess(requestWithUser(http.MethodGet, "/", &auth.User{Username: "pg-user"}))

	if !access(issues.EvidenceRead{Resource: "pods", Namespace: "team-a", Verb: "list"}) {
		t.Fatal("namespace access must authorize a namespaced inventory")
	}
	if access(issues.EvidenceRead{Resource: "pods", Namespace: "team-b", Verb: "list"}) {
		t.Fatal("another namespace's inventory was authorized")
	}
	if access(issues.EvidenceRead{Resource: "secrets", Namespace: "team-a", Verb: "list"}) {
		t.Fatal("Secrets were authorized without Secret access")
	}
	if access(issues.EvidenceRead{Resource: "nodes", Verb: "list"}) {
		t.Fatal("a cluster-wide inventory was authorized without the SAR")
	}
	perms.SetCanI("list", "", "secrets", "team-a", true)
	perms.SetCanI("list", "", "nodes", "", true)
	if !access(issues.EvidenceRead{Resource: "secrets", Namespace: "team-a", Verb: "list"}) || !access(issues.EvidenceRead{Resource: "nodes", Verb: "list"}) {
		t.Fatal("granted inventories were withheld")
	}
}

// A Secret stuck terminating is an issue about the Secret, so it reaches only
// a caller who can read Secrets there, on the list as on the drawer.
func TestIssuesListWithholdsSecretsTheCallerCantRead(t *testing.T) {
	restoreFixtureCache(t)
	k8s.ResetResourceCache()
	deleted := metav1.NewTime(time.Now().Add(-2 * time.Hour))
	client := fake.NewClientset(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{
		Name: "creds", Namespace: "vault", DeletionTimestamp: &deleted, Finalizers: []string{"example.com/hold"},
	}})
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	s := newAuthServer(auth.Config{Mode: "proxy"})
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"vault"}}
	perms.SetCanI("get", "", "secrets", "vault", false)
	s.permCache.Set("pg-user", nil, perms)

	secretIssue := func() bool {
		w := resourceIssuesRequest(t, s, "/api/issues?namespaces=vault")
		if w.Code != http.StatusOK {
			t.Fatalf("status %d (body %s)", w.Code, w.Body.String())
		}
		var resp struct {
			Issues []issues.Issue `json:"issues"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		for _, i := range resp.Issues {
			if i.Kind == "Secret" && i.Name == "creds" {
				return true
			}
		}
		return false
	}
	if secretIssue() {
		t.Fatal("a caller without Secret access saw the Secret's issue")
	}
	perms.SetCanI("get", "", "secrets", "vault", true)
	if !secretIssue() {
		t.Fatal("a caller with Secret access lost the Secret's issue")
	}
}

func TestResourceIssuesCoverageOfAnUnwatchedKind(t *testing.T) {
	if got := resourceIssuesCoverage("Widget", "example.com", "pg"); got != resourceIssuesCoverageNotWatched {
		t.Fatalf("coverage %q, want notWatched before discovery is ready", got)
	}
}

// A link made on one cluster can open on another that has no such kind; that
// is not a kind Radar could start watching.
func TestResourceIssuesCoverageOfAKindTheClusterDoesNotServe(t *testing.T) {
	appGVR := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{appGVR: "ApplicationList"})
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{
		{Group: "argoproj.io", Version: "v1alpha1", Kind: "Application", Name: "applications", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	if got := resourceIssuesCoverage("Widget", "example.com", "pg"); got != resourceIssuesCoverageUnknownKind {
		t.Fatalf("coverage %q, want unknownKind for a kind discovery doesn't serve", got)
	}
	if got := resourceIssuesCoverage("Application", "argoproj.io", "pg"); got != resourceIssuesCoverageNotWatched {
		t.Fatalf("coverage %q, want notWatched for a served kind with no watch", got)
	}
}

func restoreFixtureCache(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		k8s.ResetResourceCache()
		if err := k8s.InitTestResourceCache(testFakeClient); err != nil {
			t.Fatalf("restore package fixture cache: %v", err)
		}
	})
}

func TestResourceIssuesCoverageOfAKindWatchedElsewhere(t *testing.T) {
	restoreFixtureCache(t)
	k8s.ResetResourceCache()
	if err := k8s.InitScopedTestResourceCache(testFakeClient, map[string]k8score.ResourceScope{
		"deployments": {Enabled: true, Namespace: "default"},
	}); err != nil {
		t.Fatalf("InitScopedTestResourceCache: %v", err)
	}
	if got := resourceIssuesCoverage("Deployment", "apps", "broken"); got != resourceIssuesCoverageNotWatched {
		t.Fatalf("coverage in an unwatched namespace %q, want notWatched", got)
	}
	if got := resourceIssuesCoverage("Deployment", "apps", "default"); got != resourceIssuesCoverageOK {
		t.Fatalf("coverage in the watched namespace %q, want ok", got)
	}
}

func TestResourceIssuesCountNodeIssuesTheListGateDropped(t *testing.T) {
	restoreFixtureCache(t)
	k8s.ResetResourceCache()
	since := metav1.NewTime(time.Now().Add(-time.Hour))
	client := fake.NewClientset(&corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "n1", CreationTimestamp: since},
		Status: corev1.NodeStatus{Conditions: []corev1.NodeCondition{{
			Type: corev1.NodeReady, Status: corev1.ConditionFalse, LastTransitionTime: since,
		}}},
	})
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}

	s := newAuthServer(auth.Config{Mode: "proxy"})
	perms := &auth.UserPermissions{AllowedNamespaces: nil}
	perms.SetCanI("get", "", "nodes", "", true)
	perms.SetCanI("list", "", "nodes", "", false)
	s.permCache.Set("pg-user", nil, perms)

	w := resourceIssuesRequest(t, s, "/api/issues/resource/Node/_/n1?coverage=1")
	if w.Code != http.StatusOK {
		t.Fatalf("status %d (body %s)", w.Code, w.Body.String())
	}
	var resp ResourceIssuesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Issues) != 0 {
		t.Fatalf("a caller who cannot list nodes got node issues: %+v", resp.Issues)
	}
	if resp.Withheld == nil || resp.Withheld.Issues == 0 {
		t.Fatalf("withheld %+v: the NotReady node's issue vanished instead of being counted", resp.Withheld)
	}

	perms.SetCanI("list", "", "nodes", "", true)
	w = resourceIssuesRequest(t, s, "/api/issues/resource/Node/_/n1?coverage=1")
	resp = ResourceIssuesResponse{}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Issues) == 0 || resp.Withheld != nil {
		t.Fatalf("a caller who can list nodes: issues %d withheld %+v", len(resp.Issues), resp.Withheld)
	}
}

func TestResourceIssuesCountWithheldWithAndWithoutTheGroupParam(t *testing.T) {
	restoreFixtureCache(t)
	k8s.ResetResourceCache()
	client := fake.NewClientset(&rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "dangling"},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "gone"},
		Subjects:   []rbacv1.Subject{{Kind: "User", Name: "someone"}},
	})
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}

	s := newAuthServer(auth.Config{Mode: "proxy"})
	perms := &auth.UserPermissions{AllowedNamespaces: nil}
	perms.SetCanI("get", "rbac.authorization.k8s.io", "clusterrolebindings", "", true)
	perms.SetCanI("list", "rbac.authorization.k8s.io", "clusterrolebindings", "", false)
	s.permCache.Set("pg-user", nil, perms)

	for _, path := range []string{
		"/api/issues/resource/ClusterRoleBinding/_/dangling?coverage=1",
		"/api/issues/resource/ClusterRoleBinding/_/dangling?coverage=1&group=rbac.authorization.k8s.io",
	} {
		w := resourceIssuesRequest(t, s, path)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d (body %s)", path, w.Code, w.Body.String())
		}
		var resp ResourceIssuesResponse
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("%s: decode: %v", path, err)
		}
		if len(resp.Issues) != 0 || resp.Withheld == nil || resp.Withheld.Issues == 0 {
			t.Fatalf("%s: issues %d withheld %+v, want the dangling binding's issue counted as withheld", path, len(resp.Issues), resp.Withheld)
		}
	}

	w := resourceIssuesRequest(t, s, "/api/issues/resource/ClusterRoleBinding/_/dangling")
	var bare []issues.Issue
	if err := json.Unmarshal(w.Body.Bytes(), &bare); err != nil || len(bare) != 0 {
		t.Fatalf("bare path: %v, %d issues", err, len(bare))
	}
}

func TestResourceIssuesUnresolvedKindUsesPreflight(t *testing.T) {
	s := newAuthServer(auth.Config{Mode: "proxy"})
	s.permCache.Set("pg-user", nil, &auth.UserPermissions{AllowedNamespaces: []string{"broken"}})
	w := resourceIssuesRequest(t, s, "/api/issues/resource/NewWidget/broken/new?group=widgets.example.io&coverage=1")
	if w.Code != http.StatusOK {
		t.Fatalf("unresolved kind: status=%d body=%s", w.Code, w.Body.String())
	}
	var resp ResourceIssuesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Coverage != resourceIssuesCoverageNotWatched {
		t.Fatalf("coverage %q, want notWatched for an undiscovered kind", resp.Coverage)
	}
	if w := resourceIssuesRequest(t, s, "/api/issues/resource/NewWidget/denied/new?group=widgets.example.io"); w.Code != http.StatusForbidden {
		t.Fatalf("preflight deny: status=%d body=%s", w.Code, w.Body.String())
	}
}

// A finding drawn from a Secret's history is told to a caller who can't read
// the Secret without that history: the Pod may hold a stale value and needs a
// restart, but not which Secret, which key, or when it changed. A missing TLS
// Secret names only what the Ingress names, so it needs only namespace access.
func TestSecretDerivedFindingsDegradeWithoutSecretAccess(t *testing.T) {
	restoreFixtureCache(t)
	k8s.ResetResourceCache()
	now := time.Now().UTC().Truncate(time.Second)
	started := metav1.NewTime(now.Add(-2 * time.Minute))
	secretEnv := []corev1.EnvVar{{
		Name: "DB_PASSWORD",
		ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{
			LocalObjectReference: corev1.LocalObjectReference{Name: "db-conn"}, Key: "password",
		}},
	}}
	controller := true
	client := fake.NewClientset(
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "shop", UID: "dep", CreationTimestamp: metav1.NewTime(now.Add(-time.Hour))},
			Spec:       appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "worker"}}},
		},
		&appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
			Name: "worker-abc", Namespace: "shop", UID: "rs",
			OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "worker", UID: "dep", Controller: &controller}},
		}},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{
				Name: "worker-abc-1", Namespace: "shop", CreationTimestamp: started,
				OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "worker-abc", UID: "rs", Controller: &controller}},
			},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Env: secretEnv}}},
			Status: corev1.PodStatus{
				Phase:      corev1.PodRunning,
				Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}},
				ContainerStatuses: []corev1.ContainerStatus{{
					Name: "app", Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: started}},
				}},
			},
		},
		&corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Name: "api-0", Namespace: "shop", CreationTimestamp: started},
			Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Env: secretEnv}}},
			Status: corev1.PodStatus{
				Phase:      corev1.PodRunning,
				Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionFalse}},
				ContainerStatuses: []corev1.ContainerStatus{{
					Name: "app", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{StartedAt: started}},
				}},
			},
		},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "db-conn", Namespace: "shop"}},
		&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop"}, Spec: corev1.ServiceSpec{Ports: []corev1.ServicePort{{Port: 80}}}},
		&networkingv1.Ingress{
			ObjectMeta: metav1.ObjectMeta{Name: "front", Namespace: "shop", CreationTimestamp: metav1.NewTime(now.Add(-time.Hour))},
			Spec: networkingv1.IngressSpec{
				TLS: []networkingv1.IngressTLS{{Hosts: []string{"shop.example.com"}, SecretName: "shop-tls"}},
				DefaultBackend: &networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{
					Name: "web", Port: networkingv1.ServiceBackendPort{Number: 80},
				}},
			},
		},
	)
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	timeline.ResetStore()
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 10}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	t.Cleanup(func() {
		timeline.ResetStore()
		if err := timeline.InitStore(timeline.DefaultStoreConfig()); err != nil {
			t.Fatalf("re-init global store: %v", err)
		}
	})
	if err := timeline.RecordEvent(context.Background(), timeline.TimelineEvent{
		ID: "db-conn-change", Timestamp: now.Add(-time.Minute), Source: timeline.SourceInformer,
		ClusterContext: k8s.ActiveClusterContext(), Kind: "Secret", Namespace: "shop", Name: "db-conn",
		EventType: timeline.EventTypeUpdate,
		Diff:      &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "data (modified keys)", NewValue: []string{"password"}}}},
	}); err != nil {
		t.Fatalf("RecordEvent: %v", err)
	}

	s := newAuthServer(auth.Config{Mode: "proxy"})
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"shop"}}
	perms.SetCanI("get", "", "secrets", "shop", false)
	s.permCache.Set("pg-user", nil, perms)
	env := newAuthTestServer(t)
	env.srv.permCache.Set("pg-user", nil, perms)

	findings := func() (stale *issues.Issue, tls bool) {
		w := resourceIssuesRequest(t, s, "/api/issues?namespaces=shop")
		var resp struct {
			Issues []issues.Issue `json:"issues"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode %d: %v (%s)", w.Code, err, w.Body.String())
		}
		for i := range resp.Issues {
			switch resp.Issues[i].Reason {
			case "StaleSecretEnv":
				if resp.Issues[i].Kind == "Pod" && resp.Issues[i].Name == "api-0" {
					stale = &resp.Issues[i]
				}
			case "Missing TLS Secret":
				tls = true
			}
		}
		return stale, tls
	}
	dashboard := func() (stale *DashboardProblem, tls bool) {
		resp := env.authGet(t, "/api/dashboard?namespaces=shop", "pg-user", "")
		defer resp.Body.Close()
		var body DashboardResponse
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode dashboard %d: %v", resp.StatusCode, err)
		}
		for i := range body.Problems {
			switch body.Problems[i].Reason {
			case "StaleSecretEnv":
				if body.Problems[i].Kind == "Deployment" && body.Problems[i].Name == "worker" {
					stale = &body.Problems[i]
				}
			case "Missing TLS Secret":
				tls = true
			}
		}
		return stale, tls
	}
	revealsTheSecret := func(text string) bool {
		return strings.Contains(text, "db-conn") || strings.Contains(text, "password")
	}

	stale, tls := findings()
	if stale == nil {
		t.Fatal("a caller without Secret access lost the stale-env finding instead of a degraded one")
	}
	if revealsTheSecret(stale.Message+stale.RawMessage+stale.Cause+stale.Action) || !stale.FirstSeen.IsZero() || !stale.OnsetUnknown ||
		!strings.Contains(stale.Cause, "shown only to callers who can read the Secret") || !strings.Contains(stale.Action, "Restart the pod") {
		t.Fatalf("degraded issue = %+v", stale)
	}
	problem, dashTLS := dashboard()
	if problem == nil || revealsTheSecret(problem.Message) || !strings.Contains(problem.Message, "may hold stale Secret-backed environment values") ||
		problem.Duration != "" || !problem.OnsetUnknown {
		t.Fatalf("degraded dashboard problem = %+v", problem)
	}
	if !tls || !dashTLS {
		t.Fatal("a caller with namespace access lost the missing-TLS finding")
	}

	perms.SetCanI("get", "", "secrets", "shop", true)
	stale, _ = findings()
	if stale == nil || !strings.Contains(stale.Message, "Secret/db-conn key password") || stale.FirstSeen.IsZero() {
		t.Fatalf("a caller with Secret access lost the full finding: %+v", stale)
	}
	if problem, _ := dashboard(); problem == nil || !strings.Contains(problem.Message, "before Radar observed") || problem.Duration == "" {
		t.Fatalf("a caller with Secret access lost the full dashboard problem: %+v", problem)
	}
}

// A cluster-scoped problem needs list on its kind, on the dashboard as on
// Issues, even for a caller who can read Pods everywhere.
func TestDashboardClusterScopedProblemsNeedListOnTheKind(t *testing.T) {
	restoreFixtureCache(t)
	k8s.ResetResourceCache()
	client := fake.NewClientset(&rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: "dangling"},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "gone"},
		Subjects:   []rbacv1.Subject{{Kind: "User", Name: "someone"}},
	})
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	env := newAuthTestServer(t)
	perms := &auth.UserPermissions{AllowedNamespaces: nil}
	perms.SetCanI("list", "rbac.authorization.k8s.io", "clusterrolebindings", "", false)
	env.srv.permCache.Set("pod-reader", nil, perms)

	dangling := func() bool {
		resp := env.authGet(t, "/api/dashboard", "pod-reader", "")
		defer resp.Body.Close()
		var body DashboardResponse
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatalf("decode dashboard %d: %v", resp.StatusCode, err)
		}
		for _, p := range body.Problems {
			if p.Kind == "ClusterRoleBinding" && p.Name == "dangling" {
				return true
			}
		}
		return false
	}
	if dangling() {
		t.Fatal("a caller who can't list ClusterRoleBindings saw one's problem")
	}
	perms.SetCanI("list", "rbac.authorization.k8s.io", "clusterrolebindings", "", true)
	if !dangling() {
		t.Fatal("a caller who can list ClusterRoleBindings lost the problem")
	}
}

func TestResourceIssuesCountsWithheldTerminatingClusterCR(t *testing.T) {
	restoreFixtureCache(t)
	k8s.ResetResourceCache()
	if err := k8s.InitTestResourceCache(fake.NewClientset()); err != nil {
		t.Fatal(err)
	}
	k8s.ResetTestDynamicState()
	t.Cleanup(k8s.ResetTestDynamicState)
	gvr := schema.GroupVersionResource{Group: "widgets.example.com", Version: "v1", Resource: "clusterwidgets"}
	widget := &unstructured.Unstructured{}
	widget.SetAPIVersion(gvr.Group + "/" + gvr.Version)
	widget.SetKind("ClusterWidget")
	widget.SetName("stuck")
	widget.SetUID("cluster-widget-uid")
	widget.SetCreationTimestamp(metav1.NewTime(time.Now().Add(-24 * time.Hour)))
	widget.SetDeletionTimestamp(&metav1.Time{Time: time.Now().Add(-2 * time.Hour)})
	widget.SetFinalizers([]string{"widgets.example.com/cleanup"})
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "ClusterWidgetList"}, widget)
	if err := k8s.InitTestDynamicResourceCache(client, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "ClusterWidget", Name: gvr.Resource, IsCRD: true, Verbs: []string{"list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	dc := k8s.GetDynamicResourceCache()
	if err := dc.EnsureWatching(gvr); err != nil {
		t.Fatal(err)
	}
	if !dc.WaitForSync(gvr, 2*time.Second) {
		t.Fatal("ClusterWidget cache did not sync")
	}
	s := newAuthServer(auth.Config{Mode: "proxy"})
	perms := &auth.UserPermissions{AllowedNamespaces: []string{"team"}}
	perms.SetCanI("get", gvr.Group, gvr.Resource, "", true)
	perms.SetCanI("list", gvr.Group, gvr.Resource, "", false)
	s.permCache.Set("pg-user", nil, perms)
	w := resourceIssuesRequest(t, s, "/api/issues/resource/ClusterWidget/_/stuck?coverage=1&group="+gvr.Group)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var response ResourceIssuesResponse
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Issues) != 0 || response.Coverage != resourceIssuesCoverageOK || response.Withheld == nil || response.Withheld.Issues != 1 {
		t.Fatalf("a get-only caller must see one withheld issue, not an all-clear: %s", w.Body.String())
	}
	perms.SetCanI("list", gvr.Group, gvr.Resource, "", true)
	w = resourceIssuesRequest(t, s, "/api/issues/resource/ClusterWidget/_/stuck?coverage=1&group="+gvr.Group)
	response = ResourceIssuesResponse{}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Issues) != 1 || response.Issues[0].Category != issuesapi.CategoryTerminationStuck || response.Withheld != nil {
		t.Fatalf("the list grant must expose the termination issue: %s", w.Body.String())
	}
}
