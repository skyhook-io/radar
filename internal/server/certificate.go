package server

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"log"
	"math"
	"net/http"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/k8s"
)

const (
	certExpiryWarningDays  = 30
	certExpiryCriticalDays = 7
)

// CertificateInfo holds parsed X.509 certificate metadata for a single certificate.
type CertificateInfo struct {
	Subject      string   `json:"subject"`
	SANs         []string `json:"sans,omitempty"`
	Issuer       string   `json:"issuer"`
	SelfSigned   bool     `json:"selfSigned,omitempty"`
	KeyType      string   `json:"keyType"`
	SerialNumber string   `json:"serialNumber"`
	NotBefore    string   `json:"notBefore"`
	NotAfter     string   `json:"notAfter"`
	DaysLeft     int      `json:"daysLeft"`
	Expired      bool     `json:"expired,omitempty"`
}

// SecretCertificateInfo holds parsed certificate data for a TLS secret.
// Attached to secret responses so the frontend doesn't need to parse certs.
// Certificates are returned in PEM order (conventionally leaf-first: index 0 is the
// server certificate, subsequent entries are intermediates/root).
type SecretCertificateInfo struct {
	Certificates []CertificateInfo `json:"certificates"`
}

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
		certs := parsePEMCertificates(certPEM)
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
		certs := parsePEMCertificates(certPEM)
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

// parsePEMCertificates decodes PEM-encoded certificate data and returns parsed info
// for each certificate in the chain.
func parsePEMCertificates(certData []byte) []CertificateInfo {
	var result []CertificateInfo

	rest := certData
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}

		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			log.Printf("[certificate] Failed to parse certificate block %d in PEM chain: %v", len(result)+1, err)
			continue
		}

		now := time.Now()
		daysLeft := int(math.Floor(cert.NotAfter.Sub(now).Hours() / 24))

		info := CertificateInfo{
			Subject:      certSubjectCN(cert),
			Issuer:       certIssuerCN(cert),
			SelfSigned:   cert.Issuer.String() == cert.Subject.String(),
			KeyType:      certKeyType(cert),
			SerialNumber: fmt.Sprintf("%X", cert.SerialNumber),
			NotBefore:    cert.NotBefore.Format(time.RFC3339),
			NotAfter:     cert.NotAfter.Format(time.RFC3339),
			DaysLeft:     daysLeft,
			Expired:      now.After(cert.NotAfter),
		}

		// Collect SANs (DNS names + IP addresses)
		for _, dns := range cert.DNSNames {
			info.SANs = append(info.SANs, dns)
		}
		for _, ip := range cert.IPAddresses {
			info.SANs = append(info.SANs, ip.String())
		}

		result = append(result, info)
	}

	return result
}

func certSubjectCN(cert *x509.Certificate) string {
	if cert.Subject.CommonName != "" {
		return cert.Subject.CommonName
	}
	s := cert.Subject.String()
	if s != "" {
		return s
	}
	return "-"
}

func certIssuerCN(cert *x509.Certificate) string {
	if cert.Issuer.CommonName != "" {
		return cert.Issuer.CommonName
	}
	s := cert.Issuer.String()
	if s != "" {
		return s
	}
	return "-"
}

func certKeyType(cert *x509.Certificate) string {
	switch pub := cert.PublicKey.(type) {
	case *rsa.PublicKey:
		return fmt.Sprintf("RSA %d", pub.N.BitLen())
	case *ecdsa.PublicKey:
		return fmt.Sprintf("EC %s", ecCurveName(pub.Curve))
	case ed25519.PublicKey:
		return "Ed25519"
	default:
		return strings.TrimPrefix(fmt.Sprintf("%T", pub), "*")
	}
}

func ecCurveName(curve elliptic.Curve) string {
	switch curve {
	case elliptic.P224():
		return "P-224"
	case elliptic.P256():
		return "P-256"
	case elliptic.P384():
		return "P-384"
	case elliptic.P521():
		return "P-521"
	default:
		if params := curve.Params(); params != nil {
			return params.Name
		}
		return "unknown"
	}
}
