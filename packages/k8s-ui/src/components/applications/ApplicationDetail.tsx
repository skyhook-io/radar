import { useMemo, useState, useCallback, useEffect, useRef, type ReactNode } from 'react'
import { AlertTriangle, ArrowLeft, Boxes, CheckCircle2, ChevronDown, Clock3, ExternalLink, Layers, Search } from 'lucide-react'
import { clsx } from 'clsx'
import type { ResourceRef, Topology, TopologyNode } from '../../types'
import { StatusDot, mapHealthToTone } from '../ui/status-tone'
import { Tooltip } from '../ui/Tooltip'
import { EmptyState } from '../ui/EmptyState'
import { ResourceRefBadge } from '../ui/drawer-components'
import { TopologyGraph } from '../topology/TopologyGraph'
import { pluralize } from '../../utils/pluralize'
import { kindToPlural, apiVersionToGroup, refToSelectedResource } from '../../utils/navigation'
import { tagWorkloadOwnership, seedNodeIds, ownershipOf, workloadKey, type NeighborhoodSeed } from '../../utils/topology-neighborhood'
import { workloadHue, NEUTRAL_OWNER, type WorkloadFocus } from '../../utils/workload-colors'
import { getTopologyIcon } from '../../utils/resource-icons'
import {
  type AppRow,
  type AppHistory,
  type AppSourceRef,
  type AppWorkload,
  type AppHealth,
  CHIP,
  CHIP_TONE,
  HEALTH_META,
  healthOf,
  namespaceOf,
  namespacesOf,
  resolveEnv,
  identityEnvInferred,
  workloadClassOf,
  classCompositionOf,
  worstHealth,
  appGroupLagMessage,
  compareVersions,
  appSourceLabel,
  overlayProvenance,
} from '../../utils/applications'
import { PaneLoader } from '../ui/PaneLoader'
import { midTruncate } from '../../utils/format'
import { VersionTooltip, AppIdentityTooltip } from './AppTooltips'
import { ProvenanceBadge, ClassBadge, CategoryChip, VersionInfo } from './AppChips'
import { ReadyBar } from './ReadyBar'

// ApplicationDetail owns the application chrome and scope switcher. The selected
// scope decides the one tab row shown in the detail pane: app scope gets
// Overview/Topology/History; workload scope delegates to the host's WorkloadView.

export type SelectedAppWorkload = NeighborhoodSeed

/** One env instance in the identity switcher — a sibling app row's digest. */
export interface AppIdentityInstance {
  appKey: string
  name: string
  env: string
  health: AppHealth
  version?: string
  confidence: string
  evidence: string
  count?: number
}

/** Workload selection is either fully controlled (key + callback, the host
 *  wires it to the URL so back/forward works) or fully internal — providing
 *  only half silently freezes the selector, so the types forbid it. `null` means
 *  the application itself is selected; a key (see `workloadKey`) selects a
 *  workload scope. */
type SelectionProps =
  | { selectedWorkloadKey: string | null; onSelectWorkload: (key: string | null) => void }
  | { selectedWorkloadKey?: undefined; onSelectWorkload?: undefined }

type CanonicalApplicationView = 'overview' | 'topology' | 'history'

export type ApplicationView = CanonicalApplicationView

const DEFAULT_APPLICATION_VIEW: CanonicalApplicationView = 'overview'

const APPLICATION_VIEWS: Array<{ id: CanonicalApplicationView; label: string }> = [
  { id: 'overview', label: 'Overview' },
  { id: 'topology', label: 'Topology' },
  { id: 'history', label: 'History' },
]

function canonicalApplicationView(view?: ApplicationView): CanonicalApplicationView {
  return view ?? DEFAULT_APPLICATION_VIEW
}

type ViewProps =
  | { selectedView: ApplicationView; onSelectView: (view: ApplicationView) => void }
  | { selectedView?: undefined; onSelectView?: undefined }

export type ApplicationDetailProps = {
  app: AppRow
  onBack: () => void
  /** Render the host's WorkloadView for the chosen workload. */
  renderWorkload: (workload: SelectedAppWorkload) => ReactNode
  /** Resources-view topology spanning the app's namespaces. When present, it
   *  powers the application Topology view and workload hover focus. */
  topology?: Topology
  /** True while the host's topology fetch is in flight. Without it, a
   *  multi-workload app can briefly show an empty topology while topology loads. */
  topologyLoading?: boolean
  /** Open a related (non-workload) resource clicked in the app graph. */
  onNavigateToResource?: (resource: { kind: string; namespace: string; name: string; group?: string }) => void
  /** App identity siblings (this instance included, ladder-ordered) — turns the
   *  context strip's Environment fact into a switcher. Identity grouping is
   *  classification, not an address: it switches between REAL instances, never
   *  an aggregate page. */
  identityInstances?: AppIdentityInstance[] | null
  /** Switch to a sibling instance (host swaps ?app= and, when it can match
   *  the current workload in the target, preserves ?workload= + ?tab=). */
  onSwitchInstance?: (appKey: string) => void
  /** Env tokens the cluster proved (the list derives the same set) — keeps a
   *  ungrouped app's Environment fact consistent between list and detail. */
  discoveredEnvs?: ReadonlySet<string>
  /** Which instance in `identityInstances` is active, by its `appKey`. Defaults
   *  to `app.key` — correct for the OSS case, where each instance IS a distinct
   *  app row keyed the same way. A host that keys instances on something other
   *  than the app key (e.g. the Cloud fleet keys per cluster, while `app.key`
   *  must stay the logical app key for provenance) passes the active instance's
   *  key here so the switcher marks it. */
  activeInstanceKey?: string
  history?: AppHistory
  historyLoading?: boolean
  onOpenSource?: (source: AppSourceRef) => void
} & SelectionProps & ViewProps

function ContextFact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex min-w-0 items-baseline gap-1.5">
      <span className="text-[10px] uppercase tracking-wide text-theme-text-tertiary">{label}</span>
      <span className="min-w-0 truncate text-xs text-theme-text-secondary">{children}</span>
    </div>
  )
}

function collapseIdentityInstances(instances: AppIdentityInstance[], activeKey: string): AppIdentityInstance[] {
  const byEnv = new Map<string, AppIdentityInstance[]>()
  for (const inst of instances) {
    byEnv.set(inst.env, [...(byEnv.get(inst.env) ?? []), inst])
  }
  return Array.from(byEnv.values()).map((group) => {
    const active = group.find((i) => i.appKey === activeKey)
    const newest = group.reduce<AppIdentityInstance | undefined>((best, inst) => {
      if (!best) return inst
      return compareDefinedVersions(inst.version, best.version) === 1 ? inst : best
    }, undefined)
    const display = active ?? newest ?? group[0]
    return {
      ...display,
      health: worstHealth(group.map((inst) => inst.health)),
      version: newest?.version,
      count: group.length,
    }
  })
}

function compareDefinedVersions(a: string | undefined, b: string | undefined): number {
  if (!a && !b) return 0
  if (a && !b) return 1
  if (!a || !b) return -1
  return compareVersions(a, b) ?? 0
}

