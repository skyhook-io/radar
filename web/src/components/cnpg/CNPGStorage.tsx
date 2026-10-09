import { useState, type ReactNode, type CSSProperties } from 'react'
import { clsx } from 'clsx'
import yaml from 'yaml'
import { HardDrive } from 'lucide-react'
import {
  ActionConfirmDialog,
  AlertBanner,
  Badge,
  FoldSection,
  type CNPGProtectionFacts,
  CNPG_DISK_SOURCE,
  PaneLoader,
  StatusDot,
  Tooltip,
  cnpgDiskTone,
  cnpgHASlotInstance,
  cnpgVolumeRoleLabel,
  formatAge,
  parseQuantityToNumber,
  toneFillClass,
  toneTextClass,
  formatGrant,
  type Grant,
} from '@skyhook-io/k8s-ui'
import { useResource } from '../../api/client'
import { useCNPGClusterCapabilities, type CNPGRuntimeInstance, type CNPGRuntimeResponse } from '../../api/cnpg'
import type { UseQueryResult } from '@tanstack/react-query'
import {
  useCNPGClusterStorage,
  type CNPGClusterStorageResponse,
  type CNPGStorageInstance,
  type CNPGStorageTarget,
  type CNPGStorageVolume,
  type CNPGStorageWAL,
} from '../../api/cnpg-storage'
import { CreateResourceDialog } from '../shared/CreateResourceDialog'
import { useCNPGWriteGuard } from './actions/useCNPGWriteGuard'
import { buildResizeManifest, cnpgFloorTone, cnpgInstanceDiskTone, cnpgSharedExpansionGap, cnpgSlotRetentionText, cnpgWALUsageFloor } from './storageModel'
// Binary units throughout, matching claim capacities such as 1Gi.
import { formatBytes } from './lsn'
import { cnpgDimensionPath, cnpgWithinDetail } from './paths'
import type { CNPGRestoreSourceState } from './recovery/restoreModel'
import { useCNPGNavigate } from './useCNPGNavigate'
import { useLocation } from 'react-router-dom'
import { GrantText, Notice, RefreshFailedNotice } from '../workspace/layout'

const CNPG_GROUP = 'postgresql.cnpg.io'

function Card({ title, children, footer }: { title: ReactNode; children: ReactNode; footer?: ReactNode }) {
  return (
    <section className="overflow-hidden rounded-xl border border-theme-border bg-theme-surface shadow-theme-sm">
      <div className="border-b border-theme-border px-4 py-2.5 text-sm font-semibold text-theme-text-primary">{title}</div>
      <div className="p-4">{children}</div>
      {footer && <div className="border-t border-theme-border px-4 py-2 text-xs text-theme-text-tertiary">{footer}</div>}
    </section>
  )
}

function roleTitle(v: { role: string; tablespace?: string }): string {
  switch (v.role) {
    case 'PG_DATA':
      return 'Data'
    case 'PG_WAL':
      return 'WAL'
    case 'PG_TABLESPACE':
      return v.tablespace ? `Tablespace ${v.tablespace}` : 'Tablespace'
    default:
      return 'Volume'
  }
}

const USAGE_UNMEASURED: Record<string, string> = {
  noSeries: 'Prometheus has no kubelet volume stats for this claim',
  invalid: 'kubelet reported values that are not a measurement',
  noPrometheus: 'used space needs Prometheus with kubelet volume stats',
  denied: 'no access to its usage metrics',
  error: 'the Prometheus query failed',
  notRead: 'not read',
  ambiguous: 'these claim names report under more than one cluster in this Prometheus',
  scopeMismatch: "this cluster's proven identity labels are not on its volume stats",
}

