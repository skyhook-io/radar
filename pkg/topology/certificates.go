package topology

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
	"strings"
	"time"
)

// ParsePEMCertificates decodes PEM-encoded certificate data and returns parsed
// CertificateInfo for each certificate found, in the order they appear in the PEM data.
// Non-CERTIFICATE PEM blocks are silently skipped; certificates that fail to parse are logged and skipped.
func ParsePEMCertificates(certData []byte) []CertificateInfo {
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
