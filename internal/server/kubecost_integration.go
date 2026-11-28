package server

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/connectionruntime"
	"github.com/skyhook-io/radar/internal/k8s"
	internalopencost "github.com/skyhook-io/radar/internal/opencost"
	"github.com/skyhook-io/radar/internal/portforward"
	"github.com/skyhook-io/radar/pkg/urlutil"
)

type kubecostServiceRef struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Port      int    `json:"port"`
}

type costSourceApplyResponse struct {
	Source    string              `json:"source"`
	Address   string              `json:"address,omitempty"`
	Service   *kubecostServiceRef `json:"service,omitempty"`
	APIKeySet bool                `json:"apiKeySet"`
}

const costSourceApplyTimeout = 58 * time.Second

func (s *Server) handleApplyCostSource(w http.ResponseWriter, r *http.Request) {
	if !s.requireConfigEditable(w, r) {
		return
	}
	if !s.requireCloudRole(w, r, auth.RoleOwner, "modify Radar configuration") {
		return
	}
	if s.localConnections != nil && s.configManagement() == "local" {
		s.writeError(w, http.StatusConflict, "Use the cluster-scoped saved connections endpoint to change this local integration")
		return
	}
	conn := k8s.Current()
	base := s.integrationRuntime.Snapshot()
	if base.CostEnv {
		s.writeError(w, http.StatusConflict, "Cost source is configured from the environment — edit the deployment to change it.")
		return
	}
	var body struct {
		Source    string  `json:"source"`
		URL       string  `json:"url"`
		APIKey    *string `json:"apiKey"`
		ClusterID string  `json:"clusterId"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&body); err != nil {
		s.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	source, err := internalopencost.ValidateSource(body.Source)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	rawURL := strings.TrimRight(strings.TrimSpace(body.URL), "/")
	if err := internalopencost.ValidateKubecostURL(rawURL); err != nil {
		s.writeError(w, http.StatusBadRequest, "Kubecost URL "+err.Error())
		return
	}
	previous := config.Load()
	apiKey := previous.KubecostAPIKey
	apiKeyContext := previous.KubecostAPIKeyContext
	if body.APIKey != nil {
		apiKey = *body.APIKey
		apiKeyContext = ""
	} else if apiKey != "" && !sameServerOrigin(rawURL, previous.KubecostURL) {
		apiKey = ""
		apiKeyContext = ""
	}
	if apiKey != "" && rawURL == "" && apiKeyContext == "" {
		apiKeyContext = conn.ContextName()
	} else if rawURL != "" {
		apiKeyContext = ""
	}
	clusterID := strings.TrimSpace(body.ClusterID)
	clusterIDContext := ""
	if clusterID != "" {
		clusterIDContext = conn.ContextName()
	}
	candidate := internalopencost.ManagerConfig{
		Source:           source,
		URL:              rawURL,
		APIKey:           apiKey,
		APIKeyContext:    apiKeyContext,
		ClusterID:        clusterID,
		ClusterIDContext: clusterIDContext,
	}
	edit := s.integrationRuntime.PrepareShared(conn, base, func(next *connectionruntime.Settings) { next.Cost = candidate }, config.IntegrationCost)
	manager := edit.Set().Cost
	if manager == nil {
		metrics, metricsErr := connectionruntime.SetFor(conn).MetricsClient()
		manager = internalopencost.NewManager(conn, k8s.IsInCluster(), metrics, metricsErr)
		defer manager.Close()
		if err := manager.Configure(candidate); err != nil {
			s.connectionError(w, err)
			return
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), costSourceApplyTimeout)
	defer cancel()
	var connection internalopencost.Connection
	var connectErr error
	if source == internalopencost.SourceAuto && rawURL != "" {
		var probe func(context.Context) error
		probe, connectErr = internalopencost.PrepareCandidateFor(conn, k8s.IsInCluster(), candidate)
		if connectErr == nil {
			connectErr = probe(ctx)
		}
	}
	if connectErr == nil {
		connection, connectErr = manager.Selected(ctx)
	}
	if connectErr != nil {
		edit.Discard()
		if portforward.IsMemberClosed(ctx, connectErr) {
			s.writeError(w, http.StatusServiceUnavailable, "Cost connection closed. Retry the request")
			return
		}
		log.Printf("[opencost] Kubecost configuration probe failed: %s", sanitizeForLog(connectErr.Error()))
		s.writeError(w, http.StatusBadRequest, kubecostConnectionGuidance(connectErr, apiKey != ""))
		return
	}
	if err := edit.Commit(func() error {
		_, err := config.Update(func(c *config.Config) {
			c.CostSource, c.KubecostURL = string(source), rawURL
			c.KubecostAPIKey, c.KubecostAPIKeyContext = apiKey, apiKeyContext
			c.KubecostClusterID, c.KubecostClusterIDContext = clusterID, clusterIDContext
		})
		return err
	}); err != nil {
		s.integrationCommitError(w, "Kubecost", err)
		return
	}

	var service *kubecostServiceRef
	if connection.Service.Name != "" && connection.Service.Namespace != "" && connection.Service.Port > 0 {
		service = &kubecostServiceRef{
			Kind:      "Service",
			Namespace: connection.Service.Namespace,
			Name:      connection.Service.Name,
			Port:      connection.Service.Port,
		}
	}
	s.writeJSON(w, costSourceApplyResponse{
		Source:    string(connection.Source),
		Address:   connection.DisplayAddress,
		Service:   service,
		APIKeySet: apiKey != "",
	})
}

func sameServerOrigin(a, b string) bool {
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
		return strings.TrimSpace(a) == strings.TrimSpace(b)
	}
	left, leftOK := urlutil.NormalizeOrigin(a)
	right, rightOK := urlutil.NormalizeOrigin(b)
	return leftOK && rightOK && left == right
}

func kubecostConnectionGuidance(err error, apiKeySet bool) string {
	message := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "Kubecost discovery timed out — the Aggregator did not answer in time; retry or enter its URL directly."
	case errors.Is(err, internalopencost.ErrNoCostSource):
		return "No compatible cost source was found — install OpenCost metrics or enter a Kubecost Aggregator URL."
	case errors.Is(err, internalopencost.ErrKubecostAuthentication):
		if !apiKeySet {
			return "Kubecost requires authentication — add a service-account API key or use the deployment's intended internal API endpoint."
		}
		return "Kubecost rejected the API key — check the service-account key or use the deployment's intended API endpoint."
	case errors.Is(err, internalopencost.ErrKubecostContextMismatch):
		return "The configured Kubecost cluster ID or local API key is not bound to the current kubeconfig context — clear or update it in Settings."
	case errors.Is(err, internalopencost.ErrKubecostClusterID):
		return "Kubecost cluster ID could not be determined — enter the CLUSTER_ID configured on this cluster's FinOps Agent."
	case errors.Is(err, internalopencost.ErrKubecostNoData):
		return "Kubecost has no allocation data for this cluster ID yet — check the ID and wait for its ETL pipeline to become ready."
	case strings.Contains(message, "access to services") || strings.Contains(message, "no active kubecost"):
		return "Kubecost could not be auto-discovered — enter the central Aggregator URL manually."
	default:
		return "Kubecost Aggregator is unreachable or did not return its allocation API — check the URL, network path, and authentication."
	}
}
