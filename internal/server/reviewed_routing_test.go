package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clienttesting "k8s.io/client-go/testing"
)

func routingService() *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Service",
		"metadata": map[string]any{"namespace": "prod", "name": "web", "uid": "svc-uid", "resourceVersion": "7"},
		"spec": map[string]any{
			"selector": map[string]any{"app": "api"},
			"ports":    []any{map[string]any{"name": "http", "protocol": "TCP", "port": int64(80), "targetPort": "http"}},
		},
	}}
}

func routingPod(name, track string, ready bool) *unstructured.Unstructured {
	condition := "False"
	if ready {
		condition = "True"
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "Pod",
		"metadata": map[string]any{"namespace": "prod", "name": name, "uid": name + "-uid", "labels": map[string]any{"app": "api", "track": track}},
		"spec":     map[string]any{"containers": []any{map[string]any{"name": "server", "ports": []any{map[string]any{"name": "http", "containerPort": int64(8080), "protocol": "TCP"}}}}},
		"status":   map[string]any{"phase": "Running", "podIP": "10.0.0.10", "conditions": []any{map[string]any{"type": "Ready", "status": condition}}},
	}}
}

func routingSlice(name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "discovery.k8s.io/v1", "kind": "EndpointSlice",
		"metadata": map[string]any{"name": name, "namespace": "prod", "labels": map[string]any{endpointSliceServiceLabel: "web"}},
		"endpoints": []any{
			map[string]any{"targetRef": map[string]any{"kind": "Pod", "namespace": "prod", "name": "stable", "uid": "stable-uid"}},
			map[string]any{"addresses": []any{"10.0.0.12"}, "conditions": map[string]any{"ready": false}},
		},
	}}
}

func requestServiceAction(t *testing.T, router http.Handler, route string, q map[string]any) (int, map[string]any) {
	t.Helper()
	body, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/api/services/prod/web/actions/"+route, strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(w.Code, w.Body)
	}
	return w.Code, out
}

func TestReviewedRoutingServiceChange(t *testing.T) {
	stable := routingPod("stable", "stable", true)
	router, dyn := reviewedRestartRouter(t, routingService(), stable, routingPod("canary", "canary", true), routingPod("unready", "stable", false), routingSlice("v4"), routingSlice("v6"))
	body := map[string]any{"action": "setServiceSelectorLabel", "labelKey": "track", "labelValue": "stable"}
	code, review := requestServiceAction(t, router, "preview", body)
	if code != 200 || review["field"] != `spec.selector["track"]` || review["before"] != nil || review["after"] != "stable" || writes(dyn) != 0 {
		t.Fatal(code, review)
	}
	impact := review["routingImpact"].(map[string]any)
	for k, n := range map[string]float64{"publishedReadyPods": 1, "currentReadyPods": 2, "proposedReadyPods": 1, "addedReadyPods": 0, "removedReadyPods": 1} {
		if impact[k].(map[string]any)["count"] != n {
			t.Fatal(impact)
		}
	}
	if impact["publishedOtherEndpointEntries"] != float64(2) || impact["namedTargetPorts"].([]any)[0] != "http" {
		t.Fatal(impact)
	}
	body["uid"] = review["uid"]
	body["reviewToken"] = review["reviewToken"]
	body["expectedResourceVersion"] = review["resourceVersion"]
	pods := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	if err := dyn.Tracker().Update(pods, routingPod("stable", "stable", false), "prod"); err != nil {
		t.Fatal(err)
	}
	code, out := requestServiceAction(t, router, "execute", body)
	if code != 409 || out["error_code"] != "routing_no_ready_pods" || writes(dyn) != 0 {
		t.Fatal(code, out, dyn.Actions())
	}
	replacement := stable.DeepCopy()
	replacement.SetUID("recreated")
	if err := dyn.Tracker().Update(pods, replacement, "prod"); err != nil {
		t.Fatal(err)
	}
	code, out = requestServiceAction(t, router, "execute", body)
	if code != 409 || out["error_code"] != "routing_review_changed" || writes(dyn) != 0 {
		t.Fatal(code, out)
	}
	// Incidental Pod status updates preserve the original membership.
	stable.SetResourceVersion("29")
	if err := dyn.Tracker().Update(pods, stable, "prod"); err != nil {
		t.Fatal(err)
	}
	code, out = requestServiceAction(t, router, "execute", body)
	if code != 200 || writes(dyn) != 1 {
		t.Fatal(code, out, dyn.Actions())
	}
	svc, err := dyn.Tracker().Get(schema.GroupVersionResource{Version: "v1", Resource: "services"}, "prod", "web")
	if err != nil {
		t.Fatal(err)
	}
	sel, _, _ := unstructured.NestedStringMap(svc.(*unstructured.Unstructured).Object, "spec", "selector")
	if len(sel) != 2 || sel["app"] != "api" || sel["track"] != "stable" {
		t.Fatal(sel)
	}
}

