import { useState, useEffect, useCallback, useMemo } from 'react'
import { TopologyGraph } from './components/topology/TopologyGraph'
import { TopologyFilterSidebar } from './components/topology/TopologyFilterSidebar'
import { EventsTray } from './components/events/EventsTray'
import { EventsView } from './components/events/EventsView'
import { ResourceDrawer } from './components/resource-drawer/ResourceDrawer'
import { ResourcesView } from './components/resources/ResourcesView'
import { ResourceDetailDrawer } from './components/resources/ResourceDetailDrawer'
import { ResourceDetailPage } from './components/resource/ResourceDetailPage'
import { useEventSource } from './hooks/useEventSource'
import { useClusterInfo, useNamespaces } from './api/client'
import { ChevronDown, RefreshCw, Layers, FolderTree, Network, List, Clock } from 'lucide-react'
import type { TopologyNode, GroupingMode, MainView, SelectedResource, NodeKind, Topology } from './types'

// All possible node kinds for initial visibility
const ALL_NODE_KINDS: NodeKind[] = [
  'Internet', 'Ingress', 'Service', 'Deployment', 'DaemonSet', 'StatefulSet',
  'ReplicaSet', 'Pod', 'PodGroup', 'ConfigMap', 'Secret', 'HPA', 'Job', 'CronJob', 'Namespace'
]

function App() {
  // Initialize state from URL
  const getInitialState = () => {
    const params = new URLSearchParams(window.location.search)
    const ns = params.get('namespace') || ''
    return {
      namespace: ns,
      mainView: (params.get('view') as MainView) || 'topology',
      topologyMode: (params.get('mode') as 'full' | 'traffic') || 'full',
      showEvents: params.get('events') === 'true',
      // Default to namespace grouping when viewing all namespaces
      grouping: (params.get('group') as GroupingMode) || (ns === '' ? 'namespace' : 'none'),
    }
  }

  const [namespace, setNamespace] = useState<string>(getInitialState().namespace)
  const [selectedNode, setSelectedNode] = useState<TopologyNode | null>(null)
  const [selectedResource, setSelectedResource] = useState<SelectedResource | null>(null)
  const [showEvents, setShowEvents] = useState(getInitialState().showEvents)
  const [mainView, setMainView] = useState<MainView>(getInitialState().mainView)
  const [topologyMode, setTopologyMode] = useState<'full' | 'traffic'>(getInitialState().topologyMode)
  const [groupingMode, setGroupingMode] = useState<GroupingMode>(getInitialState().grouping)
  // Resource detail page state (for events view drill-down)
  const [detailResource, setDetailResource] = useState<SelectedResource | null>(null)
  // Topology filter state
  const [visibleKinds, setVisibleKinds] = useState<Set<NodeKind>>(() => new Set(ALL_NODE_KINDS))
  const [filterSidebarCollapsed, setFilterSidebarCollapsed] = useState(false)

  // Fetch cluster info and namespaces
  const { data: clusterInfo } = useClusterInfo()
  const { data: namespaces } = useNamespaces()

  // SSE connection for real-time updates
  const { topology, events, connected, reconnect } = useEventSource(namespace, topologyMode)

  // Handle node selection
  const handleNodeClick = useCallback((node: TopologyNode) => {
    setSelectedNode(node)
  }, [])

  // Close drawer
  const handleCloseDrawer = useCallback(() => {
    setSelectedNode(null)
  }, [])

  // Update URL when state changes
  useEffect(() => {
    const params = new URLSearchParams()
    if (namespace) params.set('namespace', namespace)
    if (mainView !== 'topology') params.set('view', mainView)
    if (topologyMode !== 'full') params.set('mode', topologyMode)
    if (!showEvents) params.set('events', 'false')
    if (groupingMode !== 'none' && (namespace !== '' || groupingMode !== 'namespace')) {
      params.set('group', groupingMode)
    }

    const newUrl = params.toString()
      ? `${window.location.pathname}?${params.toString()}`
      : window.location.pathname

    window.history.replaceState({}, '', newUrl)
  }, [namespace, mainView, topologyMode, showEvents, groupingMode])

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
  useEffect(() => {
    setSelectedResource(null)
    setDetailResource(null)
  }, [mainView, namespace])

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
                selectedNodeId={selectedNode?.id}
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

            {/* Events tray (only in topology view) */}
            {showEvents && (
              <EventsTray
                events={events}
                onClose={() => setShowEvents(false)}
                onEventClick={(event) => {
                  // Find and select the related node
                  const nodeId = `${event.kind.toLowerCase()}-${event.namespace}-${event.name}`
                  const node = topology?.nodes.find((n) => n.id === nodeId)
                  if (node) {
                    setSelectedNode(node)
                  }
                }}
              />
            )}
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
      </div>

      {/* Resource drawer (topology view) */}
      {selectedNode && (
        <ResourceDrawer node={selectedNode} onClose={handleCloseDrawer} />
      )}

      {/* Resource detail drawer (resources view) */}
      {mainView === 'resources' && selectedResource && (
        <ResourceDetailDrawer
          resource={selectedResource}
          onClose={() => setSelectedResource(null)}
        />
      )}

      {/* Events toggle button (when tray is closed, only in topology view) */}
      {mainView === 'topology' && !showEvents && (
        <button
          onClick={() => setShowEvents(true)}
          className="fixed bottom-4 right-4 px-4 py-2 bg-slate-700 text-white rounded-lg shadow-lg hover:bg-slate-600 transition-colors flex items-center gap-2"
        >
          Events
          {events.length > 0 && (
            <span className="bg-indigo-500 text-white text-xs px-2 py-0.5 rounded-full">
              {events.length}
            </span>
          )}
        </button>
      )}
    </div>
  )
}

export default App
