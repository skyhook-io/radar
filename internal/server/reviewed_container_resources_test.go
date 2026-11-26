package server

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestReviewedResourceQuantities(t *testing.T) {
	for _, tc := range []struct{ dimension, supplied, want string }{
		{"cpu", "1000m", "1"}, {"cpu", ".5", "500m"}, {"cpu", "1000", "1k"},
		{"memory", "1024Mi", "1Gi"}, {"memory", "1073741824", "1Gi"}, {"memory", "1.25Gi", "1280Mi"},
		{"memory", "2G", "2G"}, {"memory", "1.5Ki", "1536"}, {"memory", "0.000000001G", "1"},
		{"cpu", "100.1m", ""}, {"memory", "1.5", ""}, {"memory", "400m", ""},
		{"memory", "8Ei", ""}, {"cpu", "9223372036854775808m", ""},
		{"cpu", "0", ""}, {"cpu", "-1", ""}, {"cpu", "1e999999999", ""}, {"memory", "0.999999999999999999", ""},
		{"cpu", "+1", ""}, {"cpu", " 1", ""}, {"memory", "1e3", ""}, {"memory", "1K", ""},
	} {
		got, err := desiredResourceValue(workloadResourceValue(tc.supplied), tc.dimension)
		if (err != nil) != (tc.want == "") || string(got) != tc.want {
			t.Errorf("%s %q: %q %v, want %q", tc.dimension, tc.supplied, got, err, tc.want)
		}
		if tc.want != "" {
			readBack, err := liveResourceValue(tc.want, tc.dimension)
			if err != nil || string(readBack) != tc.want {
				t.Errorf("readback %q: %s %v", tc.want, readBack, err)
			}
		}
	}
	for _, raw := range []string{"1e999999999", "400m"} {
		if _, err := liveResourceValue(raw, "memory"); err == nil {
			t.Errorf("accepted %s bytes", raw)
		}
	}
}

func reviewedResourceDeployment() *unstructured.Unstructured {
	obj := reviewedDeployment()
	obj.SetGeneration(4)
	obj.Object["spec"] = map[string]any{"replicas": int64(3), "template": map[string]any{"spec": map[string]any{
		"initContainers": []any{map[string]any{"name": "init", "image": "init:1"}},
		"containers": []any{
			map[string]any{"name": "proxy", "image": "proxy:1", "resources": map[string]any{"limits": map[string]any{"memory": "64Mi"}}},
			map[string]any{"name": "api", "image": "checkout:1", "resources": map[string]any{
				"requests": map[string]any{"cpu": "250m", "memory": "128Mi"},
				"limits":   map[string]any{"cpu": "1", "memory": "256Mi", "example.com/device": "1"},
			}},
			map[string]any{"name": "metrics", "image": "metrics:1"},
		},
	}}}
	return obj
}

// The generic dynamic fake cannot strategic-merge into Unstructured. Use the
// native Deployment schema and require the actual request's UID/RV preconditions.
func resourceActionRouter(t *testing.T, obj *unstructured.Unstructured, admission func(*unstructured.Unstructured)) (http.Handler, *dynamicfake.FakeDynamicClient) {
	t.Helper()
	router, dyn := reviewedRestartRouter(t, obj)
	dyn.PrependReactor("patch", "deployments", func(a clienttesting.Action) (bool, runtime.Object, error) {
		p := a.(clienttesting.PatchAction)
		if p.GetPatchType() != types.StrategicMergePatchType {
			t.Fatalf("unsafe patch type: %s", p.GetPatchType())
		}
		current, err := dyn.Tracker().Get(a.GetResource(), a.GetNamespace(), p.GetName())
		if err != nil {
			return true, nil, err
		}
		live := current.(*unstructured.Unstructured)
		var guard struct {
			Metadata struct{ UID, ResourceVersion string }
		}
		if err := json.Unmarshal(p.GetPatch(), &guard); err != nil {
			return true, nil, err
		}
		if guard.Metadata.UID != string(live.GetUID()) || guard.Metadata.ResourceVersion != live.GetResourceVersion() {
			t.Fatal("unbound patch", string(p.GetPatch()))
		}
		raw, err := json.Marshal(live)
		if err != nil {
			return true, nil, err
		}
		data, err := strategicpatch.StrategicMergePatch(raw, p.GetPatch(), appsv1.Deployment{})
		if err != nil {
			return true, nil, err
		}
		var stored unstructured.Unstructured
		if err := json.Unmarshal(data, &stored); err != nil {
			return true, nil, err
		}
		stored.SetResourceVersion("8")
		stored.SetGeneration(live.GetGeneration() + 1)
		if admission != nil {
			admission(&stored)
		}
		if err := dyn.Tracker().Update(a.GetResource(), &stored, a.GetNamespace()); err != nil {
			return true, nil, err
		}
		return true, &stored, nil
	})
	return router, dyn
}

