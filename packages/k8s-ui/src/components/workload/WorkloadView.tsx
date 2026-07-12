import { useState, useMemo, useEffect, useRef, useCallback, type ComponentType, type ReactNode } from 'react'
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
  AlertTriangle,
  Terminal,
  Layers,
  FileText,
  Copy,
  Check,
  CheckCircle2,
  Clock3,
  Boxes,
  ExternalLink,
  GitBranch,
  Minimize2,
  Maximize2,
  Scale,
  Server,
  X,
  BarChart3,
  Network,
} from 'lucide-react'
import type { TimelineEvent, ResourceRef, Relationships, SelectedResource, ResolvedEnvFrom, Topology, TopologyNode, HPADiagnosis, WorkloadPodInfo } from '../../types'
import type { GitOpsStatus } from '../../types/gitops'
import type { NavigateToResource } from '../../utils/navigation'
import { refToSelectedResource, pluralToKind, kindToPlural, apiVersionToGroup } from '../../utils/navigation'
import { neighborhoodFor, seedNodeIds } from '../../utils/topology-neighborhood'
import { TopologyGraph } from '../topology/TopologyGraph'
import { gitOpsOwnerFromRelationships, type GitOpsOwnerRef } from '../../utils/gitops-owner'
import { gitOpsRouteForResource } from '../../utils/gitops-route'
import { buildResourceHierarchy, getAllEventsFromHierarchy, isProblematicEvent, type ResourceLane } from '../../utils/resource-hierarchy'
import { TimelineSwimlanes, type TimeWindow } from '../timeline/TimelineSwimlanes'
import { TimelineList } from '../timeline/TimelineList'
import { ResourceActionsBar } from '../shared/ResourceActionsBar'
import { EditableYamlView, SaveSuccessAnimation } from '../shared/EditableYamlView'
import { ResourceRendererDispatch, getResourceStatus, diagnoseHealthHint, type DiagnoseHealthHint, type RendererOverrides } from '../shared/ResourceRendererDispatch'
import type { ScalerDiagnosis } from '../resources/renderers/WorkloadRenderer'
import { DetailShell, type DetailShellTab } from '../shared/DetailShell'
import { HelmManagedByChip, ManagedByChip, type HelmOwnerRef } from '../shared/ManagedByChip'
import { getKindColorOutline, displayKindName, OperationalIssuesShownContext, ResourceRefBadge } from '../ui/drawer-components'
import { Badge, type BadgeSeverity } from '../ui/Badge'
import { Tooltip } from '../ui/Tooltip'
import { midTruncate } from '../../utils/format'
import { hpaStateLabel } from '../resources/resource-utils-hpa'
import {
  cronToHuman,
  formatAge,
  formatDuration,
  getGatewayAddresses,
  getGatewayListeners,
  getIngressAddress,
  getIngressHosts,
  getServiceExternalIP,
} from '../resources/resource-utils'
import { ServicePortCards, type ServicePortRenderProps } from '../resources/renderers/ServiceRenderer'

export type WorkloadTabType = 'overview' | 'topology' | 'timeline' | 'logs' | 'metrics' | 'yaml'
type TabType = WorkloadTabType

export interface ResourceOwnershipContext {
  application?: {
    key: string
    name: string
  }
  workload: ResourceRef
}

