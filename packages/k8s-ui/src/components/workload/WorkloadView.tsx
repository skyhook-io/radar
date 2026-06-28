import { useState, useMemo, useEffect, useRef, useCallback, type ReactNode } from 'react'
import { flushSync } from 'react-dom'
import { useRefreshAnimation } from '../../hooks/useRefreshAnimation'
import { startViewTransitionSafe } from '../../utils/view-transition'
import { FetchResult } from '../ui/FetchResult'
import { PaneLoader } from '../ui/PaneLoader'
import { useRegisterShortcuts } from '../../hooks/useKeyboardShortcuts'
import { clsx } from 'clsx'
import {
  ArrowLeft,
  ArrowRight,
  RefreshCw,
  Activity,
  Terminal,
  Layers,
  FileText,
  Copy,
  Check,
  Minimize2,
  Maximize2,
  X,
  BarChart3,
  Network,
} from 'lucide-react'
import type { TimelineEvent, ResourceRef, Relationships, SelectedResource, ResolvedEnvFrom, Topology, TopologyNode, HPADiagnosis } from '../../types'
import type { GitOpsStatus } from '../../types/gitops'
import type { NavigateToResource } from '../../utils/navigation'
import { refToSelectedResource, pluralToKind, kindToPlural, apiVersionToGroup } from '../../utils/navigation'
import { neighborhoodFor, seedNodeIds } from '../../utils/topology-neighborhood'
import { TopologyGraph } from '../topology/TopologyGraph'
import { gitOpsOwnerFromRelationships, type GitOpsOwnerRef } from '../../utils/gitops-owner'
import { gitOpsRouteForResource } from '../../utils/gitops-route'
import { isChangeEvent, isHistoricalEvent } from '../../types'
import { getKindBadgeColor, getHealthBadgeColor } from '../../utils/badge-colors'
import { buildResourceHierarchy, getAllEventsFromHierarchy, isProblematicEvent, type ResourceLane } from '../../utils/resource-hierarchy'
import {
  ZOOM_LEVELS,
  type ZoomLevel,
  formatAxisTime,
  EventMarker,
  EventDotLegend,
  HealthSpanLegend,
  HealthSpan,
  ZoomControls,
  buildHealthSpans,
  timeToX,
  calculateTimeRange,
} from '../timeline/shared'
import { ResourceActionsBar } from '../shared/ResourceActionsBar'
import { EditableYamlView, SaveSuccessAnimation } from '../shared/EditableYamlView'
import { ResourceRendererDispatch, getResourceStatus, type RendererOverrides } from '../shared/ResourceRendererDispatch'
import type { ScalerDiagnosis } from '../resources/renderers/WorkloadRenderer'
import { DetailShell, type DetailShellTab } from '../shared/DetailShell'
import { HelmManagedByChip, ManagedByChip, type HelmOwnerRef } from '../shared/ManagedByChip'
import { getKindColorOutline, displayKindName, OperationalIssuesShownContext } from '../ui/drawer-components'
import { midTruncate } from '../../utils/format'

export type WorkloadTabType = 'overview' | 'topology' | 'timeline' | 'logs' | 'metrics' | 'yaml'
type TabType = WorkloadTabType

// ============================================================================
// MAIN WORKLOAD VIEW — presentation only, data injected via props
// ============================================================================

interface WorkloadViewProps {
  kind: string
  namespace: string
  name: string
  onBack: () => void
  onNavigateToResource?: NavigateToResource
  onCollapseToDrawer?: () => void
  /** false = collapsed drawer mode, true (default) = full expanded mode */
  expanded?: boolean
  /** Close the drawer (collapsed mode) */
  onClose?: () => void
  /** Expand from drawer to full view */
  onExpand?: () => void
  /** Initial view tab — 'yaml' opens YAML directly */
  initialTab?: 'detail' | 'yaml'
  /** API group for CRD resources */
  group?: string

  // ── Hosted chrome (expanded mode) ────────────────────────────────────────
  /**
   * A breadcrumb rendered above the identity header — e.g. when a larger
   * surface (Radar Cloud's app page) hosts this view inside its own navigation.
   * When set, the standalone back button is not rendered; `onBack` still backs
   * the Escape shortcut.
   */
  breadcrumb?: ReactNode
  /** Suppress the standalone back arrow — for embeddings where "back" has no
   *  meaningful target (a single-workload app has no app graph to return to). */
  hideBackButton?: boolean
  /**
   * Controls injected into the shell's tab-row scope slot — e.g. a cluster /
   * workload picker in Radar Cloud. Absent in standalone Radar.
   */
  scopeControls?: ReactNode
  /** Hide WorkloadView's own breadcrumb/identity header when a host page owns that chrome. */
  compactHeader?: boolean

  // ── Data (injected by wrapper) ──────────────────────────────────────────
  /** The resource data object */
  resource?: any
  /** Resource relationships (pods, owner, config, etc.) */
  relationships?: Relationships
  /** TLS certificate info for secrets */
  certificateInfo?: any
  /** HPA diagnosis for HorizontalPodAutoscaler detail responses */
  hpaDiagnosis?: HPADiagnosis
  /** Compact diagnosis for autoscalers controlling this workload */
  scalerDiagnostics?: ScalerDiagnosis[]
  /** Whether the resource is loading */
  isLoading?: boolean
  /** Fetch error for the resource (preserves status + message so the
   *  drawer body can distinguish 403/404/503 from "no data"). */
  resourceError?: unknown
  /** Function to refetch the resource data */
  refetch?: () => void

  // ── Timeline data ────────────────────────────────────────────────────────
  /** All timeline events for this resource's namespace */
  allEvents?: TimelineEvent[]
  /** Whether timeline events are loading */
  eventsLoading?: boolean
  /** Topology data for hierarchy building + the Topology tab's neighborhood. */
  topology?: Topology
  resourceFocusedK8sEvents?: TimelineEvent[]
  resourceFocusedUpdates?: TimelineEvent[]
  resourceFocusedEventsLoading?: boolean
  resourceFocusedK8sError?: Error | null
  resourceFocusedUpdatesError?: Error | null

  // ── Capabilities ─────────────────────────────────────────────────────────
  /** Whether secrets can be updated */
  canUpdateSecrets?: boolean
  /** Whether YAML editing should be disabled for read-only host surfaces. */
  readOnlyYaml?: boolean

  // ── Mutations ────────────────────────────────────────────────────────────
  /** Update a resource from YAML */
  onUpdateResource?: (params: { kind: string; namespace: string; name: string; yaml: string }) => Promise<void>
  /** Whether the resource is being updated */
  isUpdatingResource?: boolean
  /** Error message from the last update attempt */
  updateResourceError?: string | null

  // ── Tab state (optional URL sync) ────────────────────────────────────────
  /** Controlled active tab. If not provided, managed internally. */
  activeTab?: TabType
  /** Called when tab changes (for URL sync etc.) */
  onTabChange?: (tab: TabType) => void

