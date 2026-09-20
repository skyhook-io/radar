package k8s

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/k8score"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
)

func correlationOwner(apiVersion, kind, name, uid string) metav1.OwnerReference {
	return metav1.OwnerReference{APIVersion: apiVersion, Kind: kind, Name: name, UID: types.UID(uid), Controller: boolPtr(true)}
}

type correlationFixture struct {
	pods    []*corev1.Pod
	objects []runtime.Object
}

func TestPostBindCorrelationIndependentOwners(t *testing.T) {
	now := time.Now()
	owner := func(name string) metav1.OwnerReference {
		return correlationOwner("apps/v1", "Deployment", name, name+"-uid")
	}
	event := func(pod *corev1.Pod, reason, message string) *corev1.Event {
		return &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: pod.Name + "-failure", Namespace: pod.Namespace}, InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name, UID: pod.UID}, Reason: reason, Message: message, LastTimestamp: metav1.NewTime(now)}
	}
	cases := []struct {
		name         string
		mutate       func(*correlationFixture)
		wantHintRows int
	}{
		{name: "independent owners", wantHintRows: 2},
		{name: "same workload replicas", mutate: func(f *correlationFixture) { f.pods[1].OwnerReferences = f.pods[0].OwnerReferences }},
		{name: "rolling ReplicaSets of one Deployment", mutate: func(f *correlationFixture) {
			for _, pod := range f.pods {
				ref := correlationOwner("apps/v1", "ReplicaSet", pod.Name+"-revision", pod.Name+"-rs-uid")
				pod.OwnerReferences = []metav1.OwnerReference{ref}
				f.objects = append(f.objects, &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: ref.Name, Namespace: pod.Namespace, UID: ref.UID, OwnerReferences: []metav1.OwnerReference{owner("shared")}}})
			}
		}},
		{name: "jobs of one CronJob", mutate: func(f *correlationFixture) {
			for _, pod := range f.pods {
				ref := correlationOwner("batch/v1", "Job", pod.Name+"-run", pod.Name+"-job-uid")
				pod.OwnerReferences = []metav1.OwnerReference{ref}
				f.objects = append(f.objects, &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: ref.Name, Namespace: pod.Namespace, UID: ref.UID, OwnerReferences: []metav1.OwnerReference{correlationOwner("batch/v1", "CronJob", "shared", "cron-uid")}}})
			}
		}},
		{name: "missing ReplicaSet", mutate: func(f *correlationFixture) {
			f.pods[1].OwnerReferences = []metav1.OwnerReference{correlationOwner("apps/v1", "ReplicaSet", "missing", "missing-uid")}
		}},
		{name: "recreated ReplicaSet UID mismatch", mutate: func(f *correlationFixture) {
			f.pods[1].OwnerReferences = []metav1.OwnerReference{correlationOwner("apps/v1", "ReplicaSet", "revision", "old")}
			f.objects = append(f.objects, &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: "revision", Namespace: "visible", UID: "new", OwnerReferences: []metav1.OwnerReference{owner("other")}}})
		}},
		{name: "standalone Pod", mutate: func(f *correlationFixture) { f.pods[1].OwnerReferences = nil }},
		{name: "unknown custom controller", mutate: func(f *correlationFixture) {
			f.pods[1].OwnerReferences = []metav1.OwnerReference{correlationOwner("example.com/v1", "Deployment", "custom", "custom-uid")}
		}},
		{name: "unknown custom ReplicaSet parent", mutate: func(f *correlationFixture) {
			ref := correlationOwner("apps/v1", "ReplicaSet", "revision", "rs-uid")
			f.pods[1].OwnerReferences = []metav1.OwnerReference{ref}
			f.objects = append(f.objects, &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: ref.Name, Namespace: "visible", UID: ref.UID, OwnerReferences: []metav1.OwnerReference{correlationOwner("argoproj.io/v1alpha1", "Rollout", "rollout", "rollout-uid")}}})
		}},
		{name: "missing owner UID", mutate: func(f *correlationFixture) { f.pods[1].OwnerReferences[0].UID = "" }},
		{name: "noncontroller reference", mutate: func(f *correlationFixture) { f.pods[1].OwnerReferences[0].Controller = boolPtr(false) }},
		{name: "different nodes", mutate: func(f *correlationFixture) { f.pods[1].Spec.NodeName = "node-b" }},
		{name: "recovered Pod", mutate: func(f *correlationFixture) { f.pods[1].Status.Phase = corev1.PodRunning }},
		{name: "terminating Pod", mutate: func(f *correlationFixture) {
			ts := metav1.NewTime(now)
			f.pods[1].DeletionTimestamp = &ts
			f.pods[1].Finalizers = []string{"fixture"}
		}},
		{name: "recent volume failure cannot corroborate network", mutate: func(f *correlationFixture) {
			f.objects = append(f.objects, event(f.pods[1], "FailedMount", "required configmap absent"))
		}},
		{name: "explicit network failure cannot corroborate unknown fallback", mutate: func(f *correlationFixture) {
			f.objects = append(f.objects, event(f.pods[1], "FailedCreatePodSandBox", "failed to assign an IP address"))
		}},
		{name: "different explicit network classes", mutate: func(f *correlationFixture) {
			f.objects = append(f.objects, event(f.pods[0], "FailedCreatePodSandBox", "failed to assign an IP address"), event(f.pods[1], "FailedCreatePodSandBox", "runtime sandbox error"))
		}},
		{name: "same explicit network class", wantHintRows: 2, mutate: func(f *correlationFixture) {
			for _, pod := range f.pods {
				f.objects = append(f.objects, event(pod, "FailedCreatePodSandBox", "failed to assign an IP address"))
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer ResetTestState()
			f := correlationFixture{}
			for _, name := range []string{"one", "two"} {
				pod := postBindContainerCreatingPod("visible", name, "node-a", now.Add(-45*time.Minute))
				pod.UID = types.UID(name + "-pod")
				pod.OwnerReferences = []metav1.OwnerReference{owner(name)}
				f.pods = append(f.pods, pod)
			}
			if tc.mutate != nil {
				tc.mutate(&f)
			}
			for _, pod := range f.pods {
				f.objects = append(f.objects, pod)
			}
			if err := InitTestResourceCache(fake.NewClientset(f.objects...)); err != nil {
				t.Fatal(err)
			}
			cache := GetResourceCache()
			original := detectPostBindProblems(cache, "visible", now)
			actual := append([]Detection(nil), original...)
			appendPostBindNodeCorrelation(cache, actual, now)
			hints := 0
			for i := range actual {
				if evidence := actual[i].NodeStartupCorroboration; evidence != nil {
					hints++
					if evidence.PodCount != 2 || evidence.OwnerCount != 2 || len(evidence.Pods) != 2 {
						t.Errorf("unexpected evidence: %+v", evidence)
					}
				}
				actual[i].NodeStartupCorroboration = nil
				actual[i].Message = strings.Split(actual[i].Message, "; same node has ")[0]
			}
			if hints != tc.wantHintRows {
				t.Errorf("hint rows=%d want=%d, detections=%+v", hints, tc.wantHintRows, actual)
			}
			if !reflect.DeepEqual(actual, original) {
				t.Fatalf("correlation changed underlying detections: before=%+v after=%+v", original, actual)
			}
		})
	}
}

