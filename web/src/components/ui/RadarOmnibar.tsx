import { forwardRef, useEffect, useMemo, useState, useRef, useImperativeHandle } from 'react'
import { Omnibar, type OmnibarHandle } from './Omnibar'
import { useSearch, useNamespaceScope, useContexts, type SearchHit, type SearchResult } from '../../api/client'
import { useAPIResources } from '../../api/apiResources'
import { loadRecentResources, recordRecentResource } from '../../hooks/useRecentResources'
import { useCommandItems, type CommandItemCallbacks } from './command-items'
import { PENDING_RETRY_BUDGET_MS, PENDING_RETRY_MS, searchCoverageNote } from './searchCoverageNote'

interface RadarOmnibarProps extends CommandItemCallbacks {
  onOpenResource: (hit: SearchHit) => void
}

const stillLoading = (data: SearchResult | undefined) => !!data?.coverage?.gaps.some((gap) => gap.code === 'kind_sync_pending')

// Radar standalone's omnibar: wires the injectable Omnibar to Radar's own hooks
// (cluster /api/search, kubeconfig contexts, API discovery, recents). Radar Hub
// provides a parallel wrapper over fleet search.
export const RadarOmnibar = forwardRef<OmnibarHandle, RadarOmnibarProps>(function RadarOmnibar(
  { onOpenResource, ...callbacks },
  ref,
) {
  const omnibarRef = useRef<OmnibarHandle>(null)
  useImperativeHandle(ref, () => ({
    focus: () => omnibarRef.current?.focus(),
    addModifier: (modifier) => omnibarRef.current?.addModifier(modifier),
  }), [])
  // The omnibar debounces internally and emits the query to search here.
  const [query, setQuery] = useState('')
  const [open, setOpen] = useState(false)
  // The query whose still-loading re-queries ran out of budget while open.
  const [exhaustedQuery, setExhaustedQuery] = useState<string | null>(null)

  const { data: searchData, isFetching, isPlaceholderData, isError } = useSearch(query, {
    enabled: open,
    globalNs: true,
    refetchInterval: (data) => (open && stillLoading(data) && exhaustedQuery !== query ? PENDING_RETRY_MS : false),
  })
  const pending = !isPlaceholderData && stillLoading(searchData)
  useEffect(() => {
    if (!open || !pending) return
    const timer = setTimeout(() => setExhaustedQuery(query), PENDING_RETRY_BUDGET_MS)
    return () => clearTimeout(timer)
  }, [open, pending, query])
  useEffect(() => { if (!open) setExhaustedQuery(null) }, [open])

  const { data: nsScope } = useNamespaceScope()
  const { data: apiResources } = useAPIResources()
  const { data: contexts } = useContexts()
  const contextKey = useMemo(() => contexts?.find((c) => c.isCurrent)?.name ?? '', [contexts])

  const modifierOptions = useMemo(() => ({
    ns: nsScope?.accessibleNamespaces ?? [],
    kind: apiResources ? [...new Set(apiResources.filter((r) => r.verbs?.includes('list')).map((r) => r.kind))].sort() : [],
  }), [nsScope, apiResources])

  const commandItems = useCommandItems(callbacks)
  const { onNavigateKind } = callbacks
  const note = searchCoverageNote(searchData?.coverage, {
    hits: searchData?.hits,
    query,
    recheckKind: (kind) => omnibarRef.current?.addModifier({ key: 'kind', value: kind }),
    pendingExhausted: exhaustedQuery === query,
    openKindList: (kind, group) => {
      const resource = apiResources?.find((r) => r.kind === kind && r.group === group)
      return resource ? () => onNavigateKind(resource.name, resource.group) : undefined
    },
  })

  return (
    <Omnibar
      ref={omnibarRef}
      onOpenResource={onOpenResource}
      commandItems={commandItems}
      onQueryChange={(q, o) => { setQuery(q); setOpen(o) }}
      searchData={searchData}
      isFetching={isFetching}
      isError={isError}
      isPlaceholderData={isPlaceholderData}
      note={note}
      modifierOptions={modifierOptions}
      seedNamespaces={nsScope?.actives}
      loadRecents={() => loadRecentResources(contextKey)}
      recordRecent={(r) => recordRecentResource(r, contextKey)}
    />
  )
})
