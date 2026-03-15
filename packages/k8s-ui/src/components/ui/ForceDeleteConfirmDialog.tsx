import { useState, useMemo } from 'react'
import { ChevronDown, ChevronRight, Loader2 } from 'lucide-react'
import { ConfirmDialog } from './ConfirmDialog'
import { formatKindName } from './drawer-components'

export interface CascadeDependent {
  kind: string
  namespace: string
  name: string
  group?: string
}

interface ForceDeleteConfirmDialogProps {
  open: boolean
  onClose: () => void
  onConfirm: (force: boolean) => void
  resourceName: string
  resourceKind: string
  namespaceName: string
  isLoading: boolean
  cascadeDependents?: CascadeDependent[]
  cascadeLoading?: boolean
}

export function ForceDeleteConfirmDialog({
  open,
  onClose,
  onConfirm,
  resourceName,
  resourceKind,
  namespaceName,
  isLoading,
  cascadeDependents,
  cascadeLoading,
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
      <div className="flex flex-col gap-3 pb-1">
        {cascadeLoading && (
          <div className="flex items-center gap-2 text-xs text-theme-text-tertiary">
            <Loader2 className="w-3.5 h-3.5 animate-spin" />
            Checking for dependent resources...
          </div>
        )}

        {!cascadeLoading && cascadeDependents && cascadeDependents.length > 0 && (
          <CascadeDependentsList dependents={cascadeDependents} />
        )}

        <label className="flex items-center gap-2 text-sm text-theme-text-secondary">
          <input
            type="checkbox"
            checked={forceDelete}
            onChange={(e) => setForceDelete(e.target.checked)}
            className="w-4 h-4 rounded border-theme-border bg-theme-base text-red-600 focus:ring-red-500 focus:ring-offset-0"
          />
          <span>Force delete (strips finalizers and bypasses grace period)</span>
        </label>
      </div>
    </ConfirmDialog>
  )
}

const MAX_NAMES_PER_KIND = 8

function CascadeDependentsList({ dependents }: { dependents: CascadeDependent[] }) {
  const [expanded, setExpanded] = useState(false)

  const grouped = useMemo(() => {
    const map = new Map<string, string[]>()
    for (const dep of dependents) {
      const kind = dep.kind
      if (!map.has(kind)) map.set(kind, [])
      map.get(kind)!.push(dep.name)
    }
    return Array.from(map.entries()).sort((a, b) => a[0].localeCompare(b[0]))
  }, [dependents])

  return (
    <div className="rounded border border-amber-500/30 bg-amber-500/5">
      <button
        type="button"
        onClick={() => setExpanded(!expanded)}
        className="flex items-center gap-2 w-full px-3 py-2 text-left text-xs font-medium text-amber-400 hover:bg-amber-500/10 transition-colors"
      >
        {expanded ? <ChevronDown className="w-3.5 h-3.5 shrink-0" /> : <ChevronRight className="w-3.5 h-3.5 shrink-0" />}
        <span>
          Will also delete {dependents.length} dependent {dependents.length === 1 ? 'resource' : 'resources'}
        </span>
      </button>

      {expanded && (
        <div className="px-3 pb-2.5 space-y-1.5">
          {grouped.map(([kind, names]) => (
            <div key={kind} className="text-xs">
              <span className="font-medium text-theme-text-primary">{formatKindName(kind)}</span>
              <span className="text-theme-text-tertiary ml-1">({names.length})</span>
              <div className="ml-3 mt-0.5 text-theme-text-secondary font-mono break-all">
                {names.slice(0, MAX_NAMES_PER_KIND).join(', ')}
                {names.length > MAX_NAMES_PER_KIND && (
                  <span className="text-theme-text-tertiary"> +{names.length - MAX_NAMES_PER_KIND} more</span>
                )}
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  )
}