func TestReviewedRoutingReadGuards(t *testing.T) {
	for _, tc := range []struct {
		name   string
		kind   string
		code   int
		reason string
		modify func(*unstructured.Unstructured)
		list   func(clienttesting.Action) (bool, runtime.Object, error)
	}{
		{name: "forbidden", kind: "pods", code: 403, reason: "routing_evidence_denied", list: func(clienttesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "pods"}, "", errors.New("denied"))
		}},
		{name: "incomplete", kind: "endpointslices", code: 409, reason: "routing_evidence_incomplete", list: func(clienttesting.Action) (bool, runtime.Object, error) {
			l := &unstructured.UnstructuredList{}
			l.SetAPIVersion("discovery.k8s.io/v1")
			l.SetKind("EndpointSliceList")
			l.SetContinue("next")
			return true, l, nil
		}},
		{name: "selectorless", code: 409, reason: "routing_action_not_applicable", modify: func(s *unstructured.Unstructured) { unstructured.RemoveNestedField(s.Object, "spec", "selector") }},
		{name: "ExternalName", code: 409, reason: "routing_action_not_applicable", modify: func(s *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(s.Object, "ExternalName", "spec", "type")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := routingService()
			if tc.modify != nil {
				tc.modify(svc)
			}
			router, dyn := reviewedRestartRouter(t, svc, routingPod("stable", "stable", true), routingSlice("v4"))
			if tc.list != nil {
				dyn.PrependReactor("list", tc.kind, tc.list)
			}
			code, out := requestServiceAction(t, router, "preview", map[string]any{"action": "setServiceSelectorLabel", "labelKey": "track", "labelValue": "stable"})
			if code != tc.code || out["error_code"] != tc.reason || writes(dyn) != 0 {
				t.Fatal(code, out, dyn.Actions())
			}
		})
	}
}

func TestReviewedRoutingDeploymentLabel(t *testing.T) {
	obj := reviewedDeployment()
	obj.Object["spec"] = map[string]any{"selector": map[string]any{"matchExpressions": []any{map[string]any{"key": "app", "operator": "In", "values": []any{"api"}}}}, "template": map[string]any{"metadata": map[string]any{"labels": map[string]any{"app": "api"}}}}
	router, dyn := reviewedRestartRouter(t, obj, routingService())
	for _, key := range []string{"pod-template-hash", "bad key", "app"} {
		code, _ := requestWorkloadAction(t, router, "Deployment", "preview", map[string]any{"action": "setPodTemplateLabel", "labelKey": key, "labelValue": "wrong"})
		if code < 400 || writes(dyn) != 0 {
			t.Fatal(key, code)
		}
	}
	q := map[string]any{"action": "setPodTemplateLabel", "labelKey": "routing.example.com/track", "labelValue": ""}
	code, view := requestWorkloadAction(t, router, "Deployment", "preview", q)
	if code != 200 || view["field"] != `spec.template.metadata.labels["routing.example.com/track"]` || view["before"] != nil || view["after"] != "" {
		t.Fatal(code, view)
	}
	q["uid"] = view["uid"]
	q["reviewToken"] = view["reviewToken"]
	code, out := requestWorkloadAction(t, router, "Deployment", "execute", q)
	if code != 200 || writes(dyn) != 1 {
		t.Fatal(code, out)
	}
}

func TestReviewedRoutingServicePortAndReadinessGuards(t *testing.T) {
	for _, tc := range []struct {
		name        string
		ready       bool
		missingPort bool
		code        string
	}{
		{"no ready backends", false, false, "routing_no_ready_pods"},
		{"declares a port but wrong protocol", true, true, "routing_action_not_applicable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pod := routingPod("stable", "stable", tc.ready)
			if tc.missingPort {
				_ = unstructured.SetNestedSlice(pod.Object, []any{map[string]any{"name": "api", "ports": []any{map[string]any{"name": "http", "protocol": "UDP", "containerPort": int64(8080)}}}}, "spec", "containers")
			}
			svc := routingService()
			_ = unstructured.SetNestedField(svc.Object, true, "spec", "publishNotReadyAddresses")
			router, dyn := reviewedRestartRouter(t, svc, pod, routingSlice("v4"))
			body := map[string]any{"action": "setServiceSelectorLabel", "labelKey": "track", "labelValue": "stable"}
			code, resp := requestServiceAction(t, router, "preview", body)
			if code != 409 || resp["error_code"] != tc.code || writes(dyn) != 0 {
				t.Fatal(code, resp)
			}
		})
	}
}

