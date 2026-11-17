package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/k8s"
)

var (
	triggerCronJobsGVR = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "cronjobs"}
	triggerJobsGVR     = schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}
)

func setupTriggerCronJobTest(t *testing.T) (*dynamicfake.FakeDynamicClient, http.Handler) {
	t.Helper()
	cronJob := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1",
		"kind":       "CronJob",
		"metadata":   map[string]any{"name": "report", "namespace": "prod", "uid": "cj-uid"},
		"spec":       map[string]any{"jobTemplate": map[string]any{"spec": map[string]any{}}},
	}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		triggerCronJobsGVR: "CronJobList", triggerJobsGVR: "JobList",
	}, cronJob)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{
		{Group: "batch", Version: "v1", Kind: "CronJob", Name: "cronjobs", Namespaced: true, Verbs: []string{"get", "list", "watch", "patch"}},
		{Group: "batch", Version: "v1", Kind: "Job", Name: "jobs", Namespaced: true, Verbs: []string{"get", "list", "watch", "create"}},
	}); err != nil {
		t.Fatalf("init test cache: %v", err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)

	router := chi.NewRouter()
	router.Post("/api/cronjobs/{namespace}/{name}/trigger", (&Server{}).handleTriggerCronJob)
	return dyn, router
}

func triggerCronJob(router http.Handler, body io.Reader) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/api/cronjobs/prod/report/trigger", body))
	return w
}

func createdTriggerJob(dyn *dynamicfake.FakeDynamicClient) *unstructured.Unstructured {
	for _, a := range dyn.Actions() {
		if ca, ok := a.(clienttesting.CreateAction); ok && ca.GetResource() == triggerJobsGVR {
			return ca.GetObject().(*unstructured.Unstructured)
		}
	}
	return nil
}

func TestTriggerCronJobWithoutABodyGeneratesTheName(t *testing.T) {
	dyn, router := setupTriggerCronJobTest(t)
	// The fake client ignores generateName; the API server fills the name in.
	dyn.PrependReactor("create", "jobs", func(action clienttesting.Action) (bool, runtime.Object, error) {
		obj := action.(clienttesting.CreateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
		if obj.GetName() == "" {
			obj.SetName(obj.GetGenerateName() + "x7k2p")
		}
		return true, obj, nil
	})

	w := triggerCronJob(router, nil)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	job := createdTriggerJob(dyn)
	if job == nil || job.GetGenerateName() != "report-manual-" {
		t.Fatalf("created Job = %v, want generateName report-manual-", job)
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if body["jobName"] != "report-manual-x7k2p" {
		t.Errorf("jobName = %q, want the server-assigned report-manual-x7k2p", body["jobName"])
	}
}

func TestTriggerCronJobMapsAPIServerErrors(t *testing.T) {
	jobs := schema.GroupResource{Group: "batch", Resource: "jobs"}
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"invalid (422) is 400", apierrors.NewInvalid(schema.GroupKind{Group: "batch", Kind: "Job"}, "report-rerun", field.ErrorList{field.Invalid(field.NewPath("spec"), "x", "bad")}), http.StatusBadRequest},
		{"bad request is 400", apierrors.NewBadRequest("bad"), http.StatusBadRequest},
		{"forbidden is 403", apierrors.NewForbidden(jobs, "report-rerun", errors.New("nope")), http.StatusForbidden},
		{"anything else is 500", errors.New("boom"), http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dyn, router := setupTriggerCronJobTest(t)
			dyn.PrependReactor("create", "jobs", func(clienttesting.Action) (bool, runtime.Object, error) {
				return true, nil, tc.err
			})

			w := triggerCronJob(router, strings.NewReader(`{"jobName":"report-rerun"}`))

			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

func TestTriggerCronJobUsesTheRequestedJobName(t *testing.T) {
	dyn, router := setupTriggerCronJobTest(t)

	w := triggerCronJob(router, strings.NewReader(`{"jobName":"report-rerun"}`))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	var body map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("response not JSON: %v", err)
	}
	if body["jobName"] != "report-rerun" {
		t.Errorf("jobName = %q, want report-rerun", body["jobName"])
	}
	if job := createdTriggerJob(dyn); job == nil || job.GetName() != "report-rerun" {
		t.Errorf("created Job = %v, want name report-rerun", job)
	}
}

func TestTriggerCronJobRejectsBadRequests(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       int
		wantError  string
	}{
		{"invalid name", `{"jobName":"Report_1"}`, http.StatusBadRequest, "invalid job name"},
		{"name over 63 characters", `{"jobName":"` + strings.Repeat("a", 64) + `"}`, http.StatusBadRequest, "no more than 63"},
		{"unknown field", `{"name":"x"}`, http.StatusBadRequest, "invalid request body"},
		{"oversized body", `{"jobName":"` + strings.Repeat("a", 8192) + `"}`, http.StatusRequestEntityTooLarge, "too large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dyn, router := setupTriggerCronJobTest(t)

			w := triggerCronJob(router, strings.NewReader(tc.body))

			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %s)", w.Code, tc.want, w.Body.String())
			}
			if !strings.Contains(w.Body.String(), tc.wantError) {
				t.Errorf("body = %s, want it to mention %q", w.Body.String(), tc.wantError)
			}
			if job := createdTriggerJob(dyn); job != nil {
				t.Errorf("created a Job for a bad request: %v", job.GetName())
			}
		})
	}
}

func TestTriggerCronJobNameTakenIsConflict(t *testing.T) {
	dyn, router := setupTriggerCronJobTest(t)
	if err := dyn.Tracker().Create(triggerJobsGVR, &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "batch/v1", "kind": "Job",
		"metadata": map[string]any{"name": "report-rerun", "namespace": "prod"},
	}}, "prod"); err != nil {
		t.Fatalf("seed job: %v", err)
	}

	w := triggerCronJob(router, strings.NewReader(`{"jobName":"report-rerun"}`))

	if w.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (body %s)", w.Code, w.Body.String())
	}
}
