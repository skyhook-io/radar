import { clsx } from 'clsx'
import {
  Badge,
  CNPG_ROLE_DETAIL_TEXT,
  CNPG_SLOT_RETENTION_WARNING_BYTES,
  cnpgFormatLag,
  cnpgHASlotName,
  StatusDot,
  Tooltip,
  toneFillClass,
  toneTextClass,
  type CNPGClusterHA,
  type CNPGHAInstance,
} from '@skyhook-io/k8s-ui'
import { useCNPGClusterCapabilities, type CNPGRuntimeInstance, type CNPGRuntimeReplication } from '../../api/cnpg'
import { CNPGInstanceActions } from './actions/CNPGInstanceActions'
import { formatBytes, lsnDistance, standbyOwnBacklog } from './lsn'
import { CNPG_BACKLOG_DEGRADED, cnpgNoStandbyText, cnpgStandbyBacklogTone, cnpgStandbyHeadline } from './runtimeModel'
import { Card, SourceState } from './runtimeParts'

function seconds(s?: number): string {
  return s === undefined ? '—' : cnpgFormatLag(s)
}

type RuntimeSlot = NonNullable<CNPGRuntimeInstance['status']['slots']>[number]

/** Readiness, node and QoS from the instance Pod: what Kubernetes says, kept apart from what PostgreSQL reports. */
function PodLine({ pod }: { pod?: CNPGHAInstance }) {
  if (!pod) return null
  return (
    <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-0.5 text-xs text-theme-text-secondary">
      <span className={pod.ready ? undefined : toneTextClass('unhealthy')}>Pod {pod.ready ? 'ready' : 'not ready'}</span>
      {pod.node && <span>node <span className="font-mono">{pod.node}</span></span>}
      {pod.qosClass && <span>QoS {pod.qosClass}</span>}
      {pod.restartCount > 0 && <span>{pod.restartCount} container restart{pod.restartCount === 1 ? '' : 's'}</span>}
      {pod.imageMatches === false && <Badge severity="warning" size="sm">image differs</Badge>}
    </div>
  )
}

/** The HA slot the primary keeps for this standby, beside the standby it serves, as `kubectl cnpg status` shows it. */
function SlotLine({ slot, name }: { slot?: RuntimeSlot; name: string }) {
  if (!slot) {
    return <div className="mt-1 text-xs text-theme-text-tertiary">Slot <span className="font-mono">{name}</span>: not present on the primary</div>
  }
  const held = (slot.retainedBytes ?? 0) >= CNPG_SLOT_RETENTION_WARNING_BYTES && slot.active === false
  return (
    <div className="mt-1 text-xs">
      <span className="text-theme-text-secondary">Slot on the primary </span>
      <span className="font-mono">{slot.name}</span>
      <span className={clsx(slot.active === false ? toneTextClass('degraded') : 'text-theme-text-secondary')}> · {slot.active === undefined ? 'state unknown' : slot.active ? 'active' : 'inactive'}</span>
      <span className={held ? toneTextClass('degraded') : 'text-theme-text-secondary'}> · holds {formatBytes(slot.retainedBytes)} of WAL</span>
      {slot.walStatus && <span className="text-theme-text-tertiary"> · WAL status {slot.walStatus}</span>}
    </div>
  )
}

function InstanceFacts({ inst, primaryVersion }: { inst: CNPGRuntimeInstance; primaryVersion?: string }) {
  const s = inst.status
  if (s.state !== 'ok' && s.state !== 'partial') return null
  const skew = s.instanceManagerVersion && primaryVersion && s.instanceManagerVersion !== primaryVersion
  return (
    <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-theme-text-secondary">
      {s.roleDetail && s.roleDetail !== 'replayPaused' && <span>{CNPG_ROLE_DETAIL_TEXT[s.roleDetail]}</span>}
      {s.timeline !== undefined && <span className="font-mono">timeline {s.timeline}</span>}
      {s.instanceManagerVersion && (
        <span className={skew ? toneTextClass('degraded') : undefined}>
          CNPG instance manager {s.instanceManagerVersion}
          {skew ? ` (primary runs ${primaryVersion})` : ''}
        </span>
      )}
      {s.pendingRestart && (
        <Tooltip content={s.pendingRestartForDecrease ? 'A lowered setting is waiting: the primary must restart before its standbys.' : 'A changed parameter only takes effect after a restart.'}>
          <Badge severity="warning" size="sm">pending restart</Badge>
        </Tooltip>
      )}
    </div>
  )
}

