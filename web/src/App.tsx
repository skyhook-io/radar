import { useState, useEffect, useCallback, useMemo, useRef } from 'react'
import { TopologyGraph } from './components/topology/TopologyGraph'
import { TopologyFilterSidebar } from './components/topology/TopologyFilterSidebar'
import { EventsView } from './components/events/EventsView'
import { ResourcesView } from './components/resources/ResourcesView'
import { ResourceDetailDrawer } from './components/resources/ResourceDetailDrawer'
import { ResourceDetailPage } from './components/resource/ResourceDetailPage'
import { HelmView } from './components/helm/HelmView'
import { HelmReleaseDrawer } from './components/helm/HelmReleaseDrawer'
import { useEventSource } from './hooks/useEventSource'
import { useClusterInfo, useNamespaces } from './api/client'
import { ChevronDown, RefreshCw, Layers, FolderTree, Network, List, Clock, Package } from 'lucide-react'
import type { TopologyNode, GroupingMode, MainView, SelectedResource, SelectedHelmRelease, NodeKind, Topology } from './types'

// All possible node kinds
const ALL_NODE_KINDS: NodeKind[] = [
  'Internet', 'Ingress', 'Service', 'Deployment', 'DaemonSet', 'StatefulSet',
  'ReplicaSet', 'Pod', 'PodGroup', 'ConfigMap', 'Secret', 'HPA', 'Job', 'CronJob', 'PVC', 'Namespace'
]

// Default visible kinds (ReplicaSet hidden by default - noisy intermediate object)
const DEFAULT_VISIBLE_KINDS: NodeKind[] = [
  'Internet', 'Ingress', 'Service', 'Deployment', 'DaemonSet', 'StatefulSet',
  'Pod', 'PodGroup', 'ConfigMap', 'Secret', 'HPA', 'Job', 'CronJob', 'PVC', 'Namespace'
]