// `stated`: the page's notice already names a cluster-wide cause, so the
// line only says unknown and keeps the reason on hover.
function UsageBar({ v, stated, floor }: { v: CNPGStorageVolume; stated?: boolean; floor?: { bytes: number; ratio?: number } }) {
  const u = v.usage
  if (u.state !== 'ok' && floor) {
    const tone = cnpgFloorTone(floor.ratio)
    return (
      <div>
        <div className="h-1.5 overflow-hidden rounded bg-[repeating-linear-gradient(45deg,var(--border-light)_0_2px,transparent_2px_5px)]">
          {floor.ratio !== undefined && <div className={clsx('h-full', toneFillClass(tone === 'unknown' ? 'neutral' : tone))} style={{ width: `${Math.min(100, floor.ratio * 100)}%` }} />}
        </div>
        <div className="mt-1 text-xs">
          <span className={toneTextClass(tone === 'unknown' ? 'neutral' : tone)}>
            ≥ {formatBytes(floor.bytes)} used by WAL alone
            {floor.ratio !== undefined && (floor.ratio >= 1 ? `, more than the ${v.capacity} the claim reports` : ` · ≥ ${Math.round(floor.ratio * 100)}% of ${v.capacity}`)}
          </span>
          <span className="text-theme-text-tertiary">{' · '}</span>
          <Tooltip content={USAGE_UNMEASURED[u.state] ?? u.state}>
            <span className="text-theme-text-tertiary">the rest unknown</span>
          </Tooltip>
        </div>
      </div>
    )
  }
  if (u.state !== 'ok' || u.ratio === undefined || u.usedBytes === undefined || u.capacityBytes === undefined) {
    return (
      <div>
        <div className="h-1.5 rounded bg-[repeating-linear-gradient(45deg,var(--border-light)_0_2px,transparent_2px_5px)]" />
        {stated ? (
          <Tooltip content={USAGE_UNMEASURED[u.state] ?? u.state}>
            <div className="mt-1 text-xs text-theme-text-tertiary">Used space unknown</div>
          </Tooltip>
        ) : (
          <div className="mt-1 text-xs text-theme-text-tertiary">Used space unknown: {USAGE_UNMEASURED[u.state] ?? u.state}</div>
        )}
      </div>
    )
  }
  const tone = cnpgDiskTone(u.ratio)
  const sharedFilesystem = v.capacityBytes !== undefined && u.capacityBytes > v.capacityBytes * 1.5
  return (
    <div>
      <div className="h-1.5 overflow-hidden rounded bg-theme-elevated">
        <div className={clsx('h-full', toneFillClass(tone))} style={{ width: `${Math.min(100, u.ratio * 100)}%` }} />
      </div>
      <div className="mt-1 flex flex-wrap justify-between gap-x-3 text-xs">
        <span className={toneTextClass(tone === 'healthy' ? 'neutral' : tone)}>
          {Math.round(u.ratio * 100)}% used · {formatBytes(u.usedBytes)} of {formatBytes(u.capacityBytes)}
        </span>
        <Tooltip content={`Filesystem use as kubelet reports it (${CNPG_DISK_SOURCE})`}>
          <span className="text-theme-text-tertiary">kubelet</span>
        </Tooltip>
      </div>
      {sharedFilesystem && (
        <div className="mt-0.5 text-[11.5px] text-theme-text-tertiary">
          kubelet measured a {formatBytes(u.capacityBytes)} filesystem for a {v.capacity} claim: the volume shares a filesystem, so this is that filesystem's use.
        </div>
      )}
    </div>
  )
}

// Causes the page's notice already states, so each volume only says "unknown".
interface StatedOnce {
  usage: boolean
  expansion: boolean
}

function ClassFact({ v, expansionStated }: { v: CNPGStorageVolume; expansionStated?: boolean }) {
  const sc = v.storageClass
  if (!sc.name) return <span className="text-theme-text-tertiary">class not named on the claim</span>
  if (sc.allowVolumeExpansion === undefined) {
    return (
      <span>
        class <span className="font-mono">{sc.name}</span>{' '}
        {expansionStated ? (
          <Tooltip content={sc.reason}>
            <span className="text-theme-text-tertiary">· expansion unknown</span>
          </Tooltip>
        ) : (
          <span className="text-theme-text-tertiary">· expansion unknown ({sc.reason})</span>
        )}
      </span>
    )
  }
  return (
    <span>
      class <span className="font-mono">{sc.name}</span> ·{' '}
      <span className={sc.allowVolumeExpansion ? undefined : toneTextClass('degraded')}>
        {sc.allowVolumeExpansion ? 'allows expansion' : 'does not allow expansion'}
      </span>
    </span>
  )
}

const CLUSTER_STATE_BADGE: Record<string, 'warning' | 'error' | 'info'> = {
  resizing: 'info',
  initializing: 'info',
  dangling: 'warning',
  unusable: 'error',
}

