import { useEffect, useMemo, useRef, useState } from 'react'
import {
  AlertTriangle,
  ExternalLink,
  Loader2,
  RefreshCw,
  X,
} from 'lucide-react'
import { clsx } from 'clsx'

import type {
  WorkloadContainerImage,
  WorkloadImageInventory,
  WorkloadImageUpdate,
  WorkloadUpdateBehavior,
} from '../../types/core'
import { DialogPortal } from '../ui/DialogPortal'
import { Badge } from '../ui/Badge'

export interface ManagedImageSource {
  type: 'GitOps' | 'Helm'
  label: string
  onOpen?: () => void
}

export interface SetImageDialogProps {
  open: boolean
  workloadLabel: string
  workloadName: string
  workloadResource: string
  managedSources?: ManagedImageSource[]
  pending?: boolean
  onClose: () => void
  onLoad: () => Promise<WorkloadImageInventory>
  onConfirm: (updates: WorkloadImageUpdate[]) => Promise<unknown>
}

function containerKey(
  container: Pick<WorkloadContainerImage, 'type' | 'name'>,
): string {
  return `${container.type}\u0000${container.name}`
}

export function changedImageUpdates(
  containers: WorkloadContainerImage[],
  drafts: Record<string, string>,
): WorkloadImageUpdate[] {
  return containers.flatMap((container) => {
    const image = (drafts[containerKey(container)] ?? container.image).trim()
    if (!image || image === container.image) return []
    return [
      {
        type: container.type,
        name: container.name,
        previousImage: container.image,
        image,
      },
    ]
  })
}

export function reconcileRefreshedImageDrafts(
  previousContainers: WorkloadContainerImage[],
  nextContainers: WorkloadContainerImage[],
  drafts: Record<string, string>,
): { drafts: Record<string, string>; changedCurrentKeys: string[] } {
  const previousByKey = new Map(
    previousContainers.map((container) => [containerKey(container), container]),
  )
  const nextDrafts: Record<string, string> = {}
  const changedCurrentKeys: string[] = []

  for (const container of nextContainers) {
    const key = containerKey(container)
    const previous = previousByKey.get(key)
    const priorDraft = drafts[key] ?? previous?.image ?? container.image
    const wasEdited = Boolean(previous) && priorDraft.trim() !== previous?.image
    nextDrafts[key] = wasEdited ? priorDraft : container.image
    if (previous && previous.image !== container.image)
      changedCurrentKeys.push(key)
  }

  return { drafts: nextDrafts, changedCurrentKeys }
}

export function canSubmitImageUpdates({
  updateCount,
  hasEmptyImage,
  managed,
  ownershipResolved,
  acknowledged,
  busy,
  loadFailed = false,
}: {
  updateCount: number
  hasEmptyImage: boolean
  managed: boolean
  ownershipResolved: boolean
  acknowledged: boolean
  busy: boolean
  loadFailed?: boolean
}): boolean {
  return (
    updateCount > 0 &&
    !hasEmptyImage &&
    ownershipResolved &&
    (!managed || acknowledged) &&
    !busy &&
    !loadFailed
  )
}

export function describeImageUpdateBehavior(
  behavior: WorkloadUpdateBehavior,
): string {
  switch (behavior.type) {
    case 'paused':
      return 'This controller is paused. Radar will save the template change, but no rollout starts until it is resumed.'
    case 'recreate':
      return 'This Deployment uses Recreate. Existing Pods are stopped before replacement Pods start.'
    case 'onDelete':
      return 'This controller uses OnDelete. Existing Pods keep running until they are deleted.'
    case 'partitioned':
      return `Only StatefulSet Pods with ordinal ${behavior.partition ?? 0} or higher update automatically.`
    case 'canary':
      return behavior.gated
        ? 'Saving starts a new Argo canary using the Rollout’s configured steps.'
        : 'This Rollout has no canary steps. Saving rolls the new revision out to 100% without a pause.'
    case 'blueGreen':
      return behavior.autoPromote
        ? 'Saving creates a new Argo preview revision, which Argo promotes to the active Service automatically once it is ready.'
        : 'Saving creates a new Argo preview revision. Promotion remains a separate action.'
    default:
      return 'Saving changes the Pod template and starts the controller’s rolling update.'
  }
}

