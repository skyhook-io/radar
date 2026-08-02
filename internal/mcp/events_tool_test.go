package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
		attachResourceExtras(context.Background(), cache, result, map[string]bool{"events": true}, "deployment", "shop", "web")
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
	attachResourceExtras(context.Background(), cache, few, map[string]bool{"events": true}, "deployment", "shop", "missing")
	if _, present := few["eventsTotalGroups"]; present {
		t.Errorf("eventsTotalGroups present with no truncation: %+v", few)
	}
}
