package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/skyhook-io/radar/internal/cloud"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/opencost"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/internal/traffic"
	"github.com/skyhook-io/radar/internal/usagedata"
	"github.com/skyhook-io/radar/internal/version"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/packages"
)

func (s *Server) startUsageData() {
	ctx, cancel := context.WithCancel(context.Background())
	s.stopUsageData = cancel
	opts := usagedata.Options{
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
		Setup:          s.usageSetup,
		Contexts:       func() int { return k8s.GetKubeconfigSummary().ContextCount },
		// Only a local Radar is ever asked, so only its install time matters.
		InstalledAt: version.LocalInstalledAt,
	}
	usagedata.Start(ctx, opts)
	connectionFailuresOnce.Do(func() {
		t := &connectionFailureTracker{record: usagedata.RecordConnectionFailure}
		k8s.OnConnectionChange(t.observe)
		// The first connection attempt can fail before the server starts.
		t.observe(k8s.GetConnectionStatus())
	})
}

var connectionFailuresOnce sync.Once

// connectionFailureTracker counts a failed connection once per episode: the
// retries that follow, automatic or not, are the same failure, while a
// switch to another context that fails too is a new one. The context name
// is only compared here; it is never stored or sent.
type connectionFailureTracker struct {
	mu      sync.Mutex
	settled k8s.ConnectionState
	context string
	record  func(errorType string)
}

func (t *connectionFailureTracker) observe(st k8s.ConnectionStatus) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch st.State {
	case k8s.StateConnected:
	case k8s.StateDisconnected:
		if t.settled != k8s.StateDisconnected || t.context != st.Context {
			t.record(st.ErrorType)
		}
	default:
		return
	}
	t.settled, t.context = st.State, st.Context
}

// requestOriginHeader marks a request the UI made on its own; see
// usagedata.APIRequest.Automatic.
const requestOriginHeader = "X-Radar-Request-Origin"

// usageResponseWriter sits under chi's status-recording wrapper so a handler
// can mark its response through markClusterNotConnected. It forwards the
// optional interfaces chi probes for, so streaming, exec and HTTP/2 push keep
// working.
type usageResponseWriter struct {
	http.ResponseWriter
	notConnected bool
}

func (u *usageResponseWriter) Unwrap() http.ResponseWriter { return u.ResponseWriter }

func (u *usageResponseWriter) markClusterNotConnected() { u.notConnected = true }

