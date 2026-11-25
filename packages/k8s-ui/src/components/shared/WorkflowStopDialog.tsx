import { useEffect, useState } from 'react'
import { Square } from 'lucide-react'
import { ActionConfirmDialog } from './ActionConfirmDialog'
import { isApiGroup } from '../resources/resource-utils-cnpg'
import { Tooltip } from '../ui/Tooltip'
import { Badge } from '../ui/Badge'
import { radarUpgradeNoteText } from '../ui/RadarUpgradeNote'
import type { RadarUpgradeRequirement } from '../../types/fetch-error'

export type WorkflowStopStrategy = 'Stop' | 'Terminate'

/**
 * What a Stop action can still do for this object. Mirrors the server's
 * terminal check (Succeeded, Failed, Error) so a finished run offers nothing;
 * a stored Terminate already satisfies both strategies.
 */
export type WorkflowStopState = 'none' | 'running' | 'stopping'

export function workflowStopState(data: any): WorkflowStopState {
  if (data?.kind !== 'Workflow' || !isApiGroup(data?.apiVersion, 'argoproj.io')) return 'none'
  if (data.metadata?.deletionTimestamp) return 'none'
  const phase = data.status?.phase
  if (phase === 'Succeeded' || phase === 'Failed' || phase === 'Error') return 'none'
  const shutdown = data.spec?.shutdown
  if (shutdown === 'Terminate') return 'none'
  return shutdown === 'Stop' ? 'stopping' : 'running'
}

/** The shutdown requested on a run that has not finished yet, for a header badge. */
export function workflowPendingShutdown(data: any): WorkflowStopStrategy | null {
  if (data?.kind !== 'Workflow' || !isApiGroup(data?.apiVersion, 'argoproj.io')) return null
  const phase = data.status?.phase
  if (phase === 'Succeeded' || phase === 'Failed' || phase === 'Error') return null
  const shutdown = data.spec?.shutdown
  return shutdown === 'Stop' || shutdown === 'Terminate' ? shutdown : null
}

const STRATEGY_NOTE: Record<WorkflowStopStrategy, string> = {
  Stop: 'Ends the running steps, starts no new ones, then runs the Workflow’s exit handlers (onExit), so cleanup and notifications still happen.',
  Terminate: 'Ends the running steps, including any exit handler, and starts no more. Use it when the exit handlers are stuck or must not run.',
}

export interface WorkflowStopDialogProps {
  open: boolean
  onClose: () => void
  onConfirm: (strategy: WorkflowStopStrategy) => void
  isLoading?: boolean
  namespace: string
  name: string
  /** The run is already stopping gracefully; only Terminate is left. */
  stopping: boolean
  /** spec.suspend is set: some Argo versions hold the shutdown until it resumes. */
  suspended?: boolean
}

export function WorkflowStopDialog({ open, onClose, onConfirm, isLoading, namespace, name, stopping, suspended }: WorkflowStopDialogProps) {
  const [choice, setChoice] = useState<WorkflowStopStrategy>('Stop')
  // Reset on open, not close, so the choice holds while the dialog animates out.
  useEffect(() => {
    if (open) setChoice('Stop')
  }, [open])
  const strategy: WorkflowStopStrategy = stopping ? 'Terminate' : choice

  return (
    <ActionConfirmDialog
      open={open}
      onClose={onClose}
      onConfirm={() => onConfirm(strategy)}
      isLoading={isLoading}
      title={stopping ? 'Terminate Workflow' : 'Stop Workflow'}
      subject={{ kind: 'Workflow', namespace, name }}
      effect={stopping
        ? 'A Stop was already requested, so Argo lets the exit handlers run. Terminate ends the run now: it stops any exit handler that is running and starts no more.'
        : 'Ends this run. Choose whether its exit handlers still run.'}
      warnings={suspended ? ['This Workflow is suspended. Some Argo versions apply the stop only after it is resumed.'] : []}
      notes={[
        'Argo applies this asynchronously, so the phase changes once its controller acts.',
        'A stopped run does not resume. Resubmit or retry the Workflow to run it again.',
      ]}
      writes={[{ summary: `patch Workflow ${namespace}/${name}`, detail: JSON.stringify({ spec: { shutdown: strategy } }) }]}
      confirmLabel={strategy}
      disruptive
    >
      {!stopping && (
        <fieldset className="space-y-2">
          <legend className="sr-only">How to stop</legend>
          {(['Stop', 'Terminate'] as const).map((option) => (
            <label key={option} className="flex items-start gap-2 text-sm">
              <input
                type="radio"
                name="workflow-stop-strategy"
                value={option}
                checked={choice === option}
                onChange={() => setChoice(option)}
                disabled={isLoading}
                className="mt-1"
              />
              <span>
                <span className="font-medium">{option}</span>{' '}
                <span className="text-theme-text-secondary">— {STRATEGY_NOTE[option]}</span>
              </span>
            </label>
          ))}
        </fieldset>
      )}
    </ActionConfirmDialog>
  )
}

