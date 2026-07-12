import { useRef, type RefObject } from 'react'
import { Search, X } from 'lucide-react'
import { clsx } from 'clsx'
import { Input } from './Input'
import { useRegisterShortcut, type ShortcutScope } from '../../hooks/useKeyboardShortcuts'

/** The standard list-view search box: themed input with a `/`-to-focus
 *  shortcut, Escape-to-blur, and a clear affordance. One definition so the
 *  views can't drift (hand-rolled copies had already diverged: a blue focus
 *  ring in Timeline, no clear button in Audit). ResourcesView keeps its inline
 *  variant — regex mode and row-navigation handoff are coupled to its table. */
export function SearchBox({
  value,
  onChange,
  scope,
  shortcutId,
  placeholder = 'Search... (press /)',
  className,
  onEnter,
  onArrowDown,
  inputRef: externalRef,
  shortcutEnabled = true,
  onBlur,
}: {
  value: string
  onChange: (value: string) => void
  /** Help-overlay grouping + collision priority for the `/` shortcut. */
  scope: ShortcutScope
  /** Unique shortcut id, e.g. 'applications-search'. */
  shortcutId: string
  placeholder?: string
  /** Width/layout overrides — the box itself stays themed. */
  className?: string
  /** Enter in the box — e.g. open the first filtered row. */
  onEnter?: () => void
  /** ArrowDown in the box — hand focus off to list keyboard navigation. */
  onArrowDown?: () => void
  /** Externally-owned input ref — lets a collapsible wrapper focus the box
   *  after it mounts. Falls back to an internal ref when omitted. */
  inputRef?: RefObject<HTMLInputElement | null>
  /** Set false when a wrapper owns the `/` shortcut, so it isn't registered
   *  twice under the same id. */
  shortcutEnabled?: boolean
  /** Blur handler — a collapsible wrapper uses it to fold back when empty. */
  onBlur?: () => void
}) {
  const internalRef = useRef<HTMLInputElement>(null)
  const inputRef = externalRef ?? internalRef

  useRegisterShortcut({
    id: shortcutId,
    keys: '/',
    description: 'Focus search',
    category: 'Search',
    scope,
    enabled: shortcutEnabled,
    handler: () => inputRef.current?.focus(),
  })

  return (
    <div className={clsx('relative', className)}>
      <Search className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-theme-text-tertiary" />
      <Input
        ref={inputRef}
        value={value}
        placeholder={placeholder}
        onChange={(e) => onChange(e.target.value)}
        onBlur={onBlur}
        onKeyDown={(e) => {
          if (e.key === 'Escape') {
            inputRef.current?.blur()
          } else if (e.key === 'Enter' && onEnter) {
            e.preventDefault()
            inputRef.current?.blur()
            onEnter()
          } else if (e.key === 'ArrowDown' && onArrowDown) {
            e.preventDefault()
            inputRef.current?.blur()
            onArrowDown()
          }
        }}
        className="w-full rounded-lg border border-theme-border-light bg-theme-elevated py-1.5 pl-10 pr-9 text-sm text-theme-text-primary placeholder-theme-text-disabled focus:outline-none focus:ring-2 focus:ring-skyhook-500"
      />
      {value && (
        <button
          type="button"
          aria-label="Clear search"
          onClick={() => {
            onChange('')
            inputRef.current?.focus()
          }}
          className="absolute right-2.5 top-1/2 -translate-y-1/2 text-theme-text-tertiary hover:text-theme-text-primary"
        >
          <X className="h-3.5 w-3.5" />
        </button>
      )}
    </div>
  )
}
