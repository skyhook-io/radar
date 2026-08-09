import type { ComponentType } from 'react'
import { CheckCircle2, AlertTriangle, ShieldAlert, Info } from 'lucide-react'
import { AlertBanner } from '../ui/drawer-components'
import { coverageBannerTone, routeOutcomeRank } from './TracePanel'
import type { Trace } from './types'

type Tone = ReturnType<typeof coverageBannerTone>

const ICON: Record<Tone, ComponentType<{ className?: string }>> = {
  success: CheckCircle2,
  warning: AlertTriangle,
  error: ShieldAlert,
  info: Info,
}

/** The passive drawer view-model - extracted as a pure function so the decision
 *  logic (tone, worst-offender-when-failing, CTA label) is unit-testable without
 *  a DOM. `minimal` = no coverage projection yet (static/config-only); `glance` =
 *  a coverage headline with its single honest tone. */
export interface TraceSummaryView {
  kind: 'glance' | 'minimal'
  headline: string
  tone?: Tone
  /** The failing route to spotlight under the headline, with a count of how many
   *  routes failed in total - so the label can read honestly ("Failing route"
   *  for one, "Worst of N failing routes" when there are more). */
  worst?: { route: string; target?: string; evidence?: string; failingCount: number }
  /** A muted descriptive line under the headline. */
  subtitle?: string
  notTested: number
  ctaLabel: string
}

// The drawer never runs active probes (that emits real traffic - deliberate, see
// client.ts useTrace), so before a run it only INVITES the test, never implies one
// ran. The "Reachability · Network Path" section header already carries the word
// "reachability", so the button drops it ("Run test →") to avoid the echo.
const RUN_CTA_SHORT = 'Run test →'

export function summarizeTrace(trace: Trace): TraceSummaryView {
  const coverage = trace.coverage
  const routes = trace.routes ?? []

  // PROBED - actual probe results exist (tested > 0) → the route-coverage glance.
  // A coverage projection with tested === 0 is NOT a tested result (the drawer never
  // probes), so it falls through to the invite-to-test button below.
  if (coverage && coverage.tested > 0 && trace.headline) {
    const tone = coverageBannerTone(coverage, routes)
    const failing = coverage.failed > 0
    // The worst-offender line shows ONLY when a route genuinely failed. Benign
    // scale-to-0 is intentional dormancy, not a failure to call out.
    const failingRoutes = failing
      ? [...routes]
          // An apiserver-proxy-only (indirect) unreachable never confirmed the real
          // path - it's amber in coverageBannerTone, so it must NOT be spotlighted as
          // a definitive "Failing route" here. A genuinely-down backend is upgraded to
          // confidence 'real' upstream (definitive 0-ready endpoints), so it still shows.
          .filter((r) => (r.outcome === 'server-error' || (r.outcome === 'unreachable' && r.confidence !== 'indirect')) && !r.benign)
          .sort((a, b) => routeOutcomeRank(a.outcome) - routeOutcomeRank(b.outcome))
      : []
    const worstRoute = failingRoutes[0]
    return {
      kind: 'glance',
      headline: trace.headline,
      tone,
      worst: worstRoute && {
        route: worstRoute.route,
        target: worstRoute.target,
        evidence: worstRoute.evidence,
        failingCount: failingRoutes.length,
      },
      notTested: trace.notTested?.length ?? coverage.skipped,
      // Calm when healthy ("see detail"), actionable when failing ("open").
      ctaLabel: failing ? 'Open Reachability →' : 'See test detail →',
    }
  }

  // Nothing actively tested. The drawer does NOT detect config issues - those are
  // owned by the resource's Operational Issues section (main's central detector). The
  // reachability glance is purely the invitation to run the live path test; it never
  // re-states anything the config already shows above it.
  //
  // For a PATH-OWNING subject (Ingress/Gateway/Route) it frames the front door;
  // the host→backend wiring is already in the resource's own Rules section.
  if (PATH_OWNING_KINDS.has(trace.subject.kind)) {
    return {
      kind: 'minimal',
      headline: 'Verify the path in through the front door',
      subtitle: 'A quick live probe of the declared host and path.',
      notTested: 0,
      ctaLabel: RUN_CTA_SHORT,
    }
  }
  //    For a Service/Pod subject the Ports section already shows the port →
  //    running-pod wiring, so restating it here ("routes to N pods on :80") just
  //    duplicates it. Frame the empty state around the VALUE of the live test
  //    (positive, action-first) - never a negative "not tested yet".
  return {
    kind: 'minimal',
    headline: 'Verify the live path to these pods',
    subtitle: 'A quick live connection test from where Radar runs.',
    notTested: 0,
    ctaLabel: RUN_CTA_SHORT,
  }
}

// Subjects whose own renderer already prints their routing rules (host→backend),
// so the reachability glance must NOT restate the wiring - and "routes to N
// pods" would overclaim a front door it never verified.
const PATH_OWNING_KINDS = new Set(['Ingress', 'Gateway', 'HTTPRoute', 'GRPCRoute'])

/**
 * TraceSummary is the PASSIVE drawer glance for the network-reachability path:
 * one honest headline (tone from coverageBannerTone - the SAME single tone source
 * the full panel uses), a worst-offender line ONLY when something failed, a muted
 * not-tested count, and ONE CTA into the full Reachability tab. It never nags - a
 * healthy verified path reads as a calm one-liner. The full route matrix, per-route
 * localization, path topology, and the in-cluster test all live on the tab
 * (TracePanel), reachable via the CTA. The drawer stays a glance, not a console.
 */
export function TraceSummary({ trace, onOpenReachability }: { trace: Trace; onOpenReachability: () => void }) {
  const v = summarizeTrace(trace)

  if (v.kind === 'minimal') {
    return (
      <div className="flex flex-col gap-1">
        <div className="flex items-center justify-between gap-2 text-sm text-theme-text-secondary">
          <span className="truncate">{v.headline}</span>
          <OpenButton onClick={onOpenReachability} label={v.ctaLabel} />
        </div>
        {v.subtitle && <span className="text-xs text-theme-text-tertiary">{v.subtitle}</span>}
      </div>
    )
  }

  return (
    <AlertBanner variant={v.tone!} icon={ICON[v.tone!]} title={v.headline}>
      {v.worst && (
        <div className="mt-1 text-xs">
          <span className="text-theme-text-secondary">
            {v.worst.failingCount > 1 ? `Worst of ${v.worst.failingCount} failing routes: ` : 'Failing route: '}
          </span>
          <span className="font-mono text-theme-text-primary break-all">{v.worst.route}</span>
          {v.worst.target && <span className="text-theme-text-tertiary break-all"> → {v.worst.target}</span>}
          {v.worst.evidence && <span className="text-theme-text-tertiary"> · {v.worst.evidence}</span>}
        </div>
      )}
      {v.subtitle ? (
        <div className="mt-1 text-xs text-theme-text-tertiary">{v.subtitle}</div>
      ) : (
        v.notTested > 0 && <div className="mt-1 text-xs text-theme-text-tertiary">{v.notTested} not tested</div>
      )}
      <OpenButton onClick={onOpenReachability} label={v.ctaLabel} />
    </AlertBanner>
  )
}

function OpenButton({ onClick, label }: { onClick: () => void; label: string }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className="mt-2 text-xs px-2 py-1 rounded border border-theme-border bg-theme-surface text-theme-text-primary hover:bg-theme-hover transition-colors"
    >
      {label}
    </button>
  )
}
