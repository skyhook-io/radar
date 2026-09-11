package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/rollouts"
)

// A sentinel falling through to 500 reads as "Radar broke" when the real answer
// is "that revision is already live".
func TestWriteRolloutErrorStatusMapping(t *testing.T) {
	rolloutGR := schema.GroupResource{Group: "argoproj.io", Resource: "rollouts"}

	tests := []struct {
		name string
		err  error
		want int
	}{
		{"revision not found", fmt.Errorf("wrapped: %w", rollouts.ErrRevisionNotFound), http.StatusNotFound},
		{"apierror not found", apierrors.NewNotFound(rolloutGR, "web"), http.StatusNotFound},
		{"forbidden", apierrors.NewForbidden(rolloutGR, "web", errors.New("nope")), http.StatusForbidden},
		{"terminating", fmt.Errorf("wrapped: %w", rollouts.ErrResourceTerminating), http.StatusConflict},
		{"template unchanged", fmt.Errorf("wrapped: %w", rollouts.ErrTemplateUnchanged), http.StatusConflict},
		{"already at last step", fmt.Errorf("wrapped: %w", rollouts.ErrAlreadyAtLastStep), http.StatusConflict},
		{"no steps", fmt.Errorf("wrapped: %w", rollouts.ErrNoSteps), http.StatusBadRequest},
		{"unsupported workloadRef", fmt.Errorf("wrapped: %w", rollouts.ErrWorkloadRefUnsupported), http.StatusBadRequest},
		{"deadline exceeded", fmt.Errorf("wrapped: %w", context.DeadlineExceeded), http.StatusGatewayTimeout},
		{"controller not caught up", fmt.Errorf("wrapped: %w", rollouts.ErrControllerNotCaughtUp), http.StatusServiceUnavailable},
		{"unknown", errors.New("boom"), http.StatusInternalServerError},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			(&Server{}).writeRolloutError(w, tc.err, "abort", "prod", "web")

			if w.Code != tc.want {
				t.Errorf("status = %d, want %d", w.Code, tc.want)
			}
			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("response not JSON: %v (%s)", err, w.Body.String())
			}
			if body["error"] == nil || body["error"] == "" {
				t.Errorf("response has no error message: %s", w.Body.String())
			}
		})
	}
}

// {action} is a route wildcard, so this map is the allowlist — a verb added here
// without a matching capability flag is reachable but ungated.
func TestRolloutOperationsAllowlist(t *testing.T) {
	want := []string{"abort", "promote", "promote-full", "retry", "skip-step"}

	got := make([]string, 0, len(rolloutOperations))
	for action, op := range rolloutOperations {
		if op == nil {
			t.Errorf("action %q has a nil operation", action)
		}
		got = append(got, action)
	}
	sort.Strings(got)

	if len(got) != len(want) {
		t.Fatalf("actions = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("actions = %v, want %v", got, want)
		}
	}
}

// Without rollouts in this map the shared /workloads route 400s before k8score
// is consulted.
func TestRollbackableWorkloadKindsIncludesRollouts(t *testing.T) {
	for _, kind := range []string{"deployments", "statefulsets", "daemonsets", "rollouts"} {
		if !rollbackableWorkloadKinds[kind] {
			t.Errorf("rollbackableWorkloadKinds[%q] = false, want true", kind)
		}
	}
	if rollbackableWorkloadKinds["replicasets"] {
		t.Error("replicasets should not be rollbackable")
	}
}

// Restart shares the generic /workloads route, so a terminating Rollout there has
// to read 409 like rollback does rather than 500.
func TestRestartTerminatingRolloutReturns409(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{rollouts.GVR: "RolloutList"},
		&unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "argoproj.io/v1alpha1",
			"kind":       "Rollout",
			"metadata": map[string]any{
				"name":              "web",
				"namespace":         "prod",
				"deletionTimestamp": "2026-01-01T00:00:00Z",
				"finalizers":        []any{"argoproj.io/finalizer"},
			},
		}},
	)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{
		{Group: "argoproj.io", Version: "v1alpha1", Kind: "Rollout", Name: "rollouts", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch", "patch"}},
	}); err != nil {
		t.Fatalf("seed rollout: %v", err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)

	s := &Server{}
	router := chi.NewRouter()
	router.Post("/api/workloads/{kind}/{namespace}/{name}/restart", s.handleRestartWorkload)

	for _, kind := range []string{"rollouts", "rollout"} {
		t.Run(kind, func(t *testing.T) {
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest(http.MethodPost,
				"/api/workloads/"+kind+"/prod/web/restart", nil))

			if w.Code != http.StatusConflict {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, http.StatusConflict, w.Body.String())
			}
		})
	}
}

