import { useState, useMemo, useRef, useEffect, useCallback, forwardRef, useImperativeHandle } from 'react'
import { createPortal } from 'react-dom'
import { Search, CornerDownLeft, Loader2, AlertTriangle } from 'lucide-react'
import { clsx } from 'clsx'
import { SearchPillInput, type SearchModifier } from '@skyhook-io/k8s-ui'
import { getResourceIcon } from '../../utils/resource-icons'
import type { SearchHit, SearchMatchedField } from '../../api/client'
import { bestScore, type CommandItem } from './command-items'
import { SearchSyntaxHelp } from './SearchSyntaxHelp'

// Minimal recent-resource shape the omnibar renders. Hosts own the storage +
// per-cluster partitioning behind loadRecents/recordRecent.
export interface OmnibarRecent {
  kind: string
  group?: string
  namespace?: string
  name: string
  cluster?: string
  clusterName?: string
}

// Search results the host feeds in (it runs its own search hook keyed on the
// debounced query the omnibar emits via onQueryChange).
export interface OmnibarSearchResult {
  hits: SearchHit[]
  total?: number
  total_matched?: number
}

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

export interface OmnibarProps {
  /** Open a resource hit (route-based — sets the URL + opens the drawer/page). */
  onOpenResource: (hit: SearchHit) => void
  /** Command-palette items, already built by the host (Views/Actions/Clusters/…).
   *  Scored + grouped internally; the host doesn't rank them. */
  commandItems: CommandItem[]
  /** The host runs its own search keyed on this debounced query; `open` lets it
   *  gate the request. */
  onQueryChange?: (query: string, open: boolean) => void
  /** Live search results for the current query (host-provided). */
  searchData?: OmnibarSearchResult
  isFetching?: boolean
  isError?: boolean
  /** True while React Query serves a previous query's data — gates Enter/clicks. */
  isPlaceholderData?: boolean
  /** Bounded modifier value sets to autocomplete (e.g. { ns: [...], kind: [...] }). */
  modifierOptions?: Record<string, string[]>
  /** Namespaces to seed as removable `ns:` pills when the launcher opens empty
   *  (reflects the current view scope). */
  seedNamespaces?: string[]
  /** Recently-viewed resources for the empty launcher (host owns storage). */
  loadRecents?: () => OmnibarRecent[]
  recordRecent?: (r: OmnibarRecent) => void
  /** When set, a "See all N results" row appears below the resource hits while
   *  searching, handing the full (uncapped) query off to the host's dedicated
   *  search surface. Omit to keep the omnibar a pure launcher. */
  onViewAllResults?: (query: string) => void
  /** Empty-launcher content. `true` (default) lists Views + Actions — the
   *  cmd-K menu. `false` shows only Actions so recents/search lead and views
   *  surface on type (use when the views are already always-visible, e.g. a
   *  persistent nav rail). */
  launcherShowsViews?: boolean
  placeholder?: string
  /** `hero` renders a large, centered field for landing surfaces (Home);
   *  `default` is the slim top-bar field. */
  size?: 'default' | 'hero'
  /** Focus the field on mount (Home hero — the primary action on the page). */
  autoFocus?: boolean
}

type Row =
  | { id: string; kind: 'resource'; hit: SearchHit; recent?: boolean }
  | { id: string; kind: 'command'; command: CommandItem }
  | { id: string; kind: 'viewAll'; query: string; count: number }

const COMMAND_CATEGORY_ORDER = ['Views', 'Resource Kinds', 'Namespaces', 'Clusters', 'Actions']
const PAGE = 8
const STRONG_KIND = 100 // exact (150) or prefix (100) kind-name match

function pillsToQuery(pills: SearchModifier[]): string {
  return pills.map((p) => `${p.key}:${p.value}`).join(' ')
}