  // ── GitOps navigation ─────────────────────────────────────────────────────
  /**
   * Open the GitOps detail page for a controller (Argo Application,
   * Flux Kustomization, Flux HelmRelease). The drawer's "Managed by" chip
   * invokes this when the user clicks through; if not provided, the chip
   * is rendered as a non-interactive label so the relationship is still
   * visible (useful for hosts that haven't routed the GitOps tab yet).
   */
  onOpenGitOpsResource?: (ref: GitOpsOwnerRef) => void
  /** Owner ref resolved by the host when relationships lack enough detail, e.g. Argo labels without namespace. */
  resolvedGitOpsOwner?: GitOpsOwnerRef | null
  /** True when the owner exists locally and can be opened as a GitOps detail page. */
  gitOpsOwnerVerified?: boolean
  /** True while the host is still resolving whether the owner exists locally. */
  gitOpsOwnerPending?: boolean
  /** Metadata key/value that caused GitOps ownership inference, when known. */
  gitOpsOwnerSource?: string | null
  /** Sync/health status for the GitOps owner, when the host can resolve it. */
  gitOpsOwnerStatus?: GitOpsStatus | null
  /** Native Helm release that manages this resource, when detected. */
  helmOwner?: HelmOwnerRef | null
  /** Metadata key/value that caused native Helm ownership inference, when known. */
  helmOwnerSource?: string | null
  /** Open the native Helm release drawer. */
  onOpenHelmRelease?: (ref: HelmOwnerRef) => void
  /**
   * Open the GitOps detail page for the resource itself, when the resource
   * is a portal-classified GitOps CR (Argo Application/ApplicationSet/
   * AppProject, Flux Kustomization/HelmRelease). Wired in addition to
   * `onOpenGitOpsResource` because the URL is derived here from the live
   * resource rather than from owner labels on a managed object.
   */
  onNavigateGitOpsPath?: (path: string) => void

  // ── Render props for platform-specific content ───────────────────────────
  /** Render the logs tab content */
  renderLogsTab?: (props: {
    kind: string
    apiKind: string
    namespace: string
    name: string
    resource: any
    pods: ResourceRef[]
    selectedPod: string | null
    onSelectPod: (name: string | null) => void
    initialContainer: string | null
    onConsumeInitialContainer: () => void
  }) => ReactNode
  /** Render the metrics tab content */
  renderMetricsTab?: (props: { kind: string; namespace: string; name: string }) => ReactNode
  /** Render a read-only YAML view for a related object from the workload's
   *  neighborhood. Providing this turns the YAML tab into an object explorer
   *  (rail of the workload + its Services/config/policies/pods); omitting it
   *  keeps the single-manifest YAML tab. Injected because resource fetching
   *  lives host-side. */
  renderRelatedYaml?: (ref: { kind: string; namespace: string; name: string; group?: string }) => ReactNode
  /** Whether metrics are available for this resource kind */
  isMetricsAvailable?: (kind: string, resource: any) => boolean
  /** Render extra content at the bottom of the overview tab (e.g. audit findings) */
  renderOverviewExtra?: (props: { kind: string; namespace: string; name: string }) => ReactNode
  /** Render content at the TOP of the overview tab, above the renderer (e.g. live
   *  Operational Issues). Optional + additive — consumers that don't pass it are
   *  unaffected. Only rendered when `hasOperationalIssues` is true: the lead
   *  component returns null when empty, but its padded wrapper can't tell, so
   *  gating on the flag avoids an empty top gap on healthy resources. */
  renderOverviewLead?: (props: { kind: string; namespace: string; name: string }) => ReactNode
  /** When true, renderers suppress their own status-derived problem displays
   *  because a dedicated Operational Issues section is shown (the host fetched
   *  live issues for this resource). Avoids showing the same failure twice.
   *  Also gates the `renderOverviewLead` wrapper (see above). */
  hasOperationalIssues?: boolean

  // ── Duplicate ────────────────────────────────────────────────────────────
  /** Duplicate handler — opens create dialog with this resource's YAML */
  onDuplicate?: (params: { kind: string; namespace: string; name: string; yaml: string }) => void

  // ── Download ─────────────────────────────────────────────────────────────
  /** Forwarded to EditableYamlView; see there. */
  onDownload?: (content: string, mime: string, filename: string) => void

  // ── ResourceActionsBar props (passed through) ────────────────────────────
  /** All props for the actions bar (forwarded as-is) */
  actionsBarProps?: Record<string, any>
  /** Platform-specific renderer overrides (e.g. with hooks for metrics, exec, port-forward) */
  rendererOverrides?: RendererOverrides
  /** Resolved ConfigMap/Secret data for envFrom expansion in PodRenderer */
  resolvedEnvFrom?: ResolvedEnvFrom
}

