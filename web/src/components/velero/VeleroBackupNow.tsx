import { useState } from 'react'
import { Play } from 'lucide-react'
import { ConfirmDialog, Tooltip, veleroRetentionText, veleroScheduleReasonText, type ResourceRef } from '@skyhook-io/k8s-ui'
import { showApiSuccess } from '../ui/Toast'
import { useRadarFeature } from '../../api/client'
import { useVeleroBackupNow, useVeleroScheduleActivity } from '../../api/velero'
import { VELERO_GROUP } from './routes'

// Velero writes the schedule's name into a label on every backup it makes.
const MAX_LABEL_VALUE = 63

const BUTTON =
  'btn-secondary inline-flex shrink-0 items-center gap-1.5 whitespace-nowrap px-2.5 py-1.5 text-xs font-medium disabled:cursor-not-allowed disabled:opacity-50'

/** The name Velero's CLI gives a backup from a schedule: <schedule>-<UTC timestamp>. */
function backupNameAt(schedule: string, at: Date): string {
  return `${schedule}-${at.toISOString().replace(/[-:T]/g, '').slice(0, 14)}`
}

function list(values: unknown): string[] {
  return Array.isArray(values) ? values.filter((v): v is string => typeof v === 'string') : []
}

/**
 * "Back up now" for a Velero Schedule, in the drawer's header with the other
 * actions: creates a Backup from the schedule's template, as `velero backup
 * create --from-schedule` does. The confirmation shows what will be backed up,
 * and the request is bound to that version of the schedule. It is unavailable
 * when the backup could not happen now: a schedule Velero can't run, or a
 * stopped Velero server, which would only queue it.
 */
export function VeleroBackupNow({ schedule, onNavigate }: {
  schedule: any
  onNavigate?: (ref: ResourceRef) => void
}) {
  const { support } = useRadarFeature('veleroBackupNow')
  const workspace = useRadarFeature('veleroWorkspace')
  const namespace: string = schedule?.metadata?.namespace ?? ''
  const name: string = schedule?.metadata?.name ?? ''
  const resourceVersion: string = schedule?.metadata?.resourceVersion ?? ''
  // Shares the drawer's request for the schedule's activity.
  const activity = useVeleroScheduleActivity(namespace, name, { enabled: workspace.support === 'supported' && !!namespace && !!name })
  const reading = activity.data?.reading
  // What the confirmation shows, frozen when it opens: a refresh while it is
  // open must not swap in a schedule the user did not review.
  const [reviewed, setReviewed] = useState<{ resourceVersion: string; template: any; backupName: string; submission: string } | null>(null)
  const backupNow = useVeleroBackupNow()
  if (support !== 'supported' || !namespace || !name) return null

  const open = reviewed !== null
  const changed = open && resourceVersion !== reviewed.resourceVersion
  const template = (open ? reviewed.template : schedule?.spec?.template) ?? {}
  const included = list(template.includedNamespaces)
  const excluded = list(template.excludedNamespaces)
  const namespaces = included.length === 0 || included.includes('*') ? 'All namespaces' : included.join(', ')
  const location: string = template.storageLocation || reading?.location?.name || ''
  const unavailable =
    name.length > MAX_LABEL_VALUE
      ? 'This schedule’s name is longer than 63 characters, so Velero cannot label a backup from it and the backup would fail.'
      : reading?.state === 'cannotRun'
        ? veleroScheduleReasonText(reading)
        : activity.data?.serverStopped || reading?.reason === 'serverDown'
          ? 'The Velero server isn’t running, so nothing would be backed up now. Start it first.'
          : !resourceVersion
            ? 'The schedule hasn’t loaded yet.'
            : null

  const button = (
    <button
      type="button"
      onClick={() => {
        backupNow.reset()
        setReviewed({ resourceVersion, template: schedule?.spec?.template ?? {}, backupName: backupNameAt(name, new Date()), submission: typeof crypto !== 'undefined' && 'randomUUID' in crypto ? crypto.randomUUID() : `s-${Date.now()}-${Math.random().toString(36).slice(2, 10)}` })
      }}
      disabled={unavailable !== null}
      className={BUTTON}
    >
      <Play className="h-3.5 w-3.5" aria-hidden />
      Back up now
    </button>
  )

  return (
    <>
      {unavailable ? (
        <Tooltip content={unavailable} position="bottom">
          <span>{button}</span>
        </Tooltip>
      ) : (
        button
      )}
      <ConfirmDialog
        open={open}
        onClose={() => setReviewed(null)}
        onConfirm={() =>
          reviewed &&
          backupNow.mutate(
            { namespace, name, resourceVersion: reviewed.resourceVersion, backupName: reviewed.backupName, submission: reviewed.submission },
            {
              onSuccess: (created) => {
                setReviewed(null)
                showApiSuccess(`Backup ${created.name} created`, undefined, onNavigate ? {
                  label: 'Open',
                  onClick: () => onNavigate({ kind: 'Backup', group: VELERO_GROUP, namespace: created.namespace, name: created.name }),
                } : undefined)
              },
            },
          )
        }
        title="Back up now"
        message={`Create a backup now with schedule ${name}'s settings.`}
        confirmLabel="Back up now"
        variant="default"
        isLoading={backupNow.isPending}
        confirmDisabled={changed || unavailable !== null}
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
          <p className="text-theme-text-secondary">The schedule's next run waits while this backup is queued or running.</p>
          {changed && (
            <p className="text-warning-text">This schedule changed while this was open. Close it and review the schedule again.</p>
          )}
          {!changed && unavailable && <p className="text-warning-text">{unavailable}</p>}
        </div>
      </ConfirmDialog>
    </>
  )
}
