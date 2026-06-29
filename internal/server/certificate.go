package server

import (
	"errors"
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
// Read pattern mirrors handleListPackages: the ServiceAccount-backed cache
// performs the read (so cert hygiene survives cloud:viewer, whose K8s `view`
// role excludes secrets), but the response is post-filtered to the caller's
// RBAC-allowed namespaces. Only public certificate metadata (issuer / domains
// / expiry) is emitted — never tls.key. A cluster without cert-manager simply
// contributes no CR rows; that's not an error.
// secretReadableNamespaces returns the subset of `namespaces` in which the
// calling user may list Secrets. The shared cache can hold every secret (the SA
// may have cluster-wide secrets RBAC the user lacks), so secret-derived data
// (TLS cert metadata) must be gated per-user even when the namespace is visible.
// Mirrors the secrets gate in preflightResourceList:
//   - nil in → all-namespace access: nil out (all) if the user can list secrets
//     cluster-wide, else empty (none).
//   - slice in → the per-namespace secrets-listable subset (possibly empty).
func (s *Server) secretReadableNamespaces(r *http.Request, namespaces []string) []string {
	if namespaces == nil {
		if s.canRead(r, "", "secrets", "", "list") {
			return nil
		}
		return []string{}
	}
	return s.filterNamespacesByCanRead(r, "", "secrets", "list", namespaces)
}

func (s *Server) handleCertificates(w http.ResponseWriter, r *http.Request) {
	if !s.requireConnected(w) {
		return
	}
	cache := k8s.GetResourceCache()
	if cache == nil {
		s.writeError(w, http.StatusServiceUnavailable, "Resource cache not available")
		return
	}

	namespaces := s.parseNamespacesForUser(r)
	if noNamespaceAccess(namespaces) {
		s.writeJSON(w, []certs.Cert{})
		return
	}

	src := certs.Sources{Now: time.Now().UTC()}

	// cert-manager Certificate CRs. ?group disambiguates from other ecosystems'
	// "Certificate" kinds. An absent CRD (ErrUnknownDynamicKind) just means the
	// cluster doesn't run cert-manager — not an error; the TLS-secret leg still
	// answers. Any OTHER error (RBAC denial, discovery not ready, transient)
	// would silently drop real cert-manager certs, so log it and remember the
	// failure rather than rendering a misleadingly-empty inventory.
	cmFailed := false
	items, err := cache.ListDynamicWithGroup(r.Context(), "certificates", "", "cert-manager.io")
	switch {
	case err == nil:
		for _, it := range items {
			if in := projectCertManagerCert(it.Object); topology.MatchesNamespace(namespaces, in.Namespace) {
				src.CertManager = append(src.CertManager, in)
			}
		}
	case errors.Is(err, k8s.ErrUnknownDynamicKind):
		// cert-manager not installed — expected.
	default:
		log.Printf("[certificate] cert-manager Certificate list failed: %v", err)
		cmFailed = true
	}

	// kubernetes.io/tls secrets. We only read tls.crt (the public chain) and
	// emit metadata; tls.key never leaves the cluster. A nil error with no
	// secrets is a benign deferred-cache state; a non-nil error means we
	// genuinely couldn't read secrets.
	//
	// The cache holds every secret (the SA can have cluster-wide secrets RBAC
	// the calling user doesn't), so the TLS leg must respect per-user secrets
	// RBAC — namespace visibility alone is not enough. secretNS is the subset
	// of namespaces in which the user may list secrets (nil = all, empty = none).
	secretNS := s.secretReadableNamespaces(r, namespaces)
	secFailed := false
	provider := k8s.NewTopologyResourceProvider(cache)
	if secrets, err := provider.Secrets(); err == nil {
		for _, sec := range secrets {
			if !topology.MatchesNamespace(secretNS, sec.Namespace) {
				continue
			}
			if in, ok := secretToCertInput(sec); ok {
				src.TLSSecrets = append(src.TLSSecrets, in)
			}
		}
	} else {
		log.Printf("[certificate] TLS secret list failed: %v", err)
		secFailed = true
	}

	// An empty result caused by a read error reads as a healthy "no certs"
	// cluster. Surface it so the fleet view marks the cluster errored rather
	// than silently green. This matters most on a cluster without cert-manager,
	// where TLS secrets are the only source: there cmFailed stays false (an
	// absent CRD is benign), so gating on both legs would miss a failed secret
	// read. Gate on "no data AND something errored" instead — which still lets
	// a partial success (one leg returned certs) through.
	result := certs.Aggregate(src)
	if len(result) == 0 && (cmFailed || secFailed) {
		s.writeError(w, http.StatusServiceUnavailable, "certificate inventory temporarily unavailable")
		return
	}

	s.writeJSON(w, result)
}

// secretToCertInput projects a kubernetes.io/tls secret into a certs.Input.
// ok=false for secrets that aren't serving certs: non-TLS types, sealed-secrets
// controller keypairs (encryption keys, not renew-able certs), and secrets
// whose tls.crt is missing or unparseable. Reads only tls.crt — never tls.key.
func secretToCertInput(sec *corev1.Secret) (certs.Input, bool) {
	if sec.Type != corev1.SecretTypeTLS {
		return certs.Input{}, false
	}
	if _, sealed := sec.Labels[sealedSecretsKeyLabel]; sealed {
		return certs.Input{}, false
	}
	pem, ok := sec.Data["tls.crt"]
	if !ok || len(pem) == 0 {
		return certs.Input{}, false
	}
	leaf := topology.ParsePEMCertificates(pem)
	if len(leaf) == 0 {
		return certs.Input{}, false
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
	return in, true
}

// projectCertManagerCert maps a cert-manager.io Certificate CR (unstructured)
// to a certs.Input. status.notAfter is absent when the cert has no issued
// secret yet (and during some renewal-failure states), in which case NotAfter
// stays nil (pkg/certs renders it as unknown expiry).
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

	// Cert expiry is read from TLS Secrets — gate by per-user secrets RBAC.
	result, err := topology.GetCertificateExpiry(provider, s.secretReadableNamespaces(r, namespaces))
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
