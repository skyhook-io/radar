import { useState } from 'react'
import { RowActionMenu, type RowActionItem } from '@skyhook-io/k8s-ui/components/ui/RowActionMenu'
import type { GitOpsRow } from '@skyhook-io/k8s-ui'
import { useGitOpsActionCapabilities } from '../../api/client'

export function GitOpsPermissionRowActions({ row, items }: { row: GitOpsRow; items: RowActionItem[] }) {
  const [open, setOpen] = useState(false)
  const { disabledReasons } = useGitOpsActionCapabilities(row.kindName, row.group, row.namespace, row.name, open)
  return <RowActionMenu onOpenChange={setOpen} items={items.map(item => {
    const action = item.key === 'hard-refresh' ? 'refresh' : item.key
    const reason = item.disabledReason || disabledReasons[action]
    return { ...item, disabled: item.disabled || !!reason, disabledReason: reason }
  })} />
}