function VolumeRow({ v, stated, wal }: { v: CNPGStorageVolume; stated: StatedOnce; wal?: CNPGStorageWAL }) {
  const resizing = v.resize.pending || (v.resize.conditions?.length ?? 0) > 0 || !!v.resize.allocatedStatus
  return (
    <div className="grid gap-1 rounded-lg border border-theme-border bg-theme-base p-3 xl:row-span-3 xl:grid-rows-subgrid">
      <div className="mb-2 space-y-1">
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-sm font-medium text-theme-text-primary">{roleTitle(v)}</span>
          <span className="font-mono text-xs text-theme-text-secondary">{v.claim}</span>
          <span className="ml-auto font-mono text-xs text-theme-text-secondary">
            {v.phase === 'Pending' && !v.capacity ? 'No capacity provisioned yet' : `capacity ${v.capacity ?? 'not reported'}`}
            {v.requested && v.requested !== v.capacity ? ` · requested ${v.requested}` : ''}
          </span>
        </div>
        <div className="flex min-h-5 flex-wrap items-center gap-2">
          {v.clusterState && CLUSTER_STATE_BADGE[v.clusterState] && (
            <Tooltip content={`Named in the Cluster's status.${v.clusterState}PVC`}>
              <Badge severity={CLUSTER_STATE_BADGE[v.clusterState]} size="sm">Operator: {v.clusterState}</Badge>
            </Tooltip>
          )}
          {v.phase && <Badge severity={v.phase === 'Bound' ? 'neutral' : 'warning'} size="sm">Claim: {v.phase}</Badge>}
        </div>
      </div>
      <UsageBar v={v} stated={stated.usage} floor={cnpgWALUsageFloor(v, wal)} />
      <div className="mt-2 text-xs text-theme-text-secondary">
        <ClassFact v={v} expansionStated={stated.expansion} />
        {resizing && (
        <div className="mt-1.5 text-xs text-theme-text-secondary">
          <span className="font-medium text-theme-text-primary">Resize</span>
          {v.resize.pending && <> · requested {v.requested} is larger than the capacity the claim reports</>}
          {v.resize.conditions?.map((c) => (
            <span key={c.type}>
              {' '}· <span className="font-mono">{c.type}</span>
              {c.message ? `: ${c.message}` : ''}
              {c.since ? ` (${formatAge(c.since)} ago)` : ''}
            </span>
          ))}
          {v.resize.allocatedStatus && <> · <span className="font-mono">{v.resize.allocatedStatus}</span></>}
        </div>
      )}
      </div>
    </div>
  )
}