func TestPostBindCorrelationVisibleNamespacesAndDuplicateInputs(t *testing.T) {
	defer ResetTestState()
	old := time.Now().Add(-45 * time.Minute)
	var objects []runtime.Object
	for _, ns := range []string{"visible", "hidden"} {
		pod := postBindContainerCreatingPod(ns, "pod", "node-a", old)
		pod.OwnerReferences = []metav1.OwnerReference{correlationOwner("apps/v1", "StatefulSet", "workload", ns+"-uid")}
		objects = append(objects, pod)
	}
	if err := InitTestResourceCache(fake.NewClientset(objects...)); err != nil {
		t.Fatal(err)
	}
	cache := GetResourceCache()
	for _, d := range DetectPostBindProblemsForNamespaces(cache, []string{"visible", "visible"}) {
		if d.NodeStartupCorroboration != nil {
			t.Fatalf("hidden namespace or duplicated row inflated correlation: %+v", d)
		}
	}
	rows := DetectPostBindProblemsForNamespaces(cache, []string{"visible", "hidden", "visible"})
	if len(rows) != 3 {
		t.Fatalf("underlying row behavior changed: %d", len(rows))
	}
	for _, d := range rows {
		if d.NodeStartupCorroboration == nil || d.NodeStartupCorroboration.PodCount != 2 || d.NodeStartupCorroboration.OwnerCount != 2 {
			t.Fatalf("bad scoped unique counts: %+v", d)
		}
	}
}

