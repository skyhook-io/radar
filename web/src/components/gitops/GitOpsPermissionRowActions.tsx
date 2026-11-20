import type { ReactNode } from 'react'
import { RowActionMenu, type RowActionItem } from '@skyhook-io/k8s-ui/components/ui/RowActionMenu'
import type { GitOpsRow } from '@skyhook-io/k8s-ui'
import { useGitOpsActionCapabilities } from '../../api/client'

export function GitOpsPermissionRowActions({ row, items }: { row: GitOpsRow; items: RowActionItem[] }) {
  return (
    <RowActionMenu items={items} renderOpenItems={(render, open) => (
      <PermissionItems row={row} items={items} render={render} open={open} />
    )} />
  )
}

function PermissionItems({ row, items, render, open }: {
  row: GitOpsRow
  items: RowActionItem[]
  render: (items: RowActionItem[], note?: string) => ReactNode
  open: boolean
}) {
  const { disabledReasons, permissionCheckUnavailable } = useGitOpsActionCapabilities(row.kindName, row.group, row.namespace, row.name, open)
  const actions = items.map(item => {
    const action = item.key === 'hard-refresh' ? 'refresh' : item.key
    const reason = item.disabledReason || disabledReasons[action]
    return { ...item, disabled: item.disabled || !!reason, disabledReason: reason }
  })
  return render(actions, permissionCheckUnavailable ? "Couldn't check your permissions. You can still try." : undefined)
}
