import { useState } from 'react'
import { CirclePause, CirclePlay } from 'lucide-react'
import {
  ActionConfirmDialog,
  GitOpsWriteWarning,
  Tooltip,
  canConfirmGitOpsWrite,
  gitOpsRouteForOwner,
  veleroNextRunText,
  type GitOpsWrite,
} from '@skyhook-io/k8s-ui'
import { showApiSuccess } from '../ui/Toast'
import { useRadarFeature } from '../../api/client'
import { useVeleroSchedulePause, useVeleroScheduleActivity } from '../../api/velero'
import { useGitOpsWriteGuard } from '../../hooks/useGitOpsWriteGuard'
import { useWorkspaceNavigate } from '../workspace/useWorkspaceNavigate'
import { VELERO_GROUP } from './routes'

const BUTTON =
  'btn-secondary inline-flex shrink-0 items-center gap-1.5 whitespace-nowrap px-2.5 py-1.5 text-xs font-medium disabled:cursor-not-allowed disabled:opacity-50'

const GUARDED_WRITES: GitOpsWrite[] = [{ scope: 'spec', paths: ['spec.paused', 'spec.skipImmediately'] }]

/**
 * Pause or Resume for a Velero Schedule, beside Back up now: sets
 * spec.paused as `velero schedule pause|unpause` does. The confirmation is
 * bound to the schedule's version when it opened, and Resume says what
 * Velero does on its next pass, with the choice to skip a run that's due.
 */
export function VeleroSchedulePause({ schedule }: { schedule: any }) {
  const { support } = useRadarFeature('veleroSchedulePause')
  const namespace: string = schedule?.metadata?.namespace ?? ''
  const name: string = schedule?.metadata?.name ?? ''
  const resourceVersion: string = schedule?.metadata?.resourceVersion ?? ''
  const paused = schedule?.spec?.paused === true
  // The version the confirmation was opened on, and which way it goes.
  const [reviewed, setReviewed] = useState<{ resourceVersion: string; pause: boolean } | null>(null)
  if (support !== 'supported' || !namespace || !name) return null

  const label = paused ? 'Resume' : 'Pause'
  const unavailable = !resourceVersion ? 'The schedule hasn’t loaded yet.' : null
  const button = (
    <button type="button" onClick={() => setReviewed({ resourceVersion, pause: !paused })} disabled={unavailable !== null} className={BUTTON}>
      {paused ? <CirclePlay className="h-3.5 w-3.5" aria-hidden /> : <CirclePause className="h-3.5 w-3.5" aria-hidden />}
      {label}
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
      {reviewed && (
        <SchedulePauseDialog
          namespace={namespace}
          name={name}
          pause={reviewed.pause}
          reviewedVersion={reviewed.resourceVersion}
          changed={resourceVersion !== reviewed.resourceVersion}
          onClose={() => setReviewed(null)}
        />
      )}
    </>
  )
}

// Mounted only while open, so the GitOps evidence is read only for a
// confirmation the user opened.
function SchedulePauseDialog({ namespace, name, pause, reviewedVersion, changed, onClose }: {
  namespace: string
  name: string
  pause: boolean
  reviewedVersion: string
  changed: boolean
  onClose: () => void
}) {
  const workspace = useRadarFeature('veleroWorkspace')
  const activity = useVeleroScheduleActivity(namespace, name, { enabled: workspace.support === 'supported' })
  const mutation = useVeleroSchedulePause()
  const navigate = useWorkspaceNavigate()
  const [skip, setSkip] = useState(false)
  const [acked, setAcked] = useState(false)
  const { guard } = useGitOpsWriteGuard({ target: { kind: 'Schedule', group: VELERO_GROUP, namespace, name }, writes: GUARDED_WRITES })

  const resume = activity.data?.resume
  const skipping = !pause && skip && resume?.skipping ? resume.skipping : undefined
  const outlook = skipping ?? resume?.default
  const effect = pause ? (
    'Velero stops submitting this schedule’s runs until it is resumed.'
  ) : outlook ? (
    <>
      {outlook.summary}
      {outlook.outcome === 'waits' && outlook.nextRun && <> Next run {veleroNextRunText({ nextRun: outlook.nextRun, nextRunCertain: !!outlook.nextRunCertain })}.</>}
    </>
  ) : (
    'Velero submits this schedule’s runs again. Radar could not read what it does first: a run that’s due may be submitted at once.'
  )
  const notes = pause
    ? [
        'A run already started continues to the end.',
        activity.data?.backupsDisabled
          ? 'Back up now stays unavailable while Velero runs restore-only.'
          : 'Back up now still creates a backup from this schedule while it is paused.',
      ]
    : []
  const warnings = [
    ...(activity.data?.serverStopped ? ['The Velero server isn’t running, so nothing happens until it starts.'] : []),
    ...(changed ? ['This schedule changed while this was open. Close it and review the schedule again.'] : []),
  ]
  const skipValue = !pause && skipping ? true : null
  const route = guard?.owner ? gitOpsRouteForOwner(guard.owner) : null

  return (
    <ActionConfirmDialog
      open
      onClose={onClose}
      onConfirm={() =>
        mutation.mutate(
          { namespace, name, paused: pause, resourceVersion: reviewedVersion, skipImmediately: skipValue },
          {
            onSuccess: (result) => {
              onClose()
              showApiSuccess(
                result.changed ? `Schedule ${name} ${pause ? 'paused' : 'resumed'}` : `Schedule ${name} was already ${pause ? 'paused' : 'running'}`,
              )
            },
          },
        )
      }
      title={pause ? `Pause ${name}?` : `Resume ${name}?`}
      subject={{ kind: 'Schedule', namespace, name }}
      effect={effect}
      notes={notes}
      warnings={warnings}
      guard={
        guard ? (
          <GitOpsWriteWarning
            guard={guard}
            acknowledged={acked}
            onAcknowledgedChange={setAcked}
            onOpenOwner={route ? () => navigate(route) : undefined}
            onOpenManager={(owner) => navigate(gitOpsRouteForOwner(owner))}
          />
        ) : null
      }
      guardSatisfied={guard ? canConfirmGitOpsWrite(guard, acked) : false}
      writes={[{
        summary: `patch Schedule ${namespace}/${name}`,
        detail: `spec.paused = ${pause}\nspec.skipImmediately = ${skipValue === true ? 'true' : 'null (the Velero server decides)'}`,
      }]}
      confirmLabel={pause ? 'Pause schedule' : 'Resume schedule'}
      isLoading={mutation.isPending}
      result={mutation.error ? { state: 'error', message: mutation.error.message } : undefined}
      incompleteReason={changed ? 'The schedule changed' : undefined}
    >
      {!pause && resume?.skipping && (
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={skip} onChange={(e) => setSkip(e.target.checked)} />
          Skip the run that’s due now
        </label>
      )}
    </ActionConfirmDialog>
  )
}