export interface WorkflowStopActionProps {
  /** The Workflow object as last read; its phase, shutdown, UID and start time drive the action. */
  data: any
  namespace: string
  name: string
  /**
   * Passed only when the Radar serves the stop endpoint and the caller may
   * patch Workflows. The host reports the outcome; the dialog closes when the
   * promise resolves and stays open with its choice when it rejects.
   */
  onStopWorkflow?: (params: { namespace: string; name: string; uid: string; strategy: WorkflowStopStrategy }) => void | Promise<unknown>
  isStoppingWorkflow?: boolean
  /** Set when the connected Radar predates the stop endpoint: the button renders disabled with the upgrade note. */
  workflowStopUpgrade?: RadarUpgradeRequirement | null
}

/**
 * Stopping / Terminating while spec.shutdown waits for Argo: the request is
 * stored at once, but the phase stays Running until the controller acts.
 */
export function WorkflowShutdownBadge({ data }: { data: any }) {
  const shutdown = workflowPendingShutdown(data)
  if (!shutdown) return null
  return (
    <Tooltip content={shutdown === 'Stop' ? 'A Stop was requested. Argo ends the run after its exit handlers.' : 'A Terminate was requested. Argo ends the running steps, including any exit handler.'}>
      <Badge severity="warning" className="gap-1 whitespace-nowrap">
        <Square className="w-3 h-3 shrink-0" aria-hidden />
        {shutdown === 'Stop' ? 'Stopping' : 'Terminating'}
      </Badge>
    </Tooltip>
  )
}

/** The Stop… / Terminate… button for one Workflow and its confirmation dialog. */
export function WorkflowStopAction({ data, namespace, name, onStopWorkflow, isStoppingWorkflow, workflowStopUpgrade }: WorkflowStopActionProps) {
  // The dialog targets the run the user opened it on and keeps the mode it
  // opened in, so a status refresh can't turn a reviewed Stop into Terminate.
  // It closes once that run ends or restarts: `argo retry` keeps the UID, so
  // a new start time is what marks another attempt.
  const state = workflowStopState(data)
  const target = { key: `${namespace}/${name}`, uid: data?.metadata?.uid as string | undefined }
  // Argo serializes an unset start time as null.
  const startedAt: string | undefined = data?.status?.startedAt || undefined
  // Closing keeps the draft, so the dialog shows what it showed while it animates out.
  const [draft, setDraft] = useState<{ target: typeof target; namespace: string; name: string; startedAt?: string; stopping: boolean; suspended: boolean } | null>(null)
  const [open, setOpen] = useState(false)
  const [pending, setPending] = useState(false)
  const targetShown = draft !== null && draft.target.key === target.key && draft.target.uid === target.uid
  if (open && draft && !pending && (
    !onStopWorkflow || state === 'none' || !targetShown ||
    (draft.startedAt !== undefined && draft.startedAt !== startedAt)
  )) {
    setOpen(false)
  } else if (open && draft && targetShown && !draft.startedAt && startedAt) {
    // Opened before the run started: its first start time is the one reviewed.
    setDraft({ ...draft, startedAt })
  }
  const confirm = (strategy: WorkflowStopStrategy) => {
    const uid = draft?.target.uid
    if (!open || !draft || !onStopWorkflow || !uid || pending) return
    setPending(true)
    // A rejection keeps the dialog and its choice; the host's mutation meta reports the error.
    Promise.resolve(onStopWorkflow({ namespace: draft.namespace, name: draft.name, uid, strategy }))
      .then(() => setOpen(false), () => {})
      .finally(() => setPending(false))
  }
  const label = state === 'stopping' ? 'Terminate…' : 'Stop…'
  const busy = isStoppingWorkflow || pending

  return (
    <>
      {state !== 'none' && (onStopWorkflow ? (
        <Tooltip content={state === 'stopping' ? 'A Stop is in progress. Terminate also ends any exit handler.' : 'Stop or terminate this run'} delay={150}>
          <button
            type="button"
            onClick={() => {
              setDraft({ target, namespace, name, startedAt, stopping: state === 'stopping', suspended: data?.spec?.suspend === true })
              setOpen(true)
            }}
            disabled={busy || !target.uid}
            aria-busy={busy}
            className="flex items-center gap-1.5 px-2 py-1.5 text-xs font-medium btn-brand-muted rounded-lg"
          >
            <Square className={`w-3.5 h-3.5 ${busy ? 'animate-pulse' : ''}`} />
            {label}
          </button>
        </Tooltip>
      ) : workflowStopUpgrade && (
        <Tooltip content={radarUpgradeNoteText(workflowStopUpgrade)} delay={150}>
          <button type="button" disabled className="flex items-center gap-1.5 px-2 py-1.5 text-xs font-medium btn-brand-muted rounded-lg disabled:cursor-not-allowed">
            <Square className="w-3.5 h-3.5" />
            {label}
          </button>
        </Tooltip>
      ))}
      {(onStopWorkflow || draft) && (
        <WorkflowStopDialog
          open={open}
          onClose={() => setOpen(false)}
          onConfirm={confirm}
          isLoading={pending}
          namespace={draft?.namespace ?? namespace}
          name={draft?.name ?? name}
          stopping={draft?.stopping ?? false}
          suspended={draft?.suspended ?? false}
        />
      )}
    </>
  )
}
