package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	aicontext "github.com/skyhook-io/radar/pkg/ai/context"
)

func typedEvent(name, reason, eventType string, last time.Time) *corev1.Event {
	return &corev1.Event{
		ObjectMeta:     metav1.ObjectMeta{Name: name, Namespace: "shop"},
		Reason:         reason,
		Message:        "message for " + reason,
		Type:           eventType,
		Count:          1,
		LastTimestamp:  metav1.Time{Time: last},
		InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "shop", Name: "pod-" + name},
	}
}

func callGetEvents(t *testing.T, input eventsInput) getEventsResponseMCP {
	t.Helper()
	res, _, err := handleGetEvents(context.Background(), nil, input)
	if err != nil {
		t.Fatalf("handleGetEvents(%+v): %v", input, err)
	}
	var resp getEventsResponseMCP
	text := res.Content[0].(*mcp.TextContent).Text
	if uerr := json.Unmarshal([]byte(text), &resp); uerr != nil {
		t.Fatalf("unmarshal %q: %v", text, uerr)
	}
	return resp
}

// get_events is named for events, not warnings: the default returns ALL
// types, but dedup sorts Warning groups first, so warnings lead while a
// resource's lifecycle timeline still shows instead of an empty result. Even
// though the Warning here is the OLDEST event, it must sort ahead of the two
// newer Normal groups. type=Warning/Normal narrow it.
func TestHandleGetEvents_TypeFilterAndWarningFirstOrder(t *testing.T) {
	defer k8s.ResetTestState()
	now := time.Now()
	client := fake.NewSimpleClientset(
		typedEvent("n1", "Scheduled", "Normal", now.Add(-1*time.Minute)), // newest
		typedEvent("n2", "Pulled", "Normal", now.Add(-2*time.Minute)),
		typedEvent("w1", "BackOff", "Warning", now.Add(-3*time.Minute)), // oldest
	)
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}

	// Informer warm-up: poll until all three groups are visible.
	deadline := time.Now().Add(2 * time.Second)
	var byDefault getEventsResponseMCP
	for time.Now().Before(deadline) {
		byDefault = callGetEvents(t, eventsInput{Namespace: "shop"})
		if len(byDefault.Events) >= 3 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if len(byDefault.Events) != 3 {
		t.Fatalf("default = %+v, want all 3 groups (all types)", byDefault.Events)
	}
	if byDefault.Events[0].Reason != "BackOff" {
		t.Errorf("default[0] = %q, want the Warning group first despite being oldest", byDefault.Events[0].Reason)
	}

	warningOnly := callGetEvents(t, eventsInput{Namespace: "shop", Type: "Warning"})
	if len(warningOnly.Events) != 1 || warningOnly.Events[0].Reason != "BackOff" {
		t.Fatalf("type=Warning = %+v, want ONLY the Warning group", warningOnly.Events)
	}

	normal := callGetEvents(t, eventsInput{Namespace: "shop", Type: "Normal"})
	reasons := map[string]bool{}
	for _, e := range normal.Events {
		reasons[e.Reason] = true
	}
	if len(normal.Events) != 2 || !reasons["Scheduled"] || !reasons["Pulled"] {
		t.Fatalf("type=Normal = %+v, want the two Normal groups", normal.Events)
	}

	if _, _, err := handleGetEvents(context.Background(), nil, eventsInput{Namespace: "shop", Type: "bogus"}); err == nil || !strings.Contains(err.Error(), "invalid type") {
		t.Fatalf("type=bogus err = %v, want invalid-type error", err)
	}
}