export function ApplicationDetail({ app, onBack, renderWorkload, topology, topologyLoading, onNavigateToResource, identityInstances, onSwitchInstance, discoveredEnvs, activeInstanceKey, history, historyLoading, onOpenSource, selectedWorkloadKey, onSelectWorkload, selectedView, onSelectView }: ApplicationDetailProps) {
  // Stable order regardless of API ordering: rail rows and the per-workload
  // color assignment both follow this array, so an order flap between
  // refetches must not reshuffle rows or reassign a workload's hue.
  const workloads = useMemo(
    () =>
      [...(app.workloads ?? [])].sort(
        (a, b) => a.name.localeCompare(b.name) || a.kind.localeCompare(b.kind) || a.namespace.localeCompare(b.namespace),
      ),
    [app.workloads],
  )
  const overall = worstHealth([app.health, ...workloads.map((w) => w.health)])
  const verdictTone = HEALTH_META[overall].pill
  const verdictLabel = HEALTH_META[overall].label
  const workloadClass = workloadClassOf(app.workload_class)
  const versions = useMemo(() => Array.from(new Set((app.versions || []).filter(Boolean))), [app.versions])
  const ready = workloads.reduce((n, w) => n + (w.ready ?? 0), 0)
  const desired = workloads.reduce((n, w) => n + (w.desired ?? 0), 0)
  const restartSignal = restartWarning(workloads)
  // Resolve namespace the same way the list does (the workloads' shared
  // namespace) so env/namespace match across list and detail. Multi-namespace
  // apps get the count, never an arbitrary pick.
  const namespace = namespaceOf(app)
  const namespaces = namespacesOf(app)
  const resolvedEnv = resolveEnv(undefined, namespace, discoveredEnvs)
  const env = app.identity?.env ?? resolvedEnv.env
  const inferred = app.identity ? identityEnvInferred(app.identity) : resolvedEnv.inferred
  const [internalView, setInternalView] = useState<CanonicalApplicationView>(DEFAULT_APPLICATION_VIEW)
  const activeView = canonicalApplicationView(selectedView ?? internalView)
  const setView = useCallback(
    (view: CanonicalApplicationView) => (onSelectView ? onSelectView(view) : setInternalView(view)),
    [onSelectView],
  )
  useEffect(() => {
    if (selectedView === undefined) setInternalView(DEFAULT_APPLICATION_VIEW)
  }, [app.key, selectedView])

  const [internalSelected, setInternalSelected] = useState<string | null>(null)
  const implicitSingleWorkloadKey = workloads.length === 1 ? workloadKey(workloads[0]) : null
  const rawSelected = selectedWorkloadKey !== undefined ? selectedWorkloadKey : (internalSelected ?? implicitSingleWorkloadKey)
  const setSelected = useCallback(
    (key: string | null) => (onSelectWorkload ? onSelectWorkload(key) : setInternalSelected(key)),
    [onSelectWorkload],
  )
  const selectedWorkload = rawSelected ? workloads.find((w) => workloadKey(w) === rawSelected) : undefined
  const singleWorkloadScope = workloads.length === 1 && !!selectedWorkload
  useEffect(() => {
    if (selectedWorkloadKey !== undefined && selectedWorkloadKey !== null && !selectedWorkload) {
      onSelectWorkload?.(null)
    }
  }, [selectedWorkloadKey, selectedWorkload, onSelectWorkload])
  useEffect(() => {
    if (selectedWorkloadKey === undefined) setInternalSelected(null)
  }, [app.key, selectedWorkloadKey])

  // Hover-focus: the workload (or NEUTRAL_OWNER) whose nodes should stay lit
  // while the rest of the graph dims. Driven by the rail and, reciprocally, by
  // hovering a node.
  const [focusedOwnerId, setFocusedOwnerId] = useState<WorkloadFocus>(null)

  const appSeeds = useMemo(
    () => workloads.map((w) => ({ kind: w.kind, namespace: w.namespace, name: w.name })),
    [workloads],
  )
  // Neighborhood subgraph + per-workload color/ownership tagging in one pass.
  const ownership = useMemo(
    () => (topology ? tagWorkloadOwnership(topology, appSeeds) : null),
    [topology, appSeeds],
  )
  const appGraph = ownership?.topology ?? null
  const appGraphFocusId = useMemo(
    () => (topology ? seedNodeIds(topology, appSeeds)[0] : undefined),
    [topology, appSeeds],
  )

  // Hovering a node lights up its owning workload (and the rail row). An
  // unowned node related to exactly ONE workload (a GitOps manager over a
  // single workload here) still focuses that workload, mirroring rail-driven
  // focus. Truly shared nodes clear instead of dimming everything.
  const handleNodeHover = useCallback((node: TopologyNode | null) => {
    if (!node) {
      setFocusedOwnerId(null)
      return
    }
    const stamp = ownershipOf(node.data)
    setFocusedOwnerId(stamp.ownerWorkloadId ?? (stamp.focusWorkloadIds.length === 1 ? stamp.focusWorkloadIds[0] : NEUTRAL_OWNER))
  }, [])

  // A node click in the app graph either drills into one of the app's workloads
  // (its runtime) or opens a related resource (Service/config/…) via the host.
  const handleAppNodeClick = useCallback(
    (node: TopologyNode) => {
      const ns = (node.data?.namespace as string) || ''
      const match = workloads.find((w) => w.kind === node.kind && w.name === node.name && w.namespace === ns)
      if (match) {
        setSelected(workloadKey(match))
        return
      }
      onNavigateToResource?.({
        kind: kindToPlural(node.kind),
        namespace: ns,
        name: node.name,
        group: apiVersionToGroup(node.data?.apiVersion as string | undefined),
      })
    },
    [workloads, onNavigateToResource, setSelected],
  )

  const appTopology = appGraph ?? null
  const appTopologyAvailable = !!appTopology && appTopology.nodes.length > 0
  const colorByWorkload = ownership?.colorByWorkload ?? null

  return (
    <div className="flex min-h-0 w-full flex-1 flex-col bg-theme-base">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-theme-border px-4 py-3 sm:px-6">
        <button type="button" onClick={onBack} className="flex shrink-0 items-center gap-1.5 text-xs text-theme-text-tertiary hover:text-theme-text-primary">
          <ArrowLeft className="h-3.5 w-3.5" aria-hidden /> Applications
        </button>
        <span className="hidden h-6 w-px bg-theme-border sm:block" aria-hidden />
        <span className={`flex h-8 w-8 shrink-0 items-center justify-center rounded-md ring-1 ring-inset ${verdictTone}`}>
          <Boxes className="h-4 w-4" aria-hidden />
        </span>
        <div className="flex min-w-0 flex-1 items-center gap-2">
          <h1 className="min-w-0 truncate text-xl font-semibold text-theme-text-primary lg:text-2xl">{app.name}</h1>
          {!singleWorkloadScope && (
            <>
              <span className="shrink-0 text-theme-text-tertiary">/</span>
              <ApplicationScopeSelector
                workloads={workloads}
                selectedWorkload={selectedWorkload ?? null}
                onSelect={setSelected}
                onFocus={setFocusedOwnerId}
              />
            </>
          )}
        </div>
        <div className="ml-auto flex shrink-0 flex-wrap items-center justify-end gap-2">
          <span className={`inline-flex items-center gap-2 rounded-md px-2.5 py-1 ring-1 ring-inset ${verdictTone}`}>
            <StatusDot tone={mapHealthToTone(overall)} />
            <span className="text-sm font-semibold">{verdictLabel}</span>
          </span>
          {restartSignal && (
            <Tooltip content={`${restartSignal.workload} · ${pluralize(restartSignal.restarts, 'restart')}`} delay={150}>
              <span className={`inline-flex items-center rounded-md px-2 py-1 text-xs font-semibold ring-1 ring-inset ${CHIP_TONE.amber}`}>
                Pod warning: {restartSignal.reason || 'Restarts'} · {pluralize(restartSignal.restarts, 'restart')}
              </span>
            </Tooltip>
          )}
          {/* Amber only on real skew (same image, different tags) — the context
              strip already covers the multi-image "N versions" case neutrally. */}
          {app.versionSkew && versions.length > 1 && (
            <Tooltip content={<VersionTooltip workloads={workloads} />} delay={150}>
              <span className={`inline-flex items-center rounded-md px-2 py-1 font-mono text-xs ring-1 ring-inset ${CHIP_TONE.amber}`}>{versions.length} versions</span>
            </Tooltip>
          )}
        </div>
      </div>

      {/* Context strip */}
      <div className="flex flex-wrap items-center gap-x-5 gap-y-2 border-b border-theme-border px-4 py-2 sm:px-6">
        <ProvenanceBadge tier={app.tier} appKey={app.key} confidence={app.confidence} />
        <CategoryChip category={app.category} addonReason={app.addonReason} />
        <ClassBadge workloadClass={workloadClass} composition={classCompositionOf(app)} />
        {singleWorkloadScope && selectedWorkload.name !== app.name && (
          <ContextFact label="Workload">
            <span className="font-mono">{selectedWorkload.kind}/{selectedWorkload.name}</span>
          </ContextFact>
        )}
        {identityInstances && identityInstances.length > 1 ? (
          // The Environment fact IS the switcher when this app runs in several
          // envs — prominent, in existing header space, no extra row. Inline
          // pills for a handful; a picker beyond that (scales to ~any count).
          <div className="flex min-w-0 items-center gap-1.5">
            <span className="text-[10px] uppercase tracking-wide text-theme-text-tertiary">Environment</span>
            <EnvSwitcher identityKey={app.identity?.key ?? ''} instances={identityInstances} activeKey={activeInstanceKey ?? app.key} onSwitch={onSwitchInstance} />
          </div>
        ) : env ? (
          <ContextFact label="Environment">
            {inferred ? (
              <Tooltip content={`Inferred from namespace "${namespace || env}" — confirm with an environment label.`} delay={150}>
                <span className="italic">~{env}</span>
              </Tooltip>
            ) : (
              env
            )}
          </ContextFact>
        ) : null}
        {namespace ? (
          <ContextFact label="Namespace">
            <span className="font-mono">{namespace}</span>
          </ContextFact>
        ) : namespaces.length > 1 ? (
          <ContextFact label="Namespaces">
            <Tooltip content={namespaces.join(', ')} delay={150}>
              <span>{namespaces.length} namespaces</span>
            </Tooltip>
          </ContextFact>
        ) : null}
        <ContextFact label="Ready">
          <ReadyBar ready={ready} desired={desired} width="w-16" />
        </ContextFact>
        {(app.appVersion || versions.length > 0) && (
          <ContextFact label="Version">
            <VersionInfo app={app} variant="fact" />
          </ContextFact>
        )}
      </div>

      <div className="min-h-0 flex-1 overflow-hidden bg-theme-base">
        {selectedWorkload ? (
          renderRuntime(selectedWorkload, renderWorkload)
        ) : (
          <ApplicationWorkspace
            app={app}
            activeView={activeView}
            onViewChange={setView}
            workloads={workloads}
            namespace={namespace}
            namespaces={namespaces}
            verdictLabel={verdictLabel}
            ready={ready}
            desired={desired}
            versions={versions}
            topology={appTopology}
            topologyLoading={topologyLoading}
            topologyAvailable={appTopologyAvailable}
            focusNodeId={appGraphFocusId}
            focusedOwnerId={focusedOwnerId}
            colorByWorkload={colorByWorkload}
            onNodeClick={handleAppNodeClick}
            onNodeHover={handleNodeHover}
            onFocusWorkload={setFocusedOwnerId}
            onSelectWorkload={(workload) => setSelected(workloadKey(workload))}
            onNavigateToResource={onNavigateToResource}
            history={history}
            historyLoading={historyLoading}
            onOpenSource={onOpenSource}
          />
        )}
      </div>
    </div>
  )
}

