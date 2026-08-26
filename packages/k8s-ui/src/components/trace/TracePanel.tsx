import { useState, useCallback, useEffect, useRef } from 'react'
import { Copy, Check, Loader2, Info, RefreshCw, MoreHorizontal, Pencil, Network } from 'lucide-react'
import { Tooltip } from '../ui/Tooltip'
import type { Trace, Finding, FindingSeverity, ProbeResult, ResourceRef, RouteResult, Coverage } from './types'
import { Badge } from '../ui/Badge'
import { StatusDot, type StatusTone } from '../ui/status-tone'

export interface TracePanelProps {
  trace: Trace | undefined
  isLoading?: boolean
  error?: Error | null
  /** A reachability-probe run that failed. Rendered inline (NOT as a
   *  full-panel error) so the valid static trace stays visible - the user
   *  clicked "run test", it failed, and they see why instead of nothing. */
  probeError?: Error | null
  /** Optional: parent-provided refetch action surfaced when the trace verdict
   *  is unknown - gives the operator a way to retry after RBAC/cache changes
   *  without forcing a drawer close+reopen. */
  onRefresh?: () => void
  /** True when the next refetch should request reachability probes. The
   *  parent passes back the resulting Trace via the trace prop. Probes are
   *  one-shot - the parent should reset this to false after results arrive. */
  probeRequested?: boolean
  /** Callback when the operator asks for a reachability test. The parent
   *  is expected to set probeRequested=true and trigger a refetch; results
   *  flow back through `trace.downstream[].probes`. */
  onRunProbes?: () => void
  /** True once a probe run has COMPLETED (the parent holds a probed trace),
   *  even if that probe found nothing testable (e.g. an ExternalName, or every
   *  route skipped from this vantage). Lets the view distinguish "not yet
   *  probed" from "probed, no result" so clicking Run is never a silent no-op. */
  probed?: boolean
  /** When provided, the resource label on each hop becomes clickable and
   *  invokes this with the hop's ResourceRef so the host can open the
   *  resource's detail view. Hops without a routable name (collections like
   *  the Pods fan-out) stay non-clickable. Without this prop, hop rows are
   *  inert text and the only interaction is the expand chevron. */
  onNavigateToResource?: (ref: ResourceRef) => void
  /** One-click "test every path from inside the cluster" - runs the in-cluster
   *  probe for the whole subject and merges the real-traffic results back into the
   *  trace, filling the matrix's "Real in-cluster traffic" column + upgrading the
   *  verdict. Shown only when the in-cluster test is actually permitted. */
  onRunInCluster?: () => void
  inClusterRunning?: boolean
  inClusterAllowed?: boolean
  /** WHY the in-cluster test is denied, from the server's capability check -
   *  it names the first missing verb ("you don't have permission to create
   *  Jobs in …"). Without it every denial rendered the same generic line. */
  inClusterDeniedReason?: string
  /** Set when the in-cluster test couldn't produce a result (e.g. a cold-start
   *  timeout) - surfaced so the click is never a silent no-op. */
  inClusterError?: string
  /** True when the run that set inClusterError still produced results for OTHER
   *  routes (folded into the merged trace) - e.g. trailing routes hit the
   *  per-call pod cap or the request time budget. The banner then reads
   *  "partially completed" instead of the false "couldn't run". */
  inClusterPartial?: boolean
  /** The copyable kubectl command to run the probe by hand when the in-cluster
   *  Job couldn't run - paired with inClusterError. */
  inClusterFallback?: string
  /** True when the in-cluster run produced results but folded NONE of them into
   *  the merged trace (every route was override-mismatch / throwaway-denied /
   *  guessed path) - route outcomes are unchanged. Surfaced as an informational
   *  note so the run isn't a silent no-op. Mutually exclusive with inClusterError. */
  inClusterEvidenceOnly?: boolean
  /** The evidence row's honest status message, paired with inClusterEvidenceOnly. */
  inClusterEvidenceNote?: string
  /** The HTTP path the probes request (default "/"); shown as the tested-request
   *  indicator. onApplyProbePath sets a new path and re-runs. */
  probePath?: string
  onApplyProbePath?: (path: string) => void
  /** Bumped by the host every time a test run COMPLETES - drives the transient
   *  "updated just now" confirmation so an identical-looking result still reads as fresh. */
  runNonce?: number
  /** Wall-clock time the displayed probe/in-cluster results were produced.
   *  Rendered as "tested HH:MM:SS" so even a kept snapshot is honestly dated. */
  testedAt?: Date
  /** True when the polled cluster state changed under a shown probe snapshot and
   *  the host dropped it - the panel shows a notice explaining the fallback to
   *  live state and inviting a re-run. */
  clusterChangedSinceTest?: boolean
}