function WALHolders({ wal, primary, slotStandby, walArchiving }: { walArchiving?: CNPGProtectionFacts['walArchiving']; wal: CNPGStorageWAL; primary: boolean; slotStandby: (slot: string) => string | undefined }) {
  const noArchive = walArchiving?.state === 'no_destination'
  const statusRead = ['ok', 'partial'].includes(wal.status.state)
  if (!noArchive && !statusRead && !['ok', 'partial'].includes(wal.metrics.state)) {
    return (
      <div className="text-xs text-theme-text-tertiary">
        WAL facts unavailable: {wal.status.error || wal.metrics.error || wal.status.state}
      </div>
    )
  }
  const slots = wal.slotInventory ?? []
  return (
    <div>
      <div className="grid gap-3 sm:grid-cols-3">
        <WALFact
          label="WAL on disk"
          value={wal.sizeBytes !== undefined ? formatBytes(wal.sizeBytes) : '—'}
          detail={wal.segments !== undefined ? `${wal.segments} segments` : undefined}
          source="Exporter cnpg_collector_pg_wal"
          missing={wal.metrics.state !== 'ok' ? wal.metrics.error || wal.metrics.reason || wal.metrics.state : undefined}
        />
        <WALFact
          label="Waiting to archive"
          value={noArchive ? walArchiving.text : wal.readyToArchive !== undefined ? `${wal.readyToArchive} files` : '—'}
          tone={noArchive ? undefined : wal.archivingFailed ? 'unhealthy' : wal.readyToArchive ? 'degraded' : undefined}
          detail={
            noArchive ? <FoldSection title="Instance manager record" summary="" attention={false}>
              <div>{!statusRead ? `Archiving record not read: ${wal.status.error || wal.status.reason || wal.status.state}.` : wal.lastArchivedAt ? `Last archived ${formatAge(wal.lastArchivedAt)} ago, as recorded by the instance manager.` : 'Last archived time not reported by the instance manager.'}</div>
              {statusRead && wal.readyToArchive !== undefined && <div>{wal.readyToArchive} files waiting, as recorded by the instance manager.</div>}
            </FoldSection> : wal.archivingFailed
              ? `archiving failing${wal.lastFailedWal ? ` at ${wal.lastFailedWal}` : ''}${wal.lastFailedAt ? `, ${formatAge(wal.lastFailedAt)} ago` : ''}`
              : !primary
                ? 'a standby; the primary archives'
                : wal.lastArchivedAt
                  ? `last archived ${formatAge(wal.lastArchivedAt)} ago${wal.readyToArchive === undefined ? ' · backlog not reported' : ''}`
                  : undefined
          }
          source="Instance manager readyWalFiles"
          missing={!noArchive && wal.status.state !== 'ok' ? wal.status.error || wal.status.reason || wal.status.state : undefined}
        />
        <WALFact
          label="Held by replication slots"
          value={cnpgSlotRetentionText(wal.slotInventory, !wal.slotInventoryTruncated, wal.slots, { separateMissingBytes: true })}
          detail={
            slots.length > 0
              ? <>{slots.some((s) => s.retainedBytes === undefined) && <div>retained WAL not reported</div>}<div>{slots
                  .map((s) => {
                    const standby = slotStandby(s.name)
                    return `${s.name}${s.retainedBytes === undefined ? '' : ` ${formatBytes(s.retainedBytes)}`}${standby ? ` (expected instance ${standby})` : ''}`
                  })
                  .join(' · ')}</div></>
              : wal.slots?.map((s) => `${s.slot} ${formatBytes(s.bytes)} retained WAL`).join(' · ')
          }
          source="Instance manager slot inventory · exporter retained WAL"
        />
      </div>
      <div className="mt-2 text-[11.5px] text-theme-text-tertiary">
        {noArchive ? 'Slot retention is part of WAL on disk, so these measurements are not added up.' : 'These overlap (a slot can hold the same segments that wait for the archive), so they are not added up.'}
        {wal.volume && <> WAL lives on <span className="font-mono">{wal.volume}</span>.</>}
      </div>
    </div>
  )
}

function WALFact({
  label,
  value,
  detail,
  source,
  tone,
  missing,
}: {
  label: string
  value: string
  detail?: ReactNode
  source: string
  tone?: 'degraded' | 'unhealthy'
  missing?: string
}) {
  return (
    <div className="card-inner">
      <Tooltip content={source}>
        <div className="text-xs text-theme-text-tertiary">{label}</div>
      </Tooltip>
      <div className={clsx('font-mono text-sm', tone ? toneTextClass(tone) : 'text-theme-text-primary')}>{missing ? '—' : value}</div>
      {(missing || detail) && <div className="mt-0.5 break-words text-[11.5px] text-theme-text-tertiary">{missing ? `unavailable: ${missing}` : detail}</div>}
    </div>
  )
}

