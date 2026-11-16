import type { KindListProps } from '../../integrations/resourceHost'
import { useVeleroWorkspace } from '../../api/velero'
import { VeleroOverview } from './VeleroOverview'
import { VeleroScreenGate, useVeleroInstallation } from './shared'

/**
 * The Velero Schedule kind's list on its Resources route: the Velero
 * Overview, so a schedule reached from the sidebar, search or a link reads as
 * late or on schedule rather than as a bare table row. Rows open the Resources
 * page's drawer. The namespace filter does not apply, as on /velero.
 */
export function VeleroSchedulesKindView({ inspected, onInspect }: KindListProps) {
  const query = useVeleroWorkspace()
  const [installation, setInstallation] = useVeleroInstallation(query.data?.installations)
  return (
    <div className="flex min-h-0 min-w-0 flex-1 flex-col bg-theme-base">
      <VeleroScreenGate query={query} discoveryPending={false} discovered>
        {(data) => (
          <VeleroOverview data={data} installation={installation} onInstallation={setInstallation} onInspect={onInspect} inspected={inspected} />
        )}
      </VeleroScreenGate>
    </div>
  )
}
