package server

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sort"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/openobserve"
	"github.com/skyhook-io/radar/pkg/prom"
)

// Local Radar's OpenObserve setup (Settings → Metrics) edits the one shared
// connection in ~/.radar/config.json and the current cluster's match.
//
// Only local config management serves these routes: in-cluster, operator-
// managed and Cloud Radar answer 403. Local management already means
// authentication is off; on a shared listener without authentication (local
// Radar on a non-loopback address) GET still answers, since it never carries
// the token, but the writes are refused: they store a credential on Radar's
// host, read a CA file from its disk and keep sending requests to an address
// the caller chose, which is more than Radar's Kubernetes identity.

const (
	openObserveSetupBodyLimit = 64 * 1024
	// The route group times out at 60s; answer with OpenObserve's own failure
	// before that.
	openObserveSetupTimeout = 55 * time.Second
	openObserveUIDTimeout   = 5 * time.Second

	openObserveClusterChanged = "Radar switched to another cluster since this page loaded. Nothing was changed; reopen Settings and try again."
)

// openObserveKubeClient is the current context's client; a variable for tests.
var openObserveKubeClient = func() kubernetes.Interface {
	conn := k8s.Current()
	if conn == nil {
		return nil
	}
	return conn.ClientInterface()
}

type openObserveSetupView struct {
	Connection *openObserveConnectionView `json:"connection"`
	Cluster    openObserveClusterState    `json:"cluster"`
}

type openObserveConnectionView struct {
	BaseURL      string                     `json:"baseUrl"`
	Org          string                     `json:"org"`
	User         string                     `json:"user"`
	TokenSource  string                     `json:"tokenSource"`
	TokenEnv     string                     `json:"tokenEnv,omitempty"`
	CACertFile   string                     `json:"caCertFile,omitempty"`
	ClusterLabel string                     `json:"clusterLabel,omitempty"`
	Clusters     []openObserveMatchedTarget `json:"clusters"`
}

type openObserveMatchedTarget struct {
	Context string `json:"context"`
	By      string `json:"by"`
}

type openObserveClusterState struct {
	UID   string                 `json:"uid,omitempty"`
	Error string                 `json:"error,omitempty"`
	Match *openobserve.MatchView `json:"match,omitempty"`
}

func (s *Server) openObserveLocal() bool {
	return s.configManagement() == "local" && !k8s.IsInCluster() && s.integrationRuntime != nil
}

// hubOffersOpenObserve is cloud.HubOffersOpenObserve; a variable for tests.
var hubOffersOpenObserve = cloud.HubOffersOpenObserve

// openObservePath says how this install can get OpenObserve metrics. A Hub
// connection makes configuration management "cloud", so the two never hold
// together; Hub is checked first because its choice is what the agent applies.
// A Hub without OpenObserve offers no path, so its clusters keep the
// Prometheus setup screens.
func (s *Server) openObservePath() string {
	switch {
	case hubOffersOpenObserve():
		return "hub"
	case s.openObserveLocal():
		return "local"
	}
	return ""
}

func (s *Server) requireOpenObserveLocal(w http.ResponseWriter) bool {
	if !s.openObserveLocal() {
		s.writeError(w, http.StatusForbidden, "OpenObserve setup is available only in local CLI and Desktop Radar. Radar running in a cluster reads metrics from the Prometheus its operator configures.")
		return false
	}
	return true
}

func (s *Server) requireOpenObserveWrite(w http.ResponseWriter, r *http.Request) bool {
	if !s.requireConfigEditable(w, r) || !s.requireCloudRole(w, r, auth.RoleOwner, "change the OpenObserve connection") || !s.requireOpenObserveLocal(w) {
		return false
	}
	if s.sharedListener() {
		s.writeError(w, http.StatusForbidden, "OpenObserve setup is available only when Radar listens on this machine alone. Restart Radar without a shared --listen-address, or use radar openobserve connect.")
		return false
	}
	return true
}

