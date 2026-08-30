import { useEffect, useMemo, useRef, useState } from 'react'
import type { Trace, RouteResult, ResourceRef } from './types'
import { ReachActions, JustTestedNote, CopyableCommand, completedRequestMode, type TracePanelProps } from './TracePanel'
import { AlertBanner } from '../ui/drawer-components'
import { PaneLoader } from '../ui/PaneLoader'
import { ReachabilityGraph, MarkGlyph } from './ReachabilityGraph'
import { Activity, ChevronRight } from 'lucide-react'
import { Tooltip } from '../ui/Tooltip'
import { buildGraph, noteHeadline } from './reachGraphModel'
import { buildOrigins, defaultOrigin, probeCheckStats, type Origin, type OriginId } from './reachOrigins'
import { buildSidebar, buildVerdict, type Sidebar, type HopReport, type InspectorCTA, type Selection } from './reachInspector'
import { markStyle, glyphStyle, markHelp, scenariosFor, routeTone, routeChip, routeIdentity, routeForOrigin, traceInClusterRunnable, SEV_COLOR, SEV_BADGE, type Scenario } from './reachMarks'
import { evidenceBannerTitle } from './inClusterSummary'
import { DEV_STATES, devTrace, type DevState } from './reachFixtures'

export { podReach, podProbeKey } from './podReach'

/**
 * ReachabilityView — the Reachability tab body.
 *
 * The organising idea is that a reachability result is meaningless without its
 * vantage. So the view is built around two selections the operator makes
 * explicitly: WHICH scenario (a port, a route, a front door) and FROM WHICH
 * ORIGIN. Everything else — the graph, the verdict, the inspector — is a
 * function of that pair, and changing the origin genuinely re-routes the graph
 * rather than relabelling it.
 *
 * Node dots carry resource health; edges carry path truth. Those are never
 * merged: a healthy resource behind a failed hop stays healthy and the hop
 * stays red.
 */
export function ReachabilityView(props: TracePanelProps) {
  const {
    trace: liveTrace,
    isLoading,
    error,
    inClusterError,
    inClusterPartial,
    inClusterFallback,
    inClusterEvidenceOnly,
    inClusterEvidenceNote,
    probeError,
    onRunProbes,
    onRefresh,
    clusterChangedSinceTest,
  } = props

  // Dev-only: drive the view from fixtures so the empty / failing / sampled /
  // denied states are reachable without a cluster shaped like each one.
  const devEnabled = !!import.meta.env?.DEV
  // Survives re-renders, and deliberately NOT state: flipping it must not cause
  // a render of its own.
  const shownRef = useRef(false)
  const [devState, setDevState] = useState<DevState | null>(null)
  const trace = devState ? devTrace(devState) : liveTrace
  const running = devState === 'running' || !!props.inClusterRunning
  const stale = devState === 'stale' || !!clusterChangedSinceTest
  const inClusterAllowed = devState === 'rbac' ? false : props.inClusterAllowed

  // The full-pane loader is for the FIRST paint only. Returning it later
  // UNMOUNTS the board, which throws away the vantage and path the operator had
  // selected - so starting an in-cluster test dropped them back to a default
  // view of a different vantage, mid-test. Once the board has been shown it
  // stays shown, and progress is expressed inside it (the tested path animates).
  const firstPaintDone = shownRef.current
  if (isLoading && !firstPaintDone && (!trace || !props.probed)) {
    return <PaneLoader className="flex-1 py-10" label="Testing reachability…" />
  }
  if (trace) shownRef.current = true
  if (error && !trace) {
    return (
      <div className="p-1">
        <AlertBanner variant="error" title="Couldn’t load reachability" message={error.message}>
          {onRefresh && (
            <button
              type="button"
              onClick={onRefresh}
              className="mt-2 rounded border border-theme-border bg-theme-surface px-2 py-1 text-xs text-theme-text-primary transition-colors hover:bg-theme-hover"
            >
              Retry
            </button>
          )}
        </AlertBanner>
      </div>
    )
  }

  return (
    <div className="flex h-full min-h-0 flex-col gap-2">
      {devEnabled && <DevStateBar state={devState} onPick={setDevState} />}

      {/* A dead backend must not present a frozen board as current truth: once
          anything rendered, poll failures kept the old verdict + timestamp on
          screen with no indication - and while polls fail, the cluster-changed
          staleness detector is blind. */}
      {error && trace && (
        <div
          className="flex items-center gap-1.5 rounded-md border px-2.5 py-1.5 text-[11.5px]"
          style={{ borderColor: 'var(--color-warning)', background: 'color-mix(in srgb, var(--color-warning) 8%, transparent)', color: 'var(--text-secondary)' }}
        >
          <span style={{ color: 'var(--color-warning-dark)' }}>◷</span>
          Live updates unavailable — showing the last result. Changes in the cluster since then are not reflected.
        </div>
      )}
      {probeError && (
        <AlertBanner variant="error" title="Reachability test failed" message={probeError.message}>
          {onRunProbes && (
            <button
              type="button"
              onClick={onRunProbes}
              className="mt-2 rounded border border-theme-border bg-theme-surface px-2 py-1 text-xs text-theme-text-primary transition-colors hover:bg-theme-hover"
            >
              Try again
            </button>
          )}
        </AlertBanner>
      )}
      {/* "Couldn't run" is only honest when NOTHING ran. A run whose trailing
          routes hit the pod cap / time budget still folded results into the
          merged trace - title that "partially completed", or the banner lies
          over live results. */}
      {inClusterError && (
        <AlertBanner
          variant="warning"
          title={inClusterPartial ? 'In-cluster test partially completed' : "In-cluster test couldn't run"}
          message={inClusterError}
        >
          {inClusterFallback && (
            <div className="mt-2">
              <CopyableCommand command={inClusterFallback} />
            </div>
          )}
        </AlertBanner>
      )}
      {!inClusterError && inClusterEvidenceOnly && (
        <AlertBanner
          variant="info"
          title={evidenceBannerTitle({ partial: !!inClusterPartial, evidenceOnly: true })}
          message={inClusterEvidenceNote || 'The in-cluster probe produced evidence but nothing that changes a declared route outcome.'}
        >
          {inClusterFallback && (
            <div className="mt-2">
              <CopyableCommand command={inClusterFallback} />
            </div>
          )}
        </AlertBanner>
      )}

      {trace ? (
        <ReachabilityBoard {...props} trace={trace} running={running} stale={stale} inClusterAllowed={inClusterAllowed} />
      ) : (
        <div className="p-4 text-sm text-theme-text-tertiary">No reachability data.</div>
      )}
    </div>
  )
}