function ApplicationWorkspace({
  app,
  activeView,
  onViewChange,
  workloads,
  namespace,
  namespaces,
  verdictLabel,
  ready,
  desired,
  versions,
  topology,
  topologyLoading,
  topologyAvailable,
  focusNodeId,
  focusedOwnerId,
  colorByWorkload,
  onNodeClick,
  onNodeHover,
  onFocusWorkload,
  onSelectWorkload,
  onNavigateToResource,
  history,
  historyLoading,
  onOpenSource,
}: {
  app: AppRow
  activeView: CanonicalApplicationView
  onViewChange: (view: CanonicalApplicationView) => void
  workloads: AppWorkload[]
  namespace: string
  namespaces: string[]
  verdictLabel: string
  ready: number
  desired: number
  versions: string[]
  topology: Topology | null
  topologyLoading?: boolean
  topologyAvailable: boolean
  focusNodeId?: string
  focusedOwnerId: WorkloadFocus
  colorByWorkload: Map<string, number> | null
  onNodeClick: (node: TopologyNode) => void
  onNodeHover: (node: TopologyNode | null) => void
  onFocusWorkload: (owner: WorkloadFocus) => void
  onSelectWorkload: (workload: AppWorkload) => void
  onNavigateToResource?: (resource: { kind: string; namespace: string; name: string; group?: string }) => void
  history?: AppHistory
  historyLoading?: boolean
  onOpenSource?: (source: AppSourceRef) => void
}) {
  const historyCount = (history?.anchors?.length ?? 0) + (history?.incidents?.length ?? (app.events?.length ?? 0))
  return (
    <div className="flex h-full min-h-0 flex-col">
      <ApplicationViewTabs
        activeView={activeView}
        historyCount={historyCount}
        onChange={onViewChange}
      />
      {activeView === 'overview' && (
        <ApplicationOverview
          app={app}
          workloads={workloads}
          namespace={namespace}
          namespaces={namespaces}
          verdictLabel={verdictLabel}
          ready={ready}
          desired={desired}
          versions={versions}
          onSelectWorkload={onSelectWorkload}
          onNavigateToResource={onNavigateToResource}
          history={history}
          onSelectHistory={() => onViewChange('history')}
          onOpenSource={onOpenSource}
        />
      )}
      {activeView === 'topology' && (
        <ApplicationTopology
          topology={topology}
          loading={topologyLoading}
          available={topologyAvailable}
          workloads={workloads}
          colorByWorkload={colorByWorkload}
          focusNodeId={focusNodeId}
          focusedOwnerId={focusedOwnerId}
          onNodeClick={onNodeClick}
          onNodeHover={onNodeHover}
          onFocusWorkload={onFocusWorkload}
          onSelectWorkload={onSelectWorkload}
        />
      )}
      {activeView === 'history' && <ApplicationHistoryView history={history} loading={historyLoading} fallbackEvents={app.events ?? []} workloads={workloads} onSelectWorkload={onSelectWorkload} onOpenSource={onOpenSource} />}
    </div>
  )
}

function ApplicationViewTabs({
  activeView,
  historyCount,
  onChange,
}: {
  activeView: CanonicalApplicationView
  historyCount: number
  onChange: (view: CanonicalApplicationView) => void
}) {
  return (
    <div className="flex shrink-0 items-center border-b border-theme-border px-4 sm:px-6" role="tablist" aria-label="Application views">
      <div className="flex min-w-0 gap-1 overflow-x-auto">
        {APPLICATION_VIEWS.map((view) => {
          const active = view.id === activeView
          const badge = view.id === 'history' && historyCount > 0 ? historyCount : null
          return (
            <button
              key={view.id}
              type="button"
              role="tab"
              aria-selected={active}
              onClick={() => onChange(view.id)}
              className={clsx(
                'flex items-center gap-1.5 whitespace-nowrap border-b-2 px-3 py-2 text-sm font-medium transition-colors',
                active
                  ? 'border-skyhook-500 text-theme-text-primary'
                  : 'border-transparent text-theme-text-secondary hover:border-theme-border-light hover:text-theme-text-primary',
              )}
            >
              {view.label}
              {badge !== null && (
                <span className="rounded bg-theme-hover px-1.5 py-0.5 text-[10px] font-semibold text-theme-text-tertiary">
                  {badge}
                </span>
              )}
            </button>
          )
        })}
      </div>
    </div>
  )
}

type AppIssueSeverity = 'error' | 'warning' | 'info'

type AppIssue = {
  key: string
  severity: AppIssueSeverity
  title: string
  detail?: string
  workload?: AppWorkload
}

function ApplicationOverview({
  app,
  workloads,
  namespace,
  namespaces,
  verdictLabel,
  ready,
  desired,
  versions,
  onSelectWorkload,
  onNavigateToResource,
  history,
  onSelectHistory,
  onOpenSource,
}: {
  app: AppRow
  workloads: AppWorkload[]
  namespace: string
  namespaces: string[]
  verdictLabel: string
  ready: number
  desired: number
  versions: string[]
  onSelectWorkload: (workload: AppWorkload) => void
  onNavigateToResource?: (resource: { kind: string; namespace: string; name: string; group?: string }) => void
  history?: AppHistory
  onSelectHistory: () => void
  onOpenSource?: (source: AppSourceRef) => void
}) {
  const rel = app.relationships
  const hasEntrypoints = Boolean(rel && ((rel.services?.length ?? 0) > 0 || (rel.ingresses?.length ?? 0) > 0 || (rel.routes?.length ?? 0) > 0))
  const hasDependencies = Boolean(rel && ((rel.configs ?? 0) > 0 || (rel.scalers ?? 0) > 0 || (rel.storage ?? 0) > 0 || (rel.pdbs ?? 0) > 0))
  const composition = classCompositionOf(app)
    .map(({ cls, count }) => `${count} ${cls}`)
    .join(' / ')
  const issues = useMemo(() => buildAppIssues(workloads, app.events ?? []), [workloads, app.events])

  return (
    <div className="min-h-0 flex-1 overflow-auto">
      <div className="grid w-full max-w-[2400px] gap-4 p-4 sm:p-6 xl:grid-cols-[minmax(0,1fr)_minmax(320px,380px)]">
        <div className="min-w-0 space-y-4">
          <ApplicationNow issues={issues} ready={ready} desired={desired} onSelectWorkload={onSelectWorkload} />
          <ApplicationLatestHistory history={history} sourceRef={app.sourceRef} onSelectHistory={onSelectHistory} onOpenSource={onOpenSource} />
          <div className="grid gap-3 md:grid-cols-2 2xl:grid-cols-4">
            <ApplicationFact label="State" value={verdictLabel} />
            <ApplicationFact label="Workloads" value={String(workloads.length)} detail={composition || 'No workloads'} />
            <ApplicationFact label="Ready" value={`${ready}/${desired}`} detail={desired === 0 ? 'No desired replicas' : undefined} />
            <ApplicationFact label="Version" value={app.appVersion || (versions.length === 1 ? versions[0] : versions.length > 1 ? `${versions.length} versions` : 'Unknown')} />
          </div>
          {hasEntrypoints && (
            <ApplicationEntrypoints
              relationships={rel}
              namespace={namespace}
              namespaces={namespaces}
              onNavigateToResource={onNavigateToResource}
            />
          )}
          <ApplicationPanel title="Workloads">
            <WorkloadsMatrix workloads={workloads} onSelectWorkload={onSelectWorkload} />
          </ApplicationPanel>
        </div>
        <aside className="min-w-0 space-y-4">
          <ApplicationSourceProvenance app={app} namespace={namespace} namespaces={namespaces} composition={composition} onOpenSource={onOpenSource} />
          {hasDependencies && <ApplicationDependencies relationships={rel} />}
        </aside>
      </div>
    </div>
  )
}