function InstanceCard({
  inst,
  walCoverage,
  stated,
  slotStandby,
  notRunning,
  walArchiving,
  volumeRows,
}: {
  volumeRows: number
  walArchiving?: CNPGProtectionFacts['walArchiving']
  notRunning?: boolean
  inst: CNPGStorageInstance
  walCoverage: CNPGClusterStorageResponse['wal']
  stated: StatedOnce
  slotStandby: (slot: string) => string | undefined
}) {
  const roleLabel = notRunning ? inst.role === 'replica' ? 'expected standby · not running' : inst.role === 'primary' ? 'expected primary · not running' : inst.role === 'noInstance' ? 'no instance' : inst.volumes.some((v) => v.clusterState === 'initializing') ? 'expected first instance · not running' : 'expected instance · not running' : inst.role === 'primary' ? 'primary' : inst.role === 'replica' ? 'replica' : inst.role === 'noInstance' ? 'no instance' : 'role unknown'
  const diskTone = cnpgInstanceDiskTone(inst.volumes, inst.wal)
  return (
    <section className="grid gap-y-3 overflow-hidden rounded-xl border border-theme-border bg-theme-surface shadow-theme-sm xl:grid-rows-subgrid xl:[grid-row:span_var(--cnpg-instance-rows)]" style={{ '--cnpg-instance-rows': 2 + volumeRows * 3 } as CSSProperties}>
      <div className="flex items-center gap-2 border-b border-theme-border px-4 py-2.5 text-sm font-semibold text-theme-text-primary">
        <StatusDot tone={diskTone} />
        <span className="font-mono">{inst.name}</span>
        <Badge severity="neutral" size="sm">{roleLabel}</Badge>
      </div>
      {inst.volumes.map((v) => <div key={v.claim} className="mx-4 grid xl:row-span-3 xl:grid-rows-subgrid"><VolumeRow v={v} stated={stated} wal={inst.wal} /></div>)}
      {Array.from({ length: Math.max(0, volumeRows - inst.volumes.length) }, (_, i) => <div key={`unread-${i}`} className={clsx('mx-4 xl:row-span-3', !(i === 0 && inst.volumes.length === 0) && 'hidden xl:block')}>{i === 0 && inst.volumes.length === 0 && <div className="text-sm text-theme-text-tertiary">No claims read for this instance.</div>}</div>)}
      <div className="px-4 pb-4">
        <div className="mb-2 text-[11px] font-semibold uppercase tracking-wide text-theme-text-tertiary">What is holding WAL</div>
        {inst.wal ? (
          <WALHolders wal={inst.wal} primary={inst.role === 'primary'} slotStandby={slotStandby} walArchiving={walArchiving} />
        ) : notRunning ? (
          <div className="text-xs text-theme-text-tertiary">WAL cannot be measured until the instance starts</div>
        ) : walCoverage.state === 'ok' ? (
          <div className="text-xs text-theme-text-tertiary">No running instance to read</div>
        ) : (
          <Tooltip content={walCoverage.state === 'denied' ? `No access: needs ${formatGrant(walCoverage.grant)}` : walCoverage.reason ?? walCoverage.state}>
            <div className="text-xs text-theme-text-tertiary">Unknown</div>
          </Tooltip>
        )}
      </div>
    </section>
  )
}

// "<label> needs …" lines; `plural` for a label that takes "need".
function coverageLine(label: string, c: { state: string; grant?: Grant; reason?: string }, plural = false): string | null {
  const needs = plural ? 'need' : 'needs'
  if (c.state === 'ok') return null
  if (c.state === 'noPrometheus') return `${label} ${needs} Prometheus: ${c.reason?.split(/\s+Candidate /)[0] ?? 'Radar is not connected to one'}`
  if (c.state === 'denied') return `${label} ${needs} ${formatGrant(c.grant) ?? 'a grant you do not have'}.`
  return `${label}: ${c.reason ?? c.state}`
}

