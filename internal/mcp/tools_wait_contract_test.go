package mcp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	clienttesting "k8s.io/client-go/testing"
)

func waitTestObject(kind, apiVersion, name string, status map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": map[string]any{"name": name, "namespace": "shop", "uid": "uid-" + name, "generation": int64(1)}, "status": status}}
}
func waitTestPod(name, ready string) *unstructured.Unstructured {
	return waitTestObject("Pod", "v1", name, map[string]any{"phase": "Running", "conditions": []any{map[string]any{"type": "Ready", "status": ready}}})
}
func waitFirstObject(t *testing.T, got map[string]any) map[string]any {
	t.Helper()
	return got["objects"].([]any)[0].(map[string]any)
}

func TestWaitConditionGrammar(t *testing.T) {
	argo := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}
	kruise := schema.GroupVersionResource{Group: "rollouts.kruise.io", Version: "v1alpha1", Resource: "rollouts"}
	for _, tc := range []struct {
		gvr       schema.GroupVersionResource
		condition string
		invalid   bool
	}{
		{deploymentsGVR, "rollout", false}, {argo, "rollout", false}, {kruise, "rollout", true}, {podsGVR, "rollout", true},
		{jobsGVR, "complete", false}, {podsGVR, "complete", true}, {podsGVR, "ready", false}, {configMapsGVR, "ready", false},
		{configMapsGVR, "Available", false}, {configMapsGVR, "Available=False", false}, {configMapsGVR, "condition=Available", false}, {configMapsGVR, "condition=Available=Unknown", false},
		{podsGVR, "=True", true}, {podsGVR, "Ready=", true}, {podsGVR, "Ready=True=Other", true}, {podsGVR, "bad condition", true},
	} {
		_, err := waitCheckFor(tc.gvr, tc.condition)
		if (err != nil) != tc.invalid {
			t.Errorf("%v %s: %v", tc.gvr, tc.condition, err)
		}
	}
	if defaultWaitCondition(kruise) != "" {
		t.Fatal("foreign Rollout got a built-in default")
	}
}

func TestWaitFreshnessAliasesAndTermination(t *testing.T) {
	for _, kind := range []string{"Pod", "Job", "Widget"} {
		for _, source := range []string{"missing", "zero", "staleCondition", "fresh"} {
			t.Run(kind+source, func(t *testing.T) {
				typ := "Ready"
				gvr := podsGVR
				condition := "ready"
				api := "v1"
				if kind == "Job" {
					typ = "Complete"
					condition = "complete"
					gvr = jobsGVR
					api = "batch/v1"
				}
				if kind == "Widget" {
					condition = "Ready"
					gvr = configMapsGVR
				}
				c := map[string]any{"type": typ, "status": "True"}
				obj := waitTestObject(kind, api, "test", map[string]any{"conditions": []any{c}})
				switch source {
				case "zero":
					obj.Object["status"].(map[string]any)["observedGeneration"] = int64(0)
				case "staleCondition":
					c["observedGeneration"] = int64(0)
					obj.Object["status"].(map[string]any)["observedGeneration"] = int64(1)
				case "fresh":
					c["observedGeneration"] = int64(1)
				}
				check, err := waitCheckFor(gvr, condition)
				if err != nil {
					t.Fatal(err)
				}
				s, err := evaluateWaitObject(t.Context(), waitDynamic(), gvr, obj, condition, check, nil, waitObjectState{})
				if err != nil || s.Met != (source == "missing" || source == "fresh") {
					t.Fatalf("met=%v err=%v", s.Met, err)
				}
				now := metav1.Now()
				obj.SetDeletionTimestamp(&now)
				s, err = evaluateWaitObject(t.Context(), waitDynamic(), gvr, obj, condition, check, nil, waitObjectState{})
				if err != nil || s.Met || s.Observed != "terminating" {
					t.Fatalf("terminating: %+v %v", s, err)
				}
			})
		}
	}
}

