import { useMemo, useState, useCallback, useEffect, useRef, type ReactNode } from 'react'
import { AlertTriangle, ArrowLeft, Boxes, ChevronDown, ExternalLink, Layers, Radio, Search } from 'lucide-react'
import { clsx } from 'clsx'
import type { ResourceRef, Topology, TopologyNode } from '../../types'
import argoCdLogo from '../../assets/gitops/argocd.png'
import fluxLogo from '../../assets/gitops/flux.svg'
import { StatusDot, mapHealthToTone } from '../ui/status-tone'
import { Tooltip } from '../ui/Tooltip'
import { EmptyState } from '../ui/EmptyState'
import { ResourceRefBadge } from '../ui/drawer-components'
import { TopologyGraph } from '../topology/TopologyGraph'
import { pluralize } from '../../utils/pluralize'
import { kindToPlural, refToSelectedResource } from '../../utils/navigation'
import { batchRunParentNodes, tagWorkloadOwnership, seedNodeIds, ownershipOf, workloadKey, type NeighborhoodSeed } from '../../utils/topology-neighborhood'
import { workloadHue, NEUTRAL_OWNER, type WorkloadFocus } from '../../utils/workload-colors'
import { getTopologyIcon } from '../../utils/resource-icons'
import {
  type AppRow,
  type AppHistory,
  type AppSourceRef,
  type AppWorkload,
  type AppBatchActivity,
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
  batchSignalForApp,
  batchActivityForApp,
  batchRuntimeForApp,
  applicationDisplayHealth,
  sourceReportedHealth,
  sourceSyncHealth,
  servingReadiness,
  worstHealth,
  appGroupLagMessage,
  compareVersions,
  appSourceLabel,
  overlayProvenance,
} from '../../utils/applications'
import { PaneLoader } from '../ui/PaneLoader'
import { midTruncate } from '../../utils/format'
import { VersionTooltip, AppIdentityTooltip } from './AppTooltips'
import { ProvenanceBadge, ClassBadge, CategoryChip, VersionInfo, BatchSignalChip } from './AppChips'
import { ReadyBar } from './ReadyBar'
import { collapseStableReplicaSets, layerDeploymentInventory, topologyGroup, type DeploymentInventory, type DeploymentTopologyLayer } from '../../utils/application-topology'

// ApplicationDetail owns the application chrome and scope switcher. The selected
// scope decides the one tab row shown in the detail pane: app scope gets
// Overview/Topology/History; workload scope delegates to the host's WorkloadView.

export type SelectedAppWorkload = NeighborhoodSeed;

/** One env instance in the identity switcher — a sibling app row's digest. */
export interface AppIdentityInstance {
  appKey: string;
  name: string;
  env: string;
  health: AppHealth;
  version?: string;
  confidence: string;
  evidence: string;
  count?: number;
}

/** Workload selection is either fully controlled (key + callback, the host
 *  wires it to the URL so back/forward works) or fully internal — providing
 *  only half silently freezes the selector, so the types forbid it. `null` means
 *  the application itself is selected; a key (see `workloadKey`) selects a
 *  workload scope. */
type SelectionProps =
  | {
      selectedWorkloadKey: string | null;
      onSelectWorkload: (key: string | null) => void;
    }
  | { selectedWorkloadKey?: undefined; onSelectWorkload?: undefined };

type CanonicalApplicationView = "overview" | "topology" | "history";

export type ApplicationView = CanonicalApplicationView;

const DEFAULT_APPLICATION_VIEW: CanonicalApplicationView = "overview";

const APPLICATION_VIEWS: Array<{
  id: CanonicalApplicationView;
  label: string;
}> = [
  { id: "overview", label: "Overview" },
  { id: "topology", label: "Topology" },
  { id: "history", label: "History" },
];

function canonicalApplicationView(
  view?: ApplicationView,
): CanonicalApplicationView {
  return view ?? DEFAULT_APPLICATION_VIEW;
}

type ViewProps =
  | {
      selectedView: ApplicationView;
      onSelectView: (view: ApplicationView) => void;
    }
  | { selectedView?: undefined; onSelectView?: undefined };

export type ApplicationDetailProps = {
  app: AppRow;
  onBack: () => void;
  /** Render the host's WorkloadView for the chosen workload. */
  renderWorkload: (workload: SelectedAppWorkload) => ReactNode;
  /** Render host-scoped operational issues for the app overview. */
  renderOverviewIssues?: () => ReactNode;
  /** Whether the host-scoped issue surface contains current issues. */
  hasOverviewIssues?: boolean;
  /** Resources-view topology spanning the app's namespaces. When present, it
   *  powers the application Topology view and workload hover focus. */
  topology?: Topology;
  /** True while the host's topology fetch is in flight. Without it, a
   *  multi-workload app can briefly show an empty topology while topology loads. */
  topologyLoading?: boolean
  /** Exact resources reported by the app's Argo CD, Flux, or Helm deployment source. */
  deploymentInventory?: DeploymentInventory
  /** Open a related (non-workload) resource clicked in the app graph. */
  onNavigateToResource?: (resource: {
    kind: string;
    namespace: string;
    name: string;
    group?: string;
  }) => void;
  /** Open a generated Job or Workflow in the run history of its parent workload. */
  onSelectWorkloadRun?: (
    workload: SelectedAppWorkload,
    run: TopologyNode,
  ) => void;
  /** App identity siblings (this instance included, ladder-ordered) — turns the
   *  context strip's Environment fact into a switcher. Identity grouping is
   *  classification, not an address: it switches between REAL instances, never
   *  an aggregate page. */
  identityInstances?: AppIdentityInstance[] | null;
  /** Switch to a sibling instance (host swaps ?app= and, when it can match
   *  the current workload in the target, preserves ?workload= + ?tab=). */
  onSwitchInstance?: (appKey: string) => void;
  /** Env tokens the cluster proved (the list derives the same set) — keeps a
   *  ungrouped app's Environment fact consistent between list and detail. */
  discoveredEnvs?: ReadonlySet<string>;
  /** Which instance in `identityInstances` is active, by its `appKey`. Defaults
   *  to `app.key` — correct for the OSS case, where each instance IS a distinct
   *  app row keyed the same way. A host that keys instances on something other
   *  than the app key (e.g. the Cloud fleet keys per cluster, while `app.key`
   *  must stay the logical app key for provenance) passes the active instance's
   *  key here so the switcher marks it. */
  activeInstanceKey?: string;
  history?: AppHistory;
  historyLoading?: boolean;
  onOpenSource?: (source: AppSourceRef) => void;
} & SelectionProps &
  ViewProps;

function ContextFact({
  label,
  children,
}: {
  label: string;
  children: ReactNode;
}) {
  return (
    <div className="flex min-w-0 items-baseline gap-1.5">
      <span className="text-[10px] uppercase tracking-wide text-theme-text-tertiary">
        {label}
      </span>
      <span className="min-w-0 truncate text-xs text-theme-text-secondary">
        {children}
      </span>
    </div>
  );
}

function collapseIdentityInstances(
  instances: AppIdentityInstance[],
  activeKey: string,
): AppIdentityInstance[] {
  const byEnv = new Map<string, AppIdentityInstance[]>();
  for (const inst of instances) {
    byEnv.set(inst.env, [...(byEnv.get(inst.env) ?? []), inst]);
  }
  return Array.from(byEnv.values()).map((group) => {
    const active = group.find((i) => i.appKey === activeKey);
    const newest = group.reduce<AppIdentityInstance | undefined>(
      (best, inst) => {
        if (!best) return inst;
        return compareDefinedVersions(inst.version, best.version) === 1
          ? inst
          : best;
      },
      undefined,
    );
    const display = active ?? newest ?? group[0];
    return {
      ...display,
      health: worstHealth(group.map((inst) => inst.health)),
      version: newest?.version,
      count: group.length,
    };
  });
}

function compareDefinedVersions(
  a: string | undefined,
  b: string | undefined,
): number {
  if (!a && !b) return 0;
  if (a && !b) return 1;
  if (!a || !b) return -1;
  return compareVersions(a, b) ?? 0;
}

