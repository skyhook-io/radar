package server

import (
	"encoding/json"
	"testing"
)

// premarshalEventData must produce exactly the bytes the old per-client writer
// produced via json.Marshal(event.Data) — the optimization moves the marshal
// from once-per-client to once-per-broadcast, it must not change the wire bytes.
func TestPremarshalEventData_MatchesPerClientMarshal(t *testing.T) {
	// Include a value with HTML-significant chars (<,>,&) — json.Marshal escapes
	// these, so this guards that the pre-marshal path keeps that escaping.
	data := map[string]any{
		"kind":      "Pod",
		"namespace": "default",
		"name":      "web<svc>&db",
		"operation": "update",
		"diff": map[string]any{
			"summary": "image changed",
			"fields":  []any{map[string]any{"path": "spec.image", "old": "a", "new": "b"}},
		},
	}
	ev := SSEEvent{Event: "k8s_event", Data: data}

	want, err := json.Marshal(data) // the bytes the old per-client path emitted
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	got := premarshalEventData(ev)
	raw, ok := got.Data.(json.RawMessage)
	if !ok {
		t.Fatalf("expected Data to be json.RawMessage after premarshal, got %T", got.Data)
	}
	if string(raw) != string(want) {
		t.Fatalf("premarshaled bytes differ from per-client marshal:\n got=%s\nwant=%s", raw, want)
	}
	if got.Event != ev.Event {
		t.Fatalf("event type changed: got %q want %q", got.Event, ev.Event)
	}

	// And the writer's RawMessage fast-path must equal the old json.Marshal(raw):
	// re-marshaling already-marshaled (compact, escaped) bytes is a no-op.
	reMarshaled, err := json.Marshal(raw)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if string(reMarshaled) != string(raw) {
		t.Fatalf("writer fast-path not equivalent to json.Marshal(RawMessage):\n direct=%s\nmarshaled=%s", raw, reMarshaled)
	}
}

// A frame whose Data is already json.RawMessage (the topology path) must pass
// through untouched — no double-marshal.
func TestPremarshalEventData_RawMessagePassthrough(t *testing.T) {
	raw := json.RawMessage(`{"already":"serialized","n":1}`)
	got := premarshalEventData(SSEEvent{Event: "topology", Data: raw})
	gotRaw, ok := got.Data.(json.RawMessage)
	if !ok {
		t.Fatalf("expected json.RawMessage, got %T", got.Data)
	}
	if string(gotRaw) != string(raw) {
		t.Fatalf("RawMessage should pass through unchanged: got %s want %s", gotRaw, raw)
	}
}
