package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

func serverImageDeployment(image string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata":   map[string]any{"namespace": "prod", "name": "web"},
		"spec": map[string]any{
			"template": map[string]any{
				"spec": map[string]any{
					"containers": []any{map[string]any{"name": "app", "image": image}},
				},
			},
		},
	}}
}

func initWorkloadImageServer(t *testing.T, client *dynamicfake.FakeDynamicClient) *chi.Mux {
	t.Helper()
	if err := k8s.InitTestDynamicResourceCache(client, []k8s.APIResource{
		{Group: "apps", Version: "v1", Kind: "Deployment", Name: "deployments", Namespaced: true, Verbs: []string{"get", "patch"}},
	}); err != nil {
		t.Fatalf("InitTestDynamicResourceCache: %v", err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	router := chi.NewRouter()
	s := &Server{}
	router.Get("/api/workloads/{kind}/{namespace}/{name}/images", s.handleGetWorkloadImages)
	router.Post("/api/workloads/{kind}/{namespace}/{name}/images", s.handleSetWorkloadImages)
	return router
}

func TestGetWorkloadImagesHandler(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), serverImageDeployment("repo/app:v1"))
	router := initWorkloadImageServer(t, client)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/workloads/deployments/prod/web/images", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var body k8score.WorkloadImageInventory
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response JSON: %v", err)
	}
	if len(body.Containers) != 1 || body.Containers[0].Image != "repo/app:v1" {
		t.Errorf("inventory = %+v", body)
	}
}

func TestSetWorkloadImagesHandler(t *testing.T) {
	clearApplicationsCache()
	t.Cleanup(clearApplicationsCache)
	applicationsCacheMu.Lock()
	applicationsCache["test"] = applicationsCacheEntry{}
	applicationsCacheMu.Unlock()
	deployment := serverImageDeployment("repo/app:v1")
	updated := serverImageDeployment("repo/app:v2")
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), deployment)
	client.PrependReactor("patch", "deployments", func(action clienttesting.Action) (bool, runtime.Object, error) {
		patch := action.(clienttesting.PatchAction)
		if patch.GetPatchType() != types.JSONPatchType {
			t.Errorf("patch type = %s", patch.GetPatchType())
		}
		return true, updated, nil
	})
	router := initWorkloadImageServer(t, client)
	body := []byte(`{"updates":[{"type":"container","name":"app","previousImage":"repo/app:v1","image":"repo/app:v2"}]}`)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/workloads/deployments/prod/web/images", bytes.NewReader(body)))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body.String())
	}
	var result k8score.SetWorkloadImagesResult
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatalf("response JSON: %v", err)
	}
	if result.Containers[0].Image != "repo/app:v2" {
		t.Errorf("result = %+v", result.Containers)
	}
	applicationsCacheMu.Lock()
	defer applicationsCacheMu.Unlock()
	if len(applicationsCache) != 0 {
		t.Errorf("applications cache was not cleared after image update")
	}
}

func TestWriteWorkloadImageErrorStatusMapping(t *testing.T) {
	resource := schema.GroupResource{Group: "apps", Resource: "deployments"}
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"not found", apierrors.NewNotFound(resource, "web"), http.StatusNotFound},
		{"forbidden", apierrors.NewForbidden(resource, "web", errors.New("denied")), http.StatusForbidden},
		{"conflict", apierrors.NewConflict(resource, "web", errors.New("changed")), http.StatusConflict},
		{"terminating", fmt.Errorf("wrapped: %w", k8score.ErrImageWorkloadTerminating), http.StatusConflict},
		{"unsupported", fmt.Errorf("wrapped: %w", k8score.ErrUnsupportedImageWorkload), http.StatusBadRequest},
		{"invalid request", fmt.Errorf("wrapped: %w", k8score.ErrInvalidImageUpdate), http.StatusBadRequest},
		{"deadline", fmt.Errorf("wrapped: %w", context.DeadlineExceeded), http.StatusGatewayTimeout},
		{"unknown", errors.New("boom"), http.StatusInternalServerError},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			(&Server{}).writeWorkloadImageError(w, tc.err, "update", "deployments", "prod", "web")
			if w.Code != tc.want {
				t.Errorf("status = %d, want %d", w.Code, tc.want)
			}
		})
	}
}

func TestSetWorkloadImagesHandlerRejectsMalformedBody(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), serverImageDeployment("repo/app:v1"))
	router := initWorkloadImageServer(t, client)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/workloads/deployments/prod/web/images", bytes.NewBufferString("{")))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
}

func TestSetWorkloadImagesHandlerRejectsOversizedBody(t *testing.T) {
	client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), serverImageDeployment("repo/app:v1"))
	router := initWorkloadImageServer(t, client)
	w := httptest.NewRecorder()
	body := `{"updates":[{"type":"container","name":"app","previousImage":"a","image":"` + strings.Repeat("b", 128<<10) + `"}]}`
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/workloads/deployments/prod/web/images", strings.NewReader(body)))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400: %s", w.Code, w.Body.String())
	}
}