func TestReviewedRoutingProtectsServiceDuringDeploymentRollout(t *testing.T) {
	obj := reviewedDeployment()
	obj.Object["spec"] = map[string]any{
		"selector": map[string]any{"matchLabels": map[string]any{"app": "api"}},
		"template": map[string]any{"metadata": map[string]any{"labels": map[string]any{"app": "api", "track": "stable"}}},
	}
	svc := routingService()
	_ = unstructured.SetNestedStringMap(svc.Object, map[string]string{"app": "api", "track": "stable"}, "spec", "selector")
	objects := []runtime.Object{obj, svc}
	for i := 11; i >= 1; i-- {
		extra := svc.DeepCopy()
		extra.SetName(fmt.Sprintf("web-%02d", i))
		objects = append(objects, extra)
	}
	router, dyn := reviewedRestartRouter(t, objects...)
	q := map[string]any{"action": "setPodTemplateLabel", "labelKey": "track", "labelValue": "canary"}
	code, out := requestWorkloadAction(t, router, "Deployment", "preview", q)
	if code != 409 || out["error_code"] != "routing_deployment_would_drop_services" || !strings.Contains(out["error"].(string), "web") || writes(dyn) != 0 {
		t.Fatal(code, out)
	}
	affected := out["affectedServices"].(map[string]any)
	names := affected["names"].([]any)
	if affected["count"] != float64(12) || len(names) != 10 || names[0] != "web" || names[9] != "web-09" {
		t.Fatal(affected)
	}
	obj.Object["spec"].(map[string]any)["paused"] = true
	if err := dyn.Tracker().Update(deploymentsGVR, obj, "prod"); err != nil {
		t.Fatal(err)
	}
	code, out = requestWorkloadAction(t, router, "Deployment", "preview", q)
	if code != 409 || !strings.Contains(out["error"].(string), "resume") || writes(dyn) != 0 {
		t.Fatal(code, out)
	}
}

func TestReviewedRoutingTargetIsCheckedBeforeEvidence(t *testing.T) {
	router, dyn := reviewedRestartRouter(t, routingService(), routingPod("stable", "stable", true), routingSlice("v4"))
	q := map[string]any{"action": "setServiceSelectorLabel", "labelKey": "track", "labelValue": "stable"}
	code, review := requestServiceAction(t, router, "preview", q)
	if code != 200 {
		t.Fatal(code, review)
	}
	q["uid"] = review["uid"]
	q["reviewToken"] = review["reviewToken"]
	q["expectedResourceVersion"] = "changed"
	dyn.ClearActions()
	code, out := requestServiceAction(t, router, "execute", q)
	if code != 409 || out["error_code"] != errCodeWorkloadChanged || len(dyn.Actions()) != 1 || dyn.Actions()[0].GetVerb() != "get" {
		t.Fatal(code, out, dyn.Actions())
	}
}

func TestReviewedRoutingPublicationDriftAndSelectorAdmission(t *testing.T) {
	for _, tc := range []string{"published EndpointSlice changed", "non-proposed current Pod replaced", "admission rewrites other selectors"} {
		t.Run(tc, func(t *testing.T) {
			svc := routingService()
			rs := routingSlice("v4")
			canary := routingPod("canary", "canary", true)
			router, dyn := reviewedRestartRouter(t, svc, routingPod("stable", "stable", true), canary, rs)
			q := map[string]any{"action": "setServiceSelectorLabel", "labelKey": "track", "labelValue": "stable"}
			code, review := requestServiceAction(t, router, "preview", q)
			if code != 200 {
				t.Fatal(code, review)
			}
			q["uid"] = review["uid"]
			q["reviewToken"] = review["reviewToken"]
			reason := "routing_review_changed"
			switch tc {
			case "published EndpointSlice changed":
				rs.Object["endpoints"] = []any{}
				if err := dyn.Tracker().Update(testEndpointSliceGVR, rs, "prod"); err != nil {
					t.Fatal(err)
				}
			case "non-proposed current Pod replaced":
				canary.SetUID("replacement")
				if err := dyn.Tracker().Update(schema.GroupVersionResource{Version: "v1", Resource: "pods"}, canary, "prod"); err != nil {
					t.Fatal(err)
				}
			default:
				reason = "workload_action_not_stored"
				dyn.PrependReactor("patch", "services", func(clienttesting.Action) (bool, runtime.Object, error) {
					stored := svc.DeepCopy()
					_ = unstructured.SetNestedStringMap(stored.Object, map[string]string{"track": "stable", "app": "another"}, "spec", "selector")
					return true, stored, nil
				})
			}
			code, out := requestServiceAction(t, router, "execute", q)
			if code != 409 || out["error_code"] != reason {
				t.Fatal(code, out)
			}
		})
	}
}
