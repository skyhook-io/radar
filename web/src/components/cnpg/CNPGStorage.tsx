import { useState, type ReactNode } from 'react'
import { clsx } from 'clsx'
import yaml from 'yaml'
import { HardDrive } from 'lucide-react'
import {
  ActionConfirmDialog,
  AlertBanner,
  Badge,
  CNPG_DISK_SOURCE,
  PaneLoader,
  StatusDot,
  Tooltip,
  cnpgDiskTone,
  cnpgVolumeRoleLabel,
  formatAge,
  formatBytes,
  parseQuantityToNumber,
  toneFillClass,
  toneTextClass,
} from '@skyhook-io/k8s-ui'
import { useResource } from '../../api/client'
import type { CNPGRuntimeInstance } from '../../api/cnpg'
import {
  useCNPGClusterStorage,
  type CNPGClusterStorageResponse,
  type CNPGStorageInstance,
  type CNPGStorageTarget,
  type CNPGStorageVolume,
  type CNPGStorageWAL,
} from '../../api/cnpg-storage'
import { Notice } from '../capacity/shared'
import { CreateResourceDialog } from '../shared/CreateResourceDialog'
import { useCNPGWriteGuard } from './actions/useCNPGWriteGuard'
import { buildResizeManifest } from './storageModel'

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

