package server

import (
	"log"
	"net/http"
	"time"

	corev1 "k8s.io/api/core/v1"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/certs"
	"github.com/skyhook-io/radar/pkg/topology"
)

// sealedSecretsKeyLabel marks a sealed-secrets controller keypair. Those are
// type=kubernetes.io/tls and parse as real (long-lived, self-signed) x509, but
// they're an internal encryption key, not a serving certificate anyone renews —
// so the certificate inventory skips them to stay signal, not noise.
const sealedSecretsKeyLabel = "sealedsecrets.bitnami.com/sealed-secrets-key"

const (
	certExpiryWarningDays  = 30
	certExpiryCriticalDays = 7
)

// Type aliases so existing server code continues to compile unchanged.
type CertificateInfo = topology.CertificateInfo
type SecretCertificateInfo = topology.SecretCertificateInfo
type CertExpiry = topology.CertExpiry

// handleCertificates returns the cluster's unified certificate inventory:
// cert-manager.io Certificate CRs + raw kubernetes.io/tls secrets, deduped and
// health-rated by pkg/certs. Backs Radar Hub's fleet Certs view; the merge
// lives in pkg/certs so the hub stays a thin pivot.
//
// Inventory read pattern (mirrors handlePackages): TLS secrets are read with
// the ServiceAccount, not the caller's impersonated identity, so cert hygiene
// doesn't vanish for cloud:viewer (whose K8s `view` role excludes secrets).
// Only public certificate metadata (issuer / domains / expiry) is emitted —
// never tls.key. A cluster without cert-manager simply contributes no CR rows;
// that's not an error.
func (s *Server) handleCertificates(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}

	src := certs.Sources{Now: time.Now().UTC()}

	// cert-manager Certificate CRs. ?group disambiguates from other ecosystems'
	// "Certificate" kinds. A CRD-absent / not-yet-discovered cluster errors
	// here and we ignore it — the TLS-secret leg still answers, so "no
	// cert-manager" is not a failure.
	if items, err := cache.ListDynamicWithGroup(r.Context(), "certificates", "", "cert-manager.io"); err == nil {
		for _, it := range items {
			src.CertManager = append(src.CertManager, projectCertManagerCert(it.Object))
		}
	}

	// kubernetes.io/tls secrets. We only read tls.crt (the public chain) and
	// emit metadata; tls.key never leaves the cluster.
	provider := k8s.NewTopologyResourceProvider(cache)
	if secrets, err := provider.Secrets(); err == nil {
		for _, sec := range secrets {
			if sec.Type != corev1.SecretTypeTLS {
				continue
			}
			if _, sealed := sec.Labels[sealedSecretsKeyLabel]; sealed {
				continue
			}
			pem, ok := sec.Data["tls.crt"]
			if !ok || len(pem) == 0 {
				continue
			}
			leaf := topology.ParsePEMCertificates(pem)
			if len(leaf) == 0 {
				continue
			}
			in := certs.Input{
				Name:      sec.Name,
				Namespace: sec.Namespace,
				Issuer:    leaf[0].Issuer,
				Domains:   leaf[0].SANs,
				Source:    certs.SourceTLSSecret,
			}
			if t, err := time.Parse(time.RFC3339, leaf[0].NotAfter); err == nil {
				in.NotAfter = &t
			}
			src.TLSSecrets = append(src.TLSSecrets, in)
		}
	}

	s.writeJSON(w, certs.Aggregate(src))
}

// projectCertManagerCert maps a cert-manager.io Certificate CR (unstructured)
// to a certs.Input. status.notAfter is absent until the cert is first issued,
// in which case NotAfter stays nil (pkg/certs renders it as unknown expiry).
func projectCertManagerCert(obj map[string]any) certs.Input {
	md, _ := obj["metadata"].(map[string]any)
	spec, _ := obj["spec"].(map[string]any)
	status, _ := obj["status"].(map[string]any)
	in := certs.Input{
		Name:       mapString(md, "name"),
		Namespace:  mapString(md, "namespace"),
		SecretName: mapString(spec, "secretName"),
		Source:     certs.SourceCertManager,
	}
	if ir, ok := spec["issuerRef"].(map[string]any); ok {
		in.Issuer = mapString(ir, "name")
	}
	if dns, ok := spec["dnsNames"].([]any); ok {
		for _, d := range dns {
			if ds, ok := d.(string); ok {
				in.Domains = append(in.Domains, ds)
			}
		}
	}
	if na := mapString(status, "notAfter"); na != "" {
		if t, err := time.Parse(time.RFC3339, na); err == nil {
			in.NotAfter = &t
		}
	}
	return in
}

func mapString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	s, _ := m[key].(string)
	return s
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
// namespaces and counts by expiry bucket. nil namespaces means
// "every namespace the cache exposes"; an empty slice means none
// (matches the MatchesNamespace contract used throughout this package).
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