export function WorkloadView({
  kind: kindProp,
  namespace,
  name,
  onBack,
  onNavigateToResource,
  onCollapseToDrawer,
  expanded = true,
  onClose,
  onExpand,
  initialTab,
  group,
  breadcrumb,
  hideBackButton,
  scopeControls,
  compactHeader,
  // Data
  resource,
  relationships,
  certificateInfo,
  hpaDiagnosis,
  scalerDiagnostics,
  isLoading: resourceLoading = false,
  resourceError,
  refetch: refetchProp,
  // Timeline
  allEvents,
  eventsLoading = false,
  topology,
  resourceFocusedK8sEvents,
  resourceFocusedUpdates,
  resourceFocusedEventsLoading = false,
  resourceFocusedK8sError = null,
  resourceFocusedUpdatesError = null,
  // Capabilities
  canUpdateSecrets,
  readOnlyYaml,
  // Mutations
  onUpdateResource,
  isUpdatingResource,
  updateResourceError,
  // Tab state
  activeTab: controlledTab,
  onTabChange,
  // Render props
  renderLogsTab,
  renderRelatedYaml,
  renderMetricsTab,
  isMetricsAvailable,
  // Duplicate
  onDuplicate,
  onDownload,
  renderOverviewExtra,
  renderOverviewLead,
  hasOperationalIssues,
  // Actions bar
  actionsBarProps,
  // Renderer overrides
  rendererOverrides,
  // Pod env expansion
  resolvedEnvFrom,
  // GitOps
  onOpenGitOpsResource,
  resolvedGitOpsOwner,
  gitOpsOwnerVerified = true,
  gitOpsOwnerPending = false,
  gitOpsOwnerSource,
  gitOpsOwnerStatus,
  helmOwner,
  helmOwnerSource,
  onOpenHelmRelease,
  onNavigateGitOpsPath,
}: WorkloadViewProps) {
  // Normalize kind: URL has plural lowercase, internal logic uses singular PascalCase
  const kind = pluralToKind(kindProp)
  const apiKind = kindProp

  // Tab state — controlled or uncontrolled
  const [internalTab, setInternalTab] = useState<TabType>('overview')
  const activeTab = controlledTab ?? internalTab
  const handleSetTab = useCallback((tab: TabType) => {
    setInternalTab(tab)
    onTabChange?.(tab)
  }, [onTabChange])

  // Collapsed mode state (YAML toggle for drawer mode)
  const [showYaml, setShowYaml] = useState(initialTab === 'yaml')
  useEffect(() => {
    setShowYaml(initialTab === 'yaml')
  }, [kindProp, namespace, name, initialTab])

  const switchView = useCallback((yaml: boolean) => {
    // startViewTransitionSafe handles the API-missing fallback AND
    // swallows the InvalidStateError that the API rejects with when
    // a new transition supersedes an in-flight one (rapid clicks).
    startViewTransitionSafe(() => flushSync(() => setShowYaml(yaml)))
  }, [])

  const [selectedEventId, setSelectedEventId] = useState<string | null>(null)
  const [zoom, setZoom] = useState<ZoomLevel>(1)
  const [selectedPod, setSelectedPod] = useState<string | null>(null)
  const [initialContainer, setInitialContainer] = useState<string | null>(null)
  const [copied, setCopied] = useState<string | null>(null)
  const [saveSuccess, setSaveSuccess] = useState(false)

  // Refresh animation
  const [refetch, isRefreshAnimating, refreshPhase] = useRefreshAnimation(refetchProp ?? (() => {}))

  // Build resource hierarchy
  const resourceLanes = useMemo(() => {
    if (!allEvents) return []
    return buildResourceHierarchy({
      events: allEvents,
      topology,
      rootResource: { kind, namespace, name },
      groupByApp: true,
    })
  }, [allEvents, topology, kind, namespace, name])

  // Topology tab — the seeded neighborhood around this one workload (its
  // ownership core + attached Services/config/policies), not the whole namespace.
  const neighborhoodSeed = useMemo(() => [{ kind, namespace, name }], [kind, namespace, name])
  const neighborhood = useMemo(
    () => (topology ? neighborhoodFor(topology, neighborhoodSeed) : null),
    [topology, neighborhoodSeed],
  )
  const neighborhoodFocusId = useMemo(
    () => (topology ? seedNodeIds(topology, neighborhoodSeed)[0] : undefined),
    [topology, neighborhoodSeed],
  )

  // The Topology tab stays visible while topology is loading (the pane shows a
  // loader) and hides only when topology arrived and nothing matched the seed.
  // A deep-linked ?tab=topology that turns out unavailable falls back to
  // overview instead of rendering an empty body under a hidden tab.
  const topologyTabHidden = !!topology && (!neighborhood || neighborhood.nodes.length === 0)
  const effectiveTab: TabType = activeTab === 'topology' && topologyTabHidden ? 'overview' : activeTab

  // YAML tab object rail — the same neighborhood, as a manifest list: the
  // workload first, then routing → config → policy/scaling → ownership.
  const yamlObjects = useMemo(() => {
    if (!neighborhood) return []
    const order: Record<string, number> = {
      Service: 1, Ingress: 1, HTTPRoute: 1,
      ConfigMap: 2, Secret: 2,
      HorizontalPodAutoscaler: 3, PodDisruptionBudget: 3, NetworkPolicy: 3,
      ReplicaSet: 4, Pod: 5,
    }
    return neighborhood.nodes
      .filter((n) => n.kind !== 'Internet' && n.kind !== 'PodGroup')
      .map((n) => ({
        id: n.id,
        kind: n.kind as string,
        namespace: (n.data?.namespace as string) || namespace,
        name: n.name,
        group: apiVersionToGroup(n.data?.apiVersion as string | undefined),
        primary: n.id === neighborhoodFocusId,
      }))
      .sort((a, b) =>
        a.primary !== b.primary
          ? (a.primary ? -1 : 1)
          : (order[a.kind] ?? 9) - (order[b.kind] ?? 9) || a.kind.localeCompare(b.kind) || a.name.localeCompare(b.name),
      )
  }, [neighborhood, neighborhoodFocusId, namespace])
  // null = the workload's own manifest (the editable one).
  const [yamlObjectId, setYamlObjectId] = useState<string | null>(null)
  useEffect(() => setYamlObjectId(null), [kind, namespace, name])
  const yamlObject = yamlObjectId ? yamlObjects.find((o) => o.id === yamlObjectId) : undefined
  const handleTopologyNodeClick = useCallback(
    (node: TopologyNode) => {
      if (!onNavigateToResource || !node.kind || !node.name) return
      onNavigateToResource({
        kind: kindToPlural(node.kind),
        namespace: (node.data?.namespace as string) || '',
        name: node.name,
        group: apiVersionToGroup(node.data?.apiVersion as string | undefined),
      })
    },
    [onNavigateToResource],
  )

  // Flatten events from hierarchy
  const resourceEvents = useMemo(() => {
    return getAllEventsFromHierarchy(resourceLanes)
  }, [resourceLanes])
  const overviewEvents = resourceEvents.length > 0 ? resourceEvents : (resourceFocusedK8sEvents ?? [])
  const overviewEventsLoading = resourceEvents.length > 0 ? eventsLoading : resourceFocusedEventsLoading
  const overviewEventsError = resourceEvents.length > 0 ? undefined : resourceFocusedK8sError

  // Get pods from relationships and hierarchy
  const childPods = useMemo(() => {
    if (resourceLanes.length === 0) return []
    const rootLane = resourceLanes[0]
    const pods: { name: string; namespace: string; events: TimelineEvent[] }[] = []
    const collectPods = (lane: ResourceLane) => {
      if (lane.kind === 'Pod') {
        pods.push({ name: lane.name, namespace: lane.namespace, events: lane.events })
      }
      lane.children?.forEach(collectPods)
    }
    rootLane.children?.forEach(collectPods)
    if (rootLane.kind === 'Pod') {
      pods.push({ name: rootLane.name, namespace: rootLane.namespace, events: rootLane.events })
    }
    return pods
  }, [resourceLanes])

  const pods = relationships?.pods || []
  const allPods: ResourceRef[] = useMemo(() => {
    const combined = [
      ...pods,
      ...childPods.map(p => ({ kind: 'Pod' as const, namespace: p.namespace, name: p.name })),
    ]
    const seen = new Set<string>()
    return combined.filter(p => {
      const key = `${p.namespace}/${p.name}`
      if (seen.has(key)) return false
      seen.add(key)
      return true
    })
  }, [pods, childPods])

  // Metadata
  const metadata = useMemo(() => extractMetadata(kind, resource), [kind, resource])
  const relationshipGitOpsOwner = useMemo(() => gitOpsOwnerFromRelationships(relationships), [relationships])
  const gitopsOwner = resolvedGitOpsOwner ?? relationshipGitOpsOwner
  // When the resource itself is a portal GitOps CR (Application, Kustomization,
  // HelmRelease, etc.), surface a link to its dedicated GitOps detail page —
  // the drawer's renderer is thorough but the tab has the tree + insights +
  // operations the drawer can't reproduce inline.
  const gitOpsResourcePath = useMemo(() => gitOpsRouteForResource(resource), [resource])

  // Copy to clipboard
  const copyToClipboard = useCallback((text: string, key: string) => {
    navigator.clipboard.writeText(text)
    setCopied(key)
    setTimeout(() => setCopied(null), 2000)
  }, [])

  const handleSaveSecretValue = useCallback(async (yaml: string) => {
    if (!onUpdateResource) return
    try {
      await onUpdateResource({
        kind: apiKind,
        namespace,
        name,
        yaml,
      })
      setTimeout(() => refetch(), 1000)
    } catch {
      // Error handled by mutation (toast)
    }
  }, [onUpdateResource, apiKind, namespace, name, refetch])

  const handleSaved = useCallback(() => {
    setSaveSuccess(true)
    setTimeout(() => {
      refetch()
      setTimeout(() => setSaveSuccess(false), 2000)
    }, 1000)
  }, [refetch])

  // Handle "open logs" from container-level buttons (e.g., PodRenderer) — switch to Logs tab with right pod+container
  const handleOpenLogs = useCallback((podName: string, containerName: string) => {
    setSelectedPod(podName)
    setInitialContainer(containerName)
    handleSetTab('logs')
  }, [handleSetTab])

  // Selected resource object for shared components
  const selectedResource: SelectedResource = useMemo(() => ({
    kind: apiKind,
    namespace,
    name,
    group,
  }), [apiKind, namespace, name, group])

  // Keyboard shortcuts — different behavior for expanded vs collapsed mode
  useRegisterShortcuts(useMemo(() => [
    {
      id: 'workload-escape',
      keys: 'Escape',
      description: expanded ? 'Go back' : 'Close drawer',
      category: expanded ? 'Navigation' as const : 'Drawer' as const,
      scope: expanded ? 'global' as const : 'drawer' as const,
      handler: expanded ? onBack : () => onClose?.(),
      enabled: true,
    },
    {
      id: 'drawer-yaml',
      keys: 'y',
      description: 'Switch to YAML view',
      category: 'Drawer' as const,
      scope: 'drawer' as const,
      handler: () => switchView(true),
      enabled: !expanded,
    },
    {
      id: 'drawer-detail',
      keys: 'e',
      description: 'Switch to detail view',
      category: 'Drawer' as const,
      scope: 'drawer' as const,
      handler: () => switchView(false),
      enabled: !expanded,
    },
  ], [expanded, onBack, onClose, switchView]))

  const status = getResourceStatus(apiKind, resource)

  const showMetricsTab = isMetricsAvailable ? isMetricsAvailable(kind, resource) : false
  const tabs: DetailShellTab<TabType>[] = [
    { id: 'overview', label: 'Overview', icon: <Layers className="w-4 h-4" /> },
    { id: 'topology', label: 'Topology', icon: <Network className="w-4 h-4" />, hidden: topologyTabHidden },
    {
      id: 'timeline',
      label: 'Timeline',
      icon: <Activity className="w-4 h-4" />,
      badge: resourceEvents.length > 0 ? <span className="ml-1 badge-sm bg-theme-elevated">{resourceEvents.length}</span> : undefined,
    },
    { id: 'logs', label: 'Logs', icon: <Terminal className="w-4 h-4" />, hidden: !(allPods.length > 0 && renderLogsTab) },
    { id: 'metrics', label: 'Metrics', icon: <BarChart3 className="w-4 h-4" />, hidden: !(showMetricsTab && renderMetricsTab) },
    { id: 'yaml', label: 'YAML', icon: <FileText className="w-4 h-4" /> },
  ]

  // ── Collapsed (drawer) mode ──────────────────────────────────────────────
  if (!expanded) {
    return (
      <div className="flex flex-col h-full w-full">
        {/* Drawer header */}
        <div className="border-b border-theme-border shrink-0">
          {/* Top row: badges and controls */}
          <div className="flex items-center justify-between px-4 pt-3 pb-2">
            <div className="flex items-center gap-2 flex-wrap">
              <span className={clsx('badge', getKindColorOutline(apiKind))}>
                {displayKindName(apiKind, resource?.kind)}
              </span>
              {status && (
                <span className={clsx('badge', status.color)}>
                  {status.text}
                </span>
              )}
            </div>
            <div className="flex items-center gap-1">
              {onExpand && (
                <button
                  onClick={onExpand}
                  className="p-1.5 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
                  title="Open full view"
                >
                  <Maximize2 className="w-4 h-4" />
                </button>
              )}
              <button
                onClick={() => refetch()}
                disabled={isRefreshAnimating}
                className={clsx(
                  'p-1.5 hover:bg-theme-elevated rounded disabled:opacity-50 transition-colors duration-500',
                  refreshPhase === 'success' ? 'text-emerald-400' : 'text-theme-text-secondary hover:text-theme-text-primary'
                )}
                title="Refresh"
              >
                {refreshPhase === 'success'
                  ? <Check className="w-4 h-4 stroke-[2.5]" />
                  : <RefreshCw className={clsx('w-4 h-4', refreshPhase === 'spinning' && 'animate-spin')} />
                }
              </button>
              {onClose && (
                <button onClick={onClose} className="p-1.5 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded" title="Close (Esc)">
                  <X className="w-4 h-4" />
                </button>
              )}
            </div>
          </div>

          {/* Name and namespace */}
          <div className="px-4 pb-3">
            <div className="flex items-center gap-2">
              <h2 className="text-lg font-semibold text-theme-text-primary truncate">{name}</h2>
              <button
                onClick={() => copyToClipboard(name, 'name')}
                className="p-1 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded shrink-0"
                title="Copy name"
              >
                {copied === 'name' ? <Check className="w-3.5 h-3.5 text-green-400" /> : <Copy className="w-3.5 h-3.5" />}
              </button>
            </div>
            <p className="text-sm text-theme-text-tertiary">{namespace}</p>
            {(gitopsOwner || helmOwner || (gitOpsResourcePath && onNavigateGitOpsPath)) && (
              <div className="mt-1 flex flex-wrap items-center gap-1.5">
                {gitopsOwner && <ManagedByChip owner={gitopsOwner} status={gitOpsOwnerStatus} verified={gitOpsOwnerVerified} pending={gitOpsOwnerPending} source={gitOpsOwnerSource} onOpen={onOpenGitOpsResource} />}
                {helmOwner && <HelmManagedByChip owner={helmOwner} source={helmOwnerSource} onOpen={onOpenHelmRelease} />}
                {gitOpsResourcePath && onNavigateGitOpsPath && (
                  <OpenInGitOpsChip onClick={() => onNavigateGitOpsPath(gitOpsResourcePath)} />
                )}
              </div>
            )}
          </div>

          {/* Actions bar */}
          <ResourceActionsBar resource={selectedResource} data={resource} onClose={onClose} showYaml={showYaml} onToggleYaml={() => switchView(!showYaml)} {...actionsBarProps} />
        </div>

        {/* Success animation overlay */}
        {saveSuccess && <SaveSuccessAnimation />}

        {/* Content — viewTransitionName scopes View Transitions API cross-fade to this element */}
        <div className="flex-1 overflow-y-auto" style={{ viewTransitionName: 'drawer-content' }}>
          {!resource ? (
            // Fill the drawer body so the loading logo centers in it, not in a
            // 128px box pinned to the top (matches the splash/PaneLoader centering).
            <FetchResult loading={resourceLoading} error={resourceError} className="h-full" />
          ) : showYaml ? (
            <EditableYamlView
              resource={selectedResource}
              data={resource}
              onCopy={(text) => copyToClipboard(text, 'yaml')}
              copied={copied === 'yaml'}
              readOnly={readOnlyYaml}
              onSaved={handleSaved}
              onSave={onUpdateResource}
              isSaving={isUpdatingResource}
              saveError={updateResourceError}
              onDuplicate={onDuplicate}
              onDownload={onDownload}
            />
          ) : (
            <OperationalIssuesShownContext.Provider value={!!hasOperationalIssues}>
              {renderOverviewLead && hasOperationalIssues && (
                <div className="px-4 pt-4">
                  {renderOverviewLead({ kind, namespace, name })}
                </div>
              )}
              <ResourceRendererDispatch
                resource={selectedResource}
                data={resource}
                relationships={relationships}
                certificateInfo={certificateInfo}
                hpaDiagnosis={hpaDiagnosis}
                scalerDiagnostics={scalerDiagnostics}
                onCopy={copyToClipboard}
                copied={copied}
                onNavigate={onNavigateToResource ? (ref) => onNavigateToResource(refToSelectedResource(ref)) : undefined}
                onSaveSecretValue={canUpdateSecrets ? handleSaveSecretValue : undefined}
                isSavingSecret={isUpdatingResource}
                rendererOverrides={rendererOverrides}
                resolvedEnvFrom={resolvedEnvFrom}
                renderMetrics={renderMetricsTab}
                events={resourceFocusedK8sEvents}
                eventsLoading={resourceFocusedEventsLoading}
                updates={resourceFocusedUpdates}
                eventsError={resourceFocusedK8sError}
                updatesError={resourceFocusedUpdatesError}
              />
              {renderOverviewExtra && (
                <div className="px-4 pb-4">
                  {renderOverviewExtra({ kind, namespace, name })}
                </div>
              )}
            </OperationalIssuesShownContext.Provider>
          )}
        </div>
      </div>
    )
  }

  // ── Expanded (full) mode ─────────────────────────────────────────────────
  return (
    <OperationalIssuesShownContext.Provider value={!!hasOperationalIssues}>
    <DetailShell
      breadcrumb={breadcrumb}
      nav={
        breadcrumb || hideBackButton ? undefined : (
          <button
            onClick={onBack}
            className="p-1.5 mt-0.5 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded-lg transition-colors"
            title="Go back (Esc)"
          >
            <ArrowLeft className="w-5 h-5" />
          </button>
        )
      }
      identity={
        <>
          <div className="flex items-center gap-3 mb-1">
            <h1 className="text-lg font-semibold text-theme-text-primary truncate">{name}</h1>
            <button
              onClick={() => copyToClipboard(name, 'name')}
              className="p-1 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded shrink-0"
              title="Copy name"
            >
              {copied === 'name' ? <Check className="w-3.5 h-3.5 text-green-400" /> : <Copy className="w-3.5 h-3.5" />}
            </button>
          </div>
          <div className="flex items-center gap-3 text-sm text-theme-text-secondary">
            <span className={clsx('badge', getKindColorOutline(apiKind))}>
              {displayKindName(apiKind, resource?.kind)}
            </span>
            {status && (
              <span className={clsx('badge', status.color)}>
                {status.text}
              </span>
            )}
            {namespace && namespace !== '_' && (
              <span>Namespace: <span className="text-theme-text-primary">{namespace}</span></span>
            )}
            {metadata.find(m => m.label === 'Image') && (
              <span className="truncate max-w-md font-mono text-xs">{metadata.find(m => m.label === 'Image')?.value}</span>
            )}
            {gitopsOwner && (
              <ManagedByChip owner={gitopsOwner} status={gitOpsOwnerStatus} verified={gitOpsOwnerVerified} pending={gitOpsOwnerPending} source={gitOpsOwnerSource} onOpen={onOpenGitOpsResource} variant="block" />
            )}
            {helmOwner && (
              <HelmManagedByChip owner={helmOwner} source={helmOwnerSource} onOpen={onOpenHelmRelease} variant="block" />
            )}
            {gitOpsResourcePath && onNavigateGitOpsPath && (
              <OpenInGitOpsChip onClick={() => onNavigateGitOpsPath(gitOpsResourcePath)} />
            )}
            {relationships?.owner && (
              <span>Owner: <button onClick={() => onNavigateToResource?.(refToSelectedResource(relationships.owner!))} className="text-blue-500 hover:underline">{relationships.owner.name}</button></span>
            )}
          </div>
        </>
      }
      headerActions={
        <>
          <button
            onClick={() => refetch()}
            disabled={isRefreshAnimating}
            className={clsx(
              'p-1.5 mt-0.5 hover:bg-theme-elevated rounded disabled:opacity-50 transition-colors duration-500',
              refreshPhase === 'success' ? 'text-emerald-400' : 'text-theme-text-secondary hover:text-theme-text-primary'
            )}
            title="Refresh"
          >
            {refreshPhase === 'success'
              ? <Check className="w-5 h-5 stroke-[2.5]" />
              : <RefreshCw className={clsx('w-5 h-5', refreshPhase === 'spinning' && 'animate-spin')} />
            }
          </button>
          {onCollapseToDrawer && (
            <button
              onClick={onCollapseToDrawer}
              className="p-1.5 mt-0.5 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded-lg transition-colors"
              title="Collapse to drawer"
            >
              <Minimize2 className="w-5 h-5" />
            </button>
          )}
        </>
      }
      tabs={tabs}
      activeTab={effectiveTab}
      onTabChange={handleSetTab}
      scopeControls={scopeControls}
      tabStripEnd={<ResourceActionsBar resource={selectedResource} data={resource} hideLogs {...actionsBarProps} />}
      overlay={saveSuccess ? <SaveSuccessAnimation /> : null}
      compactHeader={compactHeader}
    >
        {effectiveTab === 'overview' && (
            <InfoTab
              resource={resource}
              selectedResource={selectedResource}
              relationships={relationships}
              hpaDiagnosis={hpaDiagnosis}
              scalerDiagnostics={scalerDiagnostics}
              isLoading={resourceLoading}
              error={resourceError}
              onNavigate={onNavigateToResource}
              onCopy={copyToClipboard}
              copied={copied}
              onSaveSecretValue={canUpdateSecrets ? handleSaveSecretValue : undefined}
              isSavingSecret={isUpdatingResource}
              onOpenLogs={handleOpenLogs}
              onSwitchToTimeline={() => handleSetTab('timeline')}
              rendererOverrides={rendererOverrides}
              resolvedEnvFrom={resolvedEnvFrom}
              events={overviewEvents}
              eventsLoading={overviewEventsLoading}
              updates={resourceFocusedUpdates}
              eventsError={overviewEventsError}
              updatesError={resourceFocusedUpdatesError}
              extraContent={renderOverviewExtra && renderOverviewExtra({ kind, namespace, name })}
              leadContent={hasOperationalIssues && renderOverviewLead ? renderOverviewLead({ kind, namespace, name }) : undefined}
            />
        )}
        {effectiveTab === 'topology' && (
          <div className="relative h-full min-h-0 w-full">
            {topology ? (
              <TopologyGraph
                topology={neighborhood}
                viewMode="resources"
                groupingMode="namespace"
                hideGroupHeader
                onNodeClick={handleTopologyNodeClick}
                showExportButton={false}
                focusNodeId={neighborhoodFocusId}
              />
            ) : (
              <PaneLoader label="Loading topology…" className="absolute inset-0" />
            )}
          </div>
        )}
        {effectiveTab === 'timeline' && (
          <EventsTab
            events={resourceEvents}
            resourceLanes={resourceLanes}
            isLoading={eventsLoading}
            zoom={zoom}
            onZoomChange={setZoom}
            resourceKind={kind}
            resourceName={name}
            selectedEventId={selectedEventId}
            onSelectEvent={setSelectedEventId}
          />
        )}
        {effectiveTab === 'logs' && renderLogsTab && (
          renderLogsTab({
            kind,
            apiKind,
            namespace,
            name,
            resource,
            pods: allPods,
            selectedPod,
            onSelectPod: setSelectedPod,
            initialContainer,
            onConsumeInitialContainer: () => setInitialContainer(null),
          })
        )}
        {effectiveTab === 'metrics' && renderMetricsTab && (
          <div className="h-full overflow-auto p-4">
            {renderMetricsTab({ kind: resource?.kind || kind, namespace, name })}
          </div>
        )}
        {effectiveTab === 'yaml' && (
          <div className="flex h-full min-h-0">
            {renderRelatedYaml && yamlObjects.length > 1 && (
              <div className="flex w-56 shrink-0 flex-col gap-0.5 overflow-y-auto border-r border-theme-border bg-theme-base px-2 py-2">
                <div className="px-1.5 pb-1 pt-0.5 text-[10px] font-medium uppercase tracking-wide text-theme-text-tertiary">Objects</div>
                {yamlObjects.map((o) => {
                  const active = o.primary ? yamlObjectId === null : yamlObjectId === o.id
                  return (
                    <button
                      key={o.id}
                      type="button"
                      onClick={() => setYamlObjectId(o.primary ? null : o.id)}
                      className={clsx(
                        'flex w-full flex-col rounded-md px-1.5 py-1.5 text-left transition-colors',
                        active ? 'selection selection-ring' : 'hover:bg-theme-hover',
                      )}
                    >
                      <span className="truncate text-xs font-medium text-theme-text-primary">{midTruncate(o.name, 26)}</span>
                      <span className="text-[10px] uppercase tracking-wide text-theme-text-tertiary">{o.kind}</span>
                    </button>
                  )
                })}
              </div>
            )}
            <div className="h-full min-w-0 flex-1 overflow-auto">
              {yamlObject && !yamlObject.primary && renderRelatedYaml ? (
                renderRelatedYaml(yamlObject)
              ) : !resource ? (
                <FetchResult loading={resourceLoading} error={resourceError} className="h-full" />
              ) : (
                <EditableYamlView
                  resource={selectedResource}
                  data={resource}
                  onCopy={(text) => copyToClipboard(text, 'yaml')}
                  copied={copied === 'yaml'}
                  readOnly={readOnlyYaml}
                  onSaved={handleSaved}
                  onSave={onUpdateResource}
                  isSaving={isUpdatingResource}
                  saveError={updateResourceError}
                  onDuplicate={onDuplicate}
                  onDownload={onDownload}
                />
              )}
            </div>
          </div>
        )}
    </DetailShell>
    </OperationalIssuesShownContext.Provider>
  )
}