export function ApplicationDetail({ app, onBack, renderWorkload, renderOverviewIssues, hasOverviewIssues, topology, topologyLoading, deploymentInventory, onNavigateToResource, onSelectWorkloadRun, identityInstances, onSwitchInstance, discoveredEnvs, activeInstanceKey, history, historyLoading, onOpenSource, selectedWorkloadKey, onSelectWorkload, selectedView, onSelectView }: ApplicationDetailProps) {
  // Stable order regardless of API ordering: rail rows and the per-workload
  // color assignment both follow this array, so an order flap between
  // refetches must not reshuffle rows or reassign a workload's hue.
  const workloads = useMemo(
    () =>
      [...(app.workloads ?? [])].sort(
        (a, b) =>
          a.name.localeCompare(b.name) ||
          a.kind.localeCompare(b.kind) ||
          a.namespace.localeCompare(b.namespace),
      ),
    [app.workloads],
  );
  const workloadClass = workloadClassOf(app.workload_class);
  const batchRuntime = batchRuntimeForApp(app);
  const overall = applicationDisplayHealth(app);
  const verdictTone = HEALTH_META[overall].pill;
  const verdictLabel =
    workloadClass === "job" && overall === batchRuntime.health
      ? batchRuntime.label
      : HEALTH_META[overall].label;
  const versions = useMemo(
    () => Array.from(new Set((app.versions || []).filter(Boolean))),
    [app.versions],
  );
  const { ready, desired } = servingReadiness(workloads);
  const restartSignal = restartWarning(workloads);
  const batchSignal = workloadClass === "job" ? null : batchSignalForApp(app);
  // Resolve namespace the same way the list does (the workloads' shared
  // namespace) so env/namespace match across list and detail. Multi-namespace
  // apps get the count, never an arbitrary pick.
  const namespace = namespaceOf(app);
  const namespaces = namespacesOf(app);
  const resolvedEnv = resolveEnv(undefined, namespace, discoveredEnvs);
  const env = app.identity?.env ?? resolvedEnv.env;
  const inferred = app.identity
    ? identityEnvInferred(app.identity)
    : resolvedEnv.inferred;
  const [internalView, setInternalView] = useState<CanonicalApplicationView>(
    DEFAULT_APPLICATION_VIEW,
  );
  const activeView = canonicalApplicationView(selectedView ?? internalView);
  const setView = useCallback(
    (view: CanonicalApplicationView) =>
      onSelectView ? onSelectView(view) : setInternalView(view),
    [onSelectView],
  );
  useEffect(() => {
    if (selectedView === undefined) setInternalView(DEFAULT_APPLICATION_VIEW);
  }, [app.key, selectedView]);

  const [internalSelected, setInternalSelected] = useState<string | null>(null);
  const implicitSingleWorkloadKey =
    workloads.length === 1 ? workloadKey(workloads[0]) : null;
  const rawSelected =
    selectedWorkloadKey !== undefined
      ? selectedWorkloadKey
      : (internalSelected ?? implicitSingleWorkloadKey);
  const setSelected = useCallback(
    (key: string | null) =>
      onSelectWorkload ? onSelectWorkload(key) : setInternalSelected(key),
    [onSelectWorkload],
  );
  const selectedWorkload = rawSelected
    ? workloads.find((w) => workloadKey(w) === rawSelected)
    : undefined;
  const singleWorkloadScope = workloads.length === 1 && !!selectedWorkload;
  useEffect(() => {
    if (
      selectedWorkloadKey !== undefined &&
      selectedWorkloadKey !== null &&
      !selectedWorkload
    ) {
      onSelectWorkload?.(null);
    }
  }, [selectedWorkloadKey, selectedWorkload, onSelectWorkload]);
  useEffect(() => {
    if (selectedWorkloadKey === undefined) setInternalSelected(null);
  }, [app.key, selectedWorkloadKey]);

  // Hover-focus: the workload (or NEUTRAL_OWNER) whose nodes should stay lit
  // while the rest of the graph dims. Driven by the rail and, reciprocally, by
  // hovering a node.
  const [focusedOwnerId, setFocusedOwnerId] = useState<WorkloadFocus>(null)
  const [expandedReplicaSetOwners, setExpandedReplicaSetOwners] = useState<Set<string>>(new Set())
  useEffect(() => setExpandedReplicaSetOwners(new Set()), [app.key])
  const toggleReplicaSets = useCallback((ownerID: string) => {
    setExpandedReplicaSetOwners((current) => {
      const next = new Set(current)
      if (next.has(ownerID)) next.delete(ownerID)
      else next.add(ownerID)
      return next
    })
  }, [])

  const appSeeds = useMemo(
    () =>
      workloads.map((w) => ({
        kind: w.kind,
        namespace: w.namespace,
        name: w.name,
        group: w.group,
      })),
    [workloads],
  );
  // Neighborhood subgraph + per-workload color/ownership tagging in one pass.
  const composedTopology = useMemo(
    () => topology ? collapseStableReplicaSets(topology, expandedReplicaSetOwners) : null,
    [topology, expandedReplicaSetOwners],
  )
  const ownership = useMemo(
    () => (composedTopology ? tagWorkloadOwnership(composedTopology, appSeeds) : null),
    [composedTopology, appSeeds],
  )
  const appGraph = ownership?.topology ?? null
  const deploymentLayer = useMemo(
    () => appGraph && deploymentInventory && app.sourceRef
      ? layerDeploymentInventory(appGraph, deploymentInventory, sourceInventoryLabel(app.sourceRef))
      : null,
    [appGraph, deploymentInventory, app.sourceRef],
  )
  const appGraphFocusId = useMemo(
    () => (topology ? seedNodeIds(topology, appSeeds)[0] : undefined),
    [topology, appSeeds],
  );

  // Hovering a node lights up its owning workload (and the rail row). An
  // unowned node related to exactly ONE workload (a GitOps manager over a
  // single workload here) still focuses that workload, mirroring rail-driven
  // focus. Truly shared nodes clear instead of dimming everything.
  const handleNodeHover = useCallback((node: TopologyNode | null) => {
    if (!node) {
      setFocusedOwnerId(null);
      return;
    }
    const stamp = ownershipOf(node.data);
    setFocusedOwnerId(
      stamp.ownerWorkloadId ??
        (stamp.focusWorkloadIds.length === 1
          ? stamp.focusWorkloadIds[0]
          : NEUTRAL_OWNER),
    );
  }, []);

  // A node click in the app graph either drills into one of the app's workloads
  // (its runtime) or opens a related resource (Service/config/…) via the host.
  const handleAppNodeClick = useCallback(
    (node: TopologyNode) => {
      if (node.data?.sourceInventoryGroup && app.sourceRef) {
        onOpenSource?.(app.sourceRef)
        return
      }
      const ns = (node.data?.namespace as string) || ''
      const match = workloads.find((w) => w.kind === node.kind && w.name === node.name && w.namespace === ns)
      if (match) {
        setSelected(workloadKey(match));
        return;
      }
      const parents = appGraph ? batchRunParentNodes(appGraph, node) : [];
      const parentWorkload = parents.flatMap((parent) => {
        const parentNamespace = (parent.data?.namespace as string) || "";
        const workload = workloads.find(
          (candidate) =>
            candidate.kind === parent.kind &&
            candidate.name === parent.name &&
            candidate.namespace === parentNamespace,
        );
        return workload ? [workload] : [];
      })[0];
      if (parentWorkload && onSelectWorkloadRun) {
        onSelectWorkloadRun(parentWorkload, node);
        return;
      }
      onNavigateToResource?.({
        kind: kindToPlural(node.kind),
        namespace: ns,
        name: node.name,
        group: topologyGroup(node),
      })
    },
    [app.sourceRef, appGraph, workloads, onNavigateToResource, onOpenSource, onSelectWorkloadRun, setSelected],
  )

  const appTopology = deploymentLayer?.topology ?? appGraph ?? null
  const appTopologyAvailable = !!appTopology && appTopology.nodes.length > 0
  const colorByWorkload = ownership?.colorByWorkload ?? null

  return (
    <div className="flex min-h-0 w-full flex-1 flex-col bg-theme-base">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-theme-border px-4 py-3 sm:px-6">
        <button
          type="button"
          onClick={onBack}
          className="flex shrink-0 items-center gap-1.5 text-xs text-theme-text-tertiary hover:text-theme-text-primary"
        >
          <ArrowLeft className="h-3.5 w-3.5" aria-hidden /> Applications
        </button>
        <span
          className="hidden h-6 w-px bg-theme-border sm:block"
          aria-hidden
        />
        <span
          className={`flex h-8 w-8 shrink-0 items-center justify-center rounded-md ring-1 ring-inset ${verdictTone}`}
        >
          <Boxes className="h-4 w-4" aria-hidden />
        </span>
        <div className="flex min-w-0 flex-1 items-center gap-2">
          <h1 className="min-w-0 truncate text-xl font-semibold text-theme-text-primary lg:text-2xl">
            {app.name}
          </h1>
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
          <span
            className={`inline-flex items-center gap-2 rounded-md px-2.5 py-1 ring-1 ring-inset ${verdictTone}`}
          >
            <StatusDot tone={mapHealthToTone(overall)} />
            <span className="text-sm font-semibold">{verdictLabel}</span>
          </span>
          {restartSignal && (
            <Tooltip
              content={`${restartSignal.workload} · ${pluralize(restartSignal.restarts, "restart")}`}
              delay={150}
            >
              <span
                className={`inline-flex items-center rounded-md px-2 py-1 text-xs font-semibold ring-1 ring-inset ${CHIP_TONE.amber}`}
              >
                Pod warning: {restartSignal.reason || "Restarts"} ·{" "}
                {pluralize(restartSignal.restarts, "restart")}
              </span>
            </Tooltip>
          )}
          {/* Amber only on real skew (same image, different tags) — the context
              strip already covers the multi-image "N versions" case neutrally. */}
          {app.versionSkew && versions.length > 1 && (
            <Tooltip
              content={<VersionTooltip workloads={workloads} />}
              delay={150}
            >
              <span
                className={`inline-flex items-center rounded-md px-2 py-1 font-mono text-xs ring-1 ring-inset ${CHIP_TONE.amber}`}
              >
                {versions.length} versions
              </span>
            </Tooltip>
          )}
        </div>
      </div>

      {/* Context strip */}
      <div className="flex flex-wrap items-center gap-x-5 gap-y-2 border-b border-theme-border px-4 py-2 sm:px-6">
        <ProvenanceBadge
          tier={app.tier}
          appKey={app.key}
          confidence={app.confidence}
        />
        <CategoryChip category={app.category} addonReason={app.addonReason} />
        <ClassBadge
          workloadClass={workloadClass}
          composition={classCompositionOf(app)}
        />
        <BatchSignalChip signal={batchSignal} />
        {singleWorkloadScope && selectedWorkload.name !== app.name && (
          <ContextFact label="Workload">
            <span className="font-mono">
              {selectedWorkload.kind}/{selectedWorkload.name}
            </span>
          </ContextFact>
        )}
        {identityInstances && identityInstances.length > 1 ? (
          // The Environment fact IS the switcher when this app runs in several
          // envs — prominent, in existing header space, no extra row. Inline
          // pills for a handful; a picker beyond that (scales to ~any count).
          <div className="flex min-w-0 items-center gap-1.5">
            <span className="text-[10px] uppercase tracking-wide text-theme-text-tertiary">
              Environment
            </span>
            <EnvSwitcher
              identityKey={app.identity?.key ?? ""}
              instances={identityInstances}
              activeKey={activeInstanceKey ?? app.key}
              onSwitch={onSwitchInstance}
            />
          </div>
        ) : env ? (
          <ContextFact label="Environment">
            {inferred ? (
              <Tooltip
                content={`Inferred from namespace "${namespace || env}" — confirm with an environment label.`}
                delay={150}
              >
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
            <Tooltip content={namespaces.join(", ")} delay={150}>
              <span>{namespaces.length} namespaces</span>
            </Tooltip>
          </ContextFact>
        ) : null}
        {workloadClass === "job" ? (
          <ContextFact label="Runtime">{batchRuntime.label}</ContextFact>
        ) : desired > 0 ? (
          <ContextFact label="Ready">
            <ReadyBar ready={ready} desired={desired} width="w-16" />
          </ContextFact>
        ) : null}
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
            ready={ready}
            desired={desired}
            versions={versions}
            topology={appTopology}
            topologyLoading={topologyLoading}
            deploymentLayer={deploymentLayer}
            deploymentSource={app.sourceRef}
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
            onToggleReplicaSets={toggleReplicaSets}
            renderOverviewIssues={renderOverviewIssues}
            hasOverviewIssues={hasOverviewIssues}
          />
        )}
      </div>
    </div>
  );
}

