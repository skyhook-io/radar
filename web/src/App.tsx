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
import { ResourceDetailDrawer } from './components/resources/ResourceDetailDrawer'
import { ResourceDetailPage } from './components/resource/ResourceDetailPage'
import { HelmView } from './components/helm/HelmView'
import { TrafficView } from './components/traffic/TrafficView'
import { HelmReleaseDrawer } from './components/helm/HelmReleaseDrawer'
import { PortForwardManager, usePortForwardCount } from './components/portforward/PortForwardManager'
import { DockProvider, BottomDock, useDock } from './components/dock'
import { ContextSwitcher } from './components/ContextSwitcher'
import { ContextSwitchProvider, useContextSwitch } from './context/ContextSwitchContext'
import { CapabilitiesProvider } from './contexts/CapabilitiesContext'
import { ErrorBoundary } from './components/ui/ErrorBoundary'
import { useEventSource } from './hooks/useEventSource'
import { useNamespaces } from './api/client'
import { Loader2 } from 'lucide-react'
import { ChevronDown, RefreshCw, FolderTree, Network, List, Clock, Package, Sun, Moon, Activity, Home } from 'lucide-react'
import { useTheme } from './context/ThemeContext'
import type { TopologyNode, GroupingMode, MainView, SelectedResource, SelectedHelmRelease, NodeKind, Topology } from './types'

// All possible node kinds
const ALL_NODE_KINDS: NodeKind[] = [
  'Internet', 'Ingress', 'Service', 'Deployment', 'DaemonSet', 'StatefulSet',
  'ReplicaSet', 'Pod', 'PodGroup', 'ConfigMap', 'Secret', 'HPA', 'Job', 'CronJob', 'PVC', 'Namespace'
]

// Default visible kinds (ReplicaSet hidden by default - noisy intermediate object)
const DEFAULT_VISIBLE_KINDS: NodeKind[] = [
  'Internet', 'Ingress', 'Service', 'Deployment', 'Rollout', 'DaemonSet', 'StatefulSet',
  'Pod', 'PodGroup', 'ConfigMap', 'Secret', 'HPA', 'Job', 'CronJob', 'PVC', 'Namespace'
]