interface BoardProps extends TracePanelProps {
  trace: Trace
  running: boolean
  stale: boolean
}

function ReachabilityBoard(props: BoardProps) {
  const { trace, running, stale, onNavigateToResource, onRunInCluster, onRunProbes, inClusterAllowed, inClusterDeniedReason, inClusterError, testedAt, runNonce } = props

  // Scenarios group routes that agree in every respect - a Gateway route with
  // three hostnames and one backend is one situation, not three.
  const scenarios = useMemo(() => scenariosFor(trace.routes ?? [], trace.notTested ?? [], trace.upstreams ?? []), [trace])
  // Anchored to the selected ROUTE's identity, not to the scenario's group key.
  // The group key carries outcome, confidence and evidence, so a re-run that
  // changed a result changed the key and dropped the user back to the first
  // scenario - losing their place exactly when they had just asked for new
  // evidence about the path they were reading.
  const [selectedRoute, setSelectedRoute] = useState<string | null>(null)
  const scenario =
    scenarios.find((s) => s.routes.some((r) => routeIdentity(r) === selectedRoute)) ?? scenarios[0]
  const route: RouteResult | undefined = scenario?.primary

  const origins = useMemo(
    () => buildOrigins(trace, { inClusterAllowed, inClusterDeniedReason, inClusterRunning: running, inClusterRunError: inClusterError, stale, route }),
    [trace, inClusterAllowed, inClusterDeniedReason, running, inClusterError, stale, route],
  )
  const [originId, setOriginId] = useState<OriginId | null>(null)
  const origin = origins.find((o) => o.id === (originId ?? defaultOrigin(origins, route))) ?? origins[0]
  // The Job runs from the in-cluster vantage; a band scoped to the LAPTOP must
  // not read "testing" because a different vantage's test started. The graph
  // already scopes per-origin (isRunning) - the verdict + sidebar were reading
  // the global flag.
  const selectedOriginRunning = running && origin?.id === 'incluster'

  const [selection, setSelection] = useState<Selection>(undefined)
  // The entry-problem rows point at a node from outside the graph: hovering one
  // rings it, so "where is it?" is answered before a click is spent.
  const [hoveredNodeId, setHoveredNodeId] = useState<string | undefined>(undefined)

  const model = useMemo(
    () => buildGraph({ trace, route, origin, origins, stale, running }),
    [trace, route, origin, origins, stale, running],
  )
  const multiPath = scenarios.length > 1
  const sidebar = useMemo(
    () =>
      buildSidebar(selection, {
        trace,
        route,
        origin,
        origins,
        nodes: model.nodes,
        breakNodeId: model.breakNodeId,
        breakAtExitOf: model.breakAtExitOf,
        nonNetworkNodeIds: model.nonNetworkNodeIds,
        contextNodeIds: model.contextNodeIds,
        interleave: model.interleave,
        entryParallelCount: model.entryParallelCount,
        journeyEntryNodeIds: model.journeyEntryNodeIds,
        pathNodeIds: model.pathNodeIds,
        stale,
        running: selectedOriginRunning,
        multiPath,
        httpPath: props.probePath,
        canRunInCluster: !!onRunInCluster && inClusterAllowed !== false,
      }),
    [selection, trace, route, origin, origins, model, stale, selectedOriginRunning, multiPath, props.probePath, onRunInCluster, inClusterAllowed],
  )
  const verdict = useMemo(
    () => buildVerdict(trace, route, { stale, running: selectedOriginRunning, originId: origin?.id, originName: origin?.name, pathLabel: multiPath ? scenario?.primary.target || scenario?.label : undefined }),
    [trace, route, origins, stale, running, multiPath, scenario, origin],
  )

  const problems = useMemo(
    () => problemRows(trace, origins, new Set(model.nodes.map((n) => n.id))),
    // Deliberately NOT keyed on the selection: the header is the resource.
    [trace, origins, model],
  )

  const onCTA = (cta: InspectorCTA) => {
    if (cta.disabledReason) return
    if (cta.action === 'run-in-cluster') onRunInCluster?.()
    // onRefresh refetches the STATIC trace and collects no evidence - wrong for a
    // CTA whose own copy offers to go and test something.
    else if (cta.action === 'run-probes') onRunProbes?.()
    else if (cta.action === 'open-resource' && cta.ref) onNavigateToResource?.(cta.ref)
    else if (cta.action === 'copy-command' && cta.command) void navigator.clipboard?.writeText(cta.command)
  }

  return (
    <div className="flex min-h-0 flex-1 flex-col overflow-hidden rounded-lg border border-theme-border bg-theme-surface">
      {/* The HTTP-path editor only makes sense when some declared route takes an
          HTTP-shaped request - a TCP-only (redis/gRPC) subject would offer a
          path field nothing would ever send. */}
      <VerdictBand
        verdict={verdict}
        trace={trace}
        runNonce={runNonce}
        problems={problems}
        onEntryHover={setHoveredNodeId}
        onEntrySelect={(id) => {
          // A pointer row addresses a VANTAGE capsule; everything else a
          // resource node. Selecting the capsule re-routes the whole board to
          // that vantage, which is what "show me what it saw" means.
          const originId = id.startsWith('origin:') ? (id.slice('origin:'.length) as OriginId) : undefined
          if (originId) {
            setOriginId(originId)
            setSelection(undefined)
            return
          }
          setSelection(id)
        }}
        actions={
          <ReachActions
            {...props}
            runVantage={trace.runVantage}
            inClusterRunnable={traceInClusterRunnable(trace)}
            inClusterTested={origins.some((o) => o.id === 'incluster' && o.mark !== 'untested')}
            supportsHTTPPath={(trace.routes ?? []).some((r) => {
              const p = r.inClusterRequest?.protocol
              return p === 'http' || p === 'https' || (!p && !!r.inClusterRequest)
            }) || !(trace.routes ?? []).some((r) => r.inClusterRequest?.protocol === 'tcp')}
          />
        }
      />

      {/* Three columns once there is room for them. The graph is the navigation
          surface and the inspector the reading surface, so keeping them side by
          side preserves the click-an-edge-then-read loop; below xl the inspector
          wraps to its own full-width row rather than being clipped. */}
      {/* The board fills the pane. Each column scrolls on its own so a long
          origin list or inspector never pushes the graph out of view. */}
      {/* Always three columns. Below xl the inspector used to wrap onto its own
          full-width row UNDER the graph, which split one diagnosis into two
          stacked, separately-scrolling regions and left neither enough room.
          Narrow widths tighten the two rails instead - they are a picker and a
          reading column, and both compress better than the path does. */}
      {/* Wider than it was: the vantage rail used to own a column of its own, and
          that width now belongs to the two panes that carry content. */}
      {/* The inspector is the report; the graph rarely uses its full width.
          Starved at ~336px, sentences wrapped into fragments and the next
          action fell below the fold. */}
      <div className="grid min-h-0 flex-1 items-stretch grid-cols-[minmax(0,1fr)_minmax(320px,384px)] xl:grid-cols-[minmax(0,1fr)_minmax(380px,29%)]">
        {/* The vantages are IN the graph now, one node each, so the rail that
            listed them again is gone and its width belongs to the path. */}
        <div className="relative flex min-h-0 min-w-0 flex-col">
          {/* The path picker lives ON the board, like the vantage capsules -
              both selection axes (which path x from where) belong to the same
              surface. As a full-width bar above the header it read as page
              chrome outside the card, cost a row of height, and the eye skimmed
              past the most important control on the page. Top-right is the
              fitted graph's quiet corner. */}
          {scenarios.length > 1 && (
            <ScenarioPicker
              scenarios={scenarios}
              activeKey={scenario?.key}
              onPick={(k) => {
                const picked = scenarios.find((s) => s.key === k)
                setSelectedRoute(picked ? routeIdentity(picked.primary) : null)
                setSelection(undefined)
              }}
              stale={stale}
              running={running}
            />
          )}
          <ViewingStrip verdict={verdict} scenario={scenario} multiPath={multiPath} />
          <ReachabilityGraph
            model={model}
            onAction={(a) => {
              if (a.kind !== 'run-in-cluster') return
              // Pin the vantage you just asked to exercise. Without this the
              // origin stayed on whatever the default resolved to, and when the
              // results arrived the default moved - so you were sent away from
              // the very test you started.
              setOriginId('incluster')
              setSelection(undefined)
              onRunInCluster?.()
            }}
            selected={selection}
            hovered={hoveredNodeId}
            onSelect={(id) => {
              // An origin capsule selects the VANTAGE - the rest of the graph
              // and the inspector re-route to it. Any other node selects the
              // resource, as before.
              const originId = id?.startsWith('origin:') ? (id.slice('origin:'.length) as OriginId) : undefined
              if (originId) {
                setOriginId(originId)
                setSelection(undefined)
                return
              }
              setSelection(id)
            }}
          />

        </div>
        <div className="min-h-0 overflow-y-auto [scrollbar-gutter:stable]">
          <InspectorPanel
            sidebar={sidebar}
            onCTA={onCTA}
            onOpen={(r) => onNavigateToResource?.(r)}
            selected={selection}
            onSelect={setSelection}
          />
        </div>
      </div>

      <CoverageFooter trace={trace} testedAt={testedAt} stale={stale} origins={origins} />
    </div>
  )
}

