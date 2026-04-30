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
	namespaces := s.parseNamespacesForUser(r)
	if noNamespaceAccess(namespaces) {
		s.writeJSON(w, map[string]CertExpiry{})
		return
	}

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

// getDashboardCertificateHealth scans TLS secrets in the given
// namespaces and counts by expiry bucket. Empty `namespaces` means
// "every namespace the cache exposes" (matches the convention used
// throughout this package).
//
// SKY-834 bug 54: previously this took a single `namespace string`,
// and the caller only forwarded it when the user had selected
// EXACTLY one namespace — for any other count (0 = all, 2+ = some
// subset) the call collapsed to the empty-string "all namespaces"
// path. Result: the Home Cert Health card and the Secrets list page
// counted from different namespace scopes. With a 2-namespace
// selection the Home card would show "0 Expired" while the Secrets
// page (which always respects the full selection) showed multiple
// expired TLS bundles.
//
// Now takes the full slice and forwards it untouched, matching the
// shape of `getDashboardAudit`, `getDashboardNetworkPolicyCoverage`,
// etc.
func (s *Server) getDashboardCertificateHealth(namespaces []string) *DashboardCertificateHealth {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil
	}

	provider := k8s.NewTopologyResourceProvider(cache)

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
