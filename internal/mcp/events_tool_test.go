package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
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