function ApplicationWorkspace({
  app,
  activeView,
  onViewChange,
  workloads,
  ready,
  desired,
  versions,
  topology,
  topologyLoading,
  deploymentLayer,
  deploymentSource,
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
  onToggleReplicaSets,
  renderOverviewIssues,
  hasOverviewIssues,
}: {
  app: AppRow
  activeView: CanonicalApplicationView
  onViewChange: (view: CanonicalApplicationView) => void
  workloads: AppWorkload[]
  ready: number
  desired: number
  versions: string[]
  topology: Topology | null
  topologyLoading?: boolean
  deploymentLayer: DeploymentTopologyLayer | null
  deploymentSource?: AppSourceRef
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
  onToggleReplicaSets: (ownerID: string) => void
  renderOverviewIssues?: () => ReactNode
  hasOverviewIssues?: boolean
}) {
  const historyCount =
    (history?.anchors?.length ?? 0) +
    (history?.incidents?.length ?? app.events?.length ?? 0);
  return (
    <div className="flex h-full min-h-0 flex-col">
      <ApplicationViewTabs
        activeView={activeView}
        historyCount={historyCount}
        onChange={onViewChange}
      />
      {activeView === "overview" && (
        <ApplicationOverview
          app={app}
          workloads={workloads}
          ready={ready}
          desired={desired}
          versions={versions}
          onSelectWorkload={onSelectWorkload}
          onNavigateToResource={onNavigateToResource}
          history={history}
          onSelectHistory={() => onViewChange("history")}
          onOpenSource={onOpenSource}
          renderOverviewIssues={renderOverviewIssues}
          hasOverviewIssues={hasOverviewIssues}
        />
      )}
      {activeView === "topology" && (
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
          deploymentLayer={deploymentLayer}
          deploymentSource={deploymentSource}
          onOpenSource={onOpenSource}
          onToggleReplicaSets={onToggleReplicaSets}
        />
      )}
      {activeView === "history" && (
        <ApplicationHistoryView
          history={history}
          loading={historyLoading}
          fallbackEvents={app.events ?? []}
          workloads={workloads}
          onSelectWorkload={onSelectWorkload}
          onOpenSource={onOpenSource}
        />
      )}
    </div>
  );
}

function ApplicationViewTabs({
  activeView,
  historyCount,
  onChange,
}: {
  activeView: CanonicalApplicationView;
  historyCount: number;
  onChange: (view: CanonicalApplicationView) => void;
}) {
  return (
    <div
      className="flex shrink-0 items-center border-b border-theme-border px-4 sm:px-6"
      role="tablist"
      aria-label="Application views"
    >
      <div className="flex min-w-0 gap-1 overflow-x-auto">
        {APPLICATION_VIEWS.map((view) => {
          const active = view.id === activeView;
          const badge =
            view.id === "history" && historyCount > 0 ? historyCount : null;
          return (
            <button
              key={view.id}
              type="button"
              role="tab"
              aria-selected={active}
              onClick={() => onChange(view.id)}
              className={clsx(
                "flex items-center gap-1.5 whitespace-nowrap border-b-2 px-3 py-2 text-sm font-medium transition-colors",
                active
                  ? "border-skyhook-500 text-theme-text-primary"
                  : "border-transparent text-theme-text-secondary hover:border-theme-border-light hover:text-theme-text-primary",
              )}
            >
              {view.label}
              {badge !== null && (
                <span className="rounded bg-theme-hover px-1.5 py-0.5 text-[10px] font-semibold text-theme-text-tertiary">
                  {badge}
                </span>
              )}
            </button>
          );
        })}
      </div>
    </div>
  );
}

type AppIssueSeverity = "error" | "warning" | "info";

type AppIssue = {
  key: string;
  severity: AppIssueSeverity;
  title: string;
  detail?: string;
  workload?: AppWorkload;
};

function ApplicationOverview({
  app,
  workloads,
  ready,
  desired,
  versions,
  onSelectWorkload,
  onNavigateToResource,
  history,
  onSelectHistory,
  onOpenSource,
  renderOverviewIssues,
  hasOverviewIssues,
}: {
  app: AppRow
  workloads: AppWorkload[]
  ready: number
  desired: number
  versions: string[]
  onSelectWorkload: (workload: AppWorkload) => void
  onNavigateToResource?: (resource: { kind: string; namespace: string; name: string; group?: string }) => void
  history?: AppHistory
  onSelectHistory: () => void
  onOpenSource?: (source: AppSourceRef) => void
  renderOverviewIssues?: () => ReactNode
  hasOverviewIssues?: boolean
}) {
  const rel = app.relationships
  const hasEntrypoints = Boolean(rel && (relationshipRefs(rel, 'service').length > 0 || relationshipRefs(rel, 'ingress').length > 0 || relationshipRefs(rel, 'route').length > 0))
  const hasDependencies = Boolean(rel && (
    relationshipRefs(rel, 'config').length > 0 ||
    relationshipRefs(rel, 'scaler').length > 0 ||
    relationshipRefs(rel, 'storage').length > 0 ||
    relationshipRefs(rel, 'pdb').length > 0 ||
    relationshipRefs(rel, 'networkPolicy').length > 0
  ))
  const composition = classCompositionOf(app)
    .map(({ cls, count }) => `${count} ${cls}`)
    .join(' / ')
  const issues = useMemo(() => buildAppIssues(workloads, app.events ?? []), [workloads, app.events])
  const currentIssuesPresent = renderOverviewIssues
    ? hasOverviewIssues ?? issues.length > 0
    : issues.length > 0
  const batchActivity = useMemo(() => batchActivityForApp(app), [app])
  const batchStats = batchOverviewStats(batchActivity)
  const pureBatch = workloadClassOf(app.workload_class) === 'job'
  const workloadComposition = workloadKindComposition(workloads)
  const latestChange = history?.summary?.state === 'change' ? history.summary : undefined
  const runtimeHealth = healthOf(app.runtimeHealth ?? worstHealth(workloads.map((workload) => workload.health)))
  const hasDeliveryStatus = Boolean(app.sourceStatus?.sync || app.sourceStatus?.health)
  const extraFactCount = Number(Boolean(latestChange)) + Number(hasDeliveryStatus)
  const factGrid = pureBatch
    ? extraFactCount === 2
      ? '2xl:grid-cols-6'
      : extraFactCount === 1
        ? '2xl:grid-cols-5'
        : '2xl:grid-cols-4'
    : extraFactCount === 2
      ? '2xl:grid-cols-5'
      : extraFactCount === 1
        ? '2xl:grid-cols-4'
        : '2xl:grid-cols-3'

  return (
    <div className="min-h-0 flex-1 overflow-auto">
      <div className="grid w-full max-w-[2400px] gap-4 p-4 sm:p-6 xl:grid-cols-[minmax(0,1fr)_minmax(320px,380px)]">
        <div className="min-w-0 space-y-4">
          {renderOverviewIssues ? renderOverviewIssues() : <ApplicationNow issues={issues} onSelectWorkload={onSelectWorkload} />}
          <ApplicationLatestHistory
            history={history}
            sourceRef={app.sourceRef}
            showIncidentPreview={!currentIssuesPresent}
            onSelectHistory={onSelectHistory}
            onOpenSource={onOpenSource}
          />
          <div className={clsx('grid gap-3 md:grid-cols-2', factGrid)}>
            <ApplicationFact
              label="Runtime"
              value={pureBatch ? batchRuntimeForApp(app).label : HEALTH_META[runtimeHealth].label}
              detail={pureBatch ? batchStats.activeDetail : desired > 0 ? `${ready}/${desired} ready` : 'No desired replicas'}
            />
            {hasDeliveryStatus && (
              <ApplicationFact
                label="Delivery"
                value={<SourceDeliveryStatus status={app.sourceStatus!} />}
                detail={app.sourceRef ? sourceObjectLabel(app.sourceRef) : undefined}
              />
            )}
            {pureBatch ? (
              <>
                <ApplicationFact label="Batch resources" value={String(workloads.length)} detail={workloadComposition || 'No workloads'} />
                <ApplicationFact label="Active runs" value={batchStats.activeValue} detail={batchStats.activeDetail} />
                <ApplicationFact label="Retained runs" value={batchStats.retainedValue} detail="Kubernetes-retained history" />
              </>
            ) : (
              <>
                <ApplicationFact label="Workloads" value={String(workloads.length)} detail={composition || 'No workloads'} />
                <ApplicationFact label="Version" value={app.appVersion || (versions.length === 1 ? versions[0] : versions.length > 1 ? `${versions.length} versions` : 'Unknown')} />
              </>
            )}
            {latestChange && (
              <ApplicationLatestChangeFact
                summary={latestChange}
                sourceRef={app.sourceRef ? undefined : history?.sourceRef}
                onSelectHistory={onSelectHistory}
                onOpenSource={onOpenSource}
              />
            )}
          </div>
          {hasEntrypoints && (
            <ApplicationEntrypoints
              relationships={rel}
              onNavigateToResource={onNavigateToResource}
            />
          )}
          <ApplicationPanel title="Workloads">
            <WorkloadsMatrix
              workloads={workloads}
              onSelectWorkload={onSelectWorkload}
            />
          </ApplicationPanel>
          {!pureBatch && <ApplicationBatchOverview activity={batchActivity} />}
        </div>
        <aside className={clsx('min-w-0 gap-4 xl:block xl:space-y-4', hasDependencies ? 'grid md:grid-cols-2' : 'max-w-xl xl:max-w-none')}>
          <ApplicationSourceProvenance app={app} onOpenSource={onOpenSource} />
          {hasDependencies && <ApplicationDependencies relationships={rel} onNavigateToResource={onNavigateToResource} />}
        </aside>
      </div>
    </div>
  );
}

function workloadKindComposition(workloads: AppWorkload[]): string {
  const counts = new Map<string, number>();
  for (const workload of workloads) {
    if (!workload.kind) continue;
    counts.set(workload.kind, (counts.get(workload.kind) ?? 0) + 1);
  }
  return [...counts.entries()]
    .sort(
      ([aKind, aCount], [bKind, bCount]) =>
        bCount - aCount || aKind.localeCompare(bKind),
    )
    .map(([kind, count]) => pluralize(count, kind))
    .join(" / ");
}

