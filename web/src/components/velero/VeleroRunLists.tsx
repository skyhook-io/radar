import { useMemo, useState, type ReactNode } from 'react'
import { useSearchParams } from 'react-router-dom'
import { Plus, Search } from 'lucide-react'
import { useVeleroRuns, type VeleroRunRow } from '../../api/velero'
import { FilterChips, ScreenBody, SectionTable, Segments, Sub, type TableColumn } from '../workspace/layout'
import { VeleroWorkspaceHeader, inInstallation, veleroResource, type VeleroScreenProps } from './shared'
import { FAILED, KeptUntil, RunOutcome, RunsReadNotice, When, boundedCount, durationText, runsEmpty } from './runs'

const SCHEDULE_LABEL = 'velero.io/schedule-name'
// Rows rendered at once; the rest are a click away, and search narrows first.
const PAGE = 200

/** `labels=k=v,k2=v2` from a link, as exact matches. */
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

function SearchBox({ value, onChange, placeholder }: { value: string; onChange: (v: string) => void; placeholder: string }) {
  return (
    <label className="flex min-w-[14rem] items-center gap-2 rounded-lg border border-theme-border bg-theme-surface px-2.5 py-1.5 text-sm">
      <Search className="h-3.5 w-3.5 text-theme-text-tertiary" aria-hidden />
      <input value={value} onChange={(e) => onChange(e.target.value)} placeholder={placeholder} aria-label={placeholder} className="min-w-0 flex-1 bg-transparent text-theme-text-primary outline-none placeholder:text-theme-text-tertiary" />
    </label>
  )
}

function useRunView(rows: VeleroRunRow[], initial: 'failed' | 'all' | null) {
  const failed = rows.filter((r) => FAILED.has(r.phase ?? ''))
  const [view, setView] = useState<'failed' | 'all' | null>(initial)
  const current = view ?? (failed.length > 0 ? 'failed' : 'all')
  return { failed, current, setView, shown: current === 'failed' ? failed : rows }
}

function RunTable({ columns, rows, kind, onInspect, inspected, empty, footer }: {
  columns: TableColumn<VeleroRunRow>[]
  rows: VeleroRunRow[]
  kind: 'Backup' | 'Restore'
  onInspect: VeleroScreenProps['onInspect']
  inspected: VeleroScreenProps['inspected']
  empty: string
  footer?: ReactNode
}) {
  const [limit, setLimit] = useState(PAGE)
  return (
    <SectionTable
      columns={columns}
      rows={rows.slice(0, limit)}
      rowKey={(r) => r.uid || `${r.namespace}/${r.name}`}
      rowResource={(r) => veleroResource(kind, r.namespace, r.name)}
      onInspect={onInspect}
      inspected={inspected}
      minWidth={1000}
      empty={empty}
      footer={
        <>
          {rows.length > limit && (
            <button type="button" onClick={() => setLimit((l) => l + PAGE)} className="mr-3 text-accent hover:underline">
              Show {Math.min(PAGE, rows.length - limit)} more of {rows.length - limit}
            </button>
          )}
          {footer}
        </>
      }
    />
  )
}

function CreateButton({ onCreate, kind }: { onCreate?: () => void; kind: string }) {
  if (!onCreate) return null
  return (
    <button type="button" onClick={onCreate} className="btn-secondary ml-auto inline-flex items-center gap-1.5 px-2.5 py-1.5 text-xs font-medium">
      <Plus className="h-3.5 w-3.5" aria-hidden />
      Create {kind}
    </button>
  )
}

/**
 * The Backup kind's list: every run across the installations the caller can
 * read, newest first, opening on the failed ones when any failed. A link
 * narrows it to one schedule's runs (labels=velero.io/schedule-name=...).
 */
