package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/gitops"
	gitopsinsights "github.com/skyhook-io/radar/pkg/gitops/insights"
	gitopstree "github.com/skyhook-io/radar/pkg/gitops/tree"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// TestWriteGitOpsErrorStatusMapping pins the typed-sentinel → HTTP
// status mapping that every GitOps mutating handler depends on. The
// operations layer returns sentinel errors (ErrResourceTerminating,
// ErrOperationInProgress, etc); writeGitOpsError maps each to a
// specific HTTP status code that the frontend special-cases for soft
// vs hard error toasts. A bad case-clause refactor that re-orders or
// drops a branch would silently downgrade 409 → 500, regressing the
// frontend's terminating-toast UX without any operation-layer test
// catching it.
func TestWriteGitOpsErrorStatusMapping(t *testing.T) {
	gvr := schema.GroupResource{Group: "argoproj.io", Resource: "applications"}
	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{
			name:       "K8s NotFound (resource missing)",
			err:        apierrors.NewNotFound(gvr, "demo"),
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "K8s Forbidden (RBAC denied)",
			err:        apierrors.NewForbidden(gvr, "demo", errors.New("rbac denied")),
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "ErrHistoryEntryNotFound → 404",
			err:        gitops.ErrHistoryEntryNotFound,
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "ErrOperationInProgress → 409",
			err:        gitops.ErrOperationInProgress,
			wantStatus: http.StatusConflict,
		},
		{
			name:       "ErrResourceTerminating → 409",
			err:        gitops.ErrResourceTerminating,
			wantStatus: http.StatusConflict,
		},
		{
			name:       "wrapped ErrResourceTerminating still maps to 409",
			err:        errors.Join(gitops.ErrResourceTerminating, errors.New("extra context")),
			wantStatus: http.StatusConflict,
		},
		{
			name:       "ErrNoOperationInProgress → 400",
			err:        gitops.ErrNoOperationInProgress,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "ErrInvalidResourceSelection → 400",
			err:        gitops.ErrInvalidResourceSelection,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "wrapped ErrInvalidRevision → 400",
			err:        errors.Join(gitops.ErrInvalidRevision, errors.New("multi-source Application")),
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "wrapped ErrAutoSyncEnabled → 409",
			err:        errors.Join(gitops.ErrAutoSyncEnabled, errors.New("extra context")),
			wantStatus: http.StatusConflict,
		},
		{
			name:       "ErrHistoryEntryNotRollbackable → 400",
			err:        gitops.ErrHistoryEntryNotRollbackable,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "unrecognized error → 500",
			err:        errors.New("something else"),
			wantStatus: http.StatusInternalServerError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			s := &Server{}
			s.writeGitOpsError(rr, tt.err, "argo", "sync", "argocd", "demo")
			if rr.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (err=%v)", rr.Code, tt.wantStatus, tt.err)
			}
			body := rr.Body.String()
			if !strings.Contains(body, "error") {
				t.Fatalf("expected JSON body with 'error' field; got %q", body)
			}
		})
	}
}

// TestEventMatchesGroup pins the cross-group event filter used by the
// insights resolver's RecentEvents path. Without correct group matching,
// events from a CNPG Cluster could surface on a CAPI Cluster insights
// view (kind+name collide across groups), or vice versa.
func TestEventMatchesGroup(t *testing.T) {
	cases := []struct {
		name       string
		group      string
		apiVersion string
		want       bool
	}{
		{name: "exact group match (grouped kind)", group: "argoproj.io", apiVersion: "argoproj.io/v1alpha1", want: true},
		{name: "different group rejected", group: "argoproj.io", apiVersion: "fluxcd.io/v1", want: false},
		{name: "core (no slash) accepted when caller asks for any group", group: "", apiVersion: "v1", want: true},
		{name: "core apiVersion, requested group → rejected (event is on a core kind)", group: "argoproj.io", apiVersion: "v1", want: false},
		{name: "empty apiVersion accepted (informer stripped it)", group: "argoproj.io", apiVersion: "", want: true},
		{name: "empty group accepted (caller doesn't care)", group: "", apiVersion: "argoproj.io/v1alpha1", want: true},
		{name: "subgroup not equal to parent group rejected", group: "fluxcd.io", apiVersion: "kustomize.toolkit.fluxcd.io/v1", want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := eventMatchesGroup(tc.group, tc.apiVersion); got != tc.want {
				t.Fatalf("eventMatchesGroup(%q, %q) = %v, want %v", tc.group, tc.apiVersion, got, tc.want)
			}
		})
	}
}

