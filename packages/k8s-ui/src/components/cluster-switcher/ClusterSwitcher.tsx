import {
  type ReactNode,
  useEffect,
  useMemo,
  useRef,
  useState,
  forwardRef,
  useImperativeHandle,
} from 'react'
import { ChevronDown, Check, ExternalLink, FolderOpen, Loader2, Search, Server, X } from 'lucide-react'
import { ClusterName } from '../ui/ClusterName'
import { Input } from '../ui/Input'
import { MiddleEllipsis } from '../ui/MiddleEllipsis'
import { StatusDot, type StatusTone } from '../ui/status-tone'

export interface ClusterSwitcherItem {
  id: string
  /** Raw context / display string. ClusterName collapses GKE/EKS/AKS
   *  shapes; user-named clusters pass through unchanged. */
  name: string
  /** Short qualifier rendered after the parsed name without affecting
   *  provider detection, e.g. a source suffix for colliding contexts. */
  nameQualifier?: string
  secondary?: string
  badge?: string
  /** Origin label, rendered as a folder-icon line under the name.
   *  Caller must only set this when 2+ distinct sources exist — the
   *  chip surfaces unconditionally when present. */
  sourceLabel?: string
  group?: { key: string; label?: string }
  disabled?: boolean
  status?: StatusTone
  /** Hard navigation target. Takes precedence over the parent's `onSelect`. */
  href?: string
  /** Native tooltip — useful on disabled rows to explain why the row is inert.
   *  ClusterName supplies its own tooltip for the cluster name itself; this
   *  is for row-level affordances (e.g. "Cluster offline — reconnect…"). */
  title?: string
}

export interface ClusterSwitcherHandle {
  open: () => void
}

export interface ClusterSwitcherProps {
  currentId?: string
  /** Raw context / display string. Pass it as-is — the trigger renders
   *  through ClusterName, which handles parse + provider badge + tooltip. */
  currentName: string
  /** Trigger-side counterpart to {@link ClusterSwitcherItem.nameQualifier}. */
  currentNameQualifier?: string
  /** Trigger-side counterpart to {@link ClusterSwitcherItem.sourceLabel}.
   *  Only pass when 2+ kubeconfig sources are loaded. */
  currentSourceLabel?: string
  items: ClusterSwitcherItem[]
  onSelect?: (item: ClusterSwitcherItem) => void
  /** Optional secondary action rendered on each selectable row. */
  onOpenInNewTab?: (item: ClusterSwitcherItem) => void
  searchable?: boolean
  showGroupHeaders?: boolean
  showCurrentBullet?: boolean
  loading?: boolean
  disabled?: boolean
  emptyText?: string
  footerSlot?: ReactNode
  errorSlot?: ReactNode
  className?: string
  align?: 'left' | 'right'
  /**
   * 'chip' (default) renders a self-contained bordered pill. 'segment' renders
   * a borderless label+value cell for embedding in a shared bordered container
   * (the unified cluster+namespace scope control) — no border/background/min-w
   * of its own, with an optional muted {@link label} before the value.
   */
  variant?: 'chip' | 'segment'
  /** Muted label shown before the value in the 'segment' variant (e.g. "Cluster"). */
  label?: string
}

// Trigger width cap. With middle-truncation kicking in, this is a
// horizontal-real-estate guard rather than a readability one. The cap
// grows with viewport so wide screens (where there's no real estate
// pressure) show full cluster names rather than middle-truncating
// pointlessly.
const TRIGGER_NAME_MAX_WIDTH = 'max-w-[140px] sm:max-w-[220px] xl:max-w-[340px]'

