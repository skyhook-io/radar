import { useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import {
  Tooltip,
  formatRelativeAgeTime,
  getBackupErrors,
  getBackupStatus,
  getBackupWarnings,
  getRestoreBackupName,
  getRestoreErrors,
  getRestoreNamespaceMapping,
  getRestoreStatus,
  getRestoreWarnings,
  veleroLocalTime,
} from '@skyhook-io/k8s-ui'
import { useResources } from '../../api/client'
import { FilterChips, ScreenBody, SectionTable, Segments, Sub, type TableColumn } from '../workspace/layout'
import { VeleroWorkspaceHeader, inInstallation, veleroResource, type VeleroScreenProps } from './shared'
import { FAILED, Outcome, durationText, expired, leftText } from './VeleroPages'
import { VELERO_GROUP } from './routes'

const SCHEDULE_LABEL = 'velero.io/schedule-name'

function started(r: any): string {
  return r.status?.startTimestamp ?? r.metadata?.creationTimestamp ?? ''
}

function When({ iso, fallback }: { iso?: string; fallback: string }) {
  if (!iso) return <span className="text-theme-text-tertiary">{fallback}</span>
  return (
    <Tooltip content={veleroLocalTime(iso)}>
      <span>{formatRelativeAgeTime(iso)}</span>
    </Tooltip>
  )
}

/** `labels=k=v,k2=v2` from a link (a schedule's "All runs"), as exact matches. */
function useLabelFilter(): [Record<string, string>, (key: string) => void] {
  const [params, setParams] = useSearchParams()
  const raw = params.get('labels') ?? ''
  const filter = useMemo(() => {
    const out: Record<string, string> = {}
    for (const part of raw.split(',')) {
      const [k, ...v] = part.split('=')
      if (k && v.length) out[k.trim()] = v.join('=').trim()
    }
    return out
  }, [raw])
  const clear = (key: string) => {
    const next = Object.entries(filter).filter(([k]) => k !== key).map(([k, v]) => `${k}=${v}`).join(',')
    const p = new URLSearchParams(params)
    if (next) p.set('labels', next)
    else p.delete('labels')
    setParams(p, { replace: true })
  }
  return [filter, clear]
}

function matches(labels: Record<string, string> | undefined, filter: Record<string, string>): boolean {
  return Object.entries(filter).every(([k, v]) => labels?.[k] === v)
}

/**
 * The Backup kind's list: every run across the installations the caller can
 * read, newest first, failed first when any failed. A link from a schedule
 * narrows it to that schedule's runs.
 */
export function VeleroBackups({ data, installation, onInstallation, onInspect, inspected }: VeleroScreenProps) {
  const backups = useResources<any>('backups', undefined, VELERO_GROUP)
  const [filter, clearFilter] = useLabelFilter()
  const all = useMemo(
    () => inInstallation((backups.data ?? []).map((b: any) => ({ ...b, namespace: b.metadata?.namespace ?? '' })), installation)
      .filter((b: any) => matches(b.metadata?.labels, filter))
      .sort((a: any, b: any) => started(b).localeCompare(started(a))),
    [backups.data, installation, filter],
  )
  const failed = all.filter((b: any) => FAILED.has(b.status?.phase ?? ''))
  const [view, setView] = useState<'failed' | 'all' | null>(null)
  const current = view ?? (failed.length > 0 ? 'failed' : 'all')
  const rows = current === 'failed' ? failed : all
  const locations = data.locations
  const columns: TableColumn<any>[] = [
    {
      header: 'Backup',
      cell: (b) => (
        <>
          <span className="break-all font-mono text-xs">{b.metadata.name}</span>
          <Sub>{b.metadata?.labels?.[SCHEDULE_LABEL] ? `schedule ${b.metadata.labels[SCHEDULE_LABEL]}` : 'created directly'}</Sub>
        </>
      ),
    },
    { header: 'Outcome', width: '150px', cell: (b) => <Outcome badge={getBackupStatus(b)} /> },
    { header: 'Started', width: '100px', cell: (b) => <When iso={b.status?.startTimestamp} fallback="not started" /> },
    { header: 'Took', width: '75px', cell: (b) => durationText(b.status?.startTimestamp, b.status?.completionTimestamp) ?? '—' },
    { header: 'Items', width: '80px', cell: (b) => (b.status?.progress?.totalItems ? `${b.status.progress.itemsBackedUp ?? 0} / ${b.status.progress.totalItems}` : '—') },
    { header: 'Errors · warnings', width: '130px', cell: (b) => `${getBackupErrors(b)} · ${getBackupWarnings(b)}` },
    {
      header: 'Stored in',
      width: '150px',
      cell: (b) => {
        const name: string | undefined = b.spec?.storageLocation
        if (!name) return <span className="text-theme-text-tertiary">not assigned yet</span>
        const loc = locations.find((l) => l.namespace === b.metadata.namespace && l.name === name)
        return (
          <>
            <span className="break-all">{name}</span>
            {loc && loc.phase !== 'Available' && <Sub>{loc.phase ? loc.phase.toLowerCase() : 'not validated'}</Sub>}
          </>
        )
      },
    },
    {
      header: 'Kept until',
      width: '110px',
      cell: (b) => (b.status?.expiration ? (expired(b.status.expiration) ? <span className="text-theme-text-tertiary">past retention</span> : (
        <Tooltip content={veleroLocalTime(b.status.expiration)}>
          <span>{leftText(b.status.expiration)}</span>
        </Tooltip>
      )) : '—'),
    },
  ]
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <VeleroWorkspaceHeader title="Backups" data={data} installation={installation} onInstallation={onInstallation} />
      <ScreenBody>
        <div className="flex flex-wrap items-center gap-3">
          <Segments label="Backups" value={current} onChange={setView} options={[{ id: 'failed', label: 'Failed', count: failed.length }, { id: 'all', label: 'All', count: all.length }]} />
          <FilterChips chips={Object.entries(filter).map(([k, v]) => ({ label: k === SCHEDULE_LABEL ? `Schedule ${v}` : `${k}=${v}`, onClear: () => clearFilter(k) }))} />
        </div>
        <SectionTable
          columns={columns}
          rows={rows}
          rowKey={(b) => b.metadata.uid ?? `${b.metadata.namespace}/${b.metadata.name}`}
          rowResource={(b) => veleroResource('Backup', b.metadata.namespace, b.metadata.name)}
          onInspect={onInspect}
          inspected={inspected}
          minWidth={1000}
          empty={backups.isPending ? 'Loading backups…' : backups.error ? `Backups could not be read: ${(backups.error as Error).message}` : current === 'failed' ? 'No failed backups among those Velero keeps.' : 'No backups.'}
          footer="Failed counts runs that failed, partly failed or were rejected. Velero deletes each backup once its retention passes."
        />
      </ScreenBody>
    </div>
  )
}

