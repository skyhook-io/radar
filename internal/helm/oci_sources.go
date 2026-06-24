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
	host := ref
	if i := strings.IndexAny(host, "/"); i >= 0 {
		host = host[:i]
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if ip := net.ParseIP(host); ip != nil && (ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()) {
		return fmt.Errorf("registry host %q is in the link-local range and not allowed", host)
	}
	return nil
}

// ListOCISources returns the registered OCI prefixes (normalized).
func ListOCISources() []string {
	s := settings.Load()
	out := make([]string, 0, len(s.HelmOCISources))
	out = append(out, s.HelmOCISources...)
	return out
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

// ociUpgradeMatch is the result of probing the registered prefixes for a chart.
type ociUpgradeMatch struct {
	// LatestVersion is the newest semver tag found across registered prefixes.
	LatestVersion string
	// ChartURL is the oci:// chart reference the latest version lives at, used
	// to drive the upgrade. Always derived from a registered prefix.
	ChartURL string
}

// ociTagLister abstracts registry.Client.Tags so discovery can be unit-tested
// without a live registry.
type ociTagLister interface {
	Tags(ref string) ([]string, error)
}

// newRegistryClientConcrete builds a helm OCI registry client that authenticates
// from the user's existing `helm registry login` store (settings.RegistryConfig).
// Radar stores no registry secrets of its own.
func (c *Client) newRegistryClientConcrete() (*registry.Client, error) {
	return registry.NewClient(
		registry.ClientOptEnableCache(true),
		registry.ClientOptCredentialsFile(c.settings.RegistryConfig),
		// Bound every request (incl. dial) so an unreachable registered registry
		// can't stall the synchronous upgrade check.
		registry.ClientOptHTTPClient(&http.Client{Timeout: ociProbeTimeout}),
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

// discoverOCIUpgrade probes the registered OCI prefixes for chartName and returns
// the newest version found, or nil if no registered prefix publishes the chart.
// lister may be reused across many releases in a batch; pass nil to build one.
//
// Tags() already filters to valid semver and sorts newest-first, so tags[0] is the
// latest. tagCache dedupes repeated lookups of the same ref within a batch.
func (c *Client) discoverOCIUpgrade(chartName string, lister ociTagLister, tagCache map[string][]string) *ociUpgradeMatch {
	prefixes := ListOCISources()
	if len(prefixes) == 0 || chartName == "" {
		return nil
	}
	if lister == nil {
		lister = c.newRegistryClient()
		if lister == nil {
			return nil
		}
	}

	var best *ociUpgradeMatch
	for _, prefix := range prefixes {
		ref := ociRef(prefix, chartName)
		tags, cached := tagCache[ref]
		if !cached {
			var err error
			tags, err = lister.Tags(ref)
			if err != nil {
				// Expected when this prefix doesn't publish this chart (404) —
				// the chart may live under a different registered prefix.
				tags = nil
			}
			if tagCache != nil {
				tagCache[ref] = tags
			}
		}
		if len(tags) == 0 {
			continue
		}
		latest := tags[0]
		if best == nil || compareVersions(latest, best.LatestVersion) > 0 {
			best = &ociUpgradeMatch{LatestVersion: latest, ChartURL: ociChartURL(prefix, chartName)}
		}
	}
	return best
}

// resolveOCIUpgradeURL returns the oci:// chart URL for chartName at targetVersion
// if some registered prefix publishes that exact version. Used by the upgrade path
// so the server only ever pulls from a registered (configured) source — never a
// client-supplied ref.
func (c *Client) resolveOCIUpgradeURL(chartName, targetVersion string) (string, bool) {
	prefixes := ListOCISources()
	if len(prefixes) == 0 {
		return "", false
	}
	lister := c.newRegistryClient()
	if lister == nil {
		return "", false
	}
	for _, prefix := range prefixes {
		tags, err := lister.Tags(ociRef(prefix, chartName))
		if err != nil {
			continue
		}
		if slices.Contains(tags, targetVersion) {
			return ociChartURL(prefix, chartName), true
		}
	}
	return "", false
}
