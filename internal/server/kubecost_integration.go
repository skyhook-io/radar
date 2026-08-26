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
	"github.com/skyhook-io/radar/internal/k8s"
	internalopencost "github.com/skyhook-io/radar/internal/opencost"
)

func (s *Server) handleApplyKubecostConfig(w http.ResponseWriter, r *http.Request) {
	if !s.requireCloudRole(w, r, auth.RoleOwner, "modify Radar configuration") {
		return
	}
	if internalopencost.IsEnvManaged() {
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
		apiKeyContext = k8s.GetContextName()
	} else if rawURL != "" {
		apiKeyContext = ""
	}
	clusterID := strings.TrimSpace(body.ClusterID)
	clusterIDContext := ""
	if clusterID != "" {
		clusterIDContext = k8s.GetContextName()
	}
	candidate := internalopencost.ManagerConfig{
		Source:           source,
		URL:              rawURL,
		APIKey:           apiKey,
		APIKeyContext:    apiKeyContext,
		ClusterID:        clusterID,
		ClusterIDContext: clusterIDContext,
	}
	previousManager := internalopencost.ConfigSnapshot()
	if source != internalopencost.SourcePrometheus && rawURL != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		if _, err := internalopencost.ProbeKubecost(ctx, candidate); err != nil {
			log.Printf("[opencost] Kubecost configuration probe failed: %s", sanitizeForLog(err.Error()))
			s.writeError(w, http.StatusBadRequest, kubecostConnectionGuidance(err))
			return
		}
	}
	if err := internalopencost.Configure(candidate); err != nil {
		s.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	connection, connectErr := internalopencost.Selected(ctx)
	if connectErr != nil {
		_ = internalopencost.Configure(previousManager)
		log.Printf("[opencost] Kubecost configuration probe failed: %s", sanitizeForLog(connectErr.Error()))
		s.writeError(w, http.StatusBadRequest, kubecostConnectionGuidance(connectErr))
		return
	}

	if _, err := config.Update(func(c *config.Config) {
		c.CostSource = string(source)
		c.KubecostURL = rawURL
		c.KubecostAPIKey = apiKey
		c.KubecostAPIKeyContext = apiKeyContext
		c.KubecostClusterID = clusterID
		c.KubecostClusterIDContext = clusterIDContext
	}); err != nil {
		_ = internalopencost.Configure(previousManager)
		log.Printf("[config] Failed to persist Kubecost configuration: %v", err)
		s.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if s.openCostCurrency != nil {
		s.openCostCurrency.Invalidate()
	}
	s.writeJSON(w, struct {
		Applied   bool   `json:"applied"`
		Source    string `json:"source"`
		Address   string `json:"address,omitempty"`
		APIKeySet bool   `json:"apiKeySet"`
	}{Applied: true, Source: string(connection.Source), Address: connection.Address, APIKeySet: apiKey != ""})
}

func sameServerOrigin(a, b string) bool {
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
		return strings.TrimSpace(a) == strings.TrimSpace(b)
	}
	left, leftOK := normalizeOrigin(a)
	right, rightOK := normalizeOrigin(b)
	return leftOK && rightOK && left == right
}

func kubecostConnectionGuidance(err error) string {
	message := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "Kubecost discovery timed out — the Aggregator did not answer in time; retry or enter its URL directly."
	case errors.Is(err, internalopencost.ErrKubecostAuthentication):
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
