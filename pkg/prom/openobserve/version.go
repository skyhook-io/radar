package openobserve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

// StoreVersion is an OpenObserve release version.
type StoreVersion struct {
	Raw                 string
	Major, Minor, Patch int
}

// Before reports whether v is older than major.minor.
func (v StoreVersion) Before(major, minor int) bool {
	if v.Major != major {
		return v.Major < major
	}
	return v.Minor < minor
}

// ParseVersion reads "v1.2.0", "1.1.2" or "v1.2.0-rc3".
func ParseVersion(s string) (StoreVersion, bool) {
	raw := strings.TrimSpace(s)
	core := strings.TrimPrefix(strings.TrimPrefix(raw, "v"), "V")
	if i := strings.IndexAny(core, "-+ "); i >= 0 {
		core = core[:i]
	}
	parts := strings.Split(core, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return StoreVersion{}, false
	}
	nums := make([]int, 3)
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return StoreVersion{}, false
		}
		nums[i] = n
	}
	return StoreVersion{Raw: raw, Major: nums[0], Minor: nums[1], Patch: nums[2]}, true
}

// ErrVersionUnknown is returned by FetchVersion when neither config endpoint
// says which version the store runs.
var ErrVersionUnknown = errors.New("openobserve: couldn't read the OpenObserve version")

const maxConfigBytes = 1 << 20

// FetchVersion reads the OpenObserve version with the client, headers (the
// basic auth) and organization of t, a transport rooted at the
// organization's Prometheus API (Connection.NewHTTPTransport). OpenObserve
// v1.1 and later report it at the authenticated <base>/api/<org>/config;
// older versions in the public <base>/config.
func FetchVersion(ctx context.Context, t *prom.HTTPTransport) (StoreVersion, error) {
	root := strings.TrimSuffix(strings.TrimRight(t.BaseURL+t.BasePath, "/"), "/prometheus")
	base := root
	if i := strings.LastIndex(root, "/api/"); i >= 0 {
		base = root[:i]
	}
	var errs []error
	for _, u := range []string{root + "/config", base + "/config"} {
		v, err := fetchConfigVersion(ctx, t, u)
		if err == nil {
			return v, nil
		}
		errs = append(errs, err)
	}
	return StoreVersion{}, fmt.Errorf("%w: %w", ErrVersionUnknown, errors.Join(errs...))
}

func fetchConfigVersion(ctx context.Context, t *prom.HTTPTransport, u string) (StoreVersion, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return StoreVersion{}, err
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range t.Headers {
		req.Header.Set(k, v)
	}
	client := t.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := prom.SameOriginRedirectClient(client).Do(req)
	if err != nil {
		return StoreVersion{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxConfigBytes+1))
	if err != nil {
		return StoreVersion{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return StoreVersion{}, &prom.HTTPError{StatusCode: resp.StatusCode, URL: u}
	}
	if len(body) > maxConfigBytes {
		return StoreVersion{}, prom.ErrResponseTooLarge
	}
	var doc struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return StoreVersion{}, fmt.Errorf("parse %s: %w", u, err)
	}
	v, ok := ParseVersion(doc.Version)
	if !ok {
		return StoreVersion{}, fmt.Errorf("%s has no version", u)
	}
	return v, nil
}

// StoreBehaviour is how far an OpenObserve store can be trusted to apply a
// cluster's matchers, from its version or, when that can't be read, from
// probing it.
type StoreBehaviour struct {
	// Version is the store's version, "" when it couldn't be read.
	Version string
	// LegacyFilter: the store may ignore a matcher on a label a metric never
	// had (before v1.2, or a version that couldn't be told). Set
	// RewritingTransport.LegacyStore from it.
	LegacyFilter bool
	// LegacyKnown: LegacyFilter was determined, not assumed. Show
	// VersionWarning when both are set.
	LegacyKnown bool
	// LabelValuesUnscoped: label value and name lookups may ignore match[]
	// entirely (before v1.1, or a version that couldn't be read). Set
	// RewritingTransport.LabelValuesUnscoped from it.
	LabelValuesUnscoped bool
}

// DetectStoreBehaviour reads the store's version through raw when raw is a
// *prom.HTTPTransport, and otherwise, or when the version can't be read,
// probes the filter behaviour. Anything it can't tell is assumed unsafe; err
// says what couldn't be told and is for logging only.
func DetectStoreBehaviour(ctx context.Context, raw prom.Transport) (StoreBehaviour, error) {
	var verr error = ErrVersionUnknown
	if ht, ok := raw.(*prom.HTTPTransport); ok {
		var v StoreVersion
		if v, verr = FetchVersion(ctx, ht); verr == nil {
			legacy := v.Before(1, 2)
			// OpenObserve before v1.1 answers a label lookup with
			// SELECT DISTINCT over the whole metric, whatever match[] says.
			return StoreBehaviour{Version: v.Raw, LegacyFilter: legacy, LegacyKnown: true, LabelValuesUnscoped: v.Before(1, 1)}, nil
		}
	}
	legacy, perr := probeLegacyFilter(ctx, raw)
	b := StoreBehaviour{LegacyFilter: perr != nil || legacy, LegacyKnown: perr == nil, LabelValuesUnscoped: true}
	if perr != nil {
		return b, errors.Join(verr, perr)
	}
	return b, verr
}
