package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	topology "github.com/skyhook-io/radar/pkg/topology"
)

type lockedStream struct {
	mu     sync.Mutex
	header http.Header
	buf    strings.Builder
}

func (s *lockedStream) Header() http.Header { return s.header }
func (s *lockedStream) WriteHeader(int)     {}
func (s *lockedStream) Flush()              {}
func (s *lockedStream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}
func (s *lockedStream) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// A broadcast cycle runs on any change in the cluster, so most topology frames
// repeat the graph a client already holds, in a different node and edge order.
// The writer drops those, and starts over after context_changed, when the
// client discards its graph and waits for the next frame.
func TestHandleSSESkipsRepeatedTopologyFrames(t *testing.T) {
	b := NewSSEBroadcaster()
	stream := &lockedStream{header: http.Header{}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		req := httptest.NewRequest(http.MethodGet, "/api/events/stream", nil).WithContext(ctx)
		b.HandleSSE(stream, req, nil, nil)
	}()

	// The broadcaster's run loop isn't started, so no broadcast cycle can
	// interleave its own frames: the test plays the loop's part.
	var ch chan SSEEvent
	select {
	case reg := <-b.register:
		ch = reg.ch
	case <-time.After(2 * time.Second):
		t.Fatal("client never subscribed")
	}
	go func() { <-b.unregister }()

	// The test cluster is connected, so the handler opens with a frame of its
	// own; the first broadcast repeating it must be skipped too.
	initialDeadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(stream.String(), "event: topology\n") {
		if time.Now().After(initialDeadline) {
			t.Fatal("no initial topology frame")
		}
		time.Sleep(5 * time.Millisecond)
	}
	initial := strings.TrimSuffix(strings.SplitN(stream.String(), "event: topology\ndata: ", 2)[1], "\n\n")
	prefix := stream.String()

	reordered := func(raw string) *topology.Topology {
		var topo topology.Topology
		if err := json.Unmarshal([]byte(raw), &topo); err != nil {
			t.Fatal(err)
		}
		slices.Reverse(topo.Nodes)
		slices.Reverse(topo.Edges)
		return &topo
	}
	event := func(topo *topology.Topology) SSEEvent {
		e, err := newTopologyEvent(topo)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	changed := reordered(initial)
	if len(changed.Nodes) < 2 {
		t.Fatalf("test cluster topology too small to reorder: %s", initial)
	}
	changed.Nodes[0].Status = "changed-for-test"
	changedFrame := event(changed)
	frame := func(name, data string) SSEEvent { return SSEEvent{Event: name, Data: json.RawMessage(data)} }

	for _, e := range []SSEEvent{
		event(reordered(initial)), // same graph as the opening frame, other order
		frame("k8s_event", `{"name":"x"}`),
		changedFrame,
		event(reordered(string(changedFrame.Data.(json.RawMessage)))),
		frame("context_changed", `{"context":"other"}`),
		changedFrame,
		frame("heartbeat", `{}`),
	} {
		ch <- e
	}

	changedData := string(changedFrame.Data.(json.RawMessage))
	want := strings.Join([]string{
		"event: k8s_event\ndata: {\"name\":\"x\"}",
		"event: topology\ndata: " + changedData,
		"event: context_changed\ndata: {\"context\":\"other\"}",
		"event: topology\ndata: " + changedData,
		"event: heartbeat\ndata: {}",
	}, "\n\n") + "\n\n"
	deadline := time.Now().Add(2 * time.Second)
	for !strings.HasSuffix(stream.String(), "event: heartbeat\ndata: {}\n\n") {
		if time.Now().After(deadline) {
			t.Fatalf("stream never reached the last frame:\n%s", stream.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done

	got := strings.TrimPrefix(stream.String(), prefix)
	if got != want {
		t.Errorf("frames written:\n%s\nwant:\n%s", got, want)
	}
}
