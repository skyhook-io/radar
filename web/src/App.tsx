import { useState, useEffect, useCallback } from 'react'
import { TopologyGraph } from './components/topology/TopologyGraph'
import { EventsTray } from './components/events/EventsTray'
import { EventsView } from './components/events/EventsView'
import { ResourceDrawer } from './components/resource-drawer/ResourceDrawer'
import { ResourcesView } from './components/resources/ResourcesView'
import { ResourceDetailDrawer } from './components/resources/ResourceDetailDrawer'
import { ResourceDetailPage } from './components/resource/ResourceDetailPage'
import { useEventSource } from './hooks/useEventSource'
import { useClusterInfo, useNamespaces } from './api/client'
import { ChevronDown, RefreshCw, Layers, FolderTree, Network, List, Clock } from 'lucide-react'
import type { TopologyNode, GroupingMode, MainView, SelectedResource } from './types'

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

  return (
    <div className="flex flex-col h-screen bg-slate-900">
      {/* Header */}
      <header className="flex items-center justify-between px-4 py-2 bg-slate-800 border-b border-slate-700">
        <div className="flex items-center gap-4">
          <h1 className="text-lg font-semibold text-white flex items-center gap-2">
            <Layers className="w-5 h-5 text-indigo-400" />
            Skyhook Explorer
          </h1>

          {clusterInfo && (
            <div className="flex items-center gap-3">
              <span className="px-2 py-1 bg-slate-700 rounded text-sm font-medium text-indigo-300">
                {clusterInfo.context || clusterInfo.cluster}
              </span>
              <span className="text-sm text-slate-500">
                {clusterInfo.platform} · {clusterInfo.kubernetesVersion}
              </span>
            </div>
          )}

          {/* Main view tabs */}
          <div className="flex items-center gap-1 bg-slate-700/50 rounded-lg p-1 ml-4">
            <button
              onClick={() => setMainView('topology')}
              className={`flex items-center gap-2 px-3 py-1.5 text-sm rounded-md transition-colors ${
                mainView === 'topology'
                  ? 'bg-indigo-500 text-white'
                  : 'text-slate-400 hover:text-white hover:bg-slate-600'
              }`}
            >
              <Network className="w-4 h-4" />
              Topology
            </button>
            <button
              onClick={() => setMainView('resources')}
              className={`flex items-center gap-2 px-3 py-1.5 text-sm rounded-md transition-colors ${
                mainView === 'resources'
                  ? 'bg-indigo-500 text-white'
                  : 'text-slate-400 hover:text-white hover:bg-slate-600'
              }`}
            >
              <List className="w-4 h-4" />
              Resources
            </button>
            <button
              onClick={() => setMainView('events')}
              className={`flex items-center gap-2 px-3 py-1.5 text-sm rounded-md transition-colors ${
                mainView === 'events'
                  ? 'bg-indigo-500 text-white'
                  : 'text-slate-400 hover:text-white hover:bg-slate-600'
              }`}
            >
              <Clock className="w-4 h-4" />
              Events
            </button>
          </div>
        </div>

        <div className="flex items-center gap-4">
          {/* Topology-specific controls */}
          {mainView === 'topology' && (
            <>
              {/* Topology mode toggle */}
              <div className="flex items-center gap-1 bg-slate-700 rounded-lg p-1">
                <button
                  onClick={() => setTopologyMode('full')}
                  className={`px-3 py-1 text-sm rounded-md transition-colors ${
                    topologyMode === 'full'
                      ? 'bg-slate-600 text-white'
                      : 'text-slate-400 hover:text-white'
                  }`}
                >
                  Full
                </button>
                <button
                  onClick={() => setTopologyMode('traffic')}
                  className={`px-3 py-1 text-sm rounded-md transition-colors ${
                    topologyMode === 'traffic'
                      ? 'bg-slate-600 text-white'
                      : 'text-slate-400 hover:text-white'
                  }`}
                >
                  Traffic
                </button>
              </div>

              {/* Grouping selector */}
              <div className="flex items-center gap-2">
                <FolderTree className="w-4 h-4 text-slate-400" />
                <select
                  value={groupingMode}
                  onChange={(e) => setGroupingMode(e.target.value as GroupingMode)}
                  className="appearance-none bg-slate-700 text-white text-sm rounded-lg px-3 py-1.5 pr-8 border border-slate-600 focus:outline-none focus:ring-2 focus:ring-indigo-500"
                >
                  <option value="none">No Grouping</option>
                  <option value="namespace">By Namespace</option>
                  <option value="app">By App Label</option>
                </select>
              </div>
            </>
          )}

          {/* Namespace selector */}
          <div className="relative">
            <select
              value={namespace}
              onChange={(e) => setNamespace(e.target.value)}
              className="appearance-none bg-slate-700 text-white text-sm rounded-lg px-3 py-1.5 pr-8 border border-slate-600 focus:outline-none focus:ring-2 focus:ring-indigo-500"
            >
              <option value="">All Namespaces</option>
              {namespaces?.map((ns) => (
                <option key={ns.name} value={ns.name}>
                  {ns.name}
                </option>
              ))}
            </select>
            <ChevronDown className="absolute right-2 top-1/2 -translate-y-1/2 w-4 h-4 text-slate-400 pointer-events-none" />
          </div>

          {/* Connection status */}
          <div className="flex items-center gap-2">
            <span
              className={`w-2 h-2 rounded-full ${
                connected ? 'bg-green-500' : 'bg-red-500'
              }`}
            />
            <span className="text-sm text-slate-400">
              {connected ? 'Connected' : 'Disconnected'}
            </span>
            {!connected && (
              <button
                onClick={reconnect}
                className="p-1 text-slate-400 hover:text-white"
                title="Reconnect"
              >
                <RefreshCw className="w-4 h-4" />
              </button>
            )}
          </div>
        </div>
      </header>

      {/* Main content */}
      <div className="flex-1 flex overflow-hidden">
        {/* Topology view */}
        {mainView === 'topology' && (
          <>
            <div className="flex-1 relative">
              <TopologyGraph
                topology={topology}
                viewMode={topologyMode}
                groupingMode={groupingMode}
                onNodeClick={handleNodeClick}
                selectedNodeId={selectedNode?.id}
              />
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