export function SetImageDialog({
  open,
  workloadLabel,
  workloadName,
  workloadResource,
  managedSources,
  pending = false,
  onClose,
  onLoad,
  onConfirm,
}: SetImageDialogProps) {
  const [inventory, setInventory] = useState<WorkloadImageInventory | null>(
    null,
  )
  const [drafts, setDrafts] = useState<Record<string, string>>({})
  const [loading, setLoading] = useState(false)
  const [submitting, setSubmitting] = useState(false)
  const [loadError, setLoadError] = useState<string | null>(null)
  const [submitError, setSubmitError] = useState<string | null>(null)
  const [acknowledged, setAcknowledged] = useState(false)
  const [changedCurrentKeys, setChangedCurrentKeys] = useState<string[]>([])
  const onLoadRef = useRef(onLoad)
  const loadRequestRef = useRef(0)

  useEffect(() => {
    onLoadRef.current = onLoad
  }, [onLoad])

  const loadInventory = async (preserveDrafts = false): Promise<boolean> => {
    const request = ++loadRequestRef.current
    const previousInventory = inventory
    const previousDrafts = drafts
    setLoading(true)
    setLoadError(null)
    try {
      const next = await onLoadRef.current()
      if (request !== loadRequestRef.current) return false
      setInventory(next)
      if (preserveDrafts && previousInventory) {
        const reconciled = reconcileRefreshedImageDrafts(
          previousInventory.containers,
          next.containers,
          previousDrafts,
        )
        setDrafts(reconciled.drafts)
        setChangedCurrentKeys(reconciled.changedCurrentKeys)
      } else {
        setDrafts(
          Object.fromEntries(
            next.containers.map((container) => [
              containerKey(container),
              container.image,
            ]),
          ),
        )
        setChangedCurrentKeys([])
      }
      return true
    } catch (error) {
      if (request !== loadRequestRef.current) return false
      setLoadError(
        error instanceof Error
          ? error.message
          : 'Failed to load container images',
      )
      return false
    } finally {
      if (request === loadRequestRef.current) setLoading(false)
    }
  }

  useEffect(() => {
    if (!open) {
      loadRequestRef.current += 1
      return
    }
    setInventory(null)
    setDrafts({})
    setChangedCurrentKeys([])
    setSubmitError(null)
    setAcknowledged(false)
    void loadInventory()
  }, [open, workloadLabel, workloadName, workloadResource])

  const updates = useMemo(
    () => changedImageUpdates(inventory?.containers ?? [], drafts),
    [drafts, inventory?.containers],
  )
  const hasEmptyImage =
    inventory?.containers.some(
      (container) => !(drafts[containerKey(container)] ?? '').trim(),
    ) ?? false
  const busy = pending || submitting
  const ownershipResolved = managedSources !== undefined
  const canSubmit = canSubmitImageUpdates({
    updateCount: updates.length,
    hasEmptyImage,
    managed: (managedSources?.length ?? 0) > 0,
    ownershipResolved,
    acknowledged,
    busy,
    loadFailed: Boolean(loadError),
  })

  const handleSubmit = async () => {
    if (!canSubmit) return
    setSubmitting(true)
    setSubmitError(null)
    try {
      await onConfirm(updates)
      onClose()
    } catch (error) {
      const message =
        error instanceof Error
          ? error.message
          : 'Failed to update container images'
      if (
        typeof error === 'object' &&
        error !== null &&
        'status' in error &&
        error.status === 409
      ) {
        const refreshed = await loadInventory(true)
        const sentence = /[.!?]$/.test(message.trim())
          ? message.trim()
          : `${message.trim()}.`
        setSubmitError(
          refreshed
            ? `${sentence} Current images were refreshed; your proposed images were preserved.`
            : `${sentence} Reload the current images before trying again.`,
        )
      } else {
        setSubmitError(message)
      }
    } finally {
      setSubmitting(false)
    }
  }

  const regular =
    inventory?.containers.filter(
      (container) => container.type === 'container',
    ) ?? []
  const init =
    inventory?.containers.filter(
      (container) => container.type === 'initContainer',
    ) ?? []
  const targetDiffers =
    inventory &&
    (inventory.target.name !== workloadName ||
      inventory.target.resource !== workloadResource.toLowerCase())

  return (
    <DialogPortal
      open={open}
      onClose={busy ? () => {} : onClose}
      closable={!busy}
      className="w-[760px] max-w-[calc(100vw-2rem)] max-h-[85vh] flex flex-col"
    >
      <div className="flex items-start justify-between gap-4 border-b border-theme-border px-5 py-4">
        <div className="min-w-0">
          <h2 className="text-sm font-semibold text-theme-text-primary">
            Update container images
          </h2>
          <p className="mt-0.5 truncate text-xs text-theme-text-tertiary">
            {workloadLabel}
          </p>
        </div>
        <button
          type="button"
          onClick={onClose}
          disabled={busy}
          aria-label="Close"
          className="rounded p-1 text-theme-text-tertiary hover:bg-theme-hover hover:text-theme-text-primary disabled:opacity-50"
        >
          <X className="h-4 w-4" />
        </button>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto px-5 py-4">
        {loading && !inventory ? (
          <div className="flex items-center justify-center gap-2 py-12 text-sm text-theme-text-secondary">
            <Loader2 className="h-4 w-4 animate-spin" />
            Loading current images…
          </div>
        ) : loadError && !inventory ? (
          <div className="rounded-lg border border-theme-border bg-theme-base px-4 py-6 text-center">
            <p className="text-sm text-theme-text-primary">
              Couldn’t load container images
            </p>
            <p className="mt-1 text-xs text-theme-text-tertiary">{loadError}</p>
            <button
              type="button"
              onClick={() => void loadInventory()}
              className="mt-3 inline-flex items-center gap-1.5 rounded-md px-3 py-1.5 text-xs btn-brand-muted"
            >
              <RefreshCw className="h-3.5 w-3.5" />
              Retry
            </button>
          </div>
        ) : inventory ? (
          <div className="space-y-4">
            {loadError && (
              <div className="rounded-md border border-theme-border bg-theme-base px-3 py-2 text-xs">
                <p className="font-medium text-theme-text-primary">Refresh required</p>
                <p className="mt-1 text-theme-text-secondary">
                  Radar couldn’t verify the latest images and ownership. {loadError}
                </p>
                <button
                  type="button"
                  onClick={() => void loadInventory(true)}
                  className="mt-2 inline-flex items-center gap-1 rounded-md px-2 py-1 font-medium btn-brand-muted"
                >
                  <RefreshCw className="h-3.5 w-3.5" />
                  Retry refresh
                </button>
              </div>
            )}
            {!ownershipResolved && (
              <div className="rounded-md border border-theme-border bg-theme-base px-3 py-2 text-xs">
                <p className="font-medium text-theme-text-primary">Management ownership unavailable</p>
                <p className="mt-1 text-theme-text-secondary">
                  The host must verify GitOps and Helm ownership before Radar can safely update a live image.
                </p>
              </div>
            )}
            {targetDiffers && (
              <div className="rounded-md border border-theme-border bg-theme-base px-3 py-2 text-xs text-theme-text-secondary">
                This Rollout reads its Pod template from{' '}
                <span className="font-mono text-theme-text-primary">
                  {inventory.target.kind} {inventory.target.namespace}/
                  {inventory.target.name}
                </span>
                . That referenced object will be updated.
              </div>
            )}

            <div className="rounded-md border border-skyhook-500/30 bg-skyhook-500/10 px-3 py-2 text-xs text-theme-text-secondary">
              {describeImageUpdateBehavior(inventory.behavior)}
            </div>

            {managedSources && managedSources.length > 0 && (
              <div className="rounded-md border border-amber-500/40 bg-amber-500/10 px-3 py-3">
                <div className="flex items-start gap-2">
                  <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-amber-600 dark:text-amber-400" />
                  <div className="min-w-0 flex-1">
                    <p className="text-xs font-medium text-amber-800 dark:text-amber-300">
                      The declared source may overwrite this live change
                    </p>
                    <p className="mt-1 text-xs text-theme-text-secondary">
                      Managed by{' '}
                      {managedSources
                        .map((source) => `${source.type} ${source.label}`)
                        .join(' and ')}
                      . Update the declared source for a durable change; use
                      this action only as a break-glass operation.
                    </p>
                    <div className="mt-2 flex flex-wrap gap-2">
                      {managedSources
                        .filter((source) => source.onOpen)
                        .map((source) => (
                          <button
                            key={`${source.type}-${source.label}`}
                            type="button"
                            onClick={source.onOpen}
                            className="inline-flex items-center gap-1 text-xs font-medium text-skyhook-600 hover:underline dark:text-skyhook-400"
                          >
                            {source.type === 'GitOps'
                              ? 'View GitOps owner'
                              : 'View Helm release'}
                            <ExternalLink className="h-3 w-3" />
                          </button>
                        ))}
                    </div>
                    <label className="mt-3 flex cursor-pointer items-start gap-2 text-xs text-theme-text-primary">
                      <input
                        type="checkbox"
                        checked={acknowledged}
                        onChange={(event) =>
                          setAcknowledged(event.target.checked)
                        }
                        disabled={busy}
                        className="mt-0.5 h-3.5 w-3.5 accent-skyhook-500"
                      />
                      <span>
                        I understand the declared source may overwrite this live
                        change.
                      </span>
                    </label>
                  </div>
                </div>
              </div>
            )}

            <ContainerImageGroup
              title="Containers"
              containers={regular}
              drafts={drafts}
              changedCurrentKeys={changedCurrentKeys}
              disabled={busy}
              onChange={(container, image) =>
                setDrafts((current) => ({
                  ...current,
                  [containerKey(container)]: image,
                }))
              }
            />
            {init.length > 0 && (
              <ContainerImageGroup
                title="Init containers"
                containers={init}
                drafts={drafts}
                changedCurrentKeys={changedCurrentKeys}
                disabled={busy}
                onChange={(container, image) =>
                  setDrafts((current) => ({
                    ...current,
                    [containerKey(container)]: image,
                  }))
                }
              />
            )}
            {submitError && (
              <div className="rounded-md border border-red-500/40 bg-red-500/10 px-3 py-2 text-xs text-red-700 dark:text-red-400">
                {submitError}
              </div>
            )}
          </div>
        ) : null}
      </div>

      <div className="flex items-center justify-between gap-3 border-t border-theme-border bg-theme-base px-5 py-3">
        <p className="text-xs text-theme-text-tertiary">
          {hasEmptyImage
            ? 'Enter an image for every container'
            : updates.length
              ? `${updates.length} image${updates.length === 1 ? '' : 's'} changed`
              : 'No changes'}
        </p>
        <div className="flex items-center gap-2">
          <button
            type="button"
            onClick={onClose}
            disabled={busy}
            className="rounded-md border border-theme-border bg-theme-surface px-3 py-1.5 text-xs text-theme-text-secondary hover:bg-theme-hover hover:text-theme-text-primary disabled:opacity-50"
          >
            Cancel
          </button>
          <button
            type="button"
            onClick={() => void handleSubmit()}
            disabled={!canSubmit}
            className="inline-flex items-center gap-1.5 rounded-md px-3 py-1.5 text-xs btn-brand disabled:cursor-not-allowed disabled:opacity-50"
          >
            {busy && <Loader2 className="h-3.5 w-3.5 animate-spin" />}
            {busy
              ? 'Updating…'
              : `Update ${updates.length || ''} image${updates.length === 1 ? '' : 's'}`.replace(
                  '  ',
                  ' ',
                )}
          </button>
        </div>
      </div>
    </DialogPortal>
  )
}

