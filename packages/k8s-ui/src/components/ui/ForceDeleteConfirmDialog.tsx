import { useState, useMemo } from 'react'
import { AlertTriangle, Info, Loader2 } from 'lucide-react'
import { Collapse, CollapseChevron, useDisclosure } from './Collapse'
import { ConfirmDialog } from './ConfirmDialog'
import { pluralize } from '../../utils/pluralize'

export interface CascadeDependent {
  kind: string
  namespace: string
  name: string
  group?: string
}

export const FINALIZER_ORPHAN_WARNING =
  'Removing finalizers deletes the resource without the cleanup they were waiting for. Anything their controllers manage outside Kubernetes, such as a cloud instance or a volume, may be left behind.'

export interface ForceDeleteCopy {
  checkboxLabel: string
  /** What holds the resource today. Null when nothing does. */
  finalizerNote: string | null
  /** What force delete will do here. Shown only while force is checked. */
  forceNote: { tone: 'warning' | 'info'; text: string } | null
}

// Force delete removes finalizers only from a resource that is already being
// deleted; on a live one it is a grace-period-0 delete that still waits for
// them. The copy has to match, or users expect one and get the other.
// `safeForceDelete` is false for a Radar that predates that rule and removes
// finalizers on every force delete.
export function forceDeleteCopy(finalizers: string[], terminating: boolean, safeForceDelete: boolean): ForceDeleteCopy {
  const names = finalizers.join(', ')
  // An older Radar removes whatever finalizers the resource has when the
  // delete runs, which may be more than the dialog was shown.
  if (!safeForceDelete && !terminating) {
    return {
      checkboxLabel: 'Force delete (remove finalizers)',
      finalizerNote: finalizers.length > 0
        ? `Finalizers: ${names}. A normal delete waits until the controllers that own them finish cleaning up, which can take a few minutes.`
        : null,
      forceNote: { tone: 'warning', text: FINALIZER_ORPHAN_WARNING },
    }
  }
  if (finalizers.length === 0) {
    return {
      checkboxLabel: safeForceDelete ? 'Force delete (no grace period)' : 'Force delete (remove finalizers)',
      finalizerNote: null,
      forceNote: safeForceDelete ? null : { tone: 'warning', text: FINALIZER_ORPHAN_WARNING },
    }
  }
  if (terminating) {
    return {
      checkboxLabel: 'Force delete (remove finalizers)',
      finalizerNote: `Already being deleted. Waiting on finalizers: ${names}.`,
      forceNote: { tone: 'warning', text: FINALIZER_ORPHAN_WARNING },
    }
  }
  return {
    checkboxLabel: 'Force delete (no grace period)',
    finalizerNote: `Finalizers: ${names}. A normal delete waits until the controllers that own them finish cleaning up, which can take a few minutes.`,
    forceNote: {
      tone: 'info',
      text: 'Force delete does not remove finalizers from a resource that is not being deleted yet. Their controllers get to clean up first.',
    },
  }
}

