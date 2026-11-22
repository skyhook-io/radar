// kagent cell components for ResourcesView table

import { clsx } from 'clsx'
import { Tooltip } from '../../ui/Tooltip'
import { getKagentAgentColumnValue, getKagentAgentRevisions, getKagentStatus } from '../resource-utils-kagent'

function Empty() {
  return <span className="text-sm text-theme-text-tertiary">-</span>
}

function TextCell({ value, mono, title }: { value: string | undefined; mono?: boolean; title?: string }) {
  if (!value || value === '-') return <Empty />
  return (
    <Tooltip content={title ?? value}>
      <span className={clsx('text-sm text-theme-text-secondary truncate block', mono && 'font-mono text-xs')}>{value}</span>
    </Tooltip>
  )
}

export function KagentAgentCell({ resource, column }: { resource: any; column: string }) {
  switch (column) {
    case 'status': {
      const status = getKagentStatus(resource)
      if (!status) return <Empty />
      const badge = <span className={clsx('badge', status.color)}>{status.text}</span>
      return status.message ? <Tooltip content={status.message}>{badge}</Tooltip> : badge
    }
    case 'kagentRevision':
      return <TextCell value={getKagentAgentColumnValue(resource, column)} mono title={getKagentAgentRevisions(resource).latestSuccessful} />
    default:
      return <TextCell value={getKagentAgentColumnValue(resource, column)} />
  }
}
