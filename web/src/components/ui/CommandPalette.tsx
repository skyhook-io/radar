import { useState, useMemo, useRef, useEffect, useCallback } from 'react'
import { TRANSITION_BACKDROP, TRANSITION_PANEL } from '../../utils/animation'
import { Search, X, ChevronRight } from 'lucide-react'
import { Home, Network, List, Clock, Package, Activity, Sun, Stethoscope } from 'lucide-react'
import { clsx } from 'clsx'
import { useNamespaces, useContexts } from '../../api/client'
import { CORE_RESOURCES, useAPIResources } from '../../api/apiResources'
import { getResourceIcon } from '../../utils/resource-icons'
type MainView = 'home' | 'topology' | 'resources' | 'timeline' | 'helm' | 'traffic'

interface CommandPaletteProps {
  onClose: () => void
  onNavigateView: (view: MainView) => void
  onNavigateKind: (kind: string, group: string) => void
  onSwitchContext: (name: string) => void
  onSetNamespaces: (ns: string[]) => void
  onToggleTheme: () => void
  onShowDiagnostics?: () => void
  /** Controls fade-in/out animation (driven by useAnimatedUnmount) */
  isOpen?: boolean
}

interface CommandItem {
  id: string
  label: string
  sublabel?: string
  category: string
  icon?: React.ComponentType<{ className?: string }>
  shortcut?: string
  action: () => void
  /** Extra terms to match against during search (not displayed) */
  searchTerms?: string[]
}

// Fuzzy match scoring: exact prefix > word boundary > substring
function scoreMatch(text: string, query: string): number {
  const lower = text.toLowerCase()
  const q = query.toLowerCase()
  if (!lower.includes(q)) return 0
  if (lower.startsWith(q)) return 100
  // Word boundary match
  const wordStart = lower.indexOf(q)
  if (wordStart > 0 && (lower[wordStart - 1] === ' ' || lower[wordStart - 1] === '/' || lower[wordStart - 1] === '-' || lower[wordStart - 1] === '.')) return 75
  return 50
}

function bestScore(item: CommandItem, query: string): number {
  let best = Math.max(
    scoreMatch(item.label, query),
    scoreMatch(item.sublabel || '', query),
    scoreMatch(item.category, query)
  )
  if (item.searchTerms) {
    for (const term of item.searchTerms) {
      best = Math.max(best, scoreMatch(term, query))
    }
  }
  return best
}