// HasNamespaceAccess is the gitopsRequest method that handlers call before
// running the (potentially expensive) topology+tree build. It mirrors the
// readscope.NoNamespaceAccess contract (tested in server_auth_test.go) but as a
// method on the request struct, so this guards against the predicate
// drifting out of sync with the underlying helper.
func TestGitopsRequestHasNamespaceAccess(t *testing.T) {
	cases := []struct {
		name string
		ns   []string
		want bool
	}{
		{name: "nil → allowed (no filter)", ns: nil, want: true},
		{name: "empty slice → denied", ns: []string{}, want: false},
		{name: "specific ns → allowed", ns: []string{"argocd"}, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := &gitopsRequest{AllowedNamespaces: tc.ns}
			if got := req.HasNamespaceAccess(); got != tc.want {
				t.Fatalf("HasNamespaceAccess() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestFilterGitOpsTreeForUserAppliesNamespaceAndClusterScope(t *testing.T) {
	s := &Server{permCache: auth.NewPermissionCache()}
	s.permCache.Set("gitops-tree-user", nil, &auth.UserPermissions{AllowedNamespaces: []string{"team-a"}})
	s.permCache.Get("gitops-tree-user", nil).SetCanI("get", "apps", "deployments", "team-a", true)
	req := &gitopsRequest{AllowedNamespaces: []string{"team-a"}}
	tree := &gitopstree.ResourceTree{
		Root: gitopstree.Node{ID: "root", Role: gitopstree.RoleRoot, Ref: gitopstree.ResourceRef{Kind: "Application", Namespace: "argocd", Name: "app"}},
		Nodes: []gitopstree.Node{
			{ID: "root", Role: gitopstree.RoleRoot, Ref: gitopstree.ResourceRef{Kind: "Application", Namespace: "argocd", Name: "app"}},
			{ID: "allowed", Role: gitopstree.RoleDeclared, Ref: gitopstree.ResourceRef{Kind: "Deployment", Namespace: "team-a", Name: "api"}},
			{ID: "other-ns", Role: gitopstree.RoleDeclared, Ref: gitopstree.ResourceRef{Kind: "Deployment", Namespace: "team-b", Name: "api"}},
			{ID: "node", Role: gitopstree.RoleGenerated, Ref: gitopstree.ResourceRef{Kind: "Node", Name: "worker-1"}},
		},
		Edges: []gitopstree.Edge{
			{Source: "root", Target: "allowed", Type: gitopstree.EdgeOwns},
			{Source: "root", Target: "other-ns", Type: gitopstree.EdgeOwns},
			{Source: "allowed", Target: "node", Type: gitopstree.EdgeOwns},
		},
	}
	r := httptest.NewRequest(http.MethodGet, "/api/gitops/tree/applications/argocd/app", nil)
	r = r.WithContext(auth.ContextWithUser(r.Context(), &auth.User{Username: "gitops-tree-user"}))

	got := s.filterGitOpsTreeForUser(r, req, tree)
	if len(got.Nodes) != 2 {
		t.Fatalf("expected root + one allowed namespaced node, got %#v", got.Nodes)
	}
	for _, node := range got.Nodes {
		if node.ID == "other-ns" || node.ID == "node" {
			t.Fatalf("node %q should have been hidden by RBAC filter: %#v", node.ID, got.Nodes)
		}
	}
	if len(got.Edges) != 1 || got.Edges[0].Target != "allowed" {
		t.Fatalf("expected only edge to allowed node, got %#v", got.Edges)
	}
	if got.Summary.Declared != 1 || got.Summary.Generated != 0 {
		t.Fatalf("summary not recomputed after filtering: %#v", got.Summary)
	}
	if len(got.Warnings) == 0 || !strings.Contains(got.Warnings[0], "hidden by RBAC") {
		t.Fatalf("expected RBAC warning, got %#v", got.Warnings)
	}
}

func TestGitOpsFinalizerEmptyOwnerNamespace(t *testing.T) {
	cache := k8s.GetResourceCache()
	if cache == nil || cache.Pods() == nil {
		t.Fatal("test cache unavailable")
	}
	resolver := &insightsResolver{cache: cache, canAccess: func(string, string, string, string) bool {
		t.Fatal("empty namespace catalog owner reached inventory access")
		return true
	}}
	root := &unstructured.Unstructured{}
	root.SetAPIVersion("operator.victoriametrics.com/v1")
	if got := resolver.FinalizerOwnerStatus("apps.victoriametrics.com/finalizer", root); got != "" {
		t.Fatalf("got %q", got)
	}
}

func TestGitOpsFinalizerHealthDuringControllerRollout(t *testing.T) {
	pod := &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Ready: true}}}}
	pod.SetName("rollout-source-controller")
	pod.SetNamespace("flux-system")
	pod.SetLabels(map[string]string{"app": "source-controller"})
	pod.SetDeletionTimestamp(&metav1.Time{Time: time.Now()})
	if err := k8s.InitTestResourceCache(fake.NewClientset(pod)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := k8s.InitTestResourceCache(testFakeClient); err != nil {
			t.Fatal(err)
		}
	})
	resolver := &insightsResolver{cache: k8s.GetResourceCache()}
	root := &unstructured.Unstructured{}
	root.SetAPIVersion("source.toolkit.fluxcd.io/v1")
	if got := resolver.FinalizerOwnerStatus("finalizers.source.toolkit.fluxcd.io", root); got != "source-controller is healthy (1 pod ready)" {
		t.Fatalf("rollout changed GitOps controller health text: %q", got)
	}
}

