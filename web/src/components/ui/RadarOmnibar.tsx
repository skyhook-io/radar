import { forwardRef, useMemo, useState } from 'react'
import { Omnibar, type OmnibarHandle } from './Omnibar'
import { useSearch, useNamespaceScope, useContexts, type SearchHit } from '../../api/client'
import { useAPIResources } from '../../api/apiResources'
import { loadRecentResources, recordRecentResource } from '../../hooks/useRecentResources'
import { useCommandItems, type CommandItemCallbacks } from './command-items'

interface RadarOmnibarProps extends CommandItemCallbacks {
  onOpenResource: (hit: SearchHit) => void
}

// Radar standalone's omnibar: wires the injectable Omnibar to Radar's own hooks
// (cluster /api/search, kubeconfig contexts, API discovery, recents). Radar Hub
// provides a parallel wrapper over fleet search.
export const RadarOmnibar = forwardRef<OmnibarHandle, RadarOmnibarProps>(function RadarOmnibar(
  { onOpenResource, ...callbacks },
  ref,
) {
  // The omnibar debounces internally and emits the query to search here.
  const [query, setQuery] = useState('')
  const [open, setOpen] = useState(false)

  const { data: searchData, isFetching, isPlaceholderData, isError } = useSearch(query, { enabled: open, globalNs: true })
  const { data: nsScope } = useNamespaceScope()
  const { data: apiResources } = useAPIResources()
  const { data: contexts } = useContexts()
  const contextKey = useMemo(() => contexts?.find((c) => c.isCurrent)?.name ?? '', [contexts])

  const modifierOptions = useMemo(() => ({
    ns: nsScope?.accessibleNamespaces ?? [],
    kind: apiResources ? [...new Set(apiResources.filter((r) => r.verbs?.includes('list')).map((r) => r.kind))].sort() : [],
  }), [nsScope, apiResources])

  const commandItems = useCommandItems(callbacks)

  return (
    <Omnibar
      ref={ref}
      onOpenResource={onOpenResource}
      commandItems={commandItems}
      onQueryChange={(q, o) => { setQuery(q); setOpen(o) }}
      searchData={searchData}
      isFetching={isFetching}
      isError={isError}
      isPlaceholderData={isPlaceholderData}
      modifierOptions={modifierOptions}
      seedNamespaces={nsScope?.actives}
      loadRecents={() => loadRecentResources(contextKey)}
      recordRecent={(r) => recordRecentResource(r, contextKey)}
    />
  )
})