// The omnibar: a persistent search box that IS the ⌘K surface. Typing runs the
// host's live resource search alongside its command-palette items; modifiers
// (ns:, kind:, …) become removable pills. Resources lead, commands follow.
//
// Injectable: all data — search, commands, recents, modifier options — flows in
// via props, so Radar standalone (cluster /api/search) and Radar Hub (fleet
// search) share the same UX. ⌘K focus is wired by the host.
export const Omnibar = forwardRef<OmnibarHandle, OmnibarProps>(function Omnibar(
  {
    onOpenResource,
    commandItems,
    onQueryChange,
    searchData,
    isFetching = false,
    isError = false,
    isPlaceholderData = false,
    modifierOptions,
    seedNamespaces,
    loadRecents,
    recordRecent,
    onViewAllResults,
    launcherShowsViews = true,
    placeholder = 'Search resources & commands…',
    size = 'default',
    autoFocus = false,
  },
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
  // whole top bar.
  const [anchor, setAnchor] = useState<{ centerX: number; top: number; width: number } | null>(null)

  useImperativeHandle(ref, () => ({ focus: () => { inputRef.current?.focus(); inputRef.current?.select() } }), [])

  // Hero autofocus parks the cursor in the field on mount (Home's primary
  // action) but must NOT pop the dropdown — landing on the page shouldn't dim
  // it behind a command palette. Suppress exactly the programmatic focus; a
  // later user focus opens normally.
  const skipFocusOpen = useRef(false)
  useEffect(() => { if (autoFocus) { skipFocusOpen.current = true; inputRef.current?.focus() } }, [autoFocus])

  // Reflect the current view scope as an editable `ns:` pill on open, so a
  // deliberately broad ⌘K search shows (and lets you remove) the namespace it's
  // narrowed to. Seeded once per open, only from a truly empty launcher state.
  const seededRef = useRef(false)
  useEffect(() => {
    if (!open) { seededRef.current = false; return }
    if (seededRef.current || seedNamespaces === undefined) return
    seededRef.current = true
    if (pills.length === 0 && text === '' && seedNamespaces.length > 0) {
      setPills(seedNamespaces.map((ns) => ({ key: 'ns', value: ns })))
    }
  }, [open, seedNamespaces, pills.length, text])

  const freeText = text.trim()
  const queryString = useMemo(() => [pillsToQuery(pills), freeText].filter(Boolean).join(' '), [pills, freeText])
  const searchActive = queryString.length >= 2
  // Small debounce: coalesce fast keystrokes (less list reshuffle). The host's
  // search hook handles smoothness (keepPreviousData + AbortSignal).
  const debounced = useDebounced(queryString, 120)

  // Tell the host which (debounced) query to search, and whether the surface is
  // open (so it can gate the request).
  useEffect(() => { onQueryChange?.(debounced, open) }, [debounced, open, onQueryChange])

  // Commands score against the FREE text only — modifiers live in pills, so the
  // launcher never sees "ns:" polluting a "go to topology" match. With pills but
  // no text the user is browsing a scope, so suppress the command default. Empty
  // + no pills → the launcher default: Views + Actions, or (when the host opts
  // into a lean launcher) Actions only, so recents/search lead and the views
  // surface on type instead of walling the dropdown.
  const scoredCommands = useMemo(() => {
    if (!freeText) {
      if (pills.length) return []
      const cats = launcherShowsViews ? ['Views', 'Actions'] : ['Actions']
      return commandItems.filter((i) => cats.includes(i.category)).map((item) => ({ item, score: 1 }))
    }
    return commandItems.map((item) => ({ item, score: bestScore(item, freeText) })).filter((x) => x.score > 0).sort((a, b) => b.score - a.score)
  }, [commandItems, freeText, pills.length, launcherShowsViews])

  // Kinds whose NAME strongly matches (exact 150 / prefix 100) lead ABOVE the
  // resource instances.
  const leadingKinds = useMemo<CommandItem[]>(
    () => (freeText.length < 2 ? [] : scoredCommands.filter((x) => x.item.category === 'Resource Kinds' && x.score >= STRONG_KIND).slice(0, 5).map((x) => x.item)),
    [scoredCommands, freeText],
  )
  const leadingIds = useMemo(() => new Set(leadingKinds.map((i) => i.id)), [leadingKinds])

  const resourceRows = useMemo<Row[]>(() => {
    const hits = searchData?.hits ?? []
    return hits.map((hit) => ({ id: `res:${hit.cluster || ''}:${hit.kind}:${hit.group || ''}:${hit.namespace || ''}:${hit.name}`, kind: 'resource' as const, hit }))
  }, [searchData])

  // Launcher recents: only in the truly-empty state (no text, no pills).
  const recentRows = useMemo<Row[]>(() => {
    if (!open || freeText || pills.length > 0 || !loadRecents) return []
    return loadRecents().map((r) => ({
      id: `recent:${r.cluster || ''}:${r.kind}:${r.group || ''}:${r.namespace || ''}:${r.name}`,
      kind: 'resource' as const,
      recent: true,
      hit: { score: 0, kind: r.kind, group: r.group, namespace: r.namespace, name: r.name, cluster: r.cluster, clusterName: r.clusterName } as SearchHit,
    }))
  }, [open, freeText, pills.length, loadRecents])

  // Remaining matched commands (leading kinds removed so they don't repeat),
  // grouped by their real category in a fixed order. The empty launcher IS the
  // command menu, so show it in full; while searching, cap so resource hits stay
  // prominent.
  const commandGroups = useMemo(() => {
    const launcher = !freeText && pills.length === 0
    const filtered = scoredCommands.filter((x) => !leadingIds.has(x.item.id))
    const rest = (launcher ? filtered : filtered.slice(0, 8)).map((x) => x.item)
    const byCat = new Map<string, CommandItem[]>()
    for (const c of rest) { const l = byCat.get(c.category) ?? []; l.push(c); byCat.set(c.category, l) }
    // `CommandItem.category` is an open string, so a host can use one we don't
    // rank — render those AFTER the known order rather than silently dropping
    // their commands.
    const known = COMMAND_CATEGORY_ORDER.filter((cat) => byCat.has(cat))
    const extra = [...byCat.keys()].filter((cat) => !COMMAND_CATEGORY_ORDER.includes(cat))
    return [...known, ...extra].map((cat) => ({ category: cat, items: byCat.get(cat)! }))
  }, [scoredCommands, leadingIds, freeText, pills.length])

  const toCmdRow = (c: CommandItem): Row => ({ id: `cmd:${c.id}`, kind: 'command', command: c })

  const queryTokens = useMemo(() => freeText.split(/\s+/).filter(Boolean), [freeText])

  // Ordered, id-stable list (render order == keyboard model).
  const rows = useMemo<Row[]>(() => {
    const cmds: Row[] = commandGroups.flatMap((g) => g.items.map(toCmdRow))
    if (!freeText && pills.length === 0) return [...recentRows, ...cmds]
    const out: Row[] = [...leadingKinds.map(toCmdRow), ...(searchActive ? resourceRows : []), ...cmds]
    // "See all results" tails the list when the host wired a full-search surface
    // and there's something to expand to.
    if (onViewAllResults && searchActive && resourceRows.length > 0) {
      out.push({ id: 'view-all', kind: 'viewAll', query: queryString, count: searchData?.total_matched ?? resourceRows.length })
    }
    return out
  }, [recentRows, leadingKinds, resourceRows, commandGroups, freeText, pills.length, searchActive, onViewAllResults, queryString, searchData])
  const viewAllRow = rows.find((r): r is Extract<Row, { kind: 'viewAll' }> => r.kind === 'viewAll')

  // Selection tracked by stable id (not array index) so Enter can never fire a
  // stale row when the set shifts. Auto-follows the TOP result until the user
  // arrow-keys; a new query re-enables auto-follow.
  // When the host wires a search page (onViewAllResults), Enter on an
  // un-touched query goes THERE with the query rather than firing the top hit —
  // so we never pre-select a row (the user opts into a specific result by
  // arrowing/hovering). Without a search page (OSS), keep auto-follow-top so
  // Enter still opens the best match.
  const submitToSearch = !!onViewAllResults
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const userMovedRef = useRef(false)
  useEffect(() => { userMovedRef.current = false }, [queryString])
  const rowsKey = rows.map((r) => r.id).join('|')
  useEffect(() => {
    // Only suppress the pre-selection while actively SEARCHING (so Enter goes to
    // the search page, not a maybe-wrong top hit). In the empty launcher there's
    // no search to defer to, so auto-select the first row — otherwise Enter is a
    // no-op while the footer still reads "open".
    const dflt = submitToSearch && searchActive ? null : (rows[0]?.id ?? null)
    setSelectedId((cur) => {
      if (!userMovedRef.current) return dflt
      return cur && rows.some((r) => r.id === cur) ? cur : dflt
    })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [rowsKey])
  const selectedIndex = rows.findIndex((r) => r.id === selectedId)
  const moveSelection = (delta: number) => {
    userMovedRef.current = true
    setSelectedId(rows[Math.min(Math.max(selectedIndex + delta, 0), rows.length - 1)]?.id ?? null)
  }
  const selectRow = (id: string) => { userMovedRef.current = true; setSelectedId(id) }
  // Page by a full screenful of visible rows (minus one for context overlap).
  const pageStep = () => {
    const list = listRef.current
    const rowH = (list?.querySelector('button') as HTMLElement | null)?.offsetHeight
    if (!list || !rowH) return PAGE
    return Math.max(1, Math.floor(list.clientHeight / rowH) - 1)
  }

  const execute = useCallback((row: Row) => {
    if (row.kind === 'command') {
      row.command.action()
    } else if (row.kind === 'viewAll') {
      onViewAllResults?.(row.query)
    } else {
      const h = row.hit
      recordRecent?.({ kind: h.kind, group: h.group, namespace: h.namespace, name: h.name, cluster: h.cluster, clusterName: h.clusterName })
      onOpenResource(h)
    }
    setOpen(false)
    setText('')
    setPills([])
    inputRef.current?.blur()
  }, [onOpenResource, recordRecent, onViewAllResults])

  // The resources shown don't (yet) belong to the current query: the debounce
  // hasn't fired, the data is React Query placeholder, or results haven't landed.
  const resourcesStale = searchActive && (debounced !== queryString || isPlaceholderData || (resourceRows.length === 0 && isFetching))

  const handleKeyDown = useCallback((e: React.KeyboardEvent) => {
    if (e.key === 'Escape') { e.preventDefault(); setOpen(false); inputRef.current?.blur(); return }
    if (e.key === 'ArrowDown') { e.preventDefault(); moveSelection(1) }
    else if (e.key === 'ArrowUp') { e.preventDefault(); moveSelection(-1) }
    else if (e.key === 'PageDown') { e.preventDefault(); moveSelection(pageStep()) }
    else if (e.key === 'PageUp') { e.preventDefault(); moveSelection(-pageStep()) }
    else if (e.key === 'Enter') {
      e.preventDefault()
      const row = rows[selectedIndex]
      if (row) {
        if (row.kind === 'resource' && resourcesStale) return
        execute(row)
        return
      }
      // No row chosen: submit the query to the full search page (the default
      // for a host that wired one). viewAllRow carries the count when results
      // are in; fall back to the raw query while they're still loading.
      if (submitToSearch && searchActive) {
        if (viewAllRow) execute(viewAllRow)
        else { onViewAllResults?.(queryString); setOpen(false); setText(''); setPills([]); inputRef.current?.blur() }
      }
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [rows, selectedIndex, execute, resourcesStale, submitToSearch, searchActive, viewAllRow, onViewAllResults, queryString])

  useEffect(() => {
    listRef.current?.querySelector('[data-selected="true"]')?.scrollIntoView({ block: 'nearest' })
  }, [selectedId])

  // Close on outside click — the panel is portaled out of the container.
  useEffect(() => {
    if (!open) return
    const onDown = (e: MouseEvent) => {
      const t = e.target as Node
      if (!containerRef.current?.contains(t) && !panelRef.current?.contains(t)) setOpen(false)
    }
    document.addEventListener('mousedown', onDown)
    return () => document.removeEventListener('mousedown', onDown)
  }, [open])

  // Track the input's position so the portaled panel stays anchored under it.
  useEffect(() => {
    if (!open) { setAnchor(null); return }
    const update = () => {
      const el = containerRef.current
      if (!el) return
      const r = el.getBoundingClientRect()
      const header = el.closest('header')
      setAnchor({ centerX: r.left + r.width / 2, top: header ? header.getBoundingClientRect().bottom : r.bottom, width: r.width })
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

  const hero = size === 'hero'

  return (
    <div
      ref={containerRef}
      className={clsx('relative w-full', hero ? 'max-w-3xl' : 'max-w-xl', open && hero && 'z-[16]')}
      // Open on click even when the field is already focused — onFocus alone
      // never fires again, so an autofocused hero (Home) wouldn't reveal the
      // launcher on a click.
      onMouseDown={() => setOpen(true)}
    >
      <SearchPillInput
        className={hero
          ? 'min-h-14 px-5 rounded-2xl bg-theme-surface border border-theme-border shadow-theme-sm transition-colors focus-within:border-[var(--color-brand-500)] focus-within:shadow-[0_0_0_4px_color-mix(in_srgb,var(--color-brand-500)_15%,transparent)]'
          : 'min-h-8 px-2.5 rounded-md bg-theme-elevated border border-transparent focus-within:border-theme-border focus-within:bg-theme-surface transition-colors'}
        inputClassName={hero ? 'text-lg py-4' : undefined}
        text={text}
        pills={pills}
        onChange={({ text: t, pills: p }) => { setText(t); setPills(p); setOpen(true) }}
        onKeyDown={handleKeyDown}
        onFocus={() => { if (skipFocusOpen.current) { skipFocusOpen.current = false; return } setOpen(true) }}
        onSuggestingChange={setSuggesting}
        modifierOptions={modifierOptions}
        placeholder={placeholder}
        aria-label="Search resources and commands"
        inputRef={inputRef}
        leftSlot={<Search className={hero ? 'w-5 h-5 shrink-0 text-theme-text-tertiary' : 'w-3.5 h-3.5 shrink-0 text-theme-text-tertiary'} />}
        rightSlot={
          <div className="flex items-center gap-1.5 shrink-0">
            <SearchSyntaxHelp />
            {!hero && !text && pills.length === 0 && (
              <kbd className="text-[10px] text-theme-text-tertiary bg-theme-surface px-1 py-0.5 rounded border border-theme-border-light">
                {mac ? '⌘' : 'Ctrl+'}K
              </kbd>
            )}
          </div>
        }
      />

      {open && anchor && (dropdownOpen || suggesting) && createPortal(
        <>
          {/* Scrim — separates the dropdown from the page, consistently in both
              modes. At z-[15] it sits BELOW the rail/top bar (z-20/30), so the
              nav chrome stays lit while the content behind the panel dims+blurs:
              a "spotlight on search", not a full-screen modal dim (which fits a
              centered command palette, not an anchored omnibar). The hero covers
              from the top (its box is in the content, lifted to z-[16]); the
              top-bar launcher covers from below the field (its box is already in
              the z-20 chrome). Click closes. */}
          <div
            className="fixed left-0 right-0 bottom-0 z-[15] bg-black/15 dark:bg-black/50 backdrop-blur-[3px]"
            style={{ top: hero ? 0 : anchor.top }}
            onClick={() => { setOpen(false); inputRef.current?.blur() }}
          />
          {dropdownOpen && (
          <div
            ref={panelRef}
            style={{ position: 'fixed', top: anchor.top + 8, left: anchor.centerX, transform: 'translateX(-50%)', width: hero ? Math.round(anchor.width) : 640, maxWidth: 'calc(100vw - 2rem)' }}
            className="z-[121] dialog shadow-theme-lg ring-1 ring-black/5 dark:ring-white/10 overflow-hidden"
          >
          <div ref={listRef} className="max-h-[60vh] overflow-y-auto py-1">
            {recentRows.length > 0 && (
              <div>
                <div className="px-3 py-1 text-[10px] font-semibold uppercase tracking-wider text-theme-text-tertiary">Recently viewed</div>
                {recentRows.map((row) => row.kind === 'resource' && (
                  <ResourceRow key={row.id} hit={row.hit} selected={row.id === selectedId} onSelect={() => selectRow(row.id)} onActivate={() => execute(row)} />
                ))}
              </div>
            )}

            {leadingKinds.length > 0 && (
              <div>
                <div className="px-3 py-1 text-[10px] font-semibold uppercase tracking-wider text-theme-text-tertiary">Resource Kinds</div>
                {leadingKinds.map((item) => {
                  const id = `cmd:${item.id}`
                  return <CommandRow key={id} item={item} tokens={queryTokens} selected={id === selectedId} onSelect={() => selectRow(id)} onActivate={() => execute(toCmdRow(item))} />
                })}
              </div>
            )}

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
                    <ResourceRow key={row.id} hit={row.hit} stale={resourcesStale} selected={row.id === selectedId} onSelect={() => selectRow(row.id)} onActivate={() => { if (!resourcesStale) execute(row) }} />
                  ))
                )}
              </>
            )}

            {commandGroups.map((group) => (
              <div key={group.category}>
                <div className="px-3 py-1 mt-1 text-[10px] font-semibold uppercase tracking-wider text-theme-text-tertiary">{group.category}</div>
                {group.items.map((item) => {
                  const id = `cmd:${item.id}`
                  return <CommandRow key={id} item={item} tokens={queryTokens} selected={id === selectedId} onSelect={() => selectRow(id)} onActivate={() => execute({ id, kind: 'command', command: item })} />
                })}
              </div>
            ))}

            {viewAllRow && (
              <button
                type="button"
                data-selected={viewAllRow.id === selectedId}
                onMouseEnter={() => selectRow(viewAllRow.id)}
                onMouseDown={(e) => { e.preventDefault(); execute(viewAllRow) }}
                className={clsx('w-full flex items-center gap-2.5 px-3 py-1.5 mt-1 text-left border-t border-theme-border transition-colors', viewAllRow.id === selectedId ? 'selection' : 'hover:bg-theme-elevated/40')}
              >
                <Search className="w-4 h-4 shrink-0 text-theme-text-tertiary" />
                <span className="text-sm text-[var(--color-brand)]">See all {viewAllRow.count} result{viewAllRow.count === 1 ? '' : 's'}</span>
                <CornerDownLeft className="w-3 h-3 ml-auto shrink-0 text-theme-text-tertiary" />
              </button>
            )}
          </div>
          <div className="flex items-center gap-3 px-3 py-1.5 border-t border-theme-border text-[11px] text-theme-text-tertiary">
            <span className="flex items-center gap-1">
              <CornerDownLeft className="w-3 h-3" /> {submitToSearch && searchActive && selectedIndex < 0 ? 'search all' : 'open'}
            </span>
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
        {hit.clusterName ? <> · <span className="text-theme-text-secondary">{hit.clusterName}</span></> : ''}
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