function ApplicationSourceProvenance({
  app,
  namespace,
  namespaces,
  composition,
  onOpenSource,
}: {
  app: AppRow
  namespace: string
  namespaces: string[]
  composition: string
  onOpenSource?: (source: AppSourceRef) => void
}) {
  const source = app.identity?.source ? appSourceLabel(app.identity.source) : app.tier ? overlayProvenance(app.tier) : 'raw workload'
  const confidence = app.confidence || (app.tier ? 'low' : 'raw')
  return (
    <ApplicationPanel title="Source & provenance">
      <div className="grid gap-3">
        <ApplicationFact variant="row" label="Grouped by" value={source} detail={app.identity?.evidence ?? app.key} monoDetail />
        <ApplicationFact variant="row" label="App key" value={app.name} detail={app.key} monoDetail />
        <ApplicationFact
          variant="row"
          label={namespaces.length > 1 ? 'Namespaces' : 'Namespace'}
          value={namespace || `${namespaces.length} namespaces`}
          detail={namespaces.length > 1 ? namespaces.join(', ') : undefined}
          monoDetail
        />
        <div className="grid grid-cols-2 gap-4 border-t border-theme-border pt-3">
          <ApplicationFact variant="bare" label="Confidence" value={confidence} />
          <ApplicationFact variant="bare" label="Class" value={workloadClassOf(app.workload_class)} detail={composition || undefined} />
        </div>
        {app.sourceRef && onOpenSource && (
          <button
            type="button"
            onClick={() => onOpenSource(app.sourceRef!)}
            className="inline-flex w-fit items-center gap-1.5 rounded-md px-2.5 py-1.5 text-sm font-medium text-accent-text hover:bg-theme-hover"
          >
            <ExternalLink className="h-3.5 w-3.5" aria-hidden />
            {sourceLinkLabel(app.sourceRef)}
          </button>
        )}
      </div>
    </ApplicationPanel>
  )
}

function ApplicationEntrypoints({
  relationships,
  namespace,
  namespaces,
  onNavigateToResource,
}: {
  relationships: AppRow['relationships']
  namespace: string
  namespaces: string[]
  onNavigateToResource?: (resource: { kind: string; namespace: string; name: string; group?: string }) => void
}) {
  const serviceCount = relationships?.services?.length ?? 0
  const ingressCount = relationships?.ingresses?.length ?? 0
  const routeCount = relationships?.routes?.length ?? 0
  const hasExternal = ingressCount + routeCount > 0
  if (serviceCount + ingressCount + routeCount === 0) return null

  return (
    <ApplicationPanel title="Entrypoints">
      <div className="space-y-3">
        <div>
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="text-sm font-semibold text-theme-text-primary">
              {hasExternal ? 'External entrypoints configured' : 'Internal services configured'}
            </h3>
            <span className={`${CHIP} ${hasExternal ? CHIP_TONE.blue : CHIP_TONE.muted}`}>
              {hasExternal ? 'External' : 'Internal'}
            </span>
          </div>
          <p className="mt-1 text-sm text-theme-text-secondary">
            {hasExternal
              ? 'Traffic can enter this application through ingress or route resources.'
              : 'Services target this application, but no ingress or route was detected.'}
          </p>
        </div>
        <div className="grid gap-3 sm:grid-cols-3">
          <ApplicationFact variant="bare" label="Services" value={String(serviceCount)} />
          <ApplicationFact variant="bare" label="Ingresses" value={String(ingressCount)} />
          <ApplicationFact variant="bare" label="Routes" value={String(routeCount)} />
        </div>
        <div className="space-y-3 border-t border-theme-border pt-3">
          <ApplicationRelatedNameGroup
            label="Services"
            kind="Service"
            names={relationships?.services}
            namespace={namespace}
            namespaces={namespaces}
            onNavigateToResource={onNavigateToResource}
          />
          <ApplicationRelatedNameGroup
            label="Ingresses"
            kind="Ingress"
            names={relationships?.ingresses}
            namespace={namespace}
            namespaces={namespaces}
            onNavigateToResource={onNavigateToResource}
          />
          <ApplicationRelatedNameGroup label="Routes" kind="Route" names={relationships?.routes} namespace={namespace} namespaces={namespaces} />
        </div>
      </div>
    </ApplicationPanel>
  )
}

function ApplicationDependencies({ relationships }: { relationships: AppRow['relationships'] }) {
  const configCount = relationships?.configs ?? 0
  const scalerCount = relationships?.scalers ?? 0
  const storageCount = relationships?.storage ?? 0
  const pdbCount = relationships?.pdbs ?? 0
  if (configCount + scalerCount + storageCount + pdbCount === 0) return null

  return (
    <ApplicationPanel title="Dependencies">
      <div className="space-y-3">
        <ApplicationDependencyRow label="Configuration" count={configCount} detail="ConfigMaps and Secrets referenced by app workloads." />
        <ApplicationDependencyRow label="Autoscaling" count={scalerCount} detail="Autoscalers controlling app workloads." />
        <ApplicationDependencyRow label="Storage" count={storageCount} detail="PersistentVolumeClaims mounted by app workloads." />
        <ApplicationDependencyRow label="Availability policy" count={pdbCount} detail="PodDisruptionBudgets protecting app workloads." />
      </div>
    </ApplicationPanel>
  )
}

function ApplicationDependencyRow({ label, count, detail }: { label: string; count: number; detail: string }) {
  if (!count) return null
  return (
    <div className="min-w-0 border-b border-theme-border pb-3 last:border-b-0">
      <div className="flex items-baseline justify-between gap-3">
        <div className="text-sm font-semibold text-theme-text-primary">{label}</div>
        <span className={`${CHIP} ${CHIP_TONE.muted}`}>{pluralize(count, 'resource')}</span>
      </div>
      <div className="mt-1 text-xs text-theme-text-tertiary">{detail}</div>
    </div>
  )
}

function ApplicationRelatedNameGroup({
  label,
  kind,
  names,
  namespace,
  namespaces,
  onNavigateToResource,
}: {
  label: string
  kind: string
  names?: string[]
  namespace: string
  namespaces: string[]
  onNavigateToResource?: (resource: { kind: string; namespace: string; name: string; group?: string }) => void
}) {
  if (!names || names.length === 0) return null

  const canNavigate = Boolean(onNavigateToResource && namespace && namespaces.length <= 1 && kind !== 'Route')
  const refs: ResourceRef[] = names.map((name) => ({ kind, namespace, name }))
  const visible = names.slice(0, 12)
  const overflow = Math.max(0, names.length - visible.length)
  return (
    <div>
      <div className="mb-1 text-xs font-medium text-theme-text-tertiary">{label}{names.length > 1 ? ` (${names.length})` : ''}</div>
      <div className="flex flex-wrap gap-1.5">
        {canNavigate
          ? refs.slice(0, 12).map((ref) => (
              <ResourceRefBadge
                key={`${ref.kind}/${ref.namespace}/${ref.name}`}
                resourceRef={ref}
                onClick={(clicked) => onNavigateToResource?.(refToSelectedResource(clicked))}
              />
            ))
          : visible.map((name) => <ApplicationRelatedChip key={`${kind}/${name}`} kind={kind} name={name} />)}
        {overflow > 0 && <span className={`${CHIP} ${CHIP_TONE.muted}`}>+{overflow} more</span>}
      </div>
    </div>
  )
}

function ApplicationRelatedChip({ kind, name }: { kind: string; name: string }) {
  return (
    <Tooltip content={`${kind}: ${name}`} delay={300}>
      <span className={`${CHIP} ${CHIP_TONE.muted}`}>
        <span className="opacity-70">{kind}/</span>{midTruncate(name, 28)}
      </span>
    </Tooltip>
  )
}

