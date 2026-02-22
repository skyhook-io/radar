import { useState, useEffect, useCallback, useMemo, useRef } from 'react'
import { useRefreshAnimation } from './hooks/useRefreshAnimation'
import { useQueryClient } from '@tanstack/react-query'
import { useNavigate, useLocation, useSearchParams } from 'react-router-dom'
import { HomeView } from './components/home/HomeView'
import { DebugOverlay } from './components/DebugOverlay'
import { TopologyGraph } from './components/topology/TopologyGraph'
import { TopologyFilterSidebar } from './components/topology/TopologyFilterSidebar'
import { TimelineView } from './components/timeline/TimelineView'
import { ResourcesView } from './components/resources/ResourcesView'
import { serializeColumnFilters } from './components/resources/resource-utils'
import { ResourceDetailDrawer } from './components/resources/ResourceDetailDrawer'
import { ResourceDetailPage } from './components/resource/ResourceDetailPage'
import { HelmView } from './components/helm/HelmView'
import { TrafficView } from './components/traffic/TrafficView'
import { HelmReleaseDrawer } from './components/helm/HelmReleaseDrawer'
import { PortForwardManager, usePortForwardCount } from './components/portforward/PortForwardManager'
import { DockProvider, BottomDock, useDock } from './components/dock'
import { ContextSwitcher } from './components/ContextSwitcher'
import { ContextSwitchProvider, useContextSwitch } from './context/ContextSwitchContext'
import { ConnectionProvider, useConnection } from './context/ConnectionContext'
import { ConnectionErrorView } from './components/ConnectionErrorView'
import { CapabilitiesProvider } from './contexts/CapabilitiesContext'
import { ErrorBoundary } from './components/ui/ErrorBoundary'
import { NamespaceSelector } from './components/ui/NamespaceSelector'
import { UpdateNotification } from './components/ui/UpdateNotification'
import { useEventSource } from './hooks/useEventSource'
import { useNamespaces } from './api/client'
import { Loader2 } from 'lucide-react'
import { RefreshCw, FolderTree, Network, List, Clock, Package, Sun, Moon, Activity, Home, Star } from 'lucide-react'
import { useTheme } from './context/ThemeContext'
import { Tooltip } from './components/ui/Tooltip'
import type { TopologyNode, GroupingMode, MainView, SelectedResource, SelectedHelmRelease, NodeKind, Topology } from './types'
import { kindToPlural, openExternal } from './utils/navigation'

// All possible node kinds (core + GitOps)
const ALL_NODE_KINDS: NodeKind[] = [
  'Internet', 'Ingress', 'Gateway', 'HTTPRoute', 'GRPCRoute', 'TCPRoute', 'TLSRoute',
  'Service', 'Deployment', 'Rollout', 'DaemonSet', 'StatefulSet',
  'ReplicaSet', 'Pod', 'PodGroup', 'ConfigMap', 'Secret', 'HorizontalPodAutoscaler', 'Job', 'CronJob', 'PersistentVolumeClaim', 'Namespace',
  'Application', 'Kustomization', 'HelmRelease', 'GitRepository'
]

// Default visible kinds (ReplicaSet hidden by default - noisy intermediate object)
const DEFAULT_VISIBLE_KINDS = ALL_NODE_KINDS.filter(k => k !== 'ReplicaSet')

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
  }
  return prefixMap[apiResource] || apiResource.replace(/s$/, '')
}

// Parse resource param from URL (format: "Kind/namespace/name")
function parseResourceParam(param: string | null): SelectedResource | null {
  if (!param) return null
  const parts = param.split('/')
  if (parts.length < 3) return null
  const [kind, ns, ...nameParts] = parts
  return { kind, namespace: ns, name: nameParts.join('/') }
}

// Encode resource to URL param
function encodeResourceParam(resource: SelectedResource): string {
  return `${resource.kind}/${resource.namespace}/${resource.name}`
}

// Extended MainView type that includes traffic
type ExtendedMainView = MainView | 'traffic'

