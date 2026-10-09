import type { ReactNode } from 'react'
import { toneTextClass, Tooltip, type HealthLevel } from '@skyhook-io/k8s-ui'
import {
  describeSource,
  formatLocal,
  formatUTC,
  sourcePinsBackup,
  type EvidencePoint,
  type RecoveryEvidence,
  type RestoreSource,
  type RestoreTarget,
} from './restoreModel'

const FIELD = 'rounded-lg border border-theme-border bg-theme-base px-2 py-1 text-sm text-theme-text-primary'

function When({ point, empty }: { point?: EvidencePoint & { wal?: string }; empty: string }) {
  if (!point) return <span className="text-theme-text-tertiary">{empty}</span>
  return (
    <>
      <div className="font-mono text-[12.5px] text-theme-text-primary">{formatUTC(point.at)}</div>
      <div className="text-[11px] text-theme-text-tertiary">
        {formatLocal(point.at)}
        {point.wal ? ` · ${point.wal}` : ''}
      </div>
      <div className="text-[11px] text-theme-text-tertiary">{point.source}</div>
    </>
  )
}

function EvidenceRow({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid grid-cols-[8.5rem_minmax(0,1fr)] gap-x-3 py-1.5">
      <div className="text-xs text-theme-text-secondary">{label}</div>
      <div className="min-w-0">{children}</div>
    </div>
  )
}

export function CNPGRecoveryPoint({
  sources,
  sourceIndex,
  onSourceChange,
  targetKind,
  onTargetChange,
  timeValue,
  onTimeChange,
  zone,
  onZoneChange,
  targetIso,
  evidence,
  sourceClusterName,
}: {
  sources: RestoreSource[]
  sourceIndex: number
  onSourceChange: (index: number) => void
  targetKind: RestoreTarget['kind']
  onTargetChange: (kind: RestoreTarget['kind']) => void
  timeValue: string
  onTimeChange: (value: string) => void
  zone: 'utc' | 'local'
  onZoneChange: (value: 'utc' | 'local') => void
  targetIso: string | null
  evidence: RecoveryEvidence
  sourceClusterName?: string
}) {
  const source = sources[sourceIndex]
  const pinned = sourcePinsBackup(source)
  const localZone = Intl.DateTimeFormat().resolvedOptions().timeZone
  return (
    <>
      <div className="mb-4 grid grid-cols-[7.5rem_minmax(0,1fr)] items-center gap-x-3">
        <label className="text-xs text-theme-text-secondary" htmlFor="cnpg-restore-source">
          Restore from
        </label>
        <select
          id="cnpg-restore-source"
          value={sourceIndex}
          onChange={(e) => onSourceChange(Number(e.target.value))}
          className={FIELD}
        >
          {sources.map((s, i) => (
            <option key={i} value={i}>
              {describeSource(s)}
            </option>
          ))}
        </select>
      </div>
      <div className="grid gap-5 sm:grid-cols-2">
        <div>
          <fieldset className="space-y-1.5">
            <legend className="mb-1 text-xs text-theme-text-secondary">Recover to</legend>
            {pinned && (
              <label className="flex items-center gap-2 text-sm text-theme-text-primary">
                <input
                  type="radio"
                  name="cnpg-restore-target"
                  checked={targetKind === 'backupEnd'}
                  onChange={() => onTargetChange('backupEnd')}
                />
                The end of this backup
                {source?.kind === 'objectStore' && source.backupEnd ? ` (${formatUTC(source.backupEnd)})` : ''}
              </label>
            )}
            <label className="flex items-center gap-2 text-sm text-theme-text-primary">
              <input
                type="radio"
                name="cnpg-restore-target"
                checked={targetKind === 'latest'}
                onChange={() => onTargetChange('latest')}
              />
              The latest archived WAL
            </label>
            <p className="ml-6 text-xs text-theme-text-tertiary">
              Replay the archived transaction logs to the newest available point.
            </p>
            <label className="flex items-center gap-2 text-sm text-theme-text-primary">
              <input
                type="radio"
                name="cnpg-restore-target"
                checked={targetKind === 'time'}
                onChange={() => onTargetChange('time')}
              />
              A point in time
            </label>
            {targetKind === 'time' && (
              <div className="ml-6 space-y-1">
                <div className="flex flex-wrap items-center gap-2">
                  <input
                    id="cnpg-restore-time"
                    aria-label="Target time"
                    type="datetime-local"
                    step={1}
                    value={timeValue}
                    onChange={(e) => onTimeChange(e.target.value)}
                    className={FIELD}
                  />
                  <select
                    aria-label="Time zone of the target"
                    value={zone}
                    onChange={(e) => onZoneChange(e.target.value as 'utc' | 'local')}
                    className={FIELD}
                  >
                    <option value="utc">UTC</option>
                    <option value="local">{localZone}</option>
                  </select>
                </div>
                <div className="text-[11px] text-theme-text-tertiary">
                  {targetIso ? (
                    <>
                      Target: <span className="font-mono">{targetIso}</span> · {formatLocal(targetIso)}
                    </>
                  ) : (
                    'The target is written to the manifest in UTC.'
                  )}
                </div>
              </div>
            )}
            {pinned && targetKind !== 'backupEnd' && (
              <div className="ml-6 text-[11px] text-theme-text-tertiary">
                Starts from this backup and replays archived WAL after it.
              </div>
            )}
          </fieldset>
          <p className="mt-4 text-xs text-theme-text-tertiary">
            A restore creates a separate Cluster. Choose its name, instance count and storage next.
          </p>
        </div>
        <div className="rounded-lg border border-theme-border bg-theme-base px-3 py-2">
          <div className="mb-1 text-[11px] font-semibold uppercase tracking-wide text-theme-text-tertiary">
            What the source holds
          </div>
          <EvidenceRow label="First recoverability point">
            <When point={evidence.firstPoint} empty={source?.kind === 'backup' ? 'This backup' : 'Not reported'} />
          </EvidenceRow>
          <EvidenceRow label="Last successful backup">
            <When point={evidence.lastBackup} empty="None observed" />
          </EvidenceRow>
          <EvidenceRow label="WAL archiving">
            <Tooltip content={evidence.archiving.detail} disabled={!evidence.archiving.detail}>
              <span className={toneTextClass(evidence.archiving.tone as HealthLevel)}>{evidence.archiving.text}</span>
            </Tooltip>
            <div className="text-[11px] text-theme-text-tertiary">{evidence.archiving.source}</div>
          </EvidenceRow>
          <EvidenceRow label="Last archived WAL">
            <When point={evidence.lastArchived} empty={sourceClusterName ? 'Not reported' : 'Unknown'} />
          </EvidenceRow>
          {evidence.lastArchiveFailure && (
            <EvidenceRow label="Last archive failure">
              <When point={evidence.lastArchiveFailure} empty="" />
            </EvidenceRow>
          )}
          {evidence.gaps.length > 0 && (
            <ul className="mt-1 list-disc space-y-0.5 pl-4 text-[11px] text-theme-text-tertiary">
              {evidence.gaps.map((g) => (
                <li key={g}>{g}</li>
              ))}
            </ul>
          )}
        </div>
      </div>
    </>
  )
}
