package mcp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	clienttesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
)

var pauseDeploymentsGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}

func setupPauseDeployment(t *testing.T, paused bool) *dynamicfake.FakeDynamicClient {
	t.Helper()
	deployment := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]any{"name": "web", "namespace": "prod"},
		"spec":       map[string]any{"paused": paused},
	}}
	return setupMCPDynamicResource(t, pauseDeploymentsGVR, "DeploymentList", k8s.APIResource{
		Group: "apps", Version: "v1", Kind: "Deployment", Name: "deployments", Namespaced: true,
	}, deployment).(*dynamicfake.FakeDynamicClient)
}

func pausePatches(dyn *dynamicfake.FakeDynamicClient) int {
	n := 0
	for _, action := range dyn.Actions() {
		if action.GetVerb() == "patch" {
			n++
		}
	}
	return n
}

func TestManageWorkloadPauseAndResume(t *testing.T) {
	for _, tc := range []struct {
		name       string
		start      bool
		action     string
		kind       string
		wantPaused bool
		wantNext   bool
	}{
		{"pause", false, "pause", "deployment", true, false},
		{"resume", true, "resume", "deployment", false, true},
		{"pausing a paused Deployment succeeds", true, "pause", "deployment", true, false},
		{"resuming an unpaused Deployment succeeds", false, "resume", "deployment", false, true},
		{"action and kind spellings", false, "PAUSE", "Deployments", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dyn := setupPauseDeployment(t, tc.start)

			res, _, err := handleManageWorkload(context.Background(), nil, manageWorkloadInput{
				Action: tc.action, Kind: tc.kind, Namespace: "prod", Name: "web",
			})
			if err != nil {
				t.Fatalf("handleManageWorkload: %v", err)
			}

			body := decodeToolResult(t, res)
			if body["status"] != "ok" {
				t.Errorf("status = %v, want ok", body["status"])
			}
			if _, hasNext := body["next"]; hasNext != tc.wantNext {
				t.Errorf("next present = %v, want %v (%v)", hasNext, tc.wantNext, body["next"])
			}
			stored, err := dyn.Tracker().Get(pauseDeploymentsGVR, "prod", "web")
			if err != nil {
				t.Fatalf("read deployment: %v", err)
			}
			paused, _, _ := unstructured.NestedBool(stored.(*unstructured.Unstructured).Object, "spec", "paused")
			if paused != tc.wantPaused {
				t.Errorf("spec.paused = %v, want %v", paused, tc.wantPaused)
			}
		})
	}
}

func TestManageWorkloadPauseAndResumeRejectNonDeployments(t *testing.T) {
	for _, kind := range []string{"statefulset", "daemonset", "rollout"} {
		for _, action := range []string{"pause", "resume"} {
			t.Run(kind+"/"+action, func(t *testing.T) {
				dyn := setupPauseDeployment(t, false)

				_, _, err := handleManageWorkload(context.Background(), nil, manageWorkloadInput{
					Action: action, Kind: kind, Namespace: "prod", Name: "web",
				})
				if err == nil || !strings.Contains(err.Error(), "only supported for Deployments") {
					t.Fatalf("err = %v, want the Deployment-only refusal", err)
				}
				if n := pausePatches(dyn); n != 0 {
					t.Errorf("issued %d patches for %s", n, kind)
				}
			})
		}
	}
}

func TestManageWorkloadPauseKeepsTheAPIError(t *testing.T) {
	dyn := setupPauseDeployment(t, false)
	denied := apierrors.NewForbidden(schema.GroupResource{Group: "apps", Resource: "deployments"}, "web", errors.New("nope"))
	dyn.PrependReactor("patch", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
		return true, nil, denied
	})

	_, _, err := handleManageWorkload(context.Background(), nil, manageWorkloadInput{
		Action: "pause", Kind: "deployment", Namespace: "prod", Name: "web",
	})
	if !apierrors.IsForbidden(err) || !strings.HasPrefix(err.Error(), "pause failed:") {
		t.Fatalf("err = %v, want a Forbidden wrapped as pause failed", err)
	}
}

func TestManageWorkloadPausePatchesAsTheCaller(t *testing.T) {
	dyn := setupPauseDeployment(t, false)

	var requests []string
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.Method+" as "+r.Header.Get("Impersonate-User"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"web","namespace":"prod"}}`))
	}))
	t.Cleanup(api.Close)
	previous := k8s.SetTestConfig(&rest.Config{Host: api.URL})
	t.Cleanup(func() { k8s.SetTestConfig(previous) })

	ctx := pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: "alice"})
	if _, _, err := handleManageWorkload(ctx, nil, manageWorkloadInput{
		Action: "pause", Kind: "deployment", Namespace: "prod", Name: "web",
	}); err != nil {
		t.Fatalf("handleManageWorkload: %v", err)
	}

	if len(requests) != 1 || requests[0] != "PATCH as alice" {
		t.Errorf("API requests = %v, want one PATCH as alice", requests)
	}
	if n := pausePatches(dyn); n != 0 {
		t.Errorf("issued %d patches through Radar's own client", n)
	}
}

// With auth on, the patch has to run as the caller. When the impersonating
// client can't be built, Radar's own client must not stand in for it.
func TestManageWorkloadPauseNeverFallsBackToRadarsIdentity(t *testing.T) {
	dyn := setupPauseDeployment(t, false)
	ctx := pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: "alice"})

	_, _, err := handleManageWorkload(ctx, nil, manageWorkloadInput{
		Action: "pause", Kind: "deployment", Namespace: "prod", Name: "web",
	})
	if err == nil {
		t.Fatal("want an error when the impersonating client is unavailable")
	}
	if n := pausePatches(dyn); n != 0 {
		t.Errorf("issued %d patches through Radar's own client", n)
	}
}