export function CNPGStorage({ namespace, name, primary, runtime, clusterObject, restoreState = 'unknown', walArchiving }: { restoreState?: CNPGRestoreSourceState; walArchiving?: CNPGProtectionFacts['walArchiving']; namespace: string; name: string; primary?: CNPGRuntimeInstance; runtime?: UseQueryResult<CNPGRuntimeResponse>; clusterObject?: any }) {
  const q = useCNPGClusterStorage(namespace, name)
  const caps = useCNPGClusterCapabilities(namespace, name)
  const patch = caps.data?.actions.reload
  const resizeReason = caps.error && !caps.data
    ? `Permissions could not be checked: ${caps.error.message}`
    : !patch
      ? 'Checking permissions…'
      : patch.permission === 'denied'
        ? `Needs ${formatGrant(patch.grant)}`
        : patch.permission === 'unknown'
          ? 'Patch permission could not be checked.'
          : caps.data?.facts.terminating
            ? 'The Cluster is being deleted.'
            : caps.data?.operator?.webhookRejects
              ? `The API server would reject it: ${caps.data.operator.webhookReason}`
              : undefined
  const canResize = patch?.permission === 'allowed' && !resizeReason
  const [resize, setResize] = useState<CNPGStorageTarget | null>(null)
  if (!q.data && q.isLoading) return <PaneLoader label="Reading volumes…" className="h-40" />
  if (!q.data) {
    return <Notice>Storage could not be loaded: {q.error instanceof Error ? q.error.message : 'unknown error'}</Notice>
  }
  const data = q.data
  const notes = [
    coverageLine('Volumes', data.volumes, true),
    data.usage.state !== 'notRead' ? coverageLine('Used space', data.usage) : null,
    coverageLine('WAL', data.wal),
  ].filter((x): x is string => !!x)
  if (data.usage.isolation?.mode === 'unverified') notes.push(`Used space: ${data.usage.isolation.note}.`)
  const discoveryCandidates = data.usage.state === 'noPrometheus' ? data.usage.reason?.match(/\s+(Candidate [\s\S]*)$/)?.[1] : undefined
  const allVolumes = data.instances.flatMap((i) => i.volumes)
  const volumeRows = Math.max(1, ...data.instances.map((i) => i.volumes.length))
  const notStarted = !!clusterObject && !clusterObject.status?.currentPrimary && !!runtime?.data && runtime.data.instances.length === 0 && data.instances.length > 0 && allVolumes.length > 0 && allVolumes.every((v) => v.clusterState === 'initializing' && v.phase === 'Pending' && !v.capacity)
  // The standby a CloudNativePG HA slot is kept for, so retained WAL names its cause.
  const slotStandby = (slot: string) => cnpgHASlotInstance(clusterObject, slot, data.instances.map((i) => i.name))
  const expansionGap = cnpgSharedExpansionGap(allVolumes)
  if (expansionGap) notes.push(`Volume expansion unknown: ${expansionGap}`)
  const stated: StatedOnce = { usage: data.usage.state !== 'ok' && data.usage.state !== 'notRead', expansion: !!expansionGap }

  return (
    <div className="space-y-4">
      <RefreshFailedNotice queries={runtime ? [q, runtime, caps] : [q, caps]} />
      {data.findings.map((f) => (
        <AlertBanner
          key={f.claim}
          variant={f.severity === 'critical' ? 'error' : 'warning'}
          title={f.message}
          message={`Claim ${f.claim}; measured from ${CNPG_DISK_SOURCE}. Check volume expansion below, or find what is growing (WAL held for the archive or by a slot is shown per instance).`}
        />
      ))}
      {notes.length > 0 && (
        <Notice>
          {notes.map((n) => (
            <div key={n} className="whitespace-pre-line">{n}</div>
          ))}
          {discoveryCandidates && <div className="mt-2"><FoldSection title="Discovery candidates" summary="" attention={false}><div>{discoveryCandidates}</div></FoldSection></div>}
        </Notice>
      )}
      {data.excluded && data.excluded.length > 0 && (
        <Notice>
          Not counted as this cluster's volumes:{' '}
          {data.excluded.map((e) => (
            <span key={e.claim}>
              <span className="font-mono">{e.claim}</span> ({e.reason}){' '}
            </span>
          ))}
        </Notice>
      )}

      <div className="grid gap-4 xl:grid-cols-2">
        {data.instances.map((inst) => (
          <InstanceCard volumeRows={volumeRows} key={inst.name} inst={inst} notRunning={!!runtime?.data && !runtime.data.instances.some((i) => i.pod === inst.name)} walCoverage={data.wal} stated={stated} slotStandby={slotStandby} walArchiving={walArchiving} />
        ))}
      </div>

      <ExpansionCard restoreState={restoreState} notStarted={notStarted} namespace={namespace} name={name} data={data} volumes={allVolumes} onResize={setResize} canResize={canResize} resizeReason={resizeReason} grant={patch?.permission === 'denied' ? patch.grant : undefined} onRetry={() => void caps.refetch()} />

      <Card title="Logical database sizes (primary)" footer="pg_database_size for each database: the data PostgreSQL holds, not the space the volume uses.">
        {(primary?.metrics.state === 'ok' || primary?.metrics.state === 'partial') && primary.metrics.databaseSizes?.length ? (
          primary.metrics.databaseSizes.map((d) => (
            <div key={d.database} className="flex justify-between font-mono text-sm">
              <span className="truncate">{d.database}</span>
              <span>{formatBytes(d.bytes)}</span>
            </div>
          ))
        ) : (
          <div className="text-sm text-theme-text-tertiary">
            {runtime?.data?.permission.proxy === 'denied'
              ? <>Database sizes unavailable: needs <GrantText grant={runtime.data.permission.grant ?? { verb: 'get', resource: 'pods', subresource: 'proxy', namespace }} />.</>
              : !primary
              ? clusterObject?.status?.currentPrimary ? `Database sizes unavailable: ${clusterObject.status.currentPrimary} has not reported${runtime?.error ? ` (${runtime.error.message})` : ''}.` : 'No primary reported.'
              : primary.metrics.state === 'ok' || primary.metrics.state === 'partial'
                ? "Not reported: this sample from the primary's exporter has no database sizes."
                : `Not available from the primary's exporter: ${primary.metrics.error || primary.metrics.reason || (primary.metrics.state === 'denied' ? 'no access (needs get pods/proxy)' : primary.metrics.state)}.`}
          </div>
        )}
      </Card>

      {resize && <ResizeDialog restoreState={restoreState} notStarted={notStarted} namespace={namespace} name={name} target={resize} volumes={allVolumes} disabledReason={resizeReason} onClose={() => setResize(null)} />}
    </div>
  )
}

