import type { ReactNode } from 'react'
import { CNPG_BARMAN_OBJECTSTORE_GROUP, isApiGroup, type NavigateToResource } from '@skyhook-io/k8s-ui'
import { CNPGClusterActions } from './CNPGClusterActions'
import { CNPGConnectButton } from './CNPGConnectButton'
import { CNPGScheduleActions } from './CNPGScheduleActions'
import { CNPGPoolerActions } from './CNPGPoolerActions'
import { CNPGOperationTracker } from '../operations/CNPGOperationTracker'
import { CNPGRestoreButton } from '../recovery/CNPGRestoreButton'

function backupRestoreBlocker(b: any): string | undefined {
  if (b?.status?.phase !== 'completed') return 'Only a completed Backup can be restored'
  const plugin = b?.spec?.method === 'plugin' || b?.status?.method === 'plugin'
  if (plugin && !b?.status?.backupId) return 'The Backup records no backup ID, which a plugin restore needs'
  return undefined
}

export function renderCNPGHeaderActions({
  resource,
  namespace,
  name,
  compact,
  onNavigate,
}: {
  resource: any
  namespace: string
  name: string
  compact: boolean
  onNavigate?: NavigateToResource
}): ReactNode {
  if (isApiGroup(resource?.apiVersion, CNPG_BARMAN_OBJECTSTORE_GROUP) && resource.kind === 'ObjectStore') {
    return <CNPGRestoreButton namespace={namespace} entry={{ kind: 'objectStore', name }} compact={compact} />
  }
  if (!isApiGroup(resource?.apiVersion, 'postgresql.cnpg.io')) return null
  if (resource.kind === 'Cluster') {
    return (
      <div className="flex items-center gap-1.5">
        <CNPGOperationTracker namespace={namespace} name={name} uid={resource.metadata.uid} />
        <CNPGConnectButton namespace={namespace} name={name} compact={compact} onNavigate={onNavigate} />
        <CNPGClusterActions namespace={namespace} name={name} compact={compact} />
      </div>
    )
  }
  if (resource.kind === 'Backup') {
    return <CNPGRestoreButton namespace={namespace} entry={{ kind: 'backup', name }} disabledReason={backupRestoreBlocker(resource)} compact={compact} />
  }
  if (resource.kind === 'ScheduledBackup') return <CNPGScheduleActions namespace={namespace} name={name} />
  if (resource.kind === 'Pooler') return <CNPGPoolerActions namespace={namespace} name={name} />
  return null
}