func TestWaitArgoPredicate(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}
	for _, tc := range []struct {
		observed, phase, state string
		met                    bool
	}{
		{"0", "Healthy", "", false}, {"1", "Healthy", "", true}, {"old-hash", "Healthy", "", true}, {"1", "Paused", "", false}, {"1", "Degraded", "blocked", false},
	} {
		obj := waitTestObject("Rollout", "argoproj.io/v1alpha1", "api", map[string]any{"observedGeneration": tc.observed, "phase": tc.phase, "message": "controller detail"})
		s, err := evaluateWaitObject(t.Context(), waitDynamic(), gvr, obj, "rollout", nil, nil, waitObjectState{})
		if err != nil || s.Met != tc.met || s.state != tc.state {
			t.Fatalf("%+v: %+v %v", tc, s, err)
		}
		if tc.phase == "Paused" && (!strings.HasPrefix(s.Observed, "paused:") || !strings.Contains(s.next, "duration resumes")) {
			t.Fatalf("pause: %+v", s)
		}
	}
	obj := waitTestObject("Rollout", "argoproj.io/v1alpha1", "api", map[string]any{"observedGeneration": "1", "phase": "Healthy", "workloadObservedGeneration": "1"})
	obj.Object["spec"] = map[string]any{"workloadRef": map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "source"}}
	source := deploymentObj("source", 2, 2, 1, 1, 1, 1)
	s, err := evaluateWaitObject(t.Context(), waitDynamic(source), gvr, obj, "rollout", nil, nil, waitObjectState{})
	if err != nil || s.Met {
		t.Fatalf("stale template source accepted: %+v %v", s, err)
	}
	_, err = evaluateWaitObject(t.Context(), waitDynamic(), gvr, obj, "rollout", nil, nil, waitObjectState{})
	if err == nil {
		t.Fatal("unreadable workloadRef source accepted")
	}
}

func TestWaitNamedUIDAndNormalization(t *testing.T) {
	fastWaitPolling(t)
	obj := waitTestPod("web", "True")
	for _, condition := range []string{"ready", "deleted"} {
		res, _, err := waitFor(t.Context(), waitDynamic(obj), waitForInput{Kind: "pod", Namespace: "shop", Name: "pod/web", UID: "previous", Condition: condition})
		if err != nil {
			t.Fatal(err)
		}
		got := decodeToolResult(t, res)
		if condition == "deleted" {
			if got["state"] != "met" {
				t.Fatal(got)
			}
		} else if got["state"] != "waiting" || !strings.Contains(waitFirstObject(t, got)["observed"].(string), "replaced by uid") || !strings.Contains(got["next"].(string), "drop uid") {
			t.Fatal(got)
		}
	}
	res, _, err := waitFor(t.Context(), waitDynamic(obj), waitForInput{Kind: "pod", Namespace: "shop", Name: "web"})
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeToolResult(t, res); waitFirstObject(t, got)["uid"] != "uid-web" {
		t.Fatal(got)
	}
	for _, in := range []waitForInput{
		{Kind: "pod", Namespace: "shop", All: true, UID: "u"},
		{Kind: "pod", Namespace: "shop", Name: "service/web"},
		{Kind: "pod", Namespace: "shop", Name: "web", TimeoutSeconds: -1},
		{Kind: "secret", Namespace: "shop", Name: "x", Condition: "Ready"},
		{Kind: "secret", Namespace: "shop", Name: "x", Condition: "jsonpath={.status.phase}=x"},
	} {
		if _, _, err := waitFor(t.Context(), waitDynamic(), in); err == nil {
			t.Fatalf("accepted %+v", in)
		}
	}
	res, _, err = waitFor(t.Context(), waitDynamic(), waitForInput{Kind: "secret", Namespace: "shop", Name: "x", Condition: "delete"})
	if err != nil || decodeToolResult(t, res)["state"] != "met" {
		t.Fatalf("delete Secret: %v", err)
	}
}