export interface ServingResourceDetail {
  ref: ResourceRef
  resource?: any
  loading?: boolean
  error?: Error | null
}

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
  /** High-confidence containment context for resources whose controller ownership is reliable, currently Pods. */
  ownershipContext?: ResourceOwnershipContext
  /** Open the application wrapper for ownership breadcrumbs. */
  onOpenApplication?: (appKey: string) => void

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
  /** Pod readiness data from the host's workload-pods endpoint. Optional so
   *  shared consumers can still render from relationships when they do not wire
   *  Radar-specific data hooks. */
  workloadPods?: WorkloadPodInfo[]
  workloadPodsLoading?: boolean
  workloadPodsError?: Error | null
  /** Full objects for service/route refs related to this workload. Optional;
   *  overview falls back to relationship refs when hosts do not fetch them. */
  servingResources?: ServingResourceDetail[]
  renderServicePortAction?: (props: ServicePortRenderProps) => ReactNode
  renderServicePortPanel?: (props: ServicePortRenderProps) => ReactNode
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
  /** Persisted lifecycle events reconstructed for resources related to this workload. */
  relatedTimelineEvents?: TimelineEvent[]
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
  onTabChange?: (tab: TabType, opts?: { replace?: boolean }) => void

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
  /** Render a full replacement for the expanded Overview tab. */
  renderExpandedOverview?: (props: {
    kind: string
    apiKind: string
    namespace: string
    name: string
    resource: any
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
  /** Render lightweight overview intro content before the default renderer. */
  renderOverviewIntro?: (props: { kind: string; namespace: string; name: string }) => ReactNode
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
  ownershipContext,
  onOpenApplication,
  // Data
  resource,
  relationships,
  certificateInfo,
  hpaDiagnosis,
  scalerDiagnostics,
  workloadPods,
  workloadPodsLoading = false,
  workloadPodsError = null,
  servingResources,
  renderServicePortAction,
  renderServicePortPanel,
  isLoading: resourceLoading = false,
  resourceError,
  refetch: refetchProp,
  // Timeline
  allEvents,
  relatedTimelineEvents = [],
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
  renderExpandedOverview,
  renderRelatedYaml,
  renderMetricsTab,
  isMetricsAvailable,
  // Duplicate
  onDuplicate,
  onDownload,
  renderOverviewExtra,
  renderOverviewIntro,
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
    const events = [...getAllEventsFromHierarchy(resourceLanes), ...relatedTimelineEvents]
    const unique = new Map(events.map((event) => [event.id, event]))
    return Array.from(unique.values()).sort((a, b) => Date.parse(a.timestamp) - Date.parse(b.timestamp) || a.id.localeCompare(b.id))
  }, [resourceLanes, relatedTimelineEvents])
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
      ...(workloadPods ?? []).map(p => ({ kind: 'Pod' as const, namespace, name: p.name })),
    ]
    const seen = new Set<string>()
    return combined.filter(p => {
      const key = `${p.namespace}/${p.name}`
      if (seen.has(key)) return false
      seen.add(key)
      return true
    })
  }, [pods, childPods, workloadPods, namespace])

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
  const showOwnershipHeading = kind === 'Pod' && Boolean(ownershipContext)
  const headerImage = metadata.find(m => m.label === 'Image')?.value

  // The AI/Diagnose action lives in the header chrome (next to expand/refresh/close),
  // set apart from the imperative ops in the action bar — it's an invitation to
  // understand the resource, not another verb to run on it. Adaptive by health:
  // prominent "Diagnose" on a problem, a quiet icon when fine. Rendered via the host
  // slot (DiagnoseCustomization); standalone Radar injects it, Hub overrides it.
  const renderDiagnose = actionsBarProps?.renderDiagnose as
    | ((ctx: { kind: string; namespace: string; name: string; health?: DiagnoseHealthHint }) => ReactNode)
    | undefined
  const diagnoseAction = renderDiagnose?.({
    kind: apiKind,
    namespace,
    name,
    health: diagnoseHealthHint(apiKind, resource),
  })

  const showMetricsTab = isMetricsAvailable ? isMetricsAvailable(kind, resource) : false
  const logsTabVisible = Boolean(renderLogsTab) && (allPods.length > 0 || LOGS_TAB_WITHOUT_PODS_KINDS.has(kindToPlural(kind).toLowerCase()))
  const metricsTabVisible = Boolean(showMetricsTab && renderMetricsTab)
  const podEvidenceLoading = resourceLoading || workloadPodsLoading || eventsLoading
  const logsFallbackReady = !renderLogsTab || (!logsTabVisible && !podEvidenceLoading)
  const requestedTab: TabType = activeTab
  const tabs: DetailShellTab<TabType>[] = [
    { id: 'overview', label: 'Overview', icon: <Layers className="w-4 h-4" /> },
    { id: 'topology', label: 'Topology', icon: <Network className="w-4 h-4" />, hidden: topologyTabHidden },
    {
      id: 'timeline',
      label: 'Timeline',
      icon: <Activity className="w-4 h-4" />,
      badge: resourceEvents.length > 0 ? <span className="ml-1 badge-sm bg-theme-elevated">{resourceEvents.length}</span> : undefined,
    },
    { id: 'logs', label: 'Logs', icon: <Terminal className="w-4 h-4" />, hidden: !logsTabVisible },
    { id: 'metrics', label: 'Metrics', icon: <BarChart3 className="w-4 h-4" />, hidden: !metricsTabVisible },
    { id: 'yaml', label: 'YAML', icon: <FileText className="w-4 h-4" /> },
  ]
  const requestedTabAvailable = tabs.some((tab) => tab.id === requestedTab && !tab.hidden)
  const effectiveTab: TabType = requestedTabAvailable ? requestedTab : 'overview'
  const shouldCommitFallback =
    requestedTab !== 'overview' &&
    !requestedTabAvailable &&
    (
      (requestedTab === 'topology' && topologyTabHidden) ||
      (requestedTab === 'metrics' && (!renderMetricsTab || (!!resource && !resourceLoading && !showMetricsTab))) ||
      (requestedTab === 'logs' && logsFallbackReady)
    )
  useEffect(() => {
    if (shouldCommitFallback && activeTab !== 'overview') onTabChange?.('overview', { replace: true })
  }, [activeTab, onTabChange, shouldCommitFallback])
  const expandedOverview = expanded ? renderExpandedOverview?.({ kind, apiKind, namespace, name, resource }) : null
  const overviewIntro = renderOverviewIntro?.({ kind, namespace, name })

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
            <div className="flex items-center gap-1.5">
              {diagnoseAction}
              {onExpand && (
                <Tooltip content="Open full view" delay={150} position="bottom">
                  <button
                    onClick={() => onExpand({ yaml: showYaml })}
                    // Pre-mount the fullscreen view on hover/press so the click starts
                    // the morph instantly (its heavy mount is already paid for).
                    onPointerEnter={onExpandIntent}
                    onPointerDown={onExpandIntent}
                    onPointerLeave={onCancelExpandIntent}
                    className="p-1.5 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
                    aria-label="Open full view"
                  >
                    <Maximize2 className="w-4 h-4" />
                  </button>
                </Tooltip>
              )}
              <Tooltip content="Refresh" delay={150} position="bottom">
                <button
                  onClick={() => refetch()}
                  disabled={isRefreshAnimating}
                  className={clsx(
                    'p-1.5 hover:bg-theme-elevated rounded disabled:opacity-50 transition-colors duration-500',
                    refreshPhase === 'success' ? 'text-emerald-400' : 'text-theme-text-secondary hover:text-theme-text-primary'
                  )}
                  aria-label="Refresh"
                >
                  {refreshPhase === 'success'
                    ? <Check className="w-4 h-4 stroke-[2.5]" />
                    : <RefreshCw className={clsx('w-4 h-4', refreshPhase === 'spinning' && 'animate-spin')} />
                  }
                </button>
              </Tooltip>
              {onClose && (
                <Tooltip content="Close (Esc)" delay={150} position="bottom">
                  <button onClick={onClose} className="p-1.5 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded" aria-label="Close">
                    <X className="w-4 h-4" />
                  </button>
                </Tooltip>
              )}
            </div>
          </div>

          {/* Name and namespace */}
          <div className="px-4 pb-3">
            <div className="flex items-center gap-2">
              <h2 className="text-lg font-semibold text-theme-text-primary truncate">{name}</h2>
              <Tooltip content="Copy name" delay={150}>
                <button
                  onClick={() => copyToClipboard(name, 'name')}
                  className="p-1 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded shrink-0"
                  aria-label="Copy name"
                >
                  {copied === 'name' ? <Check className="w-3.5 h-3.5 text-green-400" /> : <Copy className="w-3.5 h-3.5" />}
                </button>
              </Tooltip>
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
                mainFooter={renderOverviewExtra && renderOverviewExtra({ kind, namespace, name })}
              />
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
          <Tooltip content="Go back (Esc)" delay={150} position="bottom">
            <button
              onClick={onBack}
              className="p-1.5 mt-0.5 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded-lg transition-colors"
              aria-label="Go back"
            >
              <ArrowLeft className="w-5 h-5" />
            </button>
          </Tooltip>
        )
      }
      identity={
        <>
          {showOwnershipHeading && ownershipContext ? (
            <OwnershipHeading
              podName={name}
              context={ownershipContext}
              copied={copied === 'name'}
              onCopy={() => copyToClipboard(name, 'name')}
              onNavigateToResource={onNavigateToResource}
              onOpenApplication={onOpenApplication}
            />
          ) : (
            <div className="flex items-center gap-3 mb-1">
              <h1 className="text-lg font-semibold text-theme-text-primary truncate">{name}</h1>
              <Tooltip content="Copy name" delay={150}>
                <button
                  onClick={() => copyToClipboard(name, 'name')}
                  className="p-1 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded shrink-0"
                  aria-label="Copy name"
                >
                  {copied === 'name' ? <Check className="w-3.5 h-3.5 text-green-400" /> : <Copy className="w-3.5 h-3.5" />}
                </button>
              </Tooltip>
            </div>
          )}
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
            {headerImage && (
              <Tooltip content={headerImage} delay={300} wrapperClassName="min-w-0 max-w-md">
                <span className="truncate font-mono text-xs">
                  {midTruncate(headerImage, 72)}
                </span>
              </Tooltip>
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
            {relationships?.owner && !showOwnershipHeading && (
              <span>Owner: <button onClick={() => onNavigateToResource?.(refToSelectedResource(relationships.owner!))} className="text-blue-500 hover:underline">{relationships.owner.name}</button></span>
            )}
          </div>
        </>
      }
      headerActions={
        <>
          {diagnoseAction}
          <Tooltip content="Refresh" delay={150} position="bottom">
            <button
              onClick={() => refetch()}
              disabled={isRefreshAnimating}
              className={clsx(
                'p-1.5 mt-0.5 hover:bg-theme-elevated rounded disabled:opacity-50 transition-colors duration-500',
                refreshPhase === 'success' ? 'text-emerald-400' : 'text-theme-text-secondary hover:text-theme-text-primary'
              )}
              aria-label="Refresh"
            >
              {refreshPhase === 'success'
                ? <Check className="w-5 h-5 stroke-[2.5]" />
                : <RefreshCw className={clsx('w-5 h-5', refreshPhase === 'spinning' && 'animate-spin')} />
              }
            </button>
          </Tooltip>
          {onCollapseToDrawer && (
            <Tooltip content="Collapse to drawer" delay={150} position="bottom">
              <button
                onClick={onCollapseToDrawer}
                className="p-1.5 mt-0.5 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded-lg transition-colors"
                aria-label="Collapse to drawer"
              >
                <Minimize2 className="w-5 h-5" />
              </button>
            </Tooltip>
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
        {effectiveTab === 'overview' && expandedOverview ? (
          <div className="h-full min-h-0">
            {hasOperationalIssues && renderOverviewLead && (
              <div className="px-4 pt-4">
                {renderOverviewLead({ kind, namespace, name })}
              </div>
            )}
            {expandedOverview}
          </div>
        ) : effectiveTab === 'overview' && (
            <InfoTab
              resource={resource}
              selectedResource={selectedResource}
              relationships={relationships}
              certificateInfo={certificateInfo}
              hpaDiagnosis={hpaDiagnosis}
              scalerDiagnostics={scalerDiagnostics}
              workloadPods={workloadPods}
              workloadPodsLoading={workloadPodsLoading}
              workloadPodsError={workloadPodsError}
              servingResources={servingResources}
              renderServicePortAction={renderServicePortAction}
              renderServicePortPanel={renderServicePortPanel}
              isLoading={resourceLoading}
              error={resourceError}
              onNavigate={onNavigateToResource}
              onCopy={copyToClipboard}
              copied={copied}
              onSaveSecretValue={canUpdateSecrets ? handleSaveSecretValue : undefined}
              isSavingSecret={isUpdatingResource}
              onOpenLogs={handleOpenLogs}
              onSwitchToTimeline={() => handleSetTab('timeline')}
              onSwitchToLogs={logsTabVisible ? () => handleSetTab('logs') : undefined}
              onSwitchToTopology={!topologyTabHidden ? () => handleSetTab('topology') : undefined}
              rendererOverrides={rendererOverrides}
              resolvedEnvFrom={resolvedEnvFrom}
              events={overviewEvents}
              eventsLoading={overviewEventsLoading}
              updates={resourceFocusedUpdates}
              eventsError={overviewEventsError}
              updatesError={resourceFocusedUpdatesError}
              extraContent={renderOverviewExtra && renderOverviewExtra({ kind, namespace, name })}
              introContent={overviewIntro}
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
    <Tooltip content="Open this resource in the GitOps tab (tree + insights + ops)" delay={150}>
      <button
        type="button"
        onClick={onClick}
        className="inline-flex items-center gap-1 rounded border border-skyhook-500/40 bg-skyhook-500/10 px-1.5 py-0.5 text-[11px] font-medium text-skyhook-500 hover:bg-skyhook-500/20 transition-colors"
      >
        Open in GitOps
        <ArrowRight className="h-3 w-3 shrink-0" />
      </button>
    </Tooltip>
  )
}

function OwnershipHeading({
  podName,
  context,
  copied,
  onCopy,
  onNavigateToResource,
  onOpenApplication,
}: {
  podName: string
  context: ResourceOwnershipContext
  copied: boolean
  onCopy: () => void
  onNavigateToResource?: NavigateToResource
  onOpenApplication?: (appKey: string) => void
}) {
  const app = context.application
  const workload = context.workload
  const workloadLabel = `${displayKindName(kindToPlural(workload.kind), workload.kind)}/${workload.name}`
  return (
    <div className="mb-1 flex min-w-0 items-center gap-2">
      <div className="flex min-w-0 items-center gap-1.5 text-lg font-semibold text-theme-text-primary">
        {app ? (
          onOpenApplication ? (
            <Tooltip content={`Open application ${app.name}`} delay={150} wrapperClassName="min-w-0">
              <button
                type="button"
                onClick={() => onOpenApplication(app.key)}
                className="min-w-0 truncate rounded px-0.5 text-left hover:text-accent-text focus:outline-none focus:ring-2 focus:ring-accent"
              >
                {midTruncate(app.name, 32)}
              </button>
            </Tooltip>
          ) : (
            <Tooltip content={app.name} delay={300} wrapperClassName="min-w-0">
              <span className="truncate">{midTruncate(app.name, 32)}</span>
            </Tooltip>
          )
        ) : null}
        {app && <span className="shrink-0 text-theme-text-tertiary">/</span>}
        {onNavigateToResource ? (
          <Tooltip content={`Open workload ${workloadLabel}`} delay={150} wrapperClassName="min-w-0">
            <button
              type="button"
              onClick={() => onNavigateToResource(refToSelectedResource(workload))}
              className="min-w-0 truncate rounded px-0.5 text-left hover:text-accent-text focus:outline-none focus:ring-2 focus:ring-accent"
            >
              {midTruncate(workload.name, 32)}
            </button>
          </Tooltip>
        ) : (
          <Tooltip content={workloadLabel} delay={300} wrapperClassName="min-w-0">
            <span className="truncate">{midTruncate(workload.name, 32)}</span>
          </Tooltip>
        )}
        <span className="shrink-0 text-theme-text-tertiary">/</span>
        <Tooltip content={podName} delay={300} wrapperClassName="min-w-0">
          <h1 className="truncate text-lg font-semibold text-theme-text-primary">
            {podName}
          </h1>
        </Tooltip>
      </div>
      <Tooltip content="Copy pod name" delay={150}>
        <button
          onClick={onCopy}
          className="shrink-0 rounded p-1 text-theme-text-secondary hover:bg-theme-elevated hover:text-theme-text-primary"
          aria-label="Copy pod name"
        >
          {copied ? <Check className="h-3.5 w-3.5 text-green-400" /> : <Copy className="h-3.5 w-3.5" />}
        </button>
      </Tooltip>
    </div>
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
// INFO TAB — full-page workload triage overview
// ============================================================================

function InfoTab({
  resource,
  selectedResource,
  relationships,
  certificateInfo,
  hpaDiagnosis,
  scalerDiagnostics,
  workloadPods,
  workloadPodsLoading,
  workloadPodsError,
  servingResources,
  renderServicePortAction,
  renderServicePortPanel,
  isLoading,
  error,
  onNavigate,
  onCopy,
  copied,
  onSaveSecretValue,
  isSavingSecret,
  onOpenLogs,
  onSwitchToTimeline,
  onSwitchToLogs,
  onSwitchToTopology,
  rendererOverrides,
  resolvedEnvFrom,
  events,
  eventsLoading,
  updates,
  eventsError,
  updatesError,
  extraContent,
  introContent,
  leadContent,
}: {
  resource: any
  selectedResource: SelectedResource
  relationships?: Relationships
  certificateInfo?: any
  hpaDiagnosis?: HPADiagnosis
  scalerDiagnostics?: ScalerDiagnosis[]
  workloadPods?: WorkloadPodInfo[]
  workloadPodsLoading?: boolean
  workloadPodsError?: Error | null
  servingResources?: ServingResourceDetail[]
  renderServicePortAction?: (props: ServicePortRenderProps) => ReactNode
  renderServicePortPanel?: (props: ServicePortRenderProps) => ReactNode
  isLoading: boolean
  error?: unknown
  onNavigate?: NavigateToResource
  onCopy: (text: string, key: string) => void
  copied: string | null
  onSaveSecretValue?: (yaml: string) => Promise<void>
  isSavingSecret?: boolean
  onOpenLogs?: (podName: string, containerName: string) => void
  onSwitchToTimeline?: () => void
  onSwitchToLogs?: () => void
  onSwitchToTopology?: () => void
  rendererOverrides?: RendererOverrides
  resolvedEnvFrom?: ResolvedEnvFrom
  events?: TimelineEvent[]
  eventsLoading?: boolean
  updates?: TimelineEvent[]
  eventsError?: Error | null
  updatesError?: Error | null
  extraContent?: ReactNode
  introContent?: ReactNode
  leadContent?: ReactNode
}) {
  if (!resource) {
    return <FetchResult loading={isLoading} error={error} className="h-full" />
  }

  if (!isRuntimeWorkloadOverviewKind(selectedResource.kind)) {
    return (
      <div className="h-full overflow-auto">
        {leadContent && (
          <div className="px-4 pt-4">
            {leadContent}
          </div>
        )}
        {introContent && (
          <div className="px-4 pt-4">
            {introContent}
          </div>
        )}
        <ResourceRendererDispatch
          resource={selectedResource}
          data={resource}
          relationships={relationships}
          certificateInfo={certificateInfo}
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
              className="text-xs text-theme-text-tertiary transition-colors hover:text-theme-text-secondary"
            >
              Showing recent events for this resource. Switch to the <span className="underline">Timeline</span> tab for full history and relationships.
            </button>
          )}
          renderSidebar={(sidebarSections) => (
            <div className="border-theme-border lg:w-[35%] lg:shrink-0 lg:border-l">
              <div className="space-y-4 p-4">
                {sidebarSections}
              </div>
            </div>
          )}
          mainFooter={extraContent}
        />
      </div>
    )
  }

  return (
    <WorkloadOverviewTab
      resource={resource}
      selectedResource={selectedResource}
      relationships={relationships}
      hpaDiagnosis={hpaDiagnosis}
      scalerDiagnostics={scalerDiagnostics}
      workloadPods={workloadPods}
      workloadPodsLoading={workloadPodsLoading}
      workloadPodsError={workloadPodsError}
      servingResources={servingResources}
      renderServicePortAction={renderServicePortAction}
      renderServicePortPanel={renderServicePortPanel}
      onNavigate={onNavigate}
      onSwitchToTimeline={onSwitchToTimeline}
      onSwitchToLogs={onSwitchToLogs}
      onSwitchToTopology={onSwitchToTopology}
      events={events}
      eventsLoading={eventsLoading}
      updates={updates}
      eventsError={eventsError}
      updatesError={updatesError}
      extraContent={extraContent}
      introContent={introContent}
      leadContent={leadContent}
    />
  )
}

const POD_VISIBLE_LIMIT = 5
const EVENT_VISIBLE_LIMIT = 5
const RELATIONSHIP_GROUP_LIMIT = 5
const RELATIONSHIP_REF_LIMIT = 5
const LOGS_TAB_WITHOUT_PODS_KINDS = new Set([
  'jobs',
  'cronjobs',
  'workflows',
  'cronworkflows',
  'workflowtemplates',
  'clusterworkflowtemplates',
  'scaledjobs',
])
const RUNTIME_WORKLOAD_OVERVIEW_KINDS = new Set(['deployments', 'statefulsets', 'daemonsets', 'jobs', 'cronjobs'])
type RuntimeOverviewShape = 'replicated' | 'job' | 'cronjob'

function isRuntimeWorkloadOverviewKind(kind: string) {
  return RUNTIME_WORKLOAD_OVERVIEW_KINDS.has(kindToPlural(kind).toLowerCase())
}

function runtimeOverviewShape(kind: string): RuntimeOverviewShape {
  const k = kindToPlural(kind).toLowerCase()
  if (k === 'jobs') return 'job'
  if (k === 'cronjobs') return 'cronjob'
  return 'replicated'
}

function WorkloadOverviewTab({
  resource,
  selectedResource,
  relationships,
  hpaDiagnosis,
  scalerDiagnostics,
  workloadPods,
  workloadPodsLoading,
  workloadPodsError,
  servingResources,
  renderServicePortAction,
  renderServicePortPanel,
  onNavigate,
  onSwitchToTimeline,
  onSwitchToLogs,
  onSwitchToTopology,
  events = [],
  updates = [],
  eventsLoading,
  eventsError,
  updatesError,
  extraContent,
  introContent,
  leadContent,
}: {
  resource: any
  selectedResource: SelectedResource
  relationships?: Relationships
  hpaDiagnosis?: HPADiagnosis
  scalerDiagnostics?: ScalerDiagnosis[]
  workloadPods?: WorkloadPodInfo[]
  workloadPodsLoading?: boolean
  workloadPodsError?: Error | null
  servingResources?: ServingResourceDetail[]
  renderServicePortAction?: (props: ServicePortRenderProps) => ReactNode
  renderServicePortPanel?: (props: ServicePortRenderProps) => ReactNode
  onNavigate?: NavigateToResource
  onSwitchToTimeline?: () => void
  onSwitchToLogs?: () => void
  onSwitchToTopology?: () => void
  events?: TimelineEvent[]
  updates?: TimelineEvent[]
  eventsLoading?: boolean
  eventsError?: Error | null
  updatesError?: Error | null
  extraContent?: ReactNode
  introContent?: ReactNode
  leadContent?: ReactNode
}) {
  const apiKind = kindToPlural(selectedResource.kind)
  const resourceKind = resource?.kind || displayKindName(apiKind, resource?.kind)
  const readiness = getReadinessSummary(resource, apiKind)
  const podSummary = getPodSummary(workloadPods, relationships, apiKind)
  const scaling = getScalingSummary(resource, apiKind, relationships, scalerDiagnostics, hpaDiagnosis)
  const controlSurface = runtimeControlSurface(apiKind)
  const overviewShape = runtimeOverviewShape(apiKind)
  const summaryMetrics = buildSummaryMetrics(resource, apiKind, resourceKind, readiness, podSummary, scaling)
  const relationshipGroups = buildRelationshipGroups(relationships)
  const servingRelationshipGroups = buildServingRelationshipGroups(relationships)
  const dependencyRelationshipGroups = buildDependencyRelationshipGroups(relationships)
  const showServingPath = servingRelationshipGroups.length > 0 && overviewShape !== 'job' && overviewShape !== 'cronjob'
  const secondaryRelationshipGroups = showServingPath ? dependencyRelationshipGroups : relationshipGroups
  const relationshipCardTitle =
    overviewShape === 'job' || overviewShape === 'cronjob'
      ? 'Related resources'
      : showServingPath
        ? 'Dependencies'
        : 'Related resources'

  if (overviewShape === 'replicated') {
    const state = getReplicatedWorkloadState(resource, apiKind)
    const serviceAccountName = getPodTemplateSpec(resource, apiKind)?.serviceAccountName || 'default'
    return (
      <div className="h-full overflow-auto bg-theme-base">
        <div className="space-y-4 p-4">
          {leadContent}
          {introContent}

          <WorkloadStatusStrip
            state={state}
            readiness={readiness}
            podSummary={podSummary}
            servingSummary={showServingPath ? buildServingStripSummary(servingRelationshipGroups, servingResources) : undefined}
          />

          <div className="grid items-start gap-4 xl:grid-cols-[minmax(0,1fr)_360px] 2xl:grid-cols-[minmax(0,1fr)_400px]">
            <div className="space-y-4">
              <div className="grid items-start gap-4 2xl:grid-cols-[minmax(0,1fr)_minmax(320px,420px)]">
                <div className="space-y-4">
                  {showServingPath && (
                    <ServingConfigurationCard
                      groups={servingRelationshipGroups}
                      details={servingResources}
                      renderPortAction={renderServicePortAction}
                      renderPortPanel={renderServicePortPanel}
                      onNavigate={onNavigate}
                      onSwitchToTopology={onSwitchToTopology}
                    />
                  )}
                  <RuntimeCard
                    resource={resource}
                    apiKind={apiKind}
                    state={state}
                    readiness={readiness}
                    scaling={scaling}
                    namespace={selectedResource.namespace}
                    workloadPods={workloadPods}
                    workloadPodsLoading={workloadPodsLoading}
                    workloadPodsError={workloadPodsError}
                    relationshipPods={relationships?.pods}
                    scalerDiagnostics={scalerDiagnostics}
                    hpaDiagnosis={hpaDiagnosis}
                    relationships={relationships}
                    onNavigate={onNavigate}
                    onSwitchToLogs={onSwitchToLogs}
                  />
                </div>
                <div className="space-y-4">
                  <ConfigurationInputsCard
                    serviceAccountName={serviceAccountName}
                    namespace={selectedResource.namespace}
                    relationships={relationships}
                    onNavigate={onNavigate}
                    onSwitchToTopology={onSwitchToTopology}
                  />
                  {extraContent}
                </div>
              </div>
            </div>

            <div className="space-y-4">
              <ActivityCard
                events={events}
                updates={updates}
                loading={eventsLoading}
                eventsError={eventsError}
                updatesError={updatesError}
                onSwitchToTimeline={onSwitchToTimeline}
              />
            </div>
          </div>
        </div>
      </div>
    )
  }

  return (
    <div className="h-full overflow-auto bg-theme-base">
      <div className="space-y-4 p-4">
        {leadContent}
        {introContent}

        <div className="grid grid-cols-1 gap-3 md:grid-cols-2 2xl:grid-cols-4">
          {summaryMetrics.map((metric) => (
            <SummaryMetric key={metric.label} {...metric} />
          ))}
        </div>

        {showServingPath && (
          <ServingConfigurationCard
            groups={servingRelationshipGroups}
            details={servingResources}
            renderPortAction={renderServicePortAction}
            renderPortPanel={renderServicePortPanel}
            onNavigate={onNavigate}
            onSwitchToTopology={onSwitchToTopology}
          />
        )}

        <div className="grid items-start gap-4 xl:grid-cols-[minmax(0,1fr)_360px] 2xl:grid-cols-[minmax(0,1fr)_400px]">
          <div className="space-y-4">
            <CurrentStateCard resource={resource} apiKind={apiKind} readiness={readiness} />
            <div className="grid items-start gap-4 2xl:grid-cols-2">
              {overviewShape === 'cronjob' ? (
                <CronJobRunsCard
                  resource={resource}
                  namespace={selectedResource.namespace}
                  onNavigate={onNavigate}
                  onSwitchToTopology={onSwitchToTopology}
                />
              ) : (
                <PodsAttentionCard
                  namespace={selectedResource.namespace}
                  emptyText={overviewShape === 'job' ? 'No Pods are currently retained for this Job.' : undefined}
                  workloadPods={workloadPods}
                  workloadPodsLoading={workloadPodsLoading}
                  workloadPodsError={workloadPodsError}
                  relationshipPods={relationships?.pods}
                  onNavigate={onNavigate}
                  onSwitchToLogs={onSwitchToLogs}
                />
              )}
              <ScalingCard
                title={controlSurface.cardTitle}
                icon={controlSurface.icon}
                scaling={scaling}
                scalerDiagnostics={scalerDiagnostics}
                hpaDiagnosis={hpaDiagnosis}
                relationships={relationships}
                onNavigate={onNavigate}
              />
            </div>
            {secondaryRelationshipGroups.length > 0 ? (
              <ServingDependenciesCard
                title={relationshipCardTitle}
                relationships={relationships}
                groups={secondaryRelationshipGroups}
                onNavigate={onNavigate}
                onSwitchToTopology={onSwitchToTopology}
              />
            ) : null}
            {extraContent && (
              <div className="grid items-start gap-4 2xl:grid-cols-2">
                {extraContent}
              </div>
            )}
          </div>

          <div className="space-y-4">
            <ActivityCard
              events={events}
              updates={updates}
              loading={eventsLoading}
              eventsError={eventsError}
              updatesError={updatesError}
              onSwitchToTimeline={onSwitchToTimeline}
            />
          </div>
        </div>

      </div>
    </div>
  )
}

function OverviewCard({
  title,
  icon: Icon,
  action,
  children,
}: {
  title: string
  icon: ComponentType<{ className?: string }>
  action?: ReactNode
  children: ReactNode
}) {
  return (
    <section className="rounded-lg border border-theme-border bg-theme-surface p-4 shadow-theme-sm">
      <div className="mb-3 flex min-w-0 items-center justify-between gap-3">
        <div className="flex min-w-0 items-center gap-2">
          <Icon className="h-4 w-4 shrink-0 text-theme-text-secondary" />
          <h2 className="truncate text-[11px] font-semibold uppercase tracking-wide text-theme-text-secondary">{title}</h2>
        </div>
        {action}
      </div>
      {children}
    </section>
  )
}

function SummaryMetric({
  label,
  value,
  detail,
  icon: Icon,
  mono,
}: {
  label: string
  value: ReactNode
  detail?: ReactNode
  icon: ComponentType<{ className?: string }>
  mono?: boolean
}) {
  return (
    <section className="min-w-0 rounded-lg border border-theme-border bg-theme-surface p-4 shadow-theme-sm">
      <div className="mb-3 flex items-center gap-2 text-[11px] font-semibold uppercase tracking-wide text-theme-text-secondary">
        <Icon className="h-4 w-4 shrink-0" />
        <span>{label}</span>
      </div>
      <div className={clsx('truncate text-lg font-semibold text-theme-text-primary', mono && 'font-mono')}>{value}</div>
      {detail && <div className="mt-1 truncate text-xs text-theme-text-tertiary">{detail}</div>}
    </section>
  )
}

interface ReplicatedWorkloadState {
  label: string
  detail: string
  badgeClass: string
  level: 'healthy' | 'neutral' | 'degraded' | 'unhealthy' | 'unknown'
  healthy: boolean
  needsAttention: boolean
}

function WorkloadStatusStrip({
  state,
  readiness,
  podSummary,
  servingSummary,
}: {
  state: ReplicatedWorkloadState
  readiness: ReadinessSummary
  podSummary: ReturnType<typeof getPodSummary>
  servingSummary?: string
}) {
  const servingMode = Boolean(servingSummary)
  const stateDetail = servingMode && state.healthy ? 'runtime health' : state.detail
  const podItem = servingMode && typeof podSummary.total === 'number'
    ? {
        label: 'Pods',
        value: `${podSummary.total}`,
        detail: podSummary.total === 0
          ? 'none running'
          : (podSummary.notReady ?? 0) > 0
            ? `${podSummary.notReady} need attention`
            : 'runtime instances',
      }
    : { label: 'Pods', value: podSummary.label, detail: podSummary.detail }
  const items = [
    { label: 'State', value: <span className={clsx('badge-sm', state.badgeClass)}>{state.label}</span>, detail: stateDetail },
    { label: 'Ready', value: readiness.readyLabel, detail: readiness.detail },
    podItem,
    servingSummary ? { label: 'Exposure', value: servingSummary, detail: 'configured, not probed' } : null,
  ].filter(Boolean) as Array<{ label: string; value: ReactNode; detail?: ReactNode }>

  return (
    <section className="rounded-lg border border-theme-border bg-theme-surface px-4 py-3 shadow-theme-sm">
      <div className="grid gap-3 sm:grid-cols-2 2xl:grid-cols-4">
        {items.map((item) => (
          <div key={item.label} className="min-w-0">
            <div className="text-[10px] font-semibold uppercase tracking-wide text-theme-text-tertiary">{item.label}</div>
            <div className="mt-1 truncate text-sm font-semibold text-theme-text-primary">{item.value}</div>
            {item.detail ? <div className="mt-0.5 truncate text-xs text-theme-text-tertiary">{item.detail}</div> : null}
          </div>
        ))}
      </div>
    </section>
  )
}

function getReplicatedWorkloadState(resource: any, apiKind: string): ReplicatedWorkloadState {
  const status = resource?.status || {}
  const spec = resource?.spec || {}
  const metadata = resource?.metadata || {}
  const k = apiKind.toLowerCase()
  const generation = metadata.generation
  const observedGeneration = status.observedGeneration
  const controllerBehind =
    typeof generation === 'number' &&
    typeof observedGeneration === 'number' &&
    observedGeneration < generation

  if (k === 'daemonsets') {
    const desired = status.desiredNumberScheduled ?? 0
    const ready = status.numberReady ?? 0
    const updated = status.updatedNumberScheduled ?? 0
    const unavailable = status.numberUnavailable ?? Math.max(0, desired - ready)
    if (desired === 0) return stateSummary('No targets', 'selector matches no nodes', 'status-neutral', 'neutral')
    if (controllerBehind) return stateSummary('Applying', 'controller has not observed latest spec', 'status-neutral', 'neutral')
    if (updated < desired) return stateSummary('Rolling out', `${updated}/${desired} updated`, 'status-neutral', 'neutral')
    if (ready === desired && unavailable === 0) return stateSummary('Healthy', `${ready}/${desired} ready`, 'status-healthy', 'healthy')
    if (ready > 0) return stateSummary('Degraded', `${ready}/${desired} ready`, 'status-degraded', 'degraded')
    return stateSummary('Unhealthy', 'no scheduled pods are ready', 'status-unhealthy', 'unhealthy')
  }

  const desired = spec.replicas ?? status.replicas ?? 0
  const ready = status.readyReplicas ?? 0
  const updated = status.updatedReplicas ?? 0
  const available = status.availableReplicas ?? ready
  const unavailable = status.unavailableReplicas ?? Math.max(0, desired - available)
  if (desired === 0) return stateSummary('Scaled to zero', 'no replicas requested', 'status-neutral', 'neutral')
  if (k === 'deployments' && spec.paused) return stateSummary('Paused', `${ready}/${desired} ready`, 'status-neutral', 'neutral')
  const progressCondition = Array.isArray(status.conditions)
    ? status.conditions.find((condition: any) => condition.type === 'Progressing')
    : undefined
  if (progressCondition?.status === 'False' || progressCondition?.reason === 'ProgressDeadlineExceeded') {
    return stateSummary('Stalled', progressCondition.message || `${ready}/${desired} ready`, 'status-unhealthy', 'unhealthy')
  }
  if (controllerBehind) return stateSummary('Applying', 'controller has not observed latest spec', 'status-neutral', 'neutral')
  const statefulSetRolling = k === 'statefulsets' && status.updateRevision && status.currentRevision && status.updateRevision !== status.currentRevision
  if (updated < desired || statefulSetRolling) return stateSummary('Rolling out', `${updated}/${desired} updated`, 'status-neutral', 'neutral')
  if (ready === desired && available === desired && unavailable === 0) return stateSummary('Healthy', `${ready}/${desired} ready`, 'status-healthy', 'healthy')
  if (ready > 0) return stateSummary('Degraded', `${ready}/${desired} ready`, 'status-degraded', 'degraded')
  return stateSummary('Unhealthy', 'no replicas are ready', 'status-unhealthy', 'unhealthy')
}

function stateSummary(
  label: string,
  detail: string,
  badgeClass: string,
  level: ReplicatedWorkloadState['level'],
): ReplicatedWorkloadState {
  return {
    label,
    detail,
    badgeClass,
    level,
    healthy: level === 'healthy',
    needsAttention: level === 'degraded' || level === 'unhealthy',
  }
}

function buildServingStripSummary(groups: RelationshipGroup[], details?: ServingResourceDetail[]): string {
  const services = groups.find((group) => group.label === 'Services')?.refs ?? []
  const entrypoints = groups.find((group) => group.label === 'Entry points')?.refs ?? []
  const serviceResources = (details ?? [])
    .filter((detail) => services.some((ref) => resourceRefId(ref) === resourceRefId(detail.ref)))
    .map((detail) => detail.resource)
    .filter(Boolean)
  const serviceTypes = new Set(serviceResources.map((service) => service?.spec?.type || 'ClusterIP'))
  if (entrypoints.length > 0) return 'External entrypoint'
  if ([...serviceTypes].some((type) => ['LoadBalancer', 'NodePort', 'ExternalName'].includes(String(type)))) return 'External service'
  if (services.length > 0) return 'Internal service'
  return 'None'
}

interface SummaryMetricConfig {
  label: string
  value: ReactNode
  detail?: ReactNode
  icon: ComponentType<{ className?: string }>
  mono?: boolean
}

function buildSummaryMetrics(
  resource: any,
  apiKind: string,
  resourceKind: string,
  readiness: ReadinessSummary,
  podSummary: ReturnType<typeof getPodSummary>,
  scaling: ScalingSummary,
): SummaryMetricConfig[] {
  const spec = resource?.spec || {}
  const status = resource?.status || {}
  const k = apiKind.toLowerCase()

  if (k === 'jobs') {
    const duration = formatJobDuration(resource)
    return [
      {
        label: 'State',
        value: <span className={clsx('badge', readiness.healthBadge)}>{readiness.healthLabel}</span>,
        detail: resourceKind,
        icon: readiness.hasIssue ? AlertTriangle : Activity,
      },
      {
        label: 'Progress',
        value: readiness.readyLabel,
        detail: readiness.detail,
        icon: CheckCircle2,
      },
      {
        label: 'Duration',
        value: duration || '-',
        detail: status.startTime ? (status.completionTime ? 'finished run' : 'current run') : 'not started',
        icon: Clock3,
      },
      {
        label: 'Pods',
        value: podSummary.label,
        detail: podSummary.detail,
        icon: Boxes,
      },
    ]
  }

  if (k === 'cronjobs') {
    const active = Array.isArray(status.active) ? status.active.length : 0
    return [
      {
        label: 'State',
        value: <span className={clsx('badge', readiness.healthBadge)}>{readiness.healthLabel}</span>,
        detail: spec.suspend ? 'schedule suspended' : active > 0 ? 'run in progress' : 'waiting for next run',
        icon: readiness.hasIssue ? AlertTriangle : Clock3,
      },
      {
        label: 'Schedule',
        value: spec.schedule || '-',
        detail: spec.schedule ? cronToHuman(spec.schedule) : 'cron unavailable',
        icon: Clock3,
        mono: Boolean(spec.schedule),
      },
      {
        label: 'Latest run',
        value: status.lastScheduleTime ? formatAge(status.lastScheduleTime) : 'Never',
        detail: status.lastSuccessfulTime ? `last success ${formatAge(status.lastSuccessfulTime)}` : 'no successful run recorded',
        icon: Activity,
      },
      {
        label: 'Active jobs',
        value: active,
        detail: active > 0 ? 'currently running' : 'none running',
        icon: Boxes,
      },
    ]
  }

  const control = runtimeControlSurface(apiKind)
  return [
    {
      label: 'State',
      value: <span className={clsx('badge', readiness.healthBadge)}>{readiness.healthLabel}</span>,
      detail: resourceKind,
      icon: readiness.hasIssue ? AlertTriangle : CheckCircle2,
    },
    {
      label: 'Readiness',
      value: readiness.readyLabel,
      detail: readiness.detail,
      icon: Server,
    },
    {
      label: 'Pods',
      value: podSummary.label,
      detail: podSummary.detail,
      icon: Boxes,
    },
    {
      label: control.metricLabel,
      value: scaling.label,
      detail: scaling.detail,
      icon: control.icon,
    },
  ]
}

interface ReadinessSummary {
  readyLabel: string
  detail: string
  healthLabel: string
  healthBadge: string
  hasIssue: boolean
  stats: Array<{ label: string; value: ReactNode; tone?: 'default' | 'good' | 'warn' }>
}

function getReadinessSummary(resource: any, apiKind: string): ReadinessSummary {
  const status = resource?.status || {}
  const spec = resource?.spec || {}
  const k = apiKind.toLowerCase()

  if (k === 'daemonsets') {
    const desired = status.desiredNumberScheduled ?? 0
    const ready = status.numberReady ?? 0
    const updated = status.updatedNumberScheduled ?? 0
    const available = status.numberAvailable ?? ready
    const unavailable = status.numberUnavailable ?? Math.max(0, desired - available)
    const noTargets = desired === 0
    const healthy = !noTargets && ready === desired && unavailable === 0
    return {
      readyLabel: `${ready}/${desired}`,
      detail: `${updated}/${desired} up-to-date`,
      healthLabel: noTargets ? 'No targets' : healthy ? 'Healthy' : 'Degraded',
      healthBadge: noTargets ? 'bg-theme-elevated text-theme-text-secondary' : healthy ? 'status-healthy' : 'status-degraded',
      hasIssue: !healthy && !noTargets,
      stats: [
        { label: 'Desired', value: desired },
        { label: 'Ready', value: ready, tone: ready === desired ? 'good' : 'warn' },
        { label: 'Available', value: available },
        { label: 'Unavailable', value: unavailable, tone: unavailable > 0 ? 'warn' : 'default' },
      ],
    }
  }

  if (k === 'jobs') {
    const desired = spec.completions ?? 1
    const succeeded = status.succeeded ?? 0
    const active = status.active ?? 0
    const failed = status.failed ?? 0
    const phase = jobPhase(resource)
    const complete = phase === 'Complete'
    return {
      readyLabel: `${succeeded}/${desired}`,
      detail: complete ? 'completed successfully' : phase === 'Failed' ? jobConditionMessage(resource) || `${failed} failed` : active > 0 ? `${active} active` : 'waiting for pods',
      healthLabel: phase,
      healthBadge: phase === 'Failed' ? 'status-unhealthy' : phase === 'Pending' ? 'status-degraded' : complete ? 'status-healthy' : 'status-neutral',
      hasIssue: phase === 'Failed',
      stats: [
        { label: 'Completions', value: `${succeeded}/${desired}`, tone: complete ? 'good' : 'default' },
        { label: 'Active', value: active },
        { label: 'Failed attempts', value: failed, tone: phase === 'Failed' ? 'warn' : 'default' },
        { label: 'Parallelism', value: spec.parallelism ?? '-' },
      ],
    }
  }

  if (k === 'cronjobs') {
    const active = Array.isArray(status.active) ? status.active.length : 0
    const suspended = !!spec.suspend
    const lastSchedule = status.lastScheduleTime ? Date.parse(status.lastScheduleTime) : 0
    const lastSuccess = status.lastSuccessfulTime ? Date.parse(status.lastSuccessfulTime) : 0
    const missingRecentSuccess = !suspended && active === 0 && lastSchedule > 0 && lastSchedule > lastSuccess
    return {
      readyLabel: active > 0 ? `${active} active` : 'Idle',
      detail: spec.schedule ? `schedule ${spec.schedule}` : 'scheduled job',
      healthLabel: missingRecentSuccess ? 'No recent success' : suspended ? 'Suspended' : active > 0 ? 'Active' : 'Scheduled',
      healthBadge: missingRecentSuccess ? 'status-degraded' : suspended ? 'status-neutral' : active > 0 ? 'status-neutral' : 'status-healthy',
      hasIssue: missingRecentSuccess,
      stats: [
        { label: 'Schedule', value: spec.schedule || '-' },
        { label: 'Active jobs', value: active, tone: active > 0 ? 'warn' : 'default' },
        { label: 'Suspend', value: suspended ? 'Yes' : 'No', tone: suspended ? 'warn' : 'default' },
        { label: 'Last schedule', value: status.lastScheduleTime ? formatAge(status.lastScheduleTime) : '-' },
        { label: 'Last success', value: status.lastSuccessfulTime ? formatAge(status.lastSuccessfulTime) : '-' },
      ],
    }
  }

  const desired = spec.replicas ?? status.replicas ?? 0
  const current = status.replicas ?? desired
  const ready = status.readyReplicas ?? 0
  const updated = status.updatedReplicas ?? 0
  const available = status.availableReplicas ?? ready
  const unavailable = status.unavailableReplicas ?? Math.max(0, desired - available)
  const scaledToZero = desired === 0
  const healthy = !scaledToZero && ready === desired && unavailable === 0
  return {
    readyLabel: `${ready}/${desired}`,
    detail: `${updated}/${desired} updated · ${available} available`,
    healthLabel: scaledToZero ? 'Scaled to zero' : healthy ? 'Healthy' : 'Degraded',
    healthBadge: scaledToZero ? 'bg-theme-elevated text-theme-text-secondary' : healthy ? 'status-healthy' : 'status-degraded',
    hasIssue: !healthy && !scaledToZero,
    stats: [
      { label: 'Desired', value: desired },
      { label: 'Current', value: current },
      { label: 'Ready', value: ready, tone: ready === desired ? 'good' : 'warn' },
      { label: 'Updated', value: updated },
      { label: 'Available', value: available },
      { label: 'Unavailable', value: unavailable, tone: unavailable > 0 ? 'warn' : 'default' },
    ],
  }
}

function getPodSummary(workloadPods: WorkloadPodInfo[] | undefined, relationships: Relationships | undefined, apiKind?: string) {
  const k = apiKind?.toLowerCase()
  if (workloadPods) {
    const ready = workloadPods.filter((pod) => pod.ready).length
    const total = workloadPods.length
    const notReady = total - ready
    return {
      label: `${ready}/${total}`,
      detail: total === 0 ? (k === 'jobs' ? 'no pods retained' : 'no pods found') : notReady === 0 ? 'all ready' : `${notReady} need attention`,
      ready,
      total,
      notReady,
    }
  }
  const count = relationships?.pods?.length ?? 0
  return {
    label: count ? String(count) : '-',
    detail: count ? 'status unavailable' : k === 'jobs' ? 'no pods retained' : 'no pod refs',
    ready: undefined,
    total: count || undefined,
    notReady: undefined,
  }
}

interface ScalingSummary {
  label: string
  detail: string
  stats: Array<{ label: string; value: ReactNode; tone?: 'default' | 'good' | 'warn' }>
}

function runtimeControlSurface(apiKind: string): { metricLabel: string; cardTitle: string; icon: ComponentType<{ className?: string }> } {
  const k = apiKind.toLowerCase()
  if (k === 'cronjobs') return { metricLabel: 'Schedule', cardTitle: 'Schedule', icon: Clock3 }
  if (k === 'jobs') return { metricLabel: 'Execution', cardTitle: 'Execution', icon: Activity }
  return { metricLabel: 'Scaling', cardTitle: 'Scaling', icon: Scale }
}

function getScalingSummary(
  resource: any,
  apiKind: string,
  relationships: Relationships | undefined,
  scalerDiagnostics: ScalerDiagnosis[] | undefined,
  hpaDiagnosis: HPADiagnosis | undefined,
): ScalingSummary {
  const spec = resource?.spec || {}
  const status = resource?.status || {}
  const k = apiKind.toLowerCase()
  const scalers = relationships?.scalers ?? []
  const primaryDiagnosis = scalerDiagnostics?.find((entry) => entry.diagnosis)?.diagnosis ?? hpaDiagnosis

  if (k === 'daemonsets') {
    return {
      label: 'Node scheduled',
      detail: `${status.desiredNumberScheduled ?? 0} desired nodes`,
      stats: [
        { label: 'Controller', value: 'DaemonSet' },
        { label: 'Desired nodes', value: status.desiredNumberScheduled ?? '-' },
        { label: 'Misscheduled', value: status.numberMisscheduled ?? 0, tone: (status.numberMisscheduled ?? 0) > 0 ? 'warn' : 'default' },
      ],
    }
  }

  if (k === 'jobs') {
    const phase = jobPhase(resource)
    return {
      label: phase,
      detail: '',
      stats: [
        { label: 'Started', value: status.startTime ? formatAge(status.startTime) : '-' },
        { label: 'Finished', value: status.completionTime ? formatAge(status.completionTime) : phase === 'Running' ? 'Running' : '-' },
        { label: 'Parallelism', value: spec.parallelism ?? 1 },
        { label: 'Completions', value: spec.completions ?? 1 },
        { label: 'Completion mode', value: spec.completionMode ?? 'NonIndexed' },
        { label: 'Backoff limit', value: spec.backoffLimit ?? 6 },
        { label: 'TTL after finish', value: spec.ttlSecondsAfterFinished != null ? `${spec.ttlSecondsAfterFinished}s` : 'None' },
      ],
    }
  }

  if (k === 'cronjobs') {
    return {
      label: spec.suspend ? 'Suspended' : 'Scheduled',
      detail: '',
      stats: [
        { label: 'Schedule', value: spec.schedule ?? '-' },
        { label: 'Human', value: spec.schedule ? cronToHuman(spec.schedule) : '-' },
        { label: 'Concurrency', value: spec.concurrencyPolicy ?? 'Allow' },
        { label: 'Starting deadline', value: spec.startingDeadlineSeconds ? `${spec.startingDeadlineSeconds}s` : 'None' },
        { label: 'Success history', value: spec.successfulJobsHistoryLimit ?? 3 },
        { label: 'Failed history', value: spec.failedJobsHistoryLimit ?? 1 },
        { label: 'Suspend', value: spec.suspend ? 'Yes' : 'No', tone: spec.suspend ? 'warn' : 'default' },
      ],
    }
  }

  if (scalers.length > 0 || primaryDiagnosis) {
    return {
      label: scalers.length > 0 ? `${scalers.length} controller${scalers.length === 1 ? '' : 's'}` : hpaStateLabel(primaryDiagnosis!.state),
      detail: primaryDiagnosis ? hpaStateLabel(primaryDiagnosis.state) : 'replicas controlled externally',
      stats: [
        { label: 'Desired', value: primaryDiagnosis?.bounds?.desired ?? spec.replicas ?? '-' },
        { label: 'Current', value: primaryDiagnosis?.bounds?.current ?? status.replicas ?? '-' },
        { label: 'Min', value: primaryDiagnosis?.bounds?.min ?? '-' },
        { label: 'Max', value: primaryDiagnosis?.bounds?.max ?? '-' },
      ],
    }
  }

  return {
    label: spec.replicas !== undefined ? `${spec.replicas} replicas` : 'Manual',
    detail: 'replicas from workload spec',
    stats: [
      { label: 'Desired', value: spec.replicas ?? '-' },
      { label: 'Current', value: status.replicas ?? '-' },
      { label: 'Strategy', value: spec.strategy?.type || spec.updateStrategy?.type || '-' },
    ],
  }
}

function CurrentStateCard({ resource, apiKind, readiness }: { resource: any; apiKind: string; readiness: ReadinessSummary }) {
  const metadata = resource?.metadata || {}
  const templateSpec = getPodTemplateSpec(resource, apiKind)
  const containers = templateSpec?.containers ?? []
  const image = containers[0]?.image
  const k = apiKind.toLowerCase()
  const title = k === 'jobs' ? 'Execution state' : k === 'cronjobs' ? 'Schedule state' : 'Current state'
  const generation = metadata.generation
  const observedGeneration = resource?.status?.observedGeneration
  const controllerBehind =
    typeof generation === 'number' &&
    typeof observedGeneration === 'number' &&
    observedGeneration < generation

  return (
    <OverviewCard title={title} icon={k === 'jobs' ? Activity : k === 'cronjobs' ? Clock3 : Server}>
      <div className="grid gap-x-8 gap-y-3 sm:grid-cols-2 lg:grid-cols-3">
        {readiness.stats.map((stat) => (
          <OverviewStat key={String(stat.label)} label={stat.label} value={stat.value} tone={stat.tone} />
        ))}
        {controllerBehind && (
          <>
            <OverviewStat label="Spec generation" value={generation} tone="warn" />
            <OverviewStat label="Controller seen" value={observedGeneration} tone="warn" />
          </>
        )}
        <OverviewStat label="Age" value={metadata.creationTimestamp ? formatAge(metadata.creationTimestamp) : '-'} />
      </div>
      <div className="mt-4 grid gap-2 border-t border-theme-border pt-3 text-sm sm:grid-cols-2">
        <InlineFact label="Kind" value={displayKindName(apiKind, resource?.kind)} />
        {templateSpec && <InlineFact label="Service account" value={templateSpec.serviceAccountName || 'default'} />}
        {image && <InlineFact label="Image" value={<span className="font-mono text-xs">{image}</span>} />}
      </div>
    </OverviewCard>
  )
}

function getPodTemplateSpec(resource: any, apiKind: string) {
  const spec = resource?.spec || {}
  return apiKind.toLowerCase() === 'cronjobs'
    ? spec.jobTemplate?.spec?.template?.spec
    : spec.template?.spec
}

function jobPhase(job: any): 'Complete' | 'Failed' | 'Suspended' | 'Running' | 'Pending' {
  const status = job?.status || {}
  const spec = job?.spec || {}
  const conditions = Array.isArray(status.conditions) ? status.conditions : []
  if (conditions.some((condition: any) => condition.type === 'Failed' && condition.status === 'True')) return 'Failed'
  if (conditions.some((condition: any) => condition.type === 'Complete' && condition.status === 'True')) return 'Complete'
  if (spec.suspend) return 'Suspended'
  if ((status.active ?? 0) > 0) return 'Running'
  return 'Pending'
}

function jobConditionMessage(job: any): string {
  const conditions = Array.isArray(job?.status?.conditions) ? job.status.conditions : []
  const condition = conditions.find((entry: any) => (entry.type === 'Failed' || entry.type === 'Complete') && entry.status === 'True')
  return condition?.message || condition?.reason || ''
}

function formatJobDuration(job: any): string {
  const startTime = job?.status?.startTime
  if (!startTime) return ''
  const start = Date.parse(startTime)
  const endRaw = job?.status?.completionTime
  const end = endRaw ? Date.parse(endRaw) : Date.now()
  if (Number.isNaN(start) || Number.isNaN(end) || end < start) return ''
  return formatDuration(end - start, true)
}

function OverviewStat({ label, value, tone = 'default' }: { label: string; value: ReactNode; tone?: 'default' | 'good' | 'warn' }) {
  return (
    <div className="min-w-0">
      <div className="text-[10px] font-semibold uppercase tracking-wide text-theme-text-tertiary">{label}</div>
      <div className={clsx(
        'mt-1 truncate text-sm font-semibold',
        tone === 'good' ? 'text-emerald-500' : tone === 'warn' ? 'text-amber-500' : 'text-theme-text-primary',
      )}>
        {value}
      </div>
    </div>
  )
}

function InlineFact({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="min-w-0">
      <span className="text-theme-text-tertiary">{label}</span>
      <span className="mx-1 text-theme-text-tertiary">·</span>
      <span className="break-all text-theme-text-primary">{value}</span>
    </div>
  )
}

function RuntimeCard({
  resource,
  apiKind,
  state,
  readiness,
  scaling,
  namespace,
  workloadPods,
  workloadPodsLoading,
  workloadPodsError,
  relationshipPods,
  scalerDiagnostics,
  hpaDiagnosis,
  relationships,
  onNavigate,
  onSwitchToLogs,
}: {
  resource: any
  apiKind: string
  state: ReplicatedWorkloadState
  readiness: ReadinessSummary
  scaling: ScalingSummary
  namespace: string
  workloadPods?: WorkloadPodInfo[]
  workloadPodsLoading?: boolean
  workloadPodsError?: Error | null
  relationshipPods?: ResourceRef[]
  scalerDiagnostics?: ScalerDiagnosis[]
  hpaDiagnosis?: HPADiagnosis
  relationships?: Relationships
  onNavigate?: NavigateToResource
  onSwitchToLogs?: () => void
}) {
  const progress = rolloutProgress(resource, apiKind, state)
  const headline = rolloutHeadline(resource, apiKind, readiness, scaling)
  const facts = rolloutFacts(resource, apiKind, scaling, Boolean(progress))
  const scalerRefs = relationships?.scalers ?? []
  const navigateRef = onNavigate ? (ref: ResourceRef) => onNavigate(refToSelectedResource(ref)) : undefined

  return (
    <OverviewCard
      title="Runtime"
      icon={Boxes}
      action={onSwitchToLogs && (
        <button type="button" onClick={onSwitchToLogs} className="inline-flex items-center gap-1 text-xs text-accent-text hover:underline">
          <Terminal className="h-3.5 w-3.5" />
          Logs
        </button>
      )}
    >
      <div className="space-y-4">
        <div className="space-y-3">
          <div className="flex min-w-0 items-center gap-2">
            <span className={clsx('h-2 w-2 shrink-0 rounded-full', rolloutStateDotClass(state.level))} />
            <div className="min-w-0 truncate text-sm font-medium text-theme-text-primary">{headline}</div>
          </div>

          <div className="flex flex-wrap gap-1.5">
            {facts.map((fact) => (
              <RolloutFactChip key={String(fact.label)} label={fact.label} value={fact.value} tone={fact.tone} />
            ))}
          </div>

          {progress && <RolloutProgressBar progress={progress} state={state} />}

          {scalerRefs.length > 0 ? (
            <div className="rounded-md border border-theme-border bg-theme-base px-3 py-2">
              <div className="mb-1 text-xs font-medium text-theme-text-tertiary">Controlled by</div>
              <div className="flex flex-wrap gap-1.5">
                {scalerRefs.slice(0, RELATIONSHIP_REF_LIMIT).map((ref) => (
                  <ResourceRefBadge key={`${ref.kind}/${ref.namespace}/${ref.name}`} resourceRef={ref} onClick={navigateRef} wrapAtSeparator />
                ))}
              </div>
              {scalerDiagnostics?.map((entry) => (
                <div key={`${entry.ref.kind}/${entry.ref.namespace}/${entry.ref.name}`} className="mt-2 text-xs text-theme-text-secondary">
                  {entry.loading ? 'Loading autoscaler diagnosis...' : entry.error ? 'Autoscaler diagnosis unavailable' : entry.diagnosis ? hpaStateLabel(entry.diagnosis.state) : 'Autoscaler status unavailable'}
                  {entry.diagnosis?.summary && <span className="ml-1 text-theme-text-tertiary">{entry.diagnosis.summary}</span>}
                </div>
              ))}
            </div>
          ) : hpaDiagnosis ? (
            <div className="rounded-md border border-theme-border bg-theme-base px-3 py-2 text-xs text-theme-text-secondary">
              {hpaStateLabel(hpaDiagnosis.state)} <span className="text-theme-text-tertiary">{hpaDiagnosis.summary}</span>
            </div>
          ) : null}
        </div>

        <div className="border-t border-theme-border pt-3">
          <div className="mb-2 text-xs font-medium text-theme-text-tertiary">Pods</div>
          <PodsListContent
            namespace={namespace}
            workloadPods={workloadPods}
            workloadPodsLoading={workloadPodsLoading}
            workloadPodsError={workloadPodsError}
            relationshipPods={relationshipPods}
            onNavigate={onNavigate}
          />
        </div>
      </div>
    </OverviewCard>
  )
}

function PodsAttentionCard({
  namespace,
  emptyText,
  workloadPods,
  workloadPodsLoading,
  workloadPodsError,
  relationshipPods,
  onNavigate,
  onSwitchToLogs,
}: {
  namespace: string
  emptyText?: string
  workloadPods?: WorkloadPodInfo[]
  workloadPodsLoading?: boolean
  workloadPodsError?: Error | null
  relationshipPods?: ResourceRef[]
  onNavigate?: NavigateToResource
  onSwitchToLogs?: () => void
}) {
  return (
    <OverviewCard
      title="Pods"
      icon={Boxes}
      action={onSwitchToLogs && (
        <button type="button" onClick={onSwitchToLogs} className="inline-flex items-center gap-1 text-xs text-accent-text hover:underline">
          <Terminal className="h-3.5 w-3.5" />
          Logs
        </button>
      )}
    >
      <PodsListContent
        namespace={namespace}
        emptyText={emptyText}
        workloadPods={workloadPods}
        workloadPodsLoading={workloadPodsLoading}
        workloadPodsError={workloadPodsError}
        relationshipPods={relationshipPods}
        onNavigate={onNavigate}
      />
    </OverviewCard>
  )
}

function PodsListContent({
  namespace,
  emptyText,
  workloadPods,
  workloadPodsLoading,
  workloadPodsError,
  relationshipPods,
  onNavigate,
}: {
  namespace: string
  emptyText?: string
  workloadPods?: WorkloadPodInfo[]
  workloadPodsLoading?: boolean
  workloadPodsError?: Error | null
  relationshipPods?: ResourceRef[]
  onNavigate?: NavigateToResource
}) {
  const sortedPods = [...(workloadPods ?? [])].sort(compareWorkloadPods)
  const [expandedPods, setExpandedPods] = useState(false)
  const hasPodOverflow = sortedPods.length > POD_VISIBLE_LIMIT
  const visiblePods = sortedPods.slice(0, POD_VISIBLE_LIMIT)
  const overflowPods = sortedPods.slice(POD_VISIBLE_LIMIT)
  const hiddenPodCount = Math.max(0, sortedPods.length - POD_VISIBLE_LIMIT)
  const fallbackPodRefs = relationshipPods ?? []
  const hasFallbackOverflow = fallbackPodRefs.length > POD_VISIBLE_LIMIT
  const fallbackPods = fallbackPodRefs.slice(0, POD_VISIBLE_LIMIT)
  const overflowFallbackPods = fallbackPodRefs.slice(POD_VISIBLE_LIMIT)
  const hiddenFallbackCount = Math.max(0, fallbackPodRefs.length - POD_VISIBLE_LIMIT)

  return (
    <>
      {workloadPodsLoading ? (
        <div className="text-sm text-theme-text-tertiary">Loading pod readiness…</div>
      ) : workloadPodsError ? (
        <div className="rounded-md border border-amber-500/30 bg-amber-500/10 p-3 text-sm text-amber-600 dark:text-amber-300">
          Pod readiness unavailable. Use Logs or Timeline for deeper inspection.
        </div>
      ) : workloadPods ? (
        visiblePods.length > 0 ? (
          <PodListFrame
            expanded={expandedPods}
            hasOverflow={hasPodOverflow}
            overflow={overflowPods.map((pod) => (
              <PodRow
                key={pod.name}
                name={pod.name}
                namespace={namespace}
                ready={pod.ready}
                healthLevel={pod.healthLevel}
                detail={workloadPodDetail(pod)}
                onNavigate={onNavigate}
              />
            ))}
            toggle={(
              <PodListToggle
                expanded={expandedPods}
                hiddenCount={hiddenPodCount}
                label="pods"
                onToggle={() => setExpandedPods((value) => !value)}
              />
            )}
          >
            {visiblePods.map((pod) => (
              <PodRow
                key={pod.name}
                name={pod.name}
                namespace={namespace}
                ready={pod.ready}
                healthLevel={pod.healthLevel}
                detail={workloadPodDetail(pod)}
                onNavigate={onNavigate}
              />
            ))}
          </PodListFrame>
        ) : (
          <div className="text-sm text-theme-text-tertiary">{emptyText || 'No pods currently match this workload.'}</div>
        )
      ) : fallbackPods.length > 0 ? (
        <PodListFrame
          expanded={expandedPods}
          hasOverflow={hasFallbackOverflow}
          overflow={overflowFallbackPods.map((pod) => (
            <PodRow
              key={`${pod.namespace}/${pod.name}`}
              name={pod.name}
              namespace={pod.namespace || namespace}
              ready={null}
              detail="status unavailable"
              onNavigate={onNavigate}
            />
          ))}
          toggle={(
            <PodListToggle
              expanded={expandedPods}
              hiddenCount={hiddenFallbackCount}
              label="pods"
              onToggle={() => setExpandedPods((value) => !value)}
            />
          )}
        >
          {fallbackPods.map((pod) => (
            <PodRow
              key={`${pod.namespace}/${pod.name}`}
              name={pod.name}
              namespace={pod.namespace || namespace}
              ready={null}
              detail="status unavailable"
              onNavigate={onNavigate}
            />
          ))}
        </PodListFrame>
      ) : (
        <div className="text-sm text-theme-text-tertiary">{emptyText || 'No pod relationships found.'}</div>
      )}
    </>
  )
}

function PodRow({
  name,
  namespace,
  ready,
  healthLevel,
  detail,
  onNavigate,
}: {
  name: string
  namespace: string
  ready: boolean | null
  healthLevel?: WorkloadPodInfo['healthLevel']
  detail: string
  onNavigate?: NavigateToResource
}) {
  const statusLabel = podStatusLabel(healthLevel, ready)
  const statusClass = podStatusClass(healthLevel, ready)
  const content = (
    <>
      <span className={clsx('h-2 w-2 shrink-0 rounded-full', podDotClass(healthLevel, ready))} />
      <span className="min-w-0 flex-1">
        <span className="block truncate text-sm font-medium text-theme-text-primary">{name}</span>
        <span className="block truncate text-xs text-theme-text-tertiary">{detail}</span>
      </span>
      <span className={clsx('badge-sm shrink-0', statusClass)}>
        {statusLabel}
      </span>
    </>
  )

  if (!onNavigate) {
    return (
      <div className="flex w-full min-w-0 items-center gap-3 rounded-md border border-theme-border bg-theme-base px-3 py-2 text-left">
        {content}
      </div>
    )
  }

  return (
    <button
      type="button"
      onClick={() => onNavigate({ kind: 'pods', namespace, name })}
      className="flex w-full min-w-0 items-center gap-3 rounded-md border border-theme-border bg-theme-base px-3 py-2 text-left transition-colors hover:bg-theme-hover"
    >
      {content}
    </button>
  )
}

function PodListFrame({
  expanded,
  hasOverflow,
  overflow,
  toggle,
  children,
}: {
  expanded: boolean
  hasOverflow: boolean
  overflow?: ReactNode
  toggle?: ReactNode
  children: ReactNode
}) {
  return (
    <div className="space-y-2">
      <div className="space-y-2">
        {children}
      </div>
      {hasOverflow && (
        <div
          className={clsx(
            'grid transition-[grid-template-rows,opacity] duration-200',
            expanded ? 'grid-rows-[1fr] opacity-100' : 'grid-rows-[0fr] opacity-0',
          )}
          style={{ transitionTimingFunction: 'cubic-bezier(0.16, 1, 0.3, 1)' }}
          aria-hidden={!expanded || undefined}
        >
          <div className={clsx('min-h-0 overflow-hidden', expanded && 'max-h-[26rem] overflow-y-auto pr-1')} inert={!expanded || undefined}>
            <div className="space-y-2">
              {overflow}
            </div>
          </div>
        </div>
      )}
      {toggle}
    </div>
  )
}

function PodListToggle({
  expanded,
  hiddenCount,
  label,
  onToggle,
}: {
  expanded: boolean
  hiddenCount: number
  label: string
  onToggle: () => void
}) {
  if (!expanded && hiddenCount <= 0) return null

  return (
    <div className="pt-1">
      <button
        type="button"
        aria-expanded={expanded}
        onClick={onToggle}
        className="text-xs font-medium text-accent-text hover:underline"
      >
        {expanded ? `Collapse ${label}` : `Show ${hiddenCount} more ${label}`}
      </button>
    </div>
  )
}

function compareWorkloadPods(a: WorkloadPodInfo, b: WorkloadPodInfo): number {
  const severity = podSeverityRank(b) - podSeverityRank(a)
  if (severity !== 0) return severity
  const restartDiff = (b.restartCount ?? 0) - (a.restartCount ?? 0)
  if (restartDiff !== 0) return restartDiff
  return a.name.localeCompare(b.name)
}

function podSeverityRank(pod: WorkloadPodInfo): number {
  switch (pod.healthLevel) {
    case 'unhealthy': return 4
    case 'degraded': return 3
    case 'unknown': return 2
    case 'neutral': return pod.ready ? 0 : 1
    case 'healthy': return pod.ready ? 0 : 1
    default: return pod.ready ? 0 : 2
  }
}

function workloadPodDetail(pod: WorkloadPodInfo): string {
  const parts: string[] = []
  if (pod.phase) parts.push(pod.reason ? `${pod.phase} / ${pod.reason}` : pod.phase)
  else if (pod.reason) parts.push(pod.reason)
  parts.push(`${pod.containers.length} container${pod.containers.length === 1 ? '' : 's'}`)
  if ((pod.restartCount ?? 0) > 0) parts.push(`${pod.restartCount} restart${pod.restartCount === 1 ? '' : 's'}`)
  if (pod.lastTerminationReason && !['Unknown', 'Completed'].includes(pod.lastTerminationReason)) {
    parts.push(`last ${pod.lastTerminationReason}`)
  }
  if (pod.createdAt) parts.push(`${formatAge(pod.createdAt)} old`)
  return parts.join(' · ')
}

function podStatusLabel(healthLevel: WorkloadPodInfo['healthLevel'] | undefined, ready: boolean | null): string {
  if (healthLevel === 'unhealthy') return 'Unhealthy'
  if (healthLevel === 'degraded') return 'Degraded'
  if (healthLevel === 'neutral') return ready ? 'Ready' : 'Neutral'
  if (healthLevel === 'unknown') return 'Unknown'
  return ready === null ? 'Unknown' : ready ? 'Ready' : 'Not ready'
}

function podStatusClass(healthLevel: WorkloadPodInfo['healthLevel'] | undefined, ready: boolean | null): string {
  if (healthLevel === 'unhealthy') return 'status-unhealthy'
  if (healthLevel === 'degraded') return 'status-degraded'
  if (healthLevel === 'neutral') return 'status-neutral'
  if (healthLevel === 'unknown' || ready === null) return 'status-unknown'
  return ready ? 'status-healthy' : 'status-degraded'
}

function podDotClass(healthLevel: WorkloadPodInfo['healthLevel'] | undefined, ready: boolean | null): string {
  if (healthLevel === 'unhealthy') return 'bg-red-500'
  if (healthLevel === 'degraded') return 'bg-amber-500'
  if (healthLevel === 'neutral') return 'bg-skyhook-500'
  if (healthLevel === 'unknown' || ready === null) return 'bg-theme-text-tertiary'
  return ready ? 'bg-emerald-500' : 'bg-amber-500'
}

function MoreRows({ count, label }: { count: number; label: string }) {
  return <div className="text-xs text-theme-text-tertiary">+{count} more {label} hidden from this overview.</div>
}

type RelationshipGroup = { label: string; refs: ResourceRef[] }

function ServingConfigurationCard({
  groups,
  details = [],
  renderPortAction,
  renderPortPanel,
  onNavigate,
  onSwitchToTopology,
}: {
  groups: RelationshipGroup[]
  details?: ServingResourceDetail[]
  renderPortAction?: (props: ServicePortRenderProps) => ReactNode
  renderPortPanel?: (props: ServicePortRenderProps) => ReactNode
  onNavigate?: NavigateToResource
  onSwitchToTopology?: () => void
}) {
  const services = groups.find((group) => group.label === 'Services')?.refs ?? []
  const entrypoints = groups.find((group) => group.label === 'Entry points')?.refs ?? []
  const detailByRef = new Map(details.map((detail) => [resourceRefId(detail.ref), detail]))
  const serviceDetails = services.map((ref) => detailByRef.get(resourceRefId(ref))).filter(Boolean) as ServingResourceDetail[]
  const entrypointDetails = entrypoints.map((ref) => detailByRef.get(resourceRefId(ref))).filter(Boolean) as ServingResourceDetail[]
  const serviceResources = serviceDetails.map((detail) => detail.resource).filter(Boolean)
  const entrypointResources = entrypointDetails.map((detail) => detail.resource).filter(Boolean)
  const serviceDetailsLoading = serviceDetails.some((detail) => detail.loading)
  const serviceCount = services.length
  const entrypointCount = entrypoints.length
  const serviceTypes = summarizeServiceTypes(serviceResources, serviceCount, serviceDetailsLoading)
  const externalLabels = summarizeExternalLabels(serviceResources, entrypointResources)
  const exposure = summarizeServingExposure(serviceResources, serviceCount, entrypointCount, externalLabels, serviceDetailsLoading)
  const pathDetail =
    entrypointCount > 0 && serviceCount > 0
      ? `${entrypointCount} entry point${entrypointCount === 1 ? '' : 's'} route to ${serviceCount} service${serviceCount === 1 ? '' : 's'}.`
      : entrypointCount > 0
        ? `${entrypointCount} entry point${entrypointCount === 1 ? '' : 's'} detected. Use Topology to inspect the serving graph.`
      : `${serviceCount} service${serviceCount === 1 ? ' targets' : 's target'} this workload without an ingress or route.`
  const navigateRef = onNavigate ? (ref: ResourceRef) => onNavigate(refToSelectedResource(ref)) : undefined

  return (
    <OverviewCard
      title="Serving configuration"
      icon={Network}
      action={onSwitchToTopology && (
        <button type="button" onClick={onSwitchToTopology} className="inline-flex items-center gap-1 text-xs text-accent-text hover:underline">
          <GitBranch className="h-3.5 w-3.5" />
          Topology
        </button>
      )}
    >
      <div className="space-y-3">
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <span className="text-base font-semibold text-theme-text-primary">{exposure.label}</span>
          <Badge severity={exposure.severity} size="sm">{exposure.badge}</Badge>
          <span className="min-w-[12rem] flex-1 text-sm text-theme-text-secondary">{pathDetail}</span>
        </div>
        <div className="grid gap-4 lg:grid-cols-[minmax(0,1.35fr)_minmax(18rem,0.8fr)]">
          <div className="min-w-0 space-y-3">
            <div className="grid gap-3 sm:grid-cols-2">
              <OverviewStat label="Service type" value={serviceTypes} />
              <OverviewStat label="Services" value={serviceCount} />
            </div>
            <ServingPortRows
              services={serviceResources}
              loading={serviceDetailsLoading}
              renderPortAction={renderPortAction}
              renderPortPanel={renderPortPanel}
            />
            <ServingRefChips label="Services" refs={services} onNavigate={navigateRef} />
          </div>
          <div className="min-w-0 space-y-3 border-t border-theme-border pt-3 lg:border-l lg:border-t-0 lg:pl-4 lg:pt-0">
            <OverviewStat label="Entry points" value={entrypointCount || '-'} />
            <ServingFactChips label="Addresses & hosts" values={externalLabels} empty={entrypointCount > 0 ? 'Route details unavailable.' : 'Internal cluster traffic only.'} />
            {entrypoints.length > 0 && <ServingRefChips label="Entry point resources" refs={entrypoints} onNavigate={navigateRef} />}
          </div>
        </div>
      </div>
    </OverviewCard>
  )
}

function ServingPortRows({
  services,
  loading,
  renderPortAction,
  renderPortPanel,
}: {
  services: any[]
  loading?: boolean
  renderPortAction?: (props: ServicePortRenderProps) => ReactNode
  renderPortPanel?: (props: ServicePortRenderProps) => ReactNode
}) {
  const servicesWithPorts = services.filter((service) => (service?.spec?.ports ?? []).length > 0)
  const visible = servicesWithPorts.slice(0, 2)
  const hidden = Math.max(0, servicesWithPorts.length - visible.length)
  return (
    <div className="min-w-0">
      <div className="mb-1 text-xs font-medium text-theme-text-tertiary">Ports</div>
      {visible.length > 0 ? (
        <div className="space-y-2">
          {visible.map((service) => {
            const serviceName = service?.metadata?.name || 'service'
            return (
              <div key={service?.metadata?.uid || `${service?.metadata?.namespace}/${serviceName}`} className="min-w-0">
                {servicesWithPorts.length > 1 && (
                  <div className="mb-1 font-mono text-[11px] text-theme-text-tertiary">{serviceName}</div>
                )}
                <div className="max-w-2xl">
                  <ServicePortCards service={service} renderPortAction={renderPortAction} renderPortPanel={renderPortPanel} />
                </div>
              </div>
            )
          })}
          {hidden > 0 && <Badge tone="structural" size="sm">+{hidden} services</Badge>}
        </div>
      ) : (
        <div className="text-xs text-theme-text-tertiary">{loading ? 'Loading service ports...' : 'No service ports loaded.'}</div>
      )}
    </div>
  )
}

function ServingRefChips({ label, refs, onNavigate }: { label: string; refs: ResourceRef[]; onNavigate?: (ref: ResourceRef) => void }) {
  const visible = refs.slice(0, RELATIONSHIP_REF_LIMIT)
  const hidden = Math.max(0, refs.length - visible.length)
  return (
    <div className="min-w-0">
      <div className="mb-1 text-xs font-medium text-theme-text-tertiary">{label}</div>
      <div className="flex flex-wrap gap-1.5">
        {visible.map((ref) => (
          <ResourceRefBadge key={`${ref.kind}/${ref.namespace}/${ref.name}`} resourceRef={ref} onClick={onNavigate} wrapAtSeparator />
        ))}
        {hidden > 0 && <Badge tone="structural" size="sm">+{hidden}</Badge>}
      </div>
    </div>
  )
}

function ServingFactChips({ label, values, empty }: { label: string; values: string[]; empty: string }) {
  const visible = values.slice(0, RELATIONSHIP_REF_LIMIT)
  const hidden = Math.max(0, values.length - visible.length)
  return (
    <div>
      <div className="mb-1 text-xs font-medium text-theme-text-tertiary">{label}</div>
      {visible.length > 0 ? (
        <div className="flex flex-wrap gap-1.5">
          {visible.map((value) => (
            <Badge key={value} tone="structural" size="sm" className="max-w-full whitespace-normal break-all text-left">
              {value}
            </Badge>
          ))}
          {hidden > 0 && <Badge tone="structural" size="sm">+{hidden}</Badge>}
        </div>
      ) : (
        <div className="text-xs text-theme-text-tertiary">{empty}</div>
      )}
    </div>
  )
}

function CronJobRunsCard({
  resource,
  namespace,
  onNavigate,
  onSwitchToTopology,
}: {
  resource: any
  namespace: string
  onNavigate?: NavigateToResource
  onSwitchToTopology?: () => void
}) {
  const status = resource?.status || {}
  const spec = resource?.spec || {}
  const activeJobs = Array.isArray(status.active) ? status.active : []
  const visibleJobs = activeJobs.slice(0, POD_VISIBLE_LIMIT)
  const hiddenJobCount = Math.max(0, activeJobs.length - visibleJobs.length)
  const emptyMessage = spec.suspend
    ? 'No active Jobs. This CronJob is suspended, so new runs will not be scheduled until it is resumed.'
    : 'No active Jobs. The CronJob is waiting for the next scheduled run.'

  return (
    <OverviewCard
      title="Runs"
      icon={Activity}
      action={onSwitchToTopology && (
        <button type="button" onClick={onSwitchToTopology} className="inline-flex items-center gap-1 text-xs text-skyhook-500 hover:underline">
          <GitBranch className="h-3.5 w-3.5" />
          Topology
        </button>
      )}
    >
      <div className="mb-3 grid gap-x-8 gap-y-3 sm:grid-cols-2">
        <OverviewStat label="Active jobs" value={activeJobs.length} tone={activeJobs.length > 0 ? 'warn' : 'default'} />
        <OverviewStat label="Last schedule" value={status.lastScheduleTime ? formatAge(status.lastScheduleTime) : 'Never'} />
        <OverviewStat label="Last success" value={status.lastSuccessfulTime ? formatAge(status.lastSuccessfulTime) : 'Never'} />
        <OverviewStat label="Retained history" value={`${spec.successfulJobsHistoryLimit ?? 3} ok / ${spec.failedJobsHistoryLimit ?? 1} failed`} />
      </div>

      {visibleJobs.length > 0 ? (
        <div className="space-y-2">
          {visibleJobs.map((job: any) => {
            const ref: ResourceRef = {
              kind: job.kind || 'Job',
              namespace: job.namespace || namespace,
              name: job.name,
            }
            return (
              <button
                key={`${ref.namespace}/${ref.name}`}
                type="button"
                onClick={() => onNavigate?.(refToSelectedResource(ref))}
                className={clsx(
                  'flex w-full min-w-0 items-center gap-3 rounded-md border border-theme-border bg-theme-base px-3 py-2 text-left',
                  onNavigate && 'transition-colors hover:bg-theme-hover',
                )}
              >
                <span className="h-2 w-2 shrink-0 rounded-full bg-skyhook-500" />
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-sm font-medium text-theme-text-primary">{ref.name}</span>
                  <span className="block truncate text-xs text-theme-text-tertiary">{ref.namespace || 'default namespace'}</span>
                </span>
                <span className="badge-sm status-neutral">Active</span>
              </button>
            )
          })}
          {hiddenJobCount > 0 && <MoreRows count={hiddenJobCount} label="jobs" />}
        </div>
      ) : (
        <div className="rounded-md border border-theme-border bg-theme-base px-3 py-2 text-sm text-theme-text-tertiary">
          {emptyMessage}
        </div>
      )}
    </OverviewCard>
  )
}

function ScalingCard({
  title,
  icon,
  scaling,
  scalerDiagnostics,
  hpaDiagnosis,
  relationships,
  onNavigate,
}: {
  title: string
  icon: ComponentType<{ className?: string }>
  scaling: ScalingSummary
  scalerDiagnostics?: ScalerDiagnosis[]
  hpaDiagnosis?: HPADiagnosis
  relationships?: Relationships
  onNavigate?: NavigateToResource
}) {
  const scalerRefs = relationships?.scalers ?? []
  const navigateRef = onNavigate ? (ref: ResourceRef) => onNavigate(refToSelectedResource(ref)) : undefined

  return (
    <OverviewCard title={title} icon={icon}>
      <div className="mb-3 grid gap-x-8 gap-y-3 sm:grid-cols-2">
        {scaling.stats.map((stat) => (
          <OverviewStat key={String(stat.label)} label={stat.label} value={stat.value} tone={stat.tone} />
        ))}
      </div>
      {scalerRefs.length > 0 ? (
        <div className="space-y-2">
          <div className="text-xs font-medium text-theme-text-tertiary">Controlled by</div>
          <div className="flex flex-wrap gap-1.5">
            {scalerRefs.slice(0, RELATIONSHIP_REF_LIMIT).map((ref) => (
              <ResourceRefBadge key={`${ref.kind}/${ref.namespace}/${ref.name}`} resourceRef={ref} onClick={navigateRef} wrapAtSeparator />
            ))}
          </div>
          {scalerDiagnostics?.map((entry) => (
            <div key={`${entry.ref.kind}/${entry.ref.namespace}/${entry.ref.name}`} className="rounded-md border border-theme-border bg-theme-base px-3 py-2 text-xs text-theme-text-secondary">
              {entry.loading ? 'Loading autoscaler diagnosis…' : entry.error ? 'Autoscaler diagnosis unavailable' : entry.diagnosis ? hpaStateLabel(entry.diagnosis.state) : 'Autoscaler status unavailable'}
              {entry.diagnosis?.summary && <span className="ml-1 text-theme-text-tertiary">{entry.diagnosis.summary}</span>}
            </div>
          ))}
        </div>
      ) : hpaDiagnosis ? (
        <div className="rounded-md border border-theme-border bg-theme-base px-3 py-2 text-xs text-theme-text-secondary">
          {hpaStateLabel(hpaDiagnosis.state)} <span className="text-theme-text-tertiary">{hpaDiagnosis.summary}</span>
        </div>
      ) : scaling.detail ? (
        <div className="text-sm text-theme-text-secondary">{scaling.detail}</div>
      ) : null}
    </OverviewCard>
  )
}

interface RolloutProgressSummary {
  updated: number
  desired: number
  ready: number
  unavailable: number
  updatedLabel: string
  readyLabel: string
}

function RolloutProgressBar({ progress, state }: { progress: RolloutProgressSummary; state: ReplicatedWorkloadState }) {
  const converged = Math.min(progress.updated, progress.ready)
  const percent = progress.desired > 0 ? Math.max(0, Math.min(100, (converged / progress.desired) * 100)) : 0
  return (
    <div className="rounded-md border border-theme-border bg-theme-base px-3 py-2">
      <div className="mb-1.5 flex min-w-0 items-center justify-between gap-3 text-xs">
        <span className="font-medium text-theme-text-secondary">Rollout progress</span>
        <span className="min-w-0 truncate text-theme-text-tertiary">
          {progress.updatedLabel} · {progress.readyLabel}
        </span>
      </div>
      <div className="h-1.5 overflow-hidden rounded-full bg-theme-elevated">
        <div
          className={clsx('h-full rounded-full transition-[width]', rolloutProgressFillClass(state.level))}
          style={{ width: `${percent}%` }}
          aria-label={`${progress.updatedLabel}; ${progress.readyLabel}`}
        />
      </div>
      {progress.unavailable > 0 && (
        <div className="mt-1.5 text-xs text-amber-600 dark:text-amber-300">
          {progress.unavailable} unavailable while rollout converges.
        </div>
      )}
    </div>
  )
}

function rolloutProgressFillClass(level: ReplicatedWorkloadState['level']): string {
  if (level === 'unhealthy') return 'bg-red-500'
  if (level === 'degraded') return 'bg-amber-500'
  return 'bg-accent'
}

function RolloutFactChip({ label, value, tone = 'default' }: { label: string; value: ReactNode; tone?: 'default' | 'good' | 'warn' }) {
  return (
    <span className={clsx(
      'inline-flex min-w-0 max-w-full items-center gap-1 rounded-md border px-2 py-1 text-xs',
      tone === 'warn'
        ? 'border-amber-500/30 bg-amber-500/10 text-amber-600 dark:text-amber-300'
        : tone === 'good'
          ? 'border-emerald-500/30 bg-emerald-500/10 text-emerald-600 dark:text-emerald-300'
          : 'border-theme-border bg-theme-base text-theme-text-secondary',
    )}>
      <span className="shrink-0 text-theme-text-tertiary">{label}</span>
      <span className="min-w-0 truncate font-medium text-theme-text-primary">{value}</span>
    </span>
  )
}

function rolloutStateDotClass(level: ReplicatedWorkloadState['level']): string {
  if (level === 'unhealthy') return 'bg-red-500'
  if (level === 'degraded') return 'bg-amber-500'
  if (level === 'neutral') return 'bg-skyhook-500'
  if (level === 'unknown') return 'bg-theme-text-tertiary'
  return 'bg-emerald-500'
}

function rolloutFacts(
  resource: any,
  apiKind: string,
  scaling: ScalingSummary,
  progressVisible = false,
): Array<{ label: string; value: ReactNode; tone?: 'default' | 'good' | 'warn' }> {
  const spec = resource?.spec || {}
  const status = resource?.status || {}
  const metadata = resource?.metadata || {}
  const k = apiKind.toLowerCase()
  const generation = metadata.generation
  const observedGeneration = status.observedGeneration
  const controllerBehind =
    typeof generation === 'number' &&
    typeof observedGeneration === 'number' &&
    observedGeneration < generation
  const facts: Array<{ label: string; value: ReactNode; tone?: 'default' | 'good' | 'warn' }> = []

  if (k === 'daemonsets') {
    const desired = status.desiredNumberScheduled ?? 0
    const current = status.currentNumberScheduled ?? 0
    const updated = status.updatedNumberScheduled ?? 0
    const unavailable = status.numberUnavailable ?? 0
    const misscheduled = status.numberMisscheduled ?? 0
    facts.push({ label: 'Target nodes', value: desired })
    if (current !== desired) facts.push({ label: 'Scheduled', value: current, tone: 'warn' })
    if (!progressVisible && updated !== desired) facts.push({ label: 'Updated', value: `${updated}/${desired}`, tone: 'warn' })
    if (unavailable > 0) facts.push({ label: 'Unavailable', value: unavailable, tone: 'warn' })
    if (misscheduled > 0) facts.push({ label: 'Misscheduled', value: misscheduled, tone: 'warn' })
    facts.push({ label: 'Strategy', value: spec.updateStrategy?.type || 'RollingUpdate' })
    if (controllerBehind) facts.push({ label: 'Controller seen', value: observedGeneration, tone: 'warn' })
    return facts
  }

  const desired = spec.replicas ?? status.replicas ?? 0
  const current = status.replicas ?? desired
  const updated = status.updatedReplicas ?? 0
  const unavailable = status.unavailableReplicas ?? Math.max(0, desired - (status.availableReplicas ?? status.readyReplicas ?? 0))
  facts.push({ label: 'Target replicas', value: desired })
  if (current !== desired) facts.push({ label: 'Current', value: current, tone: 'warn' })
  if (!progressVisible && updated !== desired) facts.push({ label: 'Updated', value: `${updated}/${desired}`, tone: 'warn' })
  if (unavailable > 0) facts.push({ label: 'Unavailable', value: unavailable, tone: 'warn' })
  facts.push({ label: 'Strategy', value: spec.strategy?.type || spec.updateStrategy?.type || '-' })

  if (k === 'statefulsets') {
    if (status.updateRevision && status.currentRevision && status.updateRevision !== status.currentRevision) {
      facts.push({ label: 'Revision', value: `${midTruncate(status.currentRevision, 18)} -> ${midTruncate(status.updateRevision, 18)}`, tone: 'warn' })
    } else if (spec.updateStrategy?.rollingUpdate?.partition != null) {
      facts.push({ label: 'Partition', value: spec.updateStrategy.rollingUpdate.partition })
    }
  }

  if (controllerBehind) facts.push({ label: 'Controller seen', value: observedGeneration, tone: 'warn' })

  const externallyScaled = scaling.stats.some((stat) => !['Desired', 'Current', 'Strategy'].includes(String(stat.label)))
  if (externallyScaled) {
    for (const stat of scaling.stats) {
      const label = String(stat.label)
      if (!facts.some((existing) => existing.label === label)) facts.push(stat)
    }
  }
  return facts
}

function rolloutProgress(resource: any, apiKind: string, state: ReplicatedWorkloadState): RolloutProgressSummary | null {
  const spec = resource?.spec || {}
  const status = resource?.status || {}
  const k = apiKind.toLowerCase()

  if (k === 'daemonsets') {
    const desired = status.desiredNumberScheduled ?? 0
    const updated = status.updatedNumberScheduled ?? 0
    const ready = status.numberReady ?? 0
    const unavailable = status.numberUnavailable ?? Math.max(0, desired - ready)
    if (!shouldShowRolloutProgress(desired, updated, ready, unavailable, state)) return null
    return {
      updated,
      desired,
      ready,
      unavailable,
      updatedLabel: `${updated}/${desired} nodes updated`,
      readyLabel: `${ready}/${desired} nodes ready`,
    }
  }

  const desired = spec.replicas ?? status.replicas ?? 0
  const updated = status.updatedReplicas ?? 0
  const ready = status.readyReplicas ?? 0
  const unavailable = status.unavailableReplicas ?? Math.max(0, desired - (status.availableReplicas ?? ready))
  if (!shouldShowRolloutProgress(desired, updated, ready, unavailable, state)) return null
  return {
    updated,
    desired,
    ready,
    unavailable,
    updatedLabel: `${updated}/${desired} updated`,
    readyLabel: `${ready}/${desired} ready`,
  }
}

function shouldShowRolloutProgress(
  desired: number,
  updated: number,
  ready: number,
  unavailable: number,
  state: ReplicatedWorkloadState,
): boolean {
  if (desired <= 0) return false
  if (updated < desired || ready < desired || unavailable > 0) return true
  return state.label === 'Rolling out' || state.label === 'Applying' || state.label === 'Stalled'
}

function rolloutHeadline(resource: any, apiKind: string, readiness: ReadinessSummary, scaling: ScalingSummary): string {
  const status = resource?.status || {}
  const k = apiKind.toLowerCase()
  if (k === 'daemonsets') {
    return `${readiness.readyLabel} ready on nodes`
  }
  if (k === 'statefulsets') {
    const revision = status.updateRevision && status.currentRevision && status.updateRevision !== status.currentRevision
      ? `${status.currentRevision} -> ${status.updateRevision}`
      : status.currentRevision || status.updateRevision
    return `${readiness.readyLabel} ready · ${scaling.label}${revision && status.updateRevision !== status.currentRevision ? ' · updating revision' : ''}`
  }
  return `${readiness.readyLabel} ready · ${scaling.label}`
}

function ServingDependenciesCard({
  title = 'Serving and dependencies',
  relationships,
  groups,
  onNavigate,
  onSwitchToTopology,
}: {
  title?: string
  relationships?: Relationships
  groups?: RelationshipGroup[]
  onNavigate?: NavigateToResource
  onSwitchToTopology?: () => void
}) {
  const relationshipGroups = groups ?? buildRelationshipGroups(relationships)
  const visibleGroups = relationshipGroups.slice(0, RELATIONSHIP_GROUP_LIMIT)
  const hiddenRefs =
    visibleGroups.reduce((sum, group) => sum + Math.max(0, group.refs.length - RELATIONSHIP_REF_LIMIT), 0) +
    relationshipGroups.slice(RELATIONSHIP_GROUP_LIMIT).reduce((sum, group) => sum + group.refs.length, 0)
  const navigateRef = onNavigate ? (ref: ResourceRef) => onNavigate(refToSelectedResource(ref)) : undefined

  return (
    <OverviewCard
      title={title}
      icon={Network}
      action={onSwitchToTopology && (
        <button type="button" onClick={onSwitchToTopology} className="inline-flex items-center gap-1 text-xs text-skyhook-500 hover:underline">
          <GitBranch className="h-3.5 w-3.5" />
          Topology
        </button>
      )}
    >
      {visibleGroups.length > 0 ? (
        <div className="space-y-3">
          {visibleGroups.map((group) => (
            <div key={group.label}>
              <div className="mb-1 text-xs font-medium text-theme-text-tertiary">{group.label}</div>
              <div className="flex flex-wrap gap-1.5">
                {group.refs.slice(0, RELATIONSHIP_REF_LIMIT).map((ref) => (
                  <ResourceRefBadge key={`${ref.kind}/${ref.namespace}/${ref.name}`} resourceRef={ref} onClick={navigateRef} wrapAtSeparator />
                ))}
                {group.refs.length > RELATIONSHIP_REF_LIMIT && (
                  <span className="badge bg-theme-elevated text-theme-text-secondary">+{group.refs.length - RELATIONSHIP_REF_LIMIT}</span>
                )}
              </div>
            </div>
          ))}
          {hiddenRefs > 0 && <div className="text-xs text-theme-text-tertiary">+{hiddenRefs} more relationships; use Topology for the full graph.</div>}
        </div>
      ) : (
        <div className="text-sm text-theme-text-tertiary">No serving or dependency relationships found.</div>
      )}
    </OverviewCard>
  )
}

function ConfigurationInputsCard({
  serviceAccountName,
  namespace,
  relationships,
  onNavigate,
  onSwitchToTopology,
}: {
  serviceAccountName: string
  namespace: string
  relationships?: Relationships
  onNavigate?: NavigateToResource
  onSwitchToTopology?: () => void
}) {
  const configRefs = dedupeResourceRefs(relationships?.configRefs ?? [])
  const policyRefs = dedupeResourceRefs([
    ...(relationships?.pdbs ?? []),
    ...(relationships?.networkPolicies ?? []),
    ...(relationships?.resourceClaims ?? []),
  ])
  if (!serviceAccountName && configRefs.length === 0 && policyRefs.length === 0) return null
  const navigateRef = onNavigate ? (ref: ResourceRef) => onNavigate(refToSelectedResource(ref)) : undefined
  const serviceAccountRef: ResourceRef = {
    kind: 'ServiceAccount',
    namespace,
    name: serviceAccountName || 'default',
  }

  return (
    <OverviewCard
      title="Configuration inputs"
      icon={Layers}
      action={onSwitchToTopology && (
        <button type="button" onClick={onSwitchToTopology} className="inline-flex items-center gap-1 text-xs text-skyhook-500 hover:underline">
          <GitBranch className="h-3.5 w-3.5" />
          Topology
        </button>
      )}
    >
      <div className="space-y-4">
        <div className="min-w-0">
          <div className="mb-1 text-xs font-medium text-theme-text-tertiary">Identity</div>
          <ResourceRefBadge resourceRef={serviceAccountRef} onClick={navigateRef} wrapAtSeparator />
        </div>
        <div className="min-w-0">
          <div className="mb-1 text-xs font-medium text-theme-text-tertiary">Config</div>
          {configRefs.length > 0 ? (
            <div className="flex flex-wrap gap-1.5">
              {configRefs.slice(0, RELATIONSHIP_REF_LIMIT).map((ref) => (
                <ResourceRefBadge key={`${ref.kind}/${ref.namespace}/${ref.name}`} resourceRef={ref} onClick={navigateRef} wrapAtSeparator />
              ))}
              {configRefs.length > RELATIONSHIP_REF_LIMIT && <Badge tone="structural" size="sm">+{configRefs.length - RELATIONSHIP_REF_LIMIT}</Badge>}
            </div>
          ) : (
            <div className="text-xs text-theme-text-tertiary">No ConfigMap or Secret refs found.</div>
          )}
        </div>
        <div className="min-w-0">
          <div className="mb-1 text-xs font-medium text-theme-text-tertiary">Policy & storage</div>
          {policyRefs.length > 0 && onSwitchToTopology ? (
            <button type="button" onClick={onSwitchToTopology} className="text-left text-xs text-theme-text-secondary hover:text-accent-text">
              {policyRefs.length} related resource{policyRefs.length === 1 ? '' : 's'} in Topology
            </button>
          ) : policyRefs.length > 0 ? (
            <div className="text-xs text-theme-text-secondary">{policyRefs.length} related resource{policyRefs.length === 1 ? '' : 's'} found.</div>
          ) : (
            <div className="text-xs text-theme-text-tertiary">No PDB, NetworkPolicy, or storage refs found.</div>
          )}
        </div>
      </div>
    </OverviewCard>
  )
}

function ActivityCard({
  events,
  updates,
  loading,
  eventsError,
  updatesError,
  onSwitchToTimeline,
}: {
  events: TimelineEvent[]
  updates: TimelineEvent[]
  loading?: boolean
  eventsError?: Error | null
  updatesError?: Error | null
  onSwitchToTimeline?: () => void
}) {
  const visibleEvents = mergeAndRankEvents(events, updates).slice(0, EVENT_VISIBLE_LIMIT)

  return (
    <OverviewCard
      title="Recent activity"
      icon={Clock3}
      action={onSwitchToTimeline && (
        <button type="button" onClick={onSwitchToTimeline} className="inline-flex items-center gap-1 text-xs text-skyhook-500 hover:underline">
          <ExternalLink className="h-3.5 w-3.5" />
          Timeline
        </button>
      )}
    >
      {loading ? (
        <div className="text-sm text-theme-text-tertiary">Loading activity…</div>
      ) : visibleEvents.length > 0 ? (
        <div className="space-y-2">
          {visibleEvents.map((event, i) => (
            <div key={`${event.id}-${i}`} className="rounded-md border border-theme-border bg-theme-base px-3 py-2">
              <div className="flex min-w-0 items-start gap-2">
                <span className={clsx('mt-1 h-2 w-2 shrink-0 rounded-full', isProblematicEvent(event) ? 'bg-amber-500' : 'bg-skyhook-500')} />
                <div className="min-w-0 flex-1">
                  <div className="flex items-center justify-between gap-2">
                    <span className="truncate text-sm font-medium text-theme-text-primary">{event.reason || event.eventType || 'update'}</span>
                    <span className="shrink-0 text-xs text-theme-text-tertiary">{formatAge(event.timestamp)}</span>
                  </div>
                  <div className="mt-0.5 line-clamp-2 text-xs text-theme-text-secondary">
                    {event.message || event.diff?.summary || `${event.kind} ${event.name}`}
                  </div>
                </div>
              </div>
            </div>
          ))}
          {(events.length + updates.length) > EVENT_VISIBLE_LIMIT && (
            <div className="text-xs text-theme-text-tertiary">Showing highest-signal recent events. Timeline has the full history.</div>
          )}
        </div>
      ) : (
        <div className="text-sm text-theme-text-tertiary">No recent activity captured.</div>
      )}
      {eventsError && <div className="mt-2 text-xs text-red-500">Failed to load events: {eventsError.message}</div>}
      {updatesError && <div className="mt-2 text-xs text-red-500">Failed to load changes: {updatesError.message}</div>}
    </OverviewCard>
  )
}

function buildRelationshipGroups(relationships: Relationships | undefined): RelationshipGroup[] {
  return [
    ...buildServingRelationshipGroups(relationships),
    ...buildDependencyRelationshipGroups(relationships),
  ]
}

function buildServingRelationshipGroups(relationships: Relationships | undefined): RelationshipGroup[] {
  if (!relationships) return []
  const routeRefs = dedupeResourceRefs([
    ...(relationships.ingresses ?? []),
    ...(relationships.gateways ?? []),
    ...(relationships.routes ?? []),
  ])
  return [
    { label: 'Services', refs: dedupeResourceRefs(relationships.services ?? []) },
    { label: 'Entry points', refs: routeRefs },
  ].filter((group) => group.refs.length > 0)
}

function buildDependencyRelationshipGroups(relationships: Relationships | undefined): RelationshipGroup[] {
  if (!relationships) return []
  return [
    { label: 'Configuration', refs: dedupeResourceRefs(relationships.configRefs ?? []) },
    { label: 'Autoscalers', refs: dedupeResourceRefs(relationships.scalers ?? []) },
    { label: 'Disruption budgets', refs: dedupeResourceRefs(relationships.pdbs ?? []) },
    { label: 'Network policies', refs: dedupeResourceRefs(relationships.networkPolicies ?? []) },
    { label: 'Storage claims', refs: dedupeResourceRefs(relationships.resourceClaims ?? []) },
  ].filter((group) => group.refs.length > 0)
}

function summarizeServiceTypes(serviceResources: any[], serviceCount: number, loading: boolean): string {
  if (loading && serviceResources.length === 0) return 'Loading'
  const types = Array.from(new Set(
    serviceResources
      .map((service) => service?.spec?.type || 'ClusterIP')
      .filter(Boolean),
  ))
  if (types.length === 0) return serviceCount > 0 ? 'Unknown' : '-'
  if (types.length <= 2) return types.join(' / ')
  return `${types[0]} +${types.length - 1}`
}

function summarizeExternalLabels(serviceResources: any[], entrypointResources: any[]): string[] {
  const labels: string[] = []
  for (const service of serviceResources) {
    const external = getServiceExternalIP(service)
    if (external) labels.push(`${service?.metadata?.name || 'service'}: ${external}`)
  }
  for (const entrypoint of entrypointResources) {
    const label = summarizeEntrypointResource(entrypoint)
    if (label) labels.push(label)
  }
  return dedupeStrings(labels)
}

function summarizeEntrypointResource(resource: any): string {
  const kind = resource?.kind
  const name = resource?.metadata?.name || kind || 'entrypoint'
  if (kind === 'Ingress') {
    const hosts = getIngressHosts(resource)
    const address = getIngressAddress(resource)
    return `${name}: ${hosts}${address ? ` @ ${address}` : ' (address pending)'}`
  }
  if (kind === 'Gateway') {
    const listeners = getGatewayListeners(resource)
    const addresses = getGatewayAddresses(resource)
    return `${name}: ${listeners}${addresses && addresses !== '-' ? ` @ ${addresses}` : ''}`
  }
  const hostnames = resource?.spec?.hostnames
  if (Array.isArray(hostnames) && hostnames.length > 0) {
    return `${name}: ${hostnames.slice(0, 2).join(', ')}${hostnames.length > 2 ? ` +${hostnames.length - 2}` : ''}`
  }
  const parentRefs = resource?.spec?.parentRefs
  if (Array.isArray(parentRefs) && parentRefs.length > 0) {
    return `${name}: ${parentRefs.length} parent${parentRefs.length === 1 ? '' : 's'}`
  }
  return name
}

function summarizeServingExposure(
  serviceResources: any[],
  serviceCount: number,
  entrypointCount: number,
  externalLabels: string[],
  loading: boolean,
): { label: string; badge: string; severity: BadgeSeverity } {
  if (loading && serviceResources.length === 0) return { label: 'Checking serving exposure', badge: 'Loading', severity: 'neutral' }
  const pendingLoadBalancer = serviceResources.some((service) =>
    service?.spec?.type === 'LoadBalancer' && getServiceExternalIP(service) === 'Pending',
  )
  const hasExternalService = serviceResources.some((service) =>
    ['LoadBalancer', 'NodePort', 'ExternalName'].includes(service?.spec?.type) || Boolean(getServiceExternalIP(service)),
  )
  if (entrypointCount > 0) return { label: 'External entrypoint configured', badge: 'External', severity: 'info' }
  if (pendingLoadBalancer) return { label: 'External service pending', badge: 'Pending', severity: 'warning' }
  if (hasExternalService || externalLabels.length > 0) return { label: 'External service configured', badge: 'External', severity: 'info' }
  if (serviceCount > 0) return { label: 'Internal service only', badge: 'Internal', severity: 'neutral' }
  return { label: 'No serving path detected', badge: 'None', severity: 'neutral' }
}

function dedupeResourceRefs(refs: ResourceRef[]): ResourceRef[] {
  const seen = new Set<string>()
  return refs.filter((ref) => {
    const key = resourceRefId(ref)
    if (seen.has(key)) return false
    seen.add(key)
    return true
  })
}

function resourceRefId(ref: ResourceRef): string {
  return `${ref.kind}/${ref.namespace}/${ref.name}/${ref.group ?? ''}`
}

function dedupeStrings(values: string[]): string[] {
  return Array.from(new Set(values.filter(Boolean)))
}

function mergeAndRankEvents(events: TimelineEvent[], updates: TimelineEvent[]): TimelineEvent[] {
  const seen = new Set<string>()
  return [...events, ...updates]
    .filter((event) => {
      const key = event.id || `${event.timestamp}/${event.kind}/${event.name}/${event.reason}/${event.message}/${event.diff?.summary}`
      if (seen.has(key)) return false
      seen.add(key)
      return true
    })
    .sort((a, b) => {
      const aProblem = isProblematicEvent(a) ? 1 : 0
      const bProblem = isProblematicEvent(b) ? 1 : 0
      if (aProblem !== bProblem) return bProblem - aProblem
      return new Date(b.timestamp).getTime() - new Date(a.timestamp).getTime()
    })
}
