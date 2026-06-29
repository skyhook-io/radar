package k8s

import (
	"log"
	"os"
	"strconv"
	"time"
)

// Tunable deadlines and limits for the k8s layer.
//
// These were previously hardcoded constants. They were promoted to package-
// level variables to allow operators running Radar against high-latency
// API servers (SSH-tunneled clusters, geographically distant control planes,
// throttled endpoints) to widen the bounds without recompiling.
//
// Defaults preserve the original behavior bit-for-bit. Override via flags
// on cmd/explorer or via environment variables of the same name (see
// ConfigureDeadlines and EnvDurationOr / EnvIntOr).
//
// Concurrency: ConfigureDeadlines must be called before the first goroutine
// reads any of these (i.e. before InitializeK8s). No locking is provided —
// the contract is "configure once at startup".
var (
	// ContextSwitchTimeout caps the time a kubeconfig context switch can take
	// from the user clicking a context to the new client being live and the
	// minimal informer set having synced. On healthy clusters it completes in
	// a few seconds; on high-latency clusters reached over an SSH tunnel the
	// minimal-set sync alone can exceed 30s.
	ContextSwitchTimeout = 30 * time.Second

	// FirstPaintBackstop is the hard upper bound on the critical-sync wait
	// during initial cluster connect. Past this, Radar gives up waiting and
	// renders with whatever caches are warm instead of pinning on the
	// connecting screen indefinitely. Picked to be much longer than a
	// healthy cluster's sync time but short enough that a permanently-
	// throttled API server doesn't make Radar feel broken.
	FirstPaintBackstop = 5 * time.Minute

	// NamespaceListTimeout caps the cluster-wide LIST of namespaces used by
	// GetAccessibleNamespaces to decide whether the user is namespace-
	// restricted (RBAC fallback) or can enumerate everything. The original
	// 5s was tight for high-latency control planes — when the LIST times out
	// the UI shows "Limited list — RBAC doesn't allow listing all namespaces"
	// even though the real cause was a transient timeout, not RBAC.
	NamespaceListTimeout = 5 * time.Second

	// MaxScopeCandidates bounds the namespace-fallback probe fanout (only
	// reached by users who CAN list namespaces cluster-wide but CANNOT list
	// a specific kind cluster-wide). The original cap of 20 silently dropped
	// the tail on clusters with more than 20 namespaces; kinds reachable
	// only in dropped namespaces were marked denied in the UI.
	MaxScopeCandidates = 20
)

// DeadlineOptions carries the values supplied by the operator (via flags or
// env vars) to ConfigureDeadlines. Zero values mean "leave the default".
type DeadlineOptions struct {
	ContextSwitchTimeout time.Duration
	FirstPaintBackstop   time.Duration
	NamespaceListTimeout time.Duration
	MaxScopeCandidates   int
}

// ConfigureDeadlines applies any non-zero override in opts to the package
// variables above. Must be called before InitializeK8s.
//
// Validation: durations must be > 0 (zero leaves the default untouched);
// MaxScopeCandidates must be > 0 to be applied. Invalid values are logged
// and ignored — Radar continues to start with defaults rather than aborting.
func ConfigureDeadlines(opts DeadlineOptions) {
	if opts.ContextSwitchTimeout < 0 {
		log.Printf("[k8s] ignoring negative ContextSwitchTimeout %v, keeping default %v", opts.ContextSwitchTimeout, ContextSwitchTimeout)
	} else if opts.ContextSwitchTimeout > 0 {
		ContextSwitchTimeout = opts.ContextSwitchTimeout
	}

	if opts.FirstPaintBackstop < 0 {
		log.Printf("[k8s] ignoring negative FirstPaintBackstop %v, keeping default %v", opts.FirstPaintBackstop, FirstPaintBackstop)
	} else if opts.FirstPaintBackstop > 0 {
		FirstPaintBackstop = opts.FirstPaintBackstop
	}

	if opts.NamespaceListTimeout < 0 {
		log.Printf("[k8s] ignoring negative NamespaceListTimeout %v, keeping default %v", opts.NamespaceListTimeout, NamespaceListTimeout)
	} else if opts.NamespaceListTimeout > 0 {
		NamespaceListTimeout = opts.NamespaceListTimeout
	}

	if opts.MaxScopeCandidates < 0 {
		log.Printf("[k8s] ignoring negative MaxScopeCandidates %d, keeping default %d", opts.MaxScopeCandidates, MaxScopeCandidates)
	} else if opts.MaxScopeCandidates > 0 {
		MaxScopeCandidates = opts.MaxScopeCandidates
	}
}

// EnvDurationOr reads a Go duration from the named environment variable,
// returning the fallback if the variable is unset or unparseable. Invalid
// values are logged so a typo in a Deployment manifest doesn't silently
// reduce to the default.
func EnvDurationOr(envVar string, fallback time.Duration) time.Duration {
	raw := os.Getenv(envVar)
	if raw == "" {
		return fallback
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		log.Printf("[k8s] ignoring invalid %s=%q (%v), using default %v", envVar, raw, err, fallback)
		return fallback
	}
	return d
}

// EnvIntOr reads a positive int from the named environment variable, with
// the same fall-through-and-log semantics as EnvDurationOr.
func EnvIntOr(envVar string, fallback int) int {
	raw := os.Getenv(envVar)
	if raw == "" {
		return fallback
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		log.Printf("[k8s] ignoring invalid %s=%q (%v), using default %d", envVar, raw, err, fallback)
		return fallback
	}
	return n
}