func TestGitOpsFinalizerOwnerMayRunOffCluster(t *testing.T) {
	if err := k8s.InitTestResourceCache(fake.NewClientset()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := k8s.InitTestResourceCache(testFakeClient); err != nil {
			t.Fatal(err)
		}
	})
	resolver := &insightsResolver{cache: k8s.GetResourceCache()}
	root := &unstructured.Unstructured{}
	root.SetAPIVersion("argoproj.io/v1alpha1")
	if got := resolver.FinalizerOwnerStatus("resources-finalizer.argocd.argoproj.io", root); !strings.Contains(got, "argocd-application-controller has no pods in namespace argocd (it may run outside the cluster under Amazon EKS Capabilities") {
		t.Fatalf("managed Argo CD read as missing: %q", got)
	}
}

func TestGitOpsTreeDeniesUnreadableKindInsideAllowedNamespace(t *testing.T) {
	s := &Server{permCache: auth.NewPermissionCache()}
	s.permCache.Set("source-user", nil, &auth.UserPermissions{AllowedNamespaces: []string{"team"}})
	s.permCache.Get("source-user", nil).SetCanI("get", "", "secrets", "team", false)
	req := &gitopsRequest{AllowedNamespaces: []string{"team"}}
	tree := &gitopstree.ResourceTree{
		Root: gitopstree.Node{ID: "root", Role: gitopstree.RoleRoot},
		Nodes: []gitopstree.Node{
			{ID: "root", Role: gitopstree.RoleRoot},
			{ID: "source", Role: gitopstree.RoleDeclared, Ref: gitopstree.ResourceRef{Kind: "Secret", Namespace: "team", Name: "unreadable"}, Data: map[string]any{"labels": map[string]string{"private": "metadata"}}},
		},
		Edges: []gitopstree.Edge{{Source: "root", Target: "source", Type: gitopstree.EdgeSource}},
	}
	r := httptest.NewRequest(http.MethodGet, "/api/gitops/tree", nil)
	r = r.WithContext(auth.ContextWithUser(r.Context(), &auth.User{Username: "source-user"}))
	filtered := s.filterGitOpsTreeForUser(r, req, tree)
	if len(filtered.Nodes) != 1 || len(filtered.Edges) != 0 || filtered.Summary.Declared != 0 {
		t.Fatalf("unreadable kind metadata leaked: %+v", filtered)
	}
}

func TestGitOpsUnknownNamespacedDeclarationIsNotReadDenied(t *testing.T) {
	s := &Server{permCache: auth.NewPermissionCache()}
	s.permCache.Set("declaration-user", nil, &auth.UserPermissions{AllowedNamespaces: []string{"team"}})
	r := httptest.NewRequest(http.MethodGet, "/api/gitops/tree", nil)
	r = r.WithContext(auth.ContextWithUser(r.Context(), &auth.User{Username: "declaration-user"}))
	req := &gitopsRequest{AllowedNamespaces: []string{"team"}}
	if !s.canAccessGitOpsRef(r, req, "not-installed.example", "RemoteWidget", "team", "declared", false) {
		t.Fatal("unknown discovery erased readable inventory declaration")
	}
	if s.canAccessGitOpsRef(r, req, "not-installed.example", "RemoteWidget", "other", "declared", false) {
		t.Fatal("unknown kind bypassed namespace scope")
	}
	if s.canAccessGitOpsRef(r, req, "", "Secret", "team", "private", false) {
		t.Fatal("known kind denial bypassed by unknown handling")
	}
}