/** The Restore kind's list: every restore, newest first, failed first when any failed. */
export function VeleroRestores({ data, installation, onInstallation, onInspect, inspected }: VeleroScreenProps) {
  const restores = useResources<any>('restores', undefined, VELERO_GROUP)
  const all = useMemo(
    () => inInstallation((restores.data ?? []).map((r: any) => ({ ...r, namespace: r.metadata?.namespace ?? '' })), installation).sort((a: any, b: any) => started(b).localeCompare(started(a))),
    [restores.data, installation],
  )
  const failed = all.filter((r: any) => FAILED.has(r.status?.phase ?? ''))
  const [view, setView] = useState<'failed' | 'all' | null>(null)
  const current = view ?? (failed.length > 0 ? 'failed' : 'all')
  const rows = current === 'failed' ? failed : all
  const columns: TableColumn<any>[] = [
    { header: 'Restore', cell: (r) => <span className="break-all font-mono text-xs">{r.metadata.name}</span> },
    { header: 'Outcome', width: '170px', cell: (r) => <Outcome badge={getRestoreStatus(r)} /> },
    {
      header: 'From backup',
      cell: (r) => {
        const b = getRestoreBackupName(r)
        return b ? (
          <button
            type="button"
            onClick={(e) => {
              e.stopPropagation()
              onInspect(veleroResource('Backup', r.metadata.namespace, b))
            }}
            className="break-all text-left font-mono text-xs text-accent hover:underline"
          >
            {b}
          </button>
        ) : r.spec?.scheduleName ? `newest of ${r.spec.scheduleName}` : '—'
      },
    },
    {
      header: 'Into',
      width: '180px',
      cell: (r) => {
        const mapping = Object.entries(getRestoreNamespaceMapping(r))
        return mapping.length ? mapping.map(([from, to]) => `${from} → ${to}`).join(', ') : <span className="text-theme-text-tertiary">original namespaces</span>
      },
    },
    { header: 'Started', width: '110px', cell: (r) => <When iso={r.status?.startTimestamp} fallback="not started" /> },
    { header: 'Took', width: '90px', cell: (r) => durationText(r.status?.startTimestamp, r.status?.completionTimestamp) ?? '—' },
    { header: 'Errors · warnings', width: '130px', cell: (r) => `${getRestoreErrors(r)} · ${getRestoreWarnings(r)}` },
  ]
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <VeleroWorkspaceHeader title="Restores" data={data} installation={installation} onInstallation={onInstallation} />
      <ScreenBody>
        <Segments label="Restores" value={current} onChange={setView} options={[{ id: 'failed', label: 'Failed', count: failed.length }, { id: 'all', label: 'All', count: all.length }]} />
        <SectionTable
          columns={columns}
          rows={rows}
          rowKey={(r) => r.metadata.uid ?? `${r.metadata.namespace}/${r.metadata.name}`}
          rowResource={(r) => veleroResource('Restore', r.metadata.namespace, r.metadata.name)}
          onInspect={onInspect}
          inspected={inspected}
          minWidth={1000}
          empty={restores.isPending ? 'Loading restores…' : restores.error ? `Restores could not be read: ${(restores.error as Error).message}` : current === 'failed' ? 'No failed restores.' : 'No restores. Velero keeps a restore until it is deleted, or its backup is.'}
        />
      </ScreenBody>
    </div>
  )
}