function targetVolumes(t: CNPGStorageTarget, volumes: CNPGStorageVolume[]): CNPGStorageVolume[] {
  return volumes.filter((v) => v.role === t.role && (t.role !== 'PG_TABLESPACE' || v.tablespace === t.tablespace))
}

function expansionVerdict(vols: CNPGStorageVolume[], restoreState: CNPGRestoreSourceState = 'unknown', notStarted = false): { text: string; tone?: 'degraded' } {
  if (vols.length === 0) return { text: 'No claims read, so whether the class allows expansion is unknown' }
  const blocked = vols.filter((v) => v.storageClass.allowVolumeExpansion === false)
  if (blocked.length > 0) return {
    text: blocked.length === vols.length
      ? 'The StorageClass does not allow expansion: a larger size will not resize the existing claims. ' + (notStarted ? 'The first instance has not started. Edit the declared size for future claims; the existing Pending claims keep their size and class. A fresh Cluster can use the new settings.' : restoreState === 'available' ? 'Restore into a new Cluster with a larger size or a class that expands, then move applications to it.' : restoreState === 'none' ? 'An expandable class applies to new claims. Set up backups before moving to a new Cluster.' : 'An expandable class applies to new claims. A restore source has not been verified.')
      : 'Some StorageClasses do not allow expansion: their existing claims will not grow. Editing changes the declared size.',
    tone: 'degraded',
  }
  const known = vols.filter((v) => v.storageClass.allowVolumeExpansion !== undefined)
  if (known.length < vols.length) return { text: 'Whether the class allows expansion is unknown for some claims' }
  return { text: 'The StorageClass allows expansion: the operator can resize each claim' }
}

function ExpansionCard({ restoreState, notStarted, namespace, name, data, volumes, onResize, canResize, resizeReason, grant, onRetry }: { restoreState: CNPGRestoreSourceState; notStarted: boolean; namespace: string; name: string; data: CNPGClusterStorageResponse; volumes: CNPGStorageVolume[]; onResize: (t: CNPGStorageTarget) => void; canResize: boolean; resizeReason?: string; grant?: Grant; onRetry: () => void }) {
  const navigate = useCNPGNavigate()
  const location = useLocation()
  const inUse = data.expansion.resizeInUseVolumes
  return (
    <Card
      title={
        <span className="flex items-center gap-2">
          <HardDrive className="h-4 w-4" />
          Declared volume sizes
        </span>
      }
      footer={
        inUse === false
          ? 'spec.storage.resizeInUseVolumes is false: the operator does not resize claims while their Pod uses them. See the CloudNativePG volume expansion documentation for the offline procedure.'
          : 'Editing changes the Cluster’s declared size. Existing claims can grow only if their StorageClass allows expansion. CloudNativePG does not shrink volumes.'
      }
    >
      {!canResize && <div className="mb-3 text-xs text-theme-text-secondary">
        {grant ? <>Editing size needs <GrantText grant={grant} />.</> : resizeReason}{' '}
        <button type="button" onClick={onRetry} className="text-accent-text hover:underline">Retry</button>
      </div>}
      <div className="space-y-3">
        {data.expansion.targets.map((t) => {
          const verdict = expansionVerdict(targetVolumes(t, volumes), restoreState, notStarted)
          return (
            <div key={t.field} className="flex flex-wrap items-center gap-x-4 gap-y-1">
              <div className="min-w-0 flex-1">
                <div className="text-sm text-theme-text-primary">
                  {roleTitle(t)} · <span className="font-mono text-xs">{t.field}</span>
                  <span className="text-theme-text-secondary"> = {t.declared ?? 'not set'}</span>
                </div>
                <div className={clsx('text-xs', verdict.tone ? toneTextClass(verdict.tone) : 'text-theme-text-tertiary')}>{verdict.text}{verdict.tone && restoreState === 'none' && !notStarted && targetVolumes(t, volumes).every((v) => v.storageClass.allowVolumeExpansion === false) && <>{' '}<button type="button" onClick={() => { const target = cnpgDimensionPath(namespace, name, new URLSearchParams(location.search).get('ctx') ?? undefined, 'protection'); const inPlace = cnpgWithinDetail(location.pathname, location.search, target); navigate(inPlace ?? target, { replace: !!inPlace, state: location.state }) }} className="text-accent-text hover:underline">Backups →</button></>}</div>
              </div>
              <button
                type="button"
                disabled={!canResize}
                onClick={() => onResize(t)}
                className="btn-secondary inline-flex shrink-0 items-center gap-1.5 whitespace-nowrap px-2.5 py-1.5 text-xs font-medium"
              >
                {verdict.tone === 'degraded' ? targetVolumes(t, volumes).every((v) => v.storageClass.allowVolumeExpansion === false) ? 'Edit declared size — existing volumes unchanged' : 'Edit declared size…' : 'Edit size…'}
              </button>
            </div>
          )
        })}
      </div>
    </Card>
  )
}