/** Whether an in-cluster reachability test can run for this caller, and the
 *  cluster + namespace the probe pod would be created in (the safety rail). */
export interface InClusterCapability {
  allowed: boolean
  reason?: string
  cluster?: string
  clusterKey?: string
  namespace: string
  /** Per-call ceiling on probe Pods. Routes past it never run - ordering is
   *  deterministic, so re-running starts from the same first maxProbes. */
  maxProbes?: number
}

type AlertTone = 'success' | 'warning' | 'error' | 'info'

// coverageBannerTone derives the SINGLE banner tone from what we actually
// tested - never the legacy verdict word, which can over-claim a confident
// "healthy" on apiserver-only evidence. Honesty rules: indirect-only and
// zero-tested are never green (the real-traffic path was not confirmed);
// not-tested is never red.
export function coverageBannerTone(coverage: Coverage, routes: RouteResult[]): AlertTone {
  if (coverage.tested === 0) return 'info' // nothing actively tested
  if (coverage.passed === 0 && coverage.failed > 0) {
    // Red ONLY when something is genuinely unreachable. Failures that still
    // REACHED the server are amber, not red - a 5xx (degraded: traffic passed,
    // the app errored) or an intentional scale-to-0 (benign dormancy) - which
    // keeps the banner consistent with the degraded verdict.
    const hardUnreach = routes.filter((r) => r.outcome === 'unreachable' && !r.benign)
    // An apiserver-proxy-only (indirect) failure must NEVER set a red headline -
    // the real path was never tested (mirrors reachVerdict's onlyIndirectUnreach).
    // When every unreachable route is indirect, warn, don't condemn.
    const anyRealUnreach = hardUnreach.some((r) => r.confidence !== 'indirect')
    if (hardUnreach.length > 0) return anyRealUnreach ? 'error' : 'warning'
    return 'warning'
  }
  if (coverage.failed > 0) return 'warning' // some reachable, some not
  // Every tested route passed - green ONLY if a real-traffic path was VERIFIED.
  // A route that only 'reached' a server (3xx/4xx, route not verified) is not
  // confirmation; including it would overclaim a green headline while the coverage
  // strip below stays neutral.
  const realPass = routes.some((r) => r.confidence === 'real' && r.outcome === 'verified')
  if (!realPass) return 'info'
  // Green ONLY when coverage is complete. A skipped/not-tested route, or a
  // proxy-only (indirect) unreachable route whose real path was never confirmed,
  // is a coverage gap - down-rank to info so the drawer glance never greens a
  // partial result (mirrors the backend CoverageVerdict and the full-tab banner).
  const hasGap =
    (coverage.skipped ?? 0) > 0 || routes.some((r) => r.outcome === 'unreachable' && r.confidence === 'indirect')
  return hasGap ? 'info' : 'success'
}

// routeOutcomeRank sorts the matrix failures-first. Exported for the test.
export function routeOutcomeRank(o: RouteResult['outcome']): number {
  switch (o) {
    case 'unreachable': return 0
    case 'server-error': return 1
    case 'reached': return 2
    case 'verified': return 3
    default: return 4
  }
}

