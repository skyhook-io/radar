import { useEffect, useRef } from 'react'
import { X } from 'lucide-react'
import { clsx } from 'clsx'
import { TRANSITION_BACKDROP, TRANSITION_PANEL } from '../../utils/animation'
import { useActiveShortcuts, type ShortcutCategory } from '../../hooks/useKeyboardShortcuts'

interface ShortcutHelpOverlayProps {
  onClose: () => void
  currentView?: string
  /** Controls fade-in/out animation (driven by useAnimatedUnmount) */
  isOpen?: boolean
}

// Categories that always appear at the top
const GLOBAL_CATEGORIES: ShortcutCategory[] = ['Navigation', 'General']

// Categories tied to contextual UI (not a specific view)
const CONTEXT_CATEGORIES: ShortcutCategory[] = ['Drawer', 'Dock']

// Preferred ordering within the view section
const VIEW_CATEGORY_ORDER: ShortcutCategory[] = [
  'Search', 'Table', 'Resource Actions', 'Topology', 'Timeline', 'Helm',
]

const VIEW_LABELS: Record<string, string> = {
  home: 'Home',
  topology: 'Topology',
  resources: 'Resources',
  timeline: 'Timeline',
  helm: 'Helm',
  traffic: 'Traffic',
}

type ShortcutEntry = { description: string; keys: string[] }

function KbdKey({ children }: { children: string }) {
  return (
    <kbd className="inline-flex items-center justify-center min-w-[1.5rem] h-6 px-1.5 text-xs font-mono font-medium bg-theme-elevated border border-theme-border-light rounded text-theme-text-primary shadow-sm">
      {children}
    </kbd>
  )
}

function ShortcutKeys({ keys }: { keys: string }) {
  // Handle multi-key sequences like "g g"
  if (keys.includes(' ') && !keys.includes('+')) {
    const parts = keys.split(' ')
    return (
      <span className="flex items-center gap-1">
        {parts.map((part, i) => (
          <span key={i} className="flex items-center gap-0.5">
            {i > 0 && <span className="text-theme-text-tertiary text-xs mx-0.5"></span>}
            <KbdKey>{part}</KbdKey>
          </span>
        ))}
      </span>
    )
  }

  // Handle modifier combos like "Cmd+K", "Ctrl+D", "Shift+N"
  // But not the literal "+" key itself
  if (keys.includes('+') && keys !== '+') {
    const parts = keys.split('+')
    return (
      <span className="flex items-center gap-0.5">
        {parts.map((part, i) => (
          <span key={i} className="flex items-center gap-0.5">
            {i > 0 && <span className="text-theme-text-tertiary text-[10px]">+</span>}
            <KbdKey>{formatKeyLabel(part)}</KbdKey>
          </span>
        ))}
      </span>
    )
  }

  // Single key
  return <KbdKey>{formatKeyLabel(keys)}</KbdKey>
}

function formatKeyLabel(key: string): string {
  const isMac = typeof navigator !== 'undefined' && navigator.platform.includes('Mac')
  switch (key.toLowerCase()) {
    case 'cmd':
    case 'meta': return isMac ? '⌘' : 'Ctrl'
    case 'ctrl': return 'Ctrl'
    case 'shift': return isMac ? '⇧' : 'Shift'
    case 'alt': return isMac ? '⌥' : 'Alt'
    case 'escape': return 'Esc'
    case 'enter': return '↵'
    case 'arrowup': return '↑'
    case 'arrowdown': return '↓'
    case 'arrowleft': return '←'
    case 'arrowright': return '→'
    case '`': return '`'
    default: return key
  }
}

function CategoryBlock({ category, entries }: { category: string; entries: ShortcutEntry[] }) {
  return (
    <div>
      <h3 className="text-xs font-semibold text-theme-text-tertiary uppercase tracking-wider mb-2.5">
        {category}
      </h3>
      <div className="space-y-1.5">
        {entries.map(entry => (
          <div key={entry.description} className="flex items-center justify-between py-1">
            <span className="text-sm text-theme-text-secondary">{entry.description}</span>
            <span className="flex items-center gap-1.5 ml-4 shrink-0">
              {entry.keys.map((k, i) => (
                <span key={k} className="flex items-center gap-1.5">
                  {i > 0 && <span className="text-theme-text-tertiary text-[10px]">/</span>}
                  <ShortcutKeys keys={k} />
                </span>
              ))}
            </span>
          </div>
        ))}
      </div>
    </div>
  )
}

// Balance categories into two columns by item count (descending-greedy for best packing)
function balanceColumns(categories: ShortcutCategory[], grouped: Map<ShortcutCategory, ShortcutEntry[]>): [ShortcutCategory[], ShortcutCategory[]] {
  const sorted = [...categories].sort((a, b) =>
    (grouped.get(b)?.length || 0) - (grouped.get(a)?.length || 0)
  )
  const leftSet = new Set<ShortcutCategory>()
  const rightSet = new Set<ShortcutCategory>()
  let leftCount = 0, rightCount = 0

  for (const cat of sorted) {
    const count = grouped.get(cat)!.length
    if (leftCount <= rightCount) {
      leftSet.add(cat)
      leftCount += count
    } else {
      rightSet.add(cat)
      rightCount += count
    }
  }

  // Preserve original ordering within each column
  const left = categories.filter(c => leftSet.has(c))
  const right = categories.filter(c => rightSet.has(c))
  return [left, right]
}

