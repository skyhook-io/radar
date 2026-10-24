package helm

import (
	"fmt"
	"log"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"helm.sh/helm/v3/pkg/registry"

	"github.com/skyhook-io/radar/internal/settings"
)

// ociProbeTimeout bounds a single registry tag lookup via the registry client's
// http.Client. helm's Tags() uses context.Background() with no deadline of its
// own, so without this an unreachable registered registry would stall the
// (synchronous) batch upgrade check that backs the Helm list view.
const ociProbeTimeout = 5 * time.Second

// OCI chart-source registration.
//
// Helm v3 never persists the ref a release was installed from, and unlike
// classic HTTP repos (listed in repositories.yaml) there is no native "configured
// source list" for OCI registries — `helm registry login` stores only auth. So a
// release installed via `helm install oci://…` has no discoverable upstream and
// shows "source not tracked".
//
// A registered OCI prefix is the OCI analog of `helm repo add`: the user declares
// once where their charts live (e.g. "oci://ghcr.io/myorg/charts"), and Radar
// probes "<prefix>/<chartName>" to discover newer versions. Because matching is by
// chart name at query time, no per-release mapping is persisted — which also means
// prefixes are global to the Radar instance, not cluster-scoped.
//
// Credentials are reused from the user's existing `helm registry login` store
// (settings.RegistryConfig); Radar stores no registry secrets of its own.

// normalizeOCIPrefix validates and canonicalizes a registered prefix. It must be
// an oci:// reference with at least a host; the trailing slash is trimmed so
// "<prefix>/<chart>" joins cleanly.
func normalizeOCIPrefix(raw string) (string, error) {
	p := strings.TrimSpace(raw)
	if p == "" {
		return "", fmt.Errorf("source is empty")
	}
	if !strings.HasPrefix(p, "oci://") {
		return "", fmt.Errorf("source must be an oci:// reference, got %q", raw)
	}
	// Strip the scheme before trimming slashes — trimming "oci://" directly would
	// eat the "//" and yield "oci:".
	rest := strings.Trim(strings.TrimPrefix(p, "oci://"), "/")
	if rest == "" {
		return "", fmt.Errorf("source must include a registry host")
	}
	if err := rejectLinkLocalHost(rest); err != nil {
		return "", err
	}
	return "oci://" + rest, nil
}

// rejectLinkLocalHost blocks the highest-value SSRF target — the cloud metadata
// endpoint (169.254.169.254) and link-local range — when the host is a literal
// IP. Loopback / private ranges are deliberately allowed so local dev registries
// (localhost:5000) still work; broader private-range/DNS-resolution blocking is
// hosted-mode hardening tracked separately. Does NOT defend against a DNS name
// that resolves into these ranges.
func rejectLinkLocalHost(ref string) error {
	authority := ref
	if i := strings.IndexAny(authority, "/"); i >= 0 {
		authority = authority[:i]
	}
	host := authority
	if strings.HasPrefix(authority, "[") {
		if end := strings.Index(authority, "]"); end > 1 {
			host = authority[1:end]
		}
	} else if h, _, err := net.SplitHostPort(authority); err == nil {
		host = h
	}
	// An IPv6 zone (fe80::1%eth0, or %25eth0 before URL decoding) is not part
	// of the address; net.ParseIP rejects it, which would skip the check.
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i]
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()) {
		return fmt.Errorf("registry host %q is in the link-local range and not allowed", host)
	}
	return nil
}

// ListOCISources returns the registered OCI prefixes (normalized).
func ListOCISources() []string {
	return settings.EffectiveOCISources()
}

func ValidateOCISources(sources []string) ([]string, error) {
	result := make([]string, 0, len(sources))
	for _, source := range sources {
		prefix, err := normalizeOCIPrefix(source)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(result, prefix) {
			result = append(result, prefix)
		}
	}
	return result, nil
}

// AddOCISource registers a prefix. Idempotent: re-adding an existing prefix is a
// no-op rather than a duplicate. Returns the full updated list.
func AddOCISource(raw string) ([]string, error) {
	prefix, err := normalizeOCIPrefix(raw)
	if err != nil {
		return nil, err
	}
	updated, err := settings.Update(func(s *settings.Settings) {
		if slices.Contains(s.HelmOCISources, prefix) {
			return
		}
		s.HelmOCISources = append(s.HelmOCISources, prefix)
	})
	if err != nil {
		return nil, err
	}
	return updated.HelmOCISources, nil
}

// RemoveOCISource unregisters a prefix. Returns the full updated list.
func RemoveOCISource(raw string) ([]string, error) {
	prefix, err := normalizeOCIPrefix(raw)
	if err != nil {
		return nil, err
	}
	updated, err := settings.Update(func(s *settings.Settings) {
		kept := s.HelmOCISources[:0]
		for _, existing := range s.HelmOCISources {
			if existing != prefix {
				kept = append(kept, existing)
			}
		}
		s.HelmOCISources = kept
	})
	if err != nil {
		return nil, err
	}
	return updated.HelmOCISources, nil
}

// ociRef joins a registered prefix and chart name into the bare reference helm's
// registry client expects (no oci:// scheme): "ghcr.io/myorg/charts/mychart".
func ociRef(prefix, chartName string) string {
	return strings.TrimPrefix(prefix, "oci://") + "/" + chartName
}

// ociChartURL is ociRef with the scheme retained, for LocateChart / display.
func ociChartURL(prefix, chartName string) string {
	return strings.TrimRight(prefix, "/") + "/" + chartName
}

// ociTagLister abstracts registry.Client.Tags so discovery can be unit-tested
// without a live registry.
type ociTagLister interface {
	Tags(ref string) ([]string, error)
}

// newRegistryClientConcrete builds the timeout-bounded helm OCI registry client
// used for registry probes. Radar stores no registry secrets of its own.
func (c *Client) newRegistryClientConcrete() (*registry.Client, error) {
	return registry.NewClient(
		registry.ClientOptEnableCache(true),
		registry.ClientOptCredentialsFile(c.settings.RegistryConfig),
		// Bound every request (incl. dial) so an unreachable registered registry
		// can't stall the synchronous upgrade check.
		registry.ClientOptHTTPClient(&http.Client{Timeout: ociProbeTimeout}),
	)
}

// newRegistryClientForChartPull builds a helm OCI registry client for chart
// downloads. Pulls intentionally use helm's normal transport without the short
// timeout that bounds synchronous registry probes.
func (c *Client) newRegistryClientForChartPull() (*registry.Client, error) {
	return registry.NewClient(
		registry.ClientOptEnableCache(true),
		registry.ClientOptCredentialsFile(c.settings.RegistryConfig),
	)
}

// newRegistryClient is newRegistryClientConcrete for the discovery path. Returns
// nil (logged) on failure so discovery degrades to "not tracked" rather than
// erroring the whole upgrade check.
func (c *Client) newRegistryClient() ociTagLister {
	rc, err := c.newRegistryClientConcrete()
	if err != nil {
		log.Printf("[helm] OCI discovery disabled: failed to build registry client: %v", err)
		return nil
	}
	return rc
}