// ---------------------------------------------------------------- scenarios

/** The hostnames folded into one tab. Only a hover when there is more than one -
 *  a single host is already the tab's own label. */
function TabTooltip({ hosts, members, verdict, children }: { hosts: string[]; members: string[]; verdict?: string; children: React.ReactNode }) {
  // Hostnames when they really are hostnames, otherwise the route labels
  // themselves - a Service port folded with a Pod is neither "2 hostnames" nor
  // meaningfully summarised by dropping one of them.
  const items = hosts.length === members.length && hosts.length > 0 ? hosts : members
  const noun = items === hosts ? 'hostnames' : 'paths'
  // Below the widescreen cutoff the row shows only a tone dot, so the verdict
  // words must live here - a dot alone answers nothing on hover.
  if (items.length < 2 && !verdict) return <>{children}</>
  if (items.length < 2) {
    return (
      <Tooltip content={verdict} wrapperClassName="flex-none cursor-help">
        {children}
      </Tooltip>
    )
  }
  return (
    <Tooltip
      content={
        <span className="flex flex-col gap-0.5">
          {verdict && <span className="font-semibold">{verdict}</span>}
          <span className="font-semibold">
            Same result on {items.length} {noun}
          </span>
          {items.map((h) => (
            <span key={h} className="font-mono">
              {h}
            </span>
          ))}
          {/* The fold is presentational, and users rightly ask whether the
              checks are separate - say the contract out loud. */}
          <span className="mt-1 max-w-[42ch] text-theme-text-tertiary">
            {items === hosts
              ? 'Each hostname is tested separately — its own DNS lookup, TLS handshake and request. They share one tab only while their results are identical; a hostname that behaves differently gets its own.'
              : 'Each path is tested separately; they share one tab only while their results are identical.'}
          </span>
        </span>
      }
      wrapperClassName="flex-none cursor-help"
    >
      {children}
    </Tooltip>
  )
}

/** Matches the `2xl:` breakpoint that shows the verdict chips - the tooltip
 *  duplicates the chip unless it only speaks when the chip is hidden, so the
 *  two MUST key on the same width. */
function useChipsVisible(): boolean {
  const [wide, setWide] = useState(() => typeof window !== 'undefined' && window.matchMedia('(min-width: 1536px)').matches)
  useEffect(() => {
    const mq = window.matchMedia('(min-width: 1536px)')
    const on = () => setWide(mq.matches)
    mq.addEventListener('change', on)
    return () => mq.removeEventListener('change', on)
  }, [])
  return wide
}