function Backlog({ primary, rep }: { primary?: CNPGRuntimeInstance; rep: CNPGRuntimeReplication }) {
  const current = primary?.status.currentLsn
  const replay = lsnDistance(current, rep.replayLsn)
  const parts: [string, number | undefined][] = [
    ['not sent', lsnDistance(current, rep.sentLsn)],
    ['not written', lsnDistance(rep.sentLsn, rep.writeLsn)],
    ['not flushed', lsnDistance(rep.writeLsn, rep.flushLsn)],
    ['not replayed', lsnDistance(rep.flushLsn, rep.replayLsn)],
  ]
  return (
    <div className="mt-1 text-xs">
      <span className="text-theme-text-secondary">Replay backlog </span>
      <span className="font-mono text-theme-text-primary">{formatBytes(replay)}</span>
      <span className="text-theme-text-tertiary">
        {' · '}
        {parts.map(([label, v]) => `${label} ${formatBytes(v)}`).join(' · ')}
      </span>
    </div>
  )
}

function Delays({ rep }: { rep: CNPGRuntimeReplication }) {
  const none = rep.writeLag === undefined && rep.flushLag === undefined && rep.replayLag === undefined
  return (
    <div className="mt-0.5 text-xs text-theme-text-tertiary">
      {none ? (
        'Acknowledgement delay: none recorded (idle and caught up, or not yet measured)'
      ) : (
        <>
          Acknowledgement delay · write <span className="font-mono">{seconds(rep.writeLag)}</span> · flush <span className="font-mono">{seconds(rep.flushLag)}</span> · replay{' '}
          <span className="font-mono">{seconds(rep.replayLag)}</span>
        </>
      )}
    </div>
  )
}

/**
 * Instances and replication: each standby's catch-up measured as bytes of WAL
 * behind the primary, beside PostgreSQL's acknowledgement delays, with each
 * instance's own report of what it is doing.
 */
