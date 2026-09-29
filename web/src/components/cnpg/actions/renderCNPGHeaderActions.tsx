import type { ReactNode } from 'react'
import { isApiGroup } from '@skyhook-io/k8s-ui'
import { CNPGClusterActions } from './CNPGClusterActions'
import { CNPGScheduleActions } from './CNPGScheduleActions'
import { CNPGPoolerActions } from './CNPGPoolerActions'

export function renderCNPGHeaderActions({ resource, namespace, name, compact }: { resource: any; namespace: string; name: string; compact: boolean }): ReactNode {
  if (!isApiGroup(resource?.apiVersion, 'postgresql.cnpg.io')) return null
  if (resource.kind === 'Cluster') return <CNPGClusterActions namespace={namespace} name={name} compact={compact} />
  if (resource.kind === 'ScheduledBackup') return <CNPGScheduleActions namespace={namespace} name={name} />
  if (resource.kind === 'Pooler') return <CNPGPoolerActions namespace={namespace} name={name} />
  return null
}
