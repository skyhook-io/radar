import { useState, useCallback, useEffect, useRef, useMemo } from 'react'
import { Search, X, ChevronRight } from 'lucide-react'
import { Input } from '../ui/Input'
import { getTopologyIcon } from '../../utils/resource-icons'
import { clsx } from 'clsx'
import type { TopologyNode } from '../../types'
import { useRegisterShortcut } from '../../hooks/useKeyboardShortcuts'

interface TopologySearchProps {
  nodes: TopologyNode[]
  onNodeSelect?: (node: TopologyNode) => void
  onZoomToNode?: (nodeId: string) => void
  /**
   * Optional unfiltered node set. When the parent has applied a
   * view-mode filter (e.g. Fleet mode hides Pods/Deployments and
   * only shows CAPI kinds), it should still pass the full topology
   * here so the empty-state can tell users "your query matches X
   * resources hidden by the current view" rather than the
   * misleading "No resources found."
   */
  allNodes?: TopologyNode[]
  /**
   * Human-readable label for the current view mode. Used in the
   * empty-state hint above. Defaults to "current view".
   */
  viewModeLabel?: string
  /**
   * Position utilities for the trigger button (default "top-4 left-4").
   * The host overrides this to avoid colliding with other canvas overlays
   * (e.g. stacking below the namespace breadcrumb).
   */
  triggerClassName?: string
}

// Icon mapping for different resource kinds
function getNodeIcon(kind: string) {
  return getTopologyIcon(kind)
}

// Color mapping for different resource kinds
function getKindColor(kind: string): string {
  switch (kind) {
    case 'Ingress':
    case 'Gateway':
      return 'text-purple-400'
    case 'HTTPRoute':
    case 'GRPCRoute':
    case 'TCPRoute':
    case 'TLSRoute':
      return 'text-purple-300'
    case 'Service':
      return 'text-blue-400'
    case 'Deployment':
    case 'Rollout':
      return 'text-emerald-400'
    case 'DaemonSet':
      return 'text-teal-400'
    case 'StatefulSet':
      return 'text-cyan-400'
    case 'ReplicaSet':
      return 'text-green-400'
    case 'Pod':
    case 'PodGroup':
      return 'text-lime-400'
    case 'ConfigMap':
      return 'text-amber-400'
    case 'Secret':
      return 'text-red-400'
    default:
      return 'text-theme-text-secondary'
  }
}