function ScenarioPicker({
  scenarios,
  activeKey,
  onPick,
  stale,
  running,
}: {
  scenarios: Scenario[]
  activeKey?: string
  onPick: (key: string) => void
  stale: boolean
  running: boolean
}) {
  const chipsVisible = useChipsVisible()
  return (
    <div
      className="absolute right-3 top-2 z-10 max-w-[46%] rounded-lg border border-theme-border bg-theme-elevated shadow-theme-md"
    >
      <div className="px-2.5 pb-0.5 pt-1.5 text-[9px] font-bold tracking-[0.07em] text-theme-text-tertiary">PATH</div>
      {/* Rows, not tabs: a corner column scales to N paths - capped at ~5 rows
          with a half-row peek, then scrolls, so many-port Services can't eat
          the graph. Tone dot + mono label; the verdict CHIP only gets a column
          on widescreen - below that the dot carries the tone and the words
          live on the hover, because eight long chips drowned the labels. */}
      <div className="flex max-h-[126px] flex-col overflow-y-auto pb-1">
        {scenarios.map((s) => {
          const tone = routeTone(s.primary, { stale, running })
          const active = s.key === activeKey
          const chip = routeChip(s.primary, { stale, running })
          return (
            <TabTooltip hosts={s.hosts} members={s.members} verdict={chipsVisible ? undefined : chip} key={s.key}>
              <button
                type="button"
                onClick={() => onPick(s.key)}
                aria-pressed={active}
                className={`mx-1 flex cursor-pointer items-center gap-1.5 rounded-md px-1.5 py-1 text-left transition-colors duration-150 ${
                  active ? 'selection selection-ring' : 'hover:bg-theme-hover'
                }`}
              >
                <span className="inline-block shrink-0 rounded-full" style={{ width: 7, height: 7, background: SEV_COLOR[tone] }} />
                <span className="min-w-0 max-w-[200px] truncate font-mono text-[11.5px] font-semibold text-theme-text-primary">{s.label}</span>
                <span className={`badge-sm hidden shrink-0 whitespace-nowrap 2xl:inline-flex ${SEV_BADGE[tone]}`}>{chip}</span>
              </button>
            </TabTooltip>
          )
        })}
      </div>
    </div>
  )
}

// ------------------------------------------------------------------ verdict

const LAYER_ORDER = ['dns', 'tcp', 'tls', 'http']

/**
 * One row in the header's problem list.
 *
 * The list is deliberately VANTAGE-INVARIANT: everything in it is true no
 * matter which capsule is selected, so a fault can never hide behind a click.
 * Faults read off cluster state (a dead entry, a promoted finding) say what is
 * wrong; a pointer row says another vantage saw something and takes you there.
 * What a vantage saw stays on the board and in the inspector.
 */
interface ProblemRow {
  key: string
  scope: string
  subject: string
  text: string
  detail?: string
  /** Graph node to ring on hover and select on click - a resource node, or an
   *  origin capsule (`origin:<id>`) for a pointer row. */
  nodeId: string
  severity: 'critical' | 'warning'
}

const SEV_RANK: Record<ProblemRow['severity'], number> = { critical: 0, warning: 1 }

/** Merges every vantage-invariant problem into ONE ranked list: the promoted
 *  diagnosis, the declared entries that cannot carry traffic, and a pointer to
 *  anything only another vantage saw. Two separate bands read as two separate
 *  problems when they were often the same one. */
export function problemRows(
  trace: Trace,
  origins: Origin[],
  knownNodeIds: ReadonlySet<string>,
): ProblemRow[] {
  const rows: ProblemRow[] = []
  const seen = new Set<string>()
  const push = (r: ProblemRow) => {
    const k = `${r.subject}\u0000${r.text}`
    if (seen.has(k)) return
    // Never address a node the graph does not draw: a Pod culprit resolves to
    // the aggregate Pods node it lives in, and anything still unknown drops its
    // pointer rather than offering a hover that rings nothing and a click that
    // selects a section no inspector can resolve.
    if (r.nodeId && !knownNodeIds.has(r.nodeId)) {
      const pods = [...knownNodeIds].find((id) => id.startsWith('n:Pods/'))
      if (r.nodeId.startsWith('n:Pod/') && pods) r = { ...r, nodeId: pods }
      else r = { ...r, nodeId: '' }
    }
    seen.add(k)
    // The caller's key names the row FAMILY and is not unique on its own - one
    // entry can carry several findings, and one origin several failed routes.
    // `k` is what `seen` just proved unique, so pairing them is what makes the
    // React key safe rather than each caller having to get it right.
    rows.push({ ...r, key: `${r.key}\x00${k}` })
  }
  const refNode = (r?: ResourceRef) => (r ? `n:${r.kind}/${r.namespace ?? ''}/${r.name || 'pods'}` : '')

  // The promoted fault. A COVERAGE-class diagnosis is not a fault - it restates
  // the headline, which is generated from the same coverage state.
  const d = trace.diagnosis
  if (d?.summary && d.class !== 'coverage') {
    const ref = d.culpritResource
    push({
      key: 'diagnosis',
      scope: ref ? ref.kind.toUpperCase() : 'PATH',
      subject: ref ? `${ref.kind} ${ref.name}` : trace.subject.name,
      text: noteHeadline(d.summary),
      detail: [d.summary, d.nextAction].filter(Boolean).join(' — '),
      nodeId: refNode(ref) || refNode(trace.subject),
      // The detector's own severity. Assuming critical turned every warning-tier
      // prediction into a red it had not earned.
      severity: d.severity === 'critical' ? 'critical' : 'warning',
    })
  }

  for (const p of trace.entryProblems ?? []) {
    push({
      key: `entry-${p.resource.kind}-${p.resource.name}`,
      scope: 'ENTRY',
      subject: `${p.resource.kind} ${p.resource.name}`,
      text: noteHeadline(p.summary),
      detail: [p.summary, p.detail, p.action].filter(Boolean).filter((v, i, a) => a.indexOf(v) === i).join(' — '),
      nodeId: refNode(p.resource),
      severity: p.severity === 'critical' ? 'critical' : 'warning',
    })
  }

  // A fault some vantage hit, on ANY declared path. Deliberately independent of
  // what is selected: the header is the resource, so a row that appears or
  // vanishes as you click capsules would hide exactly the fault the reader has
  // not thought to look for.
  for (const o of origins) {
    if (o.unsupported) continue
    for (const r of trace.routes ?? []) {
      const own = routeForOrigin(r, o.id, trace.runVantage)
      if (!own || (own.outcome !== 'unreachable' && own.outcome !== 'server-error')) continue
      push({
        key: `vantage-${o.id}-${r.route}`,
        scope: 'SEEN FROM',
        subject: o.name,
        text: own.evidence ? noteHeadline(own.evidence) : `could not get through to ${r.target || r.route}`,
        detail: [own.evidence, `path: ${r.route}`].filter(Boolean).join(' — '),
        nodeId: `origin:${o.id}`,
        severity: 'warning',
      })
    }
  }

  return rows.sort((a, b) => SEV_RANK[a.severity] - SEV_RANK[b.severity])
}

/** The header's problem list. Hover names the fault in full and rings it on the
 *  board; click focuses it there. */
/**
 * What you are currently looking at, stated ON the board that changes. Keeping
 * it here rather than in the header is the whole redesign: the header is the
 * resource and never moves, this strip is the selection and always does, and
 * the spatial split says so without anyone having to read a label.
 */