// ============================================================================
// HELPER FUNCTIONS
// ============================================================================

function extractMetadata(kind: string, resource: any): { label: string; value: string }[] {
  if (!resource) return []
  const items: { label: string; value: string }[] = []
  const spec = resource.spec || {}
  const status = resource.status || {}

  switch (kind) {
    case 'Deployment':
    case 'StatefulSet':
    case 'Rollout': {
      const containers = spec.template?.spec?.containers || []
      if (containers[0]?.image) items.push({ label: 'Image', value: containers[0].image })
      break
    }
    case 'DaemonSet': {
      const dsContainers = spec.template?.spec?.containers || []
      if (dsContainers[0]?.image) items.push({ label: 'Image', value: dsContainers[0].image })
      break
    }
    case 'Pod':
      if (status.phase) items.push({ label: 'Phase', value: status.phase })
      if (status.podIP) items.push({ label: 'Pod IP', value: status.podIP })
      break
    case 'CronJob':
      if (spec.schedule) items.push({ label: 'Schedule', value: spec.schedule })
      break
    case 'Job':
      if (status.succeeded !== undefined) items.push({ label: 'Succeeded', value: String(status.succeeded) })
      break
  }
  return items
}

// ============================================================================
// SUB-COMPONENTS
// ============================================================================