export function TopologySearch({ nodes, onNodeSelect, onZoomToNode, allNodes, viewModeLabel, triggerClassName }: TopologySearchProps) {
  const [isOpen, setIsOpen] = useState(false)
  const [query, setQuery] = useState('')
  const [selectedIndex, setSelectedIndex] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)
  const resultsRef = useRef<HTMLDivElement>(null)

  const matchesQuery = useCallback((node: TopologyNode, lowerQuery: string) => {
    const name = node.name.toLowerCase()
    const kind = node.kind.toLowerCase()
    const namespace = (node.data.namespace as string || '').toLowerCase()
    return (
      name.includes(lowerQuery) ||
      kind.includes(lowerQuery) ||
      namespace.includes(lowerQuery) ||
      `${kind}/${name}`.includes(lowerQuery) ||
      `${namespace}/${name}`.includes(lowerQuery)
    )
  }, [])

  // Filter nodes based on query
  const filteredNodes = useMemo(() => {
    if (!query.trim()) return []
    const lowerQuery = query.toLowerCase()
    return nodes
      .filter(node => matchesQuery(node, lowerQuery))
      .slice(0, 10) // Limit results
  }, [nodes, query, matchesQuery])

  // Count of matches in the unfiltered topology that the current
  // view-mode filter is hiding. We only compute this when there's
  // a query AND the visible set returned zero results, so the cost
  // is bounded to actual no-result interactions. (Fleet view hides
  // Pods, so searching a pod name there would otherwise just say
  // "No resources found".)
  const hiddenMatchCount = useMemo(() => {
    if (!query.trim() || filteredNodes.length > 0 || !allNodes) return 0
    const lowerQuery = query.toLowerCase()
    const visibleIds = new Set(nodes.map(n => n.id))
    return allNodes.filter(n => !visibleIds.has(n.id) && matchesQuery(n, lowerQuery)).length
  }, [query, filteredNodes.length, allNodes, nodes, matchesQuery])

  // Reset selection when results change
  useEffect(() => {
    setSelectedIndex(0)
  }, [filteredNodes])

  // Scroll selected item into view
  useEffect(() => {
    if (resultsRef.current && filteredNodes.length > 0) {
      const selectedElement = resultsRef.current.children[selectedIndex] as HTMLElement
      if (selectedElement) {
        selectedElement.scrollIntoView({ block: 'nearest' })
      }
    }
  }, [selectedIndex, filteredNodes.length])

  // Keyboard shortcuts for topology search
  useRegisterShortcut({
    id: 'topology-search',
    keys: '/',
    description: 'Search topology',
    category: 'Search',
    scope: 'topology',
    handler: () => {
      setIsOpen(true)
      setTimeout(() => inputRef.current?.focus(), 0)
    },
  })
  useRegisterShortcut({
    id: 'topology-search-close',
    keys: 'Escape',
    description: 'Close search',
    category: 'Topology',
    scope: 'topology',
    handler: () => { if (isOpen) handleClose() },
    enabled: isOpen,
  })

  // Navigate to node and zoom
  const navigateToNode = useCallback((node: TopologyNode) => {
    // Zoom to the node
    onZoomToNode?.(node.id)

    // Notify parent
    onNodeSelect?.(node)

    // Close search
    handleClose()
  }, [onZoomToNode, onNodeSelect])

  // Handle keyboard navigation in results
  const handleInputKeyDown = useCallback((e: React.KeyboardEvent) => {
    if (!filteredNodes.length) return

    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault()
        setSelectedIndex(i => Math.min(i + 1, filteredNodes.length - 1))
        break
      case 'ArrowUp':
        e.preventDefault()
        setSelectedIndex(i => Math.max(i - 1, 0))
        break
      case 'Enter':
        e.preventDefault()
        if (filteredNodes[selectedIndex]) {
          navigateToNode(filteredNodes[selectedIndex])
        }
        break
    }
  }, [filteredNodes, selectedIndex, navigateToNode])

  const handleClose = useCallback(() => {
    setIsOpen(false)
    setQuery('')
    setSelectedIndex(0)
  }, [])

  const handleOpen = useCallback(() => {
    setIsOpen(true)
    setTimeout(() => inputRef.current?.focus(), 0)
  }, [])

  return (
    <>
      {/* Search trigger button */}
      <button
        onClick={handleOpen}
        className={clsx(
          'absolute z-10 flex items-center gap-2 px-3 py-2 bg-theme-surface/90 backdrop-blur border border-theme-border rounded-lg text-theme-text-secondary hover:text-theme-text-primary hover:border-theme-border-light transition-colors',
          triggerClassName ?? 'top-4 left-4'
        )}
      >
        <Search className="w-4 h-4" />
        <span className="text-sm">Search</span>
        <kbd className="hidden sm:inline-flex items-center px-1.5 py-0.5 text-xs bg-theme-elevated rounded border border-theme-border-light">
          /
        </kbd>
      </button>

      {/* Search modal */}
      {isOpen && (
        <div className="absolute inset-0 z-50 flex items-start justify-center pt-[10vh]">
          {/* Backdrop */}
          <div
            className="absolute inset-0 bg-theme-base/60 backdrop-blur-sm"
            onClick={handleClose}
          />

          {/* Search panel */}
          <div className="relative w-full max-w-lg mx-4 bg-theme-surface border border-theme-border rounded-xl shadow-2xl overflow-hidden">
            {/* Search input */}
            <div className="flex items-center gap-3 px-4 py-3 border-b border-theme-border">
              <Search className="w-5 h-5 text-theme-text-secondary" />
              <Input
                ref={inputRef}
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                onKeyDown={handleInputKeyDown}
                placeholder="Search resources by name, kind, or namespace..."
                className="flex-1 bg-transparent text-theme-text-primary placeholder-theme-text-disabled outline-none text-sm"
                autoFocus
              />
              {query && (
                <button
                  onClick={() => setQuery('')}
                  className="p-1 text-theme-text-secondary hover:text-theme-text-primary"
                >
                  <X className="w-4 h-4" />
                </button>
              )}
              <kbd className="px-1.5 py-0.5 text-xs text-theme-text-tertiary bg-theme-elevated rounded border border-theme-border-light">
                ESC
              </kbd>
            </div>

            {/* Results */}
            <div ref={resultsRef} className="max-h-[60vh] overflow-y-auto">
              {query && filteredNodes.length === 0 && (
                <div className="px-4 py-8 text-center text-theme-text-tertiary">
                  <Search className="w-8 h-8 mx-auto mb-2 opacity-50" />
                  <p>No resources found for "{query}"{viewModeLabel ? ` in ${viewModeLabel}` : ''}</p>
                  {hiddenMatchCount > 0 && (
                    <p className="mt-2 text-xs text-amber-400">
                      {hiddenMatchCount} {hiddenMatchCount === 1 ? 'match is' : 'matches are'} hidden by the current view{viewModeLabel ? ` (${viewModeLabel})` : ''}. Switch view to see them.
                    </p>
                  )}
                </div>
              )}

              {filteredNodes.map((node, index) => {
                const Icon = getNodeIcon(node.kind)
                const isSelected = index === selectedIndex
                const namespace = node.data.namespace as string || ''

                return (
                  <button
                    key={node.id}
                    onClick={() => navigateToNode(node)}
                    onMouseEnter={() => setSelectedIndex(index)}
                    className={clsx(
                      'w-full flex items-center gap-3 px-4 py-3 text-left transition-colors',
                      isSelected ? 'bg-theme-elevated/50' : 'hover:bg-theme-elevated/30'
                    )}
                  >
                    <div className={clsx(
                      'flex items-center justify-center w-8 h-8 rounded-lg',
                      isSelected ? 'bg-blue-500/20' : 'bg-theme-elevated/50'
                    )}>
                      <Icon className={clsx('w-4 h-4', getKindColor(node.kind))} />
                    </div>

                    <div className="flex-1 min-w-0">
                      <div className="flex items-center gap-2">
                        <span className="font-medium text-theme-text-primary truncate">{node.name}</span>
                        <span className={clsx(
                          'px-1.5 py-0.5 text-xs rounded',
                          'bg-theme-elevated text-theme-text-secondary'
                        )}>
                          {node.kind}
                        </span>
                      </div>
                      {namespace && (
                        <div className="text-xs text-theme-text-tertiary truncate">
                          {namespace}
                        </div>
                      )}
                    </div>

                    <ChevronRight className={clsx(
                      'w-4 h-4 transition-opacity',
                      isSelected ? 'text-theme-text-secondary opacity-100' : 'opacity-0'
                    )} />
                  </button>
                )
              })}
            </div>

            {/* Footer hint */}
            {filteredNodes.length > 0 && (
              <div className="px-4 py-2 border-t border-theme-border bg-theme-surface/50">
                <div className="flex items-center gap-4 text-xs text-theme-text-tertiary">
                  <span className="flex items-center gap-1">
                    <kbd className="px-1 py-0.5 bg-theme-elevated rounded">↑</kbd>
                    <kbd className="px-1 py-0.5 bg-theme-elevated rounded">↓</kbd>
                    <span>Navigate</span>
                  </span>
                  <span className="flex items-center gap-1">
                    <kbd className="px-1.5 py-0.5 bg-theme-elevated rounded">Enter</kbd>
                    <span>Zoom to resource</span>
                  </span>
                </div>
              </div>
            )}

            {/* Empty state with hints */}
            {!query && (
              <div className="px-4 py-6 text-center text-theme-text-tertiary">
                <p className="text-sm mb-3">Search for resources in the topology</p>
                <div className="flex flex-wrap justify-center gap-2 text-xs">
                  <span className="px-2 py-1 bg-theme-elevated/50 rounded">nginx</span>
                  <span className="px-2 py-1 bg-theme-elevated/50 rounded">service/api</span>
                  <span className="px-2 py-1 bg-theme-elevated/50 rounded">production</span>
                </div>
              </div>
            )}
          </div>
        </div>
      )}
    </>
  )
}
