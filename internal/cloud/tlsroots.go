package cloud

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
)

// LoadCAFile returns the system roots plus every PEM certificate in path, for
// a hub whose certificate is signed by a private CA. The file must hold at
// least one certificate: an empty or unreadable bundle is a configuration
// error, not a reason to fall back to the system roots silently.
func LoadCAFile(path string) (*x509.CertPool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read CA file: %w", err)
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(data) {
		return nil, errors.New("CA file: holds no PEM certificates")
	}
	return pool, nil
}

// hubTLSConfig is the TLS policy shared by the enroll client and the tunnel
// dialer. Nil means Go's defaults (system roots, verification on).
func hubTLSConfig(roots *x509.CertPool, insecureSkipVerify bool) *tls.Config {
	if roots == nil && !insecureSkipVerify {
		return nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots}
	if insecureSkipVerify {
		cfg.InsecureSkipVerify = true //nolint:gosec // user-requested via --cloud-insecure-skip-verify for a self-signed hub
	}
	return cfg
}