function OpenInGitOpsChip({ onClick }: { onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      title="Open this resource in the GitOps tab (tree + insights + ops)"
      className="inline-flex items-center gap-1 rounded border border-skyhook-500/40 bg-skyhook-500/10 px-1.5 py-0.5 text-[11px] font-medium text-skyhook-500 hover:bg-skyhook-500/20 transition-colors"
    >
      Open in GitOps
      <ArrowRight className="h-3 w-3 shrink-0" />
    </button>
  )
}

// ============================================================================
// EVENTS TAB (Swimlane timeline)
// ============================================================================

function EventsTab({
  events,
  resourceLanes,
  isLoading,
  zoom,
  onZoomChange,
  resourceKind,
  resourceName,
  selectedEventId,
  onSelectEvent,
}: {
  events: TimelineEvent[]
  resourceLanes: ResourceLane[]
  isLoading: boolean
  zoom: ZoomLevel
  onZoomChange: (zoom: ZoomLevel) => void
  resourceKind: string
  resourceName: string
  selectedEventId: string | null
  onSelectEvent: (id: string | null) => void
}) {
  const rowRefs = useRef<Map<number, HTMLTableRowElement>>(new Map())
  const tableContainerRef = useRef<HTMLDivElement>(null)
  const [hoveredEventId, setHoveredEventId] = useState<string | null>(null)
  const [visibleRowRange, setVisibleRowRange] = useState<{ first: number; last: number } | null>(null)

  // Scroll to selected event
  useEffect(() => {
    if (selectedEventId) {
      const eventIndex = events.findIndex(e => e.id === selectedEventId)
      if (eventIndex >= 0) {
        const row = rowRefs.current.get(eventIndex)
        if (row) row.scrollIntoView({ behavior: 'smooth', block: 'center' })
      }
    }
  }, [selectedEventId, events])

  // Track visible rows via IntersectionObserver
  useEffect(() => {
    if (!tableContainerRef.current || events.length === 0) return
    const visibleIndices = new Set<number>()
    const observer = new IntersectionObserver(
      (entries) => {
        for (const entry of entries) {
          const idx = parseInt(entry.target.getAttribute('data-row-index') || '-1', 10)
          if (idx >= 0) {
            if (entry.isIntersecting) visibleIndices.add(idx)
            else visibleIndices.delete(idx)
          }
        }
        if (visibleIndices.size > 0) {
          const indices = Array.from(visibleIndices)
          setVisibleRowRange({ first: Math.min(...indices), last: Math.max(...indices) })
        } else {
          setVisibleRowRange(null)
        }
      },
      { root: tableContainerRef.current, threshold: 0.1 }
    )
    const timeoutId = setTimeout(() => {
      rowRefs.current.forEach((row) => observer.observe(row))
    }, 100)
    return () => { clearTimeout(timeoutId); observer.disconnect() }
  }, [events])

  // Visible time range from visible rows
  const visibleTimeRangeFromRows = useMemo(() => {
    if (!visibleRowRange || events.length === 0) return null
    const visibleEvents = events.slice(visibleRowRange.first, visibleRowRange.last + 1)
    if (visibleEvents.length === 0) return null
    const timestamps = visibleEvents.map(e => new Date(e.timestamp).getTime())
    const start = Math.min(...timestamps)
    const end = Math.max(...timestamps)
    const timeSpan = end - start
    const padding = Math.max(timeSpan * 0.1, 60000)
    return { start: start - padding, end: end + padding }
  }, [events, visibleRowRange])

  const now = Date.now()
  const { start: startTime, windowMs } = calculateTimeRange(zoom, now)
  const zoomIndex = ZOOM_LEVELS.indexOf(zoom)
  const canZoomIn = zoomIndex > 0
  const canZoomOut = zoomIndex < ZOOM_LEVELS.length - 1
  const handleZoomIn = () => { if (canZoomIn) onZoomChange(ZOOM_LEVELS[zoomIndex - 1]) }
  const handleZoomOut = () => { if (canZoomOut) onZoomChange(ZOOM_LEVELS[zoomIndex + 1]) }
  const localTimeToX = (ts: number) => timeToX(ts, startTime, windowMs)

  // Build swimlanes
  const swimlanes = useMemo(() => {
    type SwimLane = {
      id: string; label: string
      spans: { start: number; end: number; health: string }[]
      events: TimelineEvent[]
      createdAt?: number; createdBeforeWindow: boolean
    }

    if (resourceLanes.length === 0) {
      const mainResourceEvents = events.filter(e => e.kind === resourceKind && e.name === resourceName)
      const healthResult = buildHealthSpans(mainResourceEvents.filter(e => isChangeEvent(e)), startTime, now, mainResourceEvents)
      return [{ id: 'main', label: `${resourceKind}: ${resourceName}`, spans: healthResult.spans, events: mainResourceEvents, createdAt: healthResult.createdAt, createdBeforeWindow: healthResult.createdBeforeWindow }]
    }

    const rootLane = resourceLanes[0]
    const lanes: SwimLane[] = []

    const rootHealthResult = buildHealthSpans(rootLane.events.filter(e => isChangeEvent(e)), startTime, now, rootLane.events)
    lanes.push({
      id: rootLane.id,
      label: `${rootLane.kind}: ${rootLane.name.length > 40 ? rootLane.name.slice(0, 20) + '...' + rootLane.name.slice(-17) : rootLane.name}`,
      spans: rootHealthResult.spans, events: rootLane.events,
      createdAt: rootHealthResult.createdAt, createdBeforeWindow: rootHealthResult.createdBeforeWindow,
    })

    const flattenChildren = (lane: ResourceLane): ResourceLane[] => {
      const children = lane.children || []
      return children.flatMap(child => [child, ...flattenChildren(child)])
    }
    const allChildren = flattenChildren(rootLane)

    const kindPriority: Record<string, number> = {
      Service: 1, Deployment: 2, Rollout: 2, StatefulSet: 2, DaemonSet: 2,
      ReplicaSet: 3, ConfigMap: 4, Secret: 4, Gateway: 5, HTTPRoute: 4,
      GRPCRoute: 4, TCPRoute: 4, TLSRoute: 4, Ingress: 5, Pod: 6,
    }
    allChildren.sort((a, b) => {
      const aPriority = kindPriority[a.kind] || 10
      const bPriority = kindPriority[b.kind] || 10
      if (aPriority !== bPriority) return aPriority - bPriority
      return b.events.length - a.events.length
    })

    for (const child of allChildren.slice(0, 6)) {
      const childHealthResult = buildHealthSpans(child.events.filter(e => isChangeEvent(e)), startTime, now, child.events)
      lanes.push({
        id: child.id,
        label: `${child.kind}: ${child.name.length > 40 ? child.name.slice(0, 20) + '...' + child.name.slice(-17) : child.name}`,
        spans: childHealthResult.spans, events: child.events,
        createdAt: childHealthResult.createdAt, createdBeforeWindow: childHealthResult.createdBeforeWindow,
      })
    }

    return lanes
  }, [resourceLanes, events, resourceKind, resourceName, startTime, now])

  // Time axis ticks
  const tickCount = 8
  const ticks = Array.from({ length: tickCount + 1 }, (_, i) => {
    const t = startTime + (windowMs * i) / tickCount
    return { time: t, label: formatAxisTime(new Date(t)) }
  })

  const formatTimeRangeDisplay = () => {
    const start = new Date(startTime)
    const end = new Date(now)
    return `${start.toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })} → ${end.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}`
  }

  const nowX = localTimeToX(now)

  if (isLoading) {
    return (
      <div className="flex items-center justify-center h-full text-theme-text-tertiary">
        <RefreshCw className="w-5 h-5 animate-spin mr-2" />
        Loading events…
      </div>
    )
  }

  return (
    <div className="h-full flex flex-col overflow-hidden">
      {/* Timeline toolbar */}
      <div className="shrink-0 px-4 py-2 border-b border-theme-border bg-theme-surface/50 flex items-center justify-between">
        <span className="text-sm font-medium text-theme-text-secondary">Events ({events.length})</span>
        <div className="flex items-center gap-3">
          <ZoomControls zoom={zoom} onZoomIn={handleZoomIn} onZoomOut={handleZoomOut} canZoomIn={canZoomIn} canZoomOut={canZoomOut} />
          <span className="text-xs text-theme-text-tertiary">{formatTimeRangeDisplay()}</span>
        </div>
      </div>

      {/* Legend */}
      <div className="shrink-0 px-4 py-1.5 border-b border-theme-border bg-theme-surface/30 flex items-center justify-between">
        <HealthSpanLegend />
        <EventDotLegend />
      </div>

      {/* Swimlane Timeline */}
      <div className="shrink-0 border-b border-theme-border bg-theme-base relative">
        {/* Scrollable swimlane area — max 4 lanes visible before scrolling */}
        <div className="max-h-[140px] overflow-y-auto relative">
          {nowX >= 0 && nowX <= 100 && (
            <div className="absolute top-0 bottom-0 w-0.5 bg-purple-500/50 z-20 pointer-events-none" style={{ left: `calc(280px + (100% - 280px) * ${nowX / 100})` }}>
              <span className="absolute -top-4 left-1/2 -translate-x-1/2 text-xs text-purple-500 font-medium whitespace-nowrap">now</span>
            </div>
          )}

          {swimlanes.map((lane) => (
            <div key={lane.id} className="flex border-b border-theme-border/50 last:border-b-0">
              <div className="w-[280px] shrink-0 px-3 py-1 bg-theme-surface/50 border-r border-theme-border text-xs font-medium text-theme-text-secondary truncate flex items-center">
                {lane.label}
              </div>
              <div className="flex-1 relative h-7 bg-theme-base">
                {visibleTimeRangeFromRows && (
                  <div className="absolute top-0 bottom-0 bg-blue-500/10 border-x border-blue-500/30 pointer-events-none" style={{
                    left: `${Math.max(0, localTimeToX(visibleTimeRangeFromRows.start))}%`,
                    width: `${Math.max(2, Math.min(100, localTimeToX(visibleTimeRangeFromRows.end)) - Math.max(0, localTimeToX(visibleTimeRangeFromRows.start)))}%`,
                  }} />
                )}
                {lane.spans.map((span, i) => {
                  const left = Math.max(0, localTimeToX(span.start))
                  const right = Math.min(100, localTimeToX(span.end))
                  const width = right - left
                  const showCreatedBefore = i === 0 && lane.createdBeforeWindow && lane.createdAt
                  return (
                    <HealthSpan
                      key={i}
                      health={span.health}
                      left={left}
                      width={width}
                      title={`${span.health} (${new Date(span.start).toLocaleTimeString()} - ${new Date(span.end).toLocaleTimeString()})`}
                      createdBefore={showCreatedBefore ? new Date(lane.createdAt!) : undefined}
                    />
                  )
                })}
                {lane.events.map((evt, i) => {
                  const x = localTimeToX(new Date(evt.timestamp).getTime())
                  if (x < 0 || x > 100) return null
                  return (
                    <EventMarker
                      key={`${evt.id}-${i}`}
                      event={evt}
                      x={x}
                      selected={selectedEventId === evt.id}
                      onClick={() => onSelectEvent(selectedEventId === evt.id ? null : evt.id)}
                      small
                    />
                  )
                })}
              </div>
            </div>
          ))}
        </div>

        {/* Time axis */}
        <div className="flex">
          <div className="w-[280px] shrink-0 bg-theme-surface/50 border-r border-theme-border" />
          <div className="flex-1 relative h-5 bg-theme-elevated/30">
            {ticks.map((tick, i) => {
              const x = localTimeToX(tick.time)
              return (
                <div key={i} className="absolute top-0 flex flex-col items-center" style={{ left: `${x}%`, transform: 'translateX(-50%)' }}>
                  <div className="h-1.5 w-px bg-theme-border" />
                  <span className="text-[10px] text-theme-text-tertiary">{tick.label}</span>
                </div>
              )
            })}
          </div>
        </div>
      </div>

      {/* Events table */}
      <div ref={tableContainerRef} className="flex-1 overflow-auto">
        <table className="w-full text-sm">
          <thead className="sticky top-0 bg-theme-surface border-b border-theme-border z-10">
            <tr className="text-left text-xs text-theme-text-tertiary">
              <th className="px-4 py-2 font-medium w-32">Event Type</th>
              <th className="px-4 py-2 font-medium">Summary</th>
              <th className="px-4 py-2 font-medium w-40">Time</th>
              <th className="px-4 py-2 font-medium w-32">Resource</th>
              <th className="px-4 py-2 font-medium w-24">Status</th>
            </tr>
          </thead>
          <tbody className="table-divide-subtle">
            {events.length === 0 ? (
              <tr>
                <td colSpan={5} className="px-4 py-8 text-center text-theme-text-tertiary">
                  No events in this time range
                </td>
              </tr>
            ) : (
              events.map((evt, evtIdx) => {
                const isSelected = selectedEventId === evt.id
                const isHovered = hoveredEventId === evt.id
                const isWarning = isProblematicEvent(evt)
                return (
                  <tr
                    key={`${evt.id}-${evtIdx}`}
                    ref={(el) => { if (el) rowRefs.current.set(evtIdx, el); else rowRefs.current.delete(evtIdx) }}
                    data-row-index={evtIdx}
                    onClick={() => onSelectEvent(isSelected ? null : evt.id)}
                    onMouseEnter={() => setHoveredEventId(evt.id)}
                    onMouseLeave={() => setHoveredEventId(null)}
                    className={clsx(
                      'cursor-pointer transition-colors',
                      isSelected ? 'bg-blue-500/10' : isHovered ? 'bg-blue-500/5' : 'hover:bg-theme-surface/50'
                    )}
                  >
                    <td className="px-4 py-3">
                      <div className="flex items-center gap-2">
                        <EventDot event={evt} />
                        <span className={clsx('font-medium', isWarning ? 'text-amber-500' : 'text-theme-text-primary')}>
                          {isHistoricalEvent(evt) && evt.reason ? evt.reason : isChangeEvent(evt) ? evt.eventType : evt.reason}
                        </span>
                      </div>
                    </td>
                    <td className="px-4 py-3 text-theme-text-secondary">
                      {evt.message || evt.diff?.summary || '-'}
                    </td>
                    <td className="px-4 py-3 text-theme-text-tertiary">
                      {new Date(evt.timestamp).toLocaleString([], { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit', second: '2-digit' })}
                    </td>
                    <td className="px-4 py-3">
                      <span className={clsx('badge-sm', getKindBadgeColor(evt.kind))}>
                        {evt.kind}
                      </span>
                    </td>
                    <td className="px-4 py-3">
                      {isWarning ? (
                        <span className="badge status-degraded">Active</span>
                      ) : evt.healthState ? (
                        <span className={clsx('badge', getHealthBadgeColor(evt.healthState))}>{evt.healthState}</span>
                      ) : null}
                    </td>
                  </tr>
                )
              })
            )}
          </tbody>
        </table>
      </div>
    </div>
  )
}

function EventDot({ event }: { event: TimelineEvent }) {
  const isWarning = isProblematicEvent(event)
  const isDelete = event.eventType === 'delete'
  const isAdd = event.eventType === 'add'
  return (
    <div className={clsx(
      'w-3 h-3 rounded-full shrink-0',
      isWarning ? 'bg-amber-500' : isDelete ? 'bg-red-500' : isAdd ? 'bg-green-500' : 'bg-blue-500'
    )} />
  )
}

// ============================================================================
// INFO TAB — uses ResourceRendererDispatch for kind-specific rendering
// ============================================================================

function InfoTab({
  resource,
  selectedResource,
  relationships,
  hpaDiagnosis,
  scalerDiagnostics,
  isLoading,
  error,
  onNavigate,
  onCopy,
  copied,
  onSaveSecretValue,
  isSavingSecret,
  onOpenLogs,
  onSwitchToTimeline,
  rendererOverrides,
  resolvedEnvFrom,
  events,
  eventsLoading,
  updates,
  eventsError,
  updatesError,
  extraContent,
  leadContent,
}: {
  resource: any
  selectedResource: SelectedResource
  relationships?: Relationships
  hpaDiagnosis?: HPADiagnosis
  scalerDiagnostics?: ScalerDiagnosis[]
  isLoading: boolean
  error?: unknown
  onNavigate?: NavigateToResource
  onCopy: (text: string, key: string) => void
  copied: string | null
  onSaveSecretValue?: (yaml: string) => Promise<void>
  isSavingSecret?: boolean
  onOpenLogs?: (podName: string, containerName: string) => void
  onSwitchToTimeline?: () => void
  rendererOverrides?: RendererOverrides
  resolvedEnvFrom?: ResolvedEnvFrom
  events?: TimelineEvent[]
  eventsLoading?: boolean
  updates?: TimelineEvent[]
  eventsError?: Error | null
  updatesError?: Error | null
  extraContent?: ReactNode
  leadContent?: ReactNode
}) {
  if (!resource) {
    return <FetchResult loading={isLoading} error={error} className="h-full" />
  }

  return (
    <div className="h-full overflow-auto">
      {leadContent && (
        <div className="px-4 pt-4">
          {leadContent}
        </div>
      )}
      <ResourceRendererDispatch
        resource={selectedResource}
        data={resource}
        relationships={relationships}
        hpaDiagnosis={hpaDiagnosis}
        scalerDiagnostics={scalerDiagnostics}
        onCopy={onCopy}
        copied={copied}
        onNavigate={onNavigate ? (ref) => onNavigate(refToSelectedResource(ref)) : undefined}
        onSaveSecretValue={onSaveSecretValue}
        isSavingSecret={isSavingSecret}
        showCommonSections={true}
        showMetrics={false}
        onOpenLogs={onOpenLogs}
        rendererOverrides={rendererOverrides}
        resolvedEnvFrom={resolvedEnvFrom}
        events={events}
        eventsLoading={eventsLoading}
        updates={updates}
        eventsError={eventsError}
        updatesError={updatesError}
        eventsHint={onSwitchToTimeline && (
          <button
            onClick={onSwitchToTimeline}
            className="text-xs text-theme-text-tertiary hover:text-theme-text-secondary transition-colors"
          >
            Showing recent events across this workload. Switch to the <span className="underline">Timeline</span> tab for full history and resource relationships.
          </button>
        )}
        renderSidebar={(sidebarSections) => (
          <div className="lg:w-[35%] lg:shrink-0 lg:border-l border-theme-border">
            <div className="p-4 space-y-4">
              {sidebarSections}
            </div>
          </div>
        )}
      />
      {extraContent && (
        <div className="px-4 pb-4">
          {extraContent}
        </div>
      )}
    </div>
  )
}
