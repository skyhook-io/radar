// Package health is the canonical, pure resource health-classification engine
// for Radar. It lives in pkg/ (not internal/) so every consumer — topology,
// timeline, packages, resourcecontext, the REST/MCP servers — can share ONE
// classifier instead of the parallel copies that drifted historically.
//
// It is a pure leaf: it imports only the Kubernetes API types, the standard
// library, and other pkg/ leaves. It must never import anything from internal/,
// nor from the consumer packages (timeline/packages/topology) that depend on it —
// the per-surface adapters (Level → timeline.HealthState etc.) live on the
// consumer side so the dependency arrow only ever points inward.
//
// Health LEVEL (this package) is distinct from issue/finding SEVERITY
// (critical/alert/warning/info — owned by the Problems, Audit, and GitOps
// Insights subsystems). This package classifies "how is this resource doing";
// it does not rank findings.
package health

// Level is the canonical resource-health vocabulary. Five tiers, ordered from
// most-benign to most-severe by Rank below.
//
// There is deliberately no "alert" tier here: alert (orange) is an issue/finding
// severity, not a resource-health level — no core resource produces an "alert"
// health state, and the only frontend emitters of it are bespoke per-CRD
// renderers (Crossplane, NVIDIA, …) that classify locally. Keeping it out of the
// backend Level keeps the operator's color vocabulary to the smallest set that
// maps to distinct actions.
type Level string

const (
	// LevelHealthy: confirmed good.
	LevelHealthy Level = "healthy"
	// LevelNeutral: intentional / lifecycle state — scaled-to-zero, suspended,
	// completed, idle. Not a problem; must NOT pull an Unhealthy filter or a
	// rollup. Aggregates as most-benign (ties healthy in WorseOf).
	LevelNeutral Level = "neutral"
	// LevelDegraded: partial impairment — some-but-not-all ready, transient
	// problem past its grace window.
	LevelDegraded Level = "degraded"
	// LevelUnhealthy: genuine failure — crashloop, OOM, fatal waiting, none ready.
	LevelUnhealthy Level = "unhealthy"
	// LevelUnknown: no signal / unobserved. Ranks just above healthy so a
	// no-data row is not promoted to "healthy" by a rollup, but below any real
	// impairment.
	LevelUnknown Level = "unknown"
)

// Rank orders the levels for worst-of aggregation. neutral ties healthy at 0 —
// an all-neutral set aggregates as benign, NOT as a problem — while unknown sits
// just above healthy (a "we don't know" row should not be reported as healthy,
// but is quieter than any genuine impairment).
func Rank(l Level) int {
	switch l {
	case LevelHealthy, LevelNeutral:
		return 0
	case LevelUnknown:
		return 1
	case LevelDegraded:
		return 2
	case LevelUnhealthy:
		return 3
	}
	// Unrecognized vocabulary maps to the unknown rank — quieter than any real
	// impairment, but still not promoted to healthy.
	return 1
}

// WorseOf returns the more-severe of two levels by Rank, commutatively. An empty
// Level is "no opinion" — the other side wins.
//
// healthy and neutral both rank 0 (most-benign), so a rollup must break that tie
// deterministically rather than by fold order: healthy wins. A set that mixes
// running (healthy) and intentionally-off (neutral) workloads reads healthy — the
// app has something running — and only an ALL-neutral set reads neutral. Every
// other rank is held by exactly one level, so equal non-zero ranks mean the two
// sides are the same level.
func WorseOf(a, b Level) Level {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	ra, rb := Rank(a), Rank(b)
	if rb > ra {
		return b
	}
	if ra > rb {
		return a
	}
	// Equal rank. The only collision is healthy vs neutral (both rank 0): prefer
	// healthy unless both are neutral.
	if ra == 0 && (a == LevelHealthy || b == LevelHealthy) {
		return LevelHealthy
	}
	return a
}

// Verdict is the full result of classifying a resource: its Level, a short
// machine Reason token (e.g. "CrashLoopBackOff", "Completed") when the Level is
// not plainly healthy, and an optional human Message (the kubelet detail behind
// the reason). Reason/Message let the Problems and timeline surfaces reuse the
// classification without a second pass.
type Verdict struct {
	Level   Level
	Reason  string
	Message string
}

// LegacyString projects a Verdict onto the legacy three-value pod-health
// vocabulary ("healthy" | "warning" | "error") that ClassifyPodHealth returned
// before this package existed. Kept so the internal/k8s shim is a provable no-op
// for callers (dashboards, MCP counters) that still switch on those strings.
func (v Verdict) LegacyString() string {
	switch v.Level {
	case LevelDegraded:
		return "warning"
	case LevelUnhealthy:
		return "error"
	default:
		// healthy, neutral (e.g. Succeeded), unknown all read as "healthy" in
		// the legacy vocabulary — matching the pre-split ClassifyPodHealth, whose
		// default and Succeeded branches both returned "healthy".
		return "healthy"
	}
}
