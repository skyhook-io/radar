package mcp

import (
	"fmt"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

func waitFatalPod(name string, owner metav1.OwnerReference) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "shop", UID: types.UID("uid-" + name), OwnerReferences: []metav1.OwnerReference{owner}}, Status: corev1.PodStatus{ContainerStatuses: []corev1.ContainerStatus{{Name: "app", State: corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{Reason: "CrashLoopBackOff", Message: "fatal detail"}}}}}}
}
func waitOwner(kind, name string, uid types.UID) metav1.OwnerReference {
	yes := true
	return metav1.OwnerReference{APIVersion: "apps/v1", Kind: kind, Name: name, UID: uid, Controller: &yes}
}
func waitInstallPodCache(t *testing.T, objs ...runtime.Object) {
	t.Helper()
	t.Cleanup(k8s.ResetTestState)
	if err := k8s.InitTestResourceCacheFromConfig(k8score.CacheConfig{Client: fakeclientset.NewSimpleClientset(objs...), ResourceTypes: map[string]bool{"pods": true, "replicasets": true}}); err != nil {
		t.Fatal(err)
	}
}

func TestWaitCurrentRevisionBlockers(t *testing.T) {
	obj := deploymentObj("web", 2, 2, 3, 3, 1, 2)
	obj.SetAnnotations(map[string]string{"deployment.kubernetes.io/revision": "8"})
	oldRS := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "old", Namespace: "shop", UID: "old-rs", OwnerReferences: []metav1.OwnerReference{waitOwner("Deployment", "web", obj.GetUID())}, Annotations: map[string]string{"deployment.kubernetes.io/revision": "7"}, Labels: map[string]string{"pod-template-hash": "old"}}}
	newRS := oldRS.DeepCopy()
	newRS.Name = "new"
	newRS.UID = "new-rs"
	newRS.Annotations["deployment.kubernetes.io/revision"] = "8"
	newRS.Labels["pod-template-hash"] = "new"
	oldPod := waitFatalPod("old-pod", waitOwner("ReplicaSet", "old", "old-rs"))
	oldPod.Labels = map[string]string{"pod-template-hash": "old"}
	newPod := waitFatalPod("new-pod", waitOwner("ReplicaSet", "new", "new-rs"))
	newPod.Labels = map[string]string{"pod-template-hash": "new"}
	newPod.Status.ContainerStatuses = nil
	waitInstallPodCache(t, oldRS, newRS, oldPod, newPod)
	result := readWaitBlockers(t.Context(), waitDynamic(), deploymentsGVR, obj)
	if result.unavailable != "" || len(result.blockers) != 0 {
		t.Fatalf("old revision leaked: %+v", result)
	}
	// A rollback reuses the old ReplicaSet with a newly assigned revision.
	obj.SetAnnotations(map[string]string{"deployment.kubernetes.io/revision": "9"})
	oldRS.Annotations["deployment.kubernetes.io/revision"] = "9"
	revision, uid := currentWaitReplicaSet(obj, []*appsv1.ReplicaSet{newRS, oldRS})
	blockers := waitPodBlockers(deploymentsGVR, []*corev1.Pod{oldPod, newPod}, revision, uid)
	if len(blockers) != 1 || blockers[0].Pod != "old-pod" {
		t.Fatal(blockers)
	}
}

func TestWaitRevisionPodFilters(t *testing.T) {
	for _, tc := range []struct{ group, resource, label string }{
		{"apps", "statefulsets", "controller-revision-hash"}, {"apps", "daemonsets", "controller-revision-hash"}, {"argoproj.io", "rollouts", "rollouts-pod-template-hash"},
	} {
		pod := waitFatalPod("pod", metav1.OwnerReference{})
		pod.Labels = map[string]string{tc.label: "new"}
		gvr := schema.GroupVersionResource{Group: tc.group, Resource: tc.resource}
		if got := waitPodBlockers(gvr, []*corev1.Pod{pod}, "old", ""); len(got) != 0 {
			t.Fatal(got)
		}
		if got := waitPodBlockers(gvr, []*corev1.Pod{pod}, "new", ""); len(got) != 1 {
			t.Fatal(got)
		}
		now := metav1.Now()
		pod.DeletionTimestamp = &now
		if got := waitPodBlockers(gvr, []*corev1.Pod{pod}, "new", ""); len(got) != 0 {
			t.Fatal(got)
		}
	}
}