func TestWaitNothingMatchesAndClamp(t *testing.T) {
	fastWaitPolling(t)
	clock := waitNow
	now := time.Now()
	waitNow = func() time.Time { now = now.Add(5 * time.Second); return now }
	t.Cleanup(func() { waitNow = clock })
	res, _, err := waitFor(t.Context(), waitDynamic(), waitForInput{Kind: "pod", Namespace: "shop", Name: "ghost", TimeoutSeconds: 999})
	if err != nil {
		t.Fatal(err)
	}
	got := decodeToolResult(t, res)
	if got["state"] != "waiting" || got["observed"] != "nothing matches" || !strings.Contains(got["next"].(string), "list_resources") || got["note"] != "one call waits at most 45s; call again to keep waiting" || got["waitedSeconds"] != float64(0) {
		t.Fatal(got)
	}
	ns := waitTestObject("Namespace", "v1", "test", nil)
	res, _, err = waitFor(t.Context(), waitDynamic(ns), waitForInput{Kind: "namespace", Namespace: "ignored", Name: "test", Condition: "deleted"})
	if err != nil {
		t.Fatal(err)
	}
	if got := decodeToolResult(t, res); got["namespace"] != nil {
		t.Fatalf("cluster namespace echoed: %v", got)
	}
}

func TestWaitLiveMembership(t *testing.T) {
	for _, shrinks := range []bool{false, true} {
		t.Run(fmt.Sprint(shrinks), func(t *testing.T) {
			roomForPolls(t)
			old := waitTestPod("old", "True")
			now := metav1.Now()
			old.SetDeletionTimestamp(&now)
			stable := waitTestPod("stable", "True")
			replacement := waitTestPod("replacement", "True")
			dyn := waitDynamic()
			calls := 0
			dyn.PrependReactor("list", "pods", func(clienttesting.Action) (bool, runtime.Object, error) {
				calls++
				items := []unstructured.Unstructured{*stable}
				if calls == 1 {
					items = append(items, *old)
				} else if !shrinks {
					items = append(items, *replacement)
				}
				return true, &unstructured.UnstructuredList{Items: items}, nil
			})
			res, _, err := waitFor(t.Context(), dyn, waitForInput{Kind: "pod", Namespace: "shop", All: true})
			if err != nil {
				t.Fatal(err)
			}
			got := decodeToolResult(t, res)
			want := 2
			if shrinks {
				want = 1
			}
			if got["state"] != "met" || calls != 2 || len(got["objects"].([]any)) != want {
				t.Fatal(got, calls)
			}
		})
	}
}

func TestWaitPaginationAndBounds(t *testing.T) {
	for _, emptyPage := range []bool{false, true} {
		t.Run(fmt.Sprint(emptyPage), func(t *testing.T) {
			dyn := waitDynamic()
			calls := 0
			dyn.PrependReactor("list", "pods", func(a clienttesting.Action) (bool, runtime.Object, error) {
				calls++
				opts := a.(clienttesting.ListActionImpl).GetListOptions()
				if opts.Limit != 100 {
					t.Errorf("limit %d", opts.Limit)
				}
				list := &unstructured.UnstructuredList{}
				if calls == 1 {
					list.SetContinue("next")
					if !emptyPage {
						list.Items = []unstructured.Unstructured{*waitTestPod("a", "True")}
					}
				} else {
					if opts.Continue != "next" {
						t.Errorf("missing continue: %+v", opts)
					}
					list.Items = []unstructured.Unstructured{*waitTestPod("b", "False")}
				}
				return true, list, nil
			})
			objs, err := readWaitTargets(t.Context(), dyn.Resource(podsGVR).Namespace("shop"), "", "")
			want := 2
			if emptyPage {
				want = 1
			}
			if err != nil || calls != 2 || len(objs) != want {
				t.Fatalf("%d %d %v", calls, len(objs), err)
			}
		})
	}
	for _, count := range []int{25, 201} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			fastWaitPolling(t)
			var objs []runtime.Object
			for i := 0; i < count; i++ {
				ready := "True"
				if i == count-1 {
					ready = "False"
				}
				objs = append(objs, waitTestPod(fmt.Sprintf("p%03d", i), ready))
			}
			res, _, err := waitFor(t.Context(), waitDynamic(objs...), waitForInput{Kind: "pod", Namespace: "shop", All: true})
			if count == 201 {
				if err == nil || !strings.Contains(err.Error(), "more than 200 pods match") {
					t.Fatalf("%v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := decodeToolResult(t, res)
			if len(got["objects"].([]any)) != 10 || got["total"] != float64(25) || got["metCount"] != float64(24) || waitFirstObject(t, got)["name"] != "p024" {
				t.Fatal(got)
			}
		})
	}
}