function ApplicationSourceProvenance({
  app,
  onOpenSource,
}: {
  app: AppRow
  onOpenSource?: (source: AppSourceRef) => void
}) {
  if (app.sourceRef) {
    const sourceName = `${app.sourceRef.namespace}/${app.sourceRef.name}`
    return (
      <ApplicationPanel title="Deployment source">
        <div className="space-y-3">
          <div>
            <div className="text-xs font-semibold uppercase text-theme-text-tertiary">{sourceObjectLabel(app.sourceRef)}</div>
            <Tooltip content={sourceName} delay={150}>
              <div className="mt-1 truncate font-mono text-sm font-medium text-theme-text-primary">{sourceName}</div>
            </Tooltip>
          </div>
          {app.sourceStatus && <SourceDeliveryStatus status={app.sourceStatus} />}
          {onOpenSource && (
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

  if (app.sourceConflict) {
    return (
      <ApplicationPanel title="Deployment source">
        <div className="space-y-2">
          <div className="flex items-center gap-2">
            <AlertTriangle className="h-4 w-4 shrink-0 text-amber-500" aria-hidden />
            <span className="text-sm font-semibold text-theme-text-primary">Multiple deployment sources detected</span>
          </div>
          <p className="text-sm text-theme-text-secondary">Workloads in this application do not share one deployment manager.</p>
        </div>
      </ApplicationPanel>
    )
  }

  const inferred = app.identity?.confidence === 'medium'
  const identityLabel = app.identity?.source ? appSourceLabel(app.identity.source) : undefined
  const tierLabel = !app.identity && app.tier ? overlayProvenance(app.tier) : undefined
  return (
    <ApplicationPanel title="Application identity">
      <div className="space-y-2">
        <div className="flex items-center gap-2">
          {inferred && <AlertTriangle className="h-4 w-4 shrink-0 text-amber-500" aria-hidden />}
          <span className="text-sm font-semibold text-theme-text-primary">
            {identityLabel
              ? inferred
                ? 'Inferred application boundary'
                : `Identified by ${identityLabel}`
              : tierLabel
                ? `Grouped by ${tierLabel} metadata`
                : 'Grouped from Kubernetes ownership'}
          </span>
        </div>
        {inferred && identityLabel && <p className="text-sm text-theme-text-secondary">Matched using {identityLabel}.</p>}
        {app.identity?.evidence && (
          <Tooltip content={app.identity.evidence} delay={150}>
            <div className="truncate font-mono text-xs text-theme-text-tertiary">{app.identity.evidence}</div>
          </Tooltip>
        )}
      </div>
    </ApplicationPanel>
  );
}

function ApplicationEntrypoints({
  relationships,
  onNavigateToResource,
}: {
  relationships: AppRow['relationships']
  onNavigateToResource?: (resource: { kind: string; namespace: string; name: string; group?: string }) => void
}) {
  const serviceRefs = relationshipRefs(relationships, 'service')
  const ingressRefs = relationshipRefs(relationships, 'ingress')
  const routeRefs = relationshipRefs(relationships, 'route')
  const serviceCount = serviceRefs.length
  const ingressCount = ingressRefs.length
  const routeCount = routeRefs.length
  const hasExternal = ingressCount + routeCount > 0
  if (serviceCount + ingressCount + routeCount === 0) return null

  return (
    <ApplicationPanel title="Entrypoints">
      <div className="space-y-3">
        <div>
          <div className="flex flex-wrap items-center gap-2">
            <h3 className="text-sm font-semibold text-theme-text-primary">
              {hasExternal
                ? "External entrypoints configured"
                : "Internal services configured"}
            </h3>
            <span
              className={`${CHIP} ${hasExternal ? CHIP_TONE.blue : CHIP_TONE.muted}`}
            >
              {hasExternal ? "External" : "Internal"}
            </span>
          </div>
          <p className="mt-1 text-sm text-theme-text-secondary">
            {hasExternal
              ? "Traffic can enter this application through ingress or route resources."
              : "Services target this application, but no ingress or route was detected."}
          </p>
        </div>
        <div className="grid gap-3 sm:grid-cols-3">
          <ApplicationFact
            variant="bare"
            label="Services"
            value={String(serviceCount)}
          />
          <ApplicationFact
            variant="bare"
            label="Ingresses"
            value={String(ingressCount)}
          />
          <ApplicationFact
            variant="bare"
            label="Routes"
            value={String(routeCount)}
          />
        </div>
        <div className="space-y-3 border-t border-theme-border pt-3">
          <ApplicationRelatedNameGroup
            label="Services"
            refs={serviceRefs}
            onNavigateToResource={onNavigateToResource}
          />
          <ApplicationRelatedNameGroup
            label="Ingresses"
            refs={ingressRefs}
            onNavigateToResource={onNavigateToResource}
          />
          <ApplicationRelatedNameGroup label="Routes" refs={routeRefs} onNavigateToResource={onNavigateToResource} />
        </div>
      </div>
    </ApplicationPanel>
  );
}

function ApplicationDependencies({
  relationships,
  onNavigateToResource,
}: {
  relationships: AppRow['relationships']
  onNavigateToResource?: (resource: { kind: string; namespace: string; name: string; group?: string }) => void
}) {
  const configs = relationshipRefs(relationships, 'config')
  const scalers = relationshipRefs(relationships, 'scaler')
  const storage = relationshipRefs(relationships, 'storage')
  const pdbs = relationshipRefs(relationships, 'pdb')
  const networkPolicies = relationshipRefs(relationships, 'networkPolicy')
  if (configs.length + scalers.length + storage.length + pdbs.length + networkPolicies.length === 0) return null

  return (
    <ApplicationPanel title="Dependencies">
      <div className="space-y-3">
        <ApplicationDependencyRow label="Configuration" refs={configs} totalCount={relationships?.configs} detail="ConfigMaps and Secrets referenced by app workloads." onNavigateToResource={onNavigateToResource} />
        <ApplicationDependencyRow label="Autoscaling" refs={scalers} totalCount={relationships?.scalers} detail="Autoscalers controlling app workloads." onNavigateToResource={onNavigateToResource} />
        <ApplicationDependencyRow label="Storage" refs={storage} totalCount={relationships?.storage} detail="PersistentVolumeClaims mounted by app workloads." onNavigateToResource={onNavigateToResource} />
        <ApplicationDependencyRow label="Availability policy" refs={pdbs} totalCount={relationships?.pdbs} detail="PodDisruptionBudgets protecting app workloads." onNavigateToResource={onNavigateToResource} />
        <ApplicationDependencyRow label="Network policy" refs={networkPolicies} totalCount={relationships?.networkPolicies} detail="Network policies selecting app workloads." onNavigateToResource={onNavigateToResource} />
      </div>
    </ApplicationPanel>
  );
}

function ApplicationDependencyRow({
  label,
  refs,
  totalCount,
  detail,
  onNavigateToResource,
}: {
  label: string
  refs: ResourceRef[]
  totalCount?: number
  detail: string
  onNavigateToResource?: (resource: { kind: string; namespace: string; name: string; group?: string }) => void
}) {
  if (refs.length === 0) return null
  const count = Math.max(totalCount ?? refs.length, refs.length)
  return (
    <div className="min-w-0 border-b border-theme-border pb-3 last:border-b-0">
      <div className="flex items-baseline justify-between gap-3">
        <div className="text-sm font-semibold text-theme-text-primary">
          {label}
        </div>
        <span className={`${CHIP} ${CHIP_TONE.muted}`}>
          {pluralize(count, "resource")}
        </span>
      </div>
      <div className="mt-1 text-xs text-theme-text-tertiary">{detail}</div>
      <ApplicationRelatedNameGroup label={label} refs={refs} totalCount={count} onNavigateToResource={onNavigateToResource} hideLabel />
    </div>
  );
}

function ApplicationRelatedNameGroup({
  label,
  refs,
  totalCount,
  onNavigateToResource,
  hideLabel,
}: {
  label: string
  refs?: ResourceRef[]
  totalCount?: number
  onNavigateToResource?: (resource: { kind: string; namespace: string; name: string; group?: string }) => void
  hideLabel?: boolean
}) {
  if (!refs || refs.length === 0) return null

  const visible = refs.slice(0, 12)
  const count = Math.max(totalCount ?? refs.length, refs.length)
  const overflow = Math.max(0, count - visible.length)
  return (
    <div className={hideLabel ? 'mt-2' : undefined}>
      {!hideLabel && <div className="mb-1 text-xs font-medium text-theme-text-tertiary">{label}{count > 1 ? ` (${count})` : ''}</div>}
      <div className="flex flex-wrap gap-1.5">
        {visible.map((ref) => (
          <ResourceRefBadge
            key={`${ref.group || ''}/${ref.kind}/${ref.namespace}/${ref.name}`}
            resourceRef={ref}
            onClick={onNavigateToResource ? (clicked) => onNavigateToResource(refToSelectedResource(clicked)) : undefined}
          />
        ))}
        {overflow > 0 && <span className={`${CHIP} ${CHIP_TONE.muted}`}>+{overflow} more</span>}
      </div>
    </div>
  );
}

type RelationshipGroup = 'service' | 'ingress' | 'route' | 'config' | 'scaler' | 'storage' | 'pdb' | 'networkPolicy'

function relationshipRefs(relationships: AppRow['relationships'] | undefined, group: RelationshipGroup): ResourceRef[] {
  if (!relationships) return []
  switch (group) {
    case 'service':
      return relationships.serviceRefs ?? []
    case 'ingress':
      return relationships.ingressRefs ?? []
    case 'route':
      return relationships.routeRefs ?? []
    case 'config':
      return relationships.configRefs ?? []
    case 'scaler':
      return relationships.scalerRefs ?? []
    case 'storage':
      return relationships.storageRefs ?? []
    case 'pdb':
      return relationships.pdbRefs ?? []
    case 'networkPolicy':
      return relationships.networkPolicyRefs ?? []
  }
}

function ApplicationNow({
  issues,
  onSelectWorkload,
}: {
  issues: AppIssue[]
  onSelectWorkload: (workload: AppWorkload) => void
}) {
  if (issues.length === 0) return null

  const top = issues[0];
  return (
    <section className="rounded-lg border border-theme-border bg-theme-surface shadow-theme-sm">
      <div className="flex flex-wrap items-start gap-3 border-b border-theme-border px-4 py-3">
        <span
          className={`flex h-8 w-8 shrink-0 items-center justify-center rounded-md ring-1 ring-inset ${issueTone(top.severity)}`}
        >
          <AlertTriangle className="h-4 w-4" aria-hidden />
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="text-sm font-semibold text-theme-text-primary">
              {top.title}
            </h2>
            <span className={`${CHIP} ${issueTone(top.severity)}`}>
              {top.severity === "error" ? "Needs attention" : "Warning"}
            </span>
          </div>
          {top.detail && (
            <p className="mt-0.5 text-sm text-theme-text-secondary">
              {top.detail}
            </p>
          )}
        </div>
        {top.workload && (
          <button
            type="button"
            onClick={() => onSelectWorkload(top.workload!)}
            className="rounded-md px-2.5 py-1.5 text-sm font-medium text-accent-text hover:bg-theme-hover"
          >
            Open workload
          </button>
        )}
      </div>
      {issues.length > 1 && (
        <div className="divide-y divide-theme-border px-4">
          {issues.slice(1, 5).map((issue) => (
            <div key={issue.key} className="flex items-start gap-3 py-3">
              <StatusDot
                tone={
                  issue.severity === "error"
                    ? "unhealthy"
                    : issue.severity === "warning"
                      ? "degraded"
                      : "neutral"
                }
                className="mt-1"
              />
              <div className="min-w-0 flex-1">
                <div className="truncate text-sm font-medium text-theme-text-primary">
                  {issue.title}
                </div>
                {issue.detail && (
                  <div className="truncate text-sm text-theme-text-tertiary">
                    {issue.detail}
                  </div>
                )}
              </div>
              {issue.workload && (
                <button
                  type="button"
                  onClick={() => onSelectWorkload(issue.workload!)}
                  className="shrink-0 text-sm font-medium text-accent-text hover:underline"
                >
                  Open
                </button>
              )}
            </div>
          ))}
        </div>
      )}
    </section>
  );
}

function ApplicationBatchOverview({
  activity,
}: {
  activity: AppBatchActivity[];
}) {
  if (activity.length === 0) return null;

  const stats = batchOverviewStats(activity);

  return (
    <ApplicationPanel title="Batch activity">
      <div className="grid gap-4">
        <div className="grid gap-3 sm:grid-cols-2 xl:grid-cols-4">
          <ApplicationFact
            variant="bare"
            label="Latest run"
            value={stats.latestValue}
            detail={stats.latestDetail}
          />
          <ApplicationFact
            variant="bare"
            label="Retained runs"
            value={stats.retainedValue}
            detail="Kubernetes-retained history"
          />
          <ApplicationFact
            variant="bare"
            label="Active work"
            value={stats.activeValue}
            detail={stats.activeDetail}
          />
          <ApplicationFact
            variant="bare"
            label="Schedules"
            value={stats.scheduleValue}
            detail={stats.scheduleDetail}
            monoValue={stats.scheduleMono}
          />
        </div>
      </div>
    </ApplicationPanel>
  );
}

function latestBatchRuns(activity: AppBatchActivity[]): AppBatchActivity[] {
  return [...activity].sort(
    (a, b) =>
      latestRunTime(b) - latestRunTime(a) ||
      a.workload.name.localeCompare(b.workload.name),
  );
}

function latestRunTimestamp(item: AppBatchActivity): string | undefined {
  return (
    item.latestStartedAt ||
    item.latestFinishedAt ||
    item.lastScheduledAt ||
    item.lastSuccessfulAt
  );
}

function latestRunTime(item: AppBatchActivity): number {
  const value = latestRunTimestamp(item);
  if (!value) return 0;
  const t = Date.parse(value);
  return Number.isNaN(t) ? 0 : t;
}

function batchOverviewStats(activity: AppBatchActivity[]) {
  const latest = latestBatchRuns(activity)[0];
  const schedules = Array.from(
    new Set(
      activity
        .map((item) => item.schedule)
        .filter((s): s is string => Boolean(s)),
    ),
  );
  const retained = activity.reduce((n, item) => n + item.retainedRuns, 0);
  const succeeded = activity.reduce(
    (n, item) => n + (item.workload.batch?.succeededRuns ?? 0),
    0,
  );
  const failed = activity.reduce((n, item) => n + item.failedRuns, 0);
  const active = activity.reduce((n, item) => n + item.activeRuns, 0);

  return {
    latestValue: latest?.latestRunPhase || latest?.label || "None",
    latestDetail: latest
      ? `${latest.latestRunName || latest.workload.name}${latestRunTimestamp(latest) ? ` · ${relativeTime(latestRunTimestamp(latest)!)} ` : ""}`.trim()
      : "No retained runs",
    retainedValue:
      retained > 0 ? `${succeeded} succeeded / ${failed} failed` : "0 retained",
    activeValue: active > 0 ? `${active} running` : "None",
    activeDetail:
      active > 0 ? activeWorkloadNames(activity) : "No active batch work",
    scheduleValue:
      schedules.length === 0
        ? "None"
        : schedules.length === 1
          ? schedules[0]
          : `${schedules.length} schedules`,
    scheduleDetail:
      schedules.length === 1 && latest?.lastSuccessfulAt
        ? `last success ${relativeTime(latest.lastSuccessfulAt)}`
        : undefined,
    scheduleMono: schedules.length === 1,
  };
}

function activeWorkloadNames(activity: AppBatchActivity[]): string {
  const active = activity
    .filter((item) => item.activeRuns > 0)
    .map((item) => item.workload.name);
  if (active.length <= 2) return active.join(", ");
  return `${active.slice(0, 2).join(", ")} +${active.length - 2} more`;
}

function relativeTime(value: string): string {
  const t = Date.parse(value);
  if (Number.isNaN(t)) return value;
  const diff = Date.now() - t;
  if (diff < 60_000) return "just now";
  const minutes = Math.floor(diff / 60_000);
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.floor(minutes / 60);
  if (hours < 48) return `${hours}h ago`;
  return `${Math.floor(hours / 24)}d ago`;
}

function ApplicationLatestHistory({
  history,
  sourceRef,
  showIncidentPreview,
  onSelectHistory,
  onOpenSource,
}: {
  history?: AppHistory
  sourceRef?: AppSourceRef
  showIncidentPreview?: boolean
  onSelectHistory: () => void
  onOpenSource?: (source: AppSourceRef) => void
}) {
  const summary = history?.summary
  const showIncident = summary?.state === 'incident' && showIncidentPreview
  if (!summary || !showIncident) return null
  const resolvedSource = sourceRef ?? history?.sourceRef
  const tone = CHIP_TONE.amber

  return (
    <section className="rounded-lg border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm">
      <div className="flex flex-wrap items-start gap-3">
        <span className={`flex h-8 w-8 shrink-0 items-center justify-center rounded-md ring-1 ring-inset ${tone}`}>
          <AlertTriangle className="h-4 w-4" aria-hidden />
        </span>
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <h2 className="text-sm font-semibold text-theme-text-primary">{summary.title}</h2>
            <span className={`${CHIP} ${tone}`}>Latest incident</span>
          </div>
          {summary.detail && (
            <p className="mt-0.5 line-clamp-2 text-sm text-theme-text-secondary">
              {summary.detail}
            </p>
          )}
          {summary.timestamp && (
            <p className="mt-1 text-xs text-theme-text-tertiary">
              {formatAppEventTime(summary.timestamp)}
            </p>
          )}
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {resolvedSource && !sourceRef && onOpenSource && (
            <button type="button" onClick={() => onOpenSource(resolvedSource)} className="rounded-md px-2.5 py-1.5 text-sm font-medium text-accent-text hover:bg-theme-hover">
              {sourceLinkLabel(resolvedSource)}
            </button>
          )}
          <button
            type="button"
            onClick={onSelectHistory}
            className="rounded-md px-2.5 py-1.5 text-sm font-medium text-accent-text hover:bg-theme-hover"
          >
            View history
          </button>
        </div>
      </div>
    </section>
  );
}

function ApplicationLatestChangeFact({
  summary,
  sourceRef,
  onSelectHistory,
  onOpenSource,
}: {
  summary: NonNullable<AppHistory['summary']>
  sourceRef?: AppSourceRef
  onSelectHistory: () => void
  onOpenSource?: (source: AppSourceRef) => void
}) {
  return (
    <div className="min-w-0 rounded-lg border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm md:col-span-2 2xl:col-span-1">
      <div className="flex items-center justify-between gap-2">
        <div className="text-[10px] font-semibold uppercase tracking-wide text-theme-text-tertiary">Latest change</div>
        <button type="button" onClick={onSelectHistory} className="shrink-0 text-xs font-medium text-accent-text hover:underline">History</button>
      </div>
      <div className="mt-1 truncate text-sm font-semibold text-theme-text-primary">{summary.title}</div>
      {summary.detail && <div className="mt-0.5 truncate font-mono text-xs text-theme-text-tertiary">{summary.detail}</div>}
      {sourceRef && onOpenSource && (
        <button type="button" onClick={() => onOpenSource(sourceRef)} className="mt-1 text-xs font-medium text-accent-text hover:underline">
          {sourceLinkLabel(sourceRef)}
        </button>
      )}
    </div>
  )
}

function WorkloadsMatrix({ workloads, onSelectWorkload }: { workloads: AppWorkload[]; onSelectWorkload: (workload: AppWorkload) => void }) {
  if (workloads.length === 0) {
    return <EmptyState variant="inline" headline="No inspectable workloads." />;
  }

  return (
    <div className="overflow-x-auto">
      <table className="w-full min-w-[760px] table-fixed text-sm">
        <thead>
          <tr className="border-b border-theme-border text-left text-[10px] uppercase tracking-wide text-theme-text-tertiary">
            <th className="w-[34%] px-2 py-2 font-semibold">Workload</th>
            <th className="w-[13%] px-2 py-2 font-semibold">Kind</th>
            <th className="w-[12%] px-2 py-2 font-semibold">Class</th>
            <th className="w-[14%] px-2 py-2 font-semibold">Status</th>
            <th className="w-[18%] px-2 py-2 font-semibold">Runtime</th>
            <th className="w-[16%] px-2 py-2 font-semibold">Version</th>
          </tr>
        </thead>
        <tbody>
          {workloads.map((w) => {
            const status = workloadRuntimeStatus(w)
            const reason = w.reason === 'Completed' && w.kind !== 'Job' ? undefined : w.reason
            return (
              <tr
                key={workloadKey(w)}
                className="border-b border-theme-border last:border-b-0 hover:bg-theme-hover"
              >
                <td className="truncate px-2 py-2">
                  <button
                    type="button"
                    onClick={() => onSelectWorkload(w)}
                    className="truncate font-medium text-theme-text-primary hover:text-accent-text hover:underline"
                  >
                    {w.name}
                  </button>
                  {reason && <div className="truncate text-xs text-theme-text-tertiary">{reason}</div>}
                </td>
                <td className="px-2 py-2 text-theme-text-secondary">{w.kind}</td>
                <td className="px-2 py-2 text-theme-text-secondary">{workloadClassOf(w.workload_class)}</td>
                <td className="px-2 py-2">
                  <span className="inline-flex items-center gap-1.5 text-theme-text-secondary">
                    <StatusDot tone={mapHealthToTone(status.health)} />
                    {status.label}
                  </span>
                </td>
                <td className="px-2 py-2 text-xs text-theme-text-secondary">
                  {workloadRuntimeDetail(w)}
                </td>
                <td className="truncate px-2 py-2 font-mono text-xs text-theme-text-secondary">
                  {w.version || w.appVersion || "-"}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

function workloadRuntimeStatus(workload: AppWorkload): {
  label: string;
  health: AppHealth;
} {
  const batch = workload.batch;
  if (!batch) {
    const health = healthOf(workload.health);
    return { label: HEALTH_META[health].label, health };
  }
  if ((batch.activeRuns ?? 0) > 0 || batch.latestRunPhase === "Running")
    return { label: "Running", health: "neutral" };
  if (batch.latestRunPhase === "Failed" || batch.latestRunPhase === "Error")
    return { label: "Failed", health: "unhealthy" };
  if (batch.latestRunPhase === "Succeeded")
    return { label: "Succeeded", health: "healthy" };
  if (batch.suspended) return { label: "Suspended", health: "neutral" };
  return { label: "Idle", health: "neutral" };
}

function workloadRuntimeDetail(workload: AppWorkload): string {
  const batch = workload.batch;
  if (!batch) return `${workload.ready}/${workload.desired} ready`;
  if ((batch.activeRuns ?? 0) > 0)
    return pluralize(batch.activeRuns ?? 0, "active run");
  const at =
    batch.latestStartedAt || batch.latestFinishedAt || batch.lastScheduledAt;
  if (batch.latestRunName)
    return `${midTruncate(batch.latestRunName, 28)}${at ? ` · ${relativeTime(at)}` : ""}`;
  if (batch.schedule) return `Schedule ${batch.schedule}`;
  return "No retained runs";
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
  deploymentLayer,
  deploymentSource,
  onOpenSource,
  onToggleReplicaSets,
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
  deploymentLayer: DeploymentTopologyLayer | null
  deploymentSource?: AppSourceRef
  onOpenSource?: (source: AppSourceRef) => void
  onToggleReplicaSets: (ownerID: string) => void
}) {
  const hasSharedOrUnscopedNodes = useMemo(
    () => topology?.nodes.some((node) => {
      if (node.data?.deploymentMembership === 'source-only') return false
      const stamp = ownershipOf(node.data)
      return !stamp.ownerWorkloadId && stamp.focusWorkloadIds.length !== 1
    }) ?? false,
    [topology],
  );

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
            onToggleReplicaSets={onToggleReplicaSets}
            fitViewPadding={{ top: 0.08, right: 0.05, bottom: 0.08, left: '360px' }}
          />
          <TopologyWorkloadLegend
            workloads={workloads}
            colorByWorkload={colorByWorkload}
            focusedOwnerId={focusedOwnerId}
            showSharedOrUnscoped={hasSharedOrUnscopedNodes}
            onFocus={onFocusWorkload}
            onSelectWorkload={onSelectWorkload}
            deploymentLayer={deploymentLayer}
            deploymentSource={deploymentSource}
            onOpenSource={onOpenSource}
          />
        </>
      ) : loading ? (
        <PaneLoader label="Loading topology..." className="absolute inset-0" />
      ) : (
        <div className="flex h-full items-center justify-center p-6">
          <EmptyState
            headline="No topology available"
            body="Radar could not build an application topology for this app."
          />
        </div>
      )}
    </div>
  );
}

function ApplicationHistoryView({
  history,
  loading,
  fallbackEvents,
  workloads,
  onSelectWorkload,
  onOpenSource,
}: {
  history?: AppHistory;
  loading?: boolean;
  fallbackEvents: NonNullable<AppRow["events"]>;
  workloads: AppWorkload[];
  onSelectWorkload: (workload: AppWorkload) => void;
  onOpenSource?: (source: AppSourceRef) => void;
}) {
  const anchors = history?.anchors ?? [];
  const incidents =
    history?.incidents ??
    fallbackEvents.map((event) => ({
      severity: "warning",
      title: event.object ? `${event.reason} on ${event.object}` : event.reason,
      object: event.object,
      message: event.message,
      count: event.count,
      firstSeen: event.firstSeen,
      lastSeen: event.lastSeen,
    }));
  const empty = !loading && anchors.length === 0 && incidents.length === 0;

  return (
    <div className="min-h-0 flex-1 overflow-auto">
      <div className="w-full max-w-[2400px] space-y-4 p-4 sm:p-6">
        {loading && (
          <ApplicationPanel title="History">
            <div className="text-sm text-theme-text-tertiary">
              Loading application history...
            </div>
          </ApplicationPanel>
        )}
        {history?.partialSources && history.partialSources.length > 0 && (
          <ApplicationPanel title="Partial history">
            <div className="space-y-1 text-sm text-theme-text-secondary">
              {history.partialSources.map((msg, idx) => (
                <div key={`${msg}-${idx}`}>{msg}</div>
              ))}
            </div>
          </ApplicationPanel>
        )}
        {incidents.length > 0 && (
          <ApplicationPanel title="Current incidents">
            <div className="divide-y divide-theme-border">
              {incidents.map((incident, idx) => {
                const workload = directWorkloadForEvent(
                  {
                    object: incident.object,
                    reason: "",
                    count: 1,
                    type: "Warning",
                  },
                  workloads,
                );
                return (
                  <HistoryIncidentLine
                    key={`${incident.object}-${incident.title}-${idx}`}
                    incident={incident}
                    action={
                      workload ? (
                        <button
                          type="button"
                          onClick={() => onSelectWorkload(workload)}
                          className="text-xs font-medium text-accent-text hover:underline"
                        >
                          Open workload
                        </button>
                      ) : undefined
                    }
                  />
                );
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
                  <button
                    type="button"
                    onClick={() => onOpenSource(history.sourceRef!)}
                    className="text-xs font-medium text-accent-text hover:underline"
                  >
                    {sourceLinkLabel(history.sourceRef)}
                  </button>
                )}
              </span>
            }
          >
            <div className="divide-y divide-theme-border">
              {anchors.map((anchor, idx) => (
                <HistoryAnchorLine
                  key={`${anchor.type}-${anchor.timestamp}-${anchor.revision}-${idx}`}
                  anchor={anchor}
                />
              ))}
            </div>
          </ApplicationPanel>
        )}
        {empty && (
          <ApplicationPanel title="History">
            <EmptyState
              variant="inline"
              headline="No retained deployment history."
              body="Current application state is still available in Overview."
            />
          </ApplicationPanel>
        )}
      </div>
    </div>
  );
}

function HistoryAnchorLine({
  anchor,
}: {
  anchor: NonNullable<AppHistory["anchors"]>[number];
}) {
  return (
    <div className="grid grid-cols-[minmax(0,1fr)_auto] gap-3 py-3 first:pt-0 last:pb-0">
      <div className="min-w-0">
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-medium text-theme-text-primary">
            {anchor.title}
          </span>
          {anchor.status && (
            <span className={`${CHIP} ${historyStatusTone(anchor.status)}`}>
              {anchor.status}
            </span>
          )}
        </div>
        {anchor.revision && (
          <div className="truncate font-mono text-sm text-theme-text-secondary">
            {anchor.revision}
          </div>
        )}
        {anchor.message && (
          <div className="mt-1 line-clamp-2 text-sm text-theme-text-tertiary">
            {anchor.message}
          </div>
        )}
        {anchor.source && (
          <div className="mt-1 truncate text-xs text-theme-text-tertiary">
            {anchor.source}
          </div>
        )}
      </div>
      <div className="text-right text-xs text-theme-text-tertiary">
        {formatAppEventTime(anchor.timestamp)}
        {anchor.initiatedBy && <div>{anchor.initiatedBy}</div>}
      </div>
    </div>
  );
}

function HistoryIncidentLine({
  incident,
  action,
}: {
  incident: NonNullable<AppHistory["incidents"]>[number];
  action?: ReactNode;
}) {
  return (
    <div className="grid grid-cols-[minmax(0,1fr)_auto] gap-3 py-3 first:pt-0 last:pb-0">
      <div className="min-w-0">
        <div className="truncate font-medium text-theme-text-primary">
          {incident.title}
        </div>
        {incident.object && (
          <div className="truncate text-sm text-theme-text-secondary">
            {incident.object}
          </div>
        )}
        {incident.message && (
          <div className="mt-1 line-clamp-2 text-sm text-theme-text-tertiary">
            {incident.message}
          </div>
        )}
      </div>
      <div className="text-right text-xs text-theme-text-tertiary">
        {formatAppEventTime(incident.lastSeen)}
        {incident.count && incident.count > 1 && <div>{incident.count}x</div>}
        {action && <div className="mt-1">{action}</div>}
      </div>
    </div>
  );
}

function buildAppIssues(
  workloads: AppWorkload[],
  events: NonNullable<AppRow["events"]>,
): AppIssue[] {
  const issues: AppIssue[] = [];
  const issueByWorkload = new Map<string, AppIssue>();

  for (const workload of workloads) {
    const health = healthOf(workload.health);
    const notReady = workload.desired > 0 && workload.ready < workload.desired;
    const hasRestarts = (workload.restarts ?? 0) > 0;
    const hasReason = Boolean(workload.reason);
    const hasHealthProblem = health === "degraded" || health === "unhealthy";
    if (!hasHealthProblem && !notReady && !hasRestarts && !hasReason) continue;

    const severity: AppIssueSeverity =
      health === "unhealthy" || (workload.desired > 0 && workload.ready === 0)
        ? "error"
        : "warning";
    const detail = [
      notReady ? `${workload.ready}/${workload.desired} ready` : undefined,
      hasRestarts ? pluralize(workload.restarts, "restart") : undefined,
      workload.reason,
    ]
      .filter(Boolean)
      .join(" · ");
    const issue: AppIssue = {
      key: `workload:${workloadKey(workload)}`,
      severity,
      title: `${workload.name} ${health === "unhealthy" ? "is down" : health === "degraded" || notReady ? "is degraded" : "needs attention"}`,
      detail,
      workload,
    };
    issueByWorkload.set(workloadKey(workload), issue);
    issues.push(issue);
  }

  for (const event of events) {
    const workload = directWorkloadForEvent(event, workloads);
    if (workload) {
      const existing = issueByWorkload.get(workloadKey(workload));
      if (existing) {
        existing.detail = [existing.detail, event.reason]
          .filter(Boolean)
          .join(" · ");
        continue;
      }
      issues.push({
        key: `event:${event.object}:${event.reason}`,
        severity: "warning",
        title: `${event.reason} on ${workload.name}`,
        detail: event.message,
        workload,
      });
      continue;
    }
    issues.push({
      key: `event:${event.object}:${event.reason}`,
      severity: "warning",
      title: `${event.reason} on ${event.object}`,
      detail: event.message,
    });
  }

  const rank: Record<AppIssueSeverity, number> = {
    error: 0,
    warning: 1,
    info: 2,
  };
  return issues.sort(
    (a, b) =>
      rank[a.severity] - rank[b.severity] || a.title.localeCompare(b.title),
  );
}

function directWorkloadForEvent(
  event: NonNullable<AppRow["events"]>[number],
  workloads: AppWorkload[],
): AppWorkload | undefined {
  const parsed = parseEventObject(event.object);
  if (!parsed) return undefined;
  const matches = workloads.filter(
    (w) =>
      w.kind.toLowerCase() === parsed.kind.toLowerCase() &&
      w.name === parsed.name,
  );
  return matches.length === 1 ? matches[0] : undefined;
}

function parseEventObject(
  object: string,
): { kind: string; name: string } | null {
  const slash = object.indexOf("/");
  if (slash <= 0 || slash === object.length - 1) return null;
  return { kind: object.slice(0, slash), name: object.slice(slash + 1) };
}

function issueTone(severity: AppIssueSeverity): string {
  if (severity === "error") return CHIP_TONE.rose;
  if (severity === "warning") return CHIP_TONE.amber;
  return CHIP_TONE.blue;
}

function historyStatusTone(status: string): string {
  const s = status.toLowerCase();
  if (s.includes("fail") || s.includes("error")) return CHIP_TONE.rose;
  if (s.includes("running") || s.includes("pending") || s.includes("progress"))
    return CHIP_TONE.amber;
  if (s.includes("succeed") || s.includes("deployed") || s.includes("ready"))
    return CHIP_TONE.emerald;
  return CHIP_TONE.muted;
}

function sourceLinkLabel(source: AppSourceRef): string {
  return `View ${sourceObjectLabel(source)}`
}

function sourceObjectLabel(source: AppSourceRef): string {
  if (source.type === 'helm') return 'Helm release'
  if (source.tool === 'argocd' || source.kind.toLowerCase() === 'application') return 'Argo CD application'
  if (source.kind.toLowerCase() === 'helmrelease') return 'Flux HelmRelease'
  if (source.kind.toLowerCase() === 'kustomization') return 'Flux Kustomization'
  return 'GitOps source'
}

function sourceInventoryLabel(source: AppSourceRef): string {
  if (source.type === 'helm') return 'Helm'
  if (source.tool === 'argocd' || source.kind.toLowerCase() === 'application') return 'Argo CD'
  if (source.tool === 'fluxcd') return 'Flux'
  return 'GitOps'
}

function SourceDeliveryStatus({ status }: { status: NonNullable<AppRow['sourceStatus']> }) {
  const values = [
    status.sync ? { label: status.sync, tone: sourceSyncHealth(status.sync) } : null,
    status.health ? { label: status.health, tone: sourceReportedHealth(status.health) } : null,
  ].filter((value): value is { label: string; tone: AppHealth } => value !== null)

  return (
    <span className="inline-flex min-w-0 items-center gap-2">
      {values.map((value, index) => (
        <span key={`${value.label}-${index}`} className="inline-flex min-w-0 items-center gap-1.5">
          {index > 0 && <span className="text-theme-text-tertiary" aria-hidden>·</span>}
          <StatusDot tone={mapHealthToTone(value.tone)} />
          <span className="truncate">{value.label}</span>
        </span>
      ))}
    </span>
  )
}

function formatAppEventTime(value?: string): string {
  if (!value || value.startsWith("0001-01-01T00:00:00")) return "";
  return value;
}

function ApplicationPanel({
  title,
  children,
}: {
  title: ReactNode;
  children: ReactNode;
}) {
  return (
    <section className="rounded-lg border border-theme-border bg-theme-surface p-4 shadow-theme-sm">
      <h2 className="mb-3 text-sm font-semibold text-theme-text-primary">
        {title}
      </h2>
      {children}
    </section>
  );
}

function ApplicationFact({
  label,
  value,
  detail,
  monoValue,
  monoDetail,
  variant = "card",
}: {
  label: string
  value: ReactNode
  detail?: string
  monoValue?: boolean
  monoDetail?: boolean
  variant?: 'card' | 'row' | 'bare'
}) {
  if (variant === "row") {
    return (
      <div className="min-w-0 border-b border-theme-border pb-3 last:border-b-0">
        <div className="text-[10px] font-semibold uppercase tracking-wide text-theme-text-tertiary">
          {label}
        </div>
        <div
          className={clsx(
            "mt-1 truncate text-sm font-semibold text-theme-text-primary",
            monoValue && "font-mono",
          )}
        >
          {value}
        </div>
        {detail && (
          <div
            className={clsx(
              "mt-0.5 truncate text-xs text-theme-text-tertiary",
              monoDetail && "font-mono",
            )}
          >
            {detail}
          </div>
        )}
      </div>
    );
  }

  if (variant === "bare") {
    return (
      <div className="min-w-0">
        <div className="text-[10px] font-semibold uppercase tracking-wide text-theme-text-tertiary">
          {label}
        </div>
        <div
          className={clsx(
            "mt-1 truncate text-sm font-semibold text-theme-text-primary",
            monoValue && "font-mono",
          )}
        >
          {value}
        </div>
        {detail && (
          <div
            className={clsx(
              "mt-0.5 truncate text-xs text-theme-text-tertiary",
              monoDetail && "font-mono",
            )}
          >
            {detail}
          </div>
        )}
      </div>
    );
  }

  return (
    <div className="min-w-0 rounded-lg border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm">
      <div className="text-[10px] font-semibold uppercase tracking-wide text-theme-text-tertiary">
        {label}
      </div>
      <div
        className={clsx(
          "mt-1 truncate text-sm font-semibold text-theme-text-primary",
          monoValue && "font-mono",
        )}
      >
        {value}
      </div>
      {detail && (
        <div
          className={clsx(
            "mt-0.5 truncate text-xs text-theme-text-tertiary",
            monoDetail && "font-mono",
          )}
        >
          {detail}
        </div>
      )}
    </div>
  );
}

function ApplicationScopeSelector({
  workloads,
  selectedWorkload,
  onSelect,
  onFocus,
}: {
  workloads: AppWorkload[];
  selectedWorkload: AppWorkload | null;
  onSelect: (key: string | null) => void;
  onFocus: (owner: WorkloadFocus) => void;
}) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const rootRef = useDismissablePopover<HTMLDivElement>(open, setOpen);
  const selectedKey = selectedWorkload ? workloadKey(selectedWorkload) : null;
  const filteredWorkloads = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return workloads;
    return workloads.filter((w) =>
      `${w.kind} ${w.namespace} ${w.name}`.toLowerCase().includes(q),
    );
  }, [query, workloads]);

  if (workloads.length <= 1) {
    return selectedWorkload ? (
      <StaticWorkloadScope workload={selectedWorkload} />
    ) : (
      <span className="inline-flex max-w-[min(48vw,34rem)] items-center gap-2 rounded-md bg-theme-base px-2.5 py-1.5 text-sm ring-1 ring-inset ring-theme-border">
        <Boxes
          className="h-4 w-4 shrink-0 text-theme-text-secondary"
          aria-hidden
        />
        <span className="min-w-0 truncate font-medium text-theme-text-primary">
          Application
        </span>
      </span>
    );
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
          <Boxes
            className="h-4 w-4 shrink-0 text-theme-text-secondary"
            aria-hidden
          />
        )}
        <span className="min-w-0 truncate font-medium text-theme-text-primary">
          {selectedWorkload ? selectedWorkload.name : "Application"}
        </span>
        {selectedWorkload && (
          <span className="hidden shrink-0 text-xs uppercase tracking-wide text-theme-text-tertiary md:inline">
            {selectedWorkload.kind}
          </span>
        )}
        <ChevronDown
          className={clsx(
            "h-3.5 w-3.5 shrink-0 text-theme-text-tertiary transition-transform",
            open && "rotate-180",
          )}
          aria-hidden
        />
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
              icon={
                <Boxes
                  className="h-4 w-4 text-theme-text-secondary"
                  aria-hidden
                />
              }
              title="Application"
              subtitle="App scope"
              onClick={() => {
                setOpen(false);
                onSelect(null);
              }}
              onMouseEnter={() => onFocus(null)}
            />
          </div>
          <div className="border-b border-theme-border px-2 py-1.5">
            <div className="flex items-center justify-between gap-3">
              <span className="text-[10px] font-semibold uppercase tracking-wide text-theme-text-tertiary">
                Workloads
              </span>
              <span className="rounded-full bg-theme-hover px-1.5 text-[10px] font-medium text-theme-text-tertiary">
                {workloads.length}
              </span>
            </div>
            {workloads.length > 8 && (
              <label className="mt-2 flex items-center gap-2 rounded-md bg-theme-base px-2 py-1 ring-1 ring-inset ring-theme-border">
                <Search
                  className="h-3.5 w-3.5 shrink-0 text-theme-text-tertiary"
                  aria-hidden
                />
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
              <div className="px-2 py-4 text-center text-sm text-theme-text-tertiary">
                No workloads match.
              </div>
            ) : (
              filteredWorkloads.map((w) => {
                const key = workloadKey(w);
                return (
                  <ScopeOption
                    key={key}
                    active={selectedKey === key}
                    icon={<WorkloadKindIcon workload={w} />}
                    title={w.name}
                    subtitle={`${w.kind} · ${w.ready}/${w.desired} ready${w.reason ? ` · ${w.reason}` : ""}`}
                    onClick={() => {
                      setOpen(false);
                      onSelect(key);
                    }}
                    onMouseEnter={() => onFocus(key)}
                  />
                );
              })
            )}
          </div>
        </div>
      )}
    </div>
  );
}

