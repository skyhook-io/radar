import type { Topology } from '../../types'
import { pluralize } from '../../utils/pluralize'

export function topologyViewLimitMessage(inventories: Topology['incompleteInventories']): string {
  const loading = inventories?.some(inventory => inventory.state === 'pending')
  const scoped = inventories?.some(inventory => inventory.state === 'uncovered')
  if (loading && scoped) return 'Some resource data is still loading; Radar watches only selected namespaces.'
  if (loading) return 'Some resource data is still loading.'
  if (scoped) return 'Radar watches only selected namespaces.'
  return 'Large retained run history is summarized to keep the graph readable.'
}

export function topologyWarningNotice(topology: Pick<Topology, 'warnings' | 'incompleteInventories'>) {
  const warnings = topology.warnings ?? []
  const rbacWarnings = warnings.filter(w => w.includes('RBAC not granted'))
  const viewWarnings = warnings.filter(w => w.startsWith('Topology view:'))
  const otherWarnings = warnings.filter(w => !w.includes('RBAC not granted') && !w.startsWith('Topology view:'))
  const isAllRbac = otherWarnings.length === 0 && viewWarnings.length === 0
  const isViewLimited = otherWarnings.length === 0 && viewWarnings.length > 0
  const accessMessage = `${pluralize(rbacWarnings.length, 'resource type')} not accessible due to RBAC restrictions.`
  const label = isAllRbac ? 'Limited Access:' : isViewLimited ? (rbacWarnings.length > 0 ? 'Limited access · view limited:' : 'View limited:') : 'Warning:'
  const message = isAllRbac ? accessMessage : isViewLimited ? `${rbacWarnings.length > 0 ? accessMessage + ' ' : ''}${topologyViewLimitMessage(topology.incompleteInventories)}` : 'Some resources failed to load. Data may be incomplete.'
  return { rbacWarnings, viewWarnings, otherWarnings, isAllRbac, isViewLimited, label, message }
}