// The documented limit range must be REAL: a fixed internal 20-group dedup
// window used to make limit=21..100 silently unreachable. And truncation is
// reported from the true pre-cap total — with "raise limit" advice only when
// raising the limit can actually help.
func TestHandleGetEvents_LimitBeyondTwentyAndHints(t *testing.T) {
	defer k8s.ResetTestState()
	now := time.Now()
	objs := make([]runtime.Object, 0, 35)
	for i := 0; i < 35; i++ {
		objs = append(objs, &corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: fmt.Sprintf("ev-%02d", i), Namespace: "shop"},
			Reason:         fmt.Sprintf("Reason%02d", i),
			Message:        fmt.Sprintf("distinct message %02d", i),
			Type:           "Warning",
			Count:          1,
			LastTimestamp:  metav1.Time{Time: now.Add(-time.Duration(i) * time.Second)},
			InvolvedObject: corev1.ObjectReference{Kind: "Pod", Namespace: "shop", Name: fmt.Sprintf("pod-%02d", i)},
		})
	}
	if err := k8s.InitTestResourceCache(fake.NewSimpleClientset(objs...)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	var byDefault getEventsResponseMCP
	for time.Now().Before(deadline) {
		byDefault = callGetEvents(t, eventsInput{Namespace: "shop"})
		if len(byDefault.Events) >= 20 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if len(byDefault.Events) != 20 {
		t.Fatalf("default limit: %d groups, want 20", len(byDefault.Events))
	}
	if !strings.Contains(byDefault.NarrowHint, "20 of 35") || !strings.Contains(byDefault.NarrowHint, "raise limit") {
		t.Errorf("default narrowHint = %q, want true pre-cap total and raise-limit advice", byDefault.NarrowHint)
	}

	raised := callGetEvents(t, eventsInput{Namespace: "shop", Limit: 100})
	if len(raised.Events) != 35 {
		t.Fatalf("limit=100: %d groups, want all 35 (internal 20-cap must not apply)", len(raised.Events))
	}
	if raised.NarrowHint != "" {
		t.Errorf("limit=100 with 35 groups: narrowHint = %q, want none", raised.NarrowHint)
	}

	atMax := callGetEvents(t, eventsInput{Namespace: "shop", Limit: 25})
	if len(atMax.Events) != 25 || !strings.Contains(atMax.NarrowHint, "25 of 35") {
		t.Fatalf("limit=25: %d groups, hint %q; want 25 groups and 25-of-35 hint", len(atMax.Events), atMax.NarrowHint)
	}
}

// The events include on get_resource signals truncation via eventsTotalGroups
// (map shape makes this additive); the field is absent when nothing was cut.
func TestAttachResourceExtras_EventsTotalGroups(t *testing.T) {
	defer k8s.ResetTestState()
	now := time.Now()
	objs := make([]runtime.Object, 0, 12)
	for i := 0; i < 12; i++ {
		objs = append(objs, &corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: fmt.Sprintf("dep-ev-%02d", i), Namespace: "shop"},
			Reason:         fmt.Sprintf("DeployReason%02d", i),
			Message:        fmt.Sprintf("deployment condition %02d", i),
			Type:           "Warning",
			Count:          1,
			LastTimestamp:  metav1.Time{Time: now.Add(-time.Duration(i) * time.Second)},
			InvolvedObject: corev1.ObjectReference{Kind: "Deployment", Namespace: "shop", Name: "web"},
		})
	}
	if err := k8s.InitTestResourceCache(fake.NewSimpleClientset(objs...)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	cache := k8s.GetResourceCache()

	deadline := time.Now().Add(2 * time.Second)
	var result map[string]any
	for time.Now().Before(deadline) {
		result = map[string]any{}
		attachResourceExtras(context.Background(), cache, result, map[string]bool{"events": true}, "deployment", "apps", "shop", "web", "")
		if evs, ok := result["events"].([]aicontext.DeduplicatedEvent); ok && len(evs) == 10 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	evs, _ := result["events"].([]aicontext.DeduplicatedEvent)
	if len(evs) != 10 {
		t.Fatalf("events include = %d groups, want capped 10 (got %+v)", len(evs), result["events"])
	}
	if total, _ := result["eventsTotalGroups"].(int); total != 12 {
		t.Errorf("eventsTotalGroups = %v, want 12", result["eventsTotalGroups"])
	}

	// Under the cap: no truncation field.
	few := map[string]any{}
	attachResourceExtras(context.Background(), cache, few, map[string]bool{"events": true}, "deployment", "apps", "shop", "missing", "")
	if _, present := few["eventsTotalGroups"]; present {
		t.Errorf("eventsTotalGroups present with no truncation: %+v", few)
	}
}

func TestFetchEventsForResource_ReportsPreCapGroupTotal(t *testing.T) {
	defer k8s.ResetTestState()
	now := time.Now()
	objects := make([]runtime.Object, 0, 12)
	for i := 0; i < 12; i++ {
		objects = append(objects, &corev1.Event{
			ObjectMeta:     metav1.ObjectMeta{Name: fmt.Sprintf("diagnose-ev-%02d", i), Namespace: "shop"},
			Reason:         fmt.Sprintf("DiagnoseReason%02d", i),
			Message:        fmt.Sprintf("distinct diagnosis event %02d", i),
			Type:           corev1.EventTypeWarning,
			Count:          1,
			LastTimestamp:  metav1.Time{Time: now.Add(-time.Duration(i) * time.Second)},
			InvolvedObject: corev1.ObjectReference{Kind: "Deployment", Namespace: "shop", Name: "web"},
		})
	}
	if err := k8s.InitTestResourceCache(fake.NewSimpleClientset(objects...)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	var (
		groups []aicontext.DeduplicatedEvent
		total  int
		err    error
	)
	for time.Now().Before(deadline) {
		groups, total, err = fetchEventsForResource(k8s.GetResourceCache(), "deployments", "apps", "shop", "web", "", nil, 10)
		if err != nil || total == 12 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("fetchEventsForResource: %v", err)
	}
	if len(groups) != 10 || total != 12 {
		t.Fatalf("groups=%d total=%d, want capped response of 10 from 12 deduplicated groups", len(groups), total)
	}

	groups, total, err = fetchEventsForResource(k8s.GetResourceCache(), "deployments", "apps", "shop", "missing", "", nil, 10)
	if err != nil || len(groups) != 0 || total != 0 {
		t.Fatalf("missing resource groups=%d total=%d err=%v, want empty successful result", len(groups), total, err)
	}
}

func TestFetchEventsForResource_RolloutWarningSurvivesKindAndGroupFiltering(t *testing.T) {
	defer k8s.ResetTestState()
	now := metav1.Now()
	objects := []runtime.Object{
		&corev1.Event{
			ObjectMeta: metav1.ObjectMeta{Name: "argo-warning", Namespace: "shop"},
			InvolvedObject: corev1.ObjectReference{
				APIVersion: "argoproj.io/v1alpha1", Kind: "Rollout", Namespace: "shop", Name: "checkout",
			},
			Reason: "RolloutPaused", Type: corev1.EventTypeWarning,
			Message: "rollout requires analysis", LastTimestamp: now,
		},
		&corev1.Event{
			ObjectMeta: metav1.ObjectMeta{Name: "other-group-warning", Namespace: "shop"},
			InvolvedObject: corev1.ObjectReference{
				APIVersion: "delivery.example.io/v1", Kind: "Rollout", Namespace: "shop", Name: "checkout",
			},
			Reason: "WrongGroup", Type: corev1.EventTypeWarning,
			Message: "same kind and name in another API group", LastTimestamp: now,
		},
	}
	if err := k8s.InitTestResourceCache(fake.NewSimpleClientset(objects...)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}

	deadline := time.Now().Add(2 * time.Second)
	var (
		groups []aicontext.DeduplicatedEvent
		total  int
		err    error
	)
	for time.Now().Before(deadline) {
		groups, total, err = fetchEventsForResource(k8s.GetResourceCache(), "rollouts", "argoproj.io", "shop", "checkout", "", nil, 10)
		if err != nil || total == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("fetchEventsForResource: %v", err)
	}
	if len(groups) != 1 || total != 1 || groups[0].Reason != "RolloutPaused" {
		t.Fatalf("Rollout events = %+v total=%d, want only the argoproj.io warning", groups, total)
	}
}

func TestFilterEventsByInvolvedObject_RequiresExactAPIGroup(t *testing.T) {
	events := []*corev1.Event{
		{Type: corev1.EventTypeWarning, Reason: "Core", InvolvedObject: corev1.ObjectReference{APIVersion: "v1", Kind: "Service", Name: "api"}},
		{Type: corev1.EventTypeWarning, Reason: "Knative", InvolvedObject: corev1.ObjectReference{APIVersion: "serving.knative.dev/v1", Kind: "Service", Name: "api"}},
	}

	core := filterEventsByInvolvedObject(events, "Service", "", "api", "", nil)
	if len(core) != 1 || core[0].Reason != "Core" {
		t.Fatalf("core Service events = %+v, want only core/v1", core)
	}
	knative := filterEventsByInvolvedObject(events, "Service", "serving.knative.dev", "api", "", nil)
	if len(knative) != 1 || knative[0].Reason != "Knative" {
		t.Fatalf("Knative Service events = %+v, want only serving.knative.dev", knative)
	}
}

func TestFilterEventsByInvolvedObject_CurrentIncarnations(t *testing.T) {
	event := func(reason, kind, group, name, uid string) *corev1.Event {
		version := "v1"
		if group != "" {
			version = group + "/v1"
		}
		return &corev1.Event{Type: corev1.EventTypeWarning, Reason: reason, InvolvedObject: corev1.ObjectReference{APIVersion: version, Kind: kind, Name: name, UID: types.UID(uid)}}
	}
	events := []*corev1.Event{
		event("CurrentRoot", "Deployment", "apps", "web", "root-now"),
		event("ReplacedRoot", "Deployment", "apps", "web", "root-before"),
		event("UIDlessRoot", "Deployment", "apps", "web", ""),
		event("CurrentPod", "Pod", "", "worker", "pod-now"),
		event("ReplacedPod", "Pod", "", "worker", "pod-before"),
		event("UIDlessPod", "Pod", "", "worker", ""),
		event("WrongGroup", "Deployment", "custom.example.io", "web", "root-now"),
		event("WrongPodGroup", "Pod", "custom.example.io", "worker", "pod-now"),
		event("UnknownPod", "Pod", "", "foreign", "other"),
	}
	got := filterEventsByInvolvedObject(events, "Deployment", "apps", "web", "root-now", map[string]types.UID{"worker": "pod-now"})
	var reasons []string
	for _, e := range got {
		reasons = append(reasons, e.Reason)
	}
	if strings.Join(reasons, ",") != "CurrentRoot,UIDlessRoot,CurrentPod,UIDlessPod" {
		t.Fatalf("current evidence = %v", reasons)
	}
	// Supplemental includes do not enroll workload Pod events.
	if got := filterEventsByInvolvedObject(events, "Deployment", "apps", "web", "root-now", nil); len(got) != 2 {
		t.Fatalf("root-only evidence = %v", got)
	}
	// No Node UID=name exception: actual Kubernetes identity wins.
	nodes := []*corev1.Event{event("CurrentNode", "Node", "", "node-a", "node-uid"), event("NameUID", "Node", "", "node-a", "node-a")}
	if got := filterEventsByInvolvedObject(nodes, "Node", "", "node-a", "node-uid", nil); len(got) != 1 || got[0].Reason != "CurrentNode" {
		t.Fatalf("Node evidence = %v", got)
	}
}

func TestGetResourceEvents_UsesObservedResourceUID(t *testing.T) {
	defer k8s.ResetTestState()
	now := metav1.Now()
	root := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "shop", UID: "root-now"}}
	events := []runtime.Object{root}
	for _, uid := range []types.UID{"root-now", "root-before"} {
		events = append(events, &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: string(uid), Namespace: "shop"}, Type: corev1.EventTypeWarning, Reason: string(uid), Message: string(uid), LastTimestamp: now, InvolvedObject: corev1.ObjectReference{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "shop", Name: "web", UID: uid}})
	}
	client := fake.NewSimpleClientset(events...)
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatal(err)
	}
	var response struct {
		Events []aicontext.DeduplicatedEvent `json:"events"`
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		result, _, err := handleGetResource(t.Context(), nil, getResourceInput{Kind: "deployment", Namespace: "shop", Name: "web", Include: "events", Context: "none"})
		if err == nil {
			if err := json.Unmarshal([]byte(extractText(t, result)), &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Events) > 0 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("events never loaded: %+v err=%v", response, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(response.Events) != 1 || response.Events[0].Reason != "root-now" {
		t.Fatalf("get_resource current evidence = %+v", response.Events)
	}
	pods := []*corev1.Pod{{ObjectMeta: metav1.ObjectMeta{Name: "worker", Namespace: "shop", UID: "pod-now"}}}
	for _, uid := range []types.UID{"pod-now", "pod-before"} {
		_, err := client.CoreV1().Events("shop").Create(t.Context(), &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: string(uid), Namespace: "shop"}, Type: corev1.EventTypeWarning, Reason: string(uid), Message: string(uid), LastTimestamp: now, InvolvedObject: corev1.ObjectReference{APIVersion: "v1", Kind: "Pod", Namespace: "shop", Name: "worker", UID: uid}}, metav1.CreateOptions{})
		if err != nil {
			t.Fatal(err)
		}
	}
	deadline = time.Now().Add(2 * time.Second)
	for {
		groups, total, err := fetchEventsForResource(k8s.GetResourceCache(), "deployment", "apps", "shop", "web", root.UID, pods, 10)
		if err != nil {
			t.Fatal(err)
		}
		if total == 2 {
			for _, g := range groups {
				if g.Reason != "root-now" && g.Reason != "pod-now" {
					t.Fatalf("diagnose current evidence = %+v", groups)
				}
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("diagnose event groups = %+v", groups)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestDiagnoseExcludesPodsFromPreviousWorkloadAndIntermediateOwners(t *testing.T) {
	defer k8s.ResetTestState()
	controller := true
	root := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "fleet", Namespace: "shop", UID: "current-root"}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "fleet"}}, Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"app": "fleet"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}}}}
	objects := []runtime.Object{root}
	for _, item := range []struct{ name, childUID, rootUID, podOwnerUID string }{
		{"current", "current-rs", "current-root", "current-rs"},
		{"previous-root", "old-rs", "old-root", "old-rs"},
		{"previous-rs", "replaced-rs", "current-root", "previous-rs"},
	} {
		objects = append(objects, &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Name: item.name, Namespace: "shop", UID: types.UID(item.childUID), OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "Deployment", Name: "fleet", UID: types.UID(item.rootUID), Controller: &controller}}}})
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: item.name, Namespace: "shop", UID: types.UID(item.name + "-uid"), OwnerReferences: []metav1.OwnerReference{{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: item.name, UID: types.UID(item.podOwnerUID), Controller: &controller}}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}}
		event := &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: item.name, Namespace: "shop"}, Type: corev1.EventTypeWarning, Reason: item.name, Message: item.name, LastTimestamp: metav1.Now(), InvolvedObject: corev1.ObjectReference{APIVersion: "v1", Kind: "Pod", Name: pod.Name, Namespace: pod.Namespace, UID: pod.UID}}
		objects = append(objects, pod, event)
	}
	if err := k8s.InitTestResourceCache(fake.NewClientset(objects...)); err != nil {
		t.Fatal(err)
	}
	var response diagnoseResponse
	deadline := time.Now().Add(2 * time.Second)
	for {
		result, _, err := handleDiagnose(t.Context(), nil, testDiagnoseInput("deployment", "shop", "fleet"))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal([]byte(extractText(t, result)), &response); err != nil {
			t.Fatal(err)
		}
		if response.Pods > 0 && len(response.Events) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("fixture did not sync: %+v", response)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if response.Pods != 1 || len(response.PodNames) != 1 || response.PodNames[0] != "current" {
		t.Fatalf("diagnose current pods=%d names=%v", response.Pods, response.PodNames)
	}
	if len(response.Events) != 1 || response.Events[0].Reason != "current" {
		t.Fatalf("diagnose leaked previous controller warnings: %+v", response.Events)
	}
}
