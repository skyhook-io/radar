import { useMemo } from 'react'
import { useNavigate } from 'react-router-dom'
import { Badge, StatusDot, formatAge, toneTextClass, type HealthLevel } from '@skyhook-io/k8s-ui'
import { useCNPGRecovery, type CNPGContainerState, type CNPGRecoveryResponse, type CNPGRecoverySpec } from '../../../api/cnpg-recovery'
import { buildWorkloadPath } from '../../../utils/navigation'
import { observeRestore, restoreNextSteps, type RestoreNextStepId, type RestoreObservation } from './restoreModel'

/**
 * The restore observer: the recovery snapshot of one Cluster and where its
 * restore stands. Self-contained so a generic operation tracker can register
 * it as the "restore" operation's observer.
 */
export function useRestoreObservation(namespace: string, name: string, enabled = true): {
  observation: RestoreObservation | null
  snapshot: CNPGRecoveryResponse | undefined
  error: unknown
} {
  const probe = useCNPGRecovery(namespace, name, { enabled })
  const observation = useMemo(() => (probe.data?.recovery ? observeRestore(probe.data) : null), [probe.data])
  return { observation, snapshot: probe.data, error: probe.error }
}

export function describeRecoverySource(r: CNPGRecoverySpec): string {
  const target = r.target
    ? Object.entries(r.target)
        .map(([k, v]) => `${k} ${String(v)}`)
        .join(', ')
    : 'latest archived WAL'
  switch (r.sourceKind) {
    case 'objectStore':
      return `ObjectStore ${r.objectStore ?? '?'} · server ${r.serverName ?? '?'} · to ${target}`
    case 'barmanObjectStore':
      return `Barman object store · server ${r.serverName ?? '?'} · to ${target}`
    case 'backup':
      return `Backup ${r.backup ?? '?'} · to ${target}`
    case 'volumeSnapshots':
      return 'Volume snapshots'
    default:
      return 'an external source'
  }
}

const STATE_SEVERITY: Record<RestoreObservation['state'], 'success' | 'error' | 'neutral' | 'warning'> = {
  completed: 'success',
  failed: 'error',
  progressing: 'neutral',
  unobservable: 'neutral',
}

function ContainerChip({ c, init }: { c: CNPGContainerState; init?: boolean }) {
  const tone: HealthLevel =
    c.state === 'running' ? 'healthy' : c.state === 'terminated' ? ((c.exitCode ?? 0) === 0 ? 'neutral' : 'unhealthy') : c.reason && c.reason !== 'PodInitializing' && c.reason !== 'ContainerCreating' ? 'alert' : 'unknown'
  const label = c.state === 'terminated' ? (c.exitCode === 0 ? 'done' : `exit ${c.exitCode}`) : c.reason ?? c.state
  return (
    <span className="inline-flex items-center gap-1 rounded border border-theme-border px-1.5 py-0.5 text-[11px] text-theme-text-secondary">
      <StatusDot tone={tone} />
      {init && <span className="text-theme-text-tertiary">init</span>}
      <span className="font-mono">{c.name}</span>
      <span>{label}</span>
      {c.restarts > 0 && <span className="text-theme-text-tertiary">· {c.restarts} restarts</span>}
    </span>
  )
}

/**
 * "Restore in progress" on a Cluster bootstrapped from backups: phase, the
 * recovery Job's Pods with their init containers, links to their logs, and
 * the Warning events about them. Once complete it collapses to one line.
 */
export interface CNPGRestoreNextStepsHost {
  /** Whether the Cluster spec declares a backup destination; undefined when not read. */
  backupConfigured?: boolean
  onOpen: (step: RestoreNextStepId) => void
}

const STEP_TONE: Record<'done' | 'todo' | 'unknown', HealthLevel> = { done: 'healthy', todo: 'degraded', unknown: 'unknown' }

function NextSteps({ validationRecorded, host }: { validationRecorded: boolean; host: CNPGRestoreNextStepsHost }) {
  const steps = restoreNextSteps({ validationRecorded, backupConfigured: host.backupConfigured })
  return (
    <div className="mt-2 border-t border-theme-border pt-2">
      <div className="mb-1 text-[11px] font-semibold uppercase tracking-wide text-theme-text-tertiary">Next steps</div>
      <ul className="space-y-1">
        {steps.map((s) => (
          <li key={s.id} className="flex flex-wrap items-center gap-x-2 text-xs">
            <StatusDot tone={STEP_TONE[s.state]} />
            <button type="button" onClick={() => host.onOpen(s.id)} className="text-accent-text hover:underline">
              {s.label}
            </button>
            {s.state !== 'unknown' && <span className={toneTextClass(STEP_TONE[s.state])}>{s.state === 'done' ? 'done' : 'to do'}</span>}
            {s.note && <span className="text-theme-text-tertiary">· {s.note}</span>}
          </li>
        ))}
      </ul>
    </div>
  )
}

