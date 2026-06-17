import { useState, useMemo, useRef, useEffect, useCallback, forwardRef, useImperativeHandle } from 'react'
import { createPortal } from 'react-dom'
import { Search, CornerDownLeft, Loader2, AlertTriangle } from 'lucide-react'
import { clsx } from 'clsx'
import { SearchPillInput, type SearchModifier } from '@skyhook-io/k8s-ui'
import { getResourceIcon } from '../../utils/resource-icons'
import { useSearch, useNamespaceScope, useContexts, type SearchHit, type SearchMatchedField } from '../../api/client'
import { useAPIResources } from '../../api/apiResources'
import { loadRecentResources, recordRecentResource } from '../../hooks/useRecentResources'
import { useCommandItems, bestScore, type CommandItem, type CommandItemCallbacks } from './command-items'
import { SearchSyntaxHelp } from './SearchSyntaxHelp'

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

function escapeRe(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')
}

// Wrap matched substrings in a brand-tinted, bold run so the user can see WHY a
// result matched — including when the match is on the namespace/kind, not the
// name. Longest tokens first so "staging" wins over a stray "s".
function highlight(text: string, tokens: string[]): React.ReactNode {
  const toks = [...new Set(tokens.filter(Boolean))].sort((a, b) => b.length - a.length)
  if (!toks.length || !text) return text
  const re = new RegExp(`(${toks.map(escapeRe).join('|')})`, 'ig')
  const parts: React.ReactNode[] = []
  let last = 0
  for (const m of text.matchAll(re)) {
    const i = m.index ?? 0
    if (i > last) parts.push(text.slice(last, i))
    parts.push(<mark key={i} className="bg-transparent font-semibold text-[var(--color-brand)]">{m[0]}</mark>)
    last = i + m[0].length
  }
  if (!parts.length) return text
  if (last < text.length) parts.push(text.slice(last))
  return parts
}

