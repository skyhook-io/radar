// Package openobserve runs local Radar's shared OpenObserve connection: it
// matches each kubeconfig context to its cluster's series in OpenObserve and
// points the metrics client at OpenObserve through a transport that forces
// that cluster's filter into every query.
package openobserve

import (
	"fmt"
	"os"
	"strings"

	"github.com/skyhook-io/radar/internal/config"
	oo "github.com/skyhook-io/radar/pkg/prom/openobserve"
)

// maxCACertBytes bounds the CA bundle read from caCertFile.
const maxCACertBytes = 1 << 20

// Connection resolves the saved entry into a usable connection: the token
// (tokenEnv first) and the CA bundle file.
func Connection(cfg config.OpenObserveConfig) (oo.Connection, error) {
	token, err := cfg.ResolveToken()
	if err != nil {
		return oo.Connection{}, err
	}
	conn := oo.Connection{
		BaseURL: strings.TrimSpace(cfg.BaseURL),
		Org:     strings.TrimSpace(cfg.Org),
		User:    strings.TrimSpace(cfg.User),
		Token:   token,
	}
	if path := strings.TrimSpace(cfg.CACertFile); path != "" {
		info, err := os.Stat(path)
		if err != nil {
			return oo.Connection{}, fmt.Errorf("OpenObserve CA certificate: %w", err)
		}
		if info.Size() > maxCACertBytes {
			return oo.Connection{}, fmt.Errorf("OpenObserve CA certificate %s is larger than 1 MiB", path)
		}
		pem, err := os.ReadFile(path)
		if err != nil {
			return oo.Connection{}, fmt.Errorf("OpenObserve CA certificate: %w", err)
		}
		conn.CACertPEM = string(pem)
		if _, err := conn.TLSConfig(); err != nil {
			return oo.Connection{}, err
		}
	}
	if err := conn.Validate(); err != nil {
		return oo.Connection{}, err
	}
	return conn, nil
}