function ResizeDialog({
  restoreState,
  notStarted,
  namespace,
  name,
  target,
  volumes,
  disabledReason,
  onClose,
}: {
  namespace: string
  name: string
  target: CNPGStorageTarget
  restoreState: CNPGRestoreSourceState
  notStarted: boolean
  volumes: CNPGStorageVolume[]
  disabledReason?: string
  onClose: () => void
}) {
  const { data: cluster } = useResource<any>('clusters', namespace, name, CNPG_GROUP)
  const [size, setSize] = useState(target.declared ?? '')
  const [manifest, setManifest] = useState<string | null>(null)
  const guard = useCNPGWriteGuard({ namespace, name, scope: { kind: 'spec', paths: [target.field] } })
  const verdict = expansionVerdict(targetVolumes(target, volumes), restoreState, notStarted)

  if (manifest) {
    return <CreateResourceDialog open onClose={onClose} initialYaml={manifest} initialMode="apply" title={`Edit declared ${roleTitle(target).toLowerCase()} size of ${name}`} />
  }
  const next = parseQuantityToNumber(size)
  const current = parseQuantityToNumber(target.declared)
  const incompleteReason = !cluster
    ? 'Loading the cluster…'
    : !next
      ? 'Enter a size such as 20Gi.'
      : current && next <= current
        ? `CloudNativePG only grows volumes; enter more than ${target.declared}.`
        : undefined

  return (
    <ActionConfirmDialog
      open
      onClose={onClose}
      onConfirm={() => {
        setManifest(
          `# Sets ${target.field} on ${name}. Server-side apply changes only the fields below;\n` +
            `# the operator then resizes each instance's claim if its StorageClass allows expansion.\n` +
            yaml.stringify(buildResizeManifest(cluster, target, size.trim())),
        )
      }}
      title={`Edit declared ${cnpgVolumeRoleLabel(target.role, target.tablespace)} size of ${name}?`}
      subject={{ kind: 'Cluster', namespace, name }}
      effect={
        <>
          Changes <span className="font-mono">{target.field}</span> from {target.declared ?? 'unset'} to the size you enter. {verdict.text}
        </>
      }
      guard={guard.node}
      guardSatisfied={guard.satisfied}
      confirmLabel="Review manifest"
      disabledReason={disabledReason}
      incompleteReason={incompleteReason}
    >
      <div className="grid grid-cols-[8rem_minmax(0,1fr)] items-center gap-x-3 gap-y-2">
        <label className="text-xs text-theme-text-secondary" htmlFor="cnpg-resize-size">New size</label>
        <input
          id="cnpg-resize-size"
          value={size}
          onChange={(e) => setSize(e.target.value)}
          className="w-40 rounded-lg border border-theme-border bg-theme-base px-2 py-1 font-mono text-sm"
        />
      </div>
    </ActionConfirmDialog>
  )
}
