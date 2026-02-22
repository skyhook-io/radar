import { useEffect, useRef } from 'react'
import { X } from 'lucide-react'
import { useActiveShortcuts, type ShortcutCategory } from '../../hooks/useKeyboardShortcuts'

interface ShortcutHelpOverlayProps {
  onClose: () => void
}

// Order categories should appear in the help overlay
const CATEGORY_ORDER: ShortcutCategory[] = [
  'Navigation',
  'General',
  'Search',
  'Table',
  'Resource Actions',
  'Topology',
  'Timeline',
  'Helm',
  'Drawer',
  'Dock',
]

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
  if (keys.includes('+')) {
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
    case 'ctrl': return isMac ? '⌃' : 'Ctrl'
    case 'shift': return isMac ? '⇧' : 'Shift'
    case 'alt': return isMac ? '⌥' : 'Alt'
    case 'escape': return 'Esc'
    case 'enter': return '↵'
    case 'arrowup': return '↑'
    case 'arrowdown': return '↓'
    case 'arrowleft': return '←'
    case 'arrowright': return '→'
    default: return key
  }
}

export function ShortcutHelpOverlay({ onClose }: ShortcutHelpOverlayProps) {
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

  // Group shortcuts by category
  const grouped = new Map<ShortcutCategory, typeof shortcuts>()
  for (const s of shortcuts) {
    // Skip the help overlay's own shortcut (? key)
    if (s.id === 'help-toggle') continue
    const list = grouped.get(s.category) || []
    list.push(s)
    grouped.set(s.category, list)
  }

  // Sort categories by defined order
  const sortedCategories = CATEGORY_ORDER.filter(c => grouped.has(c))

  return (
    <div className="fixed inset-0 z-[100] flex items-center justify-center p-4">
      {/* Backdrop */}
      <div className="absolute inset-0 bg-theme-base/70 backdrop-blur-sm" onClick={onClose} />

      {/* Panel */}
      <div
        ref={overlayRef}
        className="relative w-full max-w-2xl max-h-[80vh] bg-theme-surface border border-theme-border rounded-xl shadow-2xl overflow-hidden flex flex-col"
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
        <div className="overflow-y-auto px-5 py-4">
          <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
            {sortedCategories.map(category => (
              <div key={category}>
                <h3 className="text-xs font-semibold text-theme-text-tertiary uppercase tracking-wider mb-2.5">
                  {category}
                </h3>
                <div className="space-y-1.5">
                  {grouped.get(category)!.map(shortcut => (
                    <div key={shortcut.id} className="flex items-center justify-between py-1">
                      <span className="text-sm text-theme-text-secondary">{shortcut.description}</span>
                      <ShortcutKeys keys={shortcut.keys} />
                    </div>
                  ))}
                </div>
              </div>
            ))}
          </div>

          {sortedCategories.length === 0 && (
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