func TestPostBindCorrelationMissingIntermediateVisibility(t *testing.T) {
	defer ResetTestState()
	old := time.Now().Add(-45 * time.Minute)
	var objects []runtime.Object
	for _, name := range []string{"one", "two"} {
		ref := correlationOwner("apps/v1", "ReplicaSet", name+"-rs", name+"-rs-uid")
		pod := postBindContainerCreatingPod("visible", name, "node-a", old)
		pod.OwnerReferences = []metav1.OwnerReference{ref}
		objects = append(objects, pod, &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: ref.Name, Namespace: pod.Namespace, UID: ref.UID, OwnerReferences: []metav1.OwnerReference{correlationOwner("apps/v1", "Deployment", name, name+"-uid")}}})
	}
	if err := InitScopedTestResourceCache(fake.NewClientset(objects...), map[string]k8score.ResourceScope{k8score.Pods: {Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	cache := GetResourceCache()
	if cache.ReplicaSets() != nil {
		t.Fatal("fixture must have no ReplicaSet visibility")
	}
	problems := DetectPostBindProblems(cache, "visible")
	if len(problems) != 2 {
		t.Fatalf("underlying issues lost: %+v", problems)
	}
	for _, d := range problems {
		if d.NodeStartupCorroboration != nil {
			t.Fatalf("missing intermediate visibility invented independence: %+v", d)
		}
	}
}

func TestPostBindCorrelationReferencesAreDeterministicAndInternal(t *testing.T) {
	defer ResetTestState()
	old := time.Now().Add(-45 * time.Minute)
	var objects []runtime.Object
	for _, name := range []string{"z", "c", "b", "a", "e", "d", "f"} {
		pod := postBindContainerCreatingPod("visible", name, "node-a", old)
		pod.OwnerReferences = []metav1.OwnerReference{correlationOwner("apps/v1", "StatefulSet", name, name+"-uid")}
		objects = append(objects, pod)
	}
	if err := InitTestResourceCache(fake.NewClientset(objects...)); err != nil {
		t.Fatal(err)
	}
	rows := DetectPostBindProblems(GetResourceCache(), "visible")
	for _, row := range rows {
		evidence := row.NodeStartupCorroboration
		if evidence == nil || evidence.PodCount != 7 || evidence.OwnerCount != 7 {
			t.Fatalf("missing complete counts: %+v", row)
		}
		for i := 1; i < len(evidence.Pods); i++ {
			if evidence.Pods[i-1].Name >= evidence.Pods[i].Name {
				t.Fatalf("unsorted refs: %+v", evidence.Pods)
			}
		}
		encoded, err := json.Marshal(row)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "NodeStartupCorroboration") {
			t.Fatal("internal evidence unexpectedly serialized")
		}
		if !strings.Contains(row.Message, "7 visible pods across 7 distinct workload owners") {
			t.Fatal("legacy consumers lost evidence")
		}
	}
}
