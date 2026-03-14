package server

import (
	"log"
	"net/http"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/topology"
)

const (
	certExpiryWarningDays  = 30
	certExpiryCriticalDays = 7
)

// Type aliases so existing server code continues to compile unchanged.
type CertificateInfo = topology.CertificateInfo
type SecretCertificateInfo = topology.SecretCertificateInfo
type CertExpiry = topology.CertExpiry

// handleSecretCertExpiry returns certificate expiry for all TLS secrets.
// Used by the frontend secrets list to show an "Expires" column without
// parsing certificates client-side.
func (s *Server) handleSecretCertExpiry(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}

	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}

	provider := k8s.NewTopologyResourceProvider(cache)
	namespaces := parseNamespaces(r.URL.Query())

	result, err := topology.GetCertificateExpiry(provider, namespaces)
	if err != nil {
		log.Printf("[certificate] Failed to get certificate expiry: %v", err)
		s.writeError(w, http.StatusInternalServerError, "Failed to list secrets")
		return
	}

	s.writeJSON(w, result)
}

// DashboardCertificateHealth holds aggregate certificate health for the dashboard.
type DashboardCertificateHealth struct {
	Total    int `json:"total"`
	Healthy  int `json:"healthy"`
	Warning  int `json:"warning"`
	Critical int `json:"critical"`
	Expired  int `json:"expired"`
}

// getDashboardCertificateHealth scans all TLS secrets and counts by expiry bucket.
func (s *Server) getDashboardCertificateHealth(namespace string) *DashboardCertificateHealth {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil
	}

	provider := k8s.NewTopologyResourceProvider(cache)
	var namespaces []string
	if namespace != "" {
		namespaces = []string{namespace}
	}

	expiry, err := topology.GetCertificateExpiry(provider, namespaces)
	if err != nil {
		log.Printf("[certificate] Failed to list secrets for dashboard health: %v", err)
		return nil
	}

	if len(expiry) == 0 {
		return nil
	}

	health := &DashboardCertificateHealth{}
	for _, ce := range expiry {
		health.Total++
		switch {
		case ce.Expired:
			health.Expired++
		case ce.DaysLeft < certExpiryCriticalDays:
			health.Critical++
		case ce.DaysLeft < certExpiryWarningDays:
			health.Warning++
		default:
			health.Healthy++
		}
	}
	return health
}