export function CNPGRestoreProgress({ namespace, name, nextSteps }: { namespace: string; name: string; nextSteps?: CNPGRestoreNextStepsHost }) {
  const navigate = useNavigate()
  const { observation, snapshot } = useRestoreObservation(namespace, name)
  if (!observation || !snapshot?.recovery) return null
  const openLogs = (pod: string) => navigate(buildWorkloadPath({ kind: 'pods', namespace, name: pod, tab: 'logs' }))
  const source = describeRecoverySource(snapshot.recovery)

  if (observation.state === 'completed') {
    return (
      <div className="mb-3 rounded-lg border border-theme-border bg-theme-surface px-3 py-2 text-sm">
        <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
          <Badge severity="success" size="sm">Restored</Badge>
          <span className="text-theme-text-secondary">from {source}</span>
          <span className="text-theme-text-tertiary">· {observation.detail}</span>
        </div>
        {nextSteps && <NextSteps validationRecorded={!!snapshot.validation} host={nextSteps} />}
      </div>
    )
  }

  const warnings = snapshot.events.filter((e) => e.type === 'Warning').slice(0, 5)
  return (
    <section aria-label="Restore progress" className="mb-3 overflow-hidden rounded-xl border border-theme-border bg-theme-surface shadow-theme-sm">
      <div className="flex flex-wrap items-center gap-2 border-b border-theme-border px-4 py-2.5">
        <Badge severity={STATE_SEVERITY[observation.state]} size="sm">
          {observation.state === 'failed' ? 'Restore failed' : observation.state === 'unobservable' ? 'Restore' : 'Restore in progress'}
        </Badge>
        <span className="text-sm font-medium text-theme-text-primary">{observation.title}</span>
        <span className="text-xs text-theme-text-tertiary">from {source}</span>
      </div>
      <div className="space-y-3 px-4 py-3 text-sm">
        {observation.detail && <div className="text-theme-text-secondary">{observation.detail}</div>}
        {observation.recoveryPods.length > 0 && (
          <div className="space-y-2">
            {observation.recoveryPods.map((p) => (
              <div key={p.uid} className="flex flex-wrap items-center gap-2">
                <span className="font-mono text-[12.5px] text-theme-text-primary">{p.name}</span>
                <span className="text-xs text-theme-text-tertiary">
                  {p.phase}
                  {p.job ? ` · Job ${p.job}` : ''}
                  {!p.ownerVerified ? ' · Job ownership not verified' : ''}
                  {p.startedAt ? ` · started ${formatAge(p.startedAt)} ago` : ''}
                </span>
                {p.initContainers.map((c) => <ContainerChip key={`i-${c.name}`} c={c} init />)}
                {p.containers.map((c) => <ContainerChip key={c.name} c={c} />)}
                <button type="button" onClick={() => openLogs(p.name)} className="text-xs text-accent-text hover:underline">
                  Logs
                </button>
              </div>
            ))}
          </div>
        )}
        {observation.logsPod && !observation.recoveryPods.some((p) => p.name === observation.logsPod?.name) && (
          <button type="button" onClick={() => openLogs(observation.logsPod!.name)} className="text-xs text-accent-text hover:underline">
            Logs of {observation.logsPod.name}
          </button>
        )}
        {snapshot.coverage.pods?.state !== 'ok' && (
          <div className="text-xs text-theme-text-tertiary">Recovery Pods not visible: {snapshot.coverage.pods?.grant ? `needs ${snapshot.coverage.pods.grant}` : snapshot.coverage.pods?.reason}</div>
        )}
        {warnings.length > 0 && (
          <div>
            <div className="mb-1 text-[11px] font-semibold uppercase tracking-wide text-theme-text-tertiary">Warning events</div>
            <ul className="space-y-1">
              {warnings.map((e, i) => (
                <li key={`${e.kind}/${e.name}/${e.reason}/${i}`} className="text-xs text-theme-text-secondary">
                  <span className="font-medium text-theme-text-primary">{e.reason}</span> on {e.kind} {e.name}
                  {e.lastSeen ? ` · ${formatAge(e.lastSeen)} ago` : ''}
                  {e.count > 1 ? ` · ×${e.count}` : ''}: {e.message}
                </li>
              ))}
            </ul>
          </div>
        )}
        <div className="text-[11px] text-theme-text-tertiary">
          Complete when the Cluster reports a healthy phase with every instance ready. Sampled {formatAge(snapshot.capturedAt)} ago.
        </div>
      </div>
    </section>
  )
}