func TestWaitReadErrors(t *testing.T) {
	for _, failures := range []int{2, 3, 100} {
		t.Run(fmt.Sprint(failures), func(t *testing.T) {
			roomForPolls(t)
			dyn := waitDynamic()
			calls := 0
			dyn.PrependReactor("get", "pods", func(clienttesting.Action) (bool, runtime.Object, error) {
				calls++
				if calls <= failures {
					return true, nil, errors.New("transient")
				}
				return true, waitTestPod("web", "True"), nil
			})
			res, _, err := waitFor(t.Context(), dyn, waitForInput{Kind: "pod", Namespace: "shop", Name: "web"})
			if failures == 2 {
				if err != nil || decodeToolResult(t, res)["state"] != "met" {
					t.Fatal(err)
				}
			} else if err == nil || calls != 3 {
				t.Fatalf("calls %d err %v", calls, err)
			}
		})
	}
}

func TestWaitDeadlineIncludesHangingReads(t *testing.T) {
	fastWaitPolling(t)
	dyn := waitDynamic()
	release := make(chan struct{})
	done := make(chan struct{})
	dyn.PrependReactor("get", "pods", func(clienttesting.Action) (bool, runtime.Object, error) {
		defer close(done)
		<-release
		return true, waitTestPod("web", "True"), nil
	})
	start := time.Now()
	_, _, err := waitFor(t.Context(), dyn, waitForInput{Kind: "pod", Namespace: "shop", Name: "web"})
	close(release)
	<-done
	if err == nil || !strings.Contains(err.Error(), "no successful read") || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("%v in %v", err, time.Since(start))
	}
}

func TestWaitDeadlineIncludesPermissionLookup(t *testing.T) {
	fastWaitPolling(t)
	previous := k8s.SetTestClient(&kubernetes.Clientset{})
	t.Cleanup(func() { k8s.SetTestClient(previous) })
	var calls atomic.Int32
	release := make(chan struct{})
	done := make(chan struct{}, 1)
	stubSubjectCanI(t, func(ctx context.Context, _ kubernetes.Interface, _ string, _ []string, _, _, _, _ string) (bool, error) {
		calls.Add(1)
		<-release
		select {
		case done <- struct{}{}:
		default:
		}
		return false, nil
	})
	// Namespace discovery calls the auth package directly, so exercise the cached
	// namespace gate followed by a slow per-kind blocker SAR instead.
	ctx := withTestUserPerms(t, "slow-permission", nil, []string{"shop"})
	dyn := waitDynamic(waitTestPod("web", "False"))
	start := time.Now()
	res, _, err := waitFor(ctx, dyn, waitForInput{Kind: "pod", Namespace: "shop", Name: "web"})
	close(release)
	<-done
	if err != nil || decodeToolResult(t, res)["state"] != "waiting" || calls.Load() != 1 || time.Since(start) > 500*time.Millisecond {
		t.Fatalf("%v calls=%d elapsed=%v", err, calls.Load(), time.Since(start))
	}
}