// Gating Rollback on `patch rollouts` shows the button to operators who cannot
// write the referenced workload, and hides it from those who can.
func TestRollbackAuthTargetFollowsTheObjectUndoPatches(t *testing.T) {
	ref := func(kind string) *unstructured.Unstructured {
		apiVersion := "apps/v1"
		if kind == "PodTemplate" {
			apiVersion = "v1"
		}
		return &unstructured.Unstructured{Object: map[string]any{
			"spec": map[string]any{
				"workloadRef": map[string]any{"apiVersion": apiVersion, "kind": kind, "name": "target"},
			},
		}}
	}

	tests := []struct {
		name         string
		ro           *unstructured.Unstructured
		wantGroup    string
		wantResource string
		wantOK       bool
	}{
		{"inline template authorizes the Rollout", &unstructured.Unstructured{Object: map[string]any{
			"spec": map[string]any{"template": map[string]any{}},
		}}, "argoproj.io", "rollouts", true},
		{"Deployment ref", ref("Deployment"), "apps", "deployments", true},
		{"ReplicaSet ref", ref("ReplicaSet"), "apps", "replicasets", true},
		{"PodTemplate ref is core group", ref("PodTemplate"), "", "podtemplates", true},
		{"unsupported ref denies rather than falling back", ref("StatefulSet"), "", "", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			group, resource, ok := rollbackAuthTarget(tc.ro)
			if group != tc.wantGroup || resource != tc.wantResource || ok != tc.wantOK {
				t.Errorf("rollbackAuthTarget() = (%q, %q, %v), want (%q, %q, %v)",
					group, resource, ok, tc.wantGroup, tc.wantResource, tc.wantOK)
			}
		})
	}
}

// Promoting a workloadRef Rollout reads the referenced workload, so the capability has
// to depend on that read — otherwise the button is offered and then denied.
func TestCanReadWorkloadRefSourceCoversTheTemplateSource(t *testing.T) {
	ref := func(kind string) *unstructured.Unstructured {
		apiVersion := "apps/v1"
		if kind == "PodTemplate" {
			apiVersion = "v1"
		}
		return &unstructured.Unstructured{Object: map[string]any{
			"spec": map[string]any{
				"workloadRef": map[string]any{"apiVersion": apiVersion, "kind": kind, "name": "target"},
			},
		}}
	}
	inline := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"template": map[string]any{}},
	}}
	// A ref that cannot be resolved has no readable template source to gate on, so the
	// capability is left alone and the operation reports the problem itself.
	unresolvable := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"workloadRef": map[string]any{"kind": "Deployment", "name": "target"},
		},
	}}

	tests := []struct {
		name string
		ro   *unstructured.Unstructured
		want bool
	}{
		{"inline template needs no extra read", inline, true},
		{"Deployment ref is readable without a permission cache", ref("Deployment"), true},
		{"unsupported ref is not gated on a read it never makes", ref("StatefulSet"), true},
		{"unresolvable ref is not gated on a read it never makes", unresolvable, true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if got := (&Server{}).canReadWorkloadRefSource(req, tc.ro, "prod"); got != tc.want {
				t.Errorf("canReadWorkloadRefSource() = %v, want %v", got, tc.want)
			}
		})
	}
}

// The whole point of the helper is the denied case: without it the UI offers Promote
// full and the operation then fails on a read the caller was never allowed to make.
func TestCanReadWorkloadRefSourceFollowsTheDeniedRead(t *testing.T) {
	workloadRef := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{
			"workloadRef": map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "target"},
		},
	}}

	for _, tc := range []struct {
		name    string
		allowed bool
	}{
		{"denied read hides the action", false},
		{"allowed read keeps it", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := &Server{permCache: auth.NewPermissionCache()}
			perms := &auth.UserPermissions{AllowedNamespaces: []string{"prod"}}
			perms.SetCanI("get", "apps", "deployments", "prod", tc.allowed)
			srv.permCache.Set("alice", nil, perms)

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req = req.WithContext(auth.ContextWithUser(req.Context(), &auth.User{Username: "alice"}))

			if got := srv.canReadWorkloadRefSource(req, workloadRef, "prod"); got != tc.allowed {
				t.Errorf("canReadWorkloadRefSource() = %v, want %v", got, tc.allowed)
			}
		})
	}
}
