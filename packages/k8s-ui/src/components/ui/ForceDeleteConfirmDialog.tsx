import { useState, useMemo } from 'react'
import { AlertTriangle, Loader2 } from 'lucide-react'
import { Collapse, CollapseChevron, useDisclosure } from './Collapse'
import { ConfirmDialog } from './ConfirmDialog'
import { pluralize } from '../../utils/pluralize'

export interface CascadeDependent {
  kind: string
  namespace: string
  name: string
  group?: string
}

/** What else a delete removes, beyond the dependents garbage collection takes for certain. */
export interface CascadeDetail {
  /** "ownerReferences" when dependents are garbage-collection results; absent from older servers. */
  basis?: string
  /** Deleted unless another owner Radar can't see is still live. */
  possibleDependents?: CascadeDependent[]
  /** What Argo CD or Flux deletes through its finalizer. */
  controllerTeardown?: {
    controller: string
    action: 'prune' | 'uninstall' | string
    resources?: CascadeDependent[]
    /** Already being deleted: the teardown may be underway. */
    terminating?: boolean
  }
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
  cascadeRootResolved?: boolean
  cascadeDetail?: CascadeDetail
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
  cascadeRootResolved,
  cascadeDetail,
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
      details={namespaceName
        ? `This will permanently delete the ${resourceKind} "${resourceName}" from the "${namespaceName}" namespace.`
        : `This will permanently delete the cluster-scoped ${resourceKind} "${resourceName}".`}
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
          cascadeDetail?.basis === 'ownerReferences' ? (
            <CascadeDependentsList
              dependents={cascadeDependents}
              title={`Will also delete ${pluralize(cascadeDependents.length, 'dependent resource')}`}
              note="Kubernetes deletes these through their owner references. Owned objects Radar doesn't track, such as EndpointSlices, go too but aren't listed."
            />
          ) : (
            // An older Radar walked every management link, not only owner references.
            <CascadeDependentsList
              dependents={cascadeDependents}
              title={`May also delete ${pluralize(cascadeDependents.length, 'related resource')}`}
              note="This Radar version can't tell owned resources from other links. Kubernetes deletes only those that name this one as their owner."
            />
          )
        )}

        {!cascadeLoading && cascadeDetail?.possibleDependents && cascadeDetail.possibleDependents.length > 0 && (
          <CascadeDependentsList
            dependents={cascadeDetail.possibleDependents}
            title={`May also delete ${pluralize(cascadeDetail.possibleDependents.length, 'resource')}`}
            note="Each also has an owner Radar can't see. Kubernetes deletes it only if that owner is gone too."
          />
        )}

        {!cascadeLoading && cascadeDetail?.controllerTeardown && (
          <ControllerTeardownNotice teardown={cascadeDetail.controllerTeardown} force={forceDelete} />
        )}

        {!cascadeLoading && cascadeRootResolved === false && (
          <div className="flex items-start gap-2 rounded border border-theme-border bg-theme-elevated px-3 py-2 text-xs text-warning-text">
            <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
            <span>
              Radar couldn&apos;t verify which dependent resources Kubernetes will also delete. Additional resources may be deleted but aren&apos;t shown here.
            </span>
          </div>
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

function ControllerTeardownNotice({ teardown, force }: { teardown: NonNullable<CascadeDetail['controllerTeardown']>; force: boolean }) {
  const resources = teardown.resources ?? []
  const what = teardown.action === 'uninstall' ? 'uninstall the Helm release' : 'delete the resources it manages'
  if (force) {
    return (
      <div className="flex items-start gap-2 rounded border border-theme-border bg-theme-elevated px-3 py-2 text-xs text-theme-text-secondary">
        <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
        <span>
          {teardown.terminating
            ? `This is already being deleted, so ${teardown.controller} may already be working to ${what}. Force delete removes its finalizer but can't undo that.`
            : `Force delete removes ${teardown.controller}'s finalizer, so ${teardown.controller} won't ${what}. They stay in the cluster.`}
        </span>
      </div>
    )
  }
  const title = teardown.action === 'uninstall'
    ? `${teardown.controller} will also uninstall the Helm release`
    : `${teardown.controller} will also delete ${resources.length > 0 ? `up to ${pluralize(resources.length, 'managed resource')}` : 'the resources it manages'}`
  return (
    <CascadeDependentsList
      dependents={resources}
      title={title}
      note={`${teardown.controller} deletes what its own inventory records, which can include resources Radar doesn't show. Resources that opt out of deletion stay (Flux: kustomize.toolkit.fluxcd.io/prune: disabled; Argo CD: Delete=false).`}
    />
  )
}

function CascadeDependentsList({ dependents, title, note }: { dependents: CascadeDependent[]; title: string; note?: string }) {
  const [expanded, setExpanded] = useState(false)
  const { panelId, buttonProps } = useDisclosure(expanded)

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
        {...buttonProps}
        type="button"
        onClick={() => setExpanded(!expanded)}
        className="flex items-center gap-2 w-full px-3 py-2 text-left text-xs font-medium text-amber-400 hover:bg-amber-500/10 transition-colors"
      >
        <CollapseChevron open={expanded} inheritColor className="w-3.5 h-3.5" />
        <span>{title}</span>
      </button>

      <Collapse open={expanded} id={panelId}>
        <div className="px-3 pb-2.5 space-y-1.5">
          {grouped.map(([kind, names]) => (
            <div key={kind} className="text-xs">
              <span className="font-medium text-theme-text-primary">{kind}</span>
              <span className="text-theme-text-tertiary ml-1">({names.length})</span>
              <div className="ml-3 mt-0.5 text-theme-text-secondary font-mono break-all">
                {names.slice(0, MAX_NAMES_PER_KIND).join(', ')}
                {names.length > MAX_NAMES_PER_KIND && (
                  <span className="text-theme-text-tertiary"> +{names.length - MAX_NAMES_PER_KIND} more</span>
                )}
              </div>
            </div>
          ))}
          {note && <p className="text-xs text-theme-text-tertiary">{note}</p>}
        </div>
      </Collapse>
    </div>
  )
}