func TestWaitOnDeleteIsInputError(t *testing.T) {
	for _, kind := range []string{"StatefulSet", "DaemonSet"} {
		resource := "statefulsets"
		if kind == "DaemonSet" {
			resource = "daemonsets"
		}
		gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: resource}
		obj := waitTestObject(kind, "apps/v1", "web", map[string]any{"observedGeneration": int64(1)})
		obj.Object["spec"] = map[string]any{"updateStrategy": map[string]any{"type": "OnDelete"}}
		_, err := evaluateWaitObject(t.Context(), waitDynamic(), gvr, obj, "rollout", rolloutStatus, nil, waitObjectState{})
		if err == nil || !strings.Contains(err.Error(), "RollingUpdate strategy") {
			t.Fatal(err)
		}
	}
}

func TestWaitLongTextAndConditionAge(t *testing.T) {
	fastWaitPolling(t)
	obj := waitTestPod("web", "False")
	c := waitCondition(obj, "Ready")
	c["reason"] = "NotReady"
	c["message"] = strings.Repeat("界", 10000)
	c["lastTransitionTime"] = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	res, _, err := waitFor(t.Context(), waitDynamic(obj), waitForInput{Kind: "pod", Namespace: "shop", Name: "web"})
	if err != nil {
		t.Fatal(err)
	}
	got := decodeToolResult(t, res)
	s := waitFirstObject(t, got)
	if len([]rune(s["observed"].(string))) > 300 || s["inStateSeconds"].(float64) < 60 || got["next"] == nil {
		t.Fatal(got)
	}
	absent := statusConditionCheck("Missing=False")
	if met, _, failure := absent(obj); met || failure != "" {
		t.Fatalf("missing condition met=%v failure=%s", met, failure)
	}
}

func TestWaitJSONPathTransitions(t *testing.T) {
	roomForPolls(t)
	dyn := waitDynamic()
	calls := 0
	dyn.PrependReactor("get", "configmaps", func(clienttesting.Action) (bool, runtime.Object, error) {
		calls++
		phase := "Progressing"
		if calls > 1 {
			phase = "Healthy"
		}
		return true, waitTestObject("ConfigMap", "v1", "app", map[string]any{"health": map[string]any{"status": phase}}), nil
	})
	res, _, err := waitFor(t.Context(), dyn, waitForInput{Kind: "configmap", Namespace: "shop", Name: "app", Condition: "jsonpath={.status.health.status}=Healthy"})
	if err != nil || decodeToolResult(t, res)["state"] != "met" || calls != 2 {
		t.Fatalf("%v calls=%d", err, calls)
	}
}

func TestWaitDeadlineIncludesNamespaceDiscovery(t *testing.T) {
	fastWaitPolling(t)
	requested := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case requested <- struct{}{}:
		default:
		}
		<-r.Context().Done()
	}))
	defer server.Close()
	client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	previous := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(previous) })
	ctx := pkgauth.ContextWithUser(t.Context(), &pkgauth.User{Username: "uncached-user"})
	getPermCache().Invalidate()
	t.Cleanup(func() { getPermCache().Invalidate() })
	dyn := waitDynamic(waitTestPod("web", "True"))
	start := time.Now()
	_, _, err = waitFor(ctx, dyn, waitForInput{Kind: "pod", Namespace: "shop", Name: "web"})
	if err == nil || time.Since(start) > 500*time.Millisecond || len(dyn.Actions()) != 0 {
		t.Fatalf("err=%v elapsed=%v actions=%v", err, time.Since(start), dyn.Actions())
	}
	select {
	case <-requested:
	default:
		t.Fatal("permission lookup was not exercised")
	}
}