function TopologyWorkloadLegend({
  workloads,
  colorByWorkload,
  focusedOwnerId,
  showSharedOrUnscoped,
  onFocus,
  onSelectWorkload,
  deploymentLayer,
  deploymentSource,
  onOpenSource,
}: {
  workloads: AppWorkload[]
  colorByWorkload: Map<string, number> | null
  focusedOwnerId: WorkloadFocus
  showSharedOrUnscoped: boolean
  onFocus: (owner: WorkloadFocus) => void
  onSelectWorkload: (workload: AppWorkload) => void
  deploymentLayer: DeploymentTopologyLayer | null
  deploymentSource?: AppSourceRef
  onOpenSource?: (source: AppSourceRef) => void
}) {
  const managedOnlyCount = deploymentLayer?.managedOnly.length ?? 0
  const runtimeOnlyCount = deploymentLayer?.runtimeOnlyCount ?? 0
  const showSourceDifferences = !!deploymentSource && managedOnlyCount + runtimeOnlyCount > 0
  const sourceLabel = deploymentSource ? sourceInventoryLabel(deploymentSource) : ''

  return (
    <div className="absolute left-4 top-4 z-10 w-[min(18.5rem,calc(100%-2rem))] 2xl:w-[min(22rem,calc(100%-2rem))] overflow-hidden rounded-lg border border-theme-border bg-theme-surface/95 shadow-theme-md backdrop-blur">
      <div className="flex items-center justify-between gap-3 border-b border-theme-border bg-theme-base px-3 py-2">
        <span className="text-xs font-semibold text-theme-text-primary">
          Workloads
        </span>
        <span className="rounded-full bg-theme-hover px-1.5 text-[10px] font-medium text-theme-text-tertiary">
          {workloads.length}
        </span>
      </div>
      <div
        className="max-h-72 overflow-y-auto p-1"
        onMouseLeave={() => onFocus(null)}
      >
        {workloads.map((w) => {
          const key = workloadKey(w);
          const idx = colorByWorkload?.get(key);
          const hue = idx != null ? workloadHue(idx) : null;
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
          );
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
      {showSourceDifferences && deploymentSource && (
        <div className="border-t border-theme-border px-3 py-2.5">
          <div className="mb-1.5 text-[10px] font-semibold uppercase tracking-wide text-theme-text-tertiary">Source differences</div>
          <div className="space-y-1">
            {managedOnlyCount > 0 && (
              <div className="flex items-center gap-2 px-1.5 py-1 text-xs text-theme-text-secondary">
                <DeploymentSourceLogo source={deploymentSource} />
                <span className="min-w-0 flex-1">
                  <span className="block">{managedOnlyCount} only in {sourceLabel}</span>
                  {deploymentLayer?.managedOnlySummary && (
                    <span className="mt-0.5 block text-[10px] text-theme-text-tertiary">{deploymentLayer.managedOnlySummary}</span>
                  )}
                </span>
                {onOpenSource && (
                  <Tooltip content={`Open ${sourceLabel} view`} delay={150}>
                    <button type="button" onClick={() => onOpenSource(deploymentSource)} className="rounded p-0.5 text-theme-text-tertiary hover:bg-theme-hover hover:text-theme-text-primary">
                      <ExternalLink className="h-3 w-3" aria-hidden />
                    </button>
                  </Tooltip>
                )}
              </div>
            )}
            {runtimeOnlyCount > 0 && (
              <div className="flex items-center gap-2 px-1.5 py-1 text-xs text-theme-text-secondary">
                <Radio className="h-3.5 w-3.5 shrink-0 text-theme-text-tertiary" aria-hidden />
                <span>{pluralize(runtimeOnlyCount, 'runtime-only resource')}</span>
              </div>
            )}
          </div>
        </div>
      )}
    </div>
  );
}

function DeploymentSourceLogo({ source }: { source: AppSourceRef }) {
  const sourceLabel = sourceInventoryLabel(source)
  const logo = sourceLabel === 'Argo CD' ? argoCdLogo : sourceLabel === 'Flux' ? fluxLogo : null
  if (!logo) return <Layers className="h-3.5 w-3.5 shrink-0 text-theme-text-tertiary" aria-hidden />
  return <img src={logo} alt="" className="h-4 w-4 shrink-0 object-contain" />
}

function StaticWorkloadScope({ workload }: { workload: AppWorkload }) {
  return (
    <span className="inline-flex max-w-[min(48vw,34rem)] items-center gap-2 rounded-md bg-theme-base px-2.5 py-1.5 text-sm ring-1 ring-inset ring-theme-border">
      <WorkloadKindIcon workload={workload} />
      <span className="min-w-0 truncate font-medium text-theme-text-primary">
        {workload.name}
      </span>
      <span className="hidden shrink-0 text-xs uppercase tracking-wide text-theme-text-tertiary md:inline">
        {workload.kind}
      </span>
    </span>
  );
}

function WorkloadKindIcon({
  workload,
  compact,
}: {
  workload: AppWorkload;
  compact?: boolean;
}) {
  const KindIcon = getTopologyIcon(workload.kind);
  return (
    <span
      className={clsx(
        "relative flex shrink-0 items-center justify-center",
        compact ? "h-4 w-4" : "h-5 w-5",
      )}
      aria-hidden
    >
      <KindIcon
        className={clsx(
          "text-theme-text-secondary",
          compact ? "h-3.5 w-3.5" : "h-4 w-4",
        )}
      />
      <StatusDot
        tone={mapHealthToTone(healthOf(workload.health))}
        size="xs"
        className="absolute -bottom-0.5 -right-0.5 ring-2 ring-theme-base"
      />
    </span>
  );
}

function SharedScopeMarker() {
  return (
    <span className="flex w-5 shrink-0 items-center gap-1" aria-hidden>
      <span className="block h-4 w-1 rounded-full bg-theme-border" />
      <span className="h-1.5 w-1.5 rounded-full bg-theme-border" />
    </span>
  );
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
  active?: boolean;
  muted?: boolean;
  compact?: boolean;
  onClick?: () => void;
  onMouseEnter?: () => void;
  icon: ReactNode;
  title: string;
  subtitle?: string;
  accentColor?: string;
}) {
  const className = clsx(
    "relative flex w-full items-center gap-2 rounded-md text-left transition-colors",
    compact ? "px-2 py-1.5" : "px-2 py-2",
    active ? "selection selection-ring" : onClick && "hover:bg-theme-hover",
    !onClick && "cursor-default",
  );
  const inner = (
    <>
      {accentColor && (
        <span
          className="absolute inset-y-2 left-0 w-1 rounded-full"
          style={{ background: accentColor }}
          aria-hidden
        />
      )}
      {icon}
      <span className="min-w-0 flex-1">
        <span
          className={clsx(
            "block truncate text-sm",
            muted
              ? "text-theme-text-tertiary"
              : "font-medium text-theme-text-primary",
          )}
        >
          {title}
        </span>
        {subtitle && (
          <span className="block truncate text-[10px] uppercase tracking-wide text-theme-text-tertiary">
            {subtitle}
          </span>
        )}
      </span>
    </>
  );
  return onClick ? (
    <button
      type="button"
      onClick={onClick}
      onMouseEnter={onMouseEnter}
      className={className}
    >
      {inner}
    </button>
  ) : (
    <div onMouseEnter={onMouseEnter} className={className}>
      {inner}
    </div>
  );
}

function useDismissablePopover<T extends HTMLElement>(
  open: boolean,
  setOpen: (open: boolean) => void,
) {
  const rootRef = useRef<T>(null);
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (rootRef.current && !rootRef.current.contains(e.target as Node))
        setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    document.addEventListener("keydown", onKey);
    return () => {
      document.removeEventListener("mousedown", onDown);
      document.removeEventListener("keydown", onKey);
    };
  }, [open, setOpen]);
  return rootRef;
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
    );
  }
  return (
    <div className="flex h-full min-h-0 flex-col">
      <div key={workloadKey(workload)} className="min-h-0 flex-1">
        {renderWorkload(workload)}
      </div>
    </div>
  );
}

