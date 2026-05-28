import { useState, useEffect, useCallback, useMemo, useRef } from 'react'
import { flushSync } from 'react-dom'
import { useRefreshAnimation } from './hooks/useRefreshAnimation'
import { startViewTransitionSafe } from '@skyhook-io/k8s-ui/utils/view-transition'
import { useQueryClient } from '@tanstack/react-query'
import { useNavigate, useLocation, useSearchParams, useNavigationType, NavigationType } from 'react-router-dom'
import { HomeView } from './components/home/HomeView'
import { DebugOverlay } from './components/DebugOverlay'
import { TopologyGraph, TopologyFilterSidebar, TopologyControls, gitOpsRouteForKind } from '@skyhook-io/k8s-ui'
import { TimelineView } from './components/timeline/TimelineView'
import { ResourcesView } from './components/resources/ResourcesView'
import { serializeColumnFilters } from './components/resources/resource-utils'
import { ResourceDetailDrawer } from './components/resources/ResourceDetailDrawer'
import { WorkloadViewRoute } from './components/workload/WorkloadView'
import { CompareViewRoute } from './components/compare/CompareViewRoute'
import { HelmView } from './components/helm/HelmView'
import { TrafficView } from './components/traffic/TrafficView'
import { CostView } from './components/cost/CostView'
import { AuditView } from './components/audit/AuditView'
import { GitOpsView } from './components/gitops/GitOpsView'
import { HelmReleaseDrawer } from './components/helm/HelmReleaseDrawer'
import { PortForwardProvider, PortForwardIndicator, PortForwardPanel } from './components/portforward/PortForwardManager'
import { DockProvider, BottomDock, useDock, useDockReservedHeight, useOpenLocalTerminal } from './components/dock'
import { DURATION_DOCK } from '@skyhook-io/k8s-ui/utils/animation'
import { ContextSwitcher } from './components/ContextSwitcher'
import { NamespaceSwitcher, type NamespaceSwitcherHandle } from './components/NamespaceSwitcher'
import { useNavCustomization } from './context/NavCustomization'
import { ContextSwitchProvider, useContextSwitch } from './context/ContextSwitchContext'
import { ConnectionProvider, useConnection } from './context/ConnectionContext'
import { ConnectionErrorView } from './components/ConnectionErrorView'
import { CapabilitiesProvider, useCapabilitiesContext } from './contexts/CapabilitiesContext'
import { UserMenu } from './components/UserMenu'
import { ErrorBoundary } from './components/ui/ErrorBoundary'
import { UpdateNotification } from './components/ui/UpdateNotification'
import { ShortcutHelpOverlay } from './components/ui/ShortcutHelpOverlay'
import { CommandPalette } from './components/ui/CommandPalette'
import { DiagnosticsOverlay } from './components/ui/DiagnosticsOverlay'
import { useEventSource } from './hooks/useEventSource'
import { debugNamespaceLog, useNamespaces, useNamespaceScope, useSetActiveNamespace, useSwitchContext, useAuthMe } from './api/client'
import { routePath, apiUrl, getAuthHeaders, getCredentialsMode } from './api/config'
import { KeyboardShortcutProvider, useRegisterShortcut, useRegisterShortcuts } from './hooks/useKeyboardShortcuts'
import { useAnimatedUnmount } from './hooks/useAnimatedUnmount'
import radarLoadingIcon from '@skyhook-io/k8s-ui/assets/radar/radar-icon-loading.svg'
import { RefreshCw, Network, List, Clock, Package, Sun, Moon, Activity, Home, Star, Search, Bug, Settings, SquareTerminal, ShieldCheck, GitBranch, Shield as ShieldIcon } from 'lucide-react'
import { useTheme } from './context/ThemeContext'
import { Tooltip } from './components/ui/Tooltip'
import { LargeClusterNamespacePicker } from './components/shared/LargeClusterNamespacePicker'
import { SettingsDialog } from './components/settings/SettingsDialog'
import { MyPermissionsDialog } from './components/settings/MyPermissionsDialog'
import type { TopologyNode, GroupingMode, MainView, SelectedResource, SelectedHelmRelease, NodeKind, TopologyMode, Topology, K8sEvent } from './types'
import { kindToPlural, openExternal, apiVersionToGroup, buildWorkloadPath } from './utils/navigation'
import type { ContextSwitcherHandle } from './components/ContextSwitcher'

// All possible node kinds (core + GitOps)
const ALL_NODE_KINDS: NodeKind[] = [
  'Internet', 'Ingress', 'Gateway', 'HTTPRoute', 'GRPCRoute', 'TCPRoute', 'TLSRoute',
  'Service', 'Deployment', 'Rollout', 'DaemonSet', 'StatefulSet',
  'ReplicaSet', 'Pod', 'PodGroup', 'ConfigMap', 'Secret', 'HorizontalPodAutoscaler', 'Job', 'CronJob', 'PersistentVolumeClaim', 'Namespace',
  'Application', 'Kustomization', 'HelmRelease', 'GitRepository',
  'KnativeService', 'KnativeConfiguration', 'KnativeRevision', 'KnativeRoute',
  'Broker', 'Trigger', 'PingSource', 'ApiServerSource', 'ContainerSource', 'SinkBinding', 'Channel',
  'IngressRoute', 'IngressRouteTCP', 'IngressRouteUDP', 'Middleware', 'MiddlewareTCP',
  'TraefikService', 'ServersTransport', 'ServersTransportTCP', 'TLSOption', 'TLSStore',
  'HTTPProxy', // Contour
  'CAPICluster', 'MachineDeployment', 'MachineSet', 'Machine', 'MachinePool', // Cluster API
  'KubeadmControlPlane', 'ClusterClass', 'MachineHealthCheck',
]

// Default visible kinds (ReplicaSet hidden by default - noisy intermediate object)
const DEFAULT_VISIBLE_KINDS = ALL_NODE_KINDS.filter(k => k !== 'ReplicaSet')

// CRD kinds hidden by default in the topology (infrastructure plumbing).
// Users can re-enable via the filter sidebar.
const CRD_HIDDEN_BY_DEFAULT = new Set(['GatewayClass', 'IngressClass', 'NodePool', 'NodeClaim', 'NodeClass'])

// CAPI kinds shown in Fleet topology mode (+ Node for Machine→Node edges)
// Includes core CAPI kinds and all infrastructure provider kinds
const FLEET_MODE_KINDS = new Set<NodeKind>([
  'CAPICluster', 'MachineDeployment', 'MachineSet', 'Machine', 'MachinePool',
  'KubeadmControlPlane', 'ClusterClass', 'MachineHealthCheck', 'Node',
  // AWS provider
  'AWSManagedControlPlane', 'AWSManagedMachinePool', 'AWSMachine',
  'AWSMachineTemplate', 'AWSManagedCluster', 'AWSClusterControllerIdentity',
  'EKSConfig', 'EKSConfigTemplate',
  // GCP provider
  'GCPManagedControlPlane', 'GCPManagedMachinePool', 'GCPMachine',
  'GCPMachineTemplate', 'GCPManagedCluster',
  // Azure provider
  'AzureManagedControlPlane', 'AzureManagedMachinePool', 'AzureMachine',
  'AzureMachineTemplate', 'AzureManagedCluster',
])

// Convert API resource name back to topology node ID prefix
function apiResourceToNodeIdPrefix(apiResource: string): string {
  const prefixMap: Record<string, string> = {
    'pods': 'pod',
    'services': 'service',
    'deployments': 'deployment',
    'daemonsets': 'daemonset',
    'statefulsets': 'statefulset',
    'replicasets': 'replicaset',
    'ingresses': 'ingress',
    'gateways': 'gateway',
    'httproutes': 'httproute',
    'grpcroutes': 'grpcroute',
    'tcproutes': 'tcproute',
    'tlsroutes': 'tlsroute',
    'configmaps': 'configmap',
    'secrets': 'secret',
    'horizontalpodautoscalers': 'horizontalpodautoscaler',
    'jobs': 'job',
    'cronjobs': 'cronjob',
    'persistentvolumeclaims': 'persistentvolumeclaim',
    'namespaces': 'namespace',
    'httpproxies': 'httpproxy', // Contour
  }
  return prefixMap[apiResource] || apiResource.replace(/s$/, '')
}

// Extended MainView type that includes traffic and cost
type ExtendedMainView = MainView | 'traffic' | 'cost' | 'workload' | 'audit' | 'gitops' | 'compare'