function ViewingStrip({ verdict, scenario, multiPath }: { verdict: ReturnType<typeof buildVerdict>; scenario?: Scenario; multiPath: boolean }) {
  const path = multiPath ? scenario?.primary.target || scenario?.label : undefined
  return (
    <div className="pointer-events-none absolute left-3 top-2 z-10 flex max-w-[50%] flex-wrap items-center gap-x-2 gap-y-1">
      <span className="text-[9.5px] font-bold tracking-[0.07em] text-theme-text-tertiary">VIEWING</span>
      {path && <span className="truncate font-mono text-[11px] font-semibold text-theme-text-secondary">{path}</span>}
      {path && <span className="text-theme-border">·</span>}
      <span className="truncate font-mono text-[11px] font-semibold text-theme-text-secondary">{verdict.originName ?? 'this resource'}</span>
      <span className={`badge-sm whitespace-nowrap ${SEV_BADGE[verdict.tone]}`}>{verdict.chipText}</span>
    </div>
  )
}

function ProblemList({
  rows,
  onHover,
  onSelect,
}: {
  rows: ProblemRow[]
  onHover?: (nodeId?: string) => void
  onSelect?: (nodeId: string) => void
}) {
  if (rows.length === 0) return null
  const shown = rows.slice(0, 3)
  const rest = rows.length - shown.length
  return (
    <div className="mt-1.5 flex flex-col gap-1">
      {shown.map((p) => (
        <Tooltip key={p.key} content={p.detail || p.text} wrapperClassName="block">
          <button
            type="button"
            onMouseEnter={() => onHover?.(p.nodeId)}
            onMouseLeave={() => onHover?.(undefined)}
            onFocus={() => onHover?.(p.nodeId)}
            onBlur={() => onHover?.(undefined)}
            onClick={() => onSelect?.(p.nodeId)}
            aria-label={`${p.subject} — ${p.text}. Show it on the path.`}
            className="flex w-full max-w-[92ch] cursor-pointer items-start gap-1.5 rounded-md px-2 py-1.5 text-left text-xs leading-relaxed text-pretty transition-colors hover:bg-theme-hover"
            style={{
              border: `1px solid var(${p.severity === 'critical' ? '--color-error' : '--color-warning'})`,
              background: `color-mix(in srgb, var(${p.severity === 'critical' ? '--color-error' : '--color-warning'}) 10%, transparent)`,
            }}
          >
            <span className="shrink-0" style={{ color: `var(${p.severity === 'critical' ? '--color-error-dark' : '--color-warning-dark'})` }}>▲</span>
            <span className="min-w-0 flex-1 text-theme-text-primary">
              <span className="text-[9.5px] font-bold tracking-[0.07em] text-theme-text-tertiary">{p.scope} </span>
              <span className="font-mono font-semibold">{p.subject}</span> — <span className="line-clamp-2">{p.text}</span>
            </span>
          </button>
        </Tooltip>
      ))}
      {rest > 0 && <div className="text-[11px] text-theme-text-tertiary">+{rest} more problem{rest > 1 ? 's' : ''} — see the path below</div>}
    </div>
  )
}

/**
 * The header. Everything in it describes THE RESOURCE and never changes when a
 * vantage or path is selected - which is the whole point: the reader can learn
 * once that this block is static and the board below is what moves. Anything
 * scoped to the current selection lives on the board (the viewing strip and the
 * capsules) and in the inspector.
 */
function VerdictBand({
  verdict,
  trace,
  actions,
  runNonce,
  problems,
  onEntryHover,
  onEntrySelect,
}: {
  verdict: ReturnType<typeof buildVerdict>
  trace: Trace
  actions: React.ReactNode
  runNonce?: number
  problems: ProblemRow[]
  onEntryHover?: (nodeId?: string) => void
  onEntrySelect?: (nodeId: string) => void
}) {
  // The check volume lives HERE, not only in the footer ledger: the band is
  // the one line everyone reads, and a verdict with no visible work behind it
  // asks to be taken on faith. Stated as work done, never as success - the
  // count includes failed dials.
  const stats = probeCheckStats(trace)
  const layerBreakdown = LAYER_ORDER.filter((l) => stats.byLayer[l])
    .map((l) => `${l.toUpperCase()} ${stats.byLayer[l]}`)
    .join(' · ')
  return (
    <div className="flex items-start gap-3 border-b border-theme-border px-5 py-3">
      <div className="min-w-0 flex-1">
        {/* The headline covers the whole resource while the badge follows the
            selected path + vantage. On one line the two read as a single
            contradictory sentence ("Reached via API server … not tested … from
            In-cluster probe"). Each scope gets its own labelled line; which of
            the two should LEAD is a product call, but they must never blur. */}
        {verdict.scopeLabel && (
          <div className="mb-0.5 text-[9.5px] font-bold tracking-[0.07em] text-theme-text-tertiary">{verdict.scopeLabel}</div>
        )}
        <div className="flex flex-wrap items-baseline gap-2">
          <span className="text-[14.5px] font-semibold text-theme-text-primary">{verdict.title}</span>
          <JustTestedNote nonce={runNonce} />
        </div>

        {stats.checks > 0 && (
          <Tooltip
            content={`${layerBreakdown}${stats.vantages > 1 ? ` — from ${stats.vantages} vantages` : ''}. Every check is a real dial; skipped ones are listed with their reasons, never counted. Each check waits up to ~1s, inside a 3s budget per run — a slow backend can time out and read as not tested.`}
            wrapperClassName="mt-1 inline-flex cursor-help"
          >
            <span className="inline-flex items-center gap-1.5 font-mono text-[11px] text-theme-text-secondary">
              {/* Accent, not green: checks include failures - this states WORK
                  done, never success. Emphasis is weight, not a new color. */}
              <Activity className="h-3 w-3 flex-none" style={{ color: 'var(--accent-text)' }} />
              <span>
                <span className="font-semibold text-theme-text-primary">{stats.checks}</span> live check{stats.checks === 1 ? '' : 's'}
                {stats.vantages > 1 ? ` from ${stats.vantages} vantages` : ''}
              </span>
            </span>
          </Tooltip>
        )}

        {verdict.body && <div className="mt-1 max-w-[76ch] text-xs leading-relaxed text-theme-text-secondary text-pretty">{verdict.body}</div>}
        <ProblemList rows={problems} onHover={onEntryHover} onSelect={onEntrySelect} />
      </div>
      <div className="flex flex-none gap-2">{actions}</div>
    </div>
  )
}

// ------------------------------------------------------------------ origins