func TestWaitBlockerPersistence(t *testing.T) {
	for _, tc := range []struct {
		seconds int
		replace bool
		want    string
	}{
		{0, false, "waiting"}, {9, false, "waiting"}, {10, false, "blocked"}, {10, true, "waiting"},
	} {
		t.Run(fmt.Sprintf("%d-%v", tc.seconds, tc.replace), func(t *testing.T) {
			fastWaitPolling(t)
			pod := waitFatalPod("web", metav1.OwnerReference{})
			waitInstallPodCache(t, pod)
			obj := waitTestPod("web", "False")
			obj.SetUID(pod.UID)
			now := time.Now()
			previousNow := waitNow
			waitNow = func() time.Time { return now }
			t.Cleanup(func() { waitNow = previousNow })
			dyn := waitDynamic(obj)
			calls := 0
			dyn.PrependReactor("get", "pods", func(clienttesting.Action) (bool, runtime.Object, error) {
				calls++
				if calls == 2 {
					now = now.Add(time.Duration(tc.seconds) * time.Second)
					if tc.replace {
						obj = obj.DeepCopy()
						obj.SetUID("replacement")
					}
				}
				return true, obj, nil
			})
			res, _, err := waitFor(t.Context(), dyn, waitForInput{Kind: "pod", Namespace: "shop", Name: "web"})
			if err != nil {
				t.Fatal(err)
			}
			got := decodeToolResult(t, res)
			if got["state"] != tc.want {
				t.Fatal(got)
			}
			if tc.want == "blocked" && (!strings.Contains(got["next"].(string), "diagnose Pod web") || len(got["blockers"].([]any)) != 1) {
				t.Fatal(got)
			}
		})
	}
}

func TestWaitUnschedulableDoesNotEndEarly(t *testing.T) {
	fastWaitPolling(t)
	pod := waitFatalPod("web", metav1.OwnerReference{})
	pod.Status.ContainerStatuses = nil
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable", Message: "need more cpu"}}
	waitInstallPodCache(t, pod)
	obj := waitTestPod("web", "False")
	obj.SetUID(pod.UID)
	start := time.Now()
	res, _, err := waitFor(t.Context(), waitDynamic(obj), waitForInput{Kind: "pod", Namespace: "shop", Name: "web"})
	if err != nil {
		t.Fatal(err)
	}
	got := decodeToolResult(t, res)
	if got["state"] != "waiting" || time.Since(start) < waitMaxTimeout || !strings.Contains(got["next"].(string), "pods can't be scheduled") {
		t.Fatal(got)
	}
}

func TestWaitBlockerAuthorization(t *testing.T) {
	fastWaitPolling(t)
	obj := deploymentObj("web", 1, 1, 1, 1, 0, 0)
	ctx := withTestUserPerms(t, "limited", nil, []string{"shop"})
	_, perms := resolveUserPerms(ctx)
	perms.SetCanI("list", "", "pods", "shop", false)
	dyn := waitDynamic(obj)
	res, _, err := waitFor(ctx, dyn, waitForInput{Kind: "deployment", Namespace: "shop", Name: "web"})
	if err != nil {
		t.Fatal(err)
	}
	got := decodeToolResult(t, res)
	if got["blockersUnavailable"] != "pods are not readable with your permissions" || got["blockers"] != nil {
		t.Fatal(got)
	}
	// Every dependency gate runs before touching a cache, including Argo's RS hop.
	perms.SetCanI("list", "", "pods", "shop", true)
	perms.SetCanI("get", "apps", "replicasets", "shop", false)
	for _, gvr := range []schema.GroupVersionResource{deploymentsGVR, {Group: "argoproj.io", Resource: "rollouts"}} {
		result := readWaitBlockers(ctx, waitDynamic(), gvr, obj)
		if result.unavailable != "workload's ReplicaSets or ControllerRevisions are not readable" || len(result.blockers) != 0 {
			t.Fatal(result)
		}
	}
	perms.SetCanI("list", "apps", "controllerrevisions", "shop", false)
	if result := readWaitBlockers(ctx, waitDynamic(), schema.GroupVersionResource{Group: "apps", Resource: "daemonsets"}, obj); result.unavailable != "workload's ReplicaSets or ControllerRevisions are not readable" {
		t.Fatal(result)
	}
}