function UsageBar({ v }: { v: CNPGStorageVolume }) {
  const u = v.usage
  if (u.state !== 'ok' || u.ratio === undefined || u.usedBytes === undefined || u.capacityBytes === undefined) {
    return (
      <div>
        <div className="h-1.5 rounded bg-[repeating-linear-gradient(45deg,var(--border-light)_0_2px,transparent_2px_5px)]" />
        <div className="mt-1 text-xs text-theme-text-tertiary">Used space unknown: {USAGE_UNMEASURED[u.state] ?? u.state}</div>
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

function ClassFact({ v }: { v: CNPGStorageVolume }) {
  const sc = v.storageClass
  if (!sc.name) return <span className="text-theme-text-tertiary">class not named on the claim</span>
  if (sc.allowVolumeExpansion === undefined) {
    return (
      <span>
        class <span className="font-mono">{sc.name}</span> <span className="text-theme-text-tertiary">· expansion unknown ({sc.reason})</span>
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

function VolumeRow({ v }: { v: CNPGStorageVolume }) {
  const resizing = v.resize.pending || (v.resize.conditions?.length ?? 0) > 0 || !!v.resize.allocatedStatus
  return (
    <div className="rounded-lg border border-theme-border bg-theme-base p-3">
      <div className="mb-2 flex flex-wrap items-center gap-2">
        <span className="text-sm font-medium text-theme-text-primary">{roleTitle(v)}</span>
        <span className="font-mono text-xs text-theme-text-secondary">{v.claim}</span>
        {v.clusterState && CLUSTER_STATE_BADGE[v.clusterState] && (
          <Tooltip content={`Named in the Cluster's status.${v.clusterState}PVC`}>
            <Badge severity={CLUSTER_STATE_BADGE[v.clusterState]} size="sm">{v.clusterState}</Badge>
          </Tooltip>
        )}
        {v.phase && v.phase !== 'Bound' && <Badge severity="warning" size="sm">{v.phase}</Badge>}
        <span className="ml-auto font-mono text-xs text-theme-text-secondary">
          capacity {v.capacity ?? '—'}
          {v.requested && v.requested !== v.capacity ? ` · requested ${v.requested}` : ''}
        </span>
      </div>
      <UsageBar v={v} />
      <div className="mt-2 text-xs text-theme-text-secondary">
        <ClassFact v={v} />
      </div>
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
  )
}

function WALHolders({ wal, primary }: { wal: CNPGStorageWAL; primary: boolean }) {
  if (wal.status.state !== 'ok' && wal.metrics.state !== 'ok') {
    return (
      <div className="text-xs text-theme-text-tertiary">
        WAL facts unavailable: {wal.status.error || wal.metrics.error || wal.status.state}
      </div>
    )
  }
  const slots = wal.slots ?? []
  const retained = slots.reduce((m, s) => Math.max(m, s.bytes), 0)
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
          value={wal.readyToArchive !== undefined ? `${wal.readyToArchive} files` : '—'}
          tone={wal.archivingFailed ? 'unhealthy' : wal.readyToArchive ? 'degraded' : undefined}
          detail={
            wal.archivingFailed
              ? `archiving failing${wal.lastFailedWal ? ` at ${wal.lastFailedWal}` : ''}${wal.lastFailedAt ? `, ${formatAge(wal.lastFailedAt)} ago` : ''}`
              : !primary
                ? 'a standby; the primary archives'
                : wal.lastArchivedAt
                  ? `last archived ${formatAge(wal.lastArchivedAt)} ago${wal.readyToArchive === undefined ? ' · backlog not reported' : ''}`
                  : undefined
          }
          source="Instance manager readyWalFiles"
          missing={wal.status.state !== 'ok' ? wal.status.error || wal.status.reason || wal.status.state : undefined}
        />
        <WALFact
          label="Held by replication slots"
          value={wal.metrics.state !== 'ok' ? '—' : slots.length === 0 ? 'No slots' : `up to ${formatBytes(retained)}`}
          detail={slots.length > 0 ? slots.map((s) => `${s.slot} ${formatBytes(s.bytes)}`).join(' · ') : undefined}
          source="Exporter pg_replication_slots"
          missing={wal.metrics.state !== 'ok' ? wal.metrics.error || wal.metrics.reason || wal.metrics.state : undefined}
        />
      </div>
      <div className="mt-2 text-[11.5px] text-theme-text-tertiary">
        These overlap (a slot can hold the same segments that wait for the archive), so they are not added up.
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
  detail?: string
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

function InstanceCard({ inst, walCoverage }: { inst: CNPGStorageInstance; walCoverage: CNPGClusterStorageResponse['wal'] }) {
  const roleLabel = inst.role === 'primary' ? 'primary' : inst.role === 'replica' ? 'replica' : inst.role === 'noInstance' ? 'no instance' : 'role unknown'
  const worst = inst.volumes.reduce<number | undefined>((m, v) => (v.usage.ratio !== undefined && (m === undefined || v.usage.ratio > m) ? v.usage.ratio : m), undefined)
  return (
    <Card
      title={
        <span className="flex items-center gap-2">
          <StatusDot tone={worst === undefined ? 'unknown' : cnpgDiskTone(worst)} />
          <span className="font-mono">{inst.name}</span>
          <span className="badge-sm bg-theme-elevated text-theme-text-secondary">{roleLabel}</span>
        </span>
      }
    >
      <div className="space-y-2">
        {inst.volumes.length === 0 ? (
          <div className="text-sm text-theme-text-tertiary">No claims read for this instance.</div>
        ) : (
          inst.volumes.map((v) => <VolumeRow key={v.claim} v={v} />)
        )}
      </div>
      <div className="mt-4">
        <div className="mb-2 text-[11px] font-semibold uppercase tracking-wide text-theme-text-tertiary">What is holding WAL</div>
        {inst.wal ? (
          <WALHolders wal={inst.wal} primary={inst.role === 'primary'} />
        ) : (
          <div className="text-xs text-theme-text-tertiary">
            {walCoverage.state === 'denied' ? `No access: needs ${walCoverage.grant}` : walCoverage.reason ?? 'No running instance to read'}
          </div>
        )}
      </div>
    </Card>
  )
}

function coverageLine(label: string, c: { state: string; grant?: string; reason?: string }): string | null {
  if (c.state === 'ok') return null
  if (c.state === 'denied') return `${label}: no access (needs ${c.grant})`
  return `${label}: ${c.reason ?? c.state}`
}

export function CNPGStorage({ namespace, name, primary }: { namespace: string; name: string; primary?: CNPGRuntimeInstance }) {
  const q = useCNPGClusterStorage(namespace, name)
  const [resize, setResize] = useState<CNPGStorageTarget | null>(null)
  if (!q.data && q.isLoading) return <PaneLoader label="Reading volumes…" className="h-40" />
  if (!q.data) {
    return <Notice>Storage could not be loaded: {q.error instanceof Error ? q.error.message : 'unknown error'}</Notice>
  }
  const data = q.data
  const notes = [
    coverageLine('Volumes', data.volumes),
    data.usage.state !== 'notRead' ? coverageLine('Used space', data.usage) : null,
    coverageLine('WAL', data.wal),
  ].filter((x): x is string => !!x)
  const allVolumes = data.instances.flatMap((i) => i.volumes)

  return (
    <div className="space-y-4">
      {data.findings.map((f) => (
        <AlertBanner
          key={f.claim}
          variant={f.severity === 'critical' ? 'error' : 'warning'}
          title={f.message}
          message={`Claim ${f.claim}; measured from ${CNPG_DISK_SOURCE}. Expand the volume below, or find what is growing (WAL held for the archive or by a slot is shown per instance).`}
        />
      ))}
      {notes.length > 0 && (
        <Notice>
          {notes.map((n) => (
            <div key={n}>{n}</div>
          ))}
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

      <div className="grid items-start gap-4 xl:grid-cols-2">
        {data.instances.map((inst) => (
          <InstanceCard key={inst.name} inst={inst} walCoverage={data.wal} />
        ))}
      </div>

      <ExpansionCard data={data} volumes={allVolumes} onResize={setResize} />

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
            {!primary
              ? 'No primary reported.'
              : primary.metrics.state === 'ok' || primary.metrics.state === 'partial'
                ? "Not reported: this sample from the primary's exporter has no database sizes."
                : `Not available from the primary's exporter: ${primary.metrics.error || primary.metrics.reason || (primary.metrics.state === 'denied' ? 'no access (needs get pods/proxy)' : primary.metrics.state)}.`}
          </div>
        )}
      </Card>

      {resize && <ResizeDialog namespace={namespace} name={name} target={resize} volumes={allVolumes} onClose={() => setResize(null)} />}
    </div>
  )
}

function targetVolumes(t: CNPGStorageTarget, volumes: CNPGStorageVolume[]): CNPGStorageVolume[] {
  return volumes.filter((v) => v.role === t.role && (t.role !== 'PG_TABLESPACE' || v.tablespace === t.tablespace))
}

function expansionVerdict(vols: CNPGStorageVolume[]): { text: string; tone?: 'degraded' } {
  if (vols.length === 0) return { text: 'No claims read, so whether the class allows expansion is unknown' }
  const known = vols.filter((v) => v.storageClass.allowVolumeExpansion !== undefined)
  if (known.length < vols.length) return { text: 'Whether the class allows expansion is unknown for some claims' }
  if (known.every((v) => v.storageClass.allowVolumeExpansion)) return { text: 'The StorageClass allows expansion: the operator resizes each claim' }
  return {
    text: 'The StorageClass does not allow expansion: a larger size will not resize the existing claims',
    tone: 'degraded',
  }
}

function ExpansionCard({ data, volumes, onResize }: { data: CNPGClusterStorageResponse; volumes: CNPGStorageVolume[]; onResize: (t: CNPGStorageTarget) => void }) {
  const inUse = data.expansion.resizeInUseVolumes
  return (
    <Card
      title={
        <span className="flex items-center gap-2">
          <HardDrive className="h-4 w-4" />
          Expanding volumes
        </span>
      }
      footer={
        inUse === false
          ? 'spec.storage.resizeInUseVolumes is false: the operator does not resize claims while their Pod uses them. See the CloudNativePG volume expansion documentation for the offline procedure.'
          : 'Sizes are declared on the Cluster and the operator applies them to every instance. CloudNativePG does not shrink volumes.'
      }
    >
      <div className="space-y-3">
        {data.expansion.targets.map((t) => {
          const verdict = expansionVerdict(targetVolumes(t, volumes))
          return (
            <div key={t.field} className="flex flex-wrap items-center gap-x-4 gap-y-1">
              <div className="min-w-0 flex-1">
                <div className="text-sm text-theme-text-primary">
                  {roleTitle(t)} · <span className="font-mono text-xs">{t.field}</span>
                  <span className="text-theme-text-secondary"> = {t.declared ?? 'not set'}</span>
                </div>
                <div className={clsx('text-xs', verdict.tone ? toneTextClass(verdict.tone) : 'text-theme-text-tertiary')}>{verdict.text}</div>
              </div>
              <button
                type="button"
                onClick={() => onResize(t)}
                className="inline-flex items-center gap-1.5 rounded-lg border border-theme-border bg-theme-surface px-2.5 py-1.5 text-xs font-medium text-theme-text-primary hover:bg-theme-hover"
              >
                Edit size…
              </button>
            </div>
          )
        })}
      </div>
    </Card>
  )
}

function ResizeDialog({
  namespace,
  name,
  target,
  volumes,
  onClose,
}: {
  namespace: string
  name: string
  target: CNPGStorageTarget
  volumes: CNPGStorageVolume[]
  onClose: () => void
}) {
  const { data: cluster } = useResource<any>('clusters', namespace, name, CNPG_GROUP)
  const [size, setSize] = useState(target.declared ?? '')
  const [manifest, setManifest] = useState<string | null>(null)
  const guard = useCNPGWriteGuard({ namespace, name, scope: { kind: 'spec', paths: [target.field] } })
  const verdict = expansionVerdict(targetVolumes(target, volumes))

  if (manifest) {
    return <CreateResourceDialog open onClose={onClose} initialYaml={manifest} initialMode="apply" title={`Resize ${roleTitle(target).toLowerCase()} volumes of ${name}`} />
  }
  const next = parseQuantityToNumber(size)
  const current = parseQuantityToNumber(target.declared)
  const disabledReason = !cluster
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
      title={`Resize ${cnpgVolumeRoleLabel(target.role, target.tablespace)}s of ${name}?`}
      subject={{ kind: 'Cluster', namespace, name }}
      effect={
        <>
          Changes <span className="font-mono">{target.field}</span> from {target.declared ?? 'unset'} to the size you enter. {verdict.text}.
        </>
      }
      guard={guard.node}
      guardSatisfied={guard.satisfied}
      confirmLabel="Review manifest"
      disabledReason={disabledReason}
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
