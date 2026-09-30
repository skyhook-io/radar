import { useState } from 'react'
import { Pause, Play } from 'lucide-react'
import { ActionConfirmDialog, Tooltip } from '@skyhook-io/k8s-ui'
import { cnpgActionOutcomeLocked, useCNPGAction } from '../../../api/cnpg'
import { useCNPGPoolerCapabilities, type CNPGPoolerCapabilities } from '../../../api/cnpg-sessions'
import { useToast } from '../../ui/Toast'
import { useCNPGWriteGuard } from './useCNPGWriteGuard'

const BUTTON =
  'inline-flex items-center gap-1.5 rounded-lg border border-theme-border bg-theme-surface px-2.5 py-1.5 text-xs font-medium text-theme-text-primary hover:bg-theme-hover disabled:cursor-not-allowed disabled:opacity-50'

/** Pause or resume a Pooler's PgBouncers (spec.pgbouncer.paused). */
export function CNPGPoolerActions({ namespace, name }: { namespace: string; name: string }) {
  const caps = useCNPGPoolerCapabilities(namespace, name)
  const [open, setOpen] = useState<'pause' | 'resume' | null>(null)
  const data = caps.data
  if (!data) return null
  const kind = data.facts.paused ? 'resume' : 'pause'
  const cap = data.actions[kind]
  return (
    <>
      <Tooltip content={cap.allowed ? undefined : cap.reason ?? 'Not allowed'} position="bottom">
        <button type="button" className={BUTTON} disabled={!cap.allowed} onClick={() => setOpen(kind)}>
          {kind === 'pause' ? <Pause className="h-3.5 w-3.5" /> : <Play className="h-3.5 w-3.5" />}
          {kind === 'pause' ? 'Pause' : 'Resume'}
        </button>
      </Tooltip>
      {open && <PoolerDialog kind={open} caps={data} namespace={namespace} name={name} onClose={() => setOpen(null)} />}
    </>
  )
}

function PoolerDialog({ kind, caps, namespace, name, onClose }: { kind: 'pause' | 'resume'; caps: CNPGPoolerCapabilities; namespace: string; name: string; onClose: () => void }) {
  const mutation = useCNPGAction('poolers', namespace, name)
  const { showSuccess } = useToast()
  const guard = useCNPGWriteGuard({ namespace, name, scope: { kind: 'spec', paths: ['spec.pgbouncer.paused'] }, targetKind: 'Pooler' })
  const pause = kind === 'pause'
  const cap = caps.actions[kind]
  const pods = caps.facts.deployment.readyReplicas
  return (
    <ActionConfirmDialog
      open
      onClose={onClose}
      onConfirm={() =>
        mutation.mutate(
          { action: kind, request: { reviewedContext: caps.context, uid: caps.uid, facts: { paused: caps.facts.paused } }, successMessage: '' },
          {
            onSuccess: (r) => {
              showSuccess(r.message)
              onClose()
            },
          },
        )
      }
      title={pause ? `Pause ${name}?` : `Resume ${name}?`}
      subject={{ kind: 'Pooler', namespace, name }}
      context={caps.context}
      effect={
        pause
          ? `Every PgBouncer of this Pooler${pods !== undefined ? ` (${pods} ready)` : ''} waits for running queries to complete, disconnects from ${caps.facts.cluster}, and then holds new client queries until it is resumed. Clients stay connected but their queries wait.`
          : `Every PgBouncer of this Pooler reconnects to ${caps.facts.cluster} and serves the client queries it was holding.`
      }
      notes={[
        'The operator applies this with PgBouncer’s PAUSE / RESUME on each Pod. Radar shows each PgBouncer’s own state (SHOW STATE) once it has applied it, when you can exec into the Pods.',
        ...(pause ? ['Clients whose queries wait longer than their own timeouts see errors; a long-running transaction delays the pause.'] : []),
      ]}
      writes={[{ summary: `patch Pooler ${namespace}/${name}`, detail: `spec.pgbouncer.paused = ${pause}` }]}
      guard={guard.node}
      guardSatisfied={guard.satisfied}
      typedConfirmation={pause ? name : undefined}
      confirmLabel={pause ? 'Pause' : 'Resume'}
      disruptive={pause}
      disabledReason={cap.allowed ? undefined : cap.reason}
      isLoading={mutation.isPending}
      error={mutation.error?.message}
      outcomeUnknown={cnpgActionOutcomeLocked(mutation.error)}
    />
  )
}
