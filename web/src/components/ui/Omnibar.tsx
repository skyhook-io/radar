import { useState, useMemo, useRef, useEffect, useCallback, forwardRef, useImperativeHandle } from 'react'
import { Search, CornerDownLeft, Loader2 } from 'lucide-react'
import { clsx } from 'clsx'
import { getResourceIcon } from '../../utils/resource-icons'
import { useSearch, type SearchHit } from '../../api/client'
import { useCommandItems, bestScore, type CommandItem, type CommandItemCallbacks } from './command-items'

// Health → dot color (summaryContext.health is the same vocabulary as the rest
// of Radar). Kept local + tiny to avoid pulling the full status-tone system.
function healthDot(health?: string): string | null {
  switch (health) {
    case 'healthy': return 'bg-emerald-500'
    case 'degraded': return 'bg-amber-500'
    case 'unhealthy': return 'bg-red-500'
    case 'unknown': return 'bg-theme-text-tertiary'
    default: return null
  }
}

function useDebounced<T>(value: T, ms: number): T {
  const [v, setV] = useState(value)
  useEffect(() => {
    const t = setTimeout(() => setV(value), ms)
    return () => clearTimeout(t)
  }, [value, ms])
  return v
}

export interface OmnibarHandle {
  focus: () => void
}

interface OmnibarProps extends CommandItemCallbacks {
  /** Open a resource hit (route-based — sets the URL + opens the drawer). */
  onOpenResource: (hit: SearchHit) => void
}

type Row =
  | { id: string; kind: 'resource'; hit: SearchHit }
  | { id: string; kind: 'command'; command: CommandItem }

