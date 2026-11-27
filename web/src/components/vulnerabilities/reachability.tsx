import { createContext, useCallback, useContext, useMemo, useState, useSyncExternalStore } from 'react'
import { clsx } from 'clsx'
import { CheckCircle2, CircleDashed, Loader2, TriangleAlert, XCircle } from 'lucide-react'
import { InClusterConsentDialog, consentRequestRows, inClusterConsentGiven } from '@skyhook-io/k8s-ui'
import { SEVERITY_TEXT } from '@skyhook-io/k8s-ui/utils/badge-colors'
import { WithTooltip } from '../ui/Tooltip'
import { getApiBase } from '../../api/config'
import { ApiError } from '../../api/client'
import {
  fetchVulnerabilityReachabilityPlan,
  normalizeReachability,
  reachabilityRequestKeys,
  runVulnerabilityReachability,
  useVulnerabilityReachabilityCapability,
  type VulnerabilityReachability,
  type VulnerabilityReachabilityCheck,
  type VulnerabilityReachabilityOutcome,
  type VulnerabilityRoute,
  type VulnerabilityWorkloadRef,
} from '../../api/vulnerabilities'
import { routeText } from './parts'

/*
 * The in-cluster reachability check for an exposed workload. It runs only on
 * a click (it creates probe Pods), never fills a column and never reaches a
 * CSV. Its last result stays for the browser tab's session (a reload
 * included), per cluster and workload, so it can be read again from every
 * view that marks the workload.
 */

type Entry =
  | { state: 'running' }
  | { state: 'done'; result: VulnerabilityReachability }
  | { state: 'failed'; error: string }

const STORAGE_KEY = 'radar-vulnerability-reachability'
// The most results a tab keeps across a reload, newest kept.
const MAX_KEPT = 50

// Finished results only: a run in flight ends with the page, and a failure
// is for the moment it happened.
function loadKept(): Map<string, Entry> {
  const out = new Map<string, Entry>()
  try {
    const raw = sessionStorage.getItem(STORAGE_KEY)
    const kept = raw ? JSON.parse(raw) as [string, VulnerabilityReachability][] : []
    if (Array.isArray(kept)) for (const [key, result] of kept) if (typeof key === 'string' && result) out.set(key, { state: 'done', result: normalizeReachability(result) })
  } catch {
    // Storage blocked or unreadable: results last until the page reloads.
  }
  return out
}

function saveKept() {
  try {
    const done = [...results].flatMap(([key, e]) => (e.state === 'done' ? [[key, e.result] as const] : [])).slice(-MAX_KEPT)
    sessionStorage.setItem(STORAGE_KEY, JSON.stringify(done))
  } catch {
    // As above.
  }
}

const results = loadKept()
const listeners = new Set<() => void>()

function setEntry(key: string, entry: Entry | undefined) {
  results.delete(key)
  if (entry) results.set(key, entry)
  if (entry?.state !== 'running') saveKept()
  for (const l of listeners) l()
}

function subscribe(l: () => void) {
  listeners.add(l)
  return () => { listeners.delete(l) }
}

/** The cluster the results belong to, so a context switch never shows another cluster's answer. */
const ScopeContext = createContext('')

export function ReachabilityScope({ scope, children }: { scope: string; children: React.ReactNode }) {
  return <ScopeContext.Provider value={scope}>{children}</ScopeContext.Provider>
}

function useEntry(w: VulnerabilityWorkloadRef): [string, Entry | undefined] {
  const scope = useContext(ScopeContext)
  const key = `${getApiBase()}\x00${scope}\x00${w.group ?? ''}/${w.kind}/${w.namespace}/${w.name}`
  const entry = useSyncExternalStore(subscribe, () => results.get(key))
  return [key, entry]
}

/** Exactly what an outcome proves, in words. */
export const REACHABILITY = {
  action: 'Check reachability',
  answered: 'answers from inside the cluster',
  degraded: 'answers from inside the cluster, with a problem',
  noAnswer: 'gave no answer from inside the cluster',
  notRun: 'not checked',
  scope: (namespace: string) =>
    `A probe Pod in ${namespace} called the Service behind each route it checked. This proves the Service answers inside the cluster. The load balancer, node port, Ingress controller and access from the internet were not tested.`,
  /** Under a run where probes ran but none got an answer. */
  scopeNoAnswer: (namespace: string) =>
    `A probe Pod in ${namespace} called the Service behind each route it checked. Real clients can differ (a NetworkPolicy by source, mesh mTLS). The load balancer, node port, Ingress controller and access from the internet were not tested.`,
  /** Under a run where no probe Pod ran. */
  nothingRan: 'No probe Pod ran, so nothing was tested.',
}

