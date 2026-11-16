import { useState } from 'react'
import { Play } from 'lucide-react'
import { ConfirmDialog, Tooltip, veleroRetentionText, type ResourceRef, type VeleroScheduleReading } from '@skyhook-io/k8s-ui'
import { useRadarFeature } from '../../api/client'
import { useVeleroBackupNow } from '../../api/velero'
import { VELERO_GROUP } from './routes'

// Velero writes the schedule's name into a label on every backup it makes.
const MAX_LABEL_VALUE = 63

/** The name Velero's CLI gives a backup from a schedule: <schedule>-<UTC timestamp>. */
function backupNameAt(schedule: string, at: Date): string {
  return `${schedule}-${at.toISOString().replace(/[-:T]/g, '').slice(0, 14)}`
}

function list(values: unknown): string[] {
  return Array.isArray(values) ? values.filter((v): v is string => typeof v === 'string') : []
}

/**
 * "Back up now" for a Velero Schedule: creates a Backup from the schedule's
 * template, as `velero backup create --from-schedule` does. The confirmation
 * shows what will be backed up, and the request is bound to that version of
 * the schedule.
 */
export function VeleroBackupNow({ schedule, reading, onNavigate }: {
  schedule: any
  reading?: VeleroScheduleReading
  onNavigate?: (ref: ResourceRef) => void
}) {
  const { support } = useRadarFeature('veleroBackupNow')
  // What the confirmation shows, frozen when it opens: a refresh while it is
  // open must not swap in a schedule the user did not review.
  const [reviewed, setReviewed] = useState<{ resourceVersion: string; template: any; backupName: string } | null>(null)
  const backupNow = useVeleroBackupNow()
  const namespace: string = schedule?.metadata?.namespace ?? ''
  const name: string = schedule?.metadata?.name ?? ''
  const resourceVersion: string = schedule?.metadata?.resourceVersion ?? ''
  if (support !== 'supported' || !namespace || !name) return null

  const open = reviewed !== null
  const changed = open && resourceVersion !== reviewed.resourceVersion
  const template = (open ? reviewed.template : schedule?.spec?.template) ?? {}
  const included = list(template.includedNamespaces)
  const excluded = list(template.excludedNamespaces)
  const namespaces = included.length === 0 || included.includes('*') ? 'All namespaces' : included.join(', ')
  const location: string = template.storageLocation || reading?.location?.name || ''
  const cannotRun = name.length > MAX_LABEL_VALUE || reading?.state === 'cannotRun'
  const serverDown = reading?.reason === 'serverDown'
  const created = backupNow.data

  const button = (
    <button
      type="button"
      onClick={() => {
        backupNow.reset()
        setReviewed({ resourceVersion, template: schedule?.spec?.template ?? {}, backupName: backupNameAt(name, new Date()) })
      }}
      disabled={cannotRun || !resourceVersion}
      className="btn-secondary inline-flex items-center gap-1.5 px-2.5 py-1 text-xs disabled:opacity-50"
    >
      <Play className="h-3.5 w-3.5" aria-hidden />
      Back up now
    </button>
  )

  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-1 px-1 pb-3 text-xs">
      {cannotRun ? (
        <Tooltip content="This schedule's name is longer than 63 characters, so Velero cannot label a backup from it and the backup would fail.">
          <span>{button}</span>
        </Tooltip>
      ) : (
        button
      )}
      {created && (
        <span className="text-theme-text-secondary">
          Backup{' '}
          <button
            type="button"
            className="font-mono text-accent-text hover:underline"
            onClick={() => onNavigate?.({ kind: 'Backup', group: VELERO_GROUP, namespace: created.namespace, name: created.name })}
          >
            {created.name}
          </button>{' '}
          created{serverDown ? '; it waits until the Velero server is running' : ''}.
        </span>
      )}
      <ConfirmDialog
        open={open}
        onClose={() => setReviewed(null)}
        onConfirm={() =>
          reviewed &&
          backupNow.mutate(
            { namespace, name, resourceVersion: reviewed.resourceVersion, backupName: reviewed.backupName },
            { onSuccess: () => setReviewed(null) },
          )
        }
        title="Back up now"
        message={`Create a backup now with schedule ${name}'s settings.`}
        confirmLabel="Back up now"
        variant="default"
        isLoading={backupNow.isPending}
        confirmDisabled={changed}
      >
        <div className="space-y-3 px-4 pb-1 text-sm">
          <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1">
            <dt className="text-theme-text-tertiary">Backs up</dt>
            <dd className="break-words">{namespaces}{excluded.length > 0 ? ` (except ${excluded.join(', ')})` : ''}</dd>
            <dt className="text-theme-text-tertiary">Stored in</dt>
            <dd className="break-words">{location ? location : 'The default storage location'}</dd>
            <dt className="text-theme-text-tertiary">Kept for</dt>
            <dd>{template.ttl ? veleroRetentionText(template.ttl) : "The Velero server's default (30 days unless it was changed)"}</dd>
          </dl>
          <p className="text-theme-text-secondary">
            The schedule's next run waits while this backup is queued or running.
          </p>
          {changed && (
            <p className="text-warning-text">This schedule changed while this was open. Close it and review the schedule again.</p>
          )}
          {serverDown && (
            <p className="text-warning-text">The Velero server is not running, so the backup waits until it starts.</p>
          )}
        </div>
      </ConfirmDialog>
    </div>
  )
}