// inClusterOutcome reduces real-dataplane probe results to a route label/tone.
// A 2xx HERE is real-traffic verified - the payoff over the indirect API-proxy read.
export function inClusterOutcome(results: ProbeResult[]): { label: string; tone: StatusTone; severity: 'error' | 'warning' | 'info' | 'success' | 'neutral' } {
  // A SKIPPED probe has ok=false but is NOT a failure - classifying it would
  // false-condemn a port that actually connected. Drop skips first so a skipped
  // HTTP probe falls through to the TCP "port reachable" / "tested" branches.
  const real = results.filter((r) => !r.skipped)
  const http = real.find((r) => r.layer === 'http')
  if (http) {
    if (http.ok && http.tone === 'healthy') return { label: 'verified', tone: 'healthy', severity: 'success' }
    if (http.ok && http.tone === 'reached') return { label: 'reached', tone: 'neutral', severity: 'info' }
    if (http.ok && http.tone === 'degraded') return { label: 'server error', tone: 'degraded', severity: 'warning' }
    if (!http.ok) return { label: 'unreachable', tone: 'unhealthy', severity: 'error' }
  }
  const tcp = real.find((r) => r.layer === 'tcp')
  if (tcp) return tcp.ok ? { label: 'port reachable', tone: 'healthy', severity: 'success' } : { label: 'unreachable', tone: 'unhealthy', severity: 'error' }
  return { label: 'tested', tone: 'neutral', severity: 'neutral' }
}

// inClusterEligible: an indirect (API-proxy-only) or failed route is worth
// confirming from inside the cluster. A real-traffic verified route already is.
export function inClusterEligible(r: RouteResult): boolean {
  return !r.benign && (r.confidence === 'indirect' || r.outcome === 'unreachable' || r.outcome === 'server-error')
}

// JustTestedNote shows a brief "✓ updated just now" next to the verdict whenever a
// test completes, giving closure even when the result is identical to the prior run (a
// fast probe otherwise looks like nothing happened, since only the button moved). Driven
// by a run nonce the host bumps on every completed run, so it fires even with no value change.
function useFreshFlag(nonce?: number): boolean {
  const [fresh, setFresh] = useState(false)
  const prev = useRef<number | undefined>(nonce)
  useEffect(() => {
    if (prev.current !== undefined && nonce !== prev.current) {
      setFresh(true)
      const t = setTimeout(() => setFresh(false), 2600)
      prev.current = nonce
      return () => clearTimeout(t)
    }
    prev.current = nonce
  }, [nonce])
  return fresh
}
export function JustTestedNote({ nonce }: { nonce?: number }) {
  const fresh = useFreshFlag(nonce)
  if (!fresh) return null
  return (
    <span className="ml-2 inline-flex shrink-0 items-center rounded px-1.5 py-0.5 text-[10px] font-medium text-accent-text" style={{ backgroundColor: 'var(--accent-muted)' }}>
      ✓ updated just now
    </span>
  )
}

// VerdictCaveat renders the verdict's muted second line: a short one-liner on
// screen, with the full explanation tucked into the project Tooltip behind an
// info icon - so a long "why + how to fix" never bloats the banner into a wall
// of text.
export function VerdictCaveat({ caveat, detail }: { caveat?: string; detail?: string }) {
  if (!caveat) return null
  return (
    <div className="text-xs text-theme-text-tertiary mt-0.5 flex items-start gap-1">
      <span className="min-w-0">{caveat}</span>
      {detail ? (
        <Tooltip content={<span className="block max-w-xs leading-snug">{detail}</span>} position="bottom">
          <Info className="w-3.5 h-3.5 mt-px shrink-0 cursor-help text-theme-text-tertiary hover:text-theme-text-secondary" aria-label="More detail" />
        </Tooltip>
      ) : null}
    </div>
  )
}

// RequestIndicator names the exact request the probes made and WHEN - shown
// in the verdict bar (always visible after a run) so the operator knows what was
// tested and that the result is a dated snapshot, not a live read.
export type RequestMode = 'http' | 'tcp' | 'mixed' | 'none'

/** What actually RAN, derived from the folded trace: which declared TCP-only
 *  targets saw a live TCP dial, and whether any live HTTP request completed.
 *  Feeds RequestIndicator so "tested with GET /" is never claimed for a run
 *  that only connected a socket - or never connected anything. */
