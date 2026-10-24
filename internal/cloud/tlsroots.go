package cloud

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"log"
	"os"
	"sync"
	"time"
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
	pool, err := CAPoolFromPEM(data)
	if err != nil {
		return nil, fmt.Errorf("CA file: %w", err)
	}
	return pool, nil
}

// CAPoolFromPEM returns the system roots plus every certificate in data.
func CAPoolFromPEM(data []byte) (*x509.CertPool, error) {
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(data) {
		return nil, errors.New("holds no PEM certificates")
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

// CAFile re-reads a CA bundle when the file changes, so a CA mounted from a
// ConfigMap can be rotated without restarting Radar. Pool is cheap when the
// file is unchanged: one stat.
type CAFile struct {
	path string

	mu      sync.Mutex
	pool    *x509.CertPool
	modTime time.Time
	size    int64
	warned  bool
}

// NewCAFile loads path, which must hold at least one certificate now.
func NewCAFile(path string) (*CAFile, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("read CA file: %w", err)
	}
	pool, err := LoadCAFile(path)
	if err != nil {
		return nil, err
	}
	return &CAFile{path: path, pool: pool, modTime: info.ModTime(), size: info.Size()}, nil
}

// Pool returns the current CA pool. If the file changed and no longer
// parses, it keeps the last good pool and logs once until the file is fixed.
// A nil *CAFile means the system roots.
func (c *CAFile) Pool() *x509.CertPool {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	info, err := os.Stat(c.path)
	if err == nil && info.ModTime().Equal(c.modTime) && info.Size() == c.size {
		return c.pool
	}
	var pool *x509.CertPool
	if err == nil {
		pool, err = LoadCAFile(c.path)
	}
	if err != nil {
		if !c.warned {
			log.Printf("[cloud] WARNING: %v; still trusting the CA bundle read earlier", err)
			c.warned = true
		}
		return c.pool
	}
	if c.warned {
		log.Printf("[cloud] CA file %s readable again", c.path)
	}
	c.pool, c.modTime, c.size, c.warned = pool, info.ModTime(), info.Size(), false
	return c.pool
}
