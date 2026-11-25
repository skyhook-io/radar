import { useEffect, useState } from 'react'
import { ActionConfirmDialog } from './ActionConfirmDialog'
import { isApiGroup } from '../resources/resource-utils-cnpg'

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