export function CNPGReplicationView({
  namespace,
  cluster,
  primary,
  replicas,
  onOpenLogs,
  ha,
  clusterObject,
}: {
  namespace: string
  cluster: string
  primary?: CNPGRuntimeInstance
  replicas: CNPGRuntimeInstance[]
  onOpenLogs?: (pod: string) => void
  /** The instance Pods as Kubernetes reports them (readiness, node, QoS). */
  ha?: CNPGClusterHA
  /** The Cluster object, for the HA slot prefix. */
  clusterObject?: any
}) {
  const rows = new Map((primary?.status.replication ?? []).map((r) => [r.applicationName, r]))
  // The instance manager keeps answering on a fenced Pod with PostgreSQL
  // stopped, so fencing comes from the Cluster, not from the runtime read.
  const fenced = new Set(useCNPGClusterCapabilities(namespace, cluster).data?.facts.instances.filter((i) => i.fenced).map((i) => i.pod))
  const primaryVersion = primary?.status.instanceManagerVersion
  const pods = new Map((ha?.pods.state === 'ok' ? ha.instances : []).map((i) => [i.pod, i]))
  const slots = new Map((primary?.status.slots ?? []).map((sl) => [sl.name, sl]))
  const slotsRead = !!primary?.status.slots
  const standbyMeasurementShown = replicas.some((r) => {
    const rep = rows.get(r.pod)
    return !!(r.status.receivedLsn || r.status.replayLsn) || !!rep && [rep.sentLsn, rep.writeLsn, rep.flushLsn, rep.replayLsn, rep.writeLag, rep.flushLag, rep.replayLag].some((value) => value !== undefined)
  })
  return (
    <Card
      title="Instances and replication"
      footer={
        standbyMeasurementShown ? <>
          Rows come from the primary’s pg_stat_replication through the instance manager. Replay backlog is the primary’s current WAL position minus what the
          standby has replayed, in bytes: the catch-up measure. A standby that isn't connected has no row, so its backlog uses the position it reports itself — only on
          the same timeline, since positions on different timelines aren't comparable.
          Write, flush and replay delay are PostgreSQL’s acknowledgement delay for recent WAL; empty when idle and caught up.
        </> : 'Source: instance manager status and the primary’s pg_stat_replication.'
      }
    >
      <div className="grid grid-cols-1 items-start gap-4 lg:grid-cols-[minmax(220px,300px)_minmax(0,1fr)]">
        <div className="rounded-lg border border-theme-border border-l-4 border-l-accent bg-theme-base p-3">
          {primary ? (
            <div className="flex items-center gap-2">
              <span className="font-mono text-sm font-semibold">{primary.pod}</span>
              <Badge tone="structural" size="sm">primary</Badge>
            </div>
          ) : (
            <div className="text-sm text-theme-text-tertiary">No primary reported</div>
          )}
          {primary && (
            <>
              <div className="mt-1 font-mono text-xs text-theme-text-secondary">WAL position {primary.status.currentLsn ?? '—'}</div>
              <PodLine pod={pods.get(primary.pod)} />
              <InstanceFacts inst={primary} />
              <SourceState label="Status" state={primary.status.state} error={primary.status.error ?? primary.status.reason} />
              <div className="mt-2 flex flex-wrap gap-3 text-xs">
                {onOpenLogs && (
                  <button type="button" className="text-accent-text hover:underline" onClick={() => onOpenLogs(primary.pod)}>
                    Logs
                  </button>
                )}
                <CNPGInstanceActions namespace={namespace} cluster={cluster} pod={primary.pod} />
              </div>
            </>
          )}
        </div>
        <div className="space-y-2">
          {replicas.length === 0 && <div className="pt-[13px] text-sm text-theme-text-tertiary">{cnpgNoStandbyText(clusterObject?.spec?.instances)}</div>}
          {replicas.map((r) => {
            const rep = rows.get(r.pod)
            // Without a pg_stat_replication row (not connected), the standby's
            // own replayed position still measures how far behind it is.
            const replayBacklog = rep ? lsnDistance(primary?.status.currentLsn, rep.replayLsn) : standbyOwnBacklog(primary?.status, r.status)
            const otherTimeline =
              r.status.timeline !== undefined && primary?.status.timeline !== undefined && r.status.timeline !== primary.status.timeline ? r.status.timeline : undefined
            const backlogTone = cnpgStandbyBacklogTone(replayBacklog, rep?.replayLag)
            const headline = cnpgStandbyHeadline(r, rep, backlogTone, { fenced: fenced.has(r.pod), primaryRead: primary?.status.state === 'ok' })
            const tone = headline.tone
            const pct = replayBacklog !== undefined ? Math.min(100, (replayBacklog / CNPG_BACKLOG_DEGRADED) * 100) : 0
            return (
              <div key={r.pod} className="rounded-lg border border-theme-border bg-theme-base p-3">
                <div className="flex flex-wrap items-center gap-2">
                  <StatusDot tone={tone} />
                  <span className="font-mono text-sm font-semibold">{r.pod}</span>
                  <span className={clsx('text-xs', toneTextClass(tone))}>{headline.text}</span>
                  {headline.secondary && <span className="text-xs text-theme-text-secondary">{headline.secondary}</span>}
                  <span className="ml-auto font-mono text-xs text-theme-text-secondary">
                    {replayBacklog !== undefined ? `${formatBytes(replayBacklog)} behind` : otherTimeline !== undefined ? 'backlog not comparable' : 'backlog unknown'}
                  </span>
                </div>
                {otherTimeline !== undefined && !rep && (
                  <div className={clsx('mt-1 text-xs', toneTextClass('degraded'))}>
                    On timeline {otherTimeline} while the primary is on {primary?.status.timeline}: it has not followed the primary’s last promotion, so how far behind it is can’t be
                    measured from WAL positions.
                  </div>
                )}
                {replayBacklog !== undefined && (
                  <div className="mt-2 flex items-center gap-2">
                    <div className="h-1 flex-1 overflow-hidden rounded bg-theme-elevated">
                      <div className={clsx('h-full', backlogTone === 'unknown' ? 'bg-transparent' : toneFillClass(backlogTone))} style={{ width: `${pct}%` }} />
                    </div>
                    <span className="text-[11px] text-theme-text-tertiary">scale: one 16 MiB WAL segment</span>
                  </div>
                )}
                {rep && <Backlog primary={primary} rep={rep} />}
                {rep && <Delays rep={rep} />}
                <div className="mt-1 font-mono text-xs text-theme-text-tertiary">
                  received {r.status.receivedLsn ?? '—'} · replayed {r.status.replayLsn ?? '—'}
                </div>
                {slotsRead && <SlotLine slot={slots.get(cnpgHASlotName(clusterObject, r.pod))} name={cnpgHASlotName(clusterObject, r.pod)} />}
                <PodLine pod={pods.get(r.pod)} />
                <InstanceFacts inst={r} primaryVersion={primaryVersion} />
                <SourceState label="Status" state={r.status.state} error={r.status.error ?? r.status.reason} />
                <div className="mt-2 flex flex-wrap gap-3 text-xs">
                  {onOpenLogs && (
                    <button type="button" className="text-accent-text hover:underline" onClick={() => onOpenLogs(r.pod)}>
                      Logs
                    </button>
                  )}
                  <CNPGInstanceActions namespace={namespace} cluster={cluster} pod={r.pod} />
                </div>
              </div>
            )
          })}
        </div>
      </div>
    </Card>
  )
}