func TestWaitNamespacePinPrecedesReads(t *testing.T) {
	previous := k8s.ForceNamespaceScope
	k8s.ForceNamespaceScope = true
	k8s.SetNamespaceScopeOverride("allowed")
	t.Cleanup(func() { k8s.ForceNamespaceScope = previous; k8s.ClearNamespaceScopeOverride() })
	dyn := waitDynamic()
	if _, _, err := waitFor(t.Context(), dyn, waitForInput{Kind: "pod", Namespace: "shop", Name: "web"}); err == nil || !strings.Contains(err.Error(), "forbidden") {
		t.Fatal(err)
	}
	if len(dyn.Actions()) != 0 {
		t.Fatal(dyn.Actions())
	}
}

func TestWaitFatalBlockerSurvivesCap(t *testing.T) {
	blockers := []waitBlocker{{Pod: "a", Reason: "Unschedulable"}, {Pod: "b", Reason: "Unschedulable"}, {Pod: "c", Reason: "Unschedulable"}, {Pod: "d", Reason: "CrashLoopBackOff"}}
	got := capWaitBlockers(blockers)
	if len(got) != 3 || got[0].Pod != "d" {
		t.Fatal(got)
	}
}

func TestWaitDaemonSetRevisionsReadThroughCallerClient(t *testing.T) {
	ds := waitTestObject("DaemonSet", "apps/v1", "agent", map[string]any{"observedGeneration": int64(1)})
	ds.Object["spec"] = map[string]any{"selector": map[string]any{"matchLabels": map[string]any{"app": "agent"}}}
	yes := true
	revision := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1", "kind": "ControllerRevision", "revision": int64(2),
		"metadata": map[string]any{"name": "agent-new", "namespace": "shop", "labels": map[string]any{"app": "agent", "controller-revision-hash": "new"},
			"ownerReferences": []any{map[string]any{"apiVersion": "apps/v1", "kind": "DaemonSet", "name": "agent", "uid": "uid-agent", "controller": yes}}},
	}}
	pod := waitFatalPod("agent-x", waitOwner("DaemonSet", "agent", "uid-agent"))
	pod.Labels = map[string]string{"app": "agent", "controller-revision-hash": "new"}
	waitInstallPodCache(t, pod)
	dyn := waitDynamic(revision)
	result := readWaitBlockers(t.Context(), dyn, schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "daemonsets"}, ds)
	if result.unavailable != "" || len(result.blockers) != 1 || result.blockers[0].Reason != "CrashLoopBackOff" {
		t.Fatalf("%+v", result)
	}
}

func TestWaitImagePullRetriesKeepOneBlockerClock(t *testing.T) {
	if waitBlockerFamily("ErrImagePull") != waitBlockerFamily("ImagePullBackOff") || waitBlockerFamily("CrashLoopBackOff") != "CrashLoopBackOff" {
		t.Fatal("image pull reasons must share a persistence key")
	}
}

func TestWaitUnschedulableJobPointsAtThePod(t *testing.T) {
	fastWaitPolling(t)
	job := waitTestObject("Job", "batch/v1", "migrate", map[string]any{"active": int64(1)})
	yes := true
	pod := waitFatalPod("migrate-x", metav1.OwnerReference{APIVersion: "batch/v1", Kind: "Job", Name: "migrate", UID: job.GetUID(), Controller: &yes})
	pod.Status.ContainerStatuses = nil
	pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable", Message: "need more cpu"}}
	waitInstallPodCache(t, pod)
	res, _, err := waitFor(t.Context(), waitDynamic(job), waitForInput{Kind: "job", Namespace: "shop", Name: "migrate"})
	if err != nil {
		t.Fatal(err)
	}
	got := decodeToolResult(t, res)
	if next, _ := got["next"].(string); got["state"] != "waiting" || !strings.Contains(next, "diagnose Pod migrate-x in shop") {
		t.Fatal(got)
	}
}
