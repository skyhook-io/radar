package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/skyhook-io/radar/internal/argocd"
	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/connectionruntime"
	"github.com/skyhook-io/radar/internal/connections"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/opencost"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/pkg/prom"
)

type localConnectionResponse struct {
	Profiles    map[config.Integration]connections.ProfileView `json:"profiles"`
	Connections []connections.StoredSettingsView               `json:"connections"`
	Connected   bool                                           `json:"connected"`
	Checked     bool                                           `json:"checked"`
	Error       string                                         `json:"error,omitempty"`
}

func (s *Server) requireLocalConnections(w http.ResponseWriter) bool {
	if s.configManagement() != "local" || s.localConnections == nil || s.localRuntime == nil {
		s.writeError(w, http.StatusForbidden, "Saved connections are available only in local CLI and Desktop Radar. Change this installation's integrations through its operator configuration.")
		return false
	}
	return true
}

func (s *Server) localConnectionViews(target k8s.ProfileTarget) map[config.Integration]connections.ProfileView {
	views := map[config.Integration]connections.ProfileView{}
	for kind, selection := range s.localConnections.ResolveAll(target, true) {
		views[kind] = selection.View
	}
	return views
}

func (s *Server) readLocalConnectionViews() map[config.Integration]connections.ProfileView {
	target, err := k8s.CurrentProfileTarget()
	views := s.localConnectionViews(target)
	if err != nil {
		for kind, view := range views {
			view.State, view.Error, view.Legacy = "error", err.Error(), nil
			views[kind] = view
		}
	}
	return views
}

func (s *Server) fillConnectionCatalog(response *localConnectionResponse) error {
	var err error
	response.Connections, err = s.localConnections.Catalog()
	return err
}

func (s *Server) handleLocalConnections(w http.ResponseWriter, r *http.Request) {
	if !s.requireLocalConnections(w) {
		return
	}
	response := localConnectionResponse{Profiles: s.readLocalConnectionViews()}
	if err := s.fillConnectionCatalog(&response); err != nil {
		s.connectionError(w, err)
		return
	}
	s.writeJSON(w, response)
}

func (s *Server) handleUpdateLocalConnection(w http.ResponseWriter, r *http.Request) {
	if !s.requireLocalConnections(w) {
		return
	}
	var request connections.Update
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil || decoder.Decode(new(any)) != io.EOF {
		s.writeError(w, 400, "invalid saved connection request")
		return
	}
	metadata := request.Action == "forget"
	response := localConnectionResponse{}
	var pending connections.Pending
	var probe func(context.Context) error
	prepare := func(func() error) error {
		target, err := k8s.CurrentProfileTarget()
		if err != nil && !metadata {
			return err
		}
		pending, err = s.localConnections.Prepare(target, request)
		if err != nil {
			return err
		}
		if !pending.Probe {
			return nil
		}
		switch request.Kind {
		case config.IntegrationArgoCD:
			probe = argocd.PrepareCandidate(*pending.Candidate.Settings.ArgoCD)
		case config.IntegrationCost:
			probe, err = opencost.PrepareCandidate(connectionruntime.CostConfig(connections.Selection{Bundle: pending.Candidate}, target))
		}
		return err
	}
	var err error
	if metadata {
		err = prepare(nil)
	} else {
		err = k8s.TryClusterConfiguration(prepare)
	}
	if err != nil {
		s.connectionError(w, err)
		return
	}
	if probe != nil {
		timeout := 12 * time.Second
		if request.Kind == config.IntegrationCost {
			timeout = costSourceApplyTimeout
		}
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		err = probe(ctx)
		cancel()
		if err != nil {
			message := "Argo CD connection check failed; check its URL, network access and TLS settings. The previous connection is unchanged"
			if request.Kind == config.IntegrationCost {
				message = kubecostConnectionGuidance(err, pending.Candidate.Settings.Kubecost.APIKey != "")
			}
			if errors.Is(err, argocd.ErrTokenInvalid) {
				message = "Argo CD rejected the token; the previous connection is unchanged"
			}
			s.connectionError(w, errors.New(message))
			return
		}
		response.Connected, response.Checked = true, true
	}
	var selection connections.Selection
	var probeClient *prometheuspkg.Client
	commit := func(func() error) error {
		target, targetErr := k8s.CurrentProfileTarget()
		if !metadata && (targetErr != nil || !target.Same(pending.Target)) {
			return config.ErrProfileConflict
		}
		if err := s.localConnections.Commit(r.Context(), pending); err != nil {
			return err
		}
		if !metadata {
			s.localRuntime.Apply(target, false)
			selection = s.localConnections.Resolve(target, request.Kind, false)
			if pending.Probe && request.Kind == config.IntegrationMetrics {
				probeClient = prometheuspkg.GetClient()
			}
		}
		return nil
	}
	if metadata {
		err = commit(nil)
	} else {
		err = k8s.TryClusterConfiguration(commit)
	}
	if err != nil {
		s.connectionError(w, err)
		return
	}
	response.Profiles = s.readLocalConnectionViews()
	if err := s.fillConnectionCatalog(&response); err != nil {
		s.connectionError(w, err)
		return
	}
	if probeClient != nil {
		response.Checked = true
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		_, _, err := probeClient.EnsureConnected(ctx)
		response.Connected = err == nil
		if err != nil {
			response.Error = "Saved, but the metrics backend could not be reached. Check its URL, authentication, network access and TLS configuration."
		}
		if prometheuspkg.GetClient() != probeClient || s.localRuntime.CheckCurrent(selection.View.Target, selection) != nil {
			response.Connected = false
			response.Error = "Settings changed during the connection check; reload Settings for the current connection"
		}
	} else if selection.Err != nil {
		response.Error = selection.View.Error
	}
	s.writeJSON(w, response)
}

func (s *Server) connectionError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	if errors.Is(err, config.ErrProfileConflict) || errors.Is(err, config.ErrProfileBusy) || errors.Is(err, k8s.ErrContextConfigurationBusy) {
		status = http.StatusConflict
	}
	var pathError *os.PathError
	if errors.As(err, &pathError) {
		status = http.StatusInternalServerError
		log.Printf("[connections] Failed to access local settings: %v", prom.RedactURLs(err.Error()))
	}
	s.writeError(w, status, prom.RedactURLs(err.Error()))
}