// Convert node kind to plural API resource name
function kindToApiResource(kind: NodeKind): string {
  const kindMap: Record<string, string> = {
    'Pod': 'pods',
    'PodGroup': 'pods', // PodGroup represents multiple pods
    'Service': 'services',
    'Deployment': 'deployments',
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

function App() {
  // Initialize state from URL
  const getInitialState = () => {
    const params = new URLSearchParams(window.location.search)
    const ns = params.get('namespace') || ''
    const resource = parseResourceParam(params.get('resource'))
    return {
      namespace: ns,
      mainView: (params.get('view') as MainView) || 'topology',
      topologyMode: (params.get('mode') as 'full' | 'traffic') || 'full',
      // Default to namespace grouping when viewing all namespaces
      grouping: (params.get('group') as GroupingMode) || (ns === '' ? 'namespace' : 'none'),
      detailResource: resource,
    }
  }

  const [namespace, setNamespace] = useState<string>(getInitialState().namespace)
  const [selectedResource, setSelectedResource] = useState<SelectedResource | null>(null)
  const [selectedHelmRelease, setSelectedHelmRelease] = useState<SelectedHelmRelease | null>(null)
  const [mainView, setMainView] = useState<MainView>(getInitialState().mainView)
  const [topologyMode, setTopologyMode] = useState<'full' | 'traffic'>(getInitialState().topologyMode)
  const [groupingMode, setGroupingMode] = useState<GroupingMode>(getInitialState().grouping)
  // Resource detail page state (for events view drill-down)
  const [detailResource, setDetailResource] = useState<SelectedResource | null>(getInitialState().detailResource)
  // Topology filter state
  const [visibleKinds, setVisibleKinds] = useState<Set<NodeKind>>(() => new Set(DEFAULT_VISIBLE_KINDS))
  const [filterSidebarCollapsed, setFilterSidebarCollapsed] = useState(false)

  // Fetch cluster info and namespaces
  const { data: clusterInfo } = useClusterInfo()
  const { data: namespaces } = useNamespaces()

  // SSE connection for real-time updates
  const { topology, connected, reconnect } = useEventSource(namespace, topologyMode)

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

  // Update URL when state changes
  useEffect(() => {
    const params = new URLSearchParams()
    if (namespace) params.set('namespace', namespace)
    if (mainView !== 'topology') params.set('view', mainView)
    if (topologyMode !== 'full') params.set('mode', topologyMode)
    if (groupingMode !== 'none' && (namespace !== '' || groupingMode !== 'namespace')) {
      params.set('group', groupingMode)
    }
    // Add resource param for events detail view
    if (mainView === 'events' && detailResource) {
      params.set('resource', encodeResourceParam(detailResource))
    }

    const newUrl = params.toString()
      ? `${window.location.pathname}?${params.toString()}`
      : window.location.pathname

    // Use pushState for resource navigation (allows back button), replaceState for other changes
    const currentUrl = window.location.pathname + window.location.search
    if (newUrl !== currentUrl) {
      // Check if only the resource changed (for back/forward support)
      const currentParams = new URLSearchParams(window.location.search)
      const currentResource = currentParams.get('resource')
      const newResource = params.get('resource')
      const isResourceChange = currentResource !== newResource &&
        currentParams.get('view') === params.get('view')

      if (isResourceChange) {
        window.history.pushState({ resource: newResource }, '', newUrl)
      } else {
        window.history.replaceState({}, '', newUrl)
      }
    }
  }, [namespace, mainView, topologyMode, groupingMode, detailResource])

  // Handle browser back/forward navigation
  useEffect(() => {
    const handlePopState = () => {
      const params = new URLSearchParams(window.location.search)
      const resource = parseResourceParam(params.get('resource'))
      const view = (params.get('view') as MainView) || 'topology'

      // Update state from URL
      setMainView(view)
      setDetailResource(resource)
      setNamespace(params.get('namespace') || '')
    }

    window.addEventListener('popstate', handlePopState)
    return () => window.removeEventListener('popstate', handlePopState)
  }, [])

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
  // And don't clear detailResource if we're navigating to events view (could be from URL)
  const prevMainView = useRef(mainView)
  useEffect(() => {
    const navigatingToResources = mainView === 'resources' && prevMainView.current !== 'resources'
    prevMainView.current = mainView

    // Don't clear selectedResource when navigating TO resources view (deep link from Helm)
    if (!navigatingToResources) {
      setSelectedResource(null)
    }
    setSelectedHelmRelease(null)
    // Only clear detailResource when leaving events view or changing namespace
    if (mainView !== 'events') {
      setDetailResource(null)
    }
  }, [mainView])

  // Clear detail resource when namespace changes (separate effect)
  useEffect(() => {
    setDetailResource(null)
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
    <div className="flex flex-col h-screen bg-slate-900">
      {/* Header */}
      <header className="flex items-center justify-between px-4 py-2 bg-slate-800 border-b border-slate-700">
        {/* Left: Logo + Cluster info */}
        <div className="flex items-center gap-4">
          <h1 className="text-lg font-semibold text-white flex items-center gap-2">
            <Layers className="w-5 h-5 text-indigo-400" />
            <span className="hidden sm:inline">Skyhook Explorer</span>
          </h1>

          {clusterInfo && (
            <div className="flex items-center gap-2">
              <span className="px-2 py-1 bg-slate-700 rounded text-sm font-medium text-indigo-300" title={clusterInfo.context || clusterInfo.cluster}>
                {clusterInfo.context || clusterInfo.cluster}
              </span>
              <span className="text-xs text-slate-500 hidden lg:inline">
                {clusterInfo.platform} · {clusterInfo.kubernetesVersion}
              </span>
              {/* Connection status - next to cluster name */}
              <div className="flex items-center gap-1.5 ml-1">
                <span
                  className={`w-2 h-2 rounded-full ${
                    connected ? 'bg-green-500' : 'bg-red-500'
                  }`}
                />
                <span className="text-xs text-slate-500 hidden sm:inline">
                  {connected ? 'Connected' : 'Disconnected'}
                </span>
                {!connected && (
                  <button
                    onClick={reconnect}
                    className="p-1 text-slate-400 hover:text-white"
                    title="Reconnect"
                  >
                    <RefreshCw className="w-3 h-3" />
                  </button>
                )}
              </div>
            </div>
          )}
        </div>

        {/* Center: View tabs */}
        <div className="flex items-center gap-1 bg-slate-700/50 rounded-lg p-1">
          <button
            onClick={() => setMainView('topology')}
            className={`flex items-center gap-1.5 px-2.5 py-1.5 text-sm rounded-md transition-colors ${
              mainView === 'topology'
                ? 'bg-indigo-500 text-white'
                : 'text-slate-400 hover:text-white hover:bg-slate-600'
            }`}
          >
            <Network className="w-4 h-4" />
            <span className="hidden sm:inline">Topology</span>
          </button>
          <button
            onClick={() => setMainView('resources')}
            className={`flex items-center gap-1.5 px-2.5 py-1.5 text-sm rounded-md transition-colors ${
              mainView === 'resources'
                ? 'bg-indigo-500 text-white'
                : 'text-slate-400 hover:text-white hover:bg-slate-600'
            }`}
          >
            <List className="w-4 h-4" />
            <span className="hidden sm:inline">Resources</span>
          </button>
          <button
            onClick={() => setMainView('events')}
            className={`flex items-center gap-1.5 px-2.5 py-1.5 text-sm rounded-md transition-colors ${
              mainView === 'events'
                ? 'bg-indigo-500 text-white'
                : 'text-slate-400 hover:text-white hover:bg-slate-600'
            }`}
          >
            <Clock className="w-4 h-4" />
            <span className="hidden sm:inline">Events</span>
          </button>
          <button
            onClick={() => setMainView('helm')}
            className={`flex items-center gap-1.5 px-2.5 py-1.5 text-sm rounded-md transition-colors ${
              mainView === 'helm'
                ? 'bg-indigo-500 text-white'
                : 'text-slate-400 hover:text-white hover:bg-slate-600'
            }`}
          >
            <Package className="w-4 h-4" />
            <span className="hidden sm:inline">Helm</span>
          </button>
        </div>

        {/* Right: Controls */}
        <div className="flex items-center gap-3">
          {/* Namespace selector - compact */}
          <div className="relative">
            <select
              value={namespace}
              onChange={(e) => setNamespace(e.target.value)}
              className="appearance-none bg-slate-700 text-white text-xs rounded px-2 py-1 pr-6 border border-slate-600 focus:outline-none focus:ring-1 focus:ring-indigo-500 min-w-[100px]"
            >
              <option value="">All Namespaces</option>
              {namespaces?.map((ns) => (
                <option key={ns.name} value={ns.name}>
                  {ns.name}
                </option>
              ))}
            </select>
            <ChevronDown className="absolute right-1.5 top-1/2 -translate-y-1/2 w-3 h-3 text-slate-400 pointer-events-none" />
          </div>
        </div>
      </header>

      {/* Main content */}
      <div className="flex-1 flex overflow-hidden">
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
            />

            <div className="flex-1 relative">
              <TopologyGraph
                topology={filteredTopology}
                viewMode={topologyMode}
                groupingMode={groupingMode}
                onNodeClick={handleNodeClick}
                selectedNodeId={selectedResource ? `${apiResourceToNodeIdPrefix(selectedResource.kind)}-${selectedResource.namespace}-${selectedResource.name}` : undefined}
              />

              {/* Topology controls overlay - top right */}
              <div className="absolute top-4 right-4 z-10 flex items-center gap-2">
                {/* Grouping selector */}
                <div className="flex items-center gap-1.5 px-2 py-1.5 bg-slate-800/90 backdrop-blur border border-slate-700 rounded-lg">
                  <FolderTree className="w-3.5 h-3.5 text-slate-400" />
                  <select
                    value={groupingMode}
                    onChange={(e) => setGroupingMode(e.target.value as GroupingMode)}
                    className="appearance-none bg-transparent text-white text-xs focus:outline-none cursor-pointer"
                  >
                    <option value="none" className="bg-slate-800">No Grouping</option>
                    <option value="namespace" className="bg-slate-800">By Namespace</option>
                    <option value="app" className="bg-slate-800">By App Label</option>
                  </select>
                </div>

                {/* View mode toggle */}
                <div className="flex items-center gap-0.5 p-1 bg-slate-800/90 backdrop-blur border border-slate-700 rounded-lg">
                  <button
                    onClick={() => setTopologyMode('full')}
                    className={`px-2.5 py-1 text-xs rounded-md transition-colors ${
                      topologyMode === 'full'
                        ? 'bg-indigo-500 text-white'
                        : 'text-slate-400 hover:text-white hover:bg-slate-700'
                    }`}
                  >
                    Full
                  </button>
                  <button
                    onClick={() => setTopologyMode('traffic')}
                    className={`px-2.5 py-1 text-xs rounded-md transition-colors ${
                      topologyMode === 'traffic'
                        ? 'bg-indigo-500 text-white'
                        : 'text-slate-400 hover:text-white hover:bg-slate-700'
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
            onResourceClick={(kind, ns, name) => {
              setSelectedResource({ kind, namespace: ns, name })
            }}
          />
        )}

        {/* Events view */}
        {mainView === 'events' && !detailResource && (
          <EventsView
            namespace={namespace}
            onResourceClick={(kind, ns, name) => setDetailResource({ kind, namespace: ns, name })}
          />
        )}

        {/* Resource detail page (drill-down from events) */}
        {mainView === 'events' && detailResource && (
          <ResourceDetailPage
            kind={detailResource.kind}
            namespace={detailResource.namespace}
            name={detailResource.name}
            onBack={() => setDetailResource(null)}
            onNavigateToResource={(kind, ns, name) => setDetailResource({ kind, namespace: ns, name })}
          />
        )}

        {/* Helm view */}
        {mainView === 'helm' && (
          <HelmView
            namespace={namespace}
            selectedRelease={selectedHelmRelease}
            onReleaseClick={(ns, name) => {
              setSelectedHelmRelease({ namespace: ns, name })
            }}
          />
        )}
      </div>

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
    </div>
  )
}

export default App
