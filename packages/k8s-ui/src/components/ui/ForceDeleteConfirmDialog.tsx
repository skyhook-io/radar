import { useState, useMemo } from 'react'
import { Loader2 } from 'lucide-react'
import { Collapse, CollapseChevron, useDisclosure } from './Collapse'
import { ConfirmDialog } from './ConfirmDialog'
import { AlertBanner } from './drawer-components'
import { pluralize } from '../../utils/pluralize'

export interface CascadeDependent {
  kind: string
  namespace: string
  name: string
  group?: string
}

/** Cached deletion evidence; all lists are observations, not completion guarantees. */
export interface CascadeDetail {
  propagation?: string
  coverage?: { complete: boolean; narrowed?: boolean; truncated?: boolean; notChecked?: unknown[] }
  unavailableReason?: string
  notEnumerated?: string[]
  /** "ownerReferences" when dependents are garbage-collection results; absent from older servers. */
  basis?: string
  /** Deleted unless another owner Radar can't see is still live. */
  possibleDependents?: CascadeDependent[]
  /** What Argo CD or Flux deletes through its finalizer. */
  controllerTeardown?: {
    controller: string
    action: 'prune' | 'uninstall' | string
    destination: 'local' | 'remote' | 'unknown'
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
  cascadePreviewFailed?: boolean
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
  cascadePreviewFailed,
}: ForceDeleteConfirmDialogProps) {
  const [forceDelete, setForceDelete] = useState(false)
  const orphaning = cascadeDetail?.propagation === 'Orphan' && !forceDelete
  const isNamespace = resourceKind.toLowerCase() === 'namespace'
  const isCRD = ['customresourcedefinition', 'customresourcedefinitions'].includes(resourceKind.toLowerCase())

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
      details={isNamespace ? `This will permanently delete the namespace "${resourceName}" and all resources in it.` : isCRD ? `This deletes the CRD and ALL its instances across the cluster, including those not shown here.` : namespaceName
        ? `This will permanently delete the ${resourceKind} "${resourceName}" from the "${namespaceName}" namespace.`
        : `This will permanently delete the cluster-scoped ${resourceKind} "${resourceName}".`}
      confirmLabel={forceDelete ? 'Force Delete' : 'Delete'}
      variant="danger"
      isLoading={isLoading}
      confirmDisabled={cascadeLoading}
    >
      <div className="flex flex-col gap-3 pb-1">
        {cascadeLoading && (
          <div className="flex items-center gap-2 text-xs text-theme-text-tertiary">
            <Loader2 className="w-3.5 h-3.5 animate-spin" />
            Checking for dependent resources...
          </div>
        )}

        {!cascadeLoading && !cascadePreviewFailed && !orphaning && cascadeDependents && cascadeDependents.length > 0 && (
          cascadeDetail?.basis === 'ownerReferences' ? (
            <CascadeDependentsList
              dependents={cascadeDependents}
              title={`Garbage collection is expected to delete ${pluralize(cascadeDependents.length, 'dependent resource')}`}
              note="Kubernetes deletes these through their owner references. Finalizers may delay deletion. This is an observation of the current cache, not a guarantee of completion."
            />
          ) : (
            <CascadeDependentsList
              dependents={cascadeDependents}
              title={`May also delete ${pluralize(cascadeDependents.length, 'related resource')}`}
              note="This Radar version can't tell owned resources from other links. Kubernetes deletes only those that name this one as their owner."
            />
          )
        )}

        {!cascadeLoading && !cascadePreviewFailed && !orphaning && cascadeDetail?.possibleDependents && cascadeDetail.possibleDependents.length > 0 && (
          <CascadeDependentsList
            dependents={cascadeDetail.possibleDependents}
            title={`May also delete ${pluralize(cascadeDetail.possibleDependents.length, 'resource')}`}
            note="Another owner or a chain of owners could not be resolved. Kubernetes deletes these only if no live owner remains."
          />
        )}

        {!cascadeLoading && !cascadePreviewFailed && cascadeDetail?.coverage && !orphaning && !cascadeDetail.coverage.complete ? (
          <p className="text-xs text-theme-text-secondary">This preview is partial: Radar may not collect or you may not be allowed to read all dependents. Additional resources may be deleted.</p>
        ) : null}
        {!cascadeLoading && !cascadePreviewFailed && cascadeDetail?.controllerTeardown && (
          <ControllerTeardownNotice teardown={cascadeDetail.controllerTeardown} force={forceDelete} />
        )}

        {!cascadeLoading && !cascadePreviewFailed && cascadeRootResolved === false && (
          <AlertBanner
            variant="warning"
            title="Dependent resources unverified"
            message="Radar couldn't verify which dependent resources Kubernetes will also delete. Additional resources may be deleted but aren't shown here."
            className="mb-0"
          />
        )}

        {!cascadeLoading && !cascadePreviewFailed && orphaning && <p className="text-xs text-theme-text-secondary">Keeping the pending orphan policy: owner-reference dependents are orphaned. Controller cleanup may still delete resources.</p>}
        {!cascadeLoading && !cascadePreviewFailed && forceDelete && cascadeDetail?.propagation === 'Orphan' && <p className="text-xs text-warning-text">Force delete removes the orphan finalizer and uses Background propagation. Owner-reference dependents may now be garbage collected.</p>}
        {!cascadeLoading && !cascadePreviewFailed && !orphaning && cascadeDetail?.coverage?.complete && cascadeDetail.coverage.narrowed && <p className="text-xs text-theme-text-secondary">Only resources Radar observes and you can read are listed.</p>}
        {!cascadeLoading && cascadePreviewFailed && <AlertBanner variant="warning" title="Deletion preview unavailable" message="Additional resources may be deleted." className="mb-0" />}
        {!cascadeLoading && !cascadePreviewFailed && cascadeDetail?.notEnumerated?.map(item => <p key={item} className="text-xs text-theme-text-secondary">Not enumerated: {item}.</p>)}

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

// How a managed resource opts out of its controller's teardown. A Helm
// uninstall keeps resources by Helm's own annotation, not Flux's.
function optOut(teardown: NonNullable<CascadeDetail['controllerTeardown']>): string | undefined {
  if (teardown.action === 'uninstall') return 'helm.sh/resource-policy: keep'
  if (teardown.controller === 'Argo CD') return 'argocd.argoproj.io/sync-options: Delete=false'
  if (teardown.controller === 'Flux') return 'kustomize.toolkit.fluxcd.io/prune: disabled'
  return undefined
}

function ControllerTeardownNotice({ teardown, force }: { teardown: NonNullable<CascadeDetail['controllerTeardown']>; force: boolean }) {
  const resources = teardown.resources ?? []
  const destinationNotice = teardown.destination === 'remote'
    ? 'This controller targets a destination Radar cannot confirm is this cluster. Radar cannot list what it will delete there.'
    : teardown.destination === 'unknown'
      ? 'The destination cluster is unknown. Radar cannot show where controller cleanup will run.'
      : ''
  const what = teardown.action === 'uninstall' ? 'uninstall the Helm release' : 'delete the resources it manages'
  if (force) {
    return (
      <AlertBanner
        variant="warning"
        title="Controller cleanup may be skipped"
        message={<>
          {destinationNotice && <>{destinationNotice} </>}
          {teardown.terminating
            ? `This is already being deleted, so ${teardown.controller} may already be working to ${what}. Force delete removes its finalizer but can't undo that.`
            : `Force delete removes ${teardown.controller}'s finalizer, so ${teardown.controller}'s remaining cleanup may be skipped. Cleanup may already have started; removing the finalizer cannot undo it.`}
        </>}
        className="mb-0"
      />
    )
  }
  const title = teardown.destination === 'unknown'
    ? `${teardown.controller} has a cleanup finalizer`
    : teardown.action === 'uninstall'
    ? `${teardown.controller} is configured to uninstall the Helm release`
    : `${teardown.controller} is configured to prune managed resources`
  return (
    <CascadeDependentsList
      dependents={resources}
      title={resources.length > 0 ? `${title} (${resources.length} shown)` : title}
      summary={`${destinationNotice ? destinationNotice + ' ' : ''}${teardown.action === 'uninstall' ? 'Helm uninstalls resources in its stored release manifest' : `${teardown.controller} deletes what its own inventory records`}, which can include resources Radar doesn't show.${optOut(teardown) ? ` Resources marked ${optOut(teardown)} stay.` : ''}`}
    />
  )
}

// `note` explains the expanded list; `summary` is a caveat that must read
// without expanding it.
function CascadeDependentsList({ dependents, title, note, summary }: { dependents: CascadeDependent[]; title: string; note?: string; summary?: string }) {
  const [expanded, setExpanded] = useState(false)
  const { panelId, buttonProps } = useDisclosure(expanded)

  const grouped = useMemo(() => {
    const map = new Map<string, string[]>()
    for (const dep of dependents) {
      const kind = dep.group ? `${dep.kind} (${dep.group})` : dep.kind
      if (!map.has(kind)) map.set(kind, [])
      map.get(kind)!.push(dep.namespace ? `${dep.namespace}/${dep.name}` : dep.name)
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
      {summary && <p className="px-3 pb-2 text-xs text-theme-text-secondary">{summary}</p>}

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