/** The vantages Radar cannot run, stated rather than offered. These are real
 *  holes in the evidence, so they must stay on screen - but as ONE limit on the
 *  claim, not a list. A per-origin line charged the rail a row for each thing
 *  Radar can't do, teaching the reader its limitations before its findings. */
function UntestableNote({ origins }: { origins: Origin[] }) {
  const out = origins.filter((o) => o.unsupported)
  if (out.length === 0) return null
  return (
    <>
      <span className="text-theme-border">|</span>
      <Tooltip
        content={
          <span className="flex flex-col gap-1">
            {out.map((o) => (
              <span key={o.id}>
                <span className="font-semibold">{o.name}</span> — {o.unavailable}
              </span>
            ))}
          </span>
        }
        wrapperClassName="cursor-help"
      >
        <span className="text-theme-text-tertiary">
          {out.map((o) => o.glyph).join(' ')} {untestableSummary(out)}
        </span>
      </Tooltip>
    </>
  )
}

/** One phrase for every vantage Radar can't run, named by what it would have
 *  proven rather than by Radar's internal origin names. */
function untestableSummary(out: Origin[]): string {
  const phrase: Partial<Record<OriginId, string>> = {
    caller: 'as your application',
    external: 'from the public internet',
    'radar-incluster': 'as Radar inside the cluster',
  }
  const parts = out.map((o) => phrase[o.id] ?? o.name.toLowerCase())
  if (parts.length === 1) return `Not tested ${parts[0]}.`
  return `Not tested ${parts.slice(0, -1).join(', ')} or ${parts[parts.length - 1]}.`
}

// ---------------------------------------------------------------- inspector

/**
 * The panel's one disclosure, matching drawer-components' Section: a rotating
 * ChevronRight and a grid-template-rows transition. Hand-rolled "›"/"⌄" toggles
 * neither looked like the rest of Radar nor animated, so a section appearing was
 * an abrupt jump with no affordance saying it could be opened at all.
 */
function Disclosure({
  open,
  onToggle,
  label,
  count,
  children,
}: {
  open: boolean
  onToggle: () => void
  label: string
  count?: number
  children: React.ReactNode
}) {
  return (
    <div>
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={open}
        className="flex w-full items-center gap-1.5 text-[9.5px] font-bold tracking-[0.07em] text-theme-text-tertiary transition-colors hover:text-theme-text-secondary"
      >
        <ChevronRight className={`h-3 w-3 flex-none transition-transform duration-200 ${open ? 'rotate-90' : ''}`} />
        <span>{label}</span>
        {count !== undefined && <span className="flex-1 text-right font-normal tracking-normal">{count}</span>}
      </button>
      <div className="grid transition-[grid-template-rows] duration-200 ease-out" style={{ gridTemplateRows: open ? '1fr' : '0fr' }}>
        <div className="overflow-hidden">{children}</div>
      </div>
    </div>
  )
}

function Caveats({ items, defaultOpen = false }: { items: string[]; defaultOpen?: boolean }) {
  const [open, setOpen] = useState(defaultOpen)
  if (items.length === 0) return null
  // Collapsed by default. These are limits on the CLAIM, and they are the same
  // sentences for every result of a given kind - so at full height they pushed
  // the evidence and the path below the fold on every single view to say
  // something invariant. Quieter than a fault, and one click from being read.
  return (
    <div>
      <Disclosure open={open} onToggle={() => setOpen((v) => !v)} label="WHAT THIS DOESN’T PROVE" count={items.length}>
        {items.map((n, i) => (
          <div key={i} className="mt-1 text-[10.5px] leading-snug text-theme-text-tertiary">
            {n}
          </div>
        ))}
      </Disclosure>
    </div>
  )
}