function ApplicationNow({
  issues,
  ready,
  desired,
  onSelectWorkload,
}: {
  issues: AppIssue[]
  ready: number
  desired: number
  onSelectWorkload: (workload: AppWorkload) => void
}) {
  if (issues.length === 0) {
    return (
      <section className="rounded-lg border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm">
        <div className="flex flex-wrap items-center gap-3">
          <span className={`flex h-8 w-8 shrink-0 items-center justify-center rounded-md ring-1 ring-inset ${CHIP_TONE.emerald}`}>
            <CheckCircle2 className="h-4 w-4" aria-hidden />
          </span>
          <div className="min-w-0 flex-1">
            <h2 className="text-sm font-semibold text-theme-text-primary">No application issues detected</h2>
            <p className="mt-0.5 text-sm text-theme-text-secondary">
              {desired === 0 ? 'No desired workload replicas are currently expected.' : `${ready}/${desired} workload replicas are ready.`}
            </p>
          </div>
        </div>
      </section>
    )
  }

  const top = issues[0]
  return (
    <section className="rounded-lg border border-theme-border bg-theme-surface shadow-theme-sm">
      <div className="flex flex-wrap items-start gap-3 border-b border-theme-border px-4 py-3">
        <span className={`flex h-8 w-8 shrink-0 items-center justify-center rounded-md ring-1 ring-inset ${issueTone(top.severity)}`}>
          <AlertTriangle className="h-4 w-4" aria-hidden />
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="text-sm font-semibold text-theme-text-primary">{top.title}</h2>
            <span className={`${CHIP} ${issueTone(top.severity)}`}>{top.severity === 'error' ? 'Needs attention' : 'Warning'}</span>
          </div>
          {top.detail && <p className="mt-0.5 text-sm text-theme-text-secondary">{top.detail}</p>}
        </div>
        {top.workload && (
          <button type="button" onClick={() => onSelectWorkload(top.workload!)} className="rounded-md px-2.5 py-1.5 text-sm font-medium text-accent-text hover:bg-theme-hover">
            Open workload
          </button>
        )}
      </div>
      {issues.length > 1 && (
        <div className="divide-y divide-theme-border px-4">
          {issues.slice(1, 5).map((issue) => (
            <div key={issue.key} className="flex items-start gap-3 py-3">
              <StatusDot tone={issue.severity === 'error' ? 'unhealthy' : issue.severity === 'warning' ? 'degraded' : 'neutral'} className="mt-1" />
              <div className="min-w-0 flex-1">
                <div className="truncate text-sm font-medium text-theme-text-primary">{issue.title}</div>
                {issue.detail && <div className="truncate text-sm text-theme-text-tertiary">{issue.detail}</div>}
              </div>
              {issue.workload && (
                <button type="button" onClick={() => onSelectWorkload(issue.workload!)} className="shrink-0 text-sm font-medium text-accent-text hover:underline">
                  Open
                </button>
              )}
            </div>
          ))}
        </div>
      )}
    </section>
  )
}

function ApplicationLatestHistory({
  history,
  sourceRef,
  onSelectHistory,
  onOpenSource,
}: {
  history?: AppHistory
  sourceRef?: AppSourceRef
  onSelectHistory: () => void
  onOpenSource?: (source: AppSourceRef) => void
}) {
  const summary = history?.summary
  if (!summary || summary.state !== 'change') return null
  const resolvedSource = sourceRef ?? history?.sourceRef

  return (
    <section className="rounded-lg border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm">
      <div className="flex flex-wrap items-start gap-3">
        <span className={`flex h-8 w-8 shrink-0 items-center justify-center rounded-md ring-1 ring-inset ${CHIP_TONE.blue}`}>
          <Clock3 className="h-4 w-4" aria-hidden />
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="text-sm font-semibold text-theme-text-primary">{summary.title}</h2>
            <span className={`${CHIP} ${CHIP_TONE.blue}`}>Latest change</span>
          </div>
          {summary.detail && <p className="mt-0.5 line-clamp-2 text-sm text-theme-text-secondary">{summary.detail}</p>}
          {summary.timestamp && <p className="mt-1 text-xs text-theme-text-tertiary">{formatAppEventTime(summary.timestamp)}</p>}
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {resolvedSource && onOpenSource && (
            <button type="button" onClick={() => onOpenSource(resolvedSource)} className="rounded-md px-2.5 py-1.5 text-sm font-medium text-accent-text hover:bg-theme-hover">
              {sourceLinkLabel(resolvedSource)}
            </button>
          )}
          <button type="button" onClick={onSelectHistory} className="rounded-md px-2.5 py-1.5 text-sm font-medium text-accent-text hover:bg-theme-hover">
            View history
          </button>
        </div>
      </div>
    </section>
  )
}

