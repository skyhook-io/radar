import type { ResourceHost } from '../../integrations/resourceHost'
import type { SelectedResource } from '../../types'
import { DrawerTrailBack } from '../workspace/DrawerTrailBack'
import { VeleroBSLRenderer } from '../resources/renderers/VeleroBSLRenderer'
import { VeleroBackupRenderer } from '../resources/renderers/VeleroBackupRenderer'
import { VeleroRestoreRenderer } from '../resources/renderers/VeleroRestoreRenderer'
import { VeleroScheduleRenderer } from '../resources/renderers/VeleroScheduleRenderer'

const KIND_BY_PLURAL: Record<string, string> = {
  backups: 'Backup',
  restores: 'Restore',
  schedules: 'Schedule',
  backupstoragelocations: 'BackupStorageLocation',
  backuprepositories: 'BackupRepository',
  volumesnapshotlocations: 'VolumeSnapshotLocation',
}

function VeleroDrawerTrailBack({ resource }: { resource: SelectedResource }) {
  return <DrawerTrailBack resource={resource} pathPrefix="/velero" kindLabel={(plural) => KIND_BY_PLURAL[plural]} />
}

export const veleroResourceHost: ResourceHost = {
  id: 'velero',
  feature: 'veleroWorkspace',
  renderers: { VeleroBSLRenderer, VeleroBackupRenderer, VeleroRestoreRenderer, VeleroScheduleRenderer },
  drawerNavigation: {
    ownsPath: (pathname) => pathname === '/velero' || pathname.startsWith('/velero/'),
    Component: VeleroDrawerTrailBack,
  },
}