function TwoColumnSection({ categories, grouped }: { categories: ShortcutCategory[]; grouped: Map<ShortcutCategory, ShortcutEntry[]> }) {
  if (categories.length === 0) return null

  if (categories.length <= 2) {
    // Sort larger category to the left for visual balance
    const sorted = [...categories].sort((a, b) =>
      (grouped.get(b)?.length || 0) - (grouped.get(a)?.length || 0)
    )
    return (
      <div className="grid grid-cols-1 md:grid-cols-2 gap-x-8 gap-y-4">
        {sorted.map(cat => (
          <CategoryBlock key={cat} category={cat} entries={grouped.get(cat)!} />
        ))}
      </div>
    )
  }

  const [left, right] = balanceColumns(categories, grouped)
  return (
    <div className="grid grid-cols-1 md:grid-cols-2 gap-x-8">
      <div className="space-y-4">
        {left.map(cat => <CategoryBlock key={cat} category={cat} entries={grouped.get(cat)!} />)}
      </div>
      <div className="space-y-4">
        {right.map(cat => <CategoryBlock key={cat} category={cat} entries={grouped.get(cat)!} />)}
      </div>
    </div>
  )
}

export function ShortcutHelpOverlay({ onClose, currentView, isOpen = true }: ShortcutHelpOverlayProps) {
  const shortcuts = useActiveShortcuts()
  const overlayRef = useRef<HTMLDivElement>(null)

  // Close on Escape
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault()
        e.stopPropagation()
        onClose()
      }
    }
    // Use capture to intercept before the shortcut system
    document.addEventListener('keydown', handler, true)
    return () => document.removeEventListener('keydown', handler, true)
  }, [onClose])

  // Group shortcuts by category, merging duplicates with same description
  // (e.g., "Zoom in" registered for both + and = shows as one row)
  const grouped = new Map<ShortcutCategory, ShortcutEntry[]>()
  for (const s of shortcuts) {
    if (s.id === 'help-toggle') continue
    const list = grouped.get(s.category) || []
    const existing = list.find(item => item.description === s.description)
    if (existing) {
      existing.keys.push(s.keys)
    } else {
      list.push({ description: s.description, keys: [s.keys] })
    }
    grouped.set(s.category, list)
  }

  // Split categories into sections
  const globalCategories = GLOBAL_CATEGORIES.filter(c => grouped.has(c))
  const viewCategories = VIEW_CATEGORY_ORDER.filter(
    c => grouped.has(c) && !GLOBAL_CATEGORIES.includes(c) && !CONTEXT_CATEGORIES.includes(c)
  )
  const contextCategories = CONTEXT_CATEGORIES.filter(c => grouped.has(c))

  const viewLabel = currentView ? VIEW_LABELS[currentView] : null
  const hasViewSection = viewCategories.length > 0
  const hasContextSection = contextCategories.length > 0
  const isEmpty = globalCategories.length === 0 && !hasViewSection && !hasContextSection

  return (
    <div className="fixed inset-0 z-[100] flex items-center justify-center p-4">
      {/* Backdrop */}
      <div
        className={clsx(
          'absolute inset-0 bg-theme-base/70 backdrop-blur-sm',
          TRANSITION_BACKDROP,
          isOpen ? 'opacity-100' : 'opacity-0'
        )}
        onClick={onClose}
      />

      {/* Panel */}
      <div
        ref={overlayRef}
        className={clsx(
          'relative w-full max-w-2xl max-h-[80vh] dialog overflow-hidden flex flex-col',
          TRANSITION_PANEL,
          isOpen ? 'opacity-100 scale-100' : 'opacity-0 scale-[0.97]'
        )}
      >
        {/* Header */}
        <div className="flex items-center justify-between px-5 py-3.5 border-b border-theme-border">
          <h2 className="text-base font-semibold text-theme-text-primary">Keyboard Shortcuts</h2>
          <button
            onClick={onClose}
            className="p-1.5 rounded-md text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-hover transition-colors"
          >
            <X className="w-4 h-4" />
          </button>
        </div>

        {/* Content */}
        <div className="overflow-y-auto px-5 py-4 space-y-5">
          {/* Global section — Navigation + General */}
          <TwoColumnSection categories={globalCategories} grouped={grouped} />

          {/* View-specific section */}
          {hasViewSection && (
            <>
              {viewLabel && (
                <div className="flex items-center gap-3">
                  <div className="h-px flex-1 bg-theme-border-light" />
                  <span className="text-[10px] font-semibold text-theme-text-tertiary uppercase tracking-wider">
                    {viewLabel}
                  </span>
                  <div className="h-px flex-1 bg-theme-border-light" />
                </div>
              )}
              <TwoColumnSection categories={viewCategories} grouped={grouped} />
            </>
          )}

          {/* Contextual section (Drawer, Dock) */}
          {hasContextSection && (
            <TwoColumnSection categories={contextCategories} grouped={grouped} />
          )}

          {/* Empty state */}
          {isEmpty && (
            <p className="text-sm text-theme-text-tertiary text-center py-8">
              No keyboard shortcuts registered.
            </p>
          )}
        </div>

        {/* Footer */}
        <div className="px-5 py-2.5 border-t border-theme-border bg-theme-surface/50">
          <div className="flex items-center justify-between text-xs text-theme-text-tertiary">
            <span>Press <KbdKey>?</KbdKey> to toggle this overlay</span>
            <span>Press <KbdKey>Esc</KbdKey> to close</span>
          </div>
        </div>
      </div>
    </div>
  )
}