function restartWarning(
  workloads: AppWorkload[],
): { restarts: number; reason?: string; workload: string } | null {
  let worst: { restarts: number; reason?: string; workload: string } | null =
    null;
  for (const w of workloads) {
    const r = w.restarts ?? 0;
    if (r > 0 && (!worst || r > worst.restarts)) {
      worst = {
        restarts: r,
        reason: w.reason,
        workload: `${w.kind}/${w.name}`,
      };
    }
  }
  return worst;
}

// EnvSwitcher — the Environment fact's interactive form when one app runs in
// several environments. Up to MAX_INLINE_ENVS: inline pills (the at-a-glance
// ladder). More: a picker popover with the full ladder-ordered list — same
// affordance at 5 envs or 100. Always ends with the evidence chip
// (AppIdentityTooltip) and, when a ranked lower env outruns a ranked higher one,
// the amber lag chip.
const MAX_INLINE_ENVS = 4;

function EnvSwitcher({
  identityKey,
  instances,
  activeKey,
  onSwitch,
}: {
  identityKey: string;
  instances: AppIdentityInstance[];
  activeKey: string;
  onSwitch?: (appKey: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const rootRef = useDismissablePopover<HTMLDivElement>(open, setOpen);

  const envInstances = useMemo(
    () => collapseIdentityInstances(instances, activeKey),
    [instances, activeKey],
  );
  const lag = appGroupLagMessage(envInstances);
  const active =
    envInstances.find((i) => i.appKey === activeKey) ??
    instances.find((i) => i.appKey === activeKey);
  const envCounts = useMemo(() => {
    const counts = new Map<string, number>();
    for (const inst of instances)
      counts.set(inst.env, (counts.get(inst.env) ?? 0) + 1);
    return counts;
  }, [instances]);
  const evidenceChip = (
    <Tooltip
      content={
        <AppIdentityTooltip
          identityKey={identityKey}
          members={instances.map((i) => ({
            name: i.name,
            env: i.env,
            confidence: i.confidence,
            evidence: i.evidence,
          }))}
        />
      }
      delay={150}
    >
      <span className="inline-flex cursor-default items-center rounded-sm bg-theme-hover px-1 py-px ring-1 ring-inset ring-theme-border">
        <Layers className="h-3 w-3 text-theme-text-tertiary" aria-hidden />
      </span>
    </Tooltip>
  );
  const lagChip = lag && (
    <span className={`${CHIP} ${CHIP_TONE.amber}`}>{lag}</span>
  );

  if (envInstances.length <= MAX_INLINE_ENVS) {
    return (
      <span className="flex flex-wrap items-center gap-1">
        {instances.map((inst) => {
          const isActive = inst.appKey === activeKey;
          const duplicateEnv = (envCounts.get(inst.env) ?? 0) > 1;
          return (
            <Tooltip
              key={inst.appKey}
              content={`${inst.name}${inst.version ? ` · ${inst.version}` : ""}`}
              delay={150}
            >
              <button
                type="button"
                disabled={isActive}
                onClick={() => !isActive && onSwitch?.(inst.appKey)}
                className={clsx(
                  "inline-flex items-center gap-1 rounded-md px-1.5 py-0.5 text-xs ring-1 ring-inset transition-colors",
                  isActive
                    ? "selection selection-ring font-medium"
                    : "bg-theme-surface ring-theme-border hover:bg-theme-hover",
                )}
              >
                <StatusDot tone={mapHealthToTone(inst.health)} />
                {inst.env}
                {duplicateEnv ? (
                  <span className="max-w-24 truncate text-theme-text-tertiary">
                    {inst.name}
                  </span>
                ) : null}
              </button>
            </Tooltip>
          );
        })}
        {evidenceChip}
        {lagChip}
      </span>
    );
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
        <span className="font-medium">{active?.env ?? "—"}</span>
        <span className="text-theme-text-tertiary">
          · {envInstances.length} environments
        </span>
        <ChevronDown
          className={clsx(
            "h-3 w-3 text-theme-text-tertiary transition-transform",
            open && "rotate-180",
          )}
          aria-hidden
        />
      </button>
      {evidenceChip}
      {lagChip}
      {open && (
        <div
          role="listbox"
          className="absolute left-0 top-full z-50 mt-1 max-h-80 w-80 overflow-y-auto rounded-md border border-theme-border bg-theme-surface p-1 shadow-theme-md"
        >
          {instances.map((inst) => {
            const isActive = inst.appKey === activeKey;
            return (
              <button
                key={inst.appKey}
                type="button"
                role="option"
                aria-selected={isActive}
                onClick={() => {
                  setOpen(false);
                  if (!isActive) onSwitch?.(inst.appKey);
                }}
                className={clsx(
                  "flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-xs",
                  isActive
                    ? "selection selection-ring"
                    : "hover:bg-theme-hover",
                )}
              >
                <StatusDot tone={mapHealthToTone(inst.health)} />
                <span className="w-20 shrink-0 font-medium text-theme-text-primary">
                  {inst.env}
                </span>
                <span className="min-w-0 flex-1 truncate text-theme-text-secondary">
                  {inst.name}
                </span>
                {inst.version && (
                  <span className="font-mono text-[10px] text-theme-text-tertiary">
                    {midTruncate(inst.version, 18)}
                  </span>
                )}
              </button>
            );
          })}
        </div>
      )}
    </div>
  );
}