function WorkloadsMatrix({ workloads, onSelectWorkload }: { workloads: AppWorkload[]; onSelectWorkload: (workload: AppWorkload) => void }) {
  if (workloads.length === 0) {
    return <EmptyState variant="inline" headline="No inspectable workloads." />
  }

  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[760px] table-fixed text-sm">
        <thead>
          <tr className="border-b border-theme-border text-left text-[10px] uppercase tracking-wide text-theme-text-tertiary">
            <th className="w-[34%] px-2 py-2 font-semibold">Workload</th>
            <th className="w-[13%] px-2 py-2 font-semibold">Kind</th>
            <th className="w-[12%] px-2 py-2 font-semibold">Class</th>
            <th className="w-[14%] px-2 py-2 font-semibold">Health</th>
            <th className="w-[11%] px-2 py-2 font-semibold">Ready</th>
            <th className="w-[16%] px-2 py-2 font-semibold">Version</th>
          </tr>
        </thead>
        <tbody>
          {workloads.map((w) => {
            const tone = healthOf(w.health)
            return (
              <tr key={workloadKey(w)} className="border-b border-theme-border last:border-b-0 hover:bg-theme-hover">
                <td className="truncate px-2 py-2">
                  <button
                    type="button"
                    onClick={() => onSelectWorkload(w)}
                    className="truncate font-medium text-theme-text-primary hover:text-accent-text hover:underline"
                  >
                    {w.name}
                  </button>
                  {w.reason && <div className="truncate text-xs text-theme-text-tertiary">{w.reason}</div>}
                </td>
                <td className="px-2 py-2 text-theme-text-secondary">{w.kind}</td>
                <td className="px-2 py-2 text-theme-text-secondary">{workloadClassOf(w.workload_class)}</td>
                <td className="px-2 py-2">
                  <span className="inline-flex items-center gap-1.5 text-theme-text-secondary">
                    <StatusDot tone={mapHealthToTone(tone)} />
                    {HEALTH_META[tone].label}
                  </span>
                </td>
                <td className="px-2 py-2 font-mono text-xs text-theme-text-secondary">{w.ready}/{w.desired}</td>
                <td className="truncate px-2 py-2 font-mono text-xs text-theme-text-secondary">{w.version || w.appVersion || '-'}</td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </div>
  )
}

function ApplicationTopology({
  topology,
  loading,
  available,
  workloads,
  colorByWorkload,
  focusNodeId,
  focusedOwnerId,
  onNodeClick,
  onNodeHover,
  onFocusWorkload,
  onSelectWorkload,
}: {
  topology: Topology | null
  loading?: boolean
  available: boolean
  workloads: AppWorkload[]
  colorByWorkload: Map<string, number> | null
  focusNodeId?: string
  focusedOwnerId: WorkloadFocus
  onNodeClick: (node: TopologyNode) => void
  onNodeHover: (node: TopologyNode | null) => void
  onFocusWorkload: (owner: WorkloadFocus) => void
  onSelectWorkload: (workload: AppWorkload) => void
}) {
  const hasSharedOrUnscopedNodes = useMemo(
    () => topology?.nodes.some((node) => {
      const stamp = ownershipOf(node.data)
      return !stamp.ownerWorkloadId && stamp.focusWorkloadIds.length !== 1
    }) ?? false,
    [topology],
  )

  return (
    <div className="relative min-h-0 flex-1 bg-theme-surface">
      {available && topology ? (
        <>
          <TopologyGraph
            topology={topology}
            viewMode="resources"
            groupingMode="namespace"
            hideGroupHeader
            onNodeClick={onNodeClick}
            showExportButton={false}
            focusNodeId={focusNodeId}
            focusedOwnerId={focusedOwnerId}
            onNodeHover={onNodeHover}
          />
          <TopologyWorkloadLegend
            workloads={workloads}
            colorByWorkload={colorByWorkload}
            focusedOwnerId={focusedOwnerId}
            showSharedOrUnscoped={hasSharedOrUnscopedNodes}
            onFocus={onFocusWorkload}
            onSelectWorkload={onSelectWorkload}
          />
        </>
      ) : loading ? (
        <PaneLoader label="Loading topology..." className="absolute inset-0" />
      ) : (
        <div className="flex h-full items-center justify-center p-6">
          <EmptyState headline="No topology available" body="Radar could not build an application topology for this app." />
        </div>
      )}
    </div>
  )
}

function ApplicationHistoryView({
  history,
  loading,
  fallbackEvents,
  workloads,
  onSelectWorkload,
  onOpenSource,
}: {
  history?: AppHistory
  loading?: boolean
  fallbackEvents: NonNullable<AppRow['events']>
  workloads: AppWorkload[]
  onSelectWorkload: (workload: AppWorkload) => void
  onOpenSource?: (source: AppSourceRef) => void
}) {
  const anchors = history?.anchors ?? []
  const incidents = history?.incidents ?? fallbackEvents.map((event) => ({
    severity: 'warning',
    title: event.object ? `${event.reason} on ${event.object}` : event.reason,
    object: event.object,
    message: event.message,
    count: event.count,
    firstSeen: event.firstSeen,
    lastSeen: event.lastSeen,
  }))
  const empty = !loading && anchors.length === 0 && incidents.length === 0

  return (
    <div className="min-h-0 flex-1 overflow-auto">
      <div className="w-full max-w-[2400px] space-y-4 p-4 sm:p-6">
        {loading && <ApplicationPanel title="History"><div className="text-sm text-theme-text-tertiary">Loading application history...</div></ApplicationPanel>}
        {history?.partialSources && history.partialSources.length > 0 && (
          <ApplicationPanel title="Partial history">
            <div className="space-y-1 text-sm text-theme-text-secondary">
              {history.partialSources.map((msg, idx) => <div key={`${msg}-${idx}`}>{msg}</div>)}
            </div>
          </ApplicationPanel>
        )}
        {incidents.length > 0 && (
          <ApplicationPanel title="Current incidents">
            <div className="divide-y divide-theme-border">
              {incidents.map((incident, idx) => {
                const workload = directWorkloadForEvent({ object: incident.object, reason: '', count: 1, type: 'Warning' }, workloads)
                return (
                  <HistoryIncidentLine
                    key={`${incident.object}-${incident.title}-${idx}`}
                    incident={incident}
                    action={workload ? <button type="button" onClick={() => onSelectWorkload(workload)} className="text-xs font-medium text-accent-text hover:underline">Open workload</button> : undefined}
                  />
                )
              })}
            </div>
          </ApplicationPanel>
        )}
        {anchors.length > 0 && (
          <ApplicationPanel
            title={
              <span className="flex items-center justify-between gap-3">
                <span>Deployment history</span>
                {history?.sourceRef && onOpenSource && (
                  <button type="button" onClick={() => onOpenSource(history.sourceRef!)} className="text-xs font-medium text-accent-text hover:underline">
                    {sourceLinkLabel(history.sourceRef)}
                  </button>
                )}
              </span>
            }
          >
            <div className="divide-y divide-theme-border">
              {anchors.map((anchor, idx) => <HistoryAnchorLine key={`${anchor.type}-${anchor.timestamp}-${anchor.revision}-${idx}`} anchor={anchor} />)}
            </div>
          </ApplicationPanel>
        )}
        {empty && (
          <ApplicationPanel title="History">
            <EmptyState variant="inline" headline="No retained deployment history." body="Current application state is still available in Overview." />
          </ApplicationPanel>
        )}
      </div>
    </div>
  )
}

function HistoryAnchorLine({ anchor }: { anchor: NonNullable<AppHistory['anchors']>[number] }) {
  return (
    <div className="grid grid-cols-[minmax(0,1fr)_auto] gap-3 py-3 first:pt-0 last:pb-0">
      <div className="min-w-0">
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-medium text-theme-text-primary">{anchor.title}</span>
          {anchor.status && <span className={`${CHIP} ${historyStatusTone(anchor.status)}`}>{anchor.status}</span>}
        </div>
        {anchor.revision && <div className="truncate font-mono text-sm text-theme-text-secondary">{anchor.revision}</div>}
        {anchor.message && <div className="mt-1 line-clamp-2 text-sm text-theme-text-tertiary">{anchor.message}</div>}
        {anchor.source && <div className="mt-1 truncate text-xs text-theme-text-tertiary">{anchor.source}</div>}
      </div>
      <div className="text-right text-xs text-theme-text-tertiary">
        {formatAppEventTime(anchor.timestamp)}
        {anchor.initiatedBy && <div>{anchor.initiatedBy}</div>}
      </div>
    </div>
  )
}

function HistoryIncidentLine({ incident, action }: { incident: NonNullable<AppHistory['incidents']>[number]; action?: ReactNode }) {
  return (
    <div className="grid grid-cols-[minmax(0,1fr)_auto] gap-3 py-3 first:pt-0 last:pb-0">
      <div className="min-w-0">
        <div className="truncate font-medium text-theme-text-primary">{incident.title}</div>
        {incident.object && <div className="truncate text-sm text-theme-text-secondary">{incident.object}</div>}
        {incident.message && <div className="mt-1 line-clamp-2 text-sm text-theme-text-tertiary">{incident.message}</div>}
      </div>
      <div className="text-right text-xs text-theme-text-tertiary">
        {formatAppEventTime(incident.lastSeen)}
        {incident.count && incident.count > 1 && <div>{incident.count}x</div>}
        {action && <div className="mt-1">{action}</div>}
      </div>
    </div>
  )
}

function buildAppIssues(workloads: AppWorkload[], events: NonNullable<AppRow['events']>): AppIssue[] {
  const issues: AppIssue[] = []
  const issueByWorkload = new Map<string, AppIssue>()

  for (const w of workloads) {
    const health = healthOf(w.health)
    const notReady = w.desired > 0 && w.ready < w.desired
    const hasRestarts = (w.restarts ?? 0) > 0
    const hasReason = !!w.reason
    const hasHealthProblem = health === 'degraded' || health === 'unhealthy'
    if (!hasHealthProblem && !notReady && !hasRestarts && !hasReason) continue

    const severity: AppIssueSeverity = health === 'unhealthy' || (w.desired > 0 && w.ready === 0) ? 'error' : 'warning'
    const parts = [
      notReady ? `${w.ready}/${w.desired} ready` : undefined,
      hasRestarts ? pluralize(w.restarts, 'restart') : undefined,
      w.reason,
    ].filter(Boolean)
    const issue: AppIssue = {
      key: `workload:${workloadKey(w)}`,
      severity,
      title: `${w.name} ${health === 'unhealthy' ? 'is down' : health === 'degraded' || notReady ? 'is degraded' : 'needs attention'}`,
      detail: parts.join(' · '),
      workload: w,
    }
    issueByWorkload.set(workloadKey(w), issue)
    issues.push(issue)
  }

  for (const event of events) {
    const workload = directWorkloadForEvent(event, workloads)
    if (workload) {
      const existing = issueByWorkload.get(workloadKey(workload))
      if (existing) {
        existing.detail = [existing.detail, event.reason].filter(Boolean).join(' · ')
        continue
      }
      issues.push({
        key: `event:${event.object}:${event.reason}`,
        severity: 'warning',
        title: `${event.reason} on ${workload.name}`,
        detail: event.message,
        workload,
      })
      continue
    }
    issues.push({
      key: `event:${event.object}:${event.reason}`,
      severity: 'warning',
      title: `${event.reason} on ${event.object}`,
      detail: event.message,
    })
  }

  const rank: Record<AppIssueSeverity, number> = { error: 0, warning: 1, info: 2 }
  return issues.sort((a, b) => rank[a.severity] - rank[b.severity] || a.title.localeCompare(b.title))
}

function directWorkloadForEvent(event: NonNullable<AppRow['events']>[number], workloads: AppWorkload[]): AppWorkload | undefined {
  const parsed = parseEventObject(event.object)
  if (!parsed) return undefined
  const matches = workloads.filter((w) => w.kind.toLowerCase() === parsed.kind.toLowerCase() && w.name === parsed.name)
  return matches.length === 1 ? matches[0] : undefined
}

function parseEventObject(object: string): { kind: string; name: string } | null {
  const slash = object.indexOf('/')
  if (slash <= 0 || slash === object.length - 1) return null
  return { kind: object.slice(0, slash), name: object.slice(slash + 1) }
}

function issueTone(severity: AppIssueSeverity): string {
  if (severity === 'error') return CHIP_TONE.rose
  if (severity === 'warning') return CHIP_TONE.amber
  return CHIP_TONE.blue
}

function historyStatusTone(status: string): string {
  const s = status.toLowerCase()
  if (s.includes('fail') || s.includes('error')) return CHIP_TONE.rose
  if (s.includes('running') || s.includes('pending') || s.includes('progress')) return CHIP_TONE.amber
  if (s.includes('succeed') || s.includes('deployed') || s.includes('ready')) return CHIP_TONE.emerald
  return CHIP_TONE.muted
}

function sourceLinkLabel(source: AppSourceRef): string {
  if (source.type === 'helm') return 'View Helm release'
  return 'View GitOps source'
}

function formatAppEventTime(value?: string): string {
  if (!value || value.startsWith('0001-01-01T00:00:00')) return ''
  return value
}

function ApplicationPanel({ title, children }: { title: ReactNode; children: ReactNode }) {
  return (
    <section className="rounded-lg border border-theme-border bg-theme-surface p-4 shadow-theme-sm">
      <h2 className="mb-3 text-sm font-semibold text-theme-text-primary">{title}</h2>
      {children}
    </section>
  )
}

function ApplicationFact({
  label,
  value,
  detail,
  monoValue,
  monoDetail,
  variant = 'card',
}: {
  label: string
  value: string
  detail?: string
  monoValue?: boolean
  monoDetail?: boolean
  variant?: 'card' | 'row' | 'bare'
}) {
  if (variant === 'row') {
    return (
      <div className="min-w-0 border-b border-theme-border pb-3 last:border-b-0">
        <div className="text-[10px] font-semibold uppercase tracking-wide text-theme-text-tertiary">{label}</div>
        <div className={clsx('mt-1 truncate text-sm font-semibold text-theme-text-primary', monoValue && 'font-mono')}>{value}</div>
        {detail && <div className={clsx('mt-0.5 truncate text-xs text-theme-text-tertiary', monoDetail && 'font-mono')}>{detail}</div>}
      </div>
    )
  }

  if (variant === 'bare') {
    return (
      <div className="min-w-0">
        <div className="text-[10px] font-semibold uppercase tracking-wide text-theme-text-tertiary">{label}</div>
        <div className={clsx('mt-1 truncate text-sm font-semibold text-theme-text-primary', monoValue && 'font-mono')}>{value}</div>
        {detail && <div className={clsx('mt-0.5 truncate text-xs text-theme-text-tertiary', monoDetail && 'font-mono')}>{detail}</div>}
      </div>
    )
  }

  return (
    <div className="min-w-0 rounded-lg border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm">
      <div className="text-[10px] font-semibold uppercase tracking-wide text-theme-text-tertiary">{label}</div>
      <div className={clsx('mt-1 truncate text-sm font-semibold text-theme-text-primary', monoValue && 'font-mono')}>{value}</div>
      {detail && <div className={clsx('mt-0.5 truncate text-xs text-theme-text-tertiary', monoDetail && 'font-mono')}>{detail}</div>}
    </div>
  )
}

function ApplicationScopeSelector({
  workloads,
  selectedWorkload,
  onSelect,
  onFocus,
}: {
  workloads: AppWorkload[]
  selectedWorkload: AppWorkload | null
  onSelect: (key: string | null) => void
  onFocus: (owner: WorkloadFocus) => void
}) {
  const [open, setOpen] = useState(false)
  const [query, setQuery] = useState('')
  const rootRef = useDismissablePopover<HTMLDivElement>(open, setOpen)
  const selectedKey = selectedWorkload ? workloadKey(selectedWorkload) : null
  const filteredWorkloads = useMemo(() => {
    const q = query.trim().toLowerCase()
    if (!q) return workloads
    return workloads.filter((w) => `${w.kind} ${w.namespace} ${w.name}`.toLowerCase().includes(q))
  }, [query, workloads])

  if (workloads.length <= 1) {
    return selectedWorkload ? (
      <StaticWorkloadScope workload={selectedWorkload} />
    ) : (
      <span className="inline-flex max-w-[min(48vw,34rem)] items-center gap-2 rounded-md bg-theme-base px-2.5 py-1.5 text-sm ring-1 ring-inset ring-theme-border">
        <Boxes className="h-4 w-4 shrink-0 text-theme-text-secondary" aria-hidden />
        <span className="min-w-0 truncate font-medium text-theme-text-primary">Application</span>
      </span>
    )
  }

  return (
    <div ref={rootRef} className="relative min-w-0">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-haspopup="listbox"
        aria-expanded={open}
        className="inline-flex max-w-[min(48vw,34rem)] items-center gap-2 rounded-md bg-theme-base px-2.5 py-1.5 text-sm ring-1 ring-inset ring-theme-border hover:bg-theme-hover"
      >
        {selectedWorkload ? (
          <WorkloadKindIcon workload={selectedWorkload} />
        ) : (
          <Boxes className="h-4 w-4 shrink-0 text-theme-text-secondary" aria-hidden />
        )}
        <span className="min-w-0 truncate font-medium text-theme-text-primary">{selectedWorkload ? selectedWorkload.name : 'Application'}</span>
        {selectedWorkload && <span className="hidden shrink-0 text-xs uppercase tracking-wide text-theme-text-tertiary md:inline">{selectedWorkload.kind}</span>}
        <ChevronDown className={clsx('h-3.5 w-3.5 shrink-0 text-theme-text-tertiary transition-transform', open && 'rotate-180')} aria-hidden />
      </button>
      {open && (
        <div
          role="listbox"
          className="absolute left-0 top-full z-50 mt-1 w-[min(32rem,calc(100vw-2rem))] overflow-hidden rounded-md border border-theme-border bg-theme-surface shadow-theme-md"
          onMouseLeave={() => onFocus(null)}
        >
          <div className="border-b border-theme-border p-1">
            <ScopeOption
              active={selectedKey === null}
              icon={<Boxes className="h-4 w-4 text-theme-text-secondary" aria-hidden />}
              title="Application"
              subtitle="App scope"
              onClick={() => {
                setOpen(false)
                onSelect(null)
              }}
              onMouseEnter={() => onFocus(null)}
            />
          </div>
          <div className="border-b border-theme-border px-2 py-1.5">
            <div className="flex items-center justify-between gap-3">
              <span className="text-[10px] font-semibold uppercase tracking-wide text-theme-text-tertiary">Workloads</span>
              <span className="rounded-full bg-theme-hover px-1.5 text-[10px] font-medium text-theme-text-tertiary">{workloads.length}</span>
            </div>
            {workloads.length > 8 && (
              <label className="mt-2 flex items-center gap-2 rounded-md bg-theme-base px-2 py-1 ring-1 ring-inset ring-theme-border">
                <Search className="h-3.5 w-3.5 shrink-0 text-theme-text-tertiary" aria-hidden />
                <input
                  value={query}
                  onChange={(e) => setQuery(e.target.value)}
                  placeholder="Filter workloads..."
                  className="min-w-0 flex-1 bg-transparent text-sm text-theme-text-primary outline-none placeholder:text-theme-text-tertiary"
                />
              </label>
            )}
          </div>
          <div className="max-h-80 overflow-y-auto p-1">
            {filteredWorkloads.length === 0 ? (
              <div className="px-2 py-4 text-center text-sm text-theme-text-tertiary">No workloads match.</div>
            ) : (
              filteredWorkloads.map((w) => {
                const key = workloadKey(w)
                return (
                  <ScopeOption
                    key={key}
                    active={selectedKey === key}
                    icon={<WorkloadKindIcon workload={w} />}
                    title={w.name}
                    subtitle={`${w.kind} · ${w.ready}/${w.desired} ready${w.reason ? ` · ${w.reason}` : ''}`}
                    onClick={() => {
                      setOpen(false)
                      onSelect(key)
                    }}
                    onMouseEnter={() => onFocus(key)}
                  />
                )
              })
            )}
          </div>
        </div>
      )}
    </div>
  )
}

function TopologyWorkloadLegend({
  workloads,
  colorByWorkload,
  focusedOwnerId,
  showSharedOrUnscoped,
  onFocus,
  onSelectWorkload,
}: {
  workloads: AppWorkload[]
  colorByWorkload: Map<string, number> | null
  focusedOwnerId: WorkloadFocus
  showSharedOrUnscoped: boolean
  onFocus: (owner: WorkloadFocus) => void
  onSelectWorkload: (workload: AppWorkload) => void
}) {
  return (
    <div className="absolute left-4 top-4 z-10 w-[min(22rem,calc(100%-2rem))] overflow-hidden rounded-lg border border-theme-border bg-theme-surface/95 shadow-theme-md backdrop-blur">
      <div className="flex items-center justify-between gap-3 border-b border-theme-border bg-theme-base px-3 py-2">
        <span className="text-xs font-semibold text-theme-text-primary">Workloads</span>
        <span className="rounded-full bg-theme-hover px-1.5 text-[10px] font-medium text-theme-text-tertiary">{workloads.length}</span>
      </div>
      <div className="max-h-72 overflow-y-auto p-1" onMouseLeave={() => onFocus(null)}>
        {workloads.map((w) => {
          const key = workloadKey(w)
          const idx = colorByWorkload?.get(key)
          const hue = idx != null ? workloadHue(idx) : null
          return (
            <ScopeOption
              key={key}
              compact
              active={focusedOwnerId === key}
              accentColor={hue?.swatch}
              icon={<WorkloadKindIcon workload={w} compact />}
              title={w.name}
              subtitle={`${w.kind} · ${w.ready}/${w.desired}`}
              onClick={() => onSelectWorkload(w)}
              onMouseEnter={() => onFocus(key)}
            />
          )
        })}
        {showSharedOrUnscoped && (
          <ScopeOption
            compact
            muted
            active={focusedOwnerId === NEUTRAL_OWNER}
            icon={<SharedScopeMarker />}
            title="Shared / unscoped"
            onMouseEnter={() => onFocus(NEUTRAL_OWNER)}
          />
        )}
      </div>
    </div>
  )
}

function StaticWorkloadScope({ workload }: { workload: AppWorkload }) {
  return (
    <span className="inline-flex max-w-[min(48vw,34rem)] items-center gap-2 rounded-md bg-theme-base px-2.5 py-1.5 text-sm ring-1 ring-inset ring-theme-border">
      <WorkloadKindIcon workload={workload} />
      <span className="min-w-0 truncate font-medium text-theme-text-primary">{workload.name}</span>
      <span className="hidden shrink-0 text-xs uppercase tracking-wide text-theme-text-tertiary md:inline">{workload.kind}</span>
    </span>
  )
}

function WorkloadKindIcon({ workload, compact }: { workload: AppWorkload; compact?: boolean }) {
  const KindIcon = getTopologyIcon(workload.kind)
  return (
    <span className={clsx('relative flex shrink-0 items-center justify-center', compact ? 'h-4 w-4' : 'h-5 w-5')} aria-hidden>
      <KindIcon className={clsx('text-theme-text-secondary', compact ? 'h-3.5 w-3.5' : 'h-4 w-4')} />
      <StatusDot tone={mapHealthToTone(healthOf(workload.health))} size="xs" className="absolute -bottom-0.5 -right-0.5 ring-2 ring-theme-base" />
    </span>
  )
}

function SharedScopeMarker() {
  return (
    <span className="flex w-5 shrink-0 items-center gap-1" aria-hidden>
      <span className="block h-4 w-1 rounded-full bg-theme-border" />
      <span className="h-1.5 w-1.5 rounded-full bg-theme-border" />
    </span>
  )
}

function ScopeOption({
  active,
  muted,
  compact,
  onClick,
  onMouseEnter,
  icon,
  title,
  subtitle,
  accentColor,
}: {
  active?: boolean
  muted?: boolean
  compact?: boolean
  onClick?: () => void
  onMouseEnter?: () => void
  icon: ReactNode
  title: string
  subtitle?: string
  accentColor?: string
}) {
  const className = clsx(
    'relative flex w-full items-center gap-2 rounded-md text-left transition-colors',
    compact ? 'px-2 py-1.5' : 'px-2 py-2',
    active
      ? 'selection selection-ring'
      : onClick && 'hover:bg-theme-hover',
    !onClick && 'cursor-default',
  )
  const inner = (
    <>
      {accentColor && <span className="absolute inset-y-2 left-0 w-1 rounded-full" style={{ background: accentColor }} aria-hidden />}
      {icon}
      <span className="min-w-0 flex-1">
        <span className={clsx('block truncate text-sm', muted ? 'text-theme-text-tertiary' : 'font-medium text-theme-text-primary')}>{title}</span>
        {subtitle && <span className="block truncate text-[10px] uppercase tracking-wide text-theme-text-tertiary">{subtitle}</span>}
      </span>
    </>
  )
  return onClick ? (
    <button type="button" onClick={onClick} onMouseEnter={onMouseEnter} className={className}>{inner}</button>
  ) : (
    <div onMouseEnter={onMouseEnter} className={className}>{inner}</div>
  )
}

function useDismissablePopover<T extends HTMLElement>(open: boolean, setOpen: (open: boolean) => void) {
  const rootRef = useRef<T>(null)
  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) setOpen(false)
    }
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('keydown', onKey)
    }
  }, [open, setOpen])
  return rootRef
}

