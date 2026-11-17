import type { DetailSlots, HostResource, ResourceHost } from '../../integrations/resourceHost'
import type { SelectedResource } from '../../types'
import { DrawerTrailBack } from '../workspace/DrawerTrailBack'
import { VeleroBSLRenderer } from '../resources/renderers/VeleroBSLRenderer'
import { VeleroBackupRenderer } from '../resources/renderers/VeleroBackupRenderer'
import { VeleroRestoreRenderer } from '../resources/renderers/VeleroRestoreRenderer'
import { VELERO_DETAIL_KINDS, VELERO_GROUP as VELERO_GROUP_NAME, VELERO_BACKUP_KIND, VELERO_LOCATION_KIND, VELERO_RESTORE_KIND, VELERO_SCHEDULE_KIND, veleroDetailKindFor, veleroDetailPath, veleroKindListMode, VELERO_NAMESPACE_FILTER_TOOLTIP } from './routes'
import { renderVeleroSummary } from './VeleroPages'
import { VeleroBackupsKindList, VeleroLocationsKindList, VeleroRestoresKindList, VeleroSchedulesKindList } from './VeleroKindLists'
import { VeleroDeleteBackup } from './VeleroDeleteBackup'
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

function detailKind(resource: HostResource): string | null {
  return veleroDetailKindFor(kindToPluralWithGroup(resource.kind, resource.group ?? ''), resource.group)
}

export const veleroResourceHost: ResourceHost = {
  id: 'velero',
  feature: 'veleroWorkspace',
  renderers: { VeleroBSLRenderer, VeleroBackupRenderer, VeleroRestoreRenderer },
  drawerNavigation: {
    ownsPath: (pathname) => pathname === '/velero' || pathname.startsWith('/velero/'),
    Component: VeleroDrawerTrailBack,
  },
  resources: Object.keys(VELERO_DETAIL_KINDS).map((name) => ({ name, group: VELERO_GROUP_NAME })),
  // Each of these kinds has a page of its own (/velero/<plural>/<ns>/<name>):
  // "Open full view" and links to the generic detail land there.
  expandedPath: (resource, context, tab) => {
    const plural = detailKind(resource)
    return plural ? veleroDetailPath({ plural, namespace: resource.namespace, name: resource.name }, context, tab) : null
  },
  detailRedirect: (resource, search) => {
    const plural = detailKind(resource)
    if (!plural) return null
    const params = new URLSearchParams(search)
    params.delete('apiGroup')
    const base = veleroDetailPath({ plural, namespace: resource.namespace, name: resource.name })
    const qs = params.toString()
    return qs ? `${base}?${qs}` : base
  },
  detailOwnership: ['summary', 'destination'],
  // The composed Overview, on the page and in the drawer (the renderer is
  // Spec & status); a backup's Delete backup; and a schedule's header: its
  // reading as the status chip, and Back up now in the action row with every
  // resource's actions.
  detailSlots: (resource): DetailSlots => {
    const summary: DetailSlots = { renderSummary: (props) => renderVeleroSummary(props) }
    if (detailKind(resource) === VELERO_BACKUP_KIND.name) return {
      ...summary,
      renderHeaderActions: ({ resource: data }) => isApiGroup(data?.apiVersion, VELERO_GROUP_NAME) ? <VeleroDeleteBackup backup={data} /> : null,
    }
    if (kindToPluralWithGroup(resource.kind, resource.group ?? VELERO_GROUP_NAME) !== VELERO_SCHEDULE_KIND.name) return summary
    return {
      ...summary,
      renderStatusBadge: (data: any) => <VeleroScheduleStatusBadge resource={data} />,
      renderHeaderActions: ({ resource: data, onNavigate }) =>
        isApiGroup(data?.apiVersion, VELERO_GROUP_NAME) ? <VeleroBackupNow schedule={data} onNavigate={onNavigate} /> : null,
    }
  },
  // A failed run's messages are what is wrong with it.
  issueDetail: (props) => <VeleroRunIssueMessages {...props} />,
  // Radar's own tables are these kinds' lists, so none has a second entry in the sidebar.
  kindLists: [
    { kind: VELERO_SCHEDULE_KIND, title: 'Velero schedules', mode: veleroKindListMode, namespaceFilterTooltip: VELERO_NAMESPACE_FILTER_TOOLTIP, Component: VeleroSchedulesKindList },
    { kind: VELERO_LOCATION_KIND, title: 'Velero storage locations', mode: veleroKindListMode, namespaceFilterTooltip: VELERO_NAMESPACE_FILTER_TOOLTIP, Component: VeleroLocationsKindList },
    { kind: VELERO_BACKUP_KIND, title: 'Velero backups', mode: veleroKindListMode, namespaceFilterTooltip: VELERO_NAMESPACE_FILTER_TOOLTIP, Component: VeleroBackupsKindList },
    { kind: VELERO_RESTORE_KIND, title: 'Velero restores', mode: veleroKindListMode, namespaceFilterTooltip: VELERO_NAMESPACE_FILTER_TOOLTIP, Component: VeleroRestoresKindList },
  ],
}
