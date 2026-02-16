import { useState } from 'react'
import { ConfirmDialog } from './ConfirmDialog'

interface ForceDeleteConfirmDialogProps {
  open: boolean
  onClose: () => void
  onConfirm: (force: boolean) => void
  resourceName: string
  resourceKind: string
  namespaceName: string
  isLoading: boolean
}

export function ForceDeleteConfirmDialog({
  open,
  onClose,
  onConfirm,
  resourceName,
  resourceKind,
  namespaceName,
  isLoading,
}: ForceDeleteConfirmDialogProps) {
  const [forceDelete, setForceDelete] = useState(false)

  function handleClose() {
    onClose()
    setForceDelete(false)
  }

  function handleConfirm() {
    onConfirm(forceDelete)
  }

  return (
    <ConfirmDialog
      open={open}
      onClose={handleClose}
      onConfirm={handleConfirm}
      title="Delete Resource"
      message={`Are you sure you want to delete "${resourceName}"?`}
      details={`This will permanently delete the ${resourceKind} "${resourceName}" from the "${namespaceName}" namespace.`}
      confirmLabel={forceDelete ? 'Force Delete' : 'Delete'}
      variant="danger"
      isLoading={isLoading}
    >
      <label className="flex items-center gap-2 text-sm text-theme-text-secondary cursor-pointer">
        <input
          type="checkbox"
          checked={forceDelete}
          onChange={(e) => setForceDelete(e.target.checked)}
          className="w-4 h-4 rounded border-theme-border bg-theme-base text-red-600 focus:ring-red-500 focus:ring-offset-0"
        />
        <span>Force delete (strips finalizers and bypasses grace period)</span>
      </label>
    </ConfirmDialog>
  )
}