function renderRuntime(
  workload: SelectedAppWorkload | undefined,
  renderWorkload: (workload: SelectedAppWorkload) => ReactNode,
): ReactNode {
  if (!workload) {
    return (
      <div className="rounded-md border border-dashed border-theme-border p-8 text-center text-sm text-theme-text-tertiary">
        This application has no inspectable workloads.
      </div>
    )
  }
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div key={workloadKey(workload)} className="min-h-0 flex-1">{renderWorkload(workload)}</div>
    </div>
  )
}

function restartWarning(workloads: AppWorkload[]): { restarts: number; reason?: string; workload: string } | null {
  let worst: { restarts: number; reason?: string; workload: string } | null = null
  for (const w of workloads) {
    const r = w.restarts ?? 0
    if (r > 0 && (!worst || r > worst.restarts)) {
      worst = { restarts: r, reason: w.reason, workload: `${w.kind}/${w.name}` }
    }
  }
  return worst
}

// EnvSwitcher — the Environment fact's interactive form when one app runs in
// several environments. Up to MAX_INLINE_ENVS: inline pills (the at-a-glance
// ladder). More: a picker popover with the full ladder-ordered list — same
// affordance at 5 envs or 100. Always ends with the evidence chip
// (AppIdentityTooltip) and, when a ranked lower env outruns a ranked higher one,
// the amber lag chip.
const MAX_INLINE_ENVS = 4