// Extract view from URL path
function getViewFromPath(pathname: string): ExtendedMainView {
  const path = pathname.replace(/^\//, '').split('/')[0]
  if (path === '' || path === 'home') return 'home'
  if (path === 'topology') return 'topology'
  if (path === 'resources') return 'resources'
  if (path === 'timeline') return 'timeline'
  if (path === 'helm') return 'helm'
  if (path === 'traffic') return 'traffic'
  return 'home'
}

function AppInner() {
  const navigate = useNavigate()
  const location = useLocation()
  const [searchParams, setSearchParams] = useSearchParams()

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
    const resource = parseResourceParam(searchParams.get('resource'))
    return {
      namespaces,
      topologyMode: (searchParams.get('mode') as 'resources' | 'traffic') || 'resources',
      // Default to namespace grouping when viewing all namespaces
      grouping: (searchParams.get('group') as GroupingMode) || (namespaces.length === 0 ? 'namespace' : 'none'),
      detailResource: resource,
    }
  }

  // Get mainView from URL path
  const mainView = getViewFromPath(location.pathname)

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

  const [namespaces, setNamespaces] = useState<string[]>(getInitialState().namespaces)
  const [selectedResource, setSelectedResource] = useState<SelectedResource | null>(null)
  const [selectedHelmRelease, setSelectedHelmRelease] = useState<SelectedHelmRelease | null>(null)
  const [topologyMode, setTopologyMode] = useState<'resources' | 'traffic'>(getInitialState().topologyMode)
  const [groupingMode, setGroupingMode] = useState<GroupingMode>(getInitialState().grouping)
  // Resource detail page state (for timeline view drill-down)
  const [detailResource, setDetailResourceState] = useState<SelectedResource | null>(getInitialState().detailResource)

  // Wrapper to handle detail resource navigation with proper history
  const setDetailResource = useCallback((resource: SelectedResource | null) => {
    if (resource) {
      // Navigating INTO detail view - push new history entry
      const params = new URLSearchParams(searchParams)
      params.set('resource', encodeResourceParam(resource))
      navigate({ pathname: '/timeline', search: params.toString() })
    } else {
      // Navigating OUT of detail view - use browser back if possible, or just clear param
      const params = new URLSearchParams(searchParams)
      params.delete('resource')
      navigate({ pathname: '/timeline', search: params.toString() })
    }
    setDetailResourceState(resource)
  }, [navigate, searchParams])
  // Topology filter state
  const [visibleKinds, setVisibleKinds] = useState<Set<NodeKind>>(() => new Set(DEFAULT_VISIBLE_KINDS))
  const [filterSidebarCollapsed, setFilterSidebarCollapsed] = useState(false)

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

  // Hide group header when viewing a single namespace with "no grouping" selected
  // (grouping header is meaningless with only one namespace, but needed for multi-namespace)
  const hideGroupHeader = namespaces.length === 1 && groupingMode === 'none'

  // Fetch available namespaces
  const { data: availableNamespaces, error: namespacesError } = useNamespaces()

  // Context switch state
  const { isSwitching, targetContext, progressMessage, updateProgress, endSwitch } = useContextSwitch()

  // Connection state (for graceful startup)
  const { connection, retry: retryConnection, isRetrying, updateFromSSE: updateConnectionFromSSE } = useConnection()

  // Query client for cache invalidation
  const queryClient = useQueryClient()

  // SSE connection for real-time updates
  const { topology, connected, reconnect: reconnectSSE } = useEventSource(namespaces, topologyMode, {
    onContextSwitchComplete: endSwitch,
    onContextSwitchProgress: updateProgress,
    onContextChanged: () => {
      // Clear all React Query caches when cluster context changes
      // This ensures helm releases, resources, etc. are refetched from the new cluster
      // removeQueries clears cached data, invalidateQueries triggers refetch
      queryClient.removeQueries()
      queryClient.invalidateQueries()

      // Reset URL to current view with no resource-specific params.
      // Old cluster's selected pod/resource/kind don't exist on the new cluster.
      navigate({ pathname: location.pathname, search: '' }, { replace: true })
    },
    onConnectionStateChange: updateConnectionFromSSE,
  })
  const [reconnect, isReconnecting] = useRefreshAnimation(reconnectSSE)

  // Track CRD discovery status from topology (more direct than cluster-info)
  // When discovery completes, topology will auto-update via SSE with new CRD nodes
  const crdDiscoveryStatus = topology?.crdDiscoveryStatus

  // Debug: log discovery status changes
  useEffect(() => {
    if (crdDiscoveryStatus) {
      console.log('[CRD Discovery] Status:', crdDiscoveryStatus)
    }
  }, [crdDiscoveryStatus])

  // Handle node selection - convert TopologyNode to SelectedResource for the drawer
  const handleNodeClick = useCallback((node: TopologyNode) => {
    // Skip Internet node - it's not a real resource
    if (node.kind === 'Internet') return

    // For PodGroup, we can't open a single resource drawer
    // TODO: Could show a list of pods in the group
    if (node.kind === 'PodGroup') return

    setSelectedResource({
      kind: kindToPlural(node.kind),
      namespace: (node.data.namespace as string) || '',
      name: node.name,
    })
  }, [])

  // Serialize namespaces for stable dependency tracking
  const namespacesKey = namespaces.join(',')

  // Update URL query params when state changes (path is handled by setMainView)
  useEffect(() => {
    const params = new URLSearchParams(searchParams)

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

    // Note: resource param for timeline detail view is handled by setDetailResource wrapper
    // to ensure proper history push/pop behavior

    // Only update if params changed
    if (params.toString() !== searchParams.toString()) {
      setSearchParams(params, { replace: true })
    }
  }, [namespacesKey, topologyMode, groupingMode, mainView, searchParams, setSearchParams])

  // Sync state from URL when navigating (back/forward)
  useEffect(() => {
    const urlNamespaces = parseNamespacesFromURL(searchParams)
    const resource = parseResourceParam(searchParams.get('resource'))

    if (urlNamespaces.join(',') !== namespacesKey) setNamespaces(urlNamespaces)
    // Use raw setter to avoid triggering navigate() again
    if (JSON.stringify(resource) !== JSON.stringify(detailResource)) setDetailResourceState(resource)
  }, [searchParams])

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
  // And don't clear detailResource if we're navigating to timeline view (could be from URL)
  const prevMainView = useRef(mainView)
  useEffect(() => {
    const navigatingToResources = mainView === 'resources' && prevMainView.current !== 'resources'
    prevMainView.current = mainView

    // Don't clear selectedResource when navigating TO resources view (deep link from Helm)
    if (!navigatingToResources) {
      setSelectedResource(null)
    }
    setSelectedHelmRelease(null)
    // Only clear detailResource when leaving timeline view or changing namespaces
    if (mainView !== 'timeline') {
      setDetailResourceState(null)
    }
  }, [mainView])

  // Clear detail resource when namespaces change (separate effect)
  useEffect(() => {
    setDetailResourceState(null)
    setSelectedResource(null)
    setSelectedHelmRelease(null)
  }, [namespacesKey])

  // Filter topology based on visible kinds
  const filteredTopology = useMemo((): Topology | null => {
    if (!topology) return null

    const filteredNodes = topology.nodes.filter(node => visibleKinds.has(node.kind))
    const filteredNodeIds = new Set(filteredNodes.map(n => n.id))

    // Keep edges where both source and target are visible
    // Also respect skipIfKindVisible - hide shortcut edges when intermediate kind is shown
    const filteredEdges = topology.edges.filter(edge => {
      // Both endpoints must be visible
      if (!filteredNodeIds.has(edge.source) || !filteredNodeIds.has(edge.target)) {
        return false
      }
      // If this is a shortcut edge, hide it when the intermediate kind is visible
      if (edge.skipIfKindVisible && visibleKinds.has(edge.skipIfKindVisible as NodeKind)) {
        return false
      }
      return true
    })

    return {
      nodes: filteredNodes,
      edges: filteredEdges,
    }
  }, [topology, visibleKinds])

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
    <div className="flex flex-col h-screen bg-theme-base min-w-[800px]">
      {/* Header */}
      <header className="relative flex items-center justify-between px-4 py-2 bg-theme-surface border-b border-theme-border">
        {/* Left: Logo + Cluster info */}
        <div className="flex items-center gap-4 shrink-0">
          <div className="flex items-center gap-2.5">
            <Logo />
            <span className="text-xl text-theme-text-primary leading-none -translate-y-0.5" style={{ fontFamily: "'DM Sans', sans-serif", fontWeight: 520 }}>radar</span>
          </div>

          <div className="flex items-center gap-2">
            <ContextSwitcher />
            {/* Connection status - next to cluster name */}
            <div className="flex items-center gap-1.5 ml-1">
              <span
                className={`w-2 h-2 rounded-full ${
                  connected ? 'bg-green-500' : 'bg-red-500'
                }`}
              />
              <span className="text-xs text-theme-text-tertiary hidden xl:inline">
                {connected ? 'Connected' : 'Disconnected'}
              </span>
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
          </div>
        </div>

        {/* Center: View tabs — absolute centered on wide, flows after left section on narrow */}
        <div className="md:absolute md:left-1/2 md:-translate-x-1/2 flex items-center gap-1 bg-theme-elevated/50 rounded-lg p-1 ml-2 md:ml-0">
          {([
            { view: 'home' as const, icon: Home, label: 'Home' },
            { view: 'topology' as const, icon: Network, label: 'Topology' },
            { view: 'resources' as const, icon: List, label: 'Resources' },
            { view: 'timeline' as const, icon: Clock, label: 'Timeline' },
            { view: 'helm' as const, icon: Package, label: 'Helm' },
            { view: 'traffic' as const, icon: Activity, label: 'Traffic' },
          ] as const).map(({ view, icon: Icon, label }) => (
            <Tooltip key={view} content={label} delay={100} position="bottom">
              <button
                onClick={() => setMainView(view)}
                className={`flex items-center gap-1.5 px-2.5 py-1.5 text-sm rounded-md transition-colors ${
                  mainView === view
                    ? 'bg-blue-500 text-theme-text-primary'
                    : 'text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-hover'
                }`}
              >
                <Icon className="w-4 h-4" />
                <span className="hidden lg:inline">{label}</span>
              </button>
            </Tooltip>
          ))}
        </div>

        {/* Right: Controls */}
        <div className="flex items-center gap-3 shrink-0">
          {/* CRD Discovery indicator */}
          {crdDiscoveryStatus === 'discovering' && (
            <div className="flex items-center gap-1.5 text-xs text-amber-400">
              <Loader2 className="w-3 h-3 animate-spin" />
              <span className="hidden md:inline">Discovering CRDs...</span>
            </div>
          )}
          {/* Namespace selector with search */}
          <NamespaceSelector
            value={namespaces}
            onChange={setNamespaces}
            namespaces={availableNamespaces}
            namespacesError={namespacesError}
            disabled={mainView === 'helm'}
            disabledTooltip="Helm view always shows all namespaces"
          />

          {/* GitHub star */}
          <GitHubStarButton />

          {/* Theme toggle */}
          <ThemeToggle />
        </div>
      </header>

      {/* Connection error view - show when disconnected */}
      {!isSwitching && connection.state === 'disconnected' && (
        <ConnectionErrorView
          connection={connection}
          onRetry={retryConnection}
          isRetrying={isRetrying}
        />
      )}

      {/* Connecting view - show during initial connection or retry */}
      {!isSwitching && connection.state === 'connecting' && (
        <div className="flex-1 flex items-center justify-center bg-theme-base">
          <div className="flex flex-col items-center gap-4 text-theme-text-secondary">
            <Loader2 className="w-8 h-8 animate-spin text-blue-400" />
            <div className="text-center">
              <p className="font-medium text-theme-text-primary">Connecting to cluster</p>
              <p className="text-sm text-theme-text-secondary mt-1">{connection.context || 'Loading...'}</p>
              {connection.progressMessage && (
                <p className="text-xs text-theme-text-tertiary animate-pulse mt-3">
                  {connection.progressMessage}
                </p>
              )}
            </div>
          </div>
        </div>
      )}

      {/* Context switching overlay */}
      {isSwitching && (
        <div className="flex-1 flex items-center justify-center bg-theme-base">
          <div className="flex flex-col items-center gap-4 text-theme-text-secondary">
            <Loader2 className="w-8 h-8 animate-spin text-blue-400" />
            <div className="text-center">
              <div className="text-sm font-medium text-theme-text-primary">Switching context</div>
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

      {/* Main content - only show when connected */}
      {!isSwitching && connection.state === 'connected' && <div className="flex-1 flex overflow-hidden">
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
                // Add the kind to visible kinds - the actual data is not available
                // since it was hidden server-side, but this prepares for when
                // we add query params to request specific kinds
                setVisibleKinds(prev => new Set(prev).add(kind as NodeKind))
                // TODO: Re-fetch topology with this kind enabled via query param
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
              />

              {/* Topology controls overlay - top right */}
              <div className="absolute top-4 right-4 z-10 flex items-center gap-2">
                {/* Grouping selector */}
                <div className="flex items-center gap-1.5 px-2 py-1.5 bg-theme-surface/90 backdrop-blur border border-theme-border rounded-lg">
                  <FolderTree className="w-3.5 h-3.5 text-theme-text-secondary" />
                  <select
                    value={groupingMode}
                    onChange={(e) => setGroupingMode(e.target.value as GroupingMode)}
                    className="appearance-none bg-transparent text-theme-text-primary text-xs focus:outline-none cursor-pointer"
                  >
                    {hasNamespaceFilter && (
                      <option value="none" className="bg-theme-surface">No Grouping</option>
                    )}
                    <option value="namespace" className="bg-theme-surface">By Namespace</option>
                    <option value="app" className="bg-theme-surface">By App Label</option>
                  </select>
                </div>

                {/* View mode toggle */}
                <div className="flex items-center gap-0.5 p-1 bg-theme-surface/90 backdrop-blur border border-theme-border rounded-lg">
                  <button
                    onClick={() => setTopologyMode('resources')}
                    className={`px-2.5 py-1 text-xs rounded-md transition-colors ${
                      topologyMode === 'resources'
                        ? 'bg-blue-500 text-theme-text-primary'
                        : 'text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated'
                    }`}
                  >
                    Resources
                  </button>
                  <button
                    onClick={() => setTopologyMode('traffic')}
                    className={`px-2.5 py-1 text-xs rounded-md transition-colors ${
                      topologyMode === 'traffic'
                        ? 'bg-blue-500 text-theme-text-primary'
                        : 'text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated'
                    }`}
                  >
                    Traffic
                  </button>
                </div>
              </div>
            </div>
          </>
        )}

        {/* Resources view */}
        {mainView === 'resources' && (
          <ResourcesView
            namespaces={namespaces}
            selectedResource={selectedResource}
            onResourceClick={setSelectedResource}
            onKindChange={() => setSelectedResource(null)}
          />
        )}

        {/* Timeline view */}
        {mainView === 'timeline' && !detailResource && (
          <TimelineView
            namespaces={namespaces}
            onResourceClick={setDetailResource}
            initialViewMode={(searchParams.get('view') as 'list' | 'swimlane') || undefined}
            initialFilter={(searchParams.get('filter') as 'all' | 'changes' | 'k8s_events' | 'warnings' | 'unhealthy') || undefined}
            initialTimeRange={(searchParams.get('time') as '5m' | '30m' | '1h' | '6h' | '24h' | 'all') || undefined}
          />
        )}

        {/* Resource detail page (drill-down from timeline) */}
        {mainView === 'timeline' && detailResource && (
          <ResourceDetailPage
            key={`${detailResource.kind}/${detailResource.namespace}/${detailResource.name}`}
            kind={detailResource.kind}
            namespace={detailResource.namespace}
            name={detailResource.name}
            onBack={() => setDetailResource(null)}
            onNavigateToResource={setDetailResource}
          />
        )}

        {/* Helm view - always show all namespaces since releases span multiple ns */}
        {mainView === 'helm' && (
          <HelmView
            namespace=""
            selectedRelease={selectedHelmRelease}
            onReleaseClick={(ns, name) => {
              setSelectedHelmRelease({ namespace: ns, name })
            }}
          />
        )}

        {/* Traffic view */}
        {mainView === 'traffic' && (
          <TrafficView namespaces={namespaces} />
        )}

        </ErrorBoundary>
      </div>}

      {/* Resource detail drawer (shared by topology and resources views) */}
      {selectedResource && (
        <ResourceDetailDrawer
          resource={selectedResource}
          onClose={() => setSelectedResource(null)}
          onNavigate={(res) => setSelectedResource(res)}
        />
      )}

      {/* Helm release drawer */}
      {mainView === 'helm' && selectedHelmRelease && (
        <HelmReleaseDrawer
          release={selectedHelmRelease}
          onClose={() => setSelectedHelmRelease(null)}
          onNavigateToResource={(resource) => {
            // Navigate to resources view with kind in path and open the resource detail drawer
            setSelectedHelmRelease(null)
            const newParams = new URLSearchParams()
            const globalNamespaces = searchParams.get('namespaces')
            if (globalNamespaces) newParams.set('namespaces', globalNamespaces)
            navigate({ pathname: `/resources/${resource.kind}`, search: newParams.toString() })
            setSelectedResource(resource)
          }}
        />
      )}

      {/* Port Forward Manager */}
      <PortForwardManagerWrapper />

      {/* Update notification */}
      <UpdateNotification />

      {/* Bottom Dock for Terminal/Logs */}
      <BottomDock />

      {/* Spacer for dock */}
      <DockSpacer />

      {/* Debug overlay - only in dev mode */}
      {import.meta.env.DEV && <DebugOverlay />}
    </div>
  )
}