const OUTCOME: Record<VulnerabilityReachabilityOutcome, { words: string; icon: typeof CheckCircle2; tone: string }> = {
  answered: { words: REACHABILITY.answered, icon: CheckCircle2, tone: SEVERITY_TEXT.success },
  degraded: { words: REACHABILITY.degraded, icon: TriangleAlert, tone: SEVERITY_TEXT.warning },
  no_answer: { words: REACHABILITY.noAnswer, icon: XCircle, tone: SEVERITY_TEXT.warning },
  not_run: { words: REACHABILITY.notRun, icon: CircleDashed, tone: 'text-theme-text-tertiary' },
}

function allChecks(r: VulnerabilityReachability): VulnerabilityReachabilityCheck[] {
  return (r.routes ?? []).flatMap((route) => route.checks)
}

/** What a finished run tested, or that it tested nothing. */
export function scopeOf(r: VulnerabilityReachability): string {
  const checks = allChecks(r)
  if (checks.some((c) => c.outcome === 'answered' || c.outcome === 'degraded')) return REACHABILITY.scope(r.namespace)
  if (checks.some((c) => c.outcome === 'no_answer')) return REACHABILITY.scopeNoAnswer(r.namespace)
  return REACHABILITY.nothingRan
}

/** One line for a whole run, for a cell or a workload link. */
export function reachabilitySummary(r: VulnerabilityReachability): { text: string; outcome: VulnerabilityReachabilityOutcome } {
  const base = checksSummary(r)
  // A route Radar couldn't probe (a TCPRoute, no Service port behind it)
  // keeps an all-clear from reading as the whole workload.
  const returned = r.routes ?? []
  const untested = returned.filter((route) => route.checks.length === 0).length + Math.max(0, (r.routesTotal ?? returned.length) - returned.length)
  if (untested === 0 || base.outcome === 'not_run') return base
  return { text: `${base.text}, ${untested} route${untested === 1 ? '' : 's'} not checked`, outcome: base.outcome === 'answered' ? 'degraded' : base.outcome }
}

function checksSummary(r: VulnerabilityReachability): { text: string; outcome: VulnerabilityReachabilityOutcome } {
  const checks = allChecks(r)
  const ran = checks.filter((c) => c.outcome && c.outcome !== 'not_run')
  const answered = ran.filter((c) => c.outcome === 'answered')
  // A degraded check got an answer too, with a problem.
  const responded = ran.filter((c) => c.outcome === 'answered' || c.outcome === 'degraded')
  if (ran.length === 0) return { text: 'Reachability not checked', outcome: 'not_run' }
  if (answered.length === ran.length && ran.length === checks.length) return { text: 'Answers inside the cluster', outcome: 'answered' }
  if (responded.length === 0) return { text: 'No answer inside the cluster', outcome: 'no_answer' }
  if (responded.length === checks.length) return { text: 'Answers inside the cluster, with a problem', outcome: 'degraded' }
  return { text: `${responded.length} of ${checks.length} answer inside the cluster`, outcome: 'degraded' }
}