function EnvSwitcher({
  identityKey,
  instances,
  activeKey,
  onSwitch,
}: {
  identityKey: string
  instances: AppIdentityInstance[]
  activeKey: string
  onSwitch?: (appKey: string) => void
}) {
  const [open, setOpen] = useState(false)
  const rootRef = useDismissablePopover<HTMLDivElement>(open, setOpen)

  const envInstances = useMemo(() => collapseIdentityInstances(instances, activeKey), [instances, activeKey])
  const lag = appGroupLagMessage(envInstances)
  const active = envInstances.find((i) => i.appKey === activeKey) ?? instances.find((i) => i.appKey === activeKey)
  const envCounts = useMemo(() => {
    const counts = new Map<string, number>()
    for (const inst of instances) counts.set(inst.env, (counts.get(inst.env) ?? 0) + 1)
    return counts
  }, [instances])
  const evidenceChip = (
    <Tooltip
      content={<AppIdentityTooltip identityKey={identityKey} members={instances.map((i) => ({ name: i.name, env: i.env, confidence: i.confidence, evidence: i.evidence }))} />}
      delay={150}
    >
      <span className="inline-flex cursor-default items-center rounded-sm bg-theme-hover px-1 py-px ring-1 ring-inset ring-theme-border">
        <Layers className="h-3 w-3 text-theme-text-tertiary" aria-hidden />
      </span>
    </Tooltip>
  )
  const lagChip = lag && <span className={`${CHIP} ${CHIP_TONE.amber}`}>{lag}</span>

  if (envInstances.length <= MAX_INLINE_ENVS) {
    return (
      <span className="flex flex-wrap items-center gap-1">
        {instances.map((inst) => {
          const isActive = inst.appKey === activeKey
          const duplicateEnv = (envCounts.get(inst.env) ?? 0) > 1
          return (
            <Tooltip key={inst.appKey} content={`${inst.name}${inst.version ? ` · ${inst.version}` : ''}`} delay={150}>
              <button
                type="button"
                disabled={isActive}
                onClick={() => !isActive && onSwitch?.(inst.appKey)}
                className={clsx(
                  'inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-xs ring-1 ring-inset transition-colors',
                  isActive ? 'selection selection-ring font-medium' : 'bg-theme-surface ring-theme-border hover:bg-theme-hover',
                )}
              >
                <StatusDot tone={mapHealthToTone(inst.health)} />
                {inst.env}
                {duplicateEnv ? <span className="max-w-24 truncate text-theme-text-tertiary">{inst.name}</span> : null}
              </button>
            </Tooltip>
          )
        })}
        {evidenceChip}
        {lagChip}
      </span>
    )
  }

  return (
    <div ref={rootRef} className="relative flex items-center gap-1">
      <button
        type="button"
        onClick={() => setOpen((o) => !o)}
        aria-haspopup="listbox"
        aria-expanded={open}
        className="inline-flex items-center gap-1.5 rounded-md bg-theme-surface px-2 py-0.5 text-xs ring-1 ring-inset ring-theme-border hover:bg-theme-hover"
      >
        {active && <StatusDot tone={mapHealthToTone(active.health)} />}
        <span className="font-medium">{active?.env ?? '—'}</span>
        <span className="text-theme-text-tertiary">· {envInstances.length} environments</span>
        <ChevronDown className={clsx('h-3 w-3 text-theme-text-tertiary transition-transform', open && 'rotate-180')} aria-hidden />
      </button>
      {evidenceChip}
      {lagChip}
      {open && (
        <div role="listbox" className="absolute left-0 top-full z-50 mt-1 max-h-80 w-80 overflow-y-auto rounded-md border border-theme-border bg-theme-surface p-1 shadow-theme-md">
          {instances.map((inst) => {
            const isActive = inst.appKey === activeKey
            return (
              <button
                key={inst.appKey}
                type="button"
                role="option"
                aria-selected={isActive}
                onClick={() => {
                  setOpen(false)
                  if (!isActive) onSwitch?.(inst.appKey)
                }}
                className={clsx(
                  'flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-xs',
                  isActive ? 'selection selection-ring' : 'hover:bg-theme-hover',
                )}
              >
                <StatusDot tone={mapHealthToTone(inst.health)} />
                <span className="w-20 shrink-0 font-medium text-theme-text-primary">{inst.env}</span>
                <span className="min-w-0 flex-1 truncate text-theme-text-secondary">
                  {inst.name}
                </span>
                {inst.version && <span className="font-mono text-[10px] text-theme-text-tertiary">{midTruncate(inst.version, 18)}</span>}
              </button>
            )
          })}
        </div>
      )}
    </div>
  )
}
