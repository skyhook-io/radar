import { useState, useMemo, useRef, useEffect, useCallback } from 'react'
import { TRANSITION_BACKDROP, TRANSITION_PANEL } from '../../utils/animation'
import { Search, X, ChevronRight } from 'lucide-react'
import { clsx } from 'clsx'
import { useCommandItems, bestScore, type CommandItem, type CommandItemCallbacks } from './command-items'
import { Input } from '@skyhook-io/k8s-ui'

interface CommandPaletteProps extends CommandItemCallbacks {
  onClose: () => void
  /** Controls fade-in/out animation (driven by useAnimatedUnmount) */
  isOpen?: boolean
}


export function CommandPalette({ onClose, isOpen = true, ...callbacks }: CommandPaletteProps) {
  const [query, setQuery] = useState('')
  const [selectedIndex, setSelectedIndex] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)
  const resultsRef = useRef<HTMLDivElement>(null)
  const isKeyboardNav = useRef(false)

  // The static command items (Views / Kinds / Namespaces / Actions) — shared
  // with the standalone omnibar via useCommandItems so the two never drift.
  const items = useCommandItems(callbacks)

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

  // Filter and rank results
  const filteredItems = useMemo(() => {
    if (!query.trim()) {
      // Show views and actions when no query
      return items.filter(i => i.category === 'Views' || i.category === 'Actions')
    }

    return items
      .map(item => {
        let score = bestScore(item, query)
        // Boost core K8s resources over CRDs at the same match quality
        if (score > 0 && item.category === 'Resource Kinds' && item.sublabel === 'core') {
          score += 10
        }
        return { item, score }
      })
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
        'relative w-full max-w-lg mx-4 dialog overflow-hidden',
        TRANSITION_PANEL,
        isOpen ? 'opacity-100 scale-100 translate-y-0' : 'opacity-0 scale-[0.97] translate-y-3'
      )}>
        {/* Search input */}
        <div className="flex items-center gap-3 px-4 py-3 border-b border-theme-border">
          <Search className="w-5 h-5 text-theme-text-secondary shrink-0" />
          <Input
            ref={inputRef}
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
                      isSelected ? 'selection' : 'hover:bg-theme-elevated/30'
                    )}
                  >
                    {Icon && (
                      <div className={clsx(
                        'flex items-center justify-center w-7 h-7 rounded-md shrink-0',
                        isSelected ? 'selection-strong' : 'bg-theme-elevated/50'
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