// Extract view from URL path
function getViewFromPath(pathname: string): ExtendedMainView {
  const path = pathname.replace(/^\//, '').split('/')[0]
  if (path === '' || path === 'home') return 'home'
  if (path === 'topology') return 'topology'
  if (path === 'resources') return 'resources'
  if (path === 'timeline') return 'timeline'
  if (path === 'helm') return 'helm'
  if (path === 'traffic') return 'traffic'
  if (path === 'cost') return 'cost'
  if (path === 'workload') return 'workload'
  if (path === 'audit') return 'audit'
  if (path === 'gitops') return 'gitops'
  if (path === 'compare') return 'compare'
  return 'home'
}

function AuthBarrier({ authMode }: { authMode: string }) {
  useEffect(() => {
    if (authMode === 'oidc') {
      window.location.href = routePath('/auth/login')
    }
  }, [authMode])

  if (authMode === 'oidc') {
    return (
      <div className="flex-1 relative bg-theme-base">
        <div className="fixed inset-0 pointer-events-none">
          <img
            src={radarLoadingIcon}
            alt=""
            aria-hidden
            className="absolute top-1/2 left-1/2 -translate-x-1/2 -translate-y-1/2 w-11 h-11"
          />
          <p
            className="absolute left-1/2 -translate-x-1/2 whitespace-nowrap text-[17px] font-semibold tracking-tight text-theme-text-primary"
            style={{ top: 'calc(50% + 34px)' }}
          >
            Redirecting to login…
          </p>
        </div>
      </div>
    )
  }

  return (
    <div className="flex-1 flex items-center justify-center bg-theme-base">
      <div className="flex flex-col items-center gap-4 max-w-md text-center">
        <div className="w-12 h-12 rounded-full bg-amber-500/10 flex items-center justify-center">
          <svg className="w-6 h-6 text-amber-500" fill="none" viewBox="0 0 24 24" stroke="currentColor" strokeWidth={2}>
            <path strokeLinecap="round" strokeLinejoin="round" d="M12 15v2m0 0v2m0-2h2m-2 0H10m4-6V7a4 4 0 00-8 0v4h8z" />
            <rect x="5" y="11" width="14" height="11" rx="2" strokeLinecap="round" strokeLinejoin="round" />
          </svg>
        </div>
        <div>
          <p className="text-lg font-medium text-theme-text-primary">Authentication Required</p>
          <p className="text-sm text-theme-text-secondary mt-2">
            Radar is configured with proxy authentication. Access it through your organization's auth proxy to authenticate.
          </p>
        </div>
      </div>
    </div>
  )
}

function AppInner() {
  const navigate = useNavigate()
  const location = useLocation()
  const navigationType = useNavigationType()
  const [searchParams, setSearchParams] = useSearchParams()
  const capabilities = useCapabilitiesContext()
  const openLocalTerminal = useOpenLocalTerminal()
  const navCustomization = useNavCustomization()

  // Auth check — detect if auth is enabled but user is not authenticated
  const { data: authMe, isPending: authMePending } = useAuthMe()

  // Restore navigation path after session-expiry re-auth redirect
  useEffect(() => {
    const returnPath = sessionStorage.getItem('radar_return_path')
    if (returnPath) {
      sessionStorage.removeItem('radar_return_path')
      navigate(returnPath, { replace: true })
    }
  }, [navigate])

  // Parse namespaces from URL (supports both 'namespaces' and legacy 'namespace')
  const parseNamespacesFromURL = (params: URLSearchParams): string[] => {
    // Prefer 'namespaces' (plural, comma-separated)
    const nsParam = params.get('namespaces')
    if (nsParam) {
      return nsParam.split(',').map(s => s.trim()).filter(Boolean)
    }
    // Fall back to 'namespace' (singular) for backward compatibility
    const ns = params.get('namespace')
    if (ns) {
      return [ns]
    }
    return []
  }

  // Initialize state from URL
  const getInitialState = () => {
    const namespaces = parseNamespacesFromURL(searchParams)
    return {
      namespaces,
      topologyMode: (searchParams.get('mode') as TopologyMode) || 'resources',
      // Default to namespace grouping when viewing all namespaces
      grouping: (searchParams.get('group') as GroupingMode) || (namespaces.length === 0 ? 'namespace' : 'none'),
    }
  }

  // Get mainView from URL path
  const mainView = getViewFromPath(location.pathname)

  // Workload slug after `/resources/` (defaults to `pods`). Bare `/resources` redirects to `/resources/pods`.
  const normalizedResourcesKindSlug = useMemo(() => {
    const m = location.pathname.match(/^\/resources(?:\/([^/]+))?/)
    const slug = m?.[1] ?? ''
    return slug || 'pods'
  }, [location.pathname])

  // Canonical URL — `/resources` is not stable for bookmarks/sharing; normalize to `/resources/pods`.
  useEffect(() => {
    const path = location.pathname.replace(/\/+$/, '') || '/'
    if (path !== '/resources') return
    navigate(
      { pathname: '/resources/pods', search: location.search, hash: location.hash },
      { replace: true },
    )
  }, [location.pathname, location.search, location.hash, navigate])

  // Set mainView by navigating to the path
  const setMainView = useCallback((view: ExtendedMainView, params?: Record<string, string>) => {
    const path = view === 'home' ? '/' : `/${view}`

    // Start fresh — keep only cross-view params (namespaces), discard all view-specific ones
    const newParams = new URLSearchParams()
    const globalNamespaces = searchParams.get('namespaces')
    if (globalNamespaces) {
      newParams.set('namespaces', globalNamespaces)
    }

    // Add any new params
    if (params) {
      for (const [key, value] of Object.entries(params)) {
        newParams.set(key, value)
      }
    }

    navigate({ pathname: path, search: newParams.toString() })
  }, [navigate, searchParams])

  // Cloud (embedded) makes the host's fleet Checks queue the one canonical
  // surface — owned by the host's left rail — so Radar drops its own Audit
  // pill (see the nav below) and any route to /audit redirects to the fleet
  // Checks queue scoped to this cluster. Entry points that still land on
  // /audit — the Home "Cluster Audit" card, ⌘K, WorkloadView's "view all"
  // findings, bookmarks/deep links — all funnel through here. `replace` (not
  // assign) keeps the transient /audit URL out of history so Back doesn't
  // bounce off the redirect. Standalone OSS (no clusterChecksHref) is
  // unaffected and renders the in-app audit view as before.
  const clusterChecksHref = navCustomization.clusterChecksHref
  useEffect(() => {
    if (clusterChecksHref && mainView === 'audit') {
      window.location.replace(clusterChecksHref())
    }
  }, [clusterChecksHref, mainView])

  const [namespaces, setNamespaces] = useState<string[]>(getInitialState().namespaces)
  // For large clusters: force SSE to reconnect with namespace filter
  const [forceNamespaceFilter, setForceNamespaceFilter] = useState<string[] | undefined>(undefined)
  const [selectedResource, setSelectedResource] = useState<SelectedResource | null>(null)
  const [drawerInitialTab, setDrawerInitialTab] = useState<'detail' | 'yaml'>('detail')
  const [selectedHelmRelease, setSelectedHelmRelease] = useState<SelectedHelmRelease | null>(null)
  const [topologyMode, setTopologyMode] = useState<TopologyMode>(getInitialState().topologyMode)
  const [groupingMode, setGroupingMode] = useState<GroupingMode>(getInitialState().grouping)
  const [showPolicyEffect, setShowPolicyEffect] = useState(false)
  // Topology filter state
  const [visibleKinds, setVisibleKinds] = useState<Set<NodeKind>>(() => new Set(DEFAULT_VISIBLE_KINDS))
  const [filterSidebarCollapsed, setFilterSidebarCollapsed] = useState(false)
  // Track CRD kinds that have been auto-added to visibleKinds so we don't override user toggles
  const seededCRDKindsRef = useRef<Set<string>>(new Set())

  // Topology live-update pause state
  const [topologyPaused, setTopologyPaused] = useState(false)
  const [displayedTopology, setDisplayedTopology] = useState<typeof topology>(null)
  const pendingTopologyRef = useRef<typeof topology>(null)

  // Help overlay state
  const [showHelp, setShowHelp] = useState(false)

  // Command palette state
  const [showCommandPalette, setShowCommandPalette] = useState(false)

  // Settings dialog state
  const [showSettings, setShowSettings] = useState(false)
  const [showMyPermissions, setShowMyPermissions] = useState(false)

  // Listen for desktop "open-settings" event from native menu
  useEffect(() => {
    const wailsRuntime = (window as unknown as Record<string, unknown>).runtime as
      | { EventsOn?: (event: string, callback: () => void) => () => void }
      | undefined
    if (!wailsRuntime?.EventsOn) return
    return wailsRuntime.EventsOn('open-settings', () => setShowSettings(true))
  }, [])

  // Listen for "open-settings" DOM event (used by MCPSetupDialog etc.)
  useEffect(() => {
    const handler = () => setShowSettings(true)
    window.addEventListener('radar:open-settings', handler)
    return () => window.removeEventListener('radar:open-settings', handler)
  }, [])

  // Diagnostics overlay state
  const [showDiagnostics, setShowDiagnostics] = useState(false)

  // Drawer expanded state (drawer grows to full width and renders WorkloadView)
  const [drawerExpanded, setDrawerExpanded] = useState(false)

  // Suppress the mainView-change clear effect during controlled expand/collapse transitions.
  const suppressViewClearRef = useRef(false)

  // Close resource drawer when the /resources route no longer matches the
  // selected drawer resource. This covers both in-view kind switches and
  // cross-kind navigations from expanded drawers (for example Node -> View Pods).
  const prevResourcesKindKeyRef = useRef<string | null>(null)
  const currentResourceKindSlug = normalizedResourcesKindSlug.toLowerCase()
  const currentResourceGroup = searchParams.get('apiGroup') ?? ''
  const selectedResourceKindSlug = selectedResource ? kindToPlural(selectedResource.kind).toLowerCase() : ''
  const selectedResourceGroup = selectedResource?.group ?? ''
  const selectedResourceRouteMismatch = mainView === 'resources' && !!selectedResource && (
    selectedResourceKindSlug !== currentResourceKindSlug ||
    selectedResourceGroup !== currentResourceGroup
  )
  const resourcesKindRouteChanged = mainView === 'resources' &&
    prevResourcesKindKeyRef.current !== null &&
    prevResourcesKindKeyRef.current !== `${currentResourceGroup}/${currentResourceKindSlug}`
  const routeSelectedResource = resourcesKindRouteChanged && selectedResourceRouteMismatch
    ? null
    : selectedResource

  useEffect(() => {
    if (mainView !== 'resources') {
      prevResourcesKindKeyRef.current = null
      return
    }
    const key = `${currentResourceGroup}/${currentResourceKindSlug}`
    const prev = prevResourcesKindKeyRef.current
    prevResourcesKindKeyRef.current = key

    if (prev !== null && prev !== key && selectedResourceRouteMismatch) {
      setSelectedResource(null)
      setDrawerExpanded(false)
    }
  }, [mainView, currentResourceKindSlug, currentResourceGroup, selectedResourceRouteMismatch])

  // Animation hooks for smooth mount/unmount transitions
  const resourceDrawer = useAnimatedUnmount(!!routeSelectedResource, 300)
  const helmDrawer = useAnimatedUnmount(!!(mainView === 'helm' && selectedHelmRelease), 300)
  const helpOverlay = useAnimatedUnmount(showHelp, 300)
  const commandPaletteAnim = useAnimatedUnmount(showCommandPalette, 300)
  const diagnosticsOverlay = useAnimatedUnmount(showDiagnostics, 300)

  // Hold last valid values so drawers can animate out before data disappears
  const lastResourceRef = useRef(routeSelectedResource)
  if (routeSelectedResource) lastResourceRef.current = routeSelectedResource
  const drawerResource = routeSelectedResource || lastResourceRef.current

  const lastHelmReleaseRef = useRef(selectedHelmRelease)
  if (selectedHelmRelease) lastHelmReleaseRef.current = selectedHelmRelease
  const drawerHelmRelease = selectedHelmRelease || lastHelmReleaseRef.current

  // Navigate to a resource — uses View Transitions cross-fade when drawer is already open
  const navigateToResource = useCallback((res: SelectedResource, tab: 'detail' | 'yaml' = 'detail') => {
    const update = () => { setDrawerInitialTab(tab); setSelectedResource(res) }
    // Skip the cross-fade animation entirely on first open (no
    // `selectedResource`); otherwise route through
    // startViewTransitionSafe to swallow the InvalidStateError that
    // the API rejects with on rapid back-to-back navigations.
    // (SKY-833 bug 49)
    if (selectedResource) {
      startViewTransitionSafe(() => flushSync(update))
    } else {
      update()
    }
  }, [selectedResource])

  // Collapse from expanded WorkloadView back to drawer
  const handleCollapseFromExpanded = useCallback(() => {
    suppressViewClearRef.current = true
    setDrawerExpanded(false)
    navigate(-1)
  }, [navigate])

  // Theme toggle for keyboard shortcut
  const { toggleTheme } = useTheme()

  // Context switching for command palette
  const switchContext = useSwitchContext()

  // Refs for dropdown components to trigger them via shortcuts
  const namespaceSwitcherRef = useRef<NamespaceSwitcherHandle>(null)
  const contextSwitcherRef = useRef<ContextSwitcherHandle>(null)

  // View switching keyboard shortcuts
  const views: ExtendedMainView[] = ['home', 'topology', 'resources', 'timeline', 'helm', 'gitops', 'traffic', 'cost', 'audit']
  useRegisterShortcuts([
    ...views.map((view, i) => ({
      id: `view-${view}`,
      keys: String(i + 1),
      description: `Go to ${view.charAt(0).toUpperCase() + view.slice(1)}`,
      category: 'Navigation' as const,
      scope: 'global' as const,
      handler: () => setMainView(view),
    })),
    {
      id: 'switch-namespace',
      keys: 'n',
      description: 'Switch namespace',
      category: 'Navigation' as const,
      scope: 'global' as const,
      handler: () => namespaceSwitcherRef.current?.open(),
    },
    {
      id: 'switch-context',
      keys: 'c',
      description: 'Switch context',
      category: 'Navigation' as const,
      scope: 'global' as const,
      handler: () => contextSwitcherRef.current?.open(),
    },
    {
      id: 'theme-toggle',
      keys: 't',
      description: 'Toggle dark/light theme',
      category: 'General' as const,
      scope: 'global' as const,
      handler: () => toggleTheme(),
    },
    {
      id: 'help-toggle',
      keys: '?',
      description: 'Show keyboard shortcuts',
      category: 'General' as const,
      scope: 'global' as const,
      handler: () => setShowHelp(prev => !prev),
    },
    {
      id: 'command-palette',
      keys: 'Cmd+k',
      description: 'Open command palette',
      category: 'General' as const,
      scope: 'global' as const,
      allowInInputs: true,
      handler: () => setShowCommandPalette(true),
    },
    {
      id: 'diagnostics',
      keys: 'Ctrl+Shift+d',
      description: 'Open diagnostics',
      category: 'General' as const,
      scope: 'global' as const,
      allowInInputs: true,
      handler: () => setShowDiagnostics(prev => !prev),
    },
  ])

  // Separate registration for help-close — its `enabled` changes with showHelp,
  // and keeping it in the batch above would cause all stable shortcuts to churn.
  useRegisterShortcut({
    id: 'help-close',
    keys: 'Escape',
    description: 'Close overlay',
    category: 'General',
    scope: 'global',
    handler: () => setShowHelp(false),
    enabled: showHelp,
  })

  // Compute effective grouping mode:
  // - All namespaces: must use 'namespace' or 'app' (no 'none')
  // - Single/specific namespaces with 'none': use 'namespace' internally but hide header
  const hasNamespaceFilter = namespaces.length > 0
  const effectiveGroupingMode: GroupingMode = useMemo(() => {
    if (!hasNamespaceFilter && groupingMode === 'none') {
      // All namespaces view - force namespace grouping
      return 'namespace'
    }
    if (hasNamespaceFilter && groupingMode === 'none') {
      // Filtered namespaces with "no grouping" - use namespace grouping for layout
      return 'namespace'
    }
    return groupingMode
  }, [hasNamespaceFilter, groupingMode])

  // Hide group header when viewing a single namespace with namespace grouping —
  // the namespace name is already shown in the breadcrumb/picker. Preserve headers
  // for app/label grouping so those group boundaries remain visible.
  const hideGroupHeader = namespaces.length === 1 && effectiveGroupingMode === 'namespace'

  // Fetch available namespaces
  const { data: availableNamespaces } = useNamespaces()

  // Per-user view filter served by the backend. Loaded eagerly so the
  // picker can render its current state without showing the multi-select
  // fallback during the initial scope fetch.
  const { data: namespaceScope } = useNamespaceScope()

  // Context switch state
  const { isSwitching, targetContext, progressMessage, updateProgress, endSwitch } = useContextSwitch()

  // Connection state (for graceful startup)
  const { connection, retry: retryConnection, isRetrying, updateFromSSE: updateConnectionFromSSE } = useConnection()

  // Query client for cache invalidation
  const queryClient = useQueryClient()

  // SSE-driven cache invalidation, split into two cadences so constant status
  // churn on large clusters doesn't force the *expensive* queries (big resource
  // lists + dashboard) to refetch every 3s. The core distinction: add/delete
  // changes what rows/counts exist (membership — keep fast); update is mostly
  // status/restart/health noise that can fire constantly on a 10k-pod cluster
  // and shouldn't drag a giant list onto a 3s cadence.
  //
  //   FAST (3s): detail drawer for any change (one cheap mounted object), and
  //     on add/delete: the list, counts, and dashboard. GitOps + cert keep
  //     their existing every-batch behavior — Phase 2 makes GitOps relevance-aware.
  //   SLOW (15s): list + dashboard for kinds with update churn. A kind that also
  //     had an add/delete in the window gets refreshed by both tiers (an extra
  //     refetch per 15s at most) — that's fine and avoids a stale-list bug:
  //     deduping by "was structural this window" would wrongly suppress an
  //     update that arrived *after* the fast structural flush already ran.
  const fastInvalidationRef = useRef<{
    changedKinds: Set<string>   // every changed kind (any op) → detail drawer
    structuralKinds: Set<string> // add/delete kinds → list membership + counts + dashboard
    secretsChanged: boolean
    timer: number | null
  }>({ changedKinds: new Set(), structuralKinds: new Set(), secretsChanged: false, timer: null })
  const slowInvalidationRef = useRef<{
    updatedKinds: Set<string>    // update-only churn → throttled list + dashboard
    timer: number | null
  }>({ updatedKinds: new Set(), timer: null })

  const handleK8sEvent = useCallback((event: K8sEvent) => {
    // Skip K8s Event kind — informational, not resource mutations
    if (event.kind === 'Event') return

    const kind = kindToPlural(event.kind)
    const structural = event.operation === 'add' || event.operation === 'delete'

    const fast = fastInvalidationRef.current
    fast.changedKinds.add(kind)
    if (structural) fast.structuralKinds.add(kind)
    if (kind === 'secrets') fast.secretsChanged = true

    const slow = slowInvalidationRef.current
    if (!structural) slow.updatedKinds.add(kind)

    // FAST tier — membership-sensitive + cheap, bounded 3s latency.
    if (fast.timer === null) {
      fast.timer = window.setTimeout(() => {
        const f = fastInvalidationRef.current
        for (const k of f.changedKinds) {
          queryClient.invalidateQueries({ queryKey: ['resource', k] }) // open detail drawer stays live
        }
        for (const k of f.structuralKinds) {
          queryClient.invalidateQueries({ queryKey: ['resources', k] }) // list membership changed
        }
        if (f.structuralKinds.size > 0) {
          queryClient.invalidateQueries({ queryKey: ['resource-counts'] })
          queryClient.invalidateQueries({ queryKey: ['dashboard'] })
        }
        if (f.secretsChanged) {
          queryClient.invalidateQueries({ queryKey: ['secret-cert-expiry'] })
        }
        // GitOps behavior unchanged from before — refreshes every batch when a
        // GitOps view is mounted (Phase 2 will make this relevance-aware).
        queryClient.invalidateQueries({ queryKey: ['gitops-tree'] })
        queryClient.invalidateQueries({ queryKey: ['gitops-insights'] })
        fastInvalidationRef.current = { changedKinds: new Set(), structuralKinds: new Set(), secretsChanged: false, timer: null }
      }, 3000)
    }

    // SLOW tier — throttle the expensive queries for status-only churn. Only
    // updates schedule it; structural changes are fully handled by the fast tier.
    if (!structural && slow.timer === null) {
      slow.timer = window.setTimeout(() => {
        const s = slowInvalidationRef.current
        for (const k of s.updatedKinds) {
          queryClient.invalidateQueries({ queryKey: ['resources', k] })
        }
        queryClient.invalidateQueries({ queryKey: ['dashboard'] }) // health reflects status updates
        slowInvalidationRef.current = { updatedKinds: new Set(), timer: null }
      }, 15000)
    }
  }, [queryClient])

  // Clear pending invalidation timers on unmount. Reset the refs (not just
  // clearTimeout) so a same-instance remount doesn't inherit a non-null timer
  // id — handleK8sEvent only schedules when timer === null, so a stale id would
  // silently wedge all further SSE-driven invalidation.
  useEffect(() => () => {
    if (fastInvalidationRef.current.timer !== null) clearTimeout(fastInvalidationRef.current.timer)
    if (slowInvalidationRef.current.timer !== null) clearTimeout(slowInvalidationRef.current.timer)
    fastInvalidationRef.current = { changedKinds: new Set(), structuralKinds: new Set(), secretsChanged: false, timer: null }
    slowInvalidationRef.current = { updatedKinds: new Set(), timer: null }
  }, [])

  // SSE connection for real-time updates — no namespace filter for small/medium clusters (frontend filters).
  // forceNamespaceFilter is only set for large clusters that require server-side filtering.
  // Fleet mode uses 'resources' topology on the backend — filtering is client-side
  const sseMode = topologyMode === 'fleet' ? 'resources' : topologyMode
  const { topology, connected, reconnect: reconnectSSE } = useEventSource(namespaces, sseMode as 'resources' | 'traffic', {
    onContextSwitchComplete: endSwitch,
    onContextSwitchProgress: updateProgress,
    onContextChanged: () => {
      // Clear all React Query caches when cluster context changes
      // This ensures helm releases, resources, etc. are refetched from the new cluster
      // removeQueries clears cached data, invalidateQueries triggers refetch
      queryClient.removeQueries()
      queryClient.invalidateQueries()

      // Cancel any pending SSE-driven invalidation — old cluster's events are irrelevant
      if (fastInvalidationRef.current.timer !== null) clearTimeout(fastInvalidationRef.current.timer)
      if (slowInvalidationRef.current.timer !== null) clearTimeout(slowInvalidationRef.current.timer)
      fastInvalidationRef.current = { changedKinds: new Set(), structuralKinds: new Set(), secretsChanged: false, timer: null }
      slowInvalidationRef.current = { updatedKinds: new Set(), timer: null }

      // Close any open drawers/overlays — old cluster's resources don't exist on the new one
      setSelectedResource(null)
      setDrawerExpanded(false)
      setSelectedHelmRelease(null)

      // Reset URL to current view with no resource-specific params.
      // Old cluster's selected pod/resource/kind don't exist on the new cluster.
      navigate({ pathname: location.pathname, search: '' }, { replace: true })

      // Auto-unpause so the new cluster's topology loads immediately
      setTopologyPaused(false)
      pendingTopologyRef.current = null
    },
    onConnectionStateChange: updateConnectionFromSSE,
    onDeferredReady: () => {
      // Deferred informers (secrets, events, configmaps, etc.) have finished syncing.
      // Refetch dashboard so counts, warning events, and cert health fill in.
      queryClient.invalidateQueries({ queryKey: ['dashboard'] })
    },
    onK8sEvent: handleK8sEvent,
  }, forceNamespaceFilter, showPolicyEffect)
  const [reconnect, isReconnecting] = useRefreshAnimation(reconnectSSE)

  // On large clusters (where the server requires namespace filtering), keep
  // SSE's server-side filter in lockstep with the user's namespace pick.
  // Without this, header switches and deep-link loads can leave SSE filtered
  // to a stale namespace while sidebar/topology show a different one. Small
  // clusters never set forceNamespaceFilter and skip this path entirely.
  useEffect(() => {
    const isLarge = forceNamespaceFilter !== undefined || topology?.requiresNamespaceFilter === true
    if (!isLarge) return
    if (namespaces.length === 0) {
      setForceNamespaceFilter(prev => (prev === undefined ? prev : undefined))
      return
    }
    setForceNamespaceFilter(prev => {
      const cur = prev ? [...prev].sort() : []
      const next = [...namespaces].sort()
      if (cur.length === next.length && cur.every((ns, i) => ns === next[i])) return prev
      return [...namespaces]
    })
  }, [namespaces, forceNamespaceFilter, topology?.requiresNamespaceFilter])

  // Apply live topology updates only when not paused. While paused, buffer the
  // latest snapshot so we can apply it instantly when the user resumes.
  useEffect(() => {
    if (!topologyPaused) {
      setDisplayedTopology(topology)
    } else {
      pendingTopologyRef.current = topology
    }
  }, [topology, topologyPaused])

  const handleTogglePause = useCallback(() => {
    setTopologyPaused(prev => {
      if (prev && pendingTopologyRef.current !== null) {
        // Resuming — apply the buffered snapshot immediately
        setDisplayedTopology(pendingTopologyRef.current)
        pendingTopologyRef.current = null
      }
      return !prev
    })
  }, [])

  // Track CRD discovery status from topology (more direct than cluster-info)
  // When discovery completes, topology will auto-update via SSE with new CRD nodes
  const crdDiscoveryStatus = topology?.crdDiscoveryStatus

  // Debug: log discovery status changes
  useEffect(() => {
    if (crdDiscoveryStatus) {
      console.log('[CRD Discovery] Status:', crdDiscoveryStatus)
    }
  }, [crdDiscoveryStatus])

  // Auto-add CRD kinds (not in ALL_NODE_KINDS) to visibleKinds the first time they appear.
  // Uses a ref to track which kinds have been seeded so user toggle-off choices are preserved.
  const allNodeKindsSet = useMemo(() => new Set<string>(ALL_NODE_KINDS), [])
  useEffect(() => {
    if (!topology?.nodes) return
    const newKinds: NodeKind[] = []
    for (const node of topology.nodes) {
      const k = node.kind as string
      if (!allNodeKindsSet.has(k) && !seededCRDKindsRef.current.has(k)) {
        seededCRDKindsRef.current.add(k)
        if (!CRD_HIDDEN_BY_DEFAULT.has(k)) {
          newKinds.push(node.kind)
        }
      }
    }
    if (newKinds.length > 0) {
      setVisibleKinds(prev => {
        const next = new Set(prev)
        for (const k of newKinds) next.add(k)
        return next
      })
    }
  }, [topology, allNodeKindsSet])

  // Handle node selection - convert TopologyNode to SelectedResource for the drawer
  const handleNodeClick = useCallback((node: TopologyNode) => {
    // Skip Internet node - it's not a real resource
    if (node.kind === 'Internet') return

    // For PodGroup, we can't open a single resource drawer
    // TODO: Could show a list of pods in the group
    if (node.kind === 'PodGroup') return

    const namespace = (node.data.namespace as string) || ''
    // GitOps CRs (Application/Kustomization/HelmRelease/etc.) have a dedicated
    // detail page with tree + insights + ops that the drawer can't reproduce.
    // Route there from the main topology when the node is one of those kinds;
    // everything else falls back to the drawer.
    const gitOpsPath = gitOpsRouteForKind(node.kind, namespace, node.name)
    if (gitOpsPath) {
      navigate(gitOpsPath)
      return
    }

    navigateToResource({
      kind: kindToPlural(node.kind),
      namespace,
      name: node.name,
      group: apiVersionToGroup(node.data.apiVersion as string | undefined),
    })
  }, [navigate])

  // Serialize namespaces for stable dependency tracking
  const namespacesKey = namespaces.join(',')

  // The server is canonical for the per-user namespace pick. Mirror its
  // `actives` into App.tsx state so consumer hooks (SSE, dashboard, resource
  // lists) stay in lockstep with the picker. The dedicated URL-write effect
  // below propagates the mirrored state to `?namespaces=`.
  const setActiveNamespace = useSetActiveNamespace()
  // Defer the state flip to onSuccess. Setting namespaces to [] before the
  // server-side pref has actually been cleared makes React Query refetch
  // under the new empty key while the server still returns the previous
  // pick's scope, caching stale data under the new key with no later
  // invalidation. onSettled would do the same on errors, leaving the UI
  // showing "All namespaces" while data is still namespace-scoped — onSuccess
  // keeps state aligned with the server.
  //
  // Don't touch the URL here either: setSearchParams on a still-set state
  // trips the URL→state sync into firing setNamespaces([]) and a duplicate
  // mutation immediately, which re-introduces the same race. The state→URL
  // effect propagates state=[] → URL on its own after onSuccess flips state.
  const clearAllNamespaces = useCallback(() => {
    if (namespaces.length === 0) return
    setActiveNamespace.mutate(
      { namespaces: [] },
      { onSuccess: () => setNamespaces([]) },
    )
  }, [namespaces.length, setActiveNamespace])
  const initialBookmarkReconciledRef = useRef(false)
  const scopeActives = useMemo(() => namespaceScope?.actives ?? [], [namespaceScope?.actives])
  const namespaceScopeKey = useMemo(() => namespaceScope ? [...scopeActives].sort().join(',') : null, [namespaceScope, scopeActives])
  useEffect(() => {
    if (!namespaceScope) return
    const sortedScope = [...scopeActives].sort()
    const sortedState = [...namespaces].sort()
    const sameAsState = sortedScope.length === sortedState.length && sortedScope.every((ns, i) => ns === sortedState[i])
    debugNamespaceLog('app:scope-mirror', {
      scopeActives,
      stateNamespaces: namespaces,
      sameAsState,
      initialBookmarkReconciled: initialBookmarkReconciledRef.current,
    })

    // First-load bookmark reconciliation: if the URL had namespaces that
    // differ from the server pick when the scope first arrives, push the
    // URL choice to the server so shared/bookmarked deep links keep
    // working. The ref flips on the first scope load regardless of whether
    // the URL had namespaces — subsequent runs mirror server → state.
    if (!initialBookmarkReconciledRef.current) {
      initialBookmarkReconciledRef.current = true
      if (!sameAsState && sortedState.length > 0) {
        debugNamespaceLog('app:scope-mirror-bookmark-to-server', {
          stateNamespaces: sortedState,
          scopeActives: sortedScope,
        })
        setActiveNamespace.mutate({ namespaces: sortedState })
        return
      }
    }

    if (!sameAsState) {
      debugNamespaceLog('app:scope-mirror-set-namespaces', { nextNamespaces: scopeActives })
      setNamespaces(scopeActives)
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps -- namespaces and setActiveNamespace are intentionally excluded; we only react to server-side changes.
  }, [namespaceScope, namespaceScopeKey])

  // Update URL query params when state changes (path is handled by setMainView)
  // Read from window.location.search (not React Router's searchParams) to preserve
  // params set by child components via window.history.replaceState (e.g., kind from ResourcesView).
  useEffect(() => {
    const currentSearch = window.location.search
    const params = new URLSearchParams(currentSearch)

    // Update namespaces param
    if (namespaces.length > 0) {
      params.set('namespaces', namespaces.join(','))
    } else {
      params.delete('namespaces')
    }
    // Remove legacy 'namespace' param if present
    params.delete('namespace')

    // Topology-specific params: only set when on topology view, clean up otherwise
    if (mainView === 'topology') {
      if (topologyMode !== 'resources') {
        params.set('mode', topologyMode)
      } else {
        params.delete('mode')
      }
      if (groupingMode !== 'none' && (namespaces.length === 0 || groupingMode !== 'namespace')) {
        params.set('group', groupingMode)
      } else {
        params.delete('group')
      }
    } else {
      params.delete('mode')
      params.delete('group')
    }

    // Only update if params actually changed vs current URL
    if (params.toString() !== new URLSearchParams(currentSearch).toString()) {
      debugNamespaceLog('app:url-write', {
        namespaces,
        currentSearch,
        nextSearch: params.toString(),
        mainView,
      })
      setSearchParams(params, { replace: true })
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps -- reads window.location.search, not searchParams
  }, [namespacesKey, topologyMode, groupingMode, mainView, setSearchParams])

  // Sync namespace + helm picks from the query string only when the query
  // string changes. If this also ran on pathname / mainView changes, a view
  // whose URL omits ?namespaces= would clear App state and POST [] to the
  // server while the per-user pick was still narrowed — the picker would
  // show the server scope but lists/dashboard would stay on "all namespaces".
  useEffect(() => {
    const urlNamespaces = parseNamespacesFromURL(searchParams)
    debugNamespaceLog('app:url-sync', {
      search: searchParams.toString(),
      urlNamespaces,
      stateNamespaces: namespaces,
      namespacesKey,
    })

    if (urlNamespaces.join(',') !== namespacesKey) {
      debugNamespaceLog('app:url-sync-set-namespaces', { nextNamespaces: urlNamespaces })
      setNamespaces(urlNamespaces)
      if (namespaceScope) {
        const sortedURL = [...urlNamespaces].sort()
        const sortedScope = [...(namespaceScope.actives ?? [])].sort()
        const same = sortedURL.length === sortedScope.length && sortedURL.every((ns, i) => ns === sortedScope[i])
        if (!same) {
          debugNamespaceLog('app:url-sync-mutate-server', {
            urlNamespaces,
            scopeActives: namespaceScope.actives ?? [],
          })
          setActiveNamespace.mutate({ namespaces: urlNamespaces })
        }
      }
    }

    const releaseParam = searchParams.get('release')
    if (releaseParam) {
      const slashIdx = releaseParam.indexOf('/')
      if (slashIdx > 0) {
        const ns = releaseParam.slice(0, slashIdx)
        const name = releaseParam.slice(slashIdx + 1)
        setSelectedHelmRelease({ namespace: ns, name, storageNamespace: searchParams.get('releaseStorage') || undefined })
      }
    }
  // eslint-disable-next-line react-hooks/exhaustive-deps -- run only when searchParams change; namespacesKey/namespaceScope are read for that transition
  }, [searchParams])

  useEffect(() => {
    if (navigationType !== NavigationType.Pop || mainView !== 'resources') return
    const kindFromPath = location.pathname.match(/^\/resources\/([^/]+)/)?.[1] ?? ''
    const resourceParam = searchParams.get('resource')
    if (kindFromPath && resourceParam) {
      const slashIdx = resourceParam.indexOf('/')
      const ns = slashIdx > 0 ? resourceParam.slice(0, slashIdx) : ''
      const name = slashIdx > 0 ? resourceParam.slice(slashIdx + 1) : resourceParam
      const apiGroup = searchParams.get('apiGroup') ?? ''
      const next: SelectedResource = { kind: kindFromPath, namespace: ns, name, group: apiGroup }
      setSelectedResource(prev => {
        if (
          prev &&
          prev.kind === next.kind &&
          prev.namespace === next.namespace &&
          prev.name === next.name &&
          (prev.group ?? '') === (next.group ?? '')
        ) return prev
        return next
      })
    } else if (kindFromPath && !resourceParam) {
      setSelectedResource(prev => (prev === null ? prev : null))
    }
  }, [navigationType, mainView, location.pathname, searchParams])

  // Auto-adjust grouping when namespaces change
  useEffect(() => {
    if (namespaces.length === 0 && groupingMode === 'none') {
      // Switching to all namespaces - enable namespace grouping by default
      setGroupingMode('namespace')
    } else if (namespaces.length > 0 && groupingMode === 'namespace') {
      // Switching to specific namespaces - disable namespace grouping
      setGroupingMode('none')
    }
  }, [namespacesKey])

  // Clear resource selection when changing views or namespaces
  // But preserve selectedResource when navigating TO resources view (e.g., from Helm deep link)
  const prevMainView = useRef(mainView)
  useEffect(() => {
    // Skip clearing during controlled expand/collapse transitions
    if (suppressViewClearRef.current) {
      suppressViewClearRef.current = false
      prevMainView.current = mainView
      return
    }

    const navigatingToResources = mainView === 'resources' && prevMainView.current !== 'resources'
    const navigatingToHelm = mainView === 'helm' && prevMainView.current !== 'helm'
    prevMainView.current = mainView

    // The URL is the source of truth for what's selected. A deep link
    // (?resource=, ?release=) seeds the selection on mount; the effects that
    // run during that same mount must not wipe a selection the URL still
    // asserts. (On a real view switch the URL no longer carries the param, so
    // the clear proceeds.) Without this, deep-linking straight to a Helm
    // release lands on the release list with no drawer.
    const params = new URLSearchParams(window.location.search)
    if (!navigatingToResources && !params.has('resource')) {
      setSelectedResource(null)
    }
    if (!navigatingToHelm && !params.has('release')) {
      setSelectedHelmRelease(null)
    }
    setDrawerExpanded(false)
  }, [mainView])

  // Clear resource selection when namespaces change — but keep a selection the
  // URL still asserts (deep link, or a release/resource the user is viewing
  // while they adjust the namespace scope filter).
  useEffect(() => {
    const params = new URLSearchParams(window.location.search)
    if (!params.has('resource')) setSelectedResource(null)
    if (!params.has('release')) setSelectedHelmRelease(null)
    setDrawerExpanded(false)
  }, [namespacesKey])

  // Filter topology based on visible kinds (uses displayedTopology which respects pause)
  const filteredTopology = useMemo((): Topology | null => {
    if (!displayedTopology) return null

    // Fleet mode overrides visible kinds to show only CAPI resources + Node
    const effectiveKinds = topologyMode === 'fleet' ? FLEET_MODE_KINDS : visibleKinds

    // Filter by namespace (frontend-side) and by visible kinds
    const nsSet = namespaces.length > 0 ? new Set(namespaces) : null
    const filteredNodes = displayedTopology.nodes.filter(node =>
      effectiveKinds.has(node.kind) &&
      (!nsSet || nsSet.has(node.data.namespace as string) || !(node.data.namespace as string))
    )
    const filteredNodeIds = new Set(filteredNodes.map(n => n.id))

    // Keep edges where both source and target are visible
    // Also respect skipIfKindVisible - hide shortcut edges when intermediate kind is shown
    const filteredEdges = displayedTopology.edges.filter(edge => {
      // Both endpoints must be visible
      if (!filteredNodeIds.has(edge.source) || !filteredNodeIds.has(edge.target)) {
        return false
      }
      // If this is a shortcut edge, hide it when the intermediate kind is visible
      if (edge.skipIfKindVisible && effectiveKinds.has(edge.skipIfKindVisible as NodeKind)) {
        return false
      }
      return true
    })

    return {
      ...displayedTopology,
      nodes: filteredNodes,
      edges: filteredEdges,
    }
  }, [displayedTopology, visibleKinds, namespaces, topologyMode])

  // Filter handlers
  const handleToggleKind = useCallback((kind: NodeKind) => {
    setVisibleKinds(prev => {
      const next = new Set(prev)
      if (next.has(kind)) {
        next.delete(kind)
      } else {
        next.add(kind)
      }
      return next
    })
  }, [])

  const handleShowAllKinds = useCallback(() => {
    // Include all static kinds plus any dynamic CRD kinds from the topology
    const allKinds = new Set<NodeKind>(ALL_NODE_KINDS)
    if (topology?.nodes) {
      for (const node of topology.nodes) {
        allKinds.add(node.kind)
      }
    }
    setVisibleKinds(allKinds)
  }, [topology])

  const handleHideAllKinds = useCallback(() => {
    setVisibleKinds(new Set())
  }, [])

  return (
    <PortForwardProvider>
    <div className="relative flex flex-col h-screen bg-theme-base min-w-[800px]">
      {/* Header */}
      <header className="relative z-50 flex items-center justify-between px-4 py-2 bg-theme-base/90 backdrop-blur-sm border-b border-theme-border/50">
        {/* Left: Logo + Cluster info */}
        <div className="flex items-center gap-4 shrink-0">
          {navCustomization.brandSlot ?? <Logo />}

          <div className="flex items-center gap-2">
            {navCustomization.contextSlot ?? <ContextSwitcher ref={contextSwitcherRef} />}
            {/* Connection status - next to cluster name */}
            <div className="flex items-center gap-1.5 ml-1">
              <Tooltip
                content={
                  !connected
                    ? 'Disconnected'
                    : crdDiscoveryStatus === 'discovering'
                      ? 'Connected — discovering Custom Resources...'
                      : 'Connected'
                }
                delay={100}
                position="bottom"
              >
                <span
                  className={`w-2 h-2 rounded-full ${
                    !connected
                      ? 'bg-red-500'
                      : crdDiscoveryStatus === 'discovering'
                        ? 'bg-amber-400 animate-pulse'
                        : 'bg-green-500'
                  }`}
                />
              </Tooltip>
              {/* Inline label only for non-steady states where the user
                  might need to act or wait. The healthy "Connected" case
                  is the dot alone; the dot's tooltip discloses it. Keeping
                  "Connected" text here would expand the left section and
                  collide with the absolute-centered nav block at xl, which
                  is the same breakpoint where nav labels appear. */}
              {(!connected || crdDiscoveryStatus === 'discovering') && (
                <span className="text-[11px] text-theme-text-tertiary hidden xl:inline">
                  {!connected ? 'Disconnected' : 'Discovering Custom Resources...'}
                </span>
              )}
              {!connected && (
                <button
                  onClick={reconnect}
                  disabled={isReconnecting}
                  className="p-1 text-theme-text-secondary hover:text-theme-text-primary disabled:opacity-50"
                  title="Reconnect"
                >
                  <RefreshCw className={`w-3 h-3 ${isReconnecting ? 'animate-spin' : ''}`} />
                </button>
              )}
            </div>
            {/* Port forwards indicator — shown only when sessions exist */}
            <PortForwardIndicator />
          </div>
        </div>

        {/* Center: View tabs — absolute centered on wide, flows after left section on narrow */}
        <div className="md:absolute md:left-1/2 md:-translate-x-1/2 flex items-center gap-0.5 bg-theme-elevated/50 rounded-full p-1 ml-2 md:ml-0">
          {([
            { view: 'home' as const, icon: Home, label: 'Home' },
            { view: 'topology' as const, icon: Network, label: 'Topology' },
            { view: 'resources' as const, icon: List, label: 'Resources' },
            { view: 'timeline' as const, icon: Clock, label: 'Timeline' },
            { view: 'helm' as const, icon: Package, label: 'Helm' },
            { view: 'gitops' as const, icon: GitBranch, label: 'GitOps' },
            { view: 'traffic' as const, icon: Activity, label: 'Traffic' },
            // Cost is intentionally hidden from the pill bar for now — the view still
            // exists and is reachable via /cost, the Home dashboard card, and the
            // command palette (⌘K). Remove this comment to restore it.
            { view: 'audit' as const, icon: ShieldCheck, label: 'Audit' },
          ] as const)
            // In Cloud, Checks is a fleet-scoped feature owned by the host's
            // left rail; the per-cluster view is just that fleet queue filtered
            // to this cluster, so duplicating it as a peer pill here would be a
            // second "Checks" that teleports out of the cluster shell. Drop the
            // Audit tab when embedded — cluster-scoped access stays available
            // via the Home "Cluster Audit" card (→ /audit, redirected to the
            // scoped fleet Checks by the clusterChecksHref effect above), ⌘K,
            // and bookmarks. Standalone OSS keeps the Audit tab.
            .filter(({ view }) => !(view === 'audit' && clusterChecksHref))
            .map(({ view, icon: Icon, label }) => (
            <Tooltip key={view} content={label} delay={100} position="bottom">
              <button
                onClick={() => setMainView(view)}
                className={`flex items-center gap-1 px-2 py-1 text-[13px] rounded-full transition-colors ${
                  mainView === view
                    ? 'bg-skyhook-600 dark:bg-skyhook-500 text-white shadow-glow-brand-sm'
                    : 'text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-hover'
                }`}
              >
                <Icon className="w-4 h-4" />
                {/* Labels appear only when the absolute-centered nav has
                    enough horizontal room past the left section. Right-side
                    chrome that adds further pressure (Connected text, star
                    count) is intentionally pushed to the next tier (xl) so
                    label rendering and right-side expansion stay decoupled.
                    Per-button Tooltip discloses labels on hover when the
                    icon-only viewport is in effect. The 1440 anchor is an
                    off-system breakpoint chosen by measurement at the time
                    of this PR — recompute if the cluster switcher cap or
                    other left-section chrome changes appreciably. */}
                <span className="hidden min-[1440px]:inline">{label}</span>
              </button>
            </Tooltip>
          ))}
        </div>

        {/* Right: Controls */}
        <div className="flex items-center gap-3 shrink-0">
          <NamespaceSwitcher
            ref={namespaceSwitcherRef}
            disabled={mainView === 'helm'}
            disabledTooltip="Helm view always shows all namespaces"
          />


          {/* Command palette trigger */}
          <button
            onClick={() => setShowCommandPalette(true)}
            className="hidden lg:flex items-center gap-2 h-7 px-2.5 rounded-md bg-theme-elevated hover:bg-theme-hover text-theme-text-secondary hover:text-theme-text-primary transition-colors"
          >
            <Search className="w-3.5 h-3.5" />
            <kbd className="text-[10px] text-theme-text-tertiary bg-theme-surface px-1 py-0.5 rounded border border-theme-border-light">
              {typeof navigator !== 'undefined' && navigator.platform.includes('Mac') ? '⌘' : 'Ctrl+'}K
            </kbd>
          </button>

          {/* GitHub star — hidden in embedded mode (not OSS-distribution chrome). */}
          {!navCustomization.embedded && (
            <div className="hidden lg:block">
              <GitHubStarButton />
            </div>
          )}

          {/* Local terminal */}
          {capabilities.localTerminal && (
            <button
              onClick={() => openLocalTerminal()}
              className="p-1.5 rounded-md bg-theme-elevated hover:bg-theme-hover text-theme-text-secondary hover:text-theme-text-primary transition-colors"
              title="Open local terminal"
            >
              <SquareTerminal className="w-4 h-4" />
            </button>
          )}

          {/* Theme toggle — hidden in embedded mode. Host apps (e.g. Radar
              Cloud) own the user-theme preference and mount their own picker
              in the account menu; a second toggle in Radar's topbar would
              fight them (one writes to Radar's localStorage key, the other
              to the host's cookie/backend) and the user would see the theme
              bounce on every navigation between host routes and /c/:id. */}
          {!navCustomization.embedded && (
            <div className="hidden md:block">
              <ThemeToggle />
            </div>
          )}

          {/* Settings — hidden in embedded mode. The standalone dialog
              exposes local-binary controls (kubeconfig paths, server port,
              "open browser on start", "Stop and restart the radar command
              to apply") that don't apply to a hosted user who doesn't SSH
              into the cluster. The audit view still opens the dialog via
              its "N namespaces hidden" link for the narrow audit-ignores
              setting — that's a deliberate escape hatch, not a general
              surface. */}
          {!navCustomization.embedded && (
            <button
              onClick={() => setShowSettings(true)}
              className="p-1.5 rounded-md bg-theme-elevated hover:bg-theme-hover text-theme-text-secondary hover:text-theme-text-primary transition-colors"
              title="Settings"
            >
              <Settings className="w-4 h-4" />
            </button>
          )}

          {/* My Permissions — what the current user can do in the cluster,
              computed live by the apiserver via SelfSubjectRulesReview.
              Available in embedded mode too — Radar Hub users still benefit
              from "why can't I do X" debugging. */}
          <button
            onClick={() => setShowMyPermissions(true)}
            className="p-1.5 rounded-md bg-theme-elevated hover:bg-theme-hover text-theme-text-secondary hover:text-theme-text-primary transition-colors"
            title="My permissions in this cluster"
          >
            <ShieldIcon className="w-4 h-4" />
          </button>

          {/* User menu (when auth enabled) — hidden in embedded mode;
              host app typically provides its own via rightExtras. */}
          {!navCustomization.embedded && <UserMenu />}

          {/* Consumer-provided extras (e.g. Radar Hub's Install button +
              avatar menu) appended to the right of the action bar. */}
          {navCustomization.rightExtras}
        </div>
      </header>

      {/* Auth barrier - show when auth is enabled but user is not authenticated */}
      {authMe?.authEnabled && !authMe?.username && authMe.authMode === 'proxy' && (
        <AuthBarrier authMode="proxy" />
      )}
      {authMe?.authEnabled && !authMe?.username && authMe.authMode === 'oidc' && (
        <AuthBarrier authMode="oidc" />
      )}

      {/* Connection error view - show when disconnected */}
      {!isSwitching && !(authMe?.authEnabled && !authMe?.username) && connection.state === 'disconnected' && (
        <ConnectionErrorView
          connection={connection}
          onRetry={retryConnection}
          isRetrying={isRetrying}
        />
      )}

      {/* Connecting view — shown during initial connection or retry.
          Icon is viewport-anchored so its screen position matches the
          host hub splash across cross-document transitions. */}
      {!isSwitching && !(authMe?.authEnabled && !authMe?.username) && connection.state === 'connecting' && (
        <div className="flex-1 relative bg-theme-base">
          {/* Icon absolutely anchored to viewport-center. The label block
              sits at a fixed offset below — independent of label height
              so multi-line messages (context + progress) don't shift the
              icon's screen position. */}
          <div className="fixed inset-0 pointer-events-none">
            <img
              src={radarLoadingIcon}
              alt=""
              aria-hidden
              // Integer offset (vw/2 − 22) — avoids sub-pixel jitter from
              // `translate(-50%, -50%)` on odd-width viewports.
              className="absolute w-11 h-11"
              style={{ left: 'calc(50% - 22px)', top: 'calc(50% - 22px)' }}
            />
            <div
              className="absolute left-1/2 -translate-x-1/2 text-center"
              style={{ top: 'calc(50% + 34px)' }}
            >
              {/* 17px semibold matches the other splash surfaces so font
                  weight doesn't visibly swap during hub → cluster
                  transitions. Subtitles below stay smaller/dimmer. */}
              <p className="whitespace-nowrap text-[17px] font-semibold tracking-tight text-theme-text-primary">
                Connecting to cluster
              </p>
              {connection.context && (
                <p className="text-sm text-theme-text-secondary mt-1">{connection.context}</p>
              )}
              {connection.progressMessage && (
                <p className="text-xs text-theme-text-tertiary animate-pulse mt-3">
                  {connection.progressMessage}
                </p>
              )}
            </div>
          </div>
        </div>
      )}

      {/* Context switching overlay — icon viewport-anchored, label below. */}
      {isSwitching && (
        <div className="flex-1 relative bg-theme-base">
          <div className="fixed inset-0 pointer-events-none">
            <img
              src={radarLoadingIcon}
              alt=""
              aria-hidden
              // Integer offset (vw/2 − 22) — avoids sub-pixel jitter from
              // `translate(-50%, -50%)` on odd-width viewports.
              className="absolute w-11 h-11"
              style={{ left: 'calc(50% - 22px)', top: 'calc(50% - 22px)' }}
            />
            <div
              className="absolute left-1/2 -translate-x-1/2 text-center"
              style={{ top: 'calc(50% + 34px)' }}
            >
              <div className="whitespace-nowrap text-[17px] font-semibold tracking-tight text-theme-text-primary">Switching context</div>
              {targetContext && (
                <div className="text-xs mt-2 text-theme-text-tertiary">
                  {targetContext.provider ? (
                    <span className="flex items-center justify-center gap-1.5">
                      <span className="text-blue-400 font-medium">{targetContext.provider}</span>
                      {targetContext.account && (
                        <>
                          <span className="text-theme-text-tertiary/50">•</span>
                          <span>{targetContext.account}</span>
                        </>
                      )}
                      {targetContext.region && (
                        <>
                          <span className="text-theme-text-tertiary/50">•</span>
                          <span>{targetContext.region}</span>
                        </>
                      )}
                      <span className="text-theme-text-tertiary/50">•</span>
                      <span className="text-theme-text-secondary font-medium">{targetContext.clusterName}</span>
                    </span>
                  ) : (
                    <span>{targetContext.raw}</span>
                  )}
                </div>
              )}
              {progressMessage && (
                <div className="text-xs mt-3 text-theme-text-tertiary animate-pulse">
                  {progressMessage}
                </div>
              )}
            </div>
          </div>
        </div>
      )}

      {/* Main content - only show when connected and authenticated */}
      {!isSwitching && !authMePending && !(authMe?.authEnabled && !authMe?.username) && connection.state === 'connected' && <div className="flex-1 flex overflow-hidden">
        <ErrorBoundary>
        {/* Home dashboard */}
        {mainView === 'home' && (
          <HomeView
            namespaces={namespaces}
            topology={topology}
            onNavigateToView={setMainView}
            onNavigateToResourceKind={(kind, apiGroup, filters) => {
              // Navigate to resources view with kind in URL path
              console.debug('[filters] App.onNavigateToResourceKind:', { kind, apiGroup, filters })
              const newParams = new URLSearchParams(searchParams)
              newParams.delete('kind') // kind is now in the path
              newParams.delete('mode')
              newParams.delete('resource')
              newParams.delete('group') // Clear topology grouping param to avoid leaking into resources view
              if (apiGroup) {
                newParams.set('apiGroup', apiGroup)
              } else {
                newParams.delete('apiGroup')
              }
              // Apply column filters if provided
              if (filters && Object.keys(filters).length > 0) {
                const filtersStr = serializeColumnFilters(filters)
                if (filtersStr) {
                  newParams.set('filters', filtersStr)
                }
              } else {
                newParams.delete('filters')
              }
              const targetURL = `/resources/${kind}?${newParams.toString()}`
              console.debug('[filters] App.onNavigateToResourceKind: navigating to', targetURL)
              navigate({ pathname: `/resources/${kind}`, search: newParams.toString() })
            }}
            onNavigateToResource={(resource) => {
              // Switch to resources view and open the resource detail drawer
              setSelectedResource(resource)
              const newParams = new URLSearchParams(searchParams)
              newParams.delete('kind') // kind is now in the path
              newParams.delete('mode')
              newParams.delete('group')
              newParams.delete('resource')
              if (resource.group) {
                newParams.set('apiGroup', resource.group)
              } else {
                newParams.delete('apiGroup')
              }
              navigate({ pathname: `/resources/${resource.kind}`, search: newParams.toString() })
            }}
          />
        )}

        {/* Topology view */}
        {mainView === 'topology' && (
          <>
            {topology?.requiresNamespaceFilter && namespaces.length === 0 ? (
              /* Large cluster: prompt user to select a namespace */
              <div className="flex-1 flex items-center justify-center">
                <div className="max-w-md w-full mx-4 text-center">
                  <div className="bg-theme-surface border border-theme-border rounded-xl shadow-lg p-6">
                    <div className="w-12 h-12 mx-auto mb-4 rounded-full bg-blue-500/10 flex items-center justify-center">
                      <Network className="w-6 h-6 text-blue-400" />
                    </div>
                    <h2 className="text-lg font-semibold text-theme-text-primary mb-2">
                      Large Cluster Detected
                    </h2>
                    <p className="text-sm text-theme-text-secondary mb-5">
                      This cluster has too many resources to render the full topology.
                      Select a namespace to explore.
                    </p>
                    <div className="relative">
                      <LargeClusterNamespacePicker
                        namespaces={availableNamespaces}
                        onSelect={(ns) => {
                          setNamespaces([ns])
                          setActiveNamespace.mutate({ namespaces: [ns] })
                          // Large clusters need server-side filtering — reconnect SSE with namespace
                          setForceNamespaceFilter([ns])
                        }}
                      />
                    </div>
                  </div>
                </div>
              </div>
            ) : (
              <>
                {/* Filter sidebar */}
                <TopologyFilterSidebar
                  nodes={topology?.nodes || []}
                  visibleKinds={visibleKinds}
                  onToggleKind={handleToggleKind}
                  onShowAll={handleShowAllKinds}
                  onHideAll={handleHideAllKinds}
                  collapsed={filterSidebarCollapsed}
                  onToggleCollapse={() => setFilterSidebarCollapsed(prev => !prev)}
                  hiddenKinds={topology?.hiddenKinds}
                  onEnableHiddenKind={(kind) => {
                    setVisibleKinds(prev => new Set(prev).add(kind as NodeKind))
                    console.log(`[topology] User requested to show hidden kind: ${kind}`)
                  }}
                />

                <div className="flex-1 relative">
                  <TopologyGraph
                    topology={filteredTopology}
                    viewMode={topologyMode}
                    groupingMode={effectiveGroupingMode}
                    hideGroupHeader={hideGroupHeader}
                    onNodeClick={handleNodeClick}
                    selectedNodeId={selectedResource ? `${apiResourceToNodeIdPrefix(selectedResource.kind)}-${selectedResource.namespace}-${selectedResource.name}` : undefined}
                    paused={topologyPaused}
                    onTogglePause={handleTogglePause}
                    onMaximizeNamespace={(ns) => setActiveNamespace.mutate({ namespaces: [ns] })}
                    namespaceBreadcrumb={namespaces.length === 1 ? namespaces[0] : undefined}
                    onClearNamespace={namespaces.length >= 1 ? () => setActiveNamespace.mutate({ namespaces: [] }) : undefined}
                    namespacesKey={namespaces.join(',')}
                  />

                  {/* Topology controls overlay - top right */}
                  <TopologyControls
                    viewMode={topologyMode}
                    onViewModeChange={(mode) => {
                      setTopologyMode(mode)
                      // Fleet mode: namespace grouping for structure, but expanded (not collapsed chips)
                      if (mode === 'fleet') setGroupingMode('namespace')
                    }}
                    groupingMode={groupingMode}
                    onGroupingModeChange={setGroupingMode}
                    showNoGrouping={hasNamespaceFilter}
                    showPolicyEffect={showPolicyEffect}
                    onShowPolicyEffectChange={setShowPolicyEffect}
                    showFleetMode={displayedTopology?.nodes?.some(n => FLEET_MODE_KINDS.has(n.kind as NodeKind)) ?? false}
                  />
                </div>
              </>
            )}
          </>
        )}

        {/* Resources view */}
        {mainView === 'resources' && (
          <ResourcesView
            namespaces={namespaces}
            selectedResource={routeSelectedResource}
            onResourceClick={(res) => res ? navigateToResource(res) : setSelectedResource(null)}
            onResourceClickYaml={(res) => navigateToResource(res, 'yaml')}
            onKindChange={() => setSelectedResource(null)}
            onClearNamespaces={clearAllNamespaces}
          />
        )}

        {/* Timeline view */}
        {mainView === 'timeline' && (
          <TimelineView
            namespaces={namespaces}
            onResourceClick={(resource) => {
              navigate(buildWorkloadPath(resource))
            }}
            initialViewMode={(searchParams.get('view') as 'list' | 'swimlane') || undefined}
            initialFilter={(searchParams.get('filter') as 'all' | 'changes' | 'k8s_events' | 'warnings' | 'unhealthy') || undefined}
            initialTimeRange={(searchParams.get('time') as '5m' | '30m' | '1h' | '6h' | '24h' | 'all') || undefined}
            requiresNamespaceFilter={topology?.requiresNamespaceFilter && namespaces.length === 0}
            availableNamespaces={availableNamespaces}
            onNamespaceSelect={(ns) => {
              setNamespaces([ns])
              setActiveNamespace.mutate({ namespaces: [ns] })
            }}
          />
        )}

        {/* Helm view - always show all namespaces since releases span multiple ns */}
        {mainView === 'helm' && (
          <HelmView
            namespace=""
            selectedRelease={selectedHelmRelease}
            onReleaseClick={(ns, name, storageNamespace) => {
              setSelectedHelmRelease({ namespace: ns, name, storageNamespace })
              const params = new URLSearchParams(window.location.search)
              params.set('release', `${ns}/${name}`)
              if (storageNamespace) {
                params.set('releaseStorage', storageNamespace)
              } else {
                params.delete('releaseStorage')
              }
              setSearchParams(params, { replace: true })
            }}
          />
        )}

        {/* GitOps view */}
        {mainView === 'gitops' && (
          <GitOpsView
            namespaces={namespaces}
            onOpenResource={(resource) => {
              setSelectedResource(resource)
            }}
            onClearNamespaces={clearAllNamespaces}
          />
        )}

        {/* Traffic view */}
        {mainView === 'traffic' && (
          <TrafficView namespaces={namespaces} />
        )}

        {/* Cost detail view */}
        {mainView === 'cost' && (
          <CostView onBack={() => setMainView('home')} />
        )}

        {/* Best practices detail view. In Cloud this redirects to the host's
            fleet Checks queue (clusterChecksHref effect above) — render a brief
            splash instead of the single-cluster view while the cross-document
            nav lands. */}
        {mainView === 'audit' && clusterChecksHref && (
          <div className="flex-1 flex flex-col items-center justify-center gap-3 bg-theme-base">
            <img src={radarLoadingIcon} alt="" aria-hidden className="w-11 h-11" />
            <p className="text-sm text-theme-text-secondary">Opening Checks…</p>
          </div>
        )}
        {mainView === 'audit' && !clusterChecksHref && (
          <AuditView
            namespaces={namespaces}
            onBack={() => setMainView('home')}
            onNavigateToResource={(resource) => {
              const pluralKind = kindToPlural(resource.kind)
              setSelectedResource({ ...resource, kind: pluralKind })
              const newParams = new URLSearchParams(searchParams)
              newParams.delete('kind')
              newParams.delete('mode')
              newParams.delete('group')
              newParams.delete('resource')
              if (resource.group) {
                newParams.set('apiGroup', resource.group)
              } else {
                newParams.delete('apiGroup')
              }
              navigate({ pathname: `/resources/${pluralKind}`, search: newParams.toString() })
            }}
          />
        )}

        {/* Workload full view (direct URL only — expand from drawer uses drawer's expanded state) */}
        {mainView === 'workload' && !drawerExpanded && (
          <WorkloadViewRoute
            onNavigateToResource={(resource) => {
              navigate(buildWorkloadPath(resource))
            }}
          />
        )}

        {/* Compare two resources of the same kind side-by-side */}
        {mainView === 'compare' && <CompareViewRoute />}

        </ErrorBoundary>
      </div>}

      {/* Resource detail drawer — stays mounted, expands to full-screen WorkloadView */}
      {resourceDrawer.shouldRender && drawerResource && (
        <ResourceDetailDrawer
          resource={drawerResource}
          initialTab={drawerInitialTab}
          isOpen={resourceDrawer.isOpen}
          expanded={drawerExpanded}
          onClose={() => { setSelectedResource(null); setDrawerInitialTab('detail'); setDrawerExpanded(false) }}
          onNavigate={(res) => navigateToResource(res)}
          onExpand={(res) => {
            suppressViewClearRef.current = true
            setDrawerExpanded(true)
            navigate(buildWorkloadPath(res))
          }}
          onCollapse={handleCollapseFromExpanded}
          onNavigateToResource={(resource) => {
            setSelectedResource(resource)
            navigate(buildWorkloadPath(resource), { replace: true })
          }}
        />
      )}

      {/* Helm release drawer */}
      {helmDrawer.shouldRender && drawerHelmRelease && (
        <HelmReleaseDrawer
          release={drawerHelmRelease}
          isOpen={helmDrawer.isOpen}
          onClose={() => {
            setSelectedHelmRelease(null)
            const params = new URLSearchParams(window.location.search)
            params.delete('release')
            params.delete('releaseStorage')
            setSearchParams(params, { replace: true })
          }}
          onNavigateToResource={(resource) => {
            setSelectedHelmRelease(null)
            const newParams = new URLSearchParams()
            const globalNamespaces = searchParams.get('namespaces')
            if (globalNamespaces) newParams.set('namespaces', globalNamespaces)
            if (resource.group) newParams.set('apiGroup', resource.group)
            navigate({ pathname: `/resources/${resource.kind}`, search: newParams.toString() })
            setSelectedResource(resource)
          }}
        />
      )}

      {/* Port Forward floating panel (indicator lives in header) */}
      <PortForwardPanel />

      {/* Update notification — hidden in embedded mode (OSS download nudge). */}
      {!navCustomization.embedded && <UpdateNotification />}

      {/* Bottom Dock for Terminal/Logs */}
      <BottomDock />

      {/* Spacer for dock */}
      <DockSpacer />

      {/* Floating action buttons — bottom-right, above dock */}
      <FloatingButtons showHelp={showHelp} showCommandPalette={showCommandPalette} showDiagnostics={showDiagnostics} onHelp={() => setShowHelp(true)} onBugReport={() => setShowDiagnostics(true)} />

      {/* Keyboard shortcut help overlay */}
      {helpOverlay.shouldRender && <ShortcutHelpOverlay isOpen={helpOverlay.isOpen} onClose={() => setShowHelp(false)} currentView={mainView} />}

      {/* Command palette */}
      {commandPaletteAnim.shouldRender && (
        <CommandPalette
          isOpen={commandPaletteAnim.isOpen}
          onClose={() => setShowCommandPalette(false)}
          onNavigateView={(view) => setMainView(view)}
          onNavigateKind={(kind, group) => {
            const params = new URLSearchParams(searchParams)
            params.delete('kind')
            if (group) params.set('apiGroup', group)
            else params.delete('apiGroup')
            params.delete('resource')
            navigate({ pathname: `/resources/${kind}`, search: params.toString() })
            // Focus the table search after navigation — the user came from ⌘K
            // (keyboard flow) and expects to type a resource name immediately.
            setTimeout(() => {
              (document.querySelector('input[placeholder="Search... (press /)"]') as HTMLInputElement)?.focus()
            }, 100)
          }}
          onSwitchContext={(name) => switchContext.mutate(
            { name },
            // Namespace filter from the previous context may not exist in the
            // new one — clear it so resource lists don't silently go empty.
            // The server clears all per-user picks on context switch already;
            // local state mirrors that via the namespace-scope effect.
            { onSettled: () => setNamespaces([]) },
          )}
          onSetNamespaces={(ns) => {
            setNamespaces(ns)
            setActiveNamespace.mutate({ namespaces: ns })
          }}
          onToggleTheme={toggleTheme}
          onShowDiagnostics={() => setShowDiagnostics(true)}
        />
      )}

      {/* Diagnostics overlay */}
      {diagnosticsOverlay.shouldRender && <DiagnosticsOverlay isOpen={diagnosticsOverlay.isOpen} onClose={() => setShowDiagnostics(false)} />}

      {/* Settings dialog */}
      <SettingsDialog open={showSettings} onClose={() => setShowSettings(false)} />
      <MyPermissionsDialog open={showMyPermissions} onClose={() => setShowMyPermissions(false)} />

      {/* Debug overlay - only in dev mode */}
      {import.meta.env.DEV && <DebugOverlay />}
    </div>
    </PortForwardProvider>
  )
}

// Spacer component that adds padding when dock is open
function DockSpacer() {
  const { tabs, isResizing } = useDock()
  const dockInset = useDockReservedHeight()
  const location = useLocation()
  // Traffic view manages its own layout — spacer would break its flex sizing
  if (tabs.length === 0 || location.pathname === '/traffic') return null
  return (
    <div
      className="shrink-0"
      style={{
        height: dockInset,
        transition: isResizing ? 'none' : `height ${DURATION_DOCK}ms cubic-bezier(0.4, 0, 0.2, 1)`,
      }}
    />
  )
}

// Floating action buttons that position themselves above the dock
function FloatingButtons({ showHelp, showCommandPalette, showDiagnostics, onHelp, onBugReport }: { showHelp: boolean; showCommandPalette: boolean; showDiagnostics: boolean; onHelp: () => void; onBugReport: () => void }) {
  const { tabs } = useDock()
  if (showHelp || showCommandPalette || showDiagnostics) return null
  // When dock tab bar is visible (36px), shift the buttons up above it
  const bottom = tabs.length > 0 ? 'bottom-10' : 'bottom-2'
  const btnClass = 'w-7 h-7 flex items-center justify-center rounded-full bg-theme-elevated/80 hover:bg-theme-hover border border-theme-border-light text-theme-text-tertiary hover:text-theme-text-secondary text-xs font-medium shadow-sm backdrop-blur-sm transition-all'
  return (
    <div className={`fixed ${bottom} right-4 z-40 flex items-center gap-1.5`}>
      <Tooltip content="Report bug / Diagnostics" position="top">
        <button onClick={onBugReport} className={btnClass}>
          <Bug className="w-3.5 h-3.5" />
        </button>
      </Tooltip>
      <Tooltip content="Keyboard shortcuts (?)" position="top">
        <button onClick={onHelp} className={btnClass}>
          ?
        </button>
      </Tooltip>
    </div>
  )
}

// Main App component wrapped with providers
function App() {
  return (
    <ConnectionProvider>
      <CapabilitiesProvider>
        <ContextSwitchProvider>
          <DockProvider>
            <KeyboardShortcutProvider>
              <AppInner />
            </KeyboardShortcutProvider>
          </DockProvider>
        </ContextSwitchProvider>
      </CapabilitiesProvider>
    </ConnectionProvider>
  )
}

// Header brand: emerald-square radar icon + stacked "Radar" / "by Skyhook"
// wordmark. Shares its visual shape with the radar-hub-web shell so the
// standalone OSS app and the embedded Cloud experience read as the same
// product, and is narrow enough to leave room for the cluster switcher
// and nav block on standard laptop viewports.
function Logo() {
  return (
    <div className="flex items-center gap-2.5">
      <div className="relative w-7 h-7 rounded-lg overflow-hidden flex-shrink-0 bg-emerald-500/10 border border-emerald-500/20">
        <img
          src="/images/radar/radar-icon.svg"
          alt=""
          aria-hidden
          className="w-full h-full p-0.5"
          // Fail loud on a missing/blocked asset rather than rendering an
          // empty emerald square next to the wordmark — the latter reads
          // as broken chrome with no diagnostics. Most likely cause is a
          // build/deploy path mismatch.
          onError={(e) =>
            console.error('Radar logo asset failed to load:', (e.currentTarget as HTMLImageElement).src)
          }
        />
      </div>
      <div className="flex flex-col leading-none">
        <span className="font-semibold text-[15px] tracking-tight text-theme-text-primary">Radar</span>
        <span className="text-[9px] mt-0.5 tracking-wide uppercase text-theme-text-tertiary">by Skyhook</span>
      </div>
    </div>
  )
}

// GitHub star button with live star count + programmatic starring via gh CLI
// Shows a callout popover when the backend says shouldPrompt is true (synced with CLI state)
function GitHubStarButton() {
  const [starCount, setStarCount] = useState<number | null>(null)
  const [starred, setStarred] = useState(false)
  const [ghAvailable, setGhAvailable] = useState(false)
  const [showCallout, setShowCallout] = useState(false)
  const calloutRef = useRef<HTMLDivElement>(null)
  const buttonRef = useRef<HTMLAnchorElement>(null)

  useEffect(() => {
    // Fetch star count from GitHub public API
    fetch('https://api.github.com/repos/skyhook-io/radar')
      .then(res => res.ok ? res.json() : null)
      .then(data => { if (data && typeof data.stargazers_count === 'number') setStarCount(data.stargazers_count) })
      .catch(() => {})

    // Check if user already starred (via backend/gh CLI) and whether to show prompt
    fetch(apiUrl('/github/starred'), { credentials: getCredentialsMode(), headers: getAuthHeaders() })
      .then(res => res.ok ? res.json() : null)
      .then(data => {
        if (data) {
          setStarred(data.starred)
          setGhAvailable(data.ghAvailable)
          if (data.shouldPrompt && !data.starred) {
            // Delay the callout, then re-check in case CLI prompted during the wait
            setTimeout(() => {
              fetch(apiUrl('/github/starred'), { credentials: getCredentialsMode(), headers: getAuthHeaders() })
                .then(res => res.ok ? res.json() : null)
                .then(fresh => {
                  if (fresh?.shouldPrompt && !fresh.starred) {
                    setShowCallout(true)
                  }
                })
                .catch(() => {})
            }, 3000)
          }
        }
      })
      .catch(() => {})
  }, [])

  const handleDismiss = useCallback(() => {
    setShowCallout(false)
    fetch(apiUrl('/github/dismiss'), { method: 'POST', credentials: getCredentialsMode(), headers: getAuthHeaders() }).catch(() => {})
  }, [])

  // Close callout when clicking outside
  useEffect(() => {
    if (!showCallout) return
    const handleClickOutside = (e: MouseEvent) => {
      if (
        calloutRef.current && !calloutRef.current.contains(e.target as Node) &&
        buttonRef.current && !buttonRef.current.contains(e.target as Node)
      ) {
        handleDismiss()
      }
    }
    document.addEventListener('mousedown', handleClickOutside)
    return () => document.removeEventListener('mousedown', handleClickOutside)
  }, [showCallout, handleDismiss])

  const handleClick = (e: React.MouseEvent) => {
    if (starred) return // Already starred, just let the link open GitHub

    if (ghAvailable) {
      // Star via backend gh CLI
      e.preventDefault()
      fetch(apiUrl('/github/star'), { method: 'POST', credentials: getCredentialsMode(), headers: getAuthHeaders() })
        .then(res => res.ok ? res.json() : null)
        .then(data => {
          if (data?.starred) {
            setStarred(true)
            setShowCallout(false)
            setStarCount(prev => prev !== null ? prev + 1 : prev)
          }
        })
        .catch(() => {
          // Fallback: open GitHub in browser
          openExternal('https://github.com/skyhook-io/radar')
        })
    } else {
      // No gh CLI — link opens GitHub; dismiss the callout
      setShowCallout(false)
      fetch(apiUrl('/github/dismiss'), { method: 'POST', credentials: getCredentialsMode(), headers: getAuthHeaders() }).catch(() => {})
    }
  }

  return (
    <div className="relative">
      <a
        ref={buttonRef}
        href="https://github.com/skyhook-io/radar"
        target="_blank"
        rel="noopener noreferrer"
        onClick={handleClick}
        className="flex items-center gap-1.5 h-7 px-2 rounded-md transition-colors bg-theme-elevated hover:bg-theme-hover text-theme-text-secondary hover:text-theme-text-primary"
      >
        <svg className="w-4 h-4" viewBox="0 0 16 16" fill="currentColor"><path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27s1.36.09 2 .27c1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0016 8c0-4.42-3.58-8-8-8z"/></svg>
        <Star className={`w-3 h-3 hidden xl:block ${starred ? 'text-yellow-500 fill-current' : ''}`} />
        {starCount !== null && (
          <>
            <span className="w-px h-3 bg-theme-border hidden xl:block" />
            <span className="text-xs tabular-nums hidden xl:inline">{starCount.toLocaleString()}</span>
          </>
        )}
      </a>

      {/* Callout popover — synced with CLI star.json state */}
      {showCallout && (
        <div
          ref={calloutRef}
          className="absolute top-full right-0 mt-2 w-64 p-3 bg-theme-surface border border-theme-border rounded-lg shadow-lg z-50"
        >
          {/* Arrow */}
          <div className="absolute -top-1.5 right-4 w-3 h-3 bg-theme-surface border-l border-t border-theme-border rotate-45" />
          <p className="text-sm text-theme-text-primary mb-2">
            Enjoying Radar? Show your support with a star!
          </p>
          <div className="flex items-center gap-2">
            <a
              href="https://github.com/skyhook-io/radar"
              target="_blank"
              rel="noopener noreferrer"
              onClick={handleClick}
              className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium bg-yellow-500/15 text-yellow-500 hover:bg-yellow-500/25 rounded-md transition-colors"
            >
              <Star className="w-3.5 h-3.5" />
              Star on GitHub
            </a>
            <button
              onClick={handleDismiss}
              className="px-2 py-1.5 text-xs text-theme-text-tertiary hover:text-theme-text-secondary transition-colors"
            >
              Maybe later
            </button>
          </div>
        </div>
      )}
    </div>
  )
}

// Theme toggle button component
function ThemeToggle() {
  const { theme, toggleTheme } = useTheme()

  return (
    <button
      onClick={toggleTheme}
      className="p-1.5 rounded-md bg-theme-elevated hover:bg-theme-hover text-theme-text-secondary hover:text-theme-text-primary transition-colors"
      title={`Switch to ${theme === 'dark' ? 'light' : 'dark'} mode`}
    >
      {theme === 'dark' ? (
        <Sun className="w-4 h-4" />
      ) : (
        <Moon className="w-4 h-4" />
      )}
    </button>
  )
}

export default App