function InspectorPanel({
  sidebar,
  onCTA,
  onOpen,
  selected,
  onSelect,
}: {
  sidebar: Sidebar
  onCTA: (c: InspectorCTA) => void
  onOpen: (r: ResourceRef) => void
  selected?: string
  onSelect: (id: string) => void
}) {
  const [scopeOpen, setScopeOpen] = useState(false)
  const { path } = sidebar
  return (
    <div className="flex h-full flex-col gap-3 bg-theme-surface px-3.5 py-3 border-l border-theme-border">
      {/* The diagnosis is ALWAYS here. Whether traffic got through must never
          require a click - it is the question the tab exists to answer. */}
      <div>
        <div className="flex items-center gap-2">
          <span className="text-[9.5px] font-bold tracking-[0.07em] text-theme-text-tertiary">THIS PATH</span>
          <div className="flex-1" />
          <span className={`badge-sm whitespace-nowrap ${SEV_BADGE[path.chipTone]}`}>{path.chipText}</span>
        </div>
        <div className="mt-1.5 font-mono text-[12.5px] font-semibold leading-snug text-theme-text-primary">{path.title}</div>
        {path.request && (
          <div className="mt-1 flex items-baseline gap-1.5">
            <span className="flex-none text-[9px] font-bold tracking-[0.04em] text-theme-text-tertiary">ASKED FOR</span>
            <span className="min-w-0 flex-1 break-words font-mono text-[10.5px] text-theme-text-secondary">{path.request}</span>
          </div>
        )}
        <div className="mt-1 text-[11.5px] leading-relaxed text-theme-text-secondary text-pretty">{path.body}</div>
      </div>

      {path.scope.length > 0 && (
        <Disclosure open={scopeOpen} onToggle={() => setScopeOpen((v) => !v)} label="TEST DETAILS" count={path.scope.length}>
          {path.scope.map((p, i) => (
            <div key={i} className="mt-0.5 flex gap-2 border-b border-theme-border-subtle py-0.5">
              <span className="w-[86px] flex-none pt-0.5 text-[9px] font-bold tracking-[0.04em] text-theme-text-tertiary">{p.k}</span>
              <span className="flex-1 break-words font-mono text-[10px] leading-snug text-theme-text-secondary">{p.v}</span>
            </div>
          ))}
        </Disclosure>
      )}

      {/* The action sits with the sentence that explains why it is offered. As
          its own block at the very bottom it repeated that sentence, and put
          the button a scroll away from the reason for it. */}
      {(path.next.body || path.next.ctas.length > 0) && (
        <div className={path.next.header ? 'rounded-md px-2.5 py-2' : ''} style={path.next.header ? { border: '1px solid var(--accent)', background: 'var(--accent-muted)' } : undefined}>
          {path.next.header && (
            <div className="mb-1 text-[9.5px] font-bold tracking-[0.05em]" style={{ color: 'var(--accent-text)' }}>
              {path.next.header}
            </div>
          )}
          {path.next.body && <div className="text-[11.5px] leading-snug text-theme-text-secondary text-pretty">{path.next.body}</div>}
          {path.next.blocked && <div className="mt-1.5 text-[10.5px] leading-snug text-theme-text-tertiary">{path.next.blocked}</div>}
          {path.next.ctas.length > 0 && (
            <div className={`flex flex-wrap gap-1.5 ${path.next.body || path.next.header ? 'mt-2' : 'mt-1'}`}>
              {path.next.ctas.map((c, i) => (
                <Tooltip key={i} content={c.disabledReason ?? ''} wrapperClassName="cursor-help">
                  <button
                    type="button"
                    onClick={() => onCTA(c)}
                    disabled={!!c.disabledReason}
                    className={
                      c.primary && !c.disabledReason
                        ? 'btn-brand cursor-pointer whitespace-nowrap rounded-md px-2.5 py-1.5 text-[11px] font-semibold'
                        : 'cursor-pointer whitespace-nowrap rounded-md border border-theme-border bg-theme-base px-2.5 py-1.5 text-[11px] font-semibold text-theme-text-secondary disabled:cursor-not-allowed disabled:opacity-60'
                    }
                  >
                    {c.text}
                  </button>
                </Tooltip>
              ))}
            </div>
          )}
        </div>
      )}

      {path.evidence.length > 0 && (
        <div>
          {/* Same count idiom as TEST DETAILS: the number of distinct
              observations is itself a trust signal, stated in chrome-weight. */}
          <div className="mb-1 flex items-center text-[9.5px] font-bold tracking-[0.07em] text-theme-text-tertiary">
            <span>WHAT WE SAW</span>
            <span className="flex-1 text-right font-normal tracking-normal">{path.evidence.length}</span>
          </div>
          {path.evidence.map((e, i) => (
            <div key={i} className="mb-1 flex items-baseline gap-1.5">
              <MarkGlyph mark={e.mark} />
              <span className="text-[11px] leading-snug text-theme-text-secondary">{e.text}</span>
            </div>
          ))}
        </div>
      )}

      {/* The whole path, in order. Every hop is here whether or not it was
          clicked; the break opens by default because it is the answer, and the
          rest stay one line so the report never becomes a wall. */}
      {sidebar.hops.length > 0 && (
        <div className="border-t border-theme-border pt-3">
          <div className="text-[9.5px] font-bold tracking-[0.07em] text-theme-text-tertiary">ALONG THIS PATH</div>
          {sidebar.hops.map((h) => (
            <HopSection key={h.id} hop={h} selected={selected === h.id} onSelect={onSelect} onOpen={onOpen} />
          ))}
        </div>
      )}
      {/* Configured-but-bypassed resources: readable for orientation, outside
          the journey - listing them under ALONG THIS PATH claimed a traversal
          the selected vantage explicitly does not make. */}
      {sidebar.context && (
        <div className="border-t border-theme-border pt-3">
          <div className="text-[9.5px] font-bold tracking-[0.07em] text-theme-text-tertiary">{sidebar.context.label}</div>
          {sidebar.context.hops.map((h) => (
            <HopSection key={h.id} hop={h} selected={selected === h.id} onSelect={onSelect} onOpen={onOpen} />
          ))}
        </div>
      )}
      <Caveats items={path.notProve} />

    </div>
  )
}

const HOP_STATE_NOTE: Record<HopReport['state'], string> = {
  break: 'the request stopped here',
  before: 'reached',
  after: 'never tried — something earlier stopped',
  plain: '',
}

function HopSection({
  hop,
  selected,
  onSelect,
  onOpen,
}: {
  hop: HopReport
  selected: boolean
  onSelect: (id: string) => void
  onOpen: (r: ResourceRef) => void
}) {
  const [openOverride, setOpenOverride] = useState<boolean | null>(null)
  const open = openOverride ?? hop.expanded
  const ref = useRef<HTMLDivElement>(null)
  // A click on the graph scrolls its hop into view instead of replacing the
  // panel - the gesture still means "I care about this one", it just no longer
  // costs the reader the rest of the story.
  //
  // Selecting also CLEARS any manual collapse. A hop the reader had folded shut
  // kept that override forever, so clicking its node in the graph highlighted a
  // closed section and appeared to do nothing at all - asking to look at
  // something has to be able to open it.
  useEffect(() => {
    if (!selected) return
    setOpenOverride(null)
    ref.current?.scrollIntoView({ block: 'nearest', behavior: 'smooth' })
  }, [selected])
  // Becoming the BREAK also clears a manual collapse: the break is the answer
  // this panel exists to show, and a collapse made while reading a different
  // scenario must not keep the answer shut when the reader switches to one
  // where this hop is where the request stopped.
  useEffect(() => {
    if (hop.state === 'break') setOpenOverride(null)
  }, [hop.state])
  return (
    <div
      ref={ref}
      // A flat tint plus a hairline ring, not a heavy left bar: the bar read as a
      // scrollbar against the panel edge and its background ran under the border.
      // `.selection` / `.selection-ring` are the repo's own tokens for exactly
      // this, so selection here looks like selection everywhere else.
      className={`mt-2 rounded-md px-2 py-1 transition-colors duration-200 ${selected ? 'selection selection-ring' : ''}`}
    >
      <button
        type="button"
        onClick={() => {
          setOpenOverride(!open)
          onSelect(hop.id)
        }}
        aria-expanded={open}
        className="flex w-full items-center gap-1.5 text-left"
      >
        <ChevronRight className={`h-3 w-3 flex-none text-theme-text-tertiary transition-transform duration-200 ${open ? 'rotate-90' : ''}`} />
        <span className="text-[9px] font-bold tracking-[0.04em] text-theme-text-tertiary">{hop.kind}</span>
        <span className="min-w-0 flex-1 truncate font-mono text-[11.5px] font-semibold text-theme-text-primary">{hop.name}</span>
        <span className={`badge-sm whitespace-nowrap ${SEV_BADGE[hop.chipTone]}`}>{hop.chipText || HOP_STATE_NOTE[hop.state]}</span>
      </button>
      {!open && (hop.state !== 'plain' || hop.parallelCount) && (
        <div className="pl-[18px] text-[10px] leading-snug text-theme-text-tertiary">
          {[hop.parallelCount ? `one of ${hop.parallelCount} parallel entry points` : '', HOP_STATE_NOTE[hop.state]]
            .filter(Boolean)
            .join(' — ')}
        </div>
      )}
      <div className="grid transition-[grid-template-rows] duration-200 ease-out" style={{ gridTemplateRows: open ? '1fr' : '0fr' }}>
        <div className="overflow-hidden">
        <div className="pb-1 pl-[18px]">
          {hop.openRef?.name && (
            <button type="button" onClick={() => onOpen(hop.openRef!)} className="text-[11px] text-accent-text hover:underline">
              Open ↗
            </button>
          )}
          <div className="mt-1 text-[11.5px] leading-relaxed text-theme-text-secondary text-pretty">{hop.body}</div>
          {hop.facts.map((f, i) => (
            <div key={i} className="mt-0.5 flex gap-2 border-b border-theme-border-subtle py-0.5">
              <span className="w-[86px] flex-none pt-0.5 text-[9px] font-bold tracking-[0.04em] text-theme-text-tertiary">{f.k}</span>
              <span className="flex-1 break-words font-mono text-[10px] leading-snug text-theme-text-secondary">{f.v}</span>
            </div>
          ))}
          {hop.anomalies && hop.anomalies.length > 0 && (
            <div className="mt-2">
              {hop.anomalies.map((a, i) => (
                <div key={i} className="mb-1 flex items-baseline gap-1.5">
                  <span style={glyphStyle(a.mark)}>{markStyle(a.mark).glyph}</span>
                  <span className="text-[10.5px] leading-snug text-theme-text-secondary">{a.text}</span>
                </div>
              ))}
            </div>
          )}
          {hop.rows && hop.rows.length > 0 && (
            <div className="mt-2 flex flex-col gap-0.5">
              {hop.rows.map((r) => (
                <Tooltip
                  key={r.name}
                  content={
                    <>
                      <span className="font-mono font-semibold">{r.name}</span>
                      <span className="text-theme-text-tertiary"> — {r.detail} · {markHelp(r.mark)}</span>
                    </>
                  }
                  wrapperClassName="w-full cursor-help"
                >
                  <div className="flex w-full items-baseline gap-1.5">
                    <span style={glyphStyle(r.mark)}>{markStyle(r.mark).glyph}</span>
                    <span className="min-w-0 flex-1 truncate font-mono text-[10px] text-theme-text-secondary">{r.name}</span>
                    <span className="min-w-0 flex-1 truncate text-right text-[9.5px] text-theme-text-tertiary">{r.detail}</span>
                  </div>
                </Tooltip>
              ))}
              {!!hop.moreRows && <div className="text-[9.5px] text-theme-text-tertiary">+{hop.moreRows} more not shown</div>}
            </div>
          )}
          <div className="mt-2">
            <Caveats items={hop.notProve} />
          </div>
        </div>
        </div>
      </div>
    </div>
  )
}

