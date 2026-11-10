import type { SelectedResource } from '../../types'
import { CNPGOverview } from './CNPGOverview'
import { CNPGScreenGate } from './shared'
import { useCNPGFleet } from './useCNPGSidebarWorkspace'
import { useCNPGScreenParams } from './useCNPGScreenParams'

/**
 * The CloudNativePG Cluster kind's list on its Resources route: the Clusters
 * view of /cnpg, over the same data and URL parameters, inspecting rows in the
 * Resources page's drawer.
 */
export function CNPGClustersKindView({
  namespaces,
  inspected,
  onInspect,
  onClearNamespaces,
  onCreate,
}: {
  namespaces: string[]
  inspected: SelectedResource | null
  onInspect: (resource: SelectedResource) => void
  onClearNamespaces: () => void
  onCreate: () => void
}) {
  const { query, fleet } = useCNPGFleet(namespaces)
  const [searchParams, setParams] = useCNPGScreenParams()
  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col bg-theme-base">
      <CNPGScreenGate query={query} fleet={fleet}>
        {(data, readyFleet) => (
          <CNPGOverview
            data={data}
            fleet={readyFleet}
            namespaces={namespaces}
            searchParams={searchParams}
            onSetParams={setParams}
            onInspect={onInspect}
            inspected={inspected}
            onClearNamespaces={onClearNamespaces}
            onCreate={onCreate}
          />
        )}
      </CNPGScreenGate>
    </div>
  )
}
