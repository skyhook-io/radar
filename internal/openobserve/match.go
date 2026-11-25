package openobserve

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/url"
	"time"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/pkg/prom"
	oo "github.com/skyhook-io/radar/pkg/prom/openobserve"
)

type outcome int

const (
	// outcomeMatched: OpenObserve serves this context.
	outcomeMatched outcome = iota
	// outcomeUnmatched: OpenObserve answered and has no usable match.
	outcomeUnmatched
	// outcomeUnreachable: OpenObserve could not be asked.
	outcomeUnreachable
)

type decision struct {
	outcome        outcome
	match          oo.ClusterMatch
	versionWarning string
	// legacyStore: the store may ignore matchers on labels a metric never
	// had (OpenObserve before v1.2, or a version that couldn't be told), so
	// every metric is checked before use.
	legacyStore bool
	// reason explains an unmatched or unreachable outcome.
	reason string
}

// UnmatchedFix says how to match a context: OpenObserve serves only the
// clusters a user connected.
func UnmatchedFix(contextName string) string {
	return fmt.Sprintf("To use OpenObserve for this context, choose OpenObserve in Settings → Metrics or run: radar openobserve connect --context %s", contextName)
}

// matchTimeout bounds one match run (ID discovery, family detection, the
// version probe).
const matchTimeout = 20 * time.Second

// resolve matches the cluster with kube-system UID uid, which has a stored
// entry in cfg. The cluster ID label is always tried first; a stored name is
// used only when the user confirmed it, and never when it is the shared
// default name.
func resolve(ctx context.Context, conn oo.Connection, cfg config.OpenObserveConfig, uid, contextName string) decision {
	raw, err := conn.NewHTTPTransport(matchTimeout)
	if err != nil {
		return decision{outcome: outcomeUnreachable, reason: UnavailablePrefix + ": " + err.Error()}
	}
	ids, err := oo.DiscoverIDs(ctx, raw, []string{uid})
	if err != nil {
		return decision{outcome: outcomeUnreachable, reason: unavailableReason(err)}
	}
	var d decision
	stored := cfg.Matches[uid]
	if m, ok := ids[uid]; ok {
		if missing, err := oo.DetectFamilies(ctx, raw, m.Options); err == nil {
			m.Missing = missing
		} else {
			log.Printf("[openobserve] Checking which metric families have data: %v", prom.RedactURLs(err.Error()))
		}
		d = decision{outcome: outcomeMatched, match: m}
	} else if stored.By == config.OpenObserveMatchByName {
		m, err := oo.ConfirmedNameMatch(ctx, raw, stored.Label, stored.Value, uid)
		switch {
		case err == nil:
			d = decision{outcome: outcomeMatched, match: m}
		case isUnreachable(err):
			return decision{outcome: outcomeUnreachable, reason: unavailableReason(err)}
		default:
			return decision{outcome: outcomeUnmatched, reason: fmt.Sprintf("The confirmed name %s=%q no longer matches: %s. %s", stored.Label, stored.Value, trimPkgPrefix(err), UnmatchedFix(contextName))}
		}
	} else {
		return decision{outcome: outcomeUnmatched, reason: fmt.Sprintf("OpenObserve has had no metrics with this cluster's ID (%s=%s) in the last 15 minutes.", stored.Label, uid)}
	}
	// The guard stays on unless the store is positively known to filter
	// correctly; only a positive detection shows the version warning.
	legacy, err := oo.LegacyFilterBehaviour(ctx, raw)
	if err != nil {
		log.Printf("[openobserve] Checking how OpenObserve filters labels (keeping the scoping check on): %v", prom.RedactURLs(err.Error()))
	}
	d.legacyStore = err != nil || legacy
	if err == nil && legacy {
		d.versionWarning = oo.VersionWarning
	}
	return d
}

func unavailableReason(err error) string {
	return prom.RedactURLs(UnavailablePrefix + ": " + trimPkgPrefix(err))
}

func trimPkgPrefix(err error) string {
	s := err.Error()
	const p = "openobserve: "
	if len(s) > len(p) && s[:len(p)] == p {
		return s[len(p):]
	}
	return s
}

// isUnreachable reports whether err says OpenObserve could not answer, as
// opposed to answering that the name has no data.
func isUnreachable(err error) bool {
	var httpErr *prom.HTTPError
	var respErr *prom.ResponseError
	var urlErr *url.Error
	var netErr net.Error
	var limited oo.ErrRateLimited
	return errors.As(err, &httpErr) || errors.As(err, &respErr) || errors.As(err, &urlErr) ||
		errors.As(err, &netErr) || errors.As(err, &limited) ||
		errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}
