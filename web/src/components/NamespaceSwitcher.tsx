import { forwardRef, useCallback, useEffect, useImperativeHandle, useMemo, useRef, useState } from 'react'
import { createPortal } from 'react-dom'
import { ChevronDown, Globe, Search, AlertTriangle, X } from 'lucide-react'
import { useNamespaceScope, useSetActiveNamespace } from '../api/client'
import { Tooltip } from './ui/Tooltip'

export interface NamespaceSwitcherHandle {
  open: () => void
}

interface NamespaceSwitcherProps {
  className?: string
  disabled?: boolean
  disabledTooltip?: string
}

/**
 * NamespaceSwitcher is normally a per-user multi-select view filter. When the
 * backend reports cacheScoped=true, it becomes a single-namespace cache scope
 * control; local sessions may rebuild the cache for a different namespace.
 *
 * Three states reflect what the backend reports:
 *   - cluster-wide: empty trigger label "All namespaces", picker lets the
 *     user narrow the view; otherwise informational.
 *   - namespace:    label shows the namespace count (or single name); picker
 *     offers other accessible namespaces and a clear-all reset.
 *   - restricted:   user can't list namespaces and isn't pinned; picker
 *     surfaces only the kubeconfig context's namespace + any saved picks.
 *
 * Selection model: the dropdown keeps a draft Set<string>; toggling rows
 * mutates the draft locally; closing the dropdown applies the draft in a
 * single mutation. "Clear all" applies immediately and closes; "Select all
 * visible" / "Clear visible" mutate the draft only and wait for close.
 */
export const NamespaceSwitcher = forwardRef<NamespaceSwitcherHandle, NamespaceSwitcherProps>(function NamespaceSwitcher(
  { className = '', disabled = false, disabledTooltip },
  ref,
) {
  const { data: scope, isLoading } = useNamespaceScope()
  const setActive = useSetActiveNamespace()

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
    setActive.mutate({ namespaces: nextArr })
  }, [activesKey, scope, setActive])

  const closeAndApply = useCallback(() => {
    setIsOpen(false)
    setSearch('')
    applySelection(draft)
  }, [applySelection, draft])

  useImperativeHandle(ref, () => ({
    open: () => {
      if (disabled || isLoading || setActive.isPending) return
      setIsOpen(true)
    },
  }), [disabled, isLoading, setActive.isPending])

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

  const selectAllVisible = () => {
    const next = new Set(draft)
    for (const ns of filteredItems) next.add(ns)
    setDraft(next)
  }

  const clearVisible = () => {
    const next = new Set(draft)
    for (const ns of filteredItems) next.delete(ns)
    setDraft(next)
  }

  const activeCount = scopeActives.length
  const triggerLabel =
    activeCount === 0 ? 'All namespaces' : activeCount === 1 ? scopeActives[0] : `${activeCount} namespaces`
  const isClusterWide = activeCount === 0
  const restrictedHint = scope.mode === 'restricted'
  const cacheScopeLocked = scope.cacheScoped && !scope.namespaceRescope
  const isDisabled = disabled || isLoading || setActive.isPending || cacheScopeLocked
  const canClearAll = scope.canClearNamespace || activeCount === 0
  const tooltipContent = disabled && disabledTooltip
    ? disabledTooltip
    : scope.cacheScoped
      ? scope.namespaceRescope
        ? 'Cache is scoped to one namespace. Switching rebuilds the local cache.'
        : `Cache is scoped to namespace ${scope.cacheScopeNamespace || triggerLabel}.`
      : restrictedHint
      ? 'Limited namespace visibility — only namespaces granted by your RBAC are shown.'
      : isClusterWide
        ? 'Currently viewing all namespaces. Click to narrow the view.'
        : activeCount === 1
          ? `View is filtered to namespace ${scopeActives[0]}. Click to switch or reset.`
          : `View is filtered to ${activeCount} namespaces. Click to adjust or reset.`

  // Counts used to label the bulk-action buttons; computed against the visible
  // (filtered) set so the labels match what the action will affect.
  const visibleSelectedCount = filteredItems.reduce((n, ns) => n + (draft.has(ns) ? 1 : 0), 0)
  const allVisibleSelected = filteredItems.length > 0 && visibleSelectedCount === filteredItems.length

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
          className={`flex items-center gap-1.5 px-2 py-1 rounded text-sm bg-theme-elevated hover:bg-theme-hover text-theme-text-primary disabled:opacity-60 transition-colors ${className}`}
          aria-label="Switch active namespaces"
        >
          {isClusterWide ? (
            <Globe className="w-3.5 h-3.5 text-theme-text-tertiary" />
          ) : restrictedHint ? (
            <AlertTriangle className="w-3.5 h-3.5 text-theme-text-tertiary" />
          ) : null}
          <span className="font-medium max-w-[180px] truncate">
            {setActive.isPending ? 'Switching…' : triggerLabel}
          </span>
          <ChevronDown className="w-3 h-3 opacity-60" />
        </button>
      </Tooltip>

      {isOpen &&
        createPortal(
          <div
            ref={dropdownRef}
            style={{ position: 'fixed', top: pos.top, left: pos.left, minWidth: pos.width, zIndex: 100 }}
            className="bg-theme-surface border border-theme-border rounded-md shadow-theme-lg overflow-hidden"
          >
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

            {scope.cacheScoped ? (
              <div className="px-3 py-1.5 border-b border-theme-border text-xs text-theme-text-secondary">
                Cache scope
              </div>
            ) : (
              <div className="flex items-center justify-between px-2 py-1.5 border-b border-theme-border text-xs text-theme-text-secondary">
                <button
                  onClick={canClearAll ? clearAll : undefined}
                  disabled={!canClearAll || activeCount === 0}
                  className="flex items-center gap-1 px-1.5 py-0.5 rounded hover:bg-theme-hover disabled:opacity-50 disabled:hover:bg-transparent"
                  aria-label="Clear namespace selection"
                >
                  <X className="w-3 h-3" />
                  Clear all
                </button>
                <button
                  onClick={allVisibleSelected ? clearVisible : selectAllVisible}
                  disabled={filteredItems.length === 0}
                  className="px-1.5 py-0.5 rounded hover:bg-theme-hover disabled:opacity-50 disabled:hover:bg-transparent"
                >
                  {allVisibleSelected
                    ? `Clear ${filteredItems.length} visible`
                    : search.trim()
                      ? `Select ${filteredItems.length} visible`
                      : 'Select all'}
                </button>
              </div>
            )}

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
                    <label
                      className="w-full flex items-center justify-between px-3 py-1.5 text-sm hover:bg-theme-hover text-left text-theme-text-primary cursor-pointer"
                    >
                      <span className="flex items-center gap-2 min-w-0">
                        <input
                          type={scope.cacheScoped ? 'radio' : 'checkbox'}
                          name={scope.cacheScoped ? 'namespace-cache-scope' : undefined}
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
              <span>
                {scope.cacheScoped
                  ? (draft.size === 1 ? Array.from(draft)[0] : 'Select a namespace')
                  : draft.size === 0 ? 'All namespaces' : `${draft.size} selected`}
              </span>
              <button
                onClick={closeAndApply}
                className="px-2 py-0.5 rounded bg-theme-elevated hover:bg-theme-hover text-theme-text-primary"
              >
                Done
              </button>
            </div>

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