func TestSyntheticPodGroupReadsPodsInsteadOfPodGroupAPIs(t *testing.T) {
	s := &Server{permCache: auth.NewPermissionCache()}
	permissions := &auth.UserPermissions{AllowedNamespaces: []string{"team"}}
	permissions.SetCanI("get", "", "pods", "team", true)
	s.permCache.Set("group-user", nil, permissions)
	r := httptest.NewRequest(http.MethodGet, "/api/gitops/tree", nil)
	r = r.WithContext(auth.ContextWithUser(r.Context(), &auth.User{Username: "group-user"}))
	req := &gitopsRequest{AllowedNamespaces: []string{"team"}}
	if !s.canAccessGitOpsRef(r, req, "", "PodGroup", "team", "display", false) {
		t.Fatal("synthetic group did not use cached pod access")
	}
	permissions.SetCanI("get", "", "pods", "team", false)
	if s.canAccessGitOpsRef(r, req, "", "PodGroup", "team", "display", false) {
		t.Fatal("synthetic group ignored pod denial")
	}
}

func TestRemoteDeclaredTreeNodePreservesRootInventory(t *testing.T) {
	s := &Server{permCache: auth.NewPermissionCache()}
	s.permCache.Set("fleet-user", nil, &auth.UserPermissions{AllowedNamespaces: []string{"argocd"}})
	root := gitopstree.Node{ID: "root", Role: gitopstree.RoleRoot}
	remote := gitopstree.Node{ID: "remote", Role: gitopstree.RoleDeclared, Remote: true, Ref: gitopstree.ResourceRef{Kind: "Secret", Namespace: "remote", Name: "declared"}, Sync: "Synced"}
	tree := &gitopstree.ResourceTree{Root: root, Nodes: []gitopstree.Node{root, remote}, Edges: []gitopstree.Edge{{Source: "root", Target: "remote", Type: gitopstree.EdgeOwns}}}
	r := httptest.NewRequest(http.MethodGet, "/api/gitops/tree", nil)
	r = r.WithContext(auth.ContextWithUser(r.Context(), &auth.User{Username: "fleet-user"}))
	filtered := s.filterGitOpsTreeForUser(r, &gitopsRequest{AllowedNamespaces: []string{"argocd"}}, tree)
	if len(filtered.Nodes) != 2 || len(filtered.Edges) != 1 {
		t.Fatalf("local permissions erased remote inventory: %+v", filtered)
	}
}

func TestRemoteInsightPreservesOnlyRemoteInventoryDeclarations(t *testing.T) {
	s := &Server{permCache: auth.NewPermissionCache()}
	s.permCache.Set("fleet-user", nil, &auth.UserPermissions{AllowedNamespaces: []string{"argocd"}})
	remote := gitopstree.Node{ID: "remote", Role: gitopstree.RoleDeclared, Remote: true, Ref: gitopstree.ResourceRef{Kind: "Secret", Namespace: "destination", Name: "declared"}}
	tree := &gitopstree.ResourceTree{Nodes: []gitopstree.Node{remote}}
	ref := gitopsinsights.Ref{Kind: "Secret", Namespace: "destination", Name: "declared"}
	local := gitopsinsights.Ref{Kind: "Secret", Namespace: "other-local", Name: "private"}
	input := gitopsinsights.Insight{Changes: []gitopsinsights.Change{{Ref: ref}, {Ref: local}}}
	r := httptest.NewRequest(http.MethodGet, "/api/gitops/insights", nil)
	r = r.WithContext(auth.ContextWithUser(r.Context(), &auth.User{Username: "fleet-user"}))
	out := s.filterGitOpsInsightForUser(r, &gitopsRequest{AllowedNamespaces: []string{"argocd"}}, input, tree)
	if len(out.Changes) != 1 || out.Changes[0].Ref != ref {
		t.Fatalf("remote inventory dropped or unrelated local ref exposed: %+v", out.Changes)
	}
}