func (u *usageResponseWriter) Flush() {
	if f, ok := u.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (u *usageResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := u.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

func (u *usageResponseWriter) Push(target string, opts *http.PushOptions) error {
	if p, ok := u.ResponseWriter.(http.Pusher); ok {
		return p.Push(target, opts)
	}
	return http.ErrNotSupported
}

// markClusterNotConnected tells the usage middleware that w answered "no
// cluster connected yet", which the report doesn't count as a failed request.
// It finds the middleware's writer through the wrappers in between; without
// one (usage data off) it does nothing.
func markClusterNotConnected(w http.ResponseWriter) {
	for w != nil {
		if m, ok := w.(interface{ markClusterNotConnected() }); ok {
			m.markClusterNotConnected()
			return
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return
		}
		w = u.Unwrap()
	}
}

// usageShared reports whether several people use this Radar: in-cluster,
// behind sign-in, or on a listener others can reach. Its configuration then
// decides for everyone, and nobody is asked.
func (s *Server) usageShared() bool {
	return deploymentMode() != k8s.DeploymentModeLocal ||
		s.configManagement() != "local" ||
		!cloud.IsLoopbackHostname(s.listenAddress)
}

// clusterSample describes the connected cluster by shape only. The key is the
// kube-system namespace UID (stable per cluster, meaningless outside it); the
// collector hashes it before storing and never sends it.
func clusterSample() (string, usagedata.ClusterShape, bool) {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return "", usagedata.ClusterShape{}, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	info, err := k8s.GetClusterInfo(ctx)
	if err != nil {
		return "", usagedata.ClusterShape{}, false
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
		return "", usagedata.ClusterShape{}, false
	}
	return key, usagedata.ClusterShape{
		KubernetesVersion: usagedata.MinorVersion(info.KubernetesVersion),
		Platform:          platformName(info.Platform),
		Nodes:             usagedata.Bucket(info.NodeCount),
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

func (s *Server) usageSetup() usagedata.Setup {
	authMode := s.authConfig.Mode
	if authMode == "" {
		authMode = "none"
	}
	timeline := "memory"
	if s.effectiveConfig != nil && s.effectiveConfig.TimelineStorage != "" {
		timeline = s.effectiveConfig.TimelineStorage
	}
	return usagedata.Setup{
		AuthMode:        authMode,
		TimelineStorage: timeline,
		MCPEnabled:      s.mcpEnabled(),
		Prometheus:      prometheuspkg.ConnectionSource(),
		CostSource:      string(opencost.ConfigSnapshot().Source),
		AIConfigured:    s.aiConfigured(),
		TrafficSource:   usageTrafficSource(),
		GitOpsReadable:  gitopsReadable(),
	}
}

// aiConfigured reports whether investigations can run now: the same test the
// AI agent picker's "enabled" uses, without probing for newly installed CLIs.
func (s *Server) aiConfigured() bool {
	diagnoser, runs := s.aiEngine()
	return diagnoser != nil && runs != nil && len(diagnoser.AgentInfos(context.Background(), false)) > 0
}

// lastUsageTrafficSource is the traffic source last read, reported while
// detection holds the traffic manager.
var lastUsageTrafficSource atomic.Value

func usageTrafficSource() string {
	m := traffic.GetManager()
	if m == nil {
		return "none"
	}
	name, ok := m.ActiveSourceNameIfIdle()
	if ok {
		lastUsageTrafficSource.Store(name)
	} else {
		name, _ = lastUsageTrafficSource.Load().(string)
	}
	return usagedata.TrafficSource(name)
}

// gitopsKinds are the resources Radar's GitOps views are built on.
var gitopsKinds = []struct{ kind, group string }{
	{"Application", "argoproj.io"},
	{"Kustomization", "kustomize.toolkit.fluxcd.io"},
	{"HelmRelease", "helm.toolkit.fluxcd.io"},
}

// gitopsReadable reports whether the cluster serves Argo CD or Flux resources
// and Radar's own identity can read them. Installed catalog kinds are watched
// from startup, so an informer that is syncing or synced means the list was
// allowed. It reads only discovery and cache bookkeeping, never the API.
func gitopsReadable() bool {
	d := k8s.GetResourceDiscovery()
	dc := k8s.GetDynamicResourceCache()
	if d == nil || dc == nil || dc.DynamicResourceCache == nil {
		return false
	}
	for _, g := range gitopsKinds {
		gvr, ok := d.GetGVRWithGroup(g.kind, g.group)
		if !ok {
			continue
		}
		switch dc.Observation(gvr).State {
		case k8score.DynamicObservationSynced, k8score.DynamicObservationSyncing:
			return true
		}
	}
	return false
}

// Replaced in tests, which can't turn the process-wide collector on.
var (
	usageRecording     = usagedata.Recording
	recordUsageRequest = usagedata.RecordRequest
	clusterConnected   = k8s.IsConnected
)

// usageMiddleware counts API requests by their route pattern once the
// router has matched them. It does nothing unless usage data is on.
func (s *Server) usageMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !usageRecording() {
			next.ServeHTTP(w, r)
			return
		}
		uw := &usageResponseWriter{ResponseWriter: w}
		ww := middleware.NewWrapResponseWriter(uw, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		rc := chi.RouteContext(r.Context())
		if rc == nil {
			return
		}
		pattern := rc.RoutePattern()
		if pattern == "" {
			return
		}
		recordUsageRequest(usagedata.APIRequest{
			Method:    r.Method,
			Pattern:   strings.TrimPrefix(pattern, s.basePath),
			Status:    ww.Status(),
			Automatic: r.Header.Get(requestOriginHeader) == "auto",
			// Handlers mark their own "not connected yet" answers, but many
			// routes answer 503 on a missing client without marking it. While
			// the cluster isn't connected, every 503 is that.
			NotConnected: uw.notConnected || (ww.Status() == http.StatusServiceUnavailable && !clusterConnected()),
			Kind:         rc.URLParam("kind"),
			Group:        r.URL.Query().Get("group"),
		})
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
	return usagedata.SortedUnique(names)
}

func (s *Server) handleGetUsageData(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, usagedata.CurrentStatus())
}

func (s *Server) handlePutUsageData(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil || body.Enabled == nil {
		s.writeError(w, http.StatusBadRequest, `request body must be {"enabled": true|false}`)
		return
	}
	status, err := usagedata.SetChoice(*body.Enabled)
	if errors.Is(err, usagedata.ErrManaged) {
		s.writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		log.Printf("[usage] Failed to save usage-data choice: %v", err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.writeJSON(w, status)
}

func (s *Server) handleUsagePromptShown(w http.ResponseWriter, r *http.Request) {
	if err := usagedata.MarkPromptShown(); err != nil {
		log.Printf("[usage] Failed to record prompt shown: %v", err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUsageEvent takes UI-side usage: views, sessions, active time and
// named UI moments. Unknown names are rejected rather than stored.
func (s *Server) handleUsageEvent(w http.ResponseWriter, r *http.Request) {
	// JSON only. Cross-site writes are already refused by requireSameOrigin.
	if !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		s.writeError(w, http.StatusUnsupportedMediaType, "expected application/json")
		return
	}
	var body struct {
		Type    string `json:"type"`
		Name    string `json:"name"`
		Minutes int    `json:"minutes"`
		View    string `json:"view"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&body); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid event")
		return
	}
	switch body.Type {
	case "view":
		if !usagedata.IsAllowedView(body.Name) {
			s.writeError(w, http.StatusBadRequest, "unknown view")
			return
		}
		usagedata.RecordView(body.Name)
	case "ui":
		if !usagedata.IsAllowedUIEvent(body.Name) {
			s.writeError(w, http.StatusBadRequest, "unknown event")
			return
		}
		usagedata.RecordUIEvent(body.Name)
	case "session":
		usagedata.RecordSession(r.UserAgent())
	case "active":
		// The view on screen, counted only if the period has no views yet.
		if body.View != "" && !usagedata.IsAllowedView(body.View) {
			s.writeError(w, http.StatusBadRequest, "unknown view")
			return
		}
		usagedata.RecordActiveView(body.Minutes, body.View)
	default:
		s.writeError(w, http.StatusBadRequest, "unknown event type")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