export function completedRequestMode(trace: Trace): RequestMode {
  const tcpTargets = new Set(
    (trace.routes ?? [])
      .filter((route) => route.inClusterRequest?.protocol === 'tcp' && !!route.target)
      .map((route) => {
        const ns = route.targetNamespace || trace.subject.namespace || ''
        return ns ? `${ns}/${route.target}` : route.target!
      }),
  )
  let testedHTTP = false
  let testedTCP = false
  for (const hop of [...(trace.downstream ?? []), ...(trace.upstreams ?? [])]) {
    const ns = hop.resource?.namespace || trace.subject.namespace || ''
    const name = hop.resource?.name || ''
    for (const result of hop.probes ?? []) {
      if (result.skipped) continue
      if (result.layer === 'http') testedHTTP = true
      if (result.layer === 'tcp' && result.port && name) {
        const target = `${name}:${result.port}`
        if (tcpTargets.has(ns ? `${ns}/${target}` : target)) testedTCP = true
      }
    }
  }
  return testedHTTP && testedTCP ? 'mixed' : testedHTTP ? 'http' : testedTCP ? 'tcp' : 'none'
}

export function RequestIndicator({ path, onApplyProbePath, testedAt, requestMode = 'http' }: {
  path?: string
  onApplyProbePath?: (p: string) => void
  testedAt?: Date
  requestMode?: RequestMode
}) {
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(path ?? '/')
  useEffect(() => { setDraft(path ?? '/') }, [path])
  const apply = () => {
    const t = draft.trim()
    onApplyProbePath?.(!t ? '/' : t.startsWith('/') ? t : '/' + t)
    setEditing(false)
  }
  const testedTime = testedAt ? ` ${testedAt.toLocaleTimeString(undefined, { hour12: false })}` : ''
  if (requestMode === 'none') {
    return (
      <div className="mt-1 text-[11px] text-theme-text-tertiary">
        test completed{testedTime}; no application request completed from here
      </div>
    )
  }
  if (requestMode === 'tcp') {
    return (
      <div className="mt-1 text-[11px] text-theme-text-tertiary">
        tested{testedTime} with <span className="font-mono text-theme-text-secondary">TCP connection</span>
      </div>
    )
  }
  if (editing) {
    return (
      <div className="mt-1 flex items-center gap-1.5 text-[11px] text-theme-text-tertiary">
        <span>tested with <span className="font-mono text-theme-text-secondary">GET</span></span>
        <input
          autoFocus
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={(e) => { if (e.key === 'Enter') apply(); else if (e.key === 'Escape') { setDraft(path ?? '/'); setEditing(false) } }}
          onBlur={apply}
          placeholder="/"
          className="w-36 rounded border border-theme-border bg-theme-base px-1.5 py-0.5 font-mono text-[11px] text-theme-text-primary"
        />
        <span>↵ applies · esc cancels</span>
      </div>
    )
  }
  return (
    <div className="mt-1 flex items-center gap-1 text-[11px] text-theme-text-tertiary">
      <span>
        tested{testedTime} with <span className="font-mono text-theme-text-secondary">GET {path || '/'}{requestMode === 'mixed' ? ' + TCP' : ''}</span>
      </span>
      {onApplyProbePath && (
        <Tooltip content="Edit the request path">
          <button
            type="button"
            onClick={() => setEditing(true)}
            aria-label="Edit the request path"
            className="rounded p-0.5 text-theme-text-tertiary transition-colors hover:bg-theme-hover hover:text-theme-text-secondary"
          >
            <Pencil className="h-3 w-3" />
          </button>
        </Tooltip>
      )}
    </div>
  )
}