// ------------------------------------------------------------------- footer

function CoverageFooter({
  trace,
  testedAt,
  stale,
  origins,
}: {
  trace: Trace
  testedAt?: Date
  stale: boolean
  origins: Origin[]
}) {
  const c = trace.coverage
  const skips = trace.notTested ?? []
  // Only skips that actually cost coverage belong in the headline count - a
  // benign skip loses nothing and padding the number would overstate the gap.
  const realGaps = skips.filter((s) => s.reasonClass !== 'benign')
  // "skipped N · M not tested" read as one thing counted twice. They are
  // different levels: attempts made, versus paths with no evidence at all.
  const attempts = c && c.tested > 0 ? `${c.passed} got through · ${c.failed} failed${c.skipped ? ` · ${c.skipped} couldn’t be tried` : ''}` : ''
  // Derived breaks were never dialled, so they are counted apart from attempts -
  // folding them in reported requests that failed when none were sent.
  const derived = c?.derived ? `${c.derived} broken without testing` : ''
  const gaps = realGaps.length ? `${realGaps.length} path${realGaps.length === 1 ? '' : 's'} with no evidence` : ''
  // The KIND of proof must stay visible after the run, not only on the consent
  // dialog: a TCP-only pass proves a socket opened, never that the application
  // protocol works, and "got through" alone let the reader assume more.
  const mode = completedRequestMode(trace)
  const proofKind =
    mode === 'tcp'
      ? 'TCP connections only — application protocol not checked'
      : mode === 'mixed'
        ? 'some ports TCP-only — application protocol not checked there'
        : ''
  const coverageText =
    (c ? [attempts, derived, gaps].filter(Boolean).join('  ·  ') || 'nothing tested yet' : 'nothing tested yet') +
    (proofKind ? `  ·  ${proofKind}` : '')
  // The live-check volume moved up into the verdict band - the footer stays
  // the coverage ledger (routes, gaps, when), not the trust headline.
  return (
    <div className="flex flex-wrap items-center gap-2.5 border-t border-theme-border bg-theme-surface px-5 py-2 text-[11px] text-theme-text-tertiary">
      <span className="text-[9.5px] font-bold tracking-[0.07em]">WHAT WAS TESTED</span>
      <span className="font-mono text-theme-text-secondary">{coverageText}</span>
      {/* The vantages Radar can never run are a COVERAGE fact, so they belong on
          the coverage line - as their own band they were a third row of chrome
          restating what this one is for. */}
      <UntestableNote origins={origins} />
      <span className="text-theme-border">|</span>
      <span className="text-[9.5px] font-bold tracking-[0.07em]">WHEN</span>
      <span className="font-mono" style={{ color: stale ? 'var(--color-warning-dark)' : 'var(--text-secondary)' }}>
        {stale
          ? 'cluster state changed since this test — results excluded from the verdict'
          : testedAt
            ? `observed ${testedAt.toLocaleTimeString()}`
            : 'no test has been run'}
      </span>
    </div>
  )
}

// -------------------------------------------------------------------- dev

function DevStateBar({ state, onPick }: { state: DevState | null; onPick: (s: DevState | null) => void }) {
  const chip = (active: boolean) =>
    `cursor-pointer whitespace-nowrap rounded-full border px-2.5 py-0.5 text-[11px] font-semibold ${
      active ? 'border-accent bg-accent-muted text-accent-text' : 'border-theme-border bg-theme-surface text-theme-text-secondary'
    }`
  return (
    <div className="flex flex-wrap items-center gap-1.5">
      <span className="text-[10px] font-bold tracking-[0.08em] text-theme-text-tertiary">DEV STATE</span>
      <button type="button" onClick={() => onPick(null)} className={chip(state === null)}>
        live
      </button>
      {DEV_STATES.map((s) => (
        <button key={s.id} type="button" onClick={() => onPick(s.id)} className={chip(state === s.id)}>
          {s.label}
        </button>
      ))}
    </div>
  )
}
