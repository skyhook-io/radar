package server

import (
	"log"
	"net/http"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

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

// CertExpiry is a lightweight certificate expiry entry for list views.
type CertExpiry struct {
	DaysLeft int  `json:"daysLeft"`
	Expired  bool `json:"expired,omitempty"`
}

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

	lister := cache.Secrets()
	if lister == nil {
		s.writeJSON(w, map[string]CertExpiry{})
		return
	}

	namespaces := parseNamespaces(r.URL.Query())
	var secrets []*corev1.Secret
	var listErr error
	if len(namespaces) == 1 {
		secrets, listErr = lister.Secrets(namespaces[0]).List(labels.Everything())
	} else if len(namespaces) > 1 {
		for _, ns := range namespaces {
			nsSecrets, err := lister.Secrets(ns).List(labels.Everything())
			if err != nil {
				listErr = err
				break
			}
			secrets = append(secrets, nsSecrets...)
		}
	} else {
		secrets, listErr = lister.List(labels.Everything())
	}
	if listErr != nil {
		log.Printf("[certificate] Failed to list secrets: %v", listErr)
		s.writeError(w, http.StatusInternalServerError, "Failed to list secrets")
		return
	}

	result := make(map[string]CertExpiry)
	for _, secret := range secrets {
		if secret.Type != corev1.SecretTypeTLS {
			continue
		}
		certPEM, exists := secret.Data["tls.crt"]
		if !exists || len(certPEM) == 0 {
			continue
		}
		certs := topology.ParsePEMCertificates(certPEM)
		if len(certs) == 0 {
			continue
		}
		// Use the leaf certificate (first in chain) for expiry
		key := secret.Namespace + "/" + secret.Name
		result[key] = CertExpiry{
			DaysLeft: certs[0].DaysLeft,
			Expired:  certs[0].Expired,
		}
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

	lister := cache.Secrets()
	if lister == nil {
		return nil
	}

	var secrets []*corev1.Secret
	var err error
	if namespace != "" {
		secrets, err = lister.Secrets(namespace).List(labels.Everything())
	} else {
		secrets, err = lister.List(labels.Everything())
	}
	if err != nil {
		log.Printf("[certificate] Failed to list secrets for dashboard health: %v", err)
		return nil
	}

	health := &DashboardCertificateHealth{}
	for _, secret := range secrets {
		if secret.Type != corev1.SecretTypeTLS {
			continue
		}
		certPEM, exists := secret.Data["tls.crt"]
		if !exists || len(certPEM) == 0 {
			continue
		}
		certs := topology.ParsePEMCertificates(certPEM)
		if len(certs) == 0 {
			continue
		}

		health.Total++
		leaf := certs[0]
		switch {
		case leaf.Expired:
			health.Expired++
		case leaf.DaysLeft < certExpiryCriticalDays:
			health.Critical++
		case leaf.DaysLeft < certExpiryWarningDays:
			health.Warning++
		default:
			health.Healthy++
		}
	}

	if health.Total == 0 {
		return nil
	}
	return health
}