// ProbeOptionsMenu is the "⋯ more options" overflow next to the run buttons -
// keeps the verdict bar clean and gives a home for power options. Today its one
// item is "Customize what we test…", which opens a small form to change the HTTP
// path the probes request. A custom path applies to BOTH tests; the "/" default
// is a sentinel for the in-cluster test, which then probes each route's own
// declared path (reachability_run.go strips it), while the laptop/proxy probes
// request "/" literally - so root on an /admin route can't be forced in-cluster.
function ProbeOptionsMenu({ probePath, onApplyProbePath }: { probePath?: string; onApplyProbePath?: (p: string) => void }) {
  const [open, setOpen] = useState(false)
  const [editing, setEditing] = useState(false)
  const [draft, setDraft] = useState(probePath ?? '/')
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => { setDraft(probePath ?? '/') }, [probePath])
  useEffect(() => {
    if (!open) return
    const onDoc = (e: MouseEvent) => { if (ref.current && !ref.current.contains(e.target as Node)) { setOpen(false); setEditing(false) } }
    document.addEventListener('mousedown', onDoc)
    return () => document.removeEventListener('mousedown', onDoc)
  }, [open])
  if (!onApplyProbePath) return null
  const apply = () => {
    const t = draft.trim()
    onApplyProbePath(!t ? '/' : t.startsWith('/') ? t : '/' + t)
    setOpen(false); setEditing(false)
  }
  return (
    <div ref={ref} className="relative">
      <Tooltip content="More options">
        <button type="button" onClick={() => setOpen((o) => !o)} aria-label="More options" className="btn-brand-muted px-1.5 py-1 text-xs">
          <MoreHorizontal className="w-3.5 h-3.5" />
        </button>
      </Tooltip>
      {open && (
        <div className="absolute right-0 mt-1 z-20 w-72 rounded-lg border border-theme-border bg-theme-surface shadow-theme-lg p-1 text-xs">
          {!editing ? (
            <button type="button" onClick={() => setEditing(true)} className="w-full text-left px-2 py-1.5 rounded hover:bg-theme-hover flex items-center gap-2 text-theme-text-primary">
              <Pencil className="w-3.5 h-3.5 shrink-0" /> Customize what we test…
            </button>
          ) : (
            <div className="p-1.5 flex flex-col gap-2">
              <div className="text-[10px] uppercase tracking-wide text-theme-text-tertiary">What to test</div>
              <label className="text-theme-text-secondary">HTTP path to request</label>
              <input
                autoFocus
                value={draft}
                onChange={(e) => setDraft(e.target.value)}
                onKeyDown={(e) => { if (e.key === 'Enter') apply(); if (e.key === 'Escape') { setOpen(false); setEditing(false) } }}
                placeholder="/"
                className="px-2 py-1 rounded border border-theme-border bg-theme-base font-mono text-theme-text-primary"
              />
              <div className="text-[11px] text-theme-text-tertiary">
                Method GET. A custom path applies to the reachability + in-cluster test; leave &ldquo;/&rdquo; and the in-cluster test uses each route&rsquo;s own declared path.
              </div>
              <div className="flex items-center justify-between pt-0.5">
                <button type="button" onClick={() => setDraft('/')} className="text-theme-text-tertiary hover:text-theme-text-primary">Reset to /</button>
                <button type="button" onClick={apply} className="btn-brand px-2.5 py-1 inline-flex items-center gap-1.5"><RefreshCw className="w-3 h-3" /> Apply &amp; run</button>
              </div>
            </div>
          )}
        </div>
      )}
    </div>
  )
}