func TestReviewedContainerResourcesRoute(t *testing.T) {
	for _, tc := range []struct {
		name, dimension     string
		args, before, after map[string]any
	}{
		{"CPU pair", "cpu", map[string]any{"request": ".5", "limit": "2"}, map[string]any{"request": "250m", "limit": "1"}, map[string]any{"request": "500m", "limit": "2"}},
		{"memory limit only", "memory", map[string]any{"limit": "1Gi"}, map[string]any{"request": "128Mi", "limit": "256Mi"}, map[string]any{"request": "128Mi", "limit": "1Gi"}},
		{"first request", "cpu", map[string]any{"request": "250m", "container": "metrics"}, map[string]any{"request": nil, "limit": nil}, map[string]any{"request": "250m", "limit": nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := reviewedResourceDeployment()
			router, dyn := resourceActionRouter(t, obj, nil)
			in := map[string]any{"action": "setContainerResources", "resource": tc.dimension, "container": "api"}
			maps.Copy(in, tc.args)
			code, v := requestWorkloadAction(t, router, "Deployment", "preview", in)
			if code != 200 || !reflect.DeepEqual(v["before"], tc.before) || !reflect.DeepEqual(v["after"], tc.after) || v["container"] != in["container"] || v["resource"] != tc.dimension || v["field"] != fmt.Sprintf("spec.template.spec.containers[name=%q].resources", in["container"]) || writes(dyn) != 0 {
				t.Fatal(code, v, dyn.Actions())
			}
			in["uid"], in["reviewToken"], in["expectedResourceVersion"] = v["uid"], v["reviewToken"], v["resourceVersion"]
			code, receipt := requestWorkloadAction(t, router, "Deployment", "execute", in)
			if code != 200 || receipt["generation"] != float64(5) || receipt["resourceVersion"] != "8" || !reflect.DeepEqual(receipt["after"], tc.after) || writes(dyn) != 1 {
				t.Fatal(code, receipt, dyn.Actions())
			}
			patch := dyn.Actions()[len(dyn.Actions())-1].(clienttesting.PatchAction).GetPatch()
			if strings.Contains(string(patch), "proxy") || strings.Contains(string(patch), "image") || (in["request"] == nil && strings.Contains(string(patch), "\"requests\"")) || (in["limit"] == nil && strings.Contains(string(patch), "\"limits\"")) {
				t.Fatal("extra field in patch", string(patch))
			}
			code, replay := requestWorkloadAction(t, router, "Deployment", "execute", in)
			if code != 409 || replay["error_code"] != "workload_action_unchanged" || writes(dyn) != 1 {
				t.Fatal(code, replay, dyn.Actions())
			}
		})
	}
}

func TestReviewedContainerResourceRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		input  map[string]any
		mutate func(*unstructured.Unstructured)
		code   int
		reason string
	}{
		{"missing container", map[string]any{"container": "absent"}, nil, 409, "resource_container_missing"},
		{"init container", map[string]any{"container": "init"}, nil, 409, "resource_container_missing"},
		{"missing explicit request", map[string]any{"container": "metrics"}, nil, 409, "resource_request_required"},
		{"existing limit without request", map[string]any{"container": "proxy"}, nil, 409, "resource_request_required"},
		{"below retained request", map[string]any{"limit": "64Mi"}, nil, 409, "resource_request_exceeds_limit"},
		{"equivalent spelling", map[string]any{"limit": "0.25Gi"}, nil, 409, "workload_action_unchanged"},
		{"pod budget", nil, func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, "2Gi", "spec", "template", "spec", "resources", "requests", "memory")
		}, 409, "resource_pod_budget"},
		{"paused", nil, func(o *unstructured.Unstructured) { _ = unstructured.SetNestedField(o.Object, true, "spec", "paused") }, 409, errCodeWorkloadPaused},
		{"null limit", map[string]any{"limit": nil, "request": "256Mi"}, nil, 400, ""},
		{"null container", map[string]any{"container": nil}, nil, 400, ""},
		{"bad resource", map[string]any{"resource": "gpu"}, nil, 400, ""},
		{"field smuggled into scale", map[string]any{"action": "scale", "replicas": int64(2)}, nil, 400, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := reviewedResourceDeployment()
			if tc.mutate != nil {
				tc.mutate(obj)
			}
			router, dyn := resourceActionRouter(t, obj, nil)
			in := map[string]any{"action": "setContainerResources", "container": "api", "resource": "memory", "limit": "512Mi"}
			maps.Copy(in, tc.input)
			code, v := requestWorkloadAction(t, router, "Deployment", "preview", in)
			if code != tc.code || (tc.reason != "" && v["error_code"] != tc.reason) || writes(dyn) != 0 {
				t.Fatal(code, v, dyn.Actions())
			}
		})
	}
}

func TestReviewedResourceBindingAndAdmission(t *testing.T) {
	obj := reviewedResourceDeployment()
	router, dyn := resourceActionRouter(t, obj, func(stored *unstructured.Unstructured) {
		change := containerResourceChange{Container: "proxy"}
		container, err := change.container(stored)
		if err != nil {
			t.Fatal(err)
		}
		container["image"] = "rewritten:2"
	})
	in := map[string]any{"action": "setContainerResources", "container": "api", "resource": "memory", "request": "128Mi", "limit": "512Mi"}
	code, preview := requestWorkloadAction(t, router, "Deployment", "preview", in)
	if code != 200 {
		t.Fatal(preview)
	}
	in["uid"], in["reviewToken"] = preview["uid"], preview["reviewToken"]
	delete(in, "request")
	code, v := requestWorkloadAction(t, router, "Deployment", "execute", in)
	if code != 409 || v["error_code"] != errCodeWorkloadChanged || writes(dyn) != 0 {
		t.Fatal(code, v)
	}
	in["request"] = "128Mi"
	code, v = requestWorkloadAction(t, router, "Deployment", "execute", in)
	if code != 409 || v["error_code"] != "workload_action_not_stored" || writes(dyn) != 1 {
		t.Fatal(code, v, dyn.Actions())
	}
}