// The standalone omnibar: a persistent top-center search box that IS the ⌘K
// surface. Typing runs the live resource search (/api/search) alongside the
// command-palette items; resources lead, commands follow. ⌘K focuses it.
export const Omnibar = forwardRef<OmnibarHandle, OmnibarProps>(function Omnibar(
  { onOpenResource, ...callbacks },
  ref,
) {
  const [query, setQuery] = useState('')
  const [open, setOpen] = useState(false)
  const inputRef = useRef<HTMLInputElement>(null)
  const containerRef = useRef<HTMLDivElement>(null)
  const listRef = useRef<HTMLDivElement>(null)

  useImperativeHandle(ref, () => ({ focus: () => { inputRef.current?.focus(); inputRef.current?.select() } }), [])

  const trimmed = query.trim()
  const debounced = useDebounced(trimmed, 200)
  // The hits in `searchData` are for `debounced`; while the user keeps typing,
  // `debounced` lags `trimmed`. We never ACT on stale rows because selection +
  // Enter are keyed to the row id currently rendered (see selectedId), and the
  // row set is rebuilt from the current data each render.
  const { data: searchData, isFetching, isPlaceholderData } = useSearch(debounced, { enabled: open })

  const commandItems = useCommandItems(callbacks)

  // Matched commands: empty query → Views + Actions (the launcher default);
  // with a query → top client-ranked matches (kept small so resources lead).
  const matchedCommands = useMemo<CommandItem[]>(() => {
    if (!trimmed) return commandItems.filter((i) => i.category === 'Views' || i.category === 'Actions')
    return commandItems
      .map((item) => ({ item, score: bestScore(item, trimmed) + (item.category === 'Resource Kinds' && item.sublabel === 'core' ? 10 : 0) }))
      .filter(({ score }) => score > 0)
      .sort((a, b) => b.score - a.score)
      .slice(0, 8)
      .map(({ item }) => item)
  }, [commandItems, trimmed])

  const resourceRows = useMemo<Row[]>(() => {
    const hits = searchData?.hits ?? []
    return hits.map((hit) => ({ id: `res:${hit.kind}:${hit.group || ''}:${hit.namespace || ''}:${hit.name}`, kind: 'resource' as const, hit }))
  }, [searchData])

  // Ordered, id-stable list. Resources lead, but ONLY when the resource section
  // is actually rendered (trimmed >= 2) — so the keyboard model never contains a
  // row that isn't visible (e.g. stale hits lingering after shrinking to 1
  // char). render condition and `rows` use the same gate.
  const rows = useMemo<Row[]>(() => {
    const cmds: Row[] = matchedCommands.map((c) => ({ id: `cmd:${c.id}`, kind: 'command' as const, command: c }))
    return trimmed.length >= 2 ? [...resourceRows, ...cmds] : cmds
  }, [resourceRows, matchedCommands, trimmed])

  // Selection tracked by stable id (not array index) so Enter can never fire a
  // stale row when the set shifts. Behaviour: the selection auto-follows the TOP
  // result (so when async resource hits land above the commands, the leading
  // resource is selected — what the user expects after typing) UNTIL they
  // arrow-key, after which it sticks to its id. A new query re-enables
  // auto-follow.
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const userMovedRef = useRef(false)
  useEffect(() => { userMovedRef.current = false }, [trimmed])
  const rowsKey = rows.map((r) => r.id).join('|')
  useEffect(() => {
    setSelectedId((cur) => {
      if (!userMovedRef.current) return rows[0]?.id ?? null
      return cur && rows.some((r) => r.id === cur) ? cur : rows[0]?.id ?? null
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [rowsKey])
  const selectedIndex = rows.findIndex((r) => r.id === selectedId)
  const moveSelection = (delta: number) => {
    userMovedRef.current = true
    setSelectedId(rows[Math.min(Math.max(selectedIndex + delta, 0), rows.length - 1)]?.id ?? null)
  }
  const selectRow = (id: string) => { userMovedRef.current = true; setSelectedId(id) }

  const execute = useCallback((row: Row) => {
    if (row.kind === 'command') row.command.action()
    else onOpenResource(row.hit)
    setOpen(false)
    setQuery('')
    inputRef.current?.blur()
  }, [onOpenResource])

  // The resources shown don't (yet) belong to the current query: the debounce
  // hasn't fired, the data is React Query placeholder from a prior query, or
  // results for this query haven't landed. Swallow Enter so it can't open a
  // stale hit or a command standing in for an imminent resource.
  const resourcesStale = trimmed.length >= 2 && (debounced !== trimmed || isPlaceholderData || (resourceRows.length === 0 && isFetching))

  const handleKeyDown = useCallback((e: React.KeyboardEvent) => {
    if (e.key === 'Escape') { e.preventDefault(); setOpen(false); inputRef.current?.blur(); return }
    if (e.key === 'ArrowDown') { e.preventDefault(); moveSelection(1) }
    else if (e.key === 'ArrowUp') { e.preventDefault(); moveSelection(-1) }
    else if (e.key === 'Enter') { e.preventDefault(); if (resourcesStale) return; const row = rows[selectedIndex]; if (row) execute(row) }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [rows, selectedIndex, execute, resourcesStale])

  // Keep the selected row in view.
  useEffect(() => {
    listRef.current?.querySelector('[data-selected="true"]')?.scrollIntoView({ block: 'nearest' })
  }, [selectedId])

  // Close on outside click.
  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => { if (!containerRef.current?.contains(e.target as Node)) setOpen(false) }
    document.addEventListener('mousedown', onDown)
    return () => document.removeEventListener('mousedown', onDown)
  }, [open])

  const mac = typeof navigator !== 'undefined' && navigator.platform.includes('Mac')
  const total = searchData?.total ?? 0
  const totalMatched = searchData?.total_matched ?? 0
  const showResourceSection = trimmed.length >= 2
  const dropdownOpen = open && (rows.length > 0 || showResourceSection)

  return (
    <div ref={containerRef} className="relative w-full max-w-md">
      <div className="flex items-center gap-2 h-8 px-2.5 rounded-md bg-theme-elevated border border-transparent focus-within:border-theme-border focus-within:bg-theme-surface transition-colors">
        <Search className="w-3.5 h-3.5 shrink-0 text-theme-text-tertiary" />
        <input
          ref={inputRef}
          type="text"
          value={query}
          onChange={(e) => { setQuery(e.target.value); setOpen(true) }}
          onFocus={() => setOpen(true)}
          onKeyDown={handleKeyDown}
          placeholder="Search resources & commands…"
          aria-label="Search resources and commands"
          className="flex-1 min-w-0 bg-transparent text-sm text-theme-text-primary placeholder-theme-text-tertiary outline-none"
        />
        {!query && (
          <kbd className="shrink-0 text-[10px] text-theme-text-tertiary bg-theme-surface px-1 py-0.5 rounded border border-theme-border-light">
            {mac ? '⌘' : 'Ctrl+'}K
          </kbd>
        )}
      </div>

      {dropdownOpen && (
        <div className="absolute left-0 right-0 top-full mt-1.5 z-[90] dialog overflow-hidden">
          <div ref={listRef} className="max-h-[60vh] overflow-y-auto py-1">
            {/* Resources section */}
            {showResourceSection && (
              <>
                <div className="flex items-center justify-between px-3 py-1 text-[10px] font-semibold uppercase tracking-wider text-theme-text-tertiary">
                  <span>Resources</span>
                  {isFetching && <Loader2 className="w-3 h-3 animate-spin" />}
                  {!isFetching && totalMatched > total && <span className="normal-case font-normal">showing {total} of {totalMatched}</span>}
                </div>
                {resourceRows.length === 0 && !isFetching && (
                  <div className="px-3 py-2 text-xs text-theme-text-tertiary">No resources match “{trimmed}”.</div>
                )}
                {resourceRows.map((row) => row.kind === 'resource' && (
                  <ResourceRow key={row.id} hit={row.hit} selected={row.id === selectedId} onSelect={() => selectRow(row.id)} onActivate={() => execute(row)} />
                ))}
              </>
            )}

            {/* Commands section */}
            {matchedCommands.length > 0 && (
              <>
                <div className="px-3 py-1 mt-1 text-[10px] font-semibold uppercase tracking-wider text-theme-text-tertiary">{trimmed ? 'Commands' : 'Jump to'}</div>
                {rows.filter((r) => r.kind === 'command').map((row) => row.kind === 'command' && (
                  <CommandRow key={row.id} item={row.command} selected={row.id === selectedId} onSelect={() => selectRow(row.id)} onActivate={() => execute(row)} />
                ))}
              </>
            )}
          </div>
          <div className="flex items-center gap-3 px-3 py-1.5 border-t border-theme-border text-[11px] text-theme-text-tertiary">
            <span className="flex items-center gap-1"><CornerDownLeft className="w-3 h-3" /> open</span>
            <span>↑↓ navigate</span>
            <span>esc close</span>
          </div>
        </div>
      )}
    </div>
  )
})

function ResourceRow({ hit, selected, onSelect, onActivate }: { hit: SearchHit; selected: boolean; onSelect: () => void; onActivate: () => void }) {
  const Icon = getResourceIcon(hit.kind)
  const dot = healthDot(hit.summaryContext?.health)
  const issues = hit.summaryContext?.issueCount ?? 0
  // Lead is a name match; flag content-only matches so a name search isn't
  // silently padded with body hits.
  const contentOnly = !!hit.matched?.length && hit.matched.every((m) => m.site.startsWith('content:'))
  return (
    <button
      data-selected={selected}
      onClick={onActivate}
      onMouseMove={onSelect}
      className={clsx('w-full flex items-center gap-2.5 px-3 py-1.5 text-left transition-colors', selected ? 'selection' : 'hover:bg-theme-elevated/40')}
    >
      <Icon className="w-4 h-4 shrink-0 text-theme-text-tertiary" />
      <span className="text-sm text-theme-text-primary truncate">{hit.name}</span>
      {dot && <span className={clsx('h-1.5 w-1.5 rounded-full shrink-0', dot)} />}
      <span className="text-xs text-theme-text-tertiary truncate">{hit.kind}{hit.namespace ? ` · ${hit.namespace}` : ''}</span>
      {contentOnly && <span className="shrink-0 text-[10px] text-theme-text-tertiary italic">in spec</span>}
      {issues > 0 && <span className="ml-auto shrink-0 text-[10px] font-medium text-amber-600 dark:text-amber-400">{issues} issue{issues === 1 ? '' : 's'}</span>}
    </button>
  )
}

function CommandRow({ item, selected, onSelect, onActivate }: { item: CommandItem; selected: boolean; onSelect: () => void; onActivate: () => void }) {
  const Icon = item.icon
  return (
    <button
      data-selected={selected}
      onClick={onActivate}
      onMouseMove={onSelect}
      className={clsx('w-full flex items-center gap-2.5 px-3 py-1.5 text-left transition-colors', selected ? 'selection' : 'hover:bg-theme-elevated/40')}
    >
      {Icon ? <Icon className="w-4 h-4 shrink-0 text-theme-text-tertiary" /> : <span className="w-4 shrink-0" />}
      <span className="text-sm text-theme-text-primary truncate">{item.label}</span>
      {item.sublabel && <span className="text-xs text-theme-text-tertiary truncate">{item.sublabel}</span>}
      {item.shortcut && <kbd className="ml-auto shrink-0 text-[10px] text-theme-text-tertiary bg-theme-elevated px-1 py-0.5 rounded border border-theme-border-light">{item.shortcut}</kbd>}
    </button>
  )
}
