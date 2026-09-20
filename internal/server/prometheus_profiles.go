package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"maps"
	"net/http"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/k8s"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/internal/traffic"
	"github.com/skyhook-io/radar/pkg/prom"
)

func (s *Server) publishLocalPrometheus(selection prometheuspkg.ProfileSelection, restartTraffic func() error) {
	if k8s.GetConnectionStatus().State != k8s.StateConnected {
		return
	}
	if selection.Err != nil || selection.View.State == "target_changed" {
		previous := prometheuspkg.GetClient()
		prometheuspkg.Retire()
		traffic.SetMetricsConfig("", nil)
		if s.openCostCurrency != nil {
			s.openCostCurrency.Invalidate()
		}
		if previous != nil {
			if err := restartTraffic(); err != nil {
				log.Printf("[traffic] Failed to clear unavailable metrics configuration: %v", err)
			}
		}
		return
	}
	oldURL, oldHeaders := prometheuspkg.CurrentConfig()
	changed := prometheuspkg.GetClient() == nil || oldURL != strings.TrimRight(selection.Connection.URL, "/") || !maps.Equal(oldHeaders, selection.Connection.Headers)
	if !changed {
		return
	}
	if prometheuspkg.GetClient() == nil {
		prometheuspkg.Initialize(k8s.GetClientInterface(), k8s.GetConfig(), k8s.GetContextName())
	}
	prometheuspkg.Configure(selection.Connection.URL, selection.Connection.Headers)
	traffic.SetMetricsConfig(selection.Connection.URL, selection.Connection.Headers)
	if err := restartTraffic(); err != nil {
		log.Printf("[traffic] Failed to refresh cluster metrics configuration: %v", err)
	}
	if s.openCostCurrency != nil {
		s.openCostCurrency.Invalidate()
	}
}

func (s *Server) localPrometheusView() prometheuspkg.ProfileView {
	s.prometheusConfigMu.Lock()
	defer s.prometheusConfigMu.Unlock()
	view := prometheuspkg.ProfileView{State: "error", HeaderKeys: []string{}}
	err := k8s.TryClusterConfiguration(func(restart func() error) error {
		target, err := k8s.CurrentProfileTarget()
		view.Target = target
		if err != nil {
			return err
		}
		selection := s.prometheusProfiles.Resolve(target, true)
		s.publishLocalPrometheus(selection, restart)
		view = selection.View
		return nil
	})
	if err != nil {
		view.Error = err.Error()
	}
	return view
}

type localPrometheusUpdate struct {
	Target         k8s.ProfileTarget  `json:"target"`
	Revision       string             `json:"revision"`
	PrometheusURL  string             `json:"prometheusUrl"`
	Headers        *map[string]string `json:"headers"`
	Action         string             `json:"action"`
	LegacyRevision string             `json:"legacyRevision"`
}

