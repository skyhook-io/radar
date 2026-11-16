import type { ResourceHost } from '../../integrations/resourceHost'
import type { SelectedResource } from '../../types'
import { DrawerTrailBack } from '../workspace/DrawerTrailBack'
import { VeleroBSLRenderer } from '../resources/renderers/VeleroBSLRenderer'
import { VeleroBackupRenderer } from '../resources/renderers/VeleroBackupRenderer'
import { VeleroRestoreRenderer } from '../resources/renderers/VeleroRestoreRenderer'
import { VeleroScheduleRenderer } from '../resources/renderers/VeleroScheduleRenderer'
import { VELERO_GROUP as VELERO_GROUP_NAME, VELERO_LOCATION_KIND, VELERO_SCHEDULE_KIND, veleroKindListMode } from './routes'
import { VeleroLocationsKindList, VeleroSchedulesKindList } from './VeleroKindLists'
import { VeleroBackupNow } from './VeleroBackupNow'
import { VeleroScheduleStatusBadge } from './VeleroScheduleStatusBadge'
import { VeleroRunIssueMessages } from './VeleroRunMessagesHost'
import { kindToPluralWithGroup } from '../../utils/navigation'
import { isApiGroup } from '@skyhook-io/k8s-ui'

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
  resources: ['schedules', 'backups', 'restores'].map((name) => ({ name, group: VELERO_GROUP_NAME })),
  // A schedule's header: its reading as the status chip, and Back up now in
  // the action row with every resource's actions.
  detailSlots: (resource) =>
    kindToPluralWithGroup(resource.kind, resource.group ?? VELERO_GROUP_NAME) === VELERO_SCHEDULE_KIND.name
      ? {
          renderStatusBadge: (data: any) => <VeleroScheduleStatusBadge resource={data} />,
          renderHeaderActions: ({ resource: data, onNavigate }) =>
            isApiGroup(data?.apiVersion, VELERO_GROUP_NAME) ? <VeleroBackupNow schedule={data} onNavigate={onNavigate} /> : null,
        }
      : {},
  // A failed run's messages are what is wrong with it.
  issueDetail: (props) => <VeleroRunIssueMessages {...props} />,
  // Radar's own tables are these kinds' lists, so neither has a second entry in the sidebar.
  kindLists: [
    { kind: VELERO_SCHEDULE_KIND, title: 'Velero schedules', mode: veleroKindListMode, Component: VeleroSchedulesKindList },
    { kind: VELERO_LOCATION_KIND, title: 'Velero storage locations', mode: veleroKindListMode, Component: VeleroLocationsKindList },
  ],
}
