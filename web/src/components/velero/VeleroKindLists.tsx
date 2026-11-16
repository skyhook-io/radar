import type { ComponentType } from 'react'
import type { HostKind, KindListProps } from '../../integrations/resourceHost'
import { useVeleroWorkspace } from '../../api/velero'
import { VeleroLocations } from './VeleroLocations'
import { VeleroSchedules } from './VeleroSchedules'
import { VeleroBackups, VeleroRestores } from './VeleroRunLists'
import { VELERO_BACKUP_KIND, VELERO_RESTORE_KIND } from './routes'
import { VeleroScreenGate, useVeleroInstallation, type VeleroScreenProps } from './shared'

/**
 * A Velero kind's list on its Resources route, as Radar reads it rather than
 * as a bare table: the same workspace data as /velero, rows opening the
 * Resources page's drawer. The namespace filter does not apply, as on /velero.
 */
function veleroKindList(Screen: ComponentType<VeleroScreenProps>, kind?: HostKind) {
  return function VeleroKindList({ inspected, onInspect, onCreate }: KindListProps) {
    const query = useVeleroWorkspace()
    const [installation, setInstallation] = useVeleroInstallation(query.data?.installations)
    return (
      <div className="flex min-h-0 min-w-0 flex-1 flex-col bg-theme-base">
        <VeleroScreenGate query={query} discoveryPending={false} discovered>
          {(data) => <Screen data={data} installation={installation} onInstallation={setInstallation} onInspect={onInspect} inspected={inspected} onCreate={kind ? () => onCreate(kind) : undefined} />}
        </VeleroScreenGate>
      </div>
    )
  }
}

export const VeleroSchedulesKindList = veleroKindList(VeleroSchedules)
export const VeleroLocationsKindList = veleroKindList(VeleroLocations)
export const VeleroBackupsKindList = veleroKindList(VeleroBackups, VELERO_BACKUP_KIND)
export const VeleroRestoresKindList = veleroKindList(VeleroRestores, VELERO_RESTORE_KIND)