func (s *Server) handleApplyLocalPrometheus(w http.ResponseWriter, r *http.Request) {
	var body localPrometheusUpdate
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256*1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || dec.Decode(new(any)) != io.EOF {
		s.writeError(w, 400, "invalid cluster settings request")
		return
	}
	if body.Action == "" {
		body.Action = "apply"
	}
	if body.Action != "apply" && body.Action != "replace" && body.Action != "adopt" && body.Action != "reconfirm" && body.Action != "finish_migration" {
		s.writeError(w, 400, "unknown cluster settings action")
		return
	}
	body.PrometheusURL = strings.TrimSpace(body.PrometheusURL)
	if body.Action == "apply" || body.Action == "replace" {
		if err := prom.ValidateBaseURL(body.PrometheusURL); err != nil {
			s.writeError(w, 400, err.Error())
			return
		}
	}
	var selection prometheuspkg.ProfileSelection
	var probeClient *prometheuspkg.Client
	status := http.StatusConflict
	s.prometheusConfigMu.Lock()
	err := k8s.TryClusterConfiguration(func(restart func() error) error {
		target, err := k8s.CurrentProfileTarget()
		if err != nil {
			return err
		}
		if !target.Same(body.Target) {
			return errors.New("cluster connection changed; reload Settings before saving")
		}
		current := s.prometheusProfiles.Resolve(target, false)
		if current.View.State == "launch" {
			return errors.New("Prometheus is set for this launch; restart without the Prometheus flags to edit saved settings")
		}
		status = http.StatusInternalServerError
		err = s.prometheusProfiles.Save(r.Context(), current, body.Revision, func(file *config.ClusterProfiles) error {
			status = http.StatusBadRequest
			if body.Action == "finish_migration" {
				file.PrometheusMigrationComplete = true
				status = http.StatusInternalServerError
				return nil
			}
			profile, exists := file.Profiles[target.Binding]
			switch body.Action {
			case "adopt":
				if exists || file.PrometheusAdopted[target.Binding] || file.PrometheusMigrationComplete {
					return errors.New("legacy settings are no longer available for this cluster; reload Settings")
				}
				legacy, err := s.prometheusProfiles.LegacyForAdoption(body.LegacyRevision)
				if err != nil {
					return err
				}
				profile.Prometheus = legacy
			case "reconfirm":
				if !exists || profile.Target == target.Fingerprint {
					return errors.New("no changed target to confirm; reload Settings")
				}
			case "replace":
				if !exists || profile.Target == target.Fingerprint || body.Headers == nil {
					return errors.New("replacing a changed target requires a complete connection, including headers")
				}
				profile.Prometheus = prom.Connection{URL: body.PrometheusURL, Headers: maps.Clone(*body.Headers)}
			case "apply":
				if exists && profile.Target != target.Fingerprint {
					return errors.New("confirm the changed Kubernetes target before editing its connection")
				}
				if body.Headers != nil && len(profile.Prometheus.HeadersFromEnv) > 0 {
					return errors.New("headers come from environment references; edit headersFromEnv in clusters.json")
				}
				if body.Headers == nil && len(profile.Prometheus.Headers)+len(profile.Prometheus.HeadersFromEnv) > 0 && !sameIntegrationOrigin(body.PrometheusURL, profile.Prometheus.URL) {
					if len(profile.Prometheus.HeadersFromEnv) > 0 {
						return errors.New("changing servers with environment-backed headers requires editing the URL and headersFromEnv together in clusters.json, then reloading Settings")
					}
					return errors.New("changing servers requires replacing or clearing the saved headers")
				}
				profile.Prometheus.URL = body.PrometheusURL
				if body.Headers != nil {
					profile.Prometheus.Headers = maps.Clone(*body.Headers)
				}
			}
			if err := profile.Prometheus.Validate(); err != nil {
				return err
			}
			profile.Context = target.Context
			profile.Source = target.Source
			profile.Target = target.Fingerprint
			file.Profiles[target.Binding] = profile
			if file.PrometheusAdopted == nil {
				file.PrometheusAdopted = map[string]bool{}
			}
			file.PrometheusAdopted[target.Binding] = true
			status = http.StatusInternalServerError
			return nil
		})
		if err != nil {
			return err
		}
		selection = s.prometheusProfiles.Resolve(target, true)
		s.publishLocalPrometheus(selection, restart)
		probeClient = prometheuspkg.GetClient()
		return nil
	})
	s.prometheusConfigMu.Unlock()
	if err != nil {
		if errors.Is(err, config.ErrProfileConflict) || errors.Is(err, config.ErrProfileBusy) || errors.Is(err, k8s.ErrContextConfigurationBusy) {
			status = http.StatusConflict
		} else if errors.Is(err, config.ErrProfileInvalid) {
			status = http.StatusBadRequest
		}
		if status == http.StatusInternalServerError {
			log.Printf("[prometheus] Failed to save cluster connection %s: %v", body.Target.Context, err)
		}
		s.writeError(w, status, err.Error())
		return
	}
	response := struct {
		Profile   prometheuspkg.ProfileView `json:"profile"`
		Connected bool                      `json:"connected"`
		Address   string                    `json:"address,omitempty"`
		Error     string                    `json:"error,omitempty"`
	}{Profile: selection.View}
	if probeClient != nil && body.Action != "finish_migration" {
		ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
		defer cancel()
		address, _, err := probeClient.EnsureConnected(ctx)
		if err != nil {
			response.Error = prom.RedactURLs(err.Error())
		} else {
			response.Connected = true
			response.Address = prom.SafeAddress(address)
		}
		if prometheuspkg.GetClient() != probeClient || !s.prometheusProfiles.IsCurrent(selection) {
			response.Connected = false
			response.Address = ""
			response.Error = "Settings changed during the connection check; reload Settings for the current connection"
		}
	} else {
		response.Error = selection.View.Error
		if response.Error == "" && body.Action != "finish_migration" {
			response.Error = "Saved; connect to Kubernetes to check this backend"
		}
	}
	s.writeJSON(w, response)
}