func TestFluxSourceInsightDropsDeniedStatusText(t *testing.T) {
	s := &Server{permCache: auth.NewPermissionCache()}
	permissions := &auth.UserPermissions{AllowedNamespaces: []string{"team", "shared"}}
	s.permCache.Set("source-user", nil, permissions)
	r := httptest.NewRequest(http.MethodGet, "/api/gitops/insights", nil)
	r = r.WithContext(auth.ContextWithUser(r.Context(), &auth.User{Username: "source-user"}))
	source := gitopsinsights.Ref{Group: "source.toolkit.fluxcd.io", Kind: "GitRepository", Namespace: "shared", Name: "private"}
	issue := gitopsinsights.Issue{Severity: gitopsinsights.SeverityWarning, Scope: gitopsinsights.ScopeTree, Reason: "SourceNotReady", Message: "GitRepository shared/private can't fetch updates: private status text. No artifact has been fetched yet.", Refs: []gitopsinsights.Ref{source}}
	input := gitopsinsights.Insight{Issues: []gitopsinsights.Issue{issue}}
	out := s.filterGitOpsInsightForUser(r, &gitopsRequest{AllowedNamespaces: []string{"team"}}, input, nil)
	if len(out.Issues) != 0 {
		t.Fatalf("denied source status leaked: %+v", out.Issues)
	}
	if len(input.Issues) != 1 || len(input.Issues[0].Refs) != 1 {
		t.Fatal("filter mutated shared insight")
	}
	out = s.filterGitOpsInsightForUser(r, &gitopsRequest{AllowedNamespaces: []string{"team", "shared"}}, input, nil)
	if len(out.Issues) != 1 || out.Issues[0].Message != issue.Message || len(out.Issues[0].Refs) != 1 {
		t.Fatalf("readable source issue lost: %+v", out)
	}
}

func TestFluxTreeResummarizesDependenciesAfterFiltering(t *testing.T) {
	s := &Server{permCache: auth.NewPermissionCache()}
	permissions := &auth.UserPermissions{AllowedNamespaces: []string{"team"}}
	permissions.SetCanI("get", "apps", "deployments", "team", true)
	s.permCache.Set("tree-user", nil, permissions)
	r := httptest.NewRequest(http.MethodGet, "/api/gitops/tree", nil)
	r = r.WithContext(auth.ContextWithUser(r.Context(), &auth.User{Username: "tree-user"}))
	root := gitopstree.Node{ID: "root", Role: gitopstree.RoleRoot}
	nodes := []gitopstree.Node{root, {ID: "source", Role: gitopstree.RoleDeclared, Dependency: true, Ref: gitopstree.ResourceRef{Group: "source.toolkit.fluxcd.io", Kind: "GitRepository", Namespace: "shared", Name: "repo"}, Health: "Degraded"}, {ID: "workload", Role: gitopstree.RoleDeclared, Ref: gitopstree.ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "team", Name: "app"}, Health: "Degraded", Sync: "OutOfSync"}}
	input := &gitopstree.ResourceTree{Root: root, Nodes: nodes, Summary: gitopstree.Summarize(nodes)}
	out := s.filterGitOpsTreeForUser(r, &gitopsRequest{AllowedNamespaces: []string{"team"}}, input)
	if len(out.Nodes) != 2 || out.Summary.Dependencies != 0 || out.Summary.Declared != 1 || out.Summary.Degraded != 1 || out.Summary.OutOfSync != 1 || input.Summary.Dependencies != 1 {
		t.Fatalf("filtered summary: %+v", out)
	}
}

func TestFluxBlockerConditionSurvivesDeniedSource(t *testing.T) {
	s := &Server{permCache: auth.NewPermissionCache()}
	s.permCache.Set("blocker-user", nil, &auth.UserPermissions{AllowedNamespaces: []string{"team"}})
	r := httptest.NewRequest(http.MethodGet, "/api/gitops/insights", nil)
	r = r.WithContext(auth.ContextWithUser(r.Context(), &auth.User{Username: "blocker-user"}))
	source := gitopsinsights.Ref{Group: "source.toolkit.fluxcd.io", Kind: "GitRepository", Namespace: "shared", Name: "private"}
	issue := gitopsinsights.Issue{Severity: gitopsinsights.SeverityCritical, Scope: gitopsinsights.ScopeCondition, Reason: "ArtifactFailed", Message: "Source artifact not found, retrying in 30s", Cause: "GitRepository private: private status text", Refs: []gitopsinsights.Ref{source}}
	out := s.filterGitOpsInsightForUser(r, &gitopsRequest{AllowedNamespaces: []string{"team"}}, gitopsinsights.Insight{Issues: []gitopsinsights.Issue{issue}}, nil)
	if len(out.Issues) != 1 || out.Issues[0].Message != issue.Message || out.Issues[0].Cause != "" || len(out.Issues[0].Refs) != 0 {
		t.Fatalf("root condition lost or source status leaked: %+v", out.Issues)
	}
}