func openObserveTarget(target k8s.ProfileTarget) openobserve.Target {
	return openobserve.Target{Context: target.Context, Fingerprint: target.Fingerprint, Client: openObserveKubeClient()}
}

// captureClusterReads is k8s.CaptureClusterReads; tests stand in a context
// switch in progress.
var captureClusterReads = k8s.CaptureClusterReads

// currentOpenObserveTarget reads the current context and its Kubernetes
// client together, while no context switch can run. Read apart, a switch
// between them would pair one cluster's identity with another's client, and
// the activator would remember the second cluster's ID under the first one's
// fingerprint, so the first would use the second's match and metrics.
func currentOpenObserveTarget() (k8s.ProfileTarget, openobserve.Target, error) {
	var target k8s.ProfileTarget
	var t openobserve.Target
	var err error
	if !captureClusterReads(func() {
		target, err = k8s.CurrentProfileTarget()
		if err == nil {
			t = openObserveTarget(target)
		}
	}) {
		return k8s.ProfileTarget{}, openobserve.Target{}, k8s.ErrContextConfigurationBusy
	}
	return target, t, err
}

func (s *Server) openObserveView(ctx context.Context) openObserveSetupView {
	view := openObserveSetupView{}
	cfg := config.Load().OpenObserve
	if cfg != nil {
		c := &openObserveConnectionView{
			BaseURL:      cfg.BaseURL,
			Org:          cfg.Org,
			User:         cfg.User,
			TokenSource:  "none",
			TokenEnv:     cfg.TokenEnv,
			CACertFile:   cfg.CACertFile,
			ClusterLabel: cfg.ClusterLabel,
			Clusters:     []openObserveMatchedTarget{},
		}
		switch {
		case cfg.TokenEnv != "":
			c.TokenSource = "env"
		case cfg.Token != "":
			c.TokenSource = "saved"
		}
		for _, m := range cfg.Matches {
			c.Clusters = append(c.Clusters, openObserveMatchedTarget{Context: m.Context, By: m.By})
		}
		sort.Slice(c.Clusters, func(i, j int) bool {
			if c.Clusters[i].Context != c.Clusters[j].Context {
				return c.Clusters[i].Context < c.Clusters[j].Context
			}
			return c.Clusters[i].By < c.Clusters[j].By
		})
		view.Connection = c
	}
	_, t, err := currentOpenObserveTarget()
	if err != nil {
		view.Cluster.Error = openObserveTargetError(err).Error()
		return view
	}
	uid, err := s.openObserveClusterUID(ctx, t)
	if err != nil {
		view.Cluster.Error = err.Error()
		return view
	}
	view.Cluster.UID = uid
	if cfg != nil {
		if m, ok := cfg.Matches[uid]; ok {
			view.Cluster.Match = &openobserve.MatchView{Label: m.Label, Value: m.Value, By: m.By}
		}
	}
	return view
}

func openObserveTargetError(err error) error {
	if errors.Is(err, k8s.ErrContextConfigurationBusy) {
		return errors.New("The current cluster is changing. Try again in a moment.")
	}
	return errors.New("Radar couldn't tell which cluster is current: " + prom.RedactURLs(err.Error()))
}

func (s *Server) openObserveClusterUID(ctx context.Context, t openobserve.Target) (string, error) {
	if t.Client == nil {
		return "", errors.New("Radar isn't connected to this cluster yet.")
	}
	ctx, cancel := context.WithTimeout(ctx, openObserveUIDTimeout)
	defer cancel()
	uid, err := openobserve.ReadClusterUID(ctx, t.Client)
	if err != nil {
		return "", errors.New("Radar couldn't read this cluster's ID: " + prom.RedactURLs(err.Error()))
	}
	return uid, nil
}

func (s *Server) handleOpenObserveSetupView(w http.ResponseWriter, r *http.Request) {
	if !s.requireOpenObserveLocal(w) {
		return
	}
	s.writeJSON(w, s.openObserveView(r.Context()))
}