function timeOf(iso?: string): string {
  if (!iso) return ''
  const d = new Date(iso)
  return Number.isNaN(d.getTime()) ? '' : d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

/** The last result's one-line summary, or nothing before a check. */
export function ReachabilityBadge({ workload }: { workload: VulnerabilityWorkloadRef }) {
  const [, entry] = useEntry(workload)
  if (!entry) return null
  if (entry.state === 'running') {
    return <span className="inline-flex items-center gap-1 text-xs text-theme-text-tertiary"><Loader2 className="h-3 w-3 animate-spin" />Checking reachability</span>
  }
  if (entry.state === 'failed') {
    return <WithTooltip tip={entry.error}><span className="text-xs text-warning-text">Reachability check failed</span></WithTooltip>
  }
  const s = reachabilitySummary(entry.result)
  const o = OUTCOME[s.outcome]
  const Icon = o.icon
  return (
    <WithTooltip tip={`${s.text}, checked at ${timeOf(entry.result.checkedAt)}. ${scopeOf(entry.result)}`}>
      <span className={clsx('inline-flex min-w-0 items-center gap-1 text-xs', o.tone)}>
        <Icon className="h-3 w-3 shrink-0" aria-hidden />
        <span className="truncate">{s.text}</span>
      </span>
    </WithTooltip>
  )
}

/**
 * The action and its result. The button is disabled, with the reason, when
 * the caller may not create the probe Pods or this Radar predates the check.
 */
export function ReachabilityCheck({ workload, routes, routesTotal }: {
  workload: VulnerabilityWorkloadRef
  routes: VulnerabilityRoute[]
  routesTotal?: number
}) {
  const [key, entry] = useEntry(workload)
  const capability = useVulnerabilityReachabilityCapability(workload, routes)
  const cap = capability.data
  // The consent screen's requests, read after the click. Planning sends
  // nothing; they come from the routes' and Services' specs.
  const [plan, setPlan] = useState<{ state: 'loading' } | { state: 'ready'; plan: VulnerabilityReachability } | { state: 'failed'; error: string }>()
  const requests = useMemo(() => {
    if (plan?.state !== 'ready') return []
    const seen = new Set<string>()
    // In the order the server runs them (by request), so the ones past
    // the Pod limit are the ones the dialog shows past it.
    const key = (c: VulnerabilityReachabilityCheck) => [c.route, c.target ?? '', c.inClusterRequest?.protocol ?? '', c.inClusterRequest?.scheme ?? '', c.inClusterRequest?.host ?? '', c.inClusterRequest?.path ?? ''].join('\x1f')
    const checks = (plan.plan.routes ?? []).flatMap((r) => r.checks).sort((a, b) => (key(a) < key(b) ? -1 : key(a) > key(b) ? 1 : 0))
    return consentRequestRows(checks, '').filter((row) => {
      const id = `${row.route}\x00${row.request}`
      if (seen.has(id)) return false
      seen.add(id)
      return true
    })
  }, [plan])
  const running = entry?.state === 'running'

  // clusterKey is the cluster the user confirmed (or chose not to be asked
  // about again): the run is refused if Radar has switched since.
  // A check refused because one is already running (429) isn't a result:
  // the last one stays, and the refusal shows here only.
  const [busy, setBusy] = useState<string>()
  const run = useCallback(async (clusterKey: string | undefined, requests?: string[]) => {
    const previous = results.get(key)
    setBusy(undefined)
    setEntry(key, { state: 'running' })
    try {
      const result = await runVulnerabilityReachability(workload, routes, clusterKey, requests)
      // The routes this check left out (past the ones the page lists) stay
      // with the result, so its summary never reads as the whole workload.
      setEntry(key, { state: 'done', result: { ...result, routesTotal: Math.max(routesTotal ?? routes.length, routes.length) } })
    } catch (e) {
      if (e instanceof ApiError && e.status === 429) {
        setEntry(key, previous?.state === 'running' ? undefined : previous)
        setBusy(e.message)
        return
      }
      setEntry(key, { state: 'failed', error: e instanceof Error ? e.message : String(e) })
    }
  }, [key, workload, routes, routesTotal])

  const start = useCallback(async () => {
    if (inClusterConsentGiven(cap?.clusterKey)) return run(cap?.clusterKey)
    setPlan({ state: 'loading' })
    try {
      setPlan({ state: 'ready', plan: await fetchVulnerabilityReachabilityPlan(workload, routes) })
    } catch (e) {
      setPlan({ state: 'failed', error: e instanceof Error ? e.message : String(e) })
    }
  }, [cap, run, workload, routes])

  let disabledReason: string | undefined
  if (capability.support === 'unsupported') disabledReason = 'Reachability checks need a newer Radar.'
  else if (capability.isLoading) disabledReason = 'Checking your permissions…'
  else if (capability.error) disabledReason = `Couldn't check your permissions: ${capability.error instanceof Error ? capability.error.message : 'request failed'}.`
  else if (cap && !cap.allowed) disabledReason = `Not available: ${cap.reason ?? 'permission denied'}. The check creates short-lived probe Pods.`
  const preparing = plan?.state === 'loading'
  const nothingToProbe = plan?.state === 'ready' && requests.length === 0

  const button = (
    <button
      type="button"
      disabled={!!disabledReason || running || preparing}
      onClick={start}
      className="inline-flex items-center gap-1 text-accent-text hover:underline disabled:cursor-not-allowed disabled:text-theme-text-tertiary disabled:no-underline"
    >
      {(running || preparing) && <Loader2 className="h-3 w-3 animate-spin" />}
      {running ? 'Checking reachability…' : preparing ? 'Preparing the check…' : entry?.state === 'done' ? 'Check again' : REACHABILITY.action}
    </button>
  )

  return (
    <div className="flex min-w-0 max-w-3xl flex-col gap-1.5 text-xs" data-reachability-check>
      <span className="flex flex-wrap items-baseline gap-x-2">
        {disabledReason ? <WithTooltip tip={disabledReason}><span>{button}</span></WithTooltip> : button}
        {disabledReason && cap && !cap.allowed && <span className="text-theme-text-tertiary">{disabledReason}</span>}
        {!disabledReason && !entry && !running && (
          <span className="text-theme-text-tertiary">
            Starts up to {cap?.maxProbes ?? 5} short-lived probe Pods in {workload.namespace}, one per Service port behind its routes
            {(routesTotal ?? routes.length) > routes.length ? `. Covers ${routes.length} of its ${routesTotal} routes` : ''}.
          </span>
        )}
        {running && <span className="text-theme-text-tertiary">Starting probe Pods in {workload.namespace}; this can take up to a minute.</span>}
      </span>
      {plan?.state === 'failed' && <span className="text-warning-text">Couldn't prepare the check: {plan.error}</span>}
      {nothingToProbe && (
        <span className="text-theme-text-tertiary">
          Nothing to probe: {(plan.plan.routes ?? []).map((r) => r.note).filter(Boolean).join(' ') || 'Radar found no Service port behind these routes.'}
        </span>
      )}
      {busy && !running && <span className="text-theme-text-secondary">{busy}</span>}
      {entry?.state === 'failed' && <span className="text-warning-text">The check didn't run: {entry.error}</span>}
      {entry?.state === 'done' && <ReachabilityResult result={entry.result} />}
      {plan?.state === 'ready' && (
        <InClusterConsentDialog
          open={requests.length > 0}
          cluster={plan.plan.cluster}
          clusterKey={plan.plan.clusterKey}
          namespace={plan.plan.namespace}
          requests={requests}
          maxProbes={plan.plan.maxProbes}
          onClose={() => setPlan(undefined)}
          onConfirm={() => { const confirmed = plan.plan; setPlan(undefined); run(confirmed.clusterKey, reachabilityRequestKeys(confirmed)) }}
        />
      )}
    </div>
  )
}

function ReachabilityResult({ result }: { result: VulnerabilityReachability }) {
  return (
    <div className="flex min-w-0 flex-col gap-1">
      {(result.routes ?? []).map((r) => (
        <div key={routeText(r.route)} className="flex min-w-0 flex-col gap-0.5">
          <span className="text-theme-text-secondary">{routeText(r.route)}</span>
          {r.note && r.checks.length === 0 && <span className="pl-3 text-theme-text-tertiary">{r.note}</span>}
          {r.checks.map((c) => <CheckLine key={`${c.route}\x00${c.target ?? ''}`} check={c} />)}
        </div>
      ))}
      <span className="text-theme-text-tertiary">
        {timeOf(result.checkedAt) && `Checked at ${timeOf(result.checkedAt)}. `}{scopeOf(result)}
      </span>
    </div>
  )
}

function CheckLine({ check: c }: { check: VulnerabilityReachabilityCheck }) {
  const o = OUTCOME[c.outcome ?? 'not_run']
  const Icon = o.icon
  const what = c.target ? `Service ${c.target}` : c.route
  return (
    <span className="flex min-w-0 flex-col pl-3">
      <span className={clsx('flex min-w-0 items-baseline gap-1.5', o.tone)}>
        <Icon className="h-3 w-3 shrink-0 self-center" aria-hidden />
        <span>
          <span className="font-mono">{what}</span> {o.words}
          {c.detail && <span className="text-theme-text-secondary">: {c.detail}</span>}
        </span>
      </span>
      {c.reason && <span className="pl-[18px] text-theme-text-tertiary">{c.reason}</span>}
      {c.fallbackCommand && c.outcome !== 'answered' && (
        <WithTooltip tip="Run this yourself to repeat the probe.">
          <code className="ml-[18px] mt-0.5 truncate rounded bg-theme-elevated px-1.5 py-0.5 font-mono text-[11px] text-theme-text-secondary">{c.fallbackCommand}</code>
        </WithTooltip>
      )}
    </span>
  )
}
