package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/pkg/gitops"
	gitopstree "github.com/skyhook-io/radar/pkg/gitops/tree"
)

// makePodWithStatus constructs a minimal Pod with the container statuses
// summarizeControllerHealth inspects. Phase + ContainerStatuses are the
// only fields the function reads.
func makePodWithStatus(phase corev1.PodPhase, statuses ...corev1.ContainerStatus) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "pod"},
		Status:     corev1.PodStatus{Phase: phase, ContainerStatuses: statuses},
	}
}

// TestSummarizeControllerHealth pins the exact wording of the
// finalizer-owner status string. The output is concatenated into the
// lifecycle Issue's Cause field surfaced to operators, so a copy-paste
// regression that changed "1 pod ready" to "1 pods ready" or "Healthy"
// to "healthy" is part of the user-visible contract — assertions are
// exact-match on purpose.
func TestSummarizeControllerHealth(t *testing.T) {
	ready := corev1.ContainerStatus{Ready: true, State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}
	notReady := corev1.ContainerStatus{Ready: false}
	crashing := corev1.ContainerStatus{Ready: false, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff"}}}
	erroring := corev1.ContainerStatus{Ready: false, State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "Error"}}}

	tests := []struct {
		name string
		pods []*corev1.Pod
		want string
	}{
		{
			name: "single ready pod uses singular form",
			pods: []*corev1.Pod{makePodWithStatus(corev1.PodRunning, ready)},
			want: "argocd-application-controller is healthy (1 pod ready)",
		},
		{
			name: "two ready pods uses plural form",
			pods: []*corev1.Pod{
				makePodWithStatus(corev1.PodRunning, ready),
				makePodWithStatus(corev1.PodRunning, ready),
			},
			want: "argocd-application-controller is healthy (2 pods ready)",
		},
		{
			name: "any crashloop pod dominates aggregate",
			pods: []*corev1.Pod{
				makePodWithStatus(corev1.PodRunning, ready),
				makePodWithStatus(corev1.PodRunning, crashing),
			},
			want: "argocd-application-controller is CrashLoopBackOff (1/2 pods)",
		},
		{
			name: "Error reason surfaces alongside crashing count",
			pods: []*corev1.Pod{makePodWithStatus(corev1.PodRunning, erroring)},
			want: "argocd-application-controller is Error (1/1 pods)",
		},
		{
			name: "all pending and zero ready reports pending",
			pods: []*corev1.Pod{
				makePodWithStatus(corev1.PodPending),
				makePodWithStatus(corev1.PodPending),
			},
			want: "argocd-application-controller is pending start (2/2 pods Pending)",
		},
		{
			name: "partial ready falls back to degraded",
			pods: []*corev1.Pod{
				makePodWithStatus(corev1.PodRunning, ready),
				makePodWithStatus(corev1.PodRunning, notReady),
			},
			want: "argocd-application-controller is degraded (1/2 pods ready)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := summarizeControllerHealth("argocd-application-controller", tt.pods)
			if got != tt.want {
				t.Fatalf("summarizeControllerHealth =\n  got:  %q\n  want: %q", got, tt.want)
			}
		})
	}
}

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
// noNamespaceAccess contract (tested in server_auth_test.go) but as a
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
	s.permCache.Set("gitops-tree-user", &auth.UserPermissions{AllowedNamespaces: []string{"team-a"}})
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