// ReachActions is the verdict card's action area: the primary "Run reachability
// test" (proxy/laptop vantage) plus, once allowed, the secondary "Test inside the
// cluster" (real pod-to-pod) - one grouped control instead of two split corners.
// Uses the design-system button classes (.btn-brand / .btn-brand-muted).
export function ReachActions({ onRunProbes, probeRequested, probed, onRunInCluster, inClusterRunning, inClusterAllowed, inClusterRunnable, inClusterTested, probePath, onApplyProbePath, supportsHTTPPath = true, runVantage }: {
  onRunProbes?: () => void
  probeRequested?: boolean
  // probed/inClusterTested: each test has already produced a result, so its
  // button is a RE-RUN - labeled and iconed accordingly. The control set itself
  // never changes (consistency): both tests stay available to re-run whenever
  // they apply. They auto-run on load, so they're almost always re-runs.
  probed?: boolean
  onRunInCluster?: () => void
  inClusterRunning?: boolean
  inClusterAllowed?: boolean
  inClusterTested?: boolean
  /** False when the trace carries no runnable in-cluster request - the button
   *  then renders DISABLED with the reason, never as a live control whose
   *  click is a guaranteed no-op. */
  inClusterRunnable?: boolean
  probePath?: string
  onApplyProbePath?: (p: string) => void
  supportsHTTPPath?: boolean
  /** Where Radar itself runs. The primary test is sent by the Radar PROCESS, so
   *  "from your machine" is only true when Radar is on one - a hosted or
   *  in-cluster Radar dials from its own Pod. */
  runVantage?: 'in-cluster' | 'local'
}) {
  // The in-cluster test stays available whenever the cluster allows it - NEVER
  // gated on the verdict. Hiding it after it succeeds (when the verdict turns
  // healthy) is exactly when an operator wants to re-run it after a change.
  const showInCluster = !!(inClusterAllowed && onRunInCluster)
  const notRunnable = inClusterRunnable === false
  if (!onRunProbes && !showInCluster) return null
  return (
    <div className="flex shrink-0 items-center gap-2">
      {/* Secondary: the in-cluster Job (real pod-to-pod traffic) - a real button
          (it mutates: spawns a Job), clearly subordinate to the primary, with an
          in-cluster glyph so it's distinguishable without hover. */}
      {showInCluster && (
        <Tooltip
          content={
            notRunnable
              ? 'No path on this resource has a request Radar can send from inside the cluster, so this test has nothing to run.'
              : 'Runs the probe from short-lived Jobs INSIDE the cluster — real pod-to-pod traffic — to confirm the in-cluster data path. Covers the testable paths on this resource, not only the one selected above; the confirmation names how many will actually run.'
          }
        >
          <button
            type="button"
            onClick={onRunInCluster}
            disabled={inClusterRunning || notRunnable}
            className="btn-brand-muted px-2.5 py-1 text-xs inline-flex items-center gap-1.5 disabled:cursor-not-allowed disabled:opacity-60"
          >
            {inClusterRunning ? <Loader2 className="w-3 h-3 animate-spin" /> : <Network className="w-3 h-3" />}
            {inClusterRunning ? 'Running in-cluster test…' : inClusterTested ? 'Re-run in-cluster' : 'Test in-cluster'}
          </button>
        </Tooltip>
      )}
      {/* Primary: the reachability test. The refresh icon signals it's
          re-runnable (it auto-ran on load), so re-running reads as a refresh. */}
      {onRunProbes && (
        <Tooltip
          content={
            runVantage === 'in-cluster'
              ? 'Tests reachability from Radar’s own Pod and through the API server. Covers EVERY path on this resource, not only the one selected above.'
              : 'Tests reachability from your machine and through the API server. Covers EVERY path on this resource, not only the one selected above.'
          }
        >
          <button
            type="button"
            onClick={onRunProbes}
            disabled={probeRequested}
            className="btn-brand px-2.5 py-1 text-xs inline-flex items-center gap-1.5"
          >
            {probeRequested ? <Loader2 className="w-3 h-3 animate-spin" /> : <RefreshCw className="w-3 h-3" />}
            {probeRequested ? 'Testing…' : probed ? 'Re-run reachability test' : 'Run reachability test'}
          </button>
        </Tooltip>
      )}
      {/* Path customization applies only to HTTP(S) requests. */}
      {supportsHTTPPath && <ProbeOptionsMenu probePath={probePath} onApplyProbePath={onApplyProbePath} />}
    </div>
  )
}