func TestWaitIncompletePaginationCannotMeet(t *testing.T) {
	fastWaitPolling(t)
	dyn := waitDynamic()
	release := make(chan struct{})
	done := make(chan struct{})
	calls := 0
	dyn.PrependReactor("list", "pods", func(clienttesting.Action) (bool, runtime.Object, error) {
		calls++
		if calls == 1 {
			page := &unstructured.UnstructuredList{Items: []unstructured.Unstructured{*waitTestPod("ready", "True")}}
			page.SetContinue("more")
			return true, page, nil
		}
		<-release
		close(done)
		return true, &unstructured.UnstructuredList{}, nil
	})
	_, _, err := waitFor(t.Context(), dyn, waitForInput{Kind: "pod", Namespace: "shop", All: true})
	close(release)
	<-done
	if err == nil || !strings.Contains(err.Error(), "no successful read") {
		t.Fatalf("partial LIST evaluated: %v", err)
	}
}

func TestWaitMissingConditionAndJSONPathHints(t *testing.T) {
	fastWaitPolling(t)
	obj := waitTestObject("ConfigMap", "v1", "app", map[string]any{"phase": "Pending", "conditions": []any{map[string]any{"type": "Available", "status": "False"}}})
	for _, tc := range []struct{ condition, want string }{
		{"Ready=False", "no Ready condition on app; it has [Available]"},
		{"jsonpath={.status.health.status}=Healthy", ".status.health.status not present; status has [conditions, phase]"},
	} {
		res, _, err := waitFor(t.Context(), waitDynamic(obj), waitForInput{Kind: "configmap", Namespace: "shop", Name: "app", Condition: tc.condition})
		if err != nil {
			t.Fatal(err)
		}
		got := decodeToolResult(t, res)
		if got["state"] != "waiting" || !strings.Contains(got["next"].(string), tc.want) {
			t.Fatal(got)
		}
	}
}

func TestWaitDeploymentConditionAgeRequiresCurrentGeneration(t *testing.T) {
	c := map[string]any{"type": "Progressing", "status": "True", "lastUpdateTime": time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339)}
	for _, observed := range []int64{1, 2} {
		obj := deploymentObj("web", 2, observed, 3, 3, 1, 2, c)
		s, err := evaluateWaitObject(t.Context(), waitDynamic(), deploymentsGVR, obj, "rollout", rolloutStatus, nil, waitObjectState{})
		if err != nil {
			t.Fatal(err)
		}
		if observed == 1 {
			if s.InStateSeconds != nil {
				t.Fatalf("previous rollout's age: %+v", s)
			}
		} else if s.InStateSeconds == nil || !strings.Contains(s.next, "no progress for") {
			t.Fatalf("current age: %+v", s)
		}
	}
}

func TestWaitFinalizersAreBoundedAndUIDHinted(t *testing.T) {
	fastWaitPolling(t)
	obj := waitTestObject("ConfigMap", "v1", "app", nil)
	now := metav1.Now()
	obj.SetDeletionTimestamp(&now)
	obj.SetFinalizers([]string{"one", "two", "three", "four", "five", "MUST-NOT-APPEAR"})
	res, _, err := waitFor(t.Context(), waitDynamic(obj), waitForInput{Kind: "configmap", Namespace: "shop", Name: "app", Condition: "deleted"})
	if err != nil {
		t.Fatal(err)
	}
	got := decodeToolResult(t, res)
	if strings.Contains(fmt.Sprint(got), "MUST-NOT-APPEAR") || !strings.Contains(got["next"].(string), "uid=uid-app") {
		t.Fatal(got)
	}
}

func TestWaitForeignRolloutDoesNotGetBuiltInDefault(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "rollouts.kruise.io", Version: "v1alpha1", Resource: "rollouts"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "RolloutList"})
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Name: "rollouts", Kind: "Rollout", Namespaced: true}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	_, _, err := waitFor(t.Context(), dyn, waitForInput{Kind: "rollout", Group: gvr.Group, Namespace: "shop", Name: "app"})
	if err == nil || !strings.Contains(err.Error(), "condition is required") {
		t.Fatalf("foreign rollout default: %v", err)
	}
	for _, a := range dyn.Actions() {
		if a.GetVerb() == "get" {
			t.Fatalf("target read before condition validation: %+v", a)
		}
	}
}