// The query tokens that the search engine recorded as landing on a given field
// (site), so each displayed field highlights only what actually matched it.
function tokensForSite(matched: SearchMatchedField[] | undefined, ...sites: string[]): string[] {
  if (!matched) return []
  return matched.filter((m) => sites.includes(m.site)).map((m) => m.token)
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
  | { id: string; kind: 'resource'; hit: SearchHit; recent?: boolean }
  | { id: string; kind: 'command'; command: CommandItem }

const COMMAND_CATEGORY_ORDER = ['Views', 'Resource Kinds', 'Namespaces', 'Clusters', 'Actions']
const PAGE = 8
const STRONG_KIND = 100 // exact (150) or prefix (100) kind-name match

function pillsToQuery(pills: SearchModifier[]): string {
  return pills.map((p) => `${p.key}:${p.value}`).join(' ')
}

// The standalone omnibar: a persistent top-center search box that IS the ⌘K
// surface. Typing runs the live, GLOBAL resource search (/api/search) alongside
// the command-palette items; modifiers (ns:, kind:, …) become removable pills.
// Resources lead, commands follow. ⌘K focuses it.
export const Omnibar = forwardRef<OmnibarHandle, OmnibarProps>(function Omnibar(
  { onOpenResource, ...callbacks },
  ref,
) {
  const [text, setText] = useState('')
  const [pills, setPills] = useState<SearchModifier[]>([])
  const [open, setOpen] = useState(false)
  const [suggesting, setSuggesting] = useState(false)
  const inputRef = useRef<HTMLInputElement>(null)
  const containerRef = useRef<HTMLDivElement>(null)
  const panelRef = useRef<HTMLDivElement>(null)
  const listRef = useRef<HTMLDivElement>(null)
  // The dropdown is portaled to <body> (so the header's stacking context can't
  // trap the dim overlay). `centerX` aligns the panel under the input; `top` is
  // the HEADER's bottom (not the input's) so the dim starts cleanly below the
  // whole top bar — the input is shorter than the bar, so anchoring to it would
  // slice the dim through the taller right-side controls.
  const [anchor, setAnchor] = useState<{ centerX: number; top: number } | null>(null)

  useImperativeHandle(ref, () => ({ focus: () => { inputRef.current?.focus(); inputRef.current?.select() } }), [])

  const { data: nsScope } = useNamespaceScope()
  const { data: apiResources } = useAPIResources()
  const { data: contexts } = useContexts()
  // Recents are partitioned by the current cluster so a context switch never
  // surfaces (or opens) the previous cluster's resources.
  const contextKey = useMemo(() => contexts?.find((c) => c.isCurrent)?.name ?? '', [contexts])
  // ns + kind are the bounded, knowable modifier value sets worth autocompleting.
  const modifierOptions = useMemo(() => ({
    ns: nsScope?.accessibleNamespaces ?? [],
    kind: apiResources ? [...new Set(apiResources.filter((r) => r.verbs?.includes('list')).map((r) => r.kind))].sort() : [],
  }), [nsScope, apiResources])

  // Reflect the current view scope as an editable `ns:` pill on open, so a
  // deliberately broad ⌘K search shows (and lets you remove) the namespace it's
  // narrowed to instead of silently scoping. Seeded once per open, only from a
  // truly empty launcher state.
  const actives = nsScope?.actives
  const seededRef = useRef(false)
  useEffect(() => {
    if (!open) { seededRef.current = false; return }
    if (seededRef.current || actives === undefined) return
    seededRef.current = true
    if (pills.length === 0 && text === '' && actives.length > 0) {
      setPills(actives.map((ns) => ({ key: 'ns', value: ns })))
    }
  }, [open, actives, pills.length, text])

  const freeText = text.trim()
  const queryString = useMemo(() => [pillsToQuery(pills), freeText].filter(Boolean).join(' '), [pills, freeText])
  const searchActive = queryString.length >= 2
  // Small debounce: /api/search is a local in-memory index, so this exists only
  // to coalesce fast keystrokes (less list reshuffle), not to cut network cost —
  // kept under the ~100-150ms "feels instant" threshold. keepPreviousData +
  // AbortSignal (see useSearch) handle the smoothness; commands aren't debounced.
  const debounced = useDebounced(queryString, 120)
  // globalNs: ⌘K searches the user's full RBAC ceiling; scope comes only from
  // `ns:` pills, never the silent view filter.
  const { data: searchData, isFetching, isPlaceholderData, isError } = useSearch(debounced, { enabled: open, globalNs: true })

  const commandItems = useCommandItems(callbacks)

  // Commands score against the FREE text only — modifiers live in pills, so the
  // launcher never sees "ns:" polluting a "go to topology" match. Empty + no
  // pills → Views + Actions (launcher default); with pills but no text the user
  // is browsing a scope, so suppress the command default.
  const scoredCommands = useMemo(() => {
    if (!freeText) {
      return pills.length ? [] : commandItems.filter((i) => i.category === 'Views' || i.category === 'Actions').map((item) => ({ item, score: 1 }))
    }
    return commandItems.map((item) => ({ item, score: bestScore(item, freeText) })).filter((x) => x.score > 0).sort((a, b) => b.score - a.score)
  }, [commandItems, freeText, pills.length])

  // Kinds whose NAME strongly matches (exact 150 / prefix 100) lead ABOVE the
  // resource instances: "⌘K → deployment → Deployments list" is a navigation
  // flow the instance hits otherwise bury.
  const leadingKinds = useMemo<CommandItem[]>(
    () => (freeText.length < 2 ? [] : scoredCommands.filter((x) => x.item.category === 'Resource Kinds' && x.score >= STRONG_KIND).slice(0, 5).map((x) => x.item)),
    [scoredCommands, freeText],
  )
  const leadingIds = useMemo(() => new Set(leadingKinds.map((i) => i.id)), [leadingKinds])

  const resourceRows = useMemo<Row[]>(() => {
    const hits = searchData?.hits ?? []
    return hits.map((hit) => ({ id: `res:${hit.kind}:${hit.group || ''}:${hit.namespace || ''}:${hit.name}`, kind: 'resource' as const, hit }))
  }, [searchData])

  // Launcher recents: only in the truly-empty state (no text, no pills). Read
  // fresh from localStorage each open.
  const recentRows = useMemo<Row[]>(() => {
    if (!open || freeText || pills.length > 0) return []
    return loadRecentResources(contextKey).map((r) => ({
      id: `recent:${r.kind}:${r.group || ''}:${r.namespace || ''}:${r.name}`,
      kind: 'resource' as const,
      recent: true,
      hit: { score: 0, kind: r.kind, group: r.group, namespace: r.namespace, name: r.name } as SearchHit,
    }))
  }, [open, freeText, pills.length, contextKey])

  // Remaining matched commands (leading kinds removed so they don't repeat),
  // grouped by their real category in a fixed order.
  const commandGroups = useMemo(() => {
    const rest = scoredCommands.filter((x) => !leadingIds.has(x.item.id)).slice(0, 8).map((x) => x.item)
    const byCat = new Map<string, CommandItem[]>()
    for (const c of rest) { const l = byCat.get(c.category) ?? []; l.push(c); byCat.set(c.category, l) }
    return COMMAND_CATEGORY_ORDER.filter((cat) => byCat.has(cat)).map((cat) => ({ category: cat, items: byCat.get(cat)! }))
  }, [scoredCommands, leadingIds])

  const toCmdRow = (c: CommandItem): Row => ({ id: `cmd:${c.id}`, kind: 'command', command: c })

  // Free-text tokens for highlighting command labels (commands are scored
  // client-side, so there's no server `matched`).
  const queryTokens = useMemo(() => freeText.split(/\s+/).filter(Boolean), [freeText])

  // Ordered, id-stable list (render order == keyboard model): recents (launcher
  // only), then leading kinds, then resources (when searchActive), then commands.
  const rows = useMemo<Row[]>(() => {
    const cmds: Row[] = commandGroups.flatMap((g) => g.items.map(toCmdRow))
    if (!freeText && pills.length === 0) return [...recentRows, ...cmds]
    return [...leadingKinds.map(toCmdRow), ...(searchActive ? resourceRows : []), ...cmds]
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [recentRows, leadingKinds, resourceRows, commandGroups, freeText, pills.length, searchActive])

  // Selection tracked by stable id (not array index) so Enter can never fire a
  // stale row when the set shifts. Auto-follows the TOP result until the user
  // arrow-keys; a new query re-enables auto-follow.
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const userMovedRef = useRef(false)
  useEffect(() => { userMovedRef.current = false }, [queryString])
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
  // Page by a full screenful of visible rows (minus one for context overlap),
  // measured from the scroll container — a fixed count feels short on tall lists.
  const pageStep = () => {
    const list = listRef.current
    const rowH = (list?.querySelector('button') as HTMLElement | null)?.offsetHeight
    if (!list || !rowH) return PAGE
    return Math.max(1, Math.floor(list.clientHeight / rowH) - 1)
  }

  const execute = useCallback((row: Row) => {
    if (row.kind === 'command') {
      row.command.action()
    } else {
      const h = row.hit
      recordRecentResource({ kind: h.kind, group: h.group, namespace: h.namespace, name: h.name }, contextKey)
      onOpenResource(h)
    }
    setOpen(false)
    setText('')
    setPills([])
    inputRef.current?.blur()
  }, [onOpenResource, contextKey])

  // The resources shown don't (yet) belong to the current query: the debounce
  // hasn't fired, the data is React Query placeholder from a prior query, or
  // results for this query haven't landed. Swallow Enter so it can't open a
  // stale hit or a command standing in for an imminent resource.
  const resourcesStale = searchActive && (debounced !== queryString || isPlaceholderData || (resourceRows.length === 0 && isFetching))

  // Forwarded from SearchPillInput for keys it doesn't consume (it owns Space →
  // pill, Backspace → pop pill, and suggestion nav).
  const handleKeyDown = useCallback((e: React.KeyboardEvent) => {
    if (e.key === 'Escape') { e.preventDefault(); setOpen(false); inputRef.current?.blur(); return }
    if (e.key === 'ArrowDown') { e.preventDefault(); moveSelection(1) }
    else if (e.key === 'ArrowUp') { e.preventDefault(); moveSelection(-1) }
    else if (e.key === 'PageDown') { e.preventDefault(); moveSelection(pageStep()) }
    else if (e.key === 'PageUp') { e.preventDefault(); moveSelection(-pageStep()) }
    // Home/End deliberately left native so they move the text caret, not the list.
    else if (e.key === 'Enter') {
      e.preventDefault()
      const row = rows[selectedIndex]
      if (!row) return
      // Block Enter only for a stale RESOURCE row (could open a hidden/stale
      // hit); commands and ready resources fire immediately.
      if (row.kind === 'resource' && resourcesStale) return
      execute(row)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [rows, selectedIndex, execute, resourcesStale])

  // Keep the selected row in view.
  useEffect(() => {
    listRef.current?.querySelector('[data-selected="true"]')?.scrollIntoView({ block: 'nearest' })
  }, [selectedId])

  // Close on outside click — the panel is portaled out of the container, so it
  // must be excluded explicitly or clicking a row would count as "outside".
  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      const t = e.target as Node
      if (!containerRef.current?.contains(t) && !panelRef.current?.contains(t)) setOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    return () => document.removeEventListener('mousedown', onDown)
  }, [open])

  // Track the input's position so the portaled panel stays anchored under it
  // through scroll / resize / layout shifts.
  useEffect(() => {
    if (!open) { setAnchor(null); return }
    const update = () => {
      const el = containerRef.current
      if (!el) return
      const r = el.getBoundingClientRect()
      const header = el.closest('header')
      setAnchor({ centerX: r.left + r.width / 2, top: header ? header.getBoundingClientRect().bottom : r.bottom })
    }
    update()
    window.addEventListener('resize', update)
    window.addEventListener('scroll', update, true)
    return () => { window.removeEventListener('resize', update); window.removeEventListener('scroll', update, true) }
  }, [open])

  const mac = typeof navigator !== 'undefined' && navigator.platform.includes('Mac')
  const total = searchData?.total ?? 0
  const totalMatched = searchData?.total_matched ?? 0
  const hasNsPill = pills.some((p) => p.key === 'ns')
  const dropdownOpen = open && !suggesting && (rows.length > 0 || searchActive)

  const clearNsPills = () => { setPills((prev) => prev.filter((p) => p.key !== 'ns')); inputRef.current?.focus() }

  return (
    <div ref={containerRef} className="relative w-full max-w-xl">
      <SearchPillInput
        className="min-h-8 px-2.5 rounded-md bg-theme-elevated border border-transparent focus-within:border-theme-border focus-within:bg-theme-surface transition-colors"
        text={text}
        pills={pills}
        onChange={({ text: t, pills: p }) => { setText(t); setPills(p); setOpen(true) }}
        onKeyDown={handleKeyDown}
        onFocus={() => setOpen(true)}
        onSuggestingChange={setSuggesting}
        modifierOptions={modifierOptions}
        placeholder="Search resources & commands…"
        aria-label="Search resources and commands"
        inputRef={inputRef}
        leftSlot={<Search className="w-3.5 h-3.5 shrink-0 text-theme-text-tertiary" />}
        rightSlot={
          <div className="flex items-center gap-1.5 shrink-0">
            <SearchSyntaxHelp />
            {!text && pills.length === 0 && (
              <kbd className="text-[10px] text-theme-text-tertiary bg-theme-surface px-1 py-0.5 rounded border border-theme-border-light">
                {mac ? '⌘' : 'Ctrl+'}K
              </kbd>
            )}
          </div>
        }
      />

      {open && anchor && (dropdownOpen || suggesting) && createPortal(
        <>
          {/* Dim + blur the busy dashboard behind so results read as a focused
              search surface (Spotlight/Linear pattern), not a weak float. Starts
              at the header's bottom edge so the search box + top bar stay crisp.
              Tied to `open`, NOT the results panel, so completing a modifier (the
              panel briefly yields to the autocomplete) doesn't strobe the dim. */}
          <div
            className="fixed left-0 right-0 bottom-0 z-[120] bg-black/25 dark:bg-black/55 backdrop-blur-[2px]"
            style={{ top: anchor.top }}
            onClick={() => { setOpen(false); inputRef.current?.blur() }}
          />
          {dropdownOpen && (
          <div
            ref={panelRef}
            style={{ position: 'fixed', top: anchor.top + 8, left: anchor.centerX, transform: 'translateX(-50%)', width: 640, maxWidth: 'calc(100vw - 2rem)' }}
            className="z-[121] dialog shadow-theme-lg overflow-hidden"
          >
          <div ref={listRef} className="max-h-[60vh] overflow-y-auto py-1">
            {/* Recently viewed — launcher state only. */}
            {recentRows.length > 0 && (
              <div>
                <div className="px-3 py-1 text-[10px] font-semibold uppercase tracking-wider text-theme-text-tertiary">Recently viewed</div>
                {recentRows.map((row) => row.kind === 'resource' && (
                  <ResourceRow key={row.id} hit={row.hit} selected={row.id === selectedId} onSelect={() => selectRow(row.id)} onActivate={() => execute(row)} />
                ))}
              </div>
            )}

            {/* Leading kinds — strong kind-name matches lead so ⌘K navigation
                to a kind isn't buried under instance hits. */}
            {leadingKinds.length > 0 && (
              <div>
                <div className="px-3 py-1 text-[10px] font-semibold uppercase tracking-wider text-theme-text-tertiary">Resource Kinds</div>
                {leadingKinds.map((item) => {
                  const id = `cmd:${item.id}`
                  return <CommandRow key={id} item={item} tokens={queryTokens} selected={id === selectedId} onSelect={() => selectRow(id)} onActivate={() => execute(toCmdRow(item))} />
                })}
              </div>
            )}

            {/* Resources section */}
            {searchActive && (
              <>
                <div className="flex items-center justify-between px-3 py-1 text-[10px] font-semibold uppercase tracking-wider text-theme-text-tertiary">
                  <span>Resources</span>
                  {isFetching && <Loader2 className="w-3 h-3 animate-spin" />}
                  {!isFetching && !isError && totalMatched > total && <span className="normal-case font-normal">showing {total} of {totalMatched}</span>}
                </div>
                {isError ? (
                  <div className="flex items-center gap-2 px-3 py-2 text-xs text-amber-600 dark:text-amber-400">
                    <AlertTriangle className="w-3.5 h-3.5 shrink-0" /> Search is unavailable right now.
                  </div>
                ) : resourceRows.length === 0 && !isFetching ? (
                  <div className="px-3 py-2 text-xs text-theme-text-tertiary">
                    No resources match{freeText ? <> “{freeText}”</> : ''}.
                    {hasNsPill && (
                      <button onMouseDown={(e) => { e.preventDefault(); clearNsPills() }} className="ml-1.5 text-[var(--color-brand)] hover:underline">
                        Search all namespaces
                      </button>
                    )}
                  </div>
                ) : (
                  resourceRows.map((row) => row.kind === 'resource' && (
                    // Mirror the Enter guard: ignore clicks on stale rows (prior
                    // query's results during debounce/placeholder) so a click can't
                    // open/record the wrong resource. Dim them so it reads as pending.
                    <ResourceRow key={row.id} hit={row.hit} stale={resourcesStale} selected={row.id === selectedId} onSelect={() => selectRow(row.id)} onActivate={() => { if (!resourcesStale) execute(row) }} />
                  ))
                )}
              </>
            )}

            {/* Command groups, each under its real category header. */}
            {commandGroups.map((group) => (
              <div key={group.category}>
                <div className="px-3 py-1 mt-1 text-[10px] font-semibold uppercase tracking-wider text-theme-text-tertiary">{group.category}</div>
                {group.items.map((item) => {
                  const id = `cmd:${item.id}`
                  return <CommandRow key={id} item={item} tokens={queryTokens} selected={id === selectedId} onSelect={() => selectRow(id)} onActivate={() => execute({ id, kind: 'command', command: item })} />
                })}
              </div>
            ))}
          </div>
          <div className="flex items-center gap-3 px-3 py-1.5 border-t border-theme-border text-[11px] text-theme-text-tertiary">
            <span className="flex items-center gap-1"><CornerDownLeft className="w-3 h-3" /> open</span>
            <span>↑↓ navigate</span>
            <span>⇞⇟ page</span>
            <span>esc close</span>
          </div>
          </div>
          )}
        </>,
        document.body,
      )}
    </div>
  )
})

function ResourceRow({ hit, selected, stale, onSelect, onActivate }: { hit: SearchHit; selected: boolean; stale?: boolean; onSelect: () => void; onActivate: () => void }) {
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
      className={clsx('w-full flex items-center gap-2.5 px-3 py-1.5 text-left transition-colors', selected ? 'selection' : 'hover:bg-theme-elevated/40', stale && 'opacity-50')}
    >
      <Icon className="w-4 h-4 shrink-0 text-theme-text-tertiary" />
      <span className="min-w-0 truncate text-sm text-theme-text-primary">{highlight(hit.name, tokensForSite(hit.matched, 'name'))}</span>
      {dot && <span className={clsx('h-1.5 w-1.5 rounded-full shrink-0', dot)} />}
      <span className="shrink-0 max-w-[45%] truncate text-xs text-theme-text-tertiary">
        {highlight(hit.kind, tokensForSite(hit.matched, 'kind'))}
        {hit.namespace ? <> · {highlight(hit.namespace, tokensForSite(hit.matched, 'namespace'))}</> : ''}
      </span>
      {contentOnly && <span className="shrink-0 text-[10px] text-theme-text-tertiary italic">in spec</span>}
      {issues > 0 && <span className="ml-auto shrink-0 text-[10px] font-medium text-amber-600 dark:text-amber-400">{issues} issue{issues === 1 ? '' : 's'}</span>}
    </button>
  )
}

function CommandRow({ item, tokens, selected, onSelect, onActivate }: { item: CommandItem; tokens: string[]; selected: boolean; onSelect: () => void; onActivate: () => void }) {
  const Icon = item.icon
  return (
    <button
      data-selected={selected}
      onClick={onActivate}
      onMouseMove={onSelect}
      className={clsx('w-full flex items-center gap-2.5 px-3 py-1.5 text-left transition-colors', selected ? 'selection' : 'hover:bg-theme-elevated/40')}
    >
      {Icon ? <Icon className="w-4 h-4 shrink-0 text-theme-text-tertiary" /> : <span className="w-4 shrink-0" />}
      <span className="min-w-0 truncate text-sm text-theme-text-primary">{highlight(item.label, tokens)}</span>
      {item.sublabel && <span className="shrink-0 max-w-[45%] truncate text-xs text-theme-text-tertiary">{highlight(item.sublabel, tokens)}</span>}
      {item.shortcut && <kbd className="ml-auto shrink-0 text-[10px] text-theme-text-tertiary bg-theme-elevated px-1 py-0.5 rounded border border-theme-border-light">{item.shortcut}</kbd>}
    </button>
  )
}
