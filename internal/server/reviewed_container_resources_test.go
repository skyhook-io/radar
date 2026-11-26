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
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	clienttesting "k8s.io/client-go/testing"
)

func TestReviewedResourceQuantities(t *testing.T) {
	for _, tc := range []struct{ dimension, supplied, want string }{
		{"cpu", "1000m", "1"}, {"cpu", ".5", "500m"}, {"cpu", "1000", "1000"},
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
			if code != 409 || replay["error_code"] != errCodeWorkloadChanged || writes(dyn) != 1 {
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

func TestReviewedResourceSchedulingEffects(t *testing.T) {
	for _, tc := range []struct {
		name, strategy string
		replicas       int64
		request        string
		want           []string
	}{
		{"raise", "RollingUpdate", 3, "500m", []string{"750m more CPU", "lowers measured utilization"}},
		{"lower", "RollingUpdate", 4, "100m", []string{"600m less CPU", "raises measured utilization"}},
		{"scaled down", "", 0, "500m", []string{"zero replicas", "Future Pods"}},
		{"recreate", "Recreate", 1, "500m", []string{"service interruption"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := reviewedResourceDeployment()
			_ = unstructured.SetNestedField(obj.Object, tc.replicas, "spec", "replicas")
			_ = unstructured.SetNestedField(obj.Object, tc.strategy, "spec", "strategy", "type")
			router, dyn := resourceActionRouter(t, obj, nil)
			code, view := requestWorkloadAction(t, router, "Deployment", "preview", map[string]any{"action": "setContainerResources", "container": "api", "resource": "cpu", "request": tc.request})
			if code != 200 || writes(dyn) != 0 {
				t.Fatal(code, view)
			}
			for _, needle := range tc.want {
				if !strings.Contains(view["effect"].(string), needle) {
					t.Errorf("%q missing from %s", needle, view["effect"])
				}
			}
		})
	}
}

func TestReviewedResourceAdmissionReceipt(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		status     int
		admit      func(*unstructured.Unstructured, map[string]any)
	}{
		{"quantity alias", "", 200, func(_ *unstructured.Unstructured, c map[string]any) {
			_ = unstructured.SetNestedField(c, "536870912", "resources", "limits", "memory")
		}},
		{"different admitted limit", "workload_action_not_stored", 409, func(_ *unstructured.Unstructured, c map[string]any) {
			_ = unstructured.SetNestedField(c, "384Mi", "resources", "limits", "memory")
		}},
		{"version missing", "workload_action_unconfirmed", 502, func(o *unstructured.Unstructured, _ map[string]any) { o.SetResourceVersion("") }},
		{"version unchanged", "workload_action_unconfirmed", 502, func(o *unstructured.Unstructured, _ map[string]any) { o.SetResourceVersion("7") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := reviewedResourceDeployment()
			target := containerResourceChange{Container: "api", Resource: "memory"}
			router, dyn := resourceActionRouter(t, obj, func(stored *unstructured.Unstructured) {
				c, err := target.container(stored)
				if err != nil {
					t.Fatal(err)
				}
				tc.admit(stored, c)
			})
			input := map[string]any{"action": "setContainerResources", "container": "api", "resource": "memory", "limit": "512Mi"}
			code, v := requestWorkloadAction(t, router, "Deployment", "preview", input)
			if code != 200 {
				t.Fatal(code, v)
			}
			input["uid"], input["reviewToken"], input["expectedResourceVersion"] = v["uid"], v["reviewToken"], v["resourceVersion"]
			code, v = requestWorkloadAction(t, router, "Deployment", "execute", input)
			if code != tc.status || tc.code != "" && v["error_code"] != tc.code || writes(dyn) != 1 {
				t.Fatal(code, v, dyn.Actions())
			}
			if tc.name == "different admitted limit" && !strings.Contains(v["error"].(string), "request=128Mi, limit=384Mi") {
				t.Fatal(v)
			}
			if code == 200 {
				if !strings.Contains(v["effect"].(string), "stored the reviewed Deployment template") {
					t.Fatal(v)
				}
				stored, err := dyn.Tracker().Get(schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, "prod", "web")
				if err != nil {
					t.Fatal(err)
				}
				c, err := target.container(obj)
				if err != nil {
					t.Fatal(err)
				}
				_ = unstructured.SetNestedField(c, "536870912", "resources", "limits", "memory")
				if !reflect.DeepEqual(obj.Object["spec"], stored.(*unstructured.Unstructured).Object["spec"]) {
					t.Fatal("stored spec differs from exactly one intended edit")
				}
			}
		})
	}
}

func TestReviewedResourceDriftPrecedesPlanning(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*unstructured.Unstructured)
		strict bool
	}{
		{"replacement with no container", func(o *unstructured.Unstructured) {
			o.SetUID("replacement")
			_ = unstructured.SetNestedSlice(o.Object, []any{}, "spec", "template", "spec", "containers")
		}, false},
		{"paused after review", func(o *unstructured.Unstructured) { _ = unstructured.SetNestedField(o.Object, true, "spec", "paused") }, false},
		{"Pod budget added", func(o *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(o.Object, "2Gi", "spec", "template", "spec", "resources", "limits", "memory")
		}, false},
		{"retained request drifted and limit already applied", func(o *unstructured.Unstructured) {
			target := containerResourceChange{Container: "api"}
			c, _ := target.container(o)
			_ = unstructured.SetNestedField(c, "256Mi", "resources", "requests", "memory")
			_ = unstructured.SetNestedField(c, "512Mi", "resources", "limits", "memory")
		}, false},
		{"only status version changed", func(o *unstructured.Unstructured) { o.SetResourceVersion("8") }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := reviewedResourceDeployment()
			router, dyn := resourceActionRouter(t, obj, nil)
			in := map[string]any{"action": "setContainerResources", "container": "api", "resource": "memory", "limit": "512Mi"}
			code, p := requestWorkloadAction(t, router, "Deployment", "preview", in)
			if code != 200 {
				t.Fatal(code, p)
			}
			in["uid"], in["reviewToken"] = p["uid"], p["reviewToken"]
			if tc.strict {
				in["expectedResourceVersion"] = p["resourceVersion"]
			}
			changed := obj.DeepCopy()
			tc.mutate(changed)
			if err := dyn.Tracker().Update(schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}, changed, "prod"); err != nil {
				t.Fatal(err)
			}
			code, v := requestWorkloadAction(t, router, "Deployment", "execute", in)
			if code != 409 || v["error_code"] != errCodeWorkloadChanged || writes(dyn) != 0 {
				t.Fatal(code, v, dyn.Actions())
			}
		})
	}
}

func TestReviewedResourceInheritedRequest(t *testing.T) {
	obj := reviewedResourceDeployment()
	_ = unstructured.SetNestedField(obj.Object, int64(1), "spec", "replicas")
	_ = unstructured.SetNestedField(obj.Object, "Recreate", "spec", "strategy", "type")
	cn := strings.Repeat("p", 63)
	target := containerResourceChange{Container: "proxy"}
	c, err := target.container(obj)
	if err != nil {
		t.Fatal(err)
	}
	c["name"] = cn
	_ = unstructured.SetNestedField(c, "9223372036854775807", "resources", "limits", "memory")
	router, dyn := resourceActionRouter(t, obj, nil)
	in := map[string]any{"action": "setContainerResources", "container": cn, "resource": "memory", "request": "32Mi", "limit": "2Gi"}
	code, p := requestWorkloadAction(t, router, "Deployment", "preview", in)
	if code != 200 || writes(dyn) != 0 {
		t.Fatal(code, p)
	}
	text := p["effect"].(string)
	if len(text) > 1024 || !strings.Contains(text, "inherited") || !strings.Contains(text, "raises measured utilization") || !strings.Contains(text, "Guaranteed QoS") {
		t.Fatal(text)
	}
}