function ContainerImageGroup({
  title,
  containers,
  drafts,
  changedCurrentKeys,
  disabled,
  onChange,
}: {
  title: string
  containers: WorkloadContainerImage[]
  drafts: Record<string, string>
  changedCurrentKeys: string[]
  disabled: boolean
  onChange: (container: WorkloadContainerImage, image: string) => void
}) {
  return (
    <section>
      <h3 className="mb-2 text-xs font-semibold uppercase tracking-wide text-theme-text-tertiary">
        {title}
      </h3>
      <div className="overflow-hidden rounded-lg border border-theme-border">
        <div className="hidden grid-cols-[minmax(100px,0.6fr)_minmax(0,1fr)_minmax(0,1.2fr)] gap-3 border-b border-theme-border bg-theme-base px-3 py-2 text-[11px] font-medium text-theme-text-tertiary sm:grid">
          <span>Name</span>
          <span>Current image</span>
          <span>New image</span>
        </div>
        <div className="divide-y divide-theme-border">
          {containers.map((container) => {
            const key = containerKey(container)
            const empty = !(drafts[key] ?? container.image).trim()
            const currentChanged = changedCurrentKeys.includes(key)
            return (
              <div
                key={key}
                className="grid gap-2 px-3 py-3 sm:grid-cols-[minmax(100px,0.6fr)_minmax(0,1fr)_minmax(0,1.2fr)] sm:items-center sm:gap-3"
              >
                <div className="min-w-0 font-mono text-xs font-medium text-theme-text-primary">
                  {container.name}
                </div>
                <div className="min-w-0">
                  <div className="break-all font-mono text-[11px] text-theme-text-tertiary">
                    {container.image}
                  </div>
                  {currentChanged && (
                    <Badge severity="warning" size="sm" className="mt-1">
                      Changed in cluster
                    </Badge>
                  )}
                </div>
                <input
                  type="text"
                  value={drafts[key] ?? container.image}
                  onChange={(event) => onChange(container, event.target.value)}
                  disabled={disabled}
                  aria-label={`New image for ${container.name}`}
                  aria-invalid={empty || undefined}
                  spellCheck={false}
                  className={clsx(
                    'min-w-0 rounded-md border bg-theme-base px-2.5 py-2 font-mono text-xs text-theme-text-primary outline-none focus:ring-1 disabled:opacity-50',
                    empty
                      ? 'border-red-500/60 focus:border-red-500 focus:ring-red-500'
                      : 'border-theme-border focus:border-skyhook-500 focus:ring-skyhook-500',
                  )}
                />
              </div>
            )
          })}
        </div>
      </div>
    </section>
  )
}
