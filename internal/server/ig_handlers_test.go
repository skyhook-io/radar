package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/skyhook-io/radar/internal/auth"
	internalig "github.com/skyhook-io/radar/internal/igdebug"
	"github.com/skyhook-io/radar/internal/k8s"
	domain "github.com/skyhook-io/radar/pkg/igdebug"
)

type fakeIGDebugService struct {
	startRun   *domain.Run
	startErr   error
	waitRun    *domain.Run
	waitErr    error
	startInput domain.Request
	stream     <-chan domain.StreamEvent
	subOwner   domain.Owner
}

func (f *fakeIGDebugService) Status(context.Context) internalig.Status {
	return internalig.Status{State: internalig.StateInstalled, Namespace: "gadget"}
}

func (f *fakeIGDebugService) Start(_ context.Context, request domain.Request) (*domain.Run, error) {
	f.startInput = request
	return f.startRun, f.startErr
}

func (f *fakeIGDebugService) Get(string, domain.Owner) (*domain.Run, error) {
	return f.waitRun, f.waitErr
}

func (f *fakeIGDebugService) WaitData(context.Context, string, domain.Owner) (*domain.Run, error) {
	return f.waitRun, f.waitErr
}

func (f *fakeIGDebugService) Stop(string, domain.Owner) (*domain.Run, error) {
	return f.waitRun, f.waitErr
}

func (f *fakeIGDebugService) Subscribe(_ string, owner domain.Owner) (<-chan domain.StreamEvent, func(), error) {
	f.subOwner = owner
	return f.stream, func() {}, f.waitErr
}

func (f *fakeIGDebugService) OnContextSwitch() {}

func TestIGSnapshotReturnsDataReadyResultAndForwardsOwner(t *testing.T) {
	connectedForIGTest(t)
	ready := &domain.Run{ID: "run-1", Aspect: domain.AspectProcesses, State: domain.StateStopping, Result: &domain.Result{Aspect: domain.AspectProcesses}}
	fake := &fakeIGDebugService{startRun: &domain.Run{ID: "run-1", State: domain.StateStarting}, waitRun: ready}
	s := &Server{igDebug: fake}
	req := httptest.NewRequest(http.MethodPost, "/api/ig/snapshots", strings.NewReader(`{"namespace":"app","pod":"api","container":"api","aspect":"processes"}`))
	req = req.WithContext(auth.ContextWithUser(req.Context(), &auth.User{Username: "alice"}))
	recorder := httptest.NewRecorder()

	s.handleIGSnapshot(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if fake.startInput.Owner.User != "alice" || fake.startInput.Target.Pod != "api" {
		t.Fatalf("unexpected request: %#v", fake.startInput)
	}
}

func TestIGSnapshotColdStartReturnsJSONAccepted(t *testing.T) {
	connectedForIGTest(t)
	fake := &fakeIGDebugService{
		startRun: &domain.Run{ID: "run-1", State: domain.StateStarting},
		waitErr:  context.DeadlineExceeded,
	}
	s := &Server{igDebug: fake}
	req := httptest.NewRequest(http.MethodPost, "/api/ig/snapshots", strings.NewReader(`{"namespace":"app","pod":"api","container":"api","aspect":"connections"}`))
	recorder := httptest.NewRecorder()

	s.handleIGSnapshot(recorder, req)

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); contentType != "application/json" {
		t.Fatalf("Content-Type = %q", contentType)
	}
}

func TestIGSnapshotMapsProxyDenialToForbidden(t *testing.T) {
	connectedForIGTest(t)
	fake := &fakeIGDebugService{
		startRun: &domain.Run{ID: "run-1", State: domain.StateStarting},
		waitErr:  status.Error(codes.PermissionDenied, "pods/portforward is forbidden"),
	}
	s := &Server{igDebug: fake}
	req := httptest.NewRequest(http.MethodPost, "/api/ig/snapshots", strings.NewReader(`{"namespace":"app","pod":"api","container":"api","aspect":"processes"}`))
	recorder := httptest.NewRecorder()

	s.handleIGSnapshot(recorder, req)

	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}

func TestIGRunStreamWritesBufferedEventsAndForwardsOwner(t *testing.T) {
	events := make(chan domain.StreamEvent, 2)
	events <- domain.StreamEvent{Type: "state", Run: &domain.Run{ID: "run-1", State: domain.StateRunning}}
	events <- domain.StreamEvent{Type: "result", Run: &domain.Run{ID: "run-1", State: domain.StateComplete}}
	close(events)
	fake := &fakeIGDebugService{stream: events}
	s := &Server{igDebug: fake}
	req := httptest.NewRequest(http.MethodGet, "/api/ig/runs/run-1/stream", nil)
	req = req.WithContext(auth.ContextWithUser(req.Context(), &auth.User{Username: "alice"}))
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", "run-1")
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	recorder := httptest.NewRecorder()

	s.handleIGRunStream(recorder, req)

	if contentType := recorder.Header().Get("Content-Type"); contentType != "text/event-stream" {
		t.Fatalf("Content-Type = %q", contentType)
	}
	if body := recorder.Body.String(); !strings.Contains(body, `"type":"state"`) || !strings.Contains(body, `"type":"result"`) {
		t.Fatalf("stream body = %q", body)
	}
	if fake.subOwner.User != "alice" {
		t.Fatalf("owner = %#v", fake.subOwner)
	}
}

func connectedForIGTest(t *testing.T) {
	t.Helper()
	previous := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	t.Cleanup(func() { k8s.SetConnectionStatus(previous) })
}