interface ForceDeleteConfirmDialogProps {
  open: boolean
  onClose: () => void
  /**
   * `uid` is the object the dialog described; the server deletes nothing
   * else. `removeFinalizers` is set only when the dialog warned about
   * removing exactly those finalizers and the user confirmed force delete.
   * The server removes no finalizers without it.
   */
  onConfirm: (force: boolean, scope: { uid?: string; removeFinalizers?: string[] }) => void
  resourceName: string
  resourceKind: string
  namespaceName: string
  isLoading: boolean
  cascadeDependents?: CascadeDependent[]
  cascadeLoading?: boolean
  cascadeRootResolved?: boolean
  cascadePreviewFailed?: boolean
  finalizers?: string[]
  /** The resource already has a deletionTimestamp. */
  terminating?: boolean
  resourceUid?: string
  /**
   * The connected Radar removes finalizers only from a resource that is
   * already being deleted. Leave unset when unknown: the dialog then warns
   * that force delete removes finalizers.
   */
  safeForceDelete?: boolean
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
  cascadePreviewFailed,
  finalizers,
  terminating = false,
  resourceUid,
  safeForceDelete = false,
}: ForceDeleteConfirmDialogProps) {
  const [forceDelete, setForceDelete] = useState(false)
  const isNamespace = resourceKind.toLowerCase() === 'namespace'
  const [forceReset, setForceReset] = useState<'resource' | 'radar' | null>(null)
  const [resetAfterRequest, setResetAfterRequest] = useState<'resource' | 'radar' | null>(null)
  const copy = forceDeleteCopy(finalizers ?? [], terminating, safeForceDelete)

  // Checking force agrees to what the dialog said at that moment. If the
  // resource changes underneath (starts terminating, gains or loses a
  // finalizer, is replaced), or the connected Radar changes what force delete
  // does, that agreement no longer covers it.
  const resourceAs = JSON.stringify([resourceUid ?? '', terminating, finalizers ?? []])
  const [agreedResource, setAgreedResource] = useState(resourceAs)
  const [agreedSafe, setAgreedSafe] = useState(safeForceDelete)
  if (resourceAs !== agreedResource || safeForceDelete !== agreedSafe) {
    const resourceChanged = resourceAs !== agreedResource
    // Only losing safeForceDelete changes what checking force agreed to: a
    // live resource would now lose its finalizers. Gaining it (for example
    // once capabilities load) only makes force do less.
    const radarChanged = agreedSafe && !safeForceDelete
    setAgreedResource(resourceAs)
    setAgreedSafe(safeForceDelete)
    if (open && forceDelete && (resourceChanged || radarChanged)) {
      // While the delete is in flight the change is usually its own effect
      // (a live resource starting to terminate), so the checkbox stays put
      // until the request settles. If the dialog is still open then, the
      // request failed and a retry must not reuse the old agreement.
      if (isLoading) {
        setResetAfterRequest(resourceChanged ? 'resource' : 'radar')
      } else {
        setForceDelete(false)
        setForceReset(resourceChanged ? 'resource' : 'radar')
      }
    }
  }
  if (open && !isLoading && resetAfterRequest) {
    setForceDelete(false)
    setForceReset(resetAfterRequest)
    setResetAfterRequest(null)
  }
  if (!open && (forceDelete || forceReset || resetAfterRequest)) {
    setForceDelete(false)
    setForceReset(null)
    setResetAfterRequest(null)
  }

  function handleClose() {
    onClose()
    setForceDelete(false)
    setForceReset(null)
    setResetAfterRequest(null)
  }

  function handleConfirm() {
    // Every terminating branch of the copy warns about removing exactly these
    // finalizers. An older Radar ignores the consent and removes them anyway,
    // as the warning says; a newer one removes them only with it, including
    // when its capabilities haven't loaded yet.
    const consented = forceDelete && terminating && (finalizers?.length ?? 0) > 0
    onConfirm(forceDelete, { uid: resourceUid, removeFinalizers: consented ? finalizers : undefined })
  }

  return (
    <ConfirmDialog
      open={open}
      onClose={handleClose}
      onConfirm={handleConfirm}
      title="Delete Resource"
      message={`Are you sure you want to delete "${resourceName}"?`}
      details={isNamespace ? `This will permanently delete the namespace "${resourceName}" and the resources in it.` : `This will permanently delete the ${resourceKind} "${resourceName}" from the "${namespaceName}" namespace.`}
      confirmLabel={forceDelete ? 'Force Delete' : 'Delete'}
      variant="danger"
      isLoading={isLoading}
    >
      <div className="flex flex-col gap-3 pb-1">
        {cascadeLoading && (
          <div className="flex items-center gap-2 text-xs text-theme-text-tertiary">
            <Loader2 className="w-3.5 h-3.5 animate-spin" />
            Checking related resources...
          </div>
        )}

        {!cascadeLoading && !cascadePreviewFailed && cascadeDependents && cascadeDependents.length > 0 && (
          <CascadeDependentsList dependents={cascadeDependents} />
        )}

        {!cascadeLoading && cascadePreviewFailed && (
          <p className="text-xs text-theme-text-tertiary">
            Related-resource preview unavailable.
          </p>
        )}

        {!isNamespace && !cascadeLoading && !cascadePreviewFailed && cascadeRootResolved === false && (
          <p className="text-xs text-theme-text-tertiary">
            This resource is not in Radar&apos;s topology, so its related resources cannot be previewed.
          </p>
        )}

        {!cascadeLoading && !cascadePreviewFailed && cascadeRootResolved === true && cascadeDependents && cascadeDependents.length > 0 && (
          <p className="text-xs text-theme-text-secondary">
            This list shows visible related resources; it does not predict what will be deleted.
          </p>
        )}

        {copy.finalizerNote && (
          <p className="text-xs text-theme-text-secondary break-words">{copy.finalizerNote}</p>
        )}

        <label className="flex items-center gap-2 text-sm text-theme-text-secondary">
          <input
            type="checkbox"
            checked={forceDelete}
            onChange={(e) => {
              setForceDelete(e.target.checked)
              setForceReset(null)
            }}
            className="w-4 h-4 rounded border-theme-border bg-theme-base text-red-600 focus:ring-red-500 focus:ring-offset-0"
          />
          <span>{copy.checkboxLabel}</span>
        </label>

        {forceReset && (
          <p className="text-xs text-theme-text-secondary">
            {forceReset === 'radar'
              ? 'The Radar this is connected to changed how force delete works while this was open, so Force delete was unchecked. Read the details above before checking it again.'
              : 'The resource changed while this was open, so Force delete was unchecked. Read the details above before checking it again.'}
          </p>
        )}

        {forceDelete && copy.forceNote && (
          <div
            className={`flex items-start gap-2 rounded border border-theme-border bg-theme-elevated px-3 py-2 text-xs ${copy.forceNote.tone === 'warning' ? 'text-warning-text' : 'text-theme-text-secondary'}`}
          >
            {copy.forceNote.tone === 'warning'
              ? <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
              : <Info className="mt-0.5 h-3.5 w-3.5 shrink-0" />}
            <span>{copy.forceNote.text}</span>
          </div>
        )}
      </div>
    </ConfirmDialog>
  )
}

const MAX_NAMES_PER_KIND = 8

function CascadeDependentsList({ dependents }: { dependents: CascadeDependent[] }) {
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
        <span>
          May affect {pluralize(dependents.length, 'related resource')}
        </span>
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
        </div>
      </Collapse>
    </div>
  )
}
