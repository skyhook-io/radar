package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/go-chi/chi/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
)

const submitPath = "/api/workflowtemplates/jobs/migrate/submit"

func submitTemplate(resourceVersion string, labels map[string]any) *unstructured.Unstructured {
	metadata := map[string]any{"name": "migrate", "namespace": "jobs", "uid": "template-1", "resourceVersion": resourceVersion}
	if labels != nil {
		metadata["labels"] = labels
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "WorkflowTemplate",
		"metadata": metadata,
		"spec": map[string]any{
			"entrypoint": "main",
			"arguments": map[string]any{"parameters": []any{
				map[string]any{"name": "target"},
				map[string]any{"name": "mode", "value": "dry-run", "enum": []any{"dry-run", "apply"}},
				map[string]any{"name": "batch", "value": int64(10)},
				map[string]any{"name": "region", "valueFrom": map[string]any{"configMapKeyRef": map[string]any{"name": "defaults", "key": "region"}}},
			}},
		},
	}}
}

func workflowSubmitServer(t *testing.T, objs ...runtime.Object) (*Server, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		argoWorkflowGVR:         "WorkflowList",
		argoWorkflowTemplateGVR: "WorkflowTemplateList",
	}, objs...)
	created := 0
	// The fake tracker neither completes generateName nor assigns UIDs.
	dyn.PrependReactor("create", "workflows", func(action k8stesting.Action) (bool, runtime.Object, error) {
		obj := action.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured)
		created++
		if obj.GetName() == "" {
			suffix := "abcde"
			if created > 1 {
				suffix = fmt.Sprintf("abc%02d", created)
			}
			obj.SetName(obj.GetGenerateName() + suffix)
		}
		obj.SetUID(types.UID("run-" + obj.GetName()))
		return false, nil, nil
	})
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{
		{Group: "argoproj.io", Version: "v1alpha1", Kind: "Workflow", Name: "workflows", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "create"}},
		{Group: "argoproj.io", Version: "v1alpha1", Kind: "WorkflowTemplate", Name: "workflowtemplates", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list"}},
	}); err != nil {
		t.Fatalf("seed workflow kinds: %v", err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	srv := New(Config{DevMode: true})
	t.Cleanup(srv.Stop)
	return srv, dyn
}

func postSubmit(srv *Server, body string, header map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, submitPath, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range header {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	return rec
}

func workflowCreates(dyn *dynamicfake.FakeDynamicClient) []*unstructured.Unstructured {
	var out []*unstructured.Unstructured
	for _, a := range dyn.Actions() {
		if c, ok := a.(k8stesting.CreateAction); ok && a.GetResource().Resource == "workflows" {
			out = append(out, c.GetObject().(*unstructured.Unstructured))
		}
	}
	return out
}

func errorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return body["error_code"]
}

func TestWorkflowTemplateSubmit(t *testing.T) {
	t.Run("creates a Workflow that references the template, with the values in the template's order", func(t *testing.T) {
		srv, dyn := workflowSubmitServer(t, submitTemplate("7", map[string]any{argoControllerInstanceLabel: "team-a", "app": "db"}))
		rec := postSubmit(srv, `{"parameters":{"mode":"apply","target":"orders"}}`, nil)
		var resp WorkflowTemplateSubmitResponse
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &resp) != nil {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if resp != (WorkflowTemplateSubmitResponse{Namespace: "jobs", Name: "migrate-abcde", UID: "run-migrate-abcde"}) {
			t.Fatalf("response = %+v", resp)
		}
		creates := workflowCreates(dyn)
		if len(creates) != 1 {
			t.Fatalf("creates = %d", len(creates))
		}
		wf := creates[0]
		if wf.GetGenerateName() != "migrate-" || wf.GetNamespace() != "jobs" || len(wf.GetAnnotations()) != 0 {
			t.Fatalf("metadata = %v", wf.Object["metadata"])
		}
		// The controller-instance label so the template's controller runs it;
		// the template's other labels are not copied.
		if want := map[string]string{argoControllerInstanceLabel: "team-a", argoWorkflowTemplateLabel: "migrate"}; !reflect.DeepEqual(wf.GetLabels(), want) {
			t.Fatalf("labels = %v, want %v", wf.GetLabels(), want)
		}
		want := map[string]any{
			"workflowTemplateRef": map[string]any{"name": "migrate"},
			"arguments": map[string]any{"parameters": []any{
				map[string]any{"name": "target", "value": "orders"},
				map[string]any{"name": "mode", "value": "apply"},
			}},
		}
		if !reflect.DeepEqual(wf.Object["spec"], want) {
			t.Fatalf("spec = %v, want %v", wf.Object["spec"], want)
		}
	})
	t.Run("an empty body runs a template whose parameters all have defaults", func(t *testing.T) {
		tmpl := submitTemplate("7", nil)
		_ = unstructured.SetNestedSlice(tmpl.Object, []any{map[string]any{"name": "mode", "value": "dry-run"}}, "spec", "arguments", "parameters")
		srv, dyn := workflowSubmitServer(t, tmpl)
		rec := postSubmit(srv, "", nil)
		if rec.Code != http.StatusOK || len(workflowCreates(dyn)) != 1 {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		wf := workflowCreates(dyn)[0]
		if _, found := wf.Object["spec"].(map[string]any)["arguments"]; found {
			t.Fatalf("defaults were copied into the run: %v", wf.Object["spec"])
		}
		if _, found := wf.GetLabels()[argoControllerInstanceLabel]; found {
			t.Fatalf("a template without an instance ID labelled the run: %v", wf.GetLabels())
		}
	})
	t.Run("parameters are checked against the template's declarations", func(t *testing.T) {
		srv, dyn := workflowSubmitServer(t, submitTemplate("7", nil))
		for body, wants := range map[string][]string{
			`{}`: {`parameter "target" is required`},
			`{"parameters":{"target":"x","mode":"destroy"}}`:  {`parameter "mode" must be one of dry-run, apply`},
			`{"parameters":{"target":"x","tagret":"y"}}`:      {`the template declares no parameter "tagret"`},
			`{"parameters":{"mode":"destroy","extra":"1"}}`:   {`"target" is required`, `"mode" must be one of`, `no parameter "extra"`},
			`{"parameters":{"target":"x","batch":"",":":""}}`: {`no parameter ":"`},
		} {
			rec := postSubmit(srv, body, nil)
			if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "invalid_parameters" {
				t.Errorf("%s: status = %d: %s", body, rec.Code, rec.Body.String())
				continue
			}
			for _, want := range wants {
				if !strings.Contains(rec.Body.String(), strings.ReplaceAll(want, `"`, `\"`)) {
					t.Errorf("%s: body %s lacks %s", body, rec.Body.String(), want)
				}
			}
		}
		if n := len(workflowCreates(dyn)); n != 0 {
			t.Fatalf("a refused request created %d workflows", n)
		}
	})
	t.Run("the entrypoint's inputs are parameters too, as Argo passes arguments to them by name", func(t *testing.T) {
		tmpl := submitTemplate("7", nil)
		_ = unstructured.SetNestedSlice(tmpl.Object, []any{
			map[string]any{"name": "main", "inputs": map[string]any{"parameters": []any{
				map[string]any{"name": "target"},
				map[string]any{"name": "message"},
				map[string]any{"name": "color", "default": "blue", "enum": []any{"blue", "red"}},
			}}},
			map[string]any{"name": "helper", "inputs": map[string]any{"parameters": []any{map[string]any{"name": "unrelated"}}}},
		}, "spec", "templates")
		srv, dyn := workflowSubmitServer(t, tmpl)
		rec := postSubmit(srv, `{"parameters":{"target":"x"}}`, nil)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `parameter \"message\" is required`) || strings.Contains(rec.Body.String(), "color") {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if rec := postSubmit(srv, `{"parameters":{"target":"x","message":"hi","color":"green"}}`, nil); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `must be one of blue, red`) {
			t.Fatalf("enum: %d %s", rec.Code, rec.Body.String())
		}
		if rec := postSubmit(srv, `{"parameters":{"target":"x","message":"hi","unrelated":"y"}}`, nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("another template's input was accepted: %d %s", rec.Code, rec.Body.String())
		}
		if rec := postSubmit(srv, `{"parameters":{"message":"hi","target":"x","color":"red"}}`, nil); rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		got, _, _ := unstructured.NestedSlice(workflowCreates(dyn)[0].Object, "spec", "arguments", "parameters")
		want := []any{
			map[string]any{"name": "target", "value": "x"},
			map[string]any{"name": "message", "value": "hi"},
			map[string]any{"name": "color", "value": "red"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("arguments = %v, want %v", got, want)
		}
	})
	t.Run("a template without an entrypoint is refused", func(t *testing.T) {
		tmpl := submitTemplate("7", nil)
		unstructured.RemoveNestedField(tmpl.Object, "spec", "entrypoint")
		srv, dyn := workflowSubmitServer(t, tmpl)
		rec := postSubmit(srv, `{"parameters":{"target":"x"}}`, nil)
		if rec.Code != http.StatusBadRequest || errorCode(t, rec) != "workflow_template_no_entrypoint" || len(workflowCreates(dyn)) != 0 {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("only a ConfigMap reference stands in for a value", func(t *testing.T) {
		tmpl := submitTemplate("7", nil)
		_ = unstructured.SetNestedSlice(tmpl.Object, []any{map[string]any{"name": "approval", "valueFrom": map[string]any{"supplied": map[string]any{}}}}, "spec", "arguments", "parameters")
		srv, _ := workflowSubmitServer(t, tmpl)
		if rec := postSubmit(srv, `{}`, nil); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "approval") {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("a rendered value: null is no value", func(t *testing.T) {
		tmpl := submitTemplate("7", nil)
		_ = unstructured.SetNestedSlice(tmpl.Object, []any{map[string]any{"name": "target", "value": nil}}, "spec", "arguments", "parameters")
		srv, _ := workflowSubmitServer(t, tmpl)
		if rec := postSubmit(srv, `{}`, nil); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "required") {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("a long template name keeps the separator in the generated name", func(t *testing.T) {
		long := strings.Repeat("a", 56) + "-.b" + strings.Repeat("c", 190)
		tmpl := submitTemplate("7", nil)
		tmpl.SetName(long)
		srv, dyn := workflowSubmitServer(t, tmpl)
		req := httptest.NewRequest(http.MethodPost, "/api/workflowtemplates/jobs/"+long+"/submit", strings.NewReader(`{"parameters":{"target":"x"}}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		wf := workflowCreates(dyn)[0]
		if want := strings.Repeat("a", 56) + "-"; wf.GetGenerateName() != want {
			t.Fatalf("generateName = %q, want %q", wf.GetGenerateName(), want)
		}
		if _, found := wf.GetLabels()[argoWorkflowTemplateLabel]; found {
			t.Fatalf("a name too long for a label value was copied into one: %v", wf.GetLabels())
		}
	})
	t.Run("a create Kubernetes can't place is 404, not 500", func(t *testing.T) {
		srv, dyn := workflowSubmitServer(t, submitTemplate("7", nil))
		dyn.PrependReactor("create", "workflows", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewNotFound(schema.GroupResource{Resource: "namespaces"}, "jobs")
		})
		if rec := postSubmit(srv, `{"parameters":{"target":"x"}}`, nil); rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("a null value is refused, an empty string is a value", func(t *testing.T) {
		srv, dyn := workflowSubmitServer(t, submitTemplate("7", nil))
		if rec := postSubmit(srv, `{"parameters":{"target":null}}`, nil); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "null") {
			t.Fatalf("null: %d %s", rec.Code, rec.Body.String())
		}
		if rec := postSubmit(srv, `{"parameters":{"target":""}}`, nil); rec.Code != http.StatusOK || len(workflowCreates(dyn)) != 1 {
			t.Fatalf("empty: %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("an enum accepts a value Argo reads from a YAML number", func(t *testing.T) {
		tmpl := submitTemplate("7", nil)
		_ = unstructured.SetNestedSlice(tmpl.Object, []any{map[string]any{"name": "replicas", "value": int64(1), "enum": []any{int64(1), int64(3), 1.5, true}}}, "spec", "arguments", "parameters")
		srv, _ := workflowSubmitServer(t, tmpl)
		for _, v := range []string{"3", "1.5", "true"} {
			if rec := postSubmit(srv, `{"parameters":{"replicas":"`+v+`"}}`, nil); rec.Code != http.StatusOK {
				t.Errorf("%s: status = %d: %s", v, rec.Code, rec.Body.String())
			}
		}
	})
	t.Run("refuses malformed requests before reading the template", func(t *testing.T) {
		srv, dyn := workflowSubmitServer(t, submitTemplate("7", nil))
		for _, b := range []string{
			`null`, `not json`, `[]`, `{"unknown":1}`, `{} {}`,
			`{"name":"a","generateName":"b-"}`,
			`{"submission":"confirmation-1"}`,
			`{"name":"migrate-1","submission":"short"}`,
			`{"name":"migrate-1","submission":"has spaces in it"}`,
			`{"name":"Migrate"}`,
			`{"name":"` + strings.Repeat("a", 64) + `"}`,
			`{"generateName":"Bad_"}`,
			`{"generateName":"` + strings.Repeat("a", 59) + `"}`,
			`{"parameters":["target=x"]}`,
		} {
			if rec := postSubmit(srv, b, nil); rec.Code != http.StatusBadRequest {
				t.Errorf("%s: status %d: %s", b, rec.Code, rec.Body.String())
			}
		}
		if len(dyn.Actions()) != 0 {
			t.Fatalf("a refused request reached Kubernetes: %v", dyn.Actions())
		}
	})
	t.Run("a body over the limit is 413", func(t *testing.T) {
		srv, dyn := workflowSubmitServer(t, submitTemplate("7", nil))
		rec := postSubmit(srv, `{"parameters":{"target":"`+strings.Repeat("x", 300<<10)+`"}}`, nil)
		if rec.Code != http.StatusRequestEntityTooLarge || len(workflowCreates(dyn)) != 0 {
			t.Fatalf("status = %d", rec.Code)
		}
	})
	t.Run("name and generateName are used as given", func(t *testing.T) {
		srv, dyn := workflowSubmitServer(t, submitTemplate("7", nil))
		if rec := postSubmit(srv, `{"name":"migrate-orders","parameters":{"target":"x"}}`, nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"migrate-orders"`) {
			t.Fatalf("named: %d %s", rec.Code, rec.Body.String())
		}
		if rec := postSubmit(srv, `{"generateName":"nightly-","parameters":{"target":"x"}}`, nil); rec.Code != http.StatusOK {
			t.Fatalf("generated: %d %s", rec.Code, rec.Body.String())
		}
		creates := workflowCreates(dyn)
		if creates[0].GetName() != "migrate-orders" || creates[0].GetGenerateName() != "" || creates[1].GetGenerateName() != "nightly-" {
			t.Fatalf("names = %q/%q, %q", creates[0].GetName(), creates[0].GetGenerateName(), creates[1].GetGenerateName())
		}
	})
	t.Run("a changed template is refused when the caller pinned the version it reviewed", func(t *testing.T) {
		srv, dyn := workflowSubmitServer(t, submitTemplate("8", nil))
		rec := postSubmit(srv, `{"resourceVersion":"7","parameters":{"target":"x"}}`, nil)
		if rec.Code != http.StatusConflict || errorCode(t, rec) != "workflow_template_changed" || len(workflowCreates(dyn)) != 0 {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
		if rec := postSubmit(srv, `{"resourceVersion":"8","parameters":{"target":"x"}}`, nil); rec.Code != http.StatusOK {
			t.Fatalf("current version: %d %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("repeating a request with its name and submission returns the run it created", func(t *testing.T) {
		srv, dyn := workflowSubmitServer(t, submitTemplate("7", nil))
		const body = `{"name":"migrate-k2x9q","submission":"confirmation-0001","resourceVersion":"7","parameters":{"target":"x"}}`
		first := postSubmit(srv, body, nil)
		if first.Code != http.StatusOK {
			t.Fatalf("first = %d: %s", first.Code, first.Body.String())
		}
		if got := workflowCreates(dyn)[0].GetAnnotations()[workflowSubmissionAnnotation]; got != "confirmation-0001" {
			t.Fatalf("submission annotation = %q", got)
		}
		// The template changed after the run was created; the repeat still
		// answers with the run, rather than refusing a version it already used.
		if err := dyn.Tracker().Update(argoWorkflowTemplateGVR, submitTemplate("8", nil), "jobs"); err != nil {
			t.Fatal(err)
		}
		again := postSubmit(srv, body, nil)
		if again.Code != http.StatusOK || again.Body.String() != first.Body.String() || len(workflowCreates(dyn)) != 1 {
			t.Fatalf("again = %d: %s (first %s), creates = %d", again.Code, again.Body.String(), first.Body.String(), len(workflowCreates(dyn)))
		}
	})
	t.Run("a submission repeated with other parameters is refused, not answered with the first run", func(t *testing.T) {
		srv, dyn := workflowSubmitServer(t, submitTemplate("7", nil))
		if rec := postSubmit(srv, `{"name":"migrate-k2x9q","submission":"confirmation-0001","parameters":{"target":"orders"}}`, nil); rec.Code != http.StatusOK {
			t.Fatalf("first = %d: %s", rec.Code, rec.Body.String())
		}
		rec := postSubmit(srv, `{"name":"migrate-k2x9q","submission":"confirmation-0001","parameters":{"target":"invoices"}}`, nil)
		if rec.Code != http.StatusConflict || errorCode(t, rec) != "workflow_submission_reused" || len(workflowCreates(dyn)) != 1 {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("a taken name Radar can't read is not called another run", func(t *testing.T) {
		for name, readErr := range map[string]error{
			"denied":    apierrors.NewForbidden(argoWorkflowGVR.GroupResource(), "migrate-k2x9q", errors.New("no get")),
			"timed out": apierrors.NewTimeoutError("slow", 1),
		} {
			t.Run(name, func(t *testing.T) {
				srv, dyn := workflowSubmitServer(t, submitTemplate("7", nil), withAnnotation(testWorkflow("Running", ""), workflowSubmissionAnnotation, "confirmation-0001"))
				dyn.PrependReactor("get", "workflows", func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, readErr })
				rec := postSubmit(srv, `{"name":"migrate-abcde","submission":"confirmation-0001","parameters":{"target":"x"}}`, nil)
				if rec.Code != http.StatusConflict || errorCode(t, rec) != "workflow_unverified" || strings.Contains(rec.Body.String(), "choose another name") {
					t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
				}
			})
		}
	})
	t.Run("a name taken by another run is 409", func(t *testing.T) {
		for name, existing := range map[string]*unstructured.Unstructured{
			"no submission":      testWorkflow("Running", ""),
			"another submission": withAnnotation(testWorkflow("Running", ""), workflowSubmissionAnnotation, "confirmation-0002"),
			"another template":   withTemplateRef(withAnnotation(testWorkflow("Running", ""), workflowSubmissionAnnotation, "confirmation-0001"), "other", false),
			"a cluster template": withTemplateRef(withAnnotation(testWorkflow("Running", ""), workflowSubmissionAnnotation, "confirmation-0001"), "migrate", true),
		} {
			t.Run(name, func(t *testing.T) {
				srv, dyn := workflowSubmitServer(t, submitTemplate("7", nil), existing)
				rec := postSubmit(srv, `{"name":"migrate-abcde","submission":"confirmation-0001","parameters":{"target":"x"}}`, nil)
				if rec.Code != http.StatusConflict || errorCode(t, rec) != "workflow_name_taken" || len(workflowCreates(dyn)) != 0 {
					t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
				}
			})
		}
		t.Run("without a submission the create decides", func(t *testing.T) {
			srv, dyn := workflowSubmitServer(t, submitTemplate("7", nil), withAnnotation(testWorkflow("Running", ""), workflowSubmissionAnnotation, "confirmation-0001"))
			rec := postSubmit(srv, `{"name":"migrate-abcde","parameters":{"target":"x"}}`, nil)
			if rec.Code != http.StatusConflict || errorCode(t, rec) != "workflow_name_taken" || len(workflowCreates(dyn)) != 1 {
				t.Fatalf("status = %d: %s, creates = %d", rec.Code, rec.Body.String(), len(workflowCreates(dyn)))
			}
		})
	})
	t.Run("a run created between the check and the create answers the repeat", func(t *testing.T) {
		srv, dyn := workflowSubmitServer(t, submitTemplate("7", nil))
		dyn.PrependReactor("create", "workflows", func(action k8stesting.Action) (bool, runtime.Object, error) {
			won := action.(k8stesting.CreateAction).GetObject().(*unstructured.Unstructured).DeepCopy()
			won.SetUID("run-first")
			if err := dyn.Tracker().Add(won); err != nil {
				t.Fatal(err)
			}
			return true, nil, apierrors.NewAlreadyExists(argoWorkflowGVR.GroupResource(), won.GetName())
		})
		rec := postSubmit(srv, `{"name":"migrate-k2x9q","submission":"confirmation-0001","parameters":{"target":"x"}}`, nil)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"uid":"run-first"`) {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("a missing template is 404 and creates nothing", func(t *testing.T) {
		srv, dyn := workflowSubmitServer(t)
		if rec := postSubmit(srv, `{"parameters":{"target":"x"}}`, nil); rec.Code != http.StatusNotFound || len(workflowCreates(dyn)) != 0 {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("a refused create is 403 with Kubernetes' reason", func(t *testing.T) {
		srv, dyn := workflowSubmitServer(t, submitTemplate("7", nil))
		dyn.PrependReactor("create", "workflows", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(argoWorkflowGVR.GroupResource(), "", errors.New(`User "jane" cannot create resource "workflows"`))
		})
		rec := postSubmit(srv, `{"parameters":{"target":"x"}}`, nil)
		if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `cannot create resource \"workflows\"`) {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("a refused template read is 403 and creates nothing", func(t *testing.T) {
		srv, dyn := workflowSubmitServer(t, submitTemplate("7", nil))
		dyn.PrependReactor("get", "workflowtemplates", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(argoWorkflowTemplateGVR.GroupResource(), "migrate", errors.New("denied"))
		})
		if rec := postSubmit(srv, `{"parameters":{"target":"x"}}`, nil); rec.Code != http.StatusForbidden || len(workflowCreates(dyn)) != 0 {
			t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
		}
	})
	t.Run("a namespace outside the --namespace-scope pin is refused before any Kubernetes call", func(t *testing.T) {
		srv, dyn := workflowSubmitServer(t, submitTemplate("7", nil))
		previous := k8s.ForceNamespaceScope
		k8s.ForceNamespaceScope = true
		k8s.SetFallbackNamespace("other")
		t.Cleanup(func() { k8s.ForceNamespaceScope = previous; k8s.SetFallbackNamespace("") })
		if rec := postSubmit(srv, `{"parameters":{"target":"x"}}`, nil); rec.Code != http.StatusForbidden || len(dyn.Actions()) != 0 {
			t.Fatalf("status = %d, actions = %v", rec.Code, dyn.Actions())
		}
	})
	// Shared without authentication: anyone who reaches Radar may submit as
	// Radar's identity, like any workload action, but a web page in their
	// browser may not.
	t.Run("a page on another site can't submit", func(t *testing.T) {
		srv, dyn := workflowSubmitServer(t, submitTemplate("7", nil))
		rec := postSubmit(srv, `{"parameters":{"target":"x"}}`, map[string]string{"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"})
		if rec.Code != http.StatusForbidden || len(dyn.Actions()) != 0 {
			t.Fatalf("status = %d actions = %v", rec.Code, dyn.Actions())
		}
	})
}

func withAnnotation(obj *unstructured.Unstructured, key, value string) *unstructured.Unstructured {
	obj.SetAnnotations(map[string]string{key: value})
	return obj
}

func withTemplateRef(obj *unstructured.Unstructured, name string, clusterScope bool) *unstructured.Unstructured {
	ref := map[string]any{"name": name}
	if clusterScope {
		ref["clusterScope"] = true
	}
	_ = unstructured.SetNestedMap(obj.Object, ref, "spec", "workflowTemplateRef")
	return obj
}

func TestWorkflowTemplateSubmitReadsAndCreatesAsTheCaller(t *testing.T) {
	srv, dyn := workflowSubmitServer(t, submitTemplate("7", nil))
	var mu sync.Mutex
	var requests []string
	groups := []string{"operators", "on-call"}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.Path+" as "+r.Header.Get("Impersonate-User"))
		mu.Unlock()
		if got := r.Header.Values("Impersonate-Group"); !slices.Equal(got, groups) {
			t.Errorf("%s impersonated groups = %v, want %v", r.Method, got, groups)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode(submitTemplate("7", nil))
			return
		}
		var wf unstructured.Unstructured
		_ = json.NewDecoder(r.Body).Decode(&wf.Object)
		wf.SetName("migrate-zzzzz")
		wf.SetUID("run-9")
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(wf.Object)
	}))
	t.Cleanup(api.Close)
	previous := k8s.SetTestConfig(&rest.Config{Host: api.URL})
	t.Cleanup(func() { k8s.SetTestConfig(previous) })

	router := chi.NewRouter()
	router.Post("/api/workflowtemplates/{namespace}/{name}/submit", srv.bindWrite(srv.handleWorkflowTemplateSubmit))
	req := httptest.NewRequest(http.MethodPost, submitPath, strings.NewReader(`{"parameters":{"target":"x"}}`))
	req = req.WithContext(auth.ContextWithUser(req.Context(), &auth.User{Username: "alice", Groups: groups}))
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"uid":"run-9"`) {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	want := []string{
		"GET /apis/argoproj.io/v1alpha1/namespaces/jobs/workflowtemplates/migrate as alice",
		"POST /apis/argoproj.io/v1alpha1/namespaces/jobs/workflows as alice",
	}
	if !slices.Equal(requests, want) {
		t.Fatalf("API requests = %v, want %v", requests, want)
	}
	if n := len(dyn.Actions()); n != 0 {
		t.Fatalf("issued %d requests through Radar's own client", n)
	}
}
