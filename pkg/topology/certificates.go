package topology

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"github.com/skyhook-io/radar/pkg/health"
	"math"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
)

// ParsePEMCertificates decodes PEM-encoded certificate data and returns parsed
// CertificateInfo for each certificate found, in the order they appear in the PEM data.
// Other blocks and malformed certificates are skipped.
func ParsePEMCertificates(certData []byte) []CertificateInfo {
	var result []CertificateInfo
	for block, rest := pem.Decode(certData); block != nil; block, rest = pem.Decode(rest) {
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err == nil {
			info := NewCertificateInfo(cert, time.Now())
			info.Organizations = cert.Subject.Organization
			result = append(result, info)
		}
	}
	return result
}

var certBegin = []byte("-----BEGIN CERTIFICATE-----")
var certEnd = []byte("-----END CERTIFICATE-----")

func parseCertificateBundle(data []byte) ([]CertificateInfo, bool) {
	if !bytes.Contains(data, certBegin) {
		return nil, false
	}
	var certs []CertificateInfo
	start, firstStart, offset := -1, -1, 0
	leafParsed := false
	for line := range bytes.Lines(data) {
		switch marker := bytes.TrimRight(line, " \t\r\n"); {
		case bytes.Equal(marker, certBegin):
			start = offset
			if firstStart < 0 {
				firstStart = start
			}
		case start >= 0 && bytes.Equal(marker, certEnd):
			parsed := ParsePEMCertificates(data[start : offset+len(line)])
			// A malformed first certificate cannot make a later CA the serving leaf.
			if start == firstStart {
				leafParsed = len(parsed) > 0
			}
			certs = append(certs, parsed...)
			start = -1
		}
		offset += len(line)
	}
	return certs, leafParsed
}

// ParseSecretCertificates describes only certificates in the supplied Secret.
func ParseSecretCertificates(secret *corev1.Secret) *SecretCertificateInfo {
	info := SecretCertificateInfo{Certificates: []CertificateInfo{}}
	for key, data := range secret.Data {
		chain, leafParsed := parseCertificateBundle(data)
		if len(chain) == 0 {
			continue
		}
		if secret.Type == corev1.SecretTypeTLS && key == corev1.TLSCertKey && leafParsed {
			info.Certificates = chain
			continue
		}
		if info.AdditionalKeys == nil {
			info.AdditionalKeys = make(map[string][]CertificateInfo)
		}
		info.AdditionalKeys[key] = chain
	}
	if len(info.Certificates) == 0 && len(info.AdditionalKeys) == 0 {
		return nil
	}
	return &info
}

// NewCertificateInfo summarizes one parsed certificate, with its remaining
// lifetime measured from now.
func NewCertificateInfo(cert *x509.Certificate, now time.Time) CertificateInfo {
	info := CertificateInfo{
		Subject:      certSubjectCN(cert),
		Issuer:       certIssuerCN(cert),
		SelfSigned:   cert.Issuer.String() == cert.Subject.String(),
		KeyType:      PublicKeyType(cert.PublicKey),
		SerialNumber: fmt.Sprintf("%X", cert.SerialNumber),
		NotBefore:    cert.NotBefore.Format(time.RFC3339),
		NotAfter:     cert.NotAfter.Format(time.RFC3339),
		DaysLeft:     int(math.Floor(cert.NotAfter.Sub(now).Hours() / 24)),
		Expired:      now.After(cert.NotAfter),
	}

	for _, dns := range cert.DNSNames {
		info.SANs = append(info.SANs, dns)
	}
	for _, ip := range cert.IPAddresses {
		info.SANs = append(info.SANs, ip.String())
	}

	return info
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

// PublicKeyType names a public key's algorithm and size, e.g. "RSA 2048" or
// "EC P-256".
func PublicKeyType(pub any) string {
	switch pub := pub.(type) {
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

// CertExpiry is a lightweight certificate expiry entry for list views.
type CertExpiry struct {
	Expiry       string                          `json:"expiry"`
	Health       health.Level                    `json:"health"`
	ExpiryPolicy health.CertificateExpiryWindows `json:"expiryPolicy"`
	DaysLeft     int                             `json:"daysLeft"`
	Expired      bool                            `json:"expired,omitempty"`
}

// GetCertificateExpiry returns certificate expiry for all TLS secrets,
// keyed by "namespace/name". If namespaces is non-empty, only matching
// namespaces are included.
func GetCertificateExpiry(provider ResourceProvider, namespaces []string) (map[string]CertExpiry, error) {
	return GetCertificateExpiryWithPolicy(provider, namespaces, health.CertificateExpiryPolicy{})
}

func GetCertificateExpiryWithPolicy(provider ResourceProvider, namespaces []string, policy health.CertificateExpiryPolicy) (map[string]CertExpiry, error) {
	secrets, err := provider.Secrets()
	if err != nil {
		// RBAC not granted or lister unavailable — return empty (not an error).
		// Matches topology builder pattern: log + continue with empty data.
		return make(map[string]CertExpiry), nil
	}

	result := make(map[string]CertExpiry)
	now := time.Now()
	for _, secret := range secrets {
		if !MatchesNamespace(namespaces, secret.Namespace) {
			continue
		}
		if secret.Type != corev1.SecretTypeTLS {
			continue
		}
		certPEM, exists := secret.Data["tls.crt"]
		if !exists || len(certPEM) == 0 {
			continue
		}
		certs, leafParsed := parseCertificateBundle(certPEM)
		if !leafParsed {
			continue
		}
		key := secret.Namespace + "/" + secret.Name
		expiry, err := time.Parse(time.RFC3339, certs[0].NotAfter)
		if err != nil {
			continue
		}
		var notBefore *time.Time
		if start, err := time.Parse(time.RFC3339, certs[0].NotBefore); err == nil {
			notBefore = &start
		}
		thresholds := policy.Resolve(notBefore, &expiry)
		result[key] = CertExpiry{
			Expiry:       certs[0].NotAfter,
			Health:       thresholds.Classify(&expiry, now),
			ExpiryPolicy: thresholds.Windows(),
			DaysLeft:     certs[0].DaysLeft,
			Expired:      certs[0].Expired,
		}
	}

	return result, nil
}