// Spacer component that adds padding when dock is open
function DockSpacer() {
  const { tabs, isExpanded } = useDock()
  if (tabs.length === 0) return null
  return <div style={{ height: isExpanded ? 300 : 36 }} />
}

// Main App component wrapped with providers
function App() {
  return (
    <ConnectionProvider>
      <CapabilitiesProvider>
        <ContextSwitchProvider>
          <DockProvider>
            <AppInner />
          </DockProvider>
        </ContextSwitchProvider>
      </CapabilitiesProvider>
    </ConnectionProvider>
  )
}

// Skyhook logo that switches based on theme
function Logo() {
  const { theme } = useTheme()
  const logoSrc = theme === 'dark'
    ? '/assets/skyhook/logotype-white-color.svg'
    : '/assets/skyhook/logotype-dark-color.svg'

  return <img src={logoSrc} alt="Skyhook" className="h-5 w-auto" />
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
    fetch('/api/github/starred')
      .then(res => res.ok ? res.json() : null)
      .then(data => {
        if (data) {
          setStarred(data.starred)
          setGhAvailable(data.ghAvailable)
          if (data.shouldPrompt && !data.starred) {
            // Delay the callout, then re-check in case CLI prompted during the wait
            setTimeout(() => {
              fetch('/api/github/starred')
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
    fetch('/api/github/dismiss', { method: 'POST' }).catch(() => {})
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
      fetch('/api/github/star', { method: 'POST' })
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
      fetch('/api/github/dismiss', { method: 'POST' }).catch(() => {})
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
        <Star className={`w-3 h-3 ${starred ? 'text-yellow-500 fill-current' : ''}`} />
        {starCount !== null && (
          <>
            <span className="w-px h-3 bg-theme-border" />
            <span className="text-xs tabular-nums">{starCount.toLocaleString()}</span>
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

// Wrapper component that conditionally renders PortForwardManager
function PortForwardManagerWrapper() {
  const [minimized, setMinimized] = useState(false)
  const count = usePortForwardCount()

  if (count === 0) return null

  return (
    <PortForwardManager
      minimized={minimized}
      onToggleMinimize={() => setMinimized(!minimized)}
    />
  )
}

export default App