// CopyableCommand renders a one-line command in a mono box with a copy button.
// Used for finding reproducers: the worst acceptable case is handing the user
// exactly what to run.
export function CopyableCommand({ command }: { command: string }) {
  const [copied, setCopied] = useState(false)
  const onCopy = useCallback(() => {
    void navigator.clipboard.writeText(command)
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }, [command])
  return (
    <div className="mt-1.5 flex items-center gap-2 bg-theme-base rounded px-2 py-1 font-mono text-[11px] text-theme-text-secondary group">
      <Tooltip content={<span className="font-mono">{command}</span>} wrapperClassName="min-w-0 flex-1">
        <code className="w-full truncate">{command}</code>
      </Tooltip>
      <Tooltip content={copied ? 'Copied!' : 'Copy'} wrapperClassName="shrink-0">
        <button
          type="button"
          onClick={onCopy}
          className="text-theme-text-tertiary hover:text-theme-text-primary"
          aria-label="Copy command"
        >
          {copied ? <Check className="w-3 h-3 text-emerald-500" /> : <Copy className="w-3 h-3" />}
        </button>
      </Tooltip>
    </div>
  )
}

// severityToTone bridges the trace's Finding vocabulary (critical / warning /
// info, plus empty when a hop is clean) to Radar's shared StatusTone, so the
// dot rendering uses the same primitive every other surface does.
function severityToTone(s: FindingSeverity | ''): StatusTone {
  switch (s) {
    case 'critical': return 'unhealthy'
    case 'warning': return 'degraded'
    case 'info': return 'unknown'
    default: return 'healthy'
  }
}

export function FindingRow({ finding, onNavigate }: { finding: Finding; onNavigate?: (ref: ResourceRef) => void }) {
  // When the detector parsed a domain-specific cause, lead with it - that's
  // the "why" the operator needs first. The raw detector message stays
  // visible below as secondary evidence. Without a parsed cause, message
  // is the primary line as before.
  const primary = finding.cause || finding.message
  const secondary = finding.cause && finding.message && finding.message !== finding.cause ? finding.message : ''
  // When the finding names a SPECIFIC culprit resource (e.g. the one crashing
  // Pod behind a Service's "0 ready" symptom), the operator's real next move is
  // to open that resource and read its logs/events - not to paste a describe.
  // Offer the click-through as the primary action; keep the command as the
  // terminal/CI fallback below it.
  const culprit = finding.resource?.name ? finding.resource : undefined
  return (
    <div className="flex gap-2.5 items-start">
      <span className="mt-1 shrink-0" aria-hidden><StatusDot tone={severityToTone(finding.severity)} size="sm" /></span>
      <div className="flex-1 min-w-0">
        <div className="text-xs text-theme-text-primary">{primary}</div>
        {secondary && (
          <div className="text-[11px] text-theme-text-tertiary mt-0.5">{secondary}</div>
        )}
        {finding.chips && finding.chips.length > 0 && (
          <div className="mt-1 flex items-center gap-1 flex-wrap">
            {finding.chipsLabel && (
              <span className="text-[11px] text-theme-text-tertiary mr-0.5">{finding.chipsLabel}:</span>
            )}
            {finding.chips.map((c, i) => (
              <Tooltip key={i} content={c.title ?? ''} wrapperClassName="cursor-help">
                <Badge size="sm" severity={c.tone === 'accent' ? 'info' : 'neutral'}>
                  {c.text}
                </Badge>
              </Tooltip>
            ))}
          </div>
        )}
        {finding.chipNotes?.map((n, i) => (
          <div key={i} className="text-[11px] text-theme-text-tertiary mt-0.5">{n}</div>
        ))}
        {finding.action && (
          <div className="text-[11px] text-theme-text-secondary mt-0.5">
            <span className="font-medium">Next step:</span> {finding.action}
          </div>
        )}
        {finding.remediation && !finding.action && (
          <div className="text-[11px] text-theme-text-tertiary mt-0.5">{finding.remediation}</div>
        )}
        {culprit && onNavigate && (
          <button
            type="button"
            onClick={() => onNavigate(culprit)}
            className="mt-1 inline-flex items-center gap-1 text-[11px] text-accent-text hover:underline"
          >
            Open {culprit.kind} {culprit.name} ↗
          </button>
        )}
        {finding.command && <CopyableCommand command={finding.command} />}
      </div>
    </div>
  )
}