func (s *Server) handleOpenObserveSetup(w http.ResponseWriter, r *http.Request) {
	if !s.requireOpenObserveWrite(w, r) {
		return
	}
	var req openobserve.SetupRequest
	if err := decodeBoundedJSONBody(w, r, openObserveSetupBodyLimit, &req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.writeError(w, http.StatusRequestEntityTooLarge, "The OpenObserve setup request is too large.")
			return
		}
		s.writeError(w, http.StatusBadRequest, "The OpenObserve setup request isn't valid.")
		return
	}
	target, t, err := currentOpenObserveTarget()
	if err != nil {
		s.connectionError(w, err)
		return
	}
	if t.Client == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Radar isn't connected to this cluster yet.")
		return
	}
	uid, err := s.openObserveClusterUID(r.Context(), t)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.ClusterUID != "" && req.ClusterUID != uid {
		s.writeError(w, http.StatusConflict, openObserveClusterChanged)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), openObserveSetupTimeout)
	defer cancel()
	result, err := openobserve.Setup(ctx, openobserve.SetupDeps{
		Existing:    config.Load().OpenObserve,
		ContextName: target.Context,
		UID:         uid,
		Kube:        t.Client,
		Save: func(update func(*config.Config)) error {
			return k8s.TryClusterConfiguration(func() error {
				current, err := k8s.CurrentProfileTarget()
				if err != nil || !current.Same(target) {
					return config.ErrProfileConflict
				}
				_, err = config.Update(update)
				return err
			})
		},
	}, req)
	if err != nil {
		var setupErr *openobserve.SetupError
		if errors.As(err, &setupErr) {
			log.Printf("[openobserve] Setup for context %q failed: %s", sanitizeForLog(target.Context), sanitizeForLog(setupErr.Message))
			s.writeError(w, http.StatusBadRequest, setupErr.Message)
			return
		}
		if errors.Is(err, config.ErrProfileConflict) {
			s.writeError(w, http.StatusConflict, "The current cluster changed during the check. Nothing was saved; try again.")
			return
		}
		s.connectionError(w, err)
		return
	}
	if result.Outcome == openobserve.SetupSaved {
		s.integrationRuntime.RefreshProfiles()
	}
	s.writeJSON(w, result)
}

func (s *Server) handleOpenObserveRemoveCluster(w http.ResponseWriter, r *http.Request) {
	if !s.requireOpenObserveWrite(w, r) {
		return
	}
	target, t, err := currentOpenObserveTarget()
	if err != nil {
		s.connectionError(w, err)
		return
	}
	uid, err := s.openObserveClusterUID(r.Context(), t)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if shown := r.URL.Query().Get("clusterUid"); shown != "" && shown != uid {
		s.writeError(w, http.StatusConflict, openObserveClusterChanged)
		return
	}
	err = k8s.TryClusterConfiguration(func() error {
		current, err := k8s.CurrentProfileTarget()
		if err != nil || !current.Same(target) {
			return config.ErrProfileConflict
		}
		_, err = config.Update(func(c *config.Config) {
			if c.OpenObserve != nil {
				delete(c.OpenObserve.Matches, uid)
			}
		})
		return err
	})
	if err != nil {
		s.connectionError(w, err)
		return
	}
	s.integrationRuntime.RefreshProfiles()
	s.writeJSON(w, s.openObserveView(r.Context()))
}

func (s *Server) handleOpenObserveRemoveAll(w http.ResponseWriter, r *http.Request) {
	if !s.requireOpenObserveWrite(w, r) {
		return
	}
	err := k8s.TryClusterConfiguration(func() error {
		_, err := config.Update(func(c *config.Config) { c.OpenObserve = nil })
		return err
	})
	if err != nil {
		s.connectionError(w, err)
		return
	}
	s.integrationRuntime.RefreshProfiles()
	s.writeJSON(w, s.openObserveView(r.Context()))
}
