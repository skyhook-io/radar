import { forwardRef, useCallback, useEffect, useImperativeHandle, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { ChevronDown, Globe, Search, AlertTriangle } from 'lucide-react'
import { Tooltip } from '../ui/Tooltip'
import { MultiSelectPicker } from '../ui/MultiSelectPicker'

/**
 * Backend-reported namespace scope. Mirrors Radar's `/cluster/namespace-scope`
 * response shape; the host supplies it however it fetches it (OSS via its API
 * hooks, Radar Hub via the per-cluster apiBase).
 */
export interface NamespaceScopeView {
  /** Currently-selected namespaces. Empty = cluster-wide ("All namespaces"). */
  actives: string[]
  /** Namespaces the user may pick from. */
  accessibleNamespaces: string[]
  mode?: 'cluster-wide' | 'namespace' | 'restricted' | string
  /** Single-namespace cache-scope control instead of a multi-select filter. */
  cacheScoped?: boolean
  /** Under cacheScoped, whether the user may re-point the watched namespace. */
  namespaceRescope?: boolean
  cacheScopeNamespace?: string
  kubeconfigNamespace?: string
  canClearNamespace?: boolean
  /** false when accessibleNamespaces is a best-effort short list (no list perm). */
  authoritative?: boolean
}

export interface NamespacePickerHandle {
  open: () => void
}

export interface NamespacePickerProps {
  /** null/undefined while loading — the picker renders nothing until it arrives. */
  scope: NamespaceScopeView | null | undefined
  /**
   * Applied when the selection is committed (dropdown close / clear all).
   * The picker only calls this when the selection actually changed and is
   * valid (respects the cacheScoped single-namespace constraint).
   */
  onApply: (namespaces: string[]) => void
  loading?: boolean
  /** A switch/mutation is in flight — trigger shows "Switching…" and is inert. */
  pending?: boolean
  disabled?: boolean
  disabledTooltip?: string
  className?: string
  /**
   * 'chip' (default) renders a self-contained pill. 'segment' renders a
   * borderless label+value cell for embedding in a shared bordered container
   * (the unified cluster+namespace scope control), with an optional muted
   * {@link label} before the value and a shorter value ("All" vs "All namespaces").
   */
  variant?: 'chip' | 'segment'
  /** Muted label shown before the value in the 'segment' variant (e.g. "Namespace"). */
  label?: string
}

/**
 * NamespacePicker is the presentational namespace scope control shared by
 * Radar OSS and Radar Hub (mirrors the ClusterSwitcher pattern — pure UI, data
 * injected via props). It is normally a per-user multi-select view filter. When
 * the scope reports cacheScoped=true, it becomes a single-namespace cache scope
 * control.
 *
 * Three states reflect what the scope reports:
 *   - cluster-wide: empty trigger label "All namespaces"; picker lets the user
 *     narrow the view.
 *   - namespace:    label shows the namespace count (or single name); picker
 *     offers other accessible namespaces and a clear-all reset.
 *   - restricted:   user can't list namespaces and isn't pinned; picker
 *     surfaces only the kubeconfig context's namespace + any saved picks.
 *
 * Selection model: the dropdown keeps a draft Set<string>; toggling rows
 * mutates the draft locally; closing the dropdown applies the draft in a single
 * onApply. "Clear all" applies immediately and closes.
 */
export const NamespacePicker = forwardRef<NamespacePickerHandle, NamespacePickerProps>(function NamespacePicker(
  { scope, onApply, loading = false, pending = false, disabled = false, disabledTooltip, className = '', variant = 'chip', label },
  ref,
) {
  const [isOpen, setIsOpen] = useState(false)
  const [search, setSearch] = useState('')
  const [pos, setPos] = useState({ top: 0, left: 0, width: 0 })
  const [draft, setDraft] = useState<Set<string>>(() => new Set())

  const triggerRef = useRef<HTMLButtonElement>(null)
  const dropdownRef = useRef<HTMLDivElement>(null)

  const scopeActives = useMemo(() => scope?.actives ?? [], [scope?.actives])
  const activesKey = useMemo(() => [...scopeActives].sort().join(','), [scopeActives])

  // Sync the draft with the server's view whenever it changes (initial load,
  // post-mutation refetch, eviction after RBAC drift).
  useEffect(() => {
    setDraft(new Set(scopeActives))
  }, [activesKey, scopeActives])

  // Fully disable when the host disables the control (e.g. view-awareness
  // navigating to a cluster-scoped surface): the trigger is inert and open()
  // is blocked, but an already-open dropdown would still commit via Done /
  // outside-click / Clear all — so close it (discarding the uncommitted draft)
  // rather than letting a dead view apply a namespace change.
  useEffect(() => {
    if (disabled && isOpen) {
      setIsOpen(false)
      setSearch('')
      // Discard the uncommitted draft so a later re-open reflects the current
      // server actives, not stale toggles from before the control was disabled.
      setDraft(new Set(scopeActives))
    }
  }, [disabled, isOpen, scopeActives])

  const items = useMemo(() => {
    if (!scope) return [] as string[]
    return [...(scope.accessibleNamespaces ?? [])].sort((a, b) => a.localeCompare(b))
  }, [scope])

  const filteredItems = useMemo(() => {
    const q = search.trim().toLowerCase()
    if (!q) return items
    return items.filter(n => n.toLowerCase().includes(q))
  }, [items, search])

  const applySelection = useCallback((next: Set<string>) => {
    if (!scope) return
    const nextArr = Array.from(next).sort()
    if (scope.cacheScoped && nextArr.length !== 1) return
    if (nextArr.join(',') === activesKey) return
    onApply(nextArr)
  }, [activesKey, scope, onApply])

  const closeAndApply = useCallback(() => {
    setIsOpen(false)
    setSearch('')
    applySelection(draft)
  }, [applySelection, draft])

  useImperativeHandle(ref, () => ({
    open: () => {
      if (disabled || loading || pending) return
      setIsOpen(true)
    },
  }), [disabled, loading, pending])

  useEffect(() => {
    if (!isOpen) return
    const trigger = triggerRef.current
    if (!trigger) return
    const r = trigger.getBoundingClientRect()
    setPos({ top: r.bottom + 4, left: r.left, width: Math.max(r.width, 240) })
  }, [isOpen])

  useEffect(() => {
    if (!isOpen) return
    function onClick(e: MouseEvent) {
      if (
        !dropdownRef.current?.contains(e.target as Node) &&
        !triggerRef.current?.contains(e.target as Node)
      ) {
        closeAndApply()
      }
    }
    function onKey(e: KeyboardEvent) {
      if (e.key === 'Escape') closeAndApply()
    }
    document.addEventListener('mousedown', onClick)
    document.addEventListener('keydown', onKey)
    return () => {
      document.removeEventListener('mousedown', onClick)
      document.removeEventListener('keydown', onKey)
    }
  }, [isOpen, closeAndApply])

  if (!scope) return null

  const toggle = (ns: string) => {
    if (scope.cacheScoped) {
      setDraft(new Set([ns]))
      return
    }
    const next = new Set(draft)
    if (next.has(ns)) next.delete(ns)
    else next.add(ns)
    setDraft(next)
  }

  const clearAll = () => {
    if (scope.cacheScoped) return
    setDraft(new Set())
    setIsOpen(false)
    setSearch('')
    applySelection(new Set())
  }

  const activeCount = scopeActives.length
  const triggerLabel =
    activeCount === 0 ? 'All namespaces' : activeCount === 1 ? scopeActives[0] : `${activeCount} namespaces`
  const isClusterWide = activeCount === 0
  const restrictedHint = scope.mode === 'restricted'
  const cacheScopeLocked = scope.cacheScoped && !scope.namespaceRescope
  const isDisabled = disabled || loading || pending || cacheScopeLocked
  const canClearAll = scope.canClearNamespace || activeCount === 0
  const tooltipContent = disabled && disabledTooltip
    ? disabledTooltip
    : scope.cacheScoped
      ? scope.namespaceRescope
        ? `Radar is watching only ${scope.cacheScopeNamespace || triggerLabel} to stay fast on large clusters. Pick another namespace to re-point it (takes a moment; closes open terminals).`
        : `Radar is watching only ${scope.cacheScopeNamespace || triggerLabel} on this cluster.`
      : restrictedHint
      ? 'Limited namespace visibility — only namespaces granted by your RBAC are shown.'
      : isClusterWide
        ? 'Currently viewing all namespaces. Click to narrow the view.'
        : activeCount === 1
          ? `View is filtered to namespace ${scopeActives[0]}. Click to switch or reset.`
          : `View is filtered to ${activeCount} namespaces. Click to adjust or reset.`

  return (
    <>
      <Tooltip
        content={tooltipContent}
        delay={300}
        position="bottom"
      >
        <button
          ref={triggerRef}
          onClick={() => !isDisabled && (isOpen ? closeAndApply() : setIsOpen(true))}
          disabled={isDisabled}
          className={
            variant === 'segment'
              ? `flex items-center justify-center gap-1.5 px-3 py-1.5 h-full min-w-[110px] max-w-[200px] text-[13px] text-theme-text-primary hover:bg-theme-hover disabled:opacity-60 transition-colors ${className}`
              : `flex items-center gap-1.5 px-2 py-1 rounded text-sm bg-theme-elevated hover:bg-theme-hover text-theme-text-primary disabled:opacity-60 transition-colors ${className}`
          }
          aria-label="Switch active namespaces"
        >
          {label && (
            <span className="shrink-0 font-normal text-theme-text-tertiary">{label}</span>
          )}
          {isClusterWide ? (
            <Globe className="w-3.5 h-3.5 shrink-0 text-theme-text-tertiary" />
          ) : restrictedHint ? (
            <AlertTriangle className="w-3.5 h-3.5 shrink-0 text-theme-text-tertiary" />
          ) : null}
          <span className={`font-medium truncate ${variant === 'segment' ? 'min-w-0' : 'max-w-[180px]'}`}>
            {pending ? 'Switching…' : triggerLabel}
          </span>
          <ChevronDown className="w-3 h-3 shrink-0 opacity-60" />
        </button>
      </Tooltip>

      {isOpen &&
        createPortal(
          <div
            ref={dropdownRef}
            style={{ position: 'fixed', top: pos.top, left: pos.left, minWidth: pos.width, zIndex: 100 }}
            className="bg-theme-surface border border-theme-border rounded-md shadow-theme-lg overflow-hidden"
          >
            {scope.cacheScoped ? (
              <>
                {items.length > 6 && (
                  <div className="flex items-center gap-2 px-2 py-1.5 border-b border-theme-border">
                    <Search className="w-3.5 h-3.5 text-theme-text-tertiary" />
                    <input
                      autoFocus
                      value={search}
                      onChange={e => setSearch(e.target.value)}
                      placeholder="Filter namespaces"
                      className="flex-1 bg-transparent text-sm outline-none text-theme-text-primary placeholder:text-theme-text-tertiary"
                    />
                  </div>
                )}

                <div className="px-3 py-1.5 border-b border-theme-border text-[11px] leading-snug text-theme-text-secondary">
                  Radar is watching one namespace to stay fast on large clusters.
                  {scope.namespaceRescope
                    ? ' Pick another to re-point it — takes a moment and closes open terminals.'
                    : ' This instance is locked to its startup namespace.'}
                </div>

                <ul className="max-h-80 overflow-y-auto py-1">
                  {filteredItems.length === 0 && (
                    <li className="px-3 py-2 text-xs text-theme-text-tertiary">
                      {search ? 'No matches.' : 'No namespaces available.'}
                    </li>
                  )}

                  {filteredItems.map(ns => {
                    const isChecked = draft.has(ns)
                    const isContextDefault = ns === scope.kubeconfigNamespace && ns !== ''
                    return (
                      <li key={ns}>
                        <label className="w-full flex items-center justify-between px-3 py-1.5 text-sm hover:bg-theme-hover text-left text-theme-text-primary cursor-pointer">
                          <span className="flex items-center gap-2 min-w-0">
                            <input
                              type="radio"
                              name="namespace-cache-scope"
                              checked={isChecked}
                              onChange={() => toggle(ns)}
                              className="shrink-0 accent-current"
                            />
                            <span className="truncate">{ns}</span>
                            {isContextDefault && (
                              <span className="text-[10px] uppercase tracking-wide text-theme-text-tertiary shrink-0">
                                kubeconfig
                              </span>
                            )}
                          </span>
                        </label>
                      </li>
                    )
                  })}
                </ul>

                <div className="flex items-center justify-between px-3 py-1.5 border-t border-theme-border text-[11px] text-theme-text-tertiary">
                  <span>{draft.size === 1 ? Array.from(draft)[0] : 'Select a namespace'}</span>
                  <button
                    onClick={closeAndApply}
                    className="px-2 py-0.5 rounded bg-theme-elevated hover:bg-theme-hover text-theme-text-primary"
                  >
                    Done
                  </button>
                </div>
              </>
            ) : (
              <MultiSelectPicker
                items={items}
                selected={draft}
                onSelectionChange={setDraft}
                onClearAll={clearAll}
                onDone={closeAndApply}
                search={search}
                onSearchChange={setSearch}
                searchPlaceholder="Filter namespaces"
                summaryEmptyLabel="All namespaces"
                noItemsLabel="No namespaces available."
                clearAllDisabled={!canClearAll || activeCount === 0}
                clearAllAriaLabel="Clear namespace selection"
                renderItemMeta={ns =>
                  ns === scope.kubeconfigNamespace && ns !== '' ? (
                    <span className="text-[10px] uppercase tracking-wide text-theme-text-tertiary shrink-0">
                      kubeconfig
                    </span>
                  ) : null
                }
              />
            )}

            {!scope.authoritative && (
              <div className="px-3 py-2 border-t border-theme-border text-[11px] status-degraded">
                Limited list — your RBAC doesn&rsquo;t allow listing all
                namespaces. Other namespaces may be accessible but won&rsquo;t
                appear here until you switch context.
              </div>
            )}
          </div>,
          document.body,
        )}
    </>
  )
})