export const ClusterSwitcher = forwardRef<ClusterSwitcherHandle, ClusterSwitcherProps>(({
  currentId,
  currentName,
  currentNameQualifier,
  currentSourceLabel,
  items,
  onSelect,
  onOpenInNewTab,
  searchable = true,
  showGroupHeaders = true,
  showCurrentBullet = true,
  loading = false,
  disabled = false,
  emptyText = 'No clusters',
  footerSlot,
  errorSlot,
  className = '',
  align = 'left',
  variant = 'chip',
  label,
}, ref) => {
  const [isOpen, setIsOpen] = useState(false)
  const [search, setSearch] = useState('')
  const [highlightedIndex, setHighlightedIndex] = useState(-1)
  const rootRef = useRef<HTMLDivElement>(null)
  const searchInputRef = useRef<HTMLInputElement>(null)

  useImperativeHandle(ref, () => ({
    open: () => {
      if (!disabled && !loading) setIsOpen(true)
    }
  }), [disabled, loading])

  const groups = useMemo(() => {
    const q = search.trim().toLowerCase()
    const matches = (item: ClusterSwitcherItem) => {
      if (!q) return true
      return (
        item.name.toLowerCase().includes(q) ||
        item.nameQualifier?.toLowerCase().includes(q) ||
        item.secondary?.toLowerCase().includes(q) ||
        item.badge?.toLowerCase().includes(q) ||
        item.sourceLabel?.toLowerCase().includes(q) ||
        item.group?.label?.toLowerCase().includes(q)
      )
    }
    const map = new Map<string, { key: string; label?: string; items: ClusterSwitcherItem[] }>()
    for (const item of items) {
      if (!matches(item)) continue
      const key = item.group?.key ?? ''
      if (!map.has(key)) map.set(key, { key, label: item.group?.label, items: [] })
      map.get(key)!.items.push(item)
    }
    return Array.from(map.values())
  }, [items, search])

  const flat = useMemo(() => groups.flatMap(g => g.items), [groups])
  const indexById = useMemo(() => {
    const m = new Map<string, number>()
    flat.forEach((it, i) => m.set(it.id, i))
    return m
  }, [flat])

  useEffect(() => {
    if (!isOpen) return
    setSearch('')
    setHighlightedIndex(-1)
    requestAnimationFrame(() => searchInputRef.current?.focus())
  }, [isOpen])

  useEffect(() => {
    setHighlightedIndex(-1)
  }, [search])

  useEffect(() => {
    if (!isOpen || highlightedIndex < 0 || !rootRef.current) return
    const el = rootRef.current.querySelector('[data-highlighted="true"]')
    if (el) (el as HTMLElement).scrollIntoView({ block: 'nearest' })
  }, [highlightedIndex, isOpen])

  // Listeners attach only while open so a closed switcher doesn't intercept
  // ESC or click-outside meant for parent modals/overlays.
  useEffect(() => {
    if (!isOpen) return
    function onClick(e: MouseEvent) {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) setIsOpen(false)
    }
    function onEsc(e: KeyboardEvent) {
      if (e.key === 'Escape') setIsOpen(false)
    }
    document.addEventListener('mousedown', onClick)
    document.addEventListener('keydown', onEsc)
    return () => {
      document.removeEventListener('mousedown', onClick)
      document.removeEventListener('keydown', onEsc)
    }
  }, [isOpen])

  const select = (item: ClusterSwitcherItem) => {
    if (item.disabled || item.id === currentId) return
    setIsOpen(false)
    if (item.href) {
      window.location.href = item.href
      return
    }
    onSelect?.(item)
  }

  const onSearchKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setHighlightedIndex(p => (p < flat.length - 1 ? p + 1 : p))
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setHighlightedIndex(p => (p > 0 ? p - 1 : 0))
    } else if (e.key === 'Enter') {
      e.preventDefault()
      if (highlightedIndex >= 0 && flat[highlightedIndex]) select(flat[highlightedIndex])
      else if (flat.length > 0) setHighlightedIndex(0)
    } else if (e.key === 'Escape') {
      e.preventDefault()
      setIsOpen(false)
    }
  }

  const positionClass = align === 'right' ? 'right-0 origin-top-right' : 'left-0 origin-top-left'

  return (
    <div className={`relative ${className}`} ref={rootRef}>
      <button
        type="button"
        onClick={() => setIsOpen(v => !v)}
        disabled={disabled || loading}
        className={
          variant === 'segment'
            ? `flex items-center gap-1.5 px-3 py-1.5 h-full min-w-[150px] max-w-[264px] overflow-hidden text-[13px] font-medium
               text-theme-text-primary hover:bg-theme-hover transition-colors cursor-pointer
               disabled:opacity-50 disabled:cursor-not-allowed`
            : `flex items-center gap-1.5 px-2.5 py-1.5 min-w-[140px]
               bg-theme-elevated border border-theme-border rounded text-sm font-medium
               text-theme-text-primary hover:bg-theme-hover hover:border-theme-border-light
               transition-colors cursor-pointer
               disabled:opacity-50 disabled:cursor-not-allowed`
        }
      >
        {label && (
          <span className="shrink-0 font-normal text-theme-text-tertiary">{label}</span>
        )}
        {loading ? (
          <>
            <Loader2 className="w-3.5 h-3.5 animate-spin" />
            <span className={`${TRIGGER_NAME_MAX_WIDTH} block truncate`}>Switching…</span>
          </>
        ) : (
          // ClusterName parses the context (provider badge for GKE/EKS/AKS,
          // raw name for custom kubeconfig) and middle-truncates to the cap.
          // Server icon is the fallback badge for the no-provider case so
          // the trigger always has a leading visual. Tooltip is suppressed
          // while the dropdown is open — the popover already shows the raw
          // context inline (per-row secondary line), and an extra hover
          // tooltip would just overlap the search input.
          <>
            {/* flex (not a plain span): ClusterName is inline-flex and would
                otherwise baseline-align inside this wrapper's line box,
                sitting a couple px above the segment's vertical center. */}
            <span className={variant === 'segment' ? 'flex items-center min-w-0 flex-1' : 'flex items-center min-w-0'}>
              <ClusterName
                name={currentName}
                fallbackBadge={<Server className="w-3.5 h-3.5 text-theme-text-secondary" />}
                className={variant === 'segment' ? 'min-w-0 max-w-full' : TRIGGER_NAME_MAX_WIDTH}
                noTooltip={isOpen}
              />
            </span>
            {currentNameQualifier && (
              <span
                className="shrink-0 max-w-[80px] truncate text-xs font-normal text-theme-text-tertiary"
                title={currentNameQualifier}
              >
                {currentNameQualifier}
              </span>
            )}
            {currentSourceLabel && (
              // Icon-only on the trigger: long folder paths (the very case
              // that motivates the chip) middle-truncate to something
              // useless like "kube-cluster-pro…ion-eu" and steal width
              // from the cluster name + nav. The folder icon signals
              // "multi-source — disambiguation in dropdown"; hover or
              // open the dropdown for the full label.
              <span
                className="shrink-0 inline-flex items-center text-theme-text-tertiary opacity-80"
                title={`From kubeconfig: ${currentSourceLabel}`}
                aria-label={`From kubeconfig: ${currentSourceLabel}`}
              >
                <FolderOpen className="w-3 h-3" />
              </span>
            )}
          </>
        )}
        <ChevronDown className={`w-3 h-3 shrink-0 transition-transform ${variant === 'segment' ? '' : 'ml-auto'} ${isOpen ? 'rotate-180' : ''}`} />
      </button>

      {isOpen && (
        <div
          className={`absolute top-full ${positionClass} mt-1 z-50 min-w-[280px] max-w-[420px] bg-theme-surface border border-theme-border-light rounded-lg shadow-xl overflow-hidden`}
        >
          {searchable && (
            <div className="p-2 border-b border-theme-border">
              <div className="relative">
                <Search className="absolute left-2 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-theme-text-tertiary" />
                <Input
                  ref={searchInputRef}
                  value={search}
                  onChange={e => setSearch(e.target.value)}
                  onKeyDown={onSearchKeyDown}
                  placeholder="Search clusters..."
                  className="w-full bg-theme-base text-theme-text-primary text-xs rounded px-2 py-1.5 pl-7 pr-7 border border-theme-border-light focus:outline-none focus:ring-1 focus:ring-[var(--color-brand)] placeholder:text-theme-text-tertiary"
                />
                {search && (
                  <button
                    type="button"
                    onClick={() => setSearch('')}
                    className="absolute right-2 top-1/2 -translate-y-1/2 text-theme-text-tertiary hover:text-theme-text-secondary"
                  >
                    <X className="w-3.5 h-3.5" />
                  </button>
                )}
              </div>
            </div>
          )}

          <div className="max-h-[400px] overflow-y-auto">
            {flat.length === 0 ? (
              <div className="px-3 py-6 text-center text-xs text-theme-text-tertiary">
                {search ? `No clusters match "${search}"` : emptyText}
              </div>
            ) : (
              groups.map((group, gi) => (
                <div key={group.key || `g${gi}`}>
                  {gi > 0 && <div className="border-t border-theme-border-light my-1" />}
                  {showGroupHeaders && group.label && (
                    <div className="px-3 py-1 bg-theme-elevated/60 border-b border-theme-border/60">
                      <span className="text-[11px] text-theme-text-secondary font-semibold">
                        {group.label}
                      </span>
                    </div>
                  )}
                  {group.items.map(item => {
                    const idx = indexById.get(item.id) ?? -1
                    const isCurrent = item.id === currentId
                    return (
                      <div
                        key={item.id}
                        data-highlighted={idx === highlightedIndex}
                        onMouseEnter={() => setHighlightedIndex(idx)}
                        className={`
                          group relative flex w-full items-stretch transition-colors
                          ${isCurrent
                            ? 'selection'
                            : idx === highlightedIndex
                              ? 'bg-theme-hover cursor-pointer'
                              : 'hover:bg-theme-hover cursor-pointer'}
                        `}
                      >
                        <button
                          type="button"
                          onClick={() => select(item)}
                          disabled={isCurrent || item.disabled}
                          title={item.title}
                          className="flex min-w-0 flex-1 items-center gap-2 px-3 py-2 text-left disabled:opacity-50"
                        >
                          <div className="shrink-0 w-4 h-4 flex items-center justify-center">
                            {isCurrent ? (
                              <Check className="w-3.5 h-3.5 selection-text" />
                            ) : showCurrentBullet ? (
                              <div className="w-1.5 h-1.5 rounded-full bg-theme-text-tertiary/30" />
                            ) : null}
                          </div>
                          {item.status && (
                            <span className="shrink-0">
                              <StatusDot tone={item.status} />
                            </span>
                          )}
                          <div className="flex-1 min-w-0">
                            <div className="flex items-center gap-1.5">
                              {/* No tooltip on row names — each row already
                                  renders the raw context inline below the
                                  name (item.secondary), so the hover tooltip
                                  would just repeat what's already visible. */}
                              <ClusterName
                                name={item.name}
                                noTooltip
                                className={`text-sm font-medium flex-1 ${
                                  isCurrent
                                    ? 'selection-text'
                                    : item.disabled
                                      ? 'text-theme-text-tertiary'
                                      : 'text-theme-text-primary'
                                }`}
                              />
                              {item.nameQualifier && (
                                <span
                                  className="shrink-0 max-w-[120px] truncate text-xs font-normal text-theme-text-tertiary"
                                  title={item.nameQualifier}
                                >
                                  {item.nameQualifier}
                                </span>
                              )}
                              {item.badge && (
                                <span className="shrink-0 text-[10px] text-theme-text-tertiary bg-theme-elevated px-1 rounded">
                                  {item.badge}
                                </span>
                              )}
                            </div>
                            {item.sourceLabel && (
                              // Source chip lives on its own line under the
                              // name so long folder paths get the full row
                              // width to render via MiddleEllipsis. Inline
                              // would steal width from the name and force
                              // both to truncate.
                              <div
                                className="flex items-center gap-0.5 text-[10px] text-theme-text-tertiary opacity-80 mt-0.5"
                                title={`From kubeconfig: ${item.sourceLabel}`}
                              >
                                <FolderOpen className="w-2.5 h-2.5 shrink-0" />
                                <MiddleEllipsis text={item.sourceLabel} className="font-mono" />
                              </div>
                            )}
                            {item.secondary && (
                              <div
                                className="text-[10px] text-theme-text-tertiary opacity-70 truncate mt-0.5"
                                title={item.secondary}
                              >
                                {item.secondary}
                              </div>
                            )}
                          </div>
                        </button>
                        {onOpenInNewTab && !isCurrent && !item.disabled && (
                          <button
                            type="button"
                            onClick={() => {
                              setIsOpen(false)
                              onOpenInNewTab(item)
                            }}
                            className="mr-2 my-2 flex h-7 w-7 shrink-0 items-center justify-center rounded text-theme-text-tertiary opacity-70 transition-colors hover:bg-theme-elevated hover:text-theme-text-primary hover:opacity-100 focus-visible:opacity-100"
                            title={`Open ${item.name} in a new tab`}
                            aria-label={`Open ${item.name} in a new tab`}
                          >
                            <ExternalLink className="h-3.5 w-3.5" />
                          </button>
                        )}
                      </div>
                    )
                  })}
                </div>
              ))
            )}
          </div>

          {search && flat.length > 0 && flat.length < items.length && (
            <div className="px-3 py-1.5 text-[10px] text-theme-text-tertiary border-t border-theme-border bg-theme-base">
              {flat.length} of {items.length} clusters
            </div>
          )}

          {footerSlot && (
            <div className="border-t border-theme-border bg-theme-base">{footerSlot}</div>
          )}

          {errorSlot && (
            <div className="px-3 py-2 bg-red-500/10 border-t border-red-500/20">
              {errorSlot}
            </div>
          )}
        </div>
      )}
    </div>
  )
})