export function VeleroBackups({ data, installation, onInstallation, onInspect, inspected, onCreate }: VeleroScreenProps) {
  const query = useVeleroRuns('backups')
  const [filter, clearFilter] = useLabelFilter()
  const [search, setSearch] = useState('')
  const linked = Object.keys(filter).length > 0
  const rows = useMemo(
    () => inInstallation(query.data?.runs ?? [], installation)
      .filter((r) => Object.entries(filter).every(([k, v]) => (k === SCHEDULE_LABEL ? r.schedule === v : false)))
      .filter((r) => !search || r.name.toLowerCase().includes(search.toLowerCase())),
    [query.data, installation, filter, search],
  )
  // A link to a schedule's runs promises all of them.
  const { failed, current, setView, shown } = useRunView(rows, linked ? 'all' : null)
  const locations = data.locations
  const columns: TableColumn<VeleroRunRow>[] = [
    {
      header: 'Backup',
      cell: (r) => (
        <>
          <span className="break-all font-mono text-xs">{r.name}</span>
          <Sub>{r.schedule ? `schedule ${r.schedule}` : 'created directly'}</Sub>
        </>
      ),
    },
    { header: 'Outcome', width: '150px', cell: (r) => <RunOutcome kind="Backup" phase={r.phase} /> },
    { header: 'Started', width: '100px', cell: (r) => <When iso={r.started} fallback="not started" /> },
    { header: 'Took', width: '75px', cell: (r) => durationText(r.started, r.completed) ?? '—' },
    { header: 'Items', width: '80px', cell: (r) => (r.itemsTotal ? `${r.itemsDone ?? 0} / ${r.itemsTotal}` : '—') },
    { header: 'Errors · warnings', width: '130px', cell: (r) => `${r.errors} · ${r.warnings}` },
    {
      header: 'Stored in',
      width: '150px',
      cell: (r) => {
        if (!r.storageLocation) return <span className="text-theme-text-tertiary">not assigned yet</span>
        const loc = locations.find((l) => l.namespace === r.namespace && l.name === r.storageLocation)
        return (
          <>
            <span className="break-all">{r.storageLocation}</span>
            {loc && loc.phase !== 'Available' && <Sub>{loc.phase ? loc.phase.toLowerCase() : 'not validated'}</Sub>}
          </>
        )
      },
    },
    { header: 'Kept until', width: '110px', cell: (r) => <KeptUntil iso={r.expiration} /> },
  ]
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <VeleroWorkspaceHeader title="Backups" data={data} installation={installation} onInstallation={onInstallation} />
      <ScreenBody>
        <div className="flex flex-wrap items-center gap-3">
          <Segments label="Backups" value={current} onChange={setView} options={[{ id: 'failed', label: 'Failed', count: boundedCount(failed.length, query) }, { id: 'all', label: 'All', count: boundedCount(rows.length, query) }]} />
          <SearchBox value={search} onChange={setSearch} placeholder="Search backups by name" />
          <FilterChips chips={Object.entries(filter).map(([k, v]) => ({ label: k === SCHEDULE_LABEL ? `Schedule ${v}` : `${k}=${v}`, onClear: () => clearFilter(k) }))} />
          <CreateButton onCreate={onCreate} kind="Backup" />
        </div>
        <RunsReadNotice query={query} noun="backups" />
        <RunTable
          columns={columns}
          rows={shown}
          kind="Backup"
          onInspect={onInspect}
          inspected={inspected}
          empty={runsEmpty(query, 'backups', current === 'failed' ? 'No failed backups' : 'No backups match')}
          footer="Failed counts runs that failed, partly failed or were rejected. Velero deletes each backup at the first garbage collection after its retention."
        />
      </ScreenBody>
    </div>
  )
}

/** The Restore kind's list: every restore, newest first, opening on the failed ones when any failed. */
export function VeleroRestores({ data, installation, onInstallation, onInspect, inspected, onCreate }: VeleroScreenProps) {
  const query = useVeleroRuns('restores')
  const [search, setSearch] = useState('')
  const rows = useMemo(
    () => inInstallation(query.data?.runs ?? [], installation).filter((r) => !search || r.name.toLowerCase().includes(search.toLowerCase()) || (r.backupName ?? '').toLowerCase().includes(search.toLowerCase())),
    [query.data, installation, search],
  )
  const { failed, current, setView, shown } = useRunView(rows, null)
  const columns: TableColumn<VeleroRunRow>[] = [
    { header: 'Restore', cell: (r) => <span className="break-all font-mono text-xs">{r.name}</span> },
    { header: 'Outcome', width: '150px', cell: (r) => <RunOutcome kind="Restore" phase={r.phase} /> },
    {
      header: 'From backup',
      cell: (r) =>
        r.backupName ? (
          <button
            type="button"
            onClick={(e) => {
              e.stopPropagation()
              onInspect(veleroResource('Backup', r.namespace, r.backupName!))
            }}
            className="break-all text-left font-mono text-xs text-accent hover:underline"
          >
            {r.backupName}
          </button>
        ) : r.scheduleName ? (
          `newest of ${r.scheduleName}`
        ) : (
          '—'
        ),
    },
    {
      header: 'Into',
      width: '180px',
      cell: (r) => {
        const mapping = Object.entries(r.namespaceMapping ?? {})
        return mapping.length ? mapping.map(([from, to]) => `${from} → ${to}`).join(', ') : <span className="text-theme-text-tertiary">original namespaces</span>
      },
    },
    { header: 'Started', width: '100px', cell: (r) => <When iso={r.started} fallback="not started" /> },
    { header: 'Took', width: '75px', cell: (r) => durationText(r.started, r.completed) ?? '—' },
    { header: 'Errors · warnings', width: '130px', cell: (r) => `${r.errors} · ${r.warnings}` },
  ]
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <VeleroWorkspaceHeader title="Restores" data={data} installation={installation} onInstallation={onInstallation} />
      <ScreenBody>
        <div className="flex flex-wrap items-center gap-3">
          <Segments label="Restores" value={current} onChange={setView} options={[{ id: 'failed', label: 'Failed', count: boundedCount(failed.length, query) }, { id: 'all', label: 'All', count: boundedCount(rows.length, query) }]} />
          <SearchBox value={search} onChange={setSearch} placeholder="Search restores or their backups" />
          <CreateButton onCreate={onCreate} kind="Restore" />
        </div>
        <RunsReadNotice query={query} noun="restores" />
        <RunTable
          columns={columns}
          rows={shown}
          kind="Restore"
          onInspect={onInspect}
          inspected={inspected}
          empty={runsEmpty(query, 'restores', current === 'failed' ? 'No failed restores' : 'No restores match')}
        />
      </ScreenBody>
    </div>
  )
}
