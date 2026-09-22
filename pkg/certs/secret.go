package certs

import (
	"bytes"
	"encoding/pem"
	"time"

	"github.com/skyhook-io/radar/pkg/topology"
	corev1 "k8s.io/api/core/v1"
)

// sealedSecretsKeyLabel marks a sealed-secrets controller keypair. Those are
// type=kubernetes.io/tls and parse as real (long-lived, self-signed) x509, but
// they're an internal encryption key, not a serving certificate anyone renews —
// so the certificate inventory skips them to stay signal, not noise.
const sealedSecretsKeyLabel = "sealedsecrets.bitnami.com/sealed-secrets-key"

// FromTLSSecret projects a kubernetes.io/tls secret into an Input.
// ok=false for secrets that aren't serving certs: non-TLS types, sealed-secrets
// controller keypairs (encryption keys, not renew-able certs), and secrets
// whose tls.crt is missing or unparseable. Reads only tls.crt — never tls.key.
func FromTLSSecret(sec *corev1.Secret) (Input, bool) {
	if sec.Type != corev1.SecretTypeTLS {
		return Input{}, false
	}
	if _, sealed := sec.Labels[sealedSecretsKeyLabel]; sealed {
		return Input{}, false
	}
	data := sec.Data["tls.crt"]
	start := bytes.Index(data, []byte("-----BEGIN CERTIFICATE-----"))
	if start < 0 {
		return Input{}, false
	}
	data = data[start:]
	endMarker := []byte("-----END CERTIFICATE-----")
	end := bytes.Index(data, endMarker)
	if end < 0 || bytes.Contains(data[len("-----BEGIN CERTIFICATE-----"):end], []byte("-----BEGIN CERTIFICATE-----")) {
		return Input{}, false
	}
	block, _ := pem.Decode(data[:end+len(endMarker)])
	if block == nil || block.Type != "CERTIFICATE" {
		return Input{}, false
	}
	// A malformed leaf must not promote an intermediate to the Secret's certificate.
	leaf := topology.ParsePEMCertificates(pem.EncodeToMemory(block))
	if len(leaf) == 0 {
		return Input{}, false
	}
	in := Input{
		Name:      sec.Name,
		Namespace: sec.Namespace,
		Issuer:    leaf[0].Issuer,
		Domains:   leaf[0].SANs,
		Source:    SourceTLSSecret,
	}
	if t, err := time.Parse(time.RFC3339, leaf[0].NotAfter); err == nil {
		in.NotAfter = &t
	}
	return in, true
}
