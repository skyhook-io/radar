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
import { buildResourceHierarchy, getAllEventsFromHierarchy, type ResourceLane } from '../../utils/resource-hierarchy'
import { TimelineSwimlanes, type TimeWindow } from '../timeline/TimelineSwimlanes'
import { TimelineList } from '../timeline/TimelineList'
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
  /** false on the outgoing layer during an expand/collapse crossfade — suspend
   *  keyboard shortcuts so the invisible layer doesn't capture them (default true) */
  active?: boolean
  /** Close the drawer (collapsed mode) */
  onClose?: () => void
  /** Expand from drawer to full view. `opts.yaml` true when expanding from the
   *  drawer's YAML view so the full view opens on the YAML tab (edits carry over). */
  onExpand?: (opts?: { yaml?: boolean }) => void
  /** Hover/press the expand control = likely expand → pre-mount the full view. */
  onExpandIntent?: () => void
  onCancelExpandIntent?: () => void
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
  active = true,
  onClose,
  onExpand,
  onExpandIntent,
  onCancelExpandIntent,
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
      // 'drawer' (top priority) in both modes so when this is the fullscreen
      // overlay its Escape unambiguously wins over any background view's Escape
      // (incl. another 'global'-scope WorkloadView mounted underneath).
      scope: 'drawer' as const,
      handler: expanded ? onBack : () => onClose?.(),
      enabled: active,
    },
    {
      id: 'drawer-yaml',
      keys: 'y',
      description: 'Switch to YAML view',
      category: 'Drawer' as const,
      scope: 'drawer' as const,
      handler: () => switchView(true),
      enabled: active && !expanded,
    },
    {
      id: 'drawer-detail',
      keys: 'e',
      description: 'Switch to detail view',
      category: 'Drawer' as const,
      scope: 'drawer' as const,
      handler: () => switchView(false),
      enabled: active && !expanded,
    },
  ], [active, expanded, onBack, onClose, switchView]))

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
                  onClick={() => onExpand({ yaml: showYaml })}
                  // Pre-mount the fullscreen view on hover/press so the click starts
                  // the morph instantly (its heavy mount is already paid for).
                  onPointerEnter={onExpandIntent}
                  onPointerDown={onExpandIntent}
                  onPointerLeave={onCancelExpandIntent}
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
            isLoading={eventsLoading}
            selectedEventId={selectedEventId}
            onSelectEvent={setSelectedEventId}
            topology={topology}
            onResourceClick={onNavigateToResource}
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
  isLoading,
  selectedEventId,
  onSelectEvent,
  topology,
  onResourceClick,
}: {
  events: TimelineEvent[]
  isLoading: boolean
  selectedEventId: string | null
  onSelectEvent: (id: string | null) => void
  topology?: Topology
  onResourceClick?: NavigateToResource
}) {
  // A ticking clock so the fitted window's right edge and the swimlane's Now line
  // track the present, not the mount time — the tab's events refresh (SSE + poll),
  // so a frozen "now" drifts left of the advancing edge and newer events land "in
  // the future" to its right.
  const [now, setNow] = useState(() => Date.now())
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), 30_000)
    return () => clearInterval(id)
  }, [])

  // Drop untimeable events once, at the source, so the swimlane, the list's date
  // grouping, and selection all agree. The Go/K8s zero time (0001-01-01) parses to
  // a valid-but-meaningless Date the swimlane can't place, but the list would
  // otherwise bucket it under a bogus "1/1/1" header (and selecting it strands the
  // pan). NaN / future timestamps are stripped for the same reason.
  const cleanEvents = useMemo(() => {
    const ceiling = Date.now() + 60_000
    return events.filter((e) => {
      const t = new Date(e.timestamp).getTime()
      return Number.isFinite(t) && t > 0 && t <= ceiling
    })
  }, [events])

  // Fit the swimlane's window to this resource's events. Uncontrolled, the
  // swimlane anchors to now and shows the last hour — but a resource's events can
  // be days old (a Deployment created last week), so that window is empty. Default
  // to the events' span; a user pan/zoom (userWindow) then takes over. Deriving the
  // default (not latching it in state) means it always tracks the current events —
  // latching once locked onto the pre-scoping event set and left the window wrong.
  const eventSpan = useMemo<TimeWindow | null>(() => {
    if (cleanEvents.length === 0) return null
    let lo = Infinity, hi = -Infinity
    for (const e of cleanEvents) {
      const t = new Date(e.timestamp).getTime()
      if (t < lo) lo = t
      if (t > hi) hi = t
    }
    const pad = Math.max((hi - lo) * 0.05, 60_000)
    // Never extend into the future: the right edge is the newest event or now,
    // whichever is earlier, so health bars stop at the Now line.
    return { fromMs: lo - pad, toMs: Math.min(hi + pad, now) }
  }, [cleanEvents, now])
  const [userWindow, setUserWindow] = useState<TimeWindow | null>(null)
  const viewWindow = userWindow ?? eventSpan

  // Hard bounds for pan/zoom: the window can never leave [oldest event, now], so
  // scrolling back can't run off into empty/invalid-date territory and zoom-out
  // can't exceed the resource's actual lifespan.
  const bounds = useMemo<TimeWindow | null>(
    () => (eventSpan ? { fromMs: eventSpan.fromMs, toMs: now } : null),
    [eventSpan, now],
  )

  // Selecting an event — from the list OR the swimlane — pans the swimlane window
  // to include it in the SAME update, THEN sets the shared selection. Panning
  // atomically is what prevents the flicker: the swimlane resolves selectedEventId
  // against the window on the same render, so an off-window event never briefly
  // resolves to null and oscillate against a separate pan effect. Refs read the
  // current window/events without making this callback churn.
  const selRefs = useRef({ viewWindow, events: cleanEvents, bounds })
  selRefs.current = { viewWindow, events: cleanEvents, bounds }
  const handleSelect = useCallback((id: string | null) => {
    if (id) {
      const { viewWindow: vw, events: evs, bounds: bd } = selRefs.current
      const evt = evs.find((e) => e.id === id)
      const t = evt ? new Date(evt.timestamp).getTime() : NaN
      if (vw && Number.isFinite(t) && (t < vw.fromMs || t > vw.toMs)) {
        const width = vw.toMs - vw.fromMs
        const now = Date.now()
        let fromMs = t - width / 2
        let toMs = t + width / 2
        if (toMs > now) { toMs = now; fromMs = now - width }
        const lo = bd?.fromMs
        if (lo != null && fromMs < lo) { fromMs = lo; toMs = lo + width }
        setUserWindow({ fromMs, toMs })
      }
    }
    onSelectEvent(id)
  }, [onSelectEvent])

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
      {/* Swimlane — the shared TimelineSwimlanes widget (kind chips, top axis with
          ticks + Now line, event clustering), flat + compact for a single subject.
          Its drawer is suppressed (compact); the list below is the detail surface. */}
      <div className="shrink-0 border-b border-theme-border">
        <TimelineSwimlanes
          events={cleanEvents}
          grouping="flat"
          compact
          topology={topology}
          onResourceClick={onResourceClick}
          viewWindow={viewWindow ?? undefined}
          bounds={bounds ?? undefined}
          nowMs={now}
          isLive={userWindow == null}
          onViewWindowChange={(w) => {
            const now = Date.now()
            const MIN = 15 * 60_000
            let { fromMs, toMs } = w
            // Floor zoom-in at ~15 min: a tiny empty sub-window strands the user
            // (the "move the strip above" hint has no strip here).
            if (toMs - fromMs < MIN) {
              const c = (fromMs + toMs) / 2
              fromMs = c - MIN / 2
              toMs = c + MIN / 2
            }
            // Never pan/zoom past now: shift the window back so its right edge is
            // at most the present (there's nothing in the future to show).
            if (toMs > now) {
              const width = toMs - fromMs
              toMs = now
              fromMs = now - width
            }
            setUserWindow({ fromMs, toMs })
          }}
          selectedEventId={selectedEventId}
          onSelectedEventChange={handleSelect}
        />
      </div>

      {/* Event list — the shared TimelineList: same event icons/colors as the
          swimlane, kind badge + resource link, namespace, message, and inline diff,
          all shown directly. compact drops its toolbar (the swimlane above owns the
          window). */}
      <div className="min-h-0 flex-1">
        <TimelineList
          compact
          events={cleanEvents}
          isLoading={false}
          onResourceClick={onResourceClick}
          selectedEventId={selectedEventId}
          onSelectEvent={handleSelect}
        />
      </div>
    </div>
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
