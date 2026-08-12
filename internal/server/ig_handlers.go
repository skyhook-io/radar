package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"google.golang.org/grpc/codes"
	grpcstatus "google.golang.org/grpc/status"
	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/skyhook-io/radar/internal/auth"
	internalig "github.com/skyhook-io/radar/internal/igdebug"
	"github.com/skyhook-io/radar/internal/k8s"
	domain "github.com/skyhook-io/radar/pkg/igdebug"
)

type igStartRequest struct {
	Namespace       string        `json:"namespace"`
	Pod             string        `json:"pod"`
	Container       string        `json:"container"`
	Aspect          domain.Aspect `json:"aspect"`
	DurationSeconds int           `json:"durationSeconds,omitempty"`
}

type igDebugService interface {
	Status(context.Context) internalig.Status
	Start(context.Context, domain.Request) (*domain.Run, error)
	Get(string, domain.Owner) (*domain.Run, error)
	WaitData(context.Context, string, domain.Owner) (*domain.Run, error)
	Stop(string, domain.Owner) (*domain.Run, error)
	Subscribe(string, domain.Owner) (<-chan domain.StreamEvent, func(), error)
	OnContextSwitch()
}

func (s *Server) handleIGStatus(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	s.writeJSON(w, s.igDebug.Status(ctx))
}

func (s *Server) handleIGSnapshot(w http.ResponseWriter, r *http.Request) {
	s.handleIGCreate(w, r, true)
}

func (s *Server) handleIGStart(w http.ResponseWriter, r *http.Request) {
	s.handleIGCreate(w, r, false)
}

func (s *Server) handleIGCreate(w http.ResponseWriter, r *http.Request, snapshotOnly bool) {
	if !s.requireConnected(w) {
		return
	}
	var body igStartRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10)).Decode(&body); err != nil {
		s.writeError(w, http.StatusBadRequest, "Invalid request body: "+err.Error())
		return
	}
	if body.Namespace == "" || body.Pod == "" || body.Container == "" || !body.Aspect.Valid() {
		s.writeError(w, http.StatusBadRequest, "namespace, pod, container, and a valid aspect are required")
		return
	}
	if snapshotOnly && body.Aspect == domain.AspectDNS {
		s.writeError(w, http.StatusBadRequest, "DNS is a bounded run, not a snapshot")
		return
	}
	duration := time.Duration(body.DurationSeconds) * time.Second
	if body.Aspect == domain.AspectDNS && duration == 0 {
		duration = 10 * time.Second
	}
	request := domain.Request{
		Owner: s.igOwner(r), Aspect: body.Aspect, Duration: duration,
		Target: domain.Target{Namespace: body.Namespace, Pod: body.Pod, Container: body.Container},
	}
	run, err := s.igDebug.Start(r.Context(), request)
	if err != nil {
		s.writeIGError(w, err)
		return
	}
	if body.Aspect == domain.AspectDNS {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		s.writeJSON(w, run)
		return
	}
	fastCtx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	ready, err := s.igDebug.WaitData(fastCtx, run.ID, request.Owner)
	if err == nil {
		s.writeJSON(w, ready)
		return
	}
	if errors.Is(err, context.DeadlineExceeded) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		s.writeJSON(w, run)
		return
	}
	s.writeIGError(w, err)
}

func (s *Server) handleIGGetRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.igDebug.Get(chi.URLParam(r, "id"), s.igOwner(r))
	if err != nil {
		s.writeIGError(w, err)
		return
	}
	s.writeJSON(w, run)
}

func (s *Server) handleIGStopRun(w http.ResponseWriter, r *http.Request) {
	run, err := s.igDebug.Stop(chi.URLParam(r, "id"), s.igOwner(r))
	if err != nil {
		s.writeIGError(w, err)
		return
	}
	s.writeJSON(w, run)
}

func (s *Server) handleIGRunStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		s.writeError(w, http.StatusInternalServerError, "Streaming is not supported")
		return
	}
	events, unsubscribe, err := s.igDebug.Subscribe(chi.URLParam(r, "id"), s.igOwner(r))
	if err != nil {
		s.writeIGError(w, err)
		return
	}
	defer unsubscribe()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case event, open := <-events:
			if !open {
				return
			}
			payload, err := json.Marshal(event)
			if err != nil {
				return
			}
			if _, err := fmt.Fprintf(w, "data: %s\n\n", payload); err != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) igOwner(r *http.Request) domain.Owner {
	username := "local"
	if user := auth.UserFromContext(r.Context()); user != nil {
		username = user.Username
	}
	return domain.Owner{User: username, Context: k8s.GetContextName()}
}

func (s *Server) writeIGError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidRequest):
		s.writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, domain.ErrForbidden), apierrors.IsForbidden(err):
		s.writeError(w, http.StatusForbidden, err.Error())
	case grpcstatus.Code(err) == codes.PermissionDenied || grpcstatus.Code(err) == codes.Unauthenticated:
		s.writeError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, domain.ErrNotFound), apierrors.IsNotFound(err):
		s.writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, domain.ErrCapacity):
		s.writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, internalig.ErrNotInstalled), errors.Is(err, internalig.ErrUnknown):
		s.writeError(w, http.StatusServiceUnavailable, err.Error())
	default:
		log.Printf("[ig] Failed to handle Live Debug request: %v", err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
	}
}
