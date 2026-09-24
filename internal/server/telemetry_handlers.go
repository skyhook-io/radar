package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/opencost"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/internal/telemetry"
	"github.com/skyhook-io/radar/internal/version"
	"github.com/skyhook-io/radar/pkg/packages"
)

func (s *Server) startTelemetry() {
	ctx, cancel := context.WithCancel(context.Background())
	s.stopTelemetry = cancel
	opts := telemetry.Options{
		// Radar Cloud runs its own usage program; the OSS switch stays off.
		Hosted: func() bool {
			return cloudMode() || s.cloudConnectCfg.CloudTunnelConfigured
		},
		Shared: s.usageShared,
		Mode: func() string {
			switch deploymentMode() {
			case k8s.DeploymentModeInCluster:
				return "in-cluster"
			case k8s.DeploymentModeCloud:
				return "cloud"
			}
			if version.IsDesktop() {
				return "desktop"
			}
			return "local"
		},
		ClusterSample: clusterSample,
		// The pending report lives on the pod's emptyDir; send it before
		// the pod goes rather than lose it.
		SendOnShutdown: deploymentMode() == k8s.DeploymentModeInCluster,
		Setup:          s.telemetrySetup,
		Contexts:       func() int { return k8s.GetKubeconfigSummary().ContextCount },
		// In-cluster, ~/.radar is a pod's emptyDir and is new on every
		// restart; the Deployment's creation time is the real install time.
		InstalledAt: func() int64 {
			if deploymentMode() == k8s.DeploymentModeInCluster {
				return k8s.InstalledAt(context.Background())
			}
			return version.LocalInstalledAt()
		},
	}
	// In-cluster, the team's choice lives in the chart's ConfigMap so a
	// restart doesn't forget it. Without one (an older chart) it lives in
	// the pod, and Settings says a restart resets it.
	if deploymentMode() == k8s.DeploymentModeInCluster {
		if store := newConfigMapUsageStore(); store != nil {
			opts.Load, opts.Save, opts.StorageScope = store.Load, store.Save, store.Scope
		} else {
			opts.StorageScope = func() string { return "pod" }
		}
	}
	telemetry.Start(ctx, opts)
}

// usageShared reports whether several people use this Radar: in-cluster,
// behind sign-in, or on a listener others can reach. The choice then covers
// everyone, so nobody may make it just by opening the UI; see
// canDecideUsage.
func (s *Server) usageShared() bool {
	return deploymentMode() != k8s.DeploymentModeLocal ||
		s.configManagement() != "local" ||
		!cloud.IsLoopbackHostname(s.listenAddress)
}

// usageOwnersDecide reports whether a shared install lets its owners answer
// in the UI. That needs to know who is asking, so it takes an in-cluster
// Radar with sign-in; any other shared Radar is decided only by the Helm
// value or RADAR_USAGE_REPORTING.
func (s *Server) usageOwnersDecide() bool {
	return deploymentMode() == k8s.DeploymentModeInCluster && s.authConfig.Enabled()
}

// canDecideUsage reports whether the caller may answer the usage-data
// question for this Radar. On a shared install that is someone who could
// change the Radar Deployment anyway (patch Deployments in its namespace), so
// a viewer can't opt the whole team in.
func (s *Server) canDecideUsage(r *http.Request) bool {
	return usageDecider(s.usageShared(), s.usageOwnersDecide(), auth.UserFromContext(r.Context()) != nil, func() bool {
		namespace := os.Getenv("MY_POD_NAMESPACE")
		return namespace != "" && s.canRead(r, "apps", "deployments", namespace, "patch")
	})
}

func usageDecider(shared, ownersDecide, signedIn bool, mayPatchDeployment func() bool) bool {
	if !shared {
		return true
	}
	// canRead allows everything when nobody is signed in, so an unknown
	// caller must be refused here rather than by the access review.
	return ownersDecide && signedIn && mayPatchDeployment()
}

// usageStatusFor narrows the install-wide status to what this caller may do.
func (s *Server) usageStatusFor(r *http.Request, st telemetry.Status) telemetry.Status {
	st.OwnersDecide = st.Shared && s.usageOwnersDecide()
	if !s.canDecideUsage(r) {
		st.CanChange = false
		st.FirstRunPrompt = false
		st.Ask = false
	}
	return st
}

// clusterSample describes the connected cluster by shape only. The key is the
// kube-system namespace UID (stable per cluster, meaningless outside it); the
// collector hashes it before storing and never sends it.
func clusterSample() (string, telemetry.ClusterShape, bool) {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return "", telemetry.ClusterShape{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	info, err := k8s.GetClusterInfo(ctx)
	if err != nil {
		return "", telemetry.ClusterShape{}, false
	}
	key := ""
	if nsLister := cache.Namespaces(); nsLister != nil {
		if ns, err := nsLister.Get("kube-system"); err == nil {
			key = string(ns.UID)
		}
	}
	if key == "" {
		if cfg := k8s.GetConfig(); cfg != nil {
			key = cfg.Host
		}
	}
	if key == "" {
		return "", telemetry.ClusterShape{}, false
	}
	return key, telemetry.ClusterShape{
		KubernetesVersion: telemetry.MinorVersion(info.KubernetesVersion),
		Platform:          platformName(info.Platform),
		Nodes:             telemetry.Bucket(info.NodeCount),
		Integrations:      servedIntegrations(),
	}, true
}

var platformPattern = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)