// Convert node kind to plural API resource name
function kindToApiResource(kind: NodeKind): string {
  const kindMap: Record<string, string> = {
    'Pod': 'pods',
    'PodGroup': 'pods', // PodGroup represents multiple pods
    'Service': 'services',
    'Deployment': 'deployments',
    'Rollout': 'rollouts',
    'DaemonSet': 'daemonsets',
    'StatefulSet': 'statefulsets',
    'ReplicaSet': 'replicasets',
    'Ingress': 'ingresses',
    'ConfigMap': 'configmaps',
    'Secret': 'secrets',
    'HPA': 'hpas',
    'Job': 'jobs',
    'CronJob': 'cronjobs',
    'PVC': 'persistentvolumeclaims',
    'Namespace': 'namespaces',
  }
  return kindMap[kind] || kind.toLowerCase() + 's'
}

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
    'configmaps': 'configmap',
    'secrets': 'secret',
    'hpas': 'hpa',
    'jobs': 'job',
    'cronjobs': 'cronjob',
    'persistentvolumeclaims': 'pvc',
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

  // Initialize state from URL
  const getInitialState = () => {
    const ns = searchParams.get('namespace') || ''
    const resource = parseResourceParam(searchParams.get('resource'))
    return {
      namespace: ns,
      topologyMode: (searchParams.get('mode') as 'resources' | 'traffic') || 'resources',
      // Default to namespace grouping when viewing all namespaces
      grouping: (searchParams.get('group') as GroupingMode) || (ns === '' ? 'namespace' : 'none'),
      detailResource: resource,
    }
  }

  // Get mainView from URL path
  const mainView = getViewFromPath(location.pathname)

  // Set mainView by navigating to the path
  const setMainView = useCallback((view: ExtendedMainView, params?: Record<string, string>) => {
    const path = view === 'home' ? '/' : `/${view}`

    // Clean up view-specific params
    const newParams = new URLSearchParams(searchParams)

    // Remove topology-only params when leaving topology
    if (view !== 'topology') {
      newParams.delete('mode')
      newParams.delete('group')
    }

    // Remove timeline-only params when leaving timeline
    if (view !== 'timeline') {
      newParams.delete('resource')
      newParams.delete('view')
      newParams.delete('filter')
    }

    // Add any new params
    if (params) {
      for (const [key, value] of Object.entries(params)) {
        newParams.set(key, value)
      }
    }

    navigate({ pathname: path, search: newParams.toString() })
  }, [navigate, searchParams])

  const [namespace, setNamespace] = useState<string>(getInitialState().namespace)
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
  // - Single namespace with 'none': use 'namespace' internally but hide header
  const isSingleNamespace = namespace !== ''
  const effectiveGroupingMode: GroupingMode = useMemo(() => {
    if (!isSingleNamespace && groupingMode === 'none') {
      // All namespaces view - force namespace grouping
      return 'namespace'
    }
    if (isSingleNamespace && groupingMode === 'none') {
      // Single namespace with "no grouping" - use namespace grouping for layout
      return 'namespace'
    }
    return groupingMode
  }, [isSingleNamespace, groupingMode])

  // Hide group header when viewing single namespace with "no grouping" selected
  const hideGroupHeader = isSingleNamespace && groupingMode === 'none'

  // Fetch cluster info and namespaces
  const { data: namespaces } = useNamespaces()

  // Context switch state
  const { isSwitching, targetContext, progressMessage, updateProgress, endSwitch } = useContextSwitch()

  // Query client for cache invalidation
  const queryClient = useQueryClient()

  // SSE connection for real-time updates
  const { topology, connected, reconnect: reconnectSSE } = useEventSource(namespace, topologyMode, {
    onContextSwitchComplete: endSwitch,
    onContextSwitchProgress: updateProgress,
    onContextChanged: () => {
      // Clear all React Query caches when cluster context changes
      // This ensures helm releases, resources, etc. are refetched from the new cluster
      // removeQueries clears cached data, invalidateQueries triggers refetch
      queryClient.removeQueries()
      queryClient.invalidateQueries()
    },
  })
  const [reconnect, isReconnecting] = useRefreshAnimation(reconnectSSE)

  // Handle node selection - convert TopologyNode to SelectedResource for the drawer
  const handleNodeClick = useCallback((node: TopologyNode) => {
    // Skip Internet node - it's not a real resource
    if (node.kind === 'Internet') return

    // For PodGroup, we can't open a single resource drawer
    // TODO: Could show a list of pods in the group
    if (node.kind === 'PodGroup') return

    setSelectedResource({
      kind: kindToApiResource(node.kind),
      namespace: (node.data.namespace as string) || '',
      name: node.name,
    })
  }, [])

  // Update URL query params when state changes (path is handled by setMainView)
  useEffect(() => {
    const params = new URLSearchParams(searchParams)

    // Update namespace param
    if (namespace) {
      params.set('namespace', namespace)
    } else {
      params.delete('namespace')
    }

    // Update mode param
    if (topologyMode !== 'resources') {
      params.set('mode', topologyMode)
    } else {
      params.delete('mode')
    }

    // Update group param
    if (groupingMode !== 'none' && (namespace !== '' || groupingMode !== 'namespace')) {
      params.set('group', groupingMode)
    } else {
      params.delete('group')
    }

    // Note: resource param for timeline detail view is handled by setDetailResource wrapper
    // to ensure proper history push/pop behavior

    // Only update if params changed
    if (params.toString() !== searchParams.toString()) {
      setSearchParams(params, { replace: true })
    }
  }, [namespace, topologyMode, groupingMode, mainView, searchParams, setSearchParams])

  // Sync state from URL when navigating (back/forward)
  useEffect(() => {
    const ns = searchParams.get('namespace') || ''
    const resource = parseResourceParam(searchParams.get('resource'))

    if (ns !== namespace) setNamespace(ns)
    // Use raw setter to avoid triggering navigate() again
    if (JSON.stringify(resource) !== JSON.stringify(detailResource)) setDetailResourceState(resource)
  }, [searchParams])

  // Auto-adjust grouping when namespace changes
  useEffect(() => {
    if (namespace === '' && groupingMode === 'none') {
      // Switching to all namespaces - enable namespace grouping by default
      setGroupingMode('namespace')
    } else if (namespace !== '' && groupingMode === 'namespace') {
      // Switching to specific namespace - disable namespace grouping
      setGroupingMode('none')
    }
  }, [namespace])

  // Clear resource selection when changing views or namespace
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
    // Only clear detailResource when leaving timeline view or changing namespace
    if (mainView !== 'timeline') {
      setDetailResourceState(null)
    }
  }, [mainView])

  // Clear detail resource when namespace changes (separate effect)
  useEffect(() => {
    setDetailResourceState(null)
    setSelectedResource(null)
    setSelectedHelmRelease(null)
  }, [namespace])

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
    setVisibleKinds(new Set(ALL_NODE_KINDS))
  }, [])

  const handleHideAllKinds = useCallback(() => {
    setVisibleKinds(new Set())
  }, [])

  return (
    <div className="flex flex-col h-screen bg-theme-base">
      {/* Header */}
      <header className="relative flex items-center justify-between px-4 py-2 bg-theme-surface border-b border-theme-border">
        {/* Left: Logo + Cluster info */}
        <div className="flex items-center gap-4">
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
              <span className="text-xs text-theme-text-tertiary hidden sm:inline">
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

        {/* Center: View tabs (absolutely centered) */}
        <div className="absolute left-1/2 -translate-x-1/2 flex items-center gap-1 bg-theme-elevated/50 rounded-lg p-1">
          <button
            onClick={() => setMainView('home')}
            className={`flex items-center gap-1.5 px-2.5 py-1.5 text-sm rounded-md transition-colors ${
              mainView === 'home'
                ? 'bg-blue-500 text-theme-text-primary'
                : 'text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-hover'
            }`}
          >
            <Home className="w-4 h-4" />
            <span className="hidden sm:inline">Home</span>
          </button>
          <button
            onClick={() => setMainView('topology')}
            className={`flex items-center gap-1.5 px-2.5 py-1.5 text-sm rounded-md transition-colors ${
              mainView === 'topology'
                ? 'bg-blue-500 text-theme-text-primary'
                : 'text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-hover'
            }`}
          >
            <Network className="w-4 h-4" />
            <span className="hidden sm:inline">Topology</span>
          </button>
          <button
            onClick={() => setMainView('resources')}
            className={`flex items-center gap-1.5 px-2.5 py-1.5 text-sm rounded-md transition-colors ${
              mainView === 'resources'
                ? 'bg-blue-500 text-theme-text-primary'
                : 'text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-hover'
            }`}
          >
            <List className="w-4 h-4" />
            <span className="hidden sm:inline">Resources</span>
          </button>
          <button
            onClick={() => setMainView('timeline')}
            className={`flex items-center gap-1.5 px-2.5 py-1.5 text-sm rounded-md transition-colors ${
              mainView === 'timeline'
                ? 'bg-blue-500 text-theme-text-primary'
                : 'text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-hover'
            }`}
          >
            <Clock className="w-4 h-4" />
            <span className="hidden sm:inline">Timeline</span>
          </button>
          <button
            onClick={() => setMainView('helm')}
            className={`flex items-center gap-1.5 px-2.5 py-1.5 text-sm rounded-md transition-colors ${
              mainView === 'helm'
                ? 'bg-blue-500 text-theme-text-primary'
                : 'text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-hover'
            }`}
          >
            <Package className="w-4 h-4" />
            <span className="hidden sm:inline">Helm</span>
          </button>
          <button
            onClick={() => setMainView('traffic')}
            className={`flex items-center gap-1.5 px-2.5 py-1.5 text-sm rounded-md transition-colors ${
              mainView === 'traffic'
                ? 'bg-blue-500 text-theme-text-primary'
                : 'text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-hover'
            }`}
          >
            <Activity className="w-4 h-4" />
            <span className="hidden sm:inline">Traffic</span>
          </button>
        </div>

        {/* Right: Controls */}
        <div className="flex items-center gap-3">
          {/* Namespace selector - compact */}
          <div className="relative">
            <select
              value={namespace}
              onChange={(e) => setNamespace(e.target.value)}
              className="appearance-none bg-theme-elevated text-theme-text-primary text-xs rounded px-2 py-1 pr-6 border border-theme-border-light focus:outline-none focus:ring-1 focus:ring-blue-500 min-w-[100px]"
            >
              <option value="">All Namespaces</option>
              {namespaces?.slice().sort((a, b) => a.name.localeCompare(b.name)).map((ns) => (
                <option key={ns.name} value={ns.name}>
                  {ns.name}
                </option>
              ))}
            </select>
            <ChevronDown className="absolute right-1.5 top-1/2 -translate-y-1/2 w-3 h-3 text-theme-text-secondary pointer-events-none" />
          </div>

          {/* Theme toggle */}
          <ThemeToggle />
        </div>
      </header>

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

      {/* Main content */}
      {!isSwitching && <div className="flex-1 flex overflow-hidden">
        <ErrorBoundary>
        {/* Home dashboard */}
        {mainView === 'home' && (
          <HomeView
            namespace={namespace}
            topology={topology}
            onNavigateToView={setMainView}
            onNavigateToResourceKind={(kind, apiGroup) => {
              // Navigate to resources view with kind pre-selected via URL param
              const newParams = new URLSearchParams(searchParams)
              newParams.set('kind', kind)
              newParams.delete('mode')
              newParams.delete('resource')
              if (apiGroup) {
                newParams.set('apiGroup', apiGroup)
              } else {
                newParams.delete('apiGroup')
              }
              navigate({ pathname: '/resources', search: newParams.toString() })
            }}
            onNavigateToResource={(resource) => {
              // Switch to resources view and open the resource detail drawer
              setSelectedResource(resource)
              const newParams = new URLSearchParams(searchParams)
              newParams.set('kind', resource.kind)
              newParams.delete('mode')
              newParams.delete('group')
              newParams.delete('resource')
              navigate({ pathname: '/resources', search: newParams.toString() })
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
                    {isSingleNamespace && (
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
            namespace={namespace}
            selectedResource={selectedResource}
            onResourceClick={(kind, ns, name, group) => {
              setSelectedResource({ kind, namespace: ns, name, group })
            }}
            onKindChange={() => setSelectedResource(null)}
          />
        )}

        {/* Timeline view */}
        {mainView === 'timeline' && !detailResource && (
          <TimelineView
            namespace={namespace}
            onResourceClick={(kind, ns, name) => setDetailResource({ kind, namespace: ns, name })}
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
            onNavigateToResource={(kind, ns, name) => setDetailResource({ kind, namespace: ns, name })}
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
          <TrafficView namespace={namespace} />
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
          onNavigateToResource={(kind, ns, name) => {
            // Navigate to resources view and select the resource
            setSelectedHelmRelease(null)
            setMainView('resources')
            setSelectedResource({ kind, namespace: ns, name })
          }}
        />
      )}

      {/* Port Forward Manager */}
      <PortForwardManagerWrapper />

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
    <CapabilitiesProvider>
      <ContextSwitchProvider>
        <DockProvider>
          <AppInner />
        </DockProvider>
      </ContextSwitchProvider>
    </CapabilitiesProvider>
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