func TestWaitMetDropsEarlierNext(t *testing.T) {
	roomForPolls(t)
	dyn := waitDynamic()
	calls := 0
	dyn.PrependReactor("get", "pods", func(clienttesting.Action) (bool, runtime.Object, error) {
		calls++
		if calls == 1 {
			return true, waitTestPod("web", "False"), nil
		}
		return true, waitTestPod("web", "True"), nil
	})
	res, _, err := waitFor(t.Context(), dyn, waitForInput{Kind: "pod", Namespace: "shop", Name: "web"})
	got := decodeToolResult(t, res)
	if err != nil || got["state"] != "met" || got["next"] != nil {
		t.Fatalf("%v %v", err, got)
	}
}

func TestWaitDefaultReadyHasNoConditionHint(t *testing.T) {
	fastWaitPolling(t)
	pending := waitTestObject("Pod", "v1", "web", map[string]any{"phase": "Pending", "conditions": []any{map[string]any{"type": "PodScheduled", "status": "True"}}})
	res, _, err := waitFor(t.Context(), waitDynamic(pending), waitForInput{Kind: "pod", Namespace: "shop", Name: "web", TimeoutSeconds: 1})
	got := decodeToolResult(t, res)
	if err != nil || got["state"] != "waiting" || strings.Contains(fmt.Sprint(got["next"]), "jsonpath") {
		t.Fatalf("%v %v", err, got)
	}
}

func TestWaitArgoBlockerGate(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "rollouts"}
	ro := func(generation int64, observed string) *unstructured.Unstructured {
		obj := waitTestObject("Rollout", "argoproj.io/v1alpha1", "api", map[string]any{"observedGeneration": observed, "workloadObservedGeneration": "2"})
		obj.SetGeneration(generation)
		obj.Object["spec"] = map[string]any{"workloadRef": map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "source"}}
		return obj
	}
	source := deploymentObj("source", 2, 2, 1, 1, 1, 1)
	// The template source is current, but the Rollout's own spec changed after
	// the controller last looked: old pods must not decide the verdict.
	if apply, coverage := waitBlockersApply(t.Context(), waitDynamic(source), gvr, ro(2, "1"), waitObjectState{}); apply || coverage != "" {
		t.Fatalf("stale rollout generation: apply=%v coverage=%q", apply, coverage)
	}
	if apply, _ := waitBlockersApply(t.Context(), waitDynamic(source), gvr, ro(2, "2"), waitObjectState{}); !apply {
		t.Fatal("observed rollout should allow blockers")
	}
	if apply, coverage := waitBlockersApply(t.Context(), waitDynamic(), gvr, ro(2, "2"), waitObjectState{}); apply || coverage == "" {
		t.Fatalf("unreadable template source must report coverage: apply=%v coverage=%q", apply, coverage)
	}
}

func TestWaitExplicitConditionsKeepTerminalStates(t *testing.T) {
	jobsGVR := schema.GroupVersionResource{Group: "batch", Version: "v1", Resource: "jobs"}
	podsGVR := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	failedJob := waitTestObject("Job", "batch/v1", "migrate", map[string]any{"conditions": []any{map[string]any{"type": "Failed", "status": "True", "message": "BackoffLimitExceeded"}}})
	finishedPod := waitTestObject("Pod", "v1", "web", map[string]any{"phase": "Succeeded"})
	for _, tc := range []struct {
		gvr       schema.GroupVersionResource
		obj       *unstructured.Unstructured
		condition string
	}{
		{jobsGVR, failedJob, "Complete"}, {jobsGVR, failedJob, "condition=Complete=True"},
		{podsGVR, finishedPod, "Ready"}, {podsGVR, finishedPod, "Ready=True"},
	} {
		check, err := waitCheckFor(tc.gvr, tc.condition)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, failure := check(tc.obj); failure == "" {
			t.Fatalf("%s on %s lost the terminal state", tc.condition, tc.obj.GetKind())
		}
	}
}