func platformName(p string) string {
	if platformPattern.MatchString(p) {
		return p
	}
	return "unknown"
}

func (s *Server) telemetrySetup() telemetry.Setup {
	authMode := s.authConfig.Mode
	if authMode == "" {
		authMode = "none"
	}
	timeline := "memory"
	if s.effectiveConfig != nil && s.effectiveConfig.TimelineStorage != "" {
		timeline = s.effectiveConfig.TimelineStorage
	}
	prom := "none"
	if url, _ := prometheuspkg.CurrentConfig(); url != "" {
		prom = "connected"
	}
	return telemetry.Setup{
		AuthMode:        authMode,
		TimelineStorage: timeline,
		MCPEnabled:      s.mcpHandler != nil,
		Prometheus:      prom,
		CostSource:      string(opencost.ConfigSnapshot().Source),
	}
}

// telemetryMiddleware counts API requests by their route pattern once the
// router has matched them. It does nothing unless usage data is on.
func (s *Server) telemetryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !telemetry.Recording() {
			next.ServeHTTP(w, r)
			return
		}
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		if rc := chi.RouteContext(r.Context()); rc != nil {
			if pattern := rc.RoutePattern(); pattern != "" {
				telemetry.RecordAPI(r.Method, pattern, ww.Status())
			}
		}
	})
}

// servedIntegrations names the known integrations whose API groups the
// cluster serves. Only groups in the curated map are reported; an unknown
// group can be a customer's own API and its name must never leave.
func servedIntegrations() []string {
	d := k8s.GetResourceDiscovery()
	if d == nil {
		return nil
	}
	resources, err := d.GetAPIResources()
	if err != nil {
		return nil
	}
	var names []string
	for _, r := range resources {
		if name, ok := packages.IntegrationForCRDGroup(r.Group); ok {
			names = append(names, name)
		}
	}
	return telemetry.SortedUnique(names)
}

func (s *Server) handleGetTelemetry(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, s.usageStatusFor(r, telemetry.CurrentStatus()))
}

func (s *Server) handlePutTelemetry(w http.ResponseWriter, r *http.Request) {
	// Consent must come from Radar's own page: a cross-site form post that
	// could switch usage data on would defeat the opt-in.
	if !s.sameOriginOK(r) {
		s.writeError(w, http.StatusForbidden, "cross-origin request rejected")
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil || body.Enabled == nil {
		s.writeError(w, http.StatusBadRequest, `request body must be {"enabled": true|false}`)
		return
	}
	if !s.canDecideUsage(r) {
		s.writeError(w, http.StatusForbidden, "only someone who can change this Radar's Deployment can change usage data for everyone")
		return
	}
	by := ""
	if user := auth.UserFromContext(r.Context()); user != nil {
		by = user.Username
	}
	status, err := telemetry.SetChoice(*body.Enabled, by)
	if errors.Is(err, telemetry.ErrManaged) {
		s.writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		log.Printf("[usage] Failed to save usage-data choice: %v", err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, s.usageStatusFor(r, status))
}

func (s *Server) handleTelemetryPromptShown(w http.ResponseWriter, r *http.Request) {
	if !s.sameOriginOK(r) {
		s.writeError(w, http.StatusForbidden, "cross-origin request rejected")
		return
	}
	// Only someone the question was meant for can mark it asked.
	if !s.canDecideUsage(r) {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if err := telemetry.MarkPromptShown(); err != nil {
		log.Printf("[usage] Failed to record prompt shown: %v", err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleTelemetryEvent takes UI-side usage: views, sessions, active time and
// named UI moments. Unknown names are rejected rather than stored.
func (s *Server) handleTelemetryEvent(w http.ResponseWriter, r *http.Request) {
	// Same-origin and JSON only: a cross-site form post (text/plain skips
	// the CORS preflight) could otherwise inflate a shared install's counts.
	if !s.sameOriginOK(r) {
		s.writeError(w, http.StatusForbidden, "cross-origin request rejected")
		return
	}
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		s.writeError(w, http.StatusUnsupportedMediaType, "expected application/json")
		return
	}
	var body struct {
		Type    string `json:"type"`
		Name    string `json:"name"`
		Minutes int    `json:"minutes"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid event")
		return
	}
	switch body.Type {
	case "view":
		if !telemetry.IsAllowedView(body.Name) {
			s.writeError(w, http.StatusBadRequest, "unknown view")
			return
		}
		telemetry.RecordView(body.Name)
	case "ui":
		if !telemetry.IsAllowedUIEvent(body.Name) {
			s.writeError(w, http.StatusBadRequest, "unknown event")
			return
		}
		telemetry.RecordUIEvent(body.Name)
	case "session":
		telemetry.RecordSession(r.UserAgent())
	case "active":
		telemetry.RecordActive(body.Minutes)
	default:
		s.writeError(w, http.StatusBadRequest, "unknown event type")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