export function CommandPalette({
  onClose,
  onNavigateView,
  onNavigateKind,
  onSwitchContext,
  onSetNamespaces,
  onToggleTheme,
  onShowDiagnostics,
  isOpen = true,
}: CommandPaletteProps) {
  const [query, setQuery] = useState('')
  const [selectedIndex, setSelectedIndex] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)
  const resultsRef = useRef<HTMLDivElement>(null)
  const isKeyboardNav = useRef(false)

  const { data: namespacesData } = useNamespaces()
  const { data: contexts } = useContexts()
  const { data: apiResources } = useAPIResources()

  // Focus input on mount
  useEffect(() => {
    inputRef.current?.focus()
  }, [])

  // Close on Escape (capture phase to beat the shortcut system)
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        e.stopPropagation()
        onClose()
      }
    }
    document.addEventListener('keydown', handler, true)
    return () => document.removeEventListener('keydown', handler, true)
  }, [onClose])

  // Build command items
  const items = useMemo<CommandItem[]>(() => {
    const result: CommandItem[] = []

    // Views
    const viewEntries: { view: MainView; label: string; icon: React.ComponentType<{ className?: string }>; shortcut: string }[] = [
      { view: 'home', label: 'Home', icon: Home, shortcut: '1' },
      { view: 'topology', label: 'Topology', icon: Network, shortcut: '2' },
      { view: 'resources', label: 'Resources', icon: List, shortcut: '3' },
      { view: 'timeline', label: 'Timeline', icon: Clock, shortcut: '4' },
      { view: 'helm', label: 'Helm', icon: Package, shortcut: '5' },
      { view: 'traffic', label: 'Traffic', icon: Activity, shortcut: '6' },
    ]
    for (const v of viewEntries) {
      result.push({
        id: `view-${v.view}`,
        label: `Go to ${v.label}`,
        category: 'Views',
        icon: v.icon,
        shortcut: v.shortcut,
        action: () => { onNavigateView(v.view) },
      })
    }

    // Resource kinds (deduplicate by name+group — backend may return multiple API versions)
    const resources = apiResources || CORE_RESOURCES
    const seenKinds = new Set<string>()
    for (const r of resources) {
      if (!r.verbs?.includes('list')) continue
      const kindKey = `${r.name}/${r.group}`
      if (seenKinds.has(kindKey)) continue
      seenKinds.add(kindKey)
      const pluralLabel = r.name.charAt(0).toUpperCase() + r.name.slice(1)
      result.push({
        id: `kind-${r.name}-${r.group}`,
        label: pluralLabel,
        sublabel: r.group || 'core',
        category: 'Resource Kinds',
        icon: getResourceIcon(r.kind),
        action: () => { onNavigateKind(r.name, r.group) },
        searchTerms: [r.name, r.kind],
      })
    }

    // Contexts
    if (contexts) {
      for (const ctx of contexts) {
        result.push({
          id: `context-${ctx.name}`,
          label: ctx.name,
          sublabel: ctx.isCurrent ? 'current' : ctx.cluster,
          category: 'Contexts',
          action: () => { if (!ctx.isCurrent) onSwitchContext(ctx.name) },
        })
      }
    }

    // Namespaces
    if (namespacesData) {
      for (const ns of namespacesData) {
        result.push({
          id: `ns-${ns.name}`,
          label: ns.name,
          category: 'Namespaces',
          action: () => { onSetNamespaces([ns.name]) },
        })
      }
      // "All namespaces" option
      result.push({
        id: 'ns-all',
        label: 'All Namespaces',
        category: 'Namespaces',
        action: () => { onSetNamespaces([]) },
      })
    }

    // Actions
    result.push({
      id: 'action-theme',
      label: 'Toggle Theme',
      category: 'Actions',
      icon: Sun,
      shortcut: 't',
      action: () => { onToggleTheme() },
    })

    if (onShowDiagnostics) {
      result.push({
        id: 'action-diagnostics',
        label: 'Diagnostics',
        category: 'Actions',
        icon: Stethoscope,
        shortcut: 'Ctrl+Shift+D',
        action: () => { onShowDiagnostics() },
        searchTerms: ['debug', 'health', 'status', 'snapshot'],
      })
    }

    return result
  // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [apiResources, contexts, namespacesData, onNavigateView, onNavigateKind, onSwitchContext, onSetNamespaces, onToggleTheme, onShowDiagnostics])

  // Filter and rank results
  const filteredItems = useMemo(() => {
    if (!query.trim()) {
      // Show views and actions when no query
      return items.filter(i => i.category === 'Views' || i.category === 'Actions')
    }

    return items
      .map(item => ({ item, score: bestScore(item, query) }))
      .filter(({ score }) => score > 0)
      .sort((a, b) => b.score - a.score)
      .slice(0, 20)
      .map(({ item }) => item)
  }, [items, query])

  // Group filtered items by category
  const grouped = useMemo(() => {
    const groups = new Map<string, CommandItem[]>()
    for (const item of filteredItems) {
      const list = groups.get(item.category) || []
      list.push(item)
      groups.set(item.category, list)
    }
    return groups
  }, [filteredItems])

  // Single source of truth for flat ordering — derived from grouped to match render order
  const flatItems = useMemo(() => {
    const result: CommandItem[] = []
    for (const [, categoryItems] of grouped) {
      result.push(...categoryItems)
    }
    return result
  }, [grouped])

  // Reset selection when results change
  useEffect(() => {
    setSelectedIndex(0)
  }, [flatItems.length, query])

  // Scroll selected into view
  useEffect(() => {
    if (resultsRef.current && selectedIndex >= 0) {
      const el = resultsRef.current.querySelector('[data-selected="true"]') as HTMLElement
      el?.scrollIntoView({ block: 'nearest' })
    }
  }, [selectedIndex])

  const handleKeyDown = useCallback((e: React.KeyboardEvent) => {
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault()
        isKeyboardNav.current = true
        setSelectedIndex(i => Math.min(i + 1, flatItems.length - 1))
        break
      case 'ArrowUp':
        e.preventDefault()
        isKeyboardNav.current = true
        setSelectedIndex(i => Math.max(i - 1, 0))
        break
      case 'Enter':
        e.preventDefault()
        if (flatItems[selectedIndex]) {
          flatItems[selectedIndex].action()
          onClose()
        }
        break
    }
  }, [flatItems, selectedIndex, onClose])

  // Flatten for index tracking
  let flatIndex = 0

  return (
    <div className="fixed inset-0 z-[100] flex items-start justify-center pt-[15vh]">
      {/* Backdrop */}
      <div
        className={clsx(
          'absolute inset-0 bg-theme-base/60 backdrop-blur-sm',
          TRANSITION_BACKDROP,
          isOpen ? 'opacity-100' : 'opacity-0'
        )}
        onClick={onClose}
      />

      {/* Panel */}
      <div className={clsx(
        'relative w-full max-w-lg mx-4 bg-theme-surface border border-theme-border rounded-xl shadow-2xl overflow-hidden',
        TRANSITION_PANEL,
        isOpen ? 'opacity-100 scale-100 translate-y-0' : 'opacity-0 scale-[0.97] translate-y-3'
      )}>
        {/* Search input */}
        <div className="flex items-center gap-3 px-4 py-3 border-b border-theme-border">
          <Search className="w-5 h-5 text-theme-text-secondary shrink-0" />
          <input
            ref={inputRef}
            type="text"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={handleKeyDown}
            placeholder="Type a command or search..."
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
        <div ref={resultsRef} className="max-h-[50vh] overflow-y-auto">
          {query && filteredItems.length === 0 && (
            <div className="px-4 py-8 text-center text-theme-text-tertiary">
              <Search className="w-8 h-8 mx-auto mb-2 opacity-50" />
              <p>No results for "{query}"</p>
            </div>
          )}

          {Array.from(grouped.entries()).map(([category, categoryItems]) => (
            <div key={category}>
              <div className="px-4 py-1.5 text-[10px] font-semibold text-theme-text-tertiary uppercase tracking-wider bg-theme-surface/50 sticky top-0">
                {category}
              </div>
              {categoryItems.map((item) => {
                const thisIndex = flatIndex++
                const isSelected = thisIndex === selectedIndex
                const Icon = item.icon

                return (
                  <button
                    key={item.id}
                    data-selected={isSelected}
                    onClick={() => { item.action(); onClose() }}
                    onMouseMove={() => {
                      if (isKeyboardNav.current) { isKeyboardNav.current = false; return }
                      setSelectedIndex(thisIndex)
                    }}
                    className={clsx(
                      'w-full flex items-center gap-3 px-4 py-2.5 text-left transition-colors',
                      isSelected ? 'bg-blue-500/15' : 'hover:bg-theme-elevated/30'
                    )}
                  >
                    {Icon && (
                      <div className={clsx(
                        'flex items-center justify-center w-7 h-7 rounded-md shrink-0',
                        isSelected ? 'bg-blue-500/20' : 'bg-theme-elevated/50'
                      )}>
                        <Icon className="w-3.5 h-3.5 text-theme-text-secondary" />
                      </div>
                    )}
                    {!Icon && <div className="w-7 h-7 shrink-0" />}

                    <div className="flex-1 min-w-0">
                      <span className="text-sm text-theme-text-primary truncate block">{item.label}</span>
                      {item.sublabel && (
                        <span className="text-xs text-theme-text-tertiary truncate block">{item.sublabel}</span>
                      )}
                    </div>

                    {item.shortcut && (
                      <kbd className="px-1.5 py-0.5 text-xs text-theme-text-tertiary bg-theme-elevated rounded border border-theme-border-light shrink-0">
                        {item.shortcut}
                      </kbd>
                    )}

                    <ChevronRight className={clsx(
                      'w-3.5 h-3.5 shrink-0 transition-opacity',
                      isSelected ? 'text-theme-text-secondary opacity-100' : 'opacity-0'
                    )} />
                  </button>
                )
              })}
            </div>
          ))}
        </div>

        {/* Footer hints */}
        {filteredItems.length > 0 && (
          <div className="px-4 py-2 border-t border-theme-border bg-theme-surface/50">
            <div className="flex items-center gap-4 text-xs text-theme-text-tertiary">
              <span className="flex items-center gap-1">
                <kbd className="px-1 py-0.5 bg-theme-elevated rounded">↑</kbd>
                <kbd className="px-1 py-0.5 bg-theme-elevated rounded">↓</kbd>
                Navigate
              </span>
              <span className="flex items-center gap-1">
                <kbd className="px-1.5 py-0.5 bg-theme-elevated rounded">↵</kbd>
                Select
              </span>
            </div>
          </div>
        )}
      </div>
    </div>
  )
}
