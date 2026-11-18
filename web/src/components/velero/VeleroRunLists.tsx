import { useMemo, useState, type ReactNode } from 'react'
import { Plus, Search } from 'lucide-react'
import { Tooltip, toneTextClass } from '@skyhook-io/k8s-ui'
import { useVeleroRuns, type VeleroRestoreRow, type VeleroRunRow } from '../../api/velero'
import { useMediaQuery } from '../../hooks/useMediaQuery'
import { ScreenBody, SectionTable, Segments, Sub, type TableColumn } from '../workspace/layout'
import { VELERO_WIDE, VeleroName, VeleroWorkspaceHeader, WholeNames, inInstallation, restoredIntoText, veleroResource, type VeleroScreenProps } from './shared'
import { Expires, RunCounts, RunOutcome, RunReason, RunsReadNotice, When, durationText, expired, runViewOptions, runsEmpty, useRunView } from './runs'

// Rows rendered at once; the rest are a click away, and search narrows first.
const PAGE = 200

function SearchBox({ value, onChange, placeholder }: { value: string; onChange: (v: string) => void; placeholder: string }) {
  return (
    <label className="flex min-w-[14rem] items-center gap-2 rounded-lg border border-theme-border bg-theme-surface px-2.5 py-1.5 text-sm">
      <Search className="h-3.5 w-3.5 text-theme-text-tertiary" aria-hidden />
      <input value={value} onChange={(e) => onChange(e.target.value)} placeholder={placeholder} aria-label={placeholder} className="min-w-0 flex-1 bg-transparent text-theme-text-primary outline-none placeholder:text-theme-text-tertiary" />
    </label>
  )
}

function RunTable<R extends VeleroRunRow>({ columns, rows, kind, onInspect, inspected, empty, footer, minWidth }: {
  columns: TableColumn<R>[]
  rows: R[]
  kind: 'Backup' | 'Restore'
  onInspect: VeleroScreenProps['onInspect']
  inspected: VeleroScreenProps['inspected']
  empty: string
  footer?: ReactNode
  minWidth: number
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
      minWidth={minWidth}
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

function plural(n: number, word: string): string {
  return `${n} ${word}${n === 1 ? '' : 's'}`
}

/** The outcome and why; on a narrow screen also its errors and warnings, said only when there are some. */
export function OutcomeCell({ kind, run, counts }: { kind: 'Backup' | 'Restore'; run: VeleroRunRow; counts?: boolean }) {
  return (
    <>
      <RunOutcome kind={kind} phase={run.phase} />
      <RunReason run={run} />
      {counts && (run.errors > 0 || run.warnings > 0) && (
        <Sub>
          {run.errors > 0 && <span className={toneTextClass('unhealthy')}>{plural(run.errors, 'error')}</span>}
          {run.errors > 0 && run.warnings > 0 && ' · '}
          {run.warnings > 0 && <span className={toneTextClass('degraded')}>{plural(run.warnings, 'warning')}</span>}
        </Sub>
      )}
    </>
  )
}

/** Where a restore put things, as its page says it; nothing for one Velero rejected. */
export function RestoreIntoCell({ run }: { run: VeleroRestoreRow }) {
  if (run.phase === 'FailedValidation') return <span className="text-theme-text-secondary">Nothing — rejected</span>
  const text = restoredIntoText(run)
  return (
    <Tooltip content={text}>
      <span className="line-clamp-2">
        <WholeNames text={text} />
      </span>
    </Tooltip>
  )
}

/**
 * Started, and on a narrow screen how long it took, the items it processed,
 * where it is stored and when Velero deletes it.
 */
export function StartedCell({ run, folded, items, storedIn, expires }: { run: VeleroRunRow; folded: boolean; items?: boolean; storedIn?: string; expires?: boolean }) {
  const took = durationText(run.started, run.completed)
  const extra = folded ? [took && `took ${took}`, items && run.itemsTotal ? `${run.itemsDone ?? 0} / ${run.itemsTotal} items` : ''].filter(Boolean) : []
  return (
    <>
      <When iso={run.started} fallback="not started" />
      {extra.length > 0 && <Sub>{extra.join(' · ')}</Sub>}
      {folded && storedIn && (
        <Sub>
          <WholeNames text={storedIn} />
        </Sub>
      )}
      {folded && expires && run.expiration && (
        <Sub>
          {!expired(run.expiration) && 'expires '}
          <Expires iso={run.expiration} />
        </Sub>
      )}
    </>
  )
}

/** Where a backup is stored, and whether that location is not available. */
function storedInText(r: VeleroRunRow, locations: VeleroScreenProps['data']['locations']): { name?: string; state?: string } {
  if (!r.storageLocation) return { state: 'not assigned yet' }
  const loc = locations.find((l) => l.namespace === r.namespace && l.name === r.storageLocation)
  return { name: r.storageLocation, state: loc && loc.phase !== 'Available' ? (loc.phase ? loc.phase.toLowerCase() : 'not validated') : undefined }
}

/**
 * The Backup kind's list: every run across the installations the caller can
 * read, newest first, with the failed ones a segment away.
 */
export function VeleroBackups({ data, installation, onInstallation, onInspect, inspected, onCreate }: VeleroScreenProps) {
  const query = useVeleroRuns('backups')
  const wide = useMediaQuery(VELERO_WIDE)
  const [search, setSearch] = useState('')
  const rows = useMemo(
    () => inInstallation(query.data?.runs ?? [], installation).filter((r) => !search || r.name.toLowerCase().includes(search.toLowerCase())),
    [query.data, installation, search],
  )
  const { failed, view, setView, shown } = useRunView(rows)
  const locations = data.locations
  const columns: TableColumn<VeleroRunRow>[] = [
    {
      header: 'Backup',
      cell: (r) => (
        <>
          <VeleroName name={r.name} middle />
          <Sub>{r.schedule ? <WholeNames text={`schedule ${r.schedule}`} /> : 'On demand'}</Sub>
        </>
      ),
    },
    { header: 'Outcome', width: wide ? '170px' : '180px', cell: (r) => <OutcomeCell kind="Backup" run={r} counts={!wide} /> },
    {
      header: 'Started',
      width: wide ? '100px' : '190px',
      cell: (r) => {
        const stored = storedInText(r, locations)
        return <StartedCell run={r} folded={!wide} items expires storedIn={stored.name ? `in ${stored.name}${stored.state ? ` · ${stored.state}` : ''}` : stored.state} />
      },
    },
    ...(wide
      ? [
          { header: 'Took', width: '75px', cell: (r: VeleroRunRow) => durationText(r.started, r.completed) ?? '—' },
          { header: 'Items', width: '80px', cell: (r: VeleroRunRow) => (r.itemsTotal ? `${r.itemsDone ?? 0} / ${r.itemsTotal}` : '—') },
        ]
      : []),
    ...(wide
      ? [
          { header: 'Errors · warnings', width: '130px', cell: (r: VeleroRunRow) => <RunCounts errors={r.errors} warnings={r.warnings} /> },
          {
            header: 'Stored in',
            width: '140px',
            cell: (r: VeleroRunRow) => {
              const stored = storedInText(r, locations)
              if (!stored.name) return <span className="text-theme-text-tertiary">{stored.state}</span>
              return (
                <>
                  <VeleroName name={stored.name} />
                  {stored.state && <Sub>{stored.state}</Sub>}
                </>
              )
            },
          },
          { header: 'Expires', width: '90px', cell: (r: VeleroRunRow) => <Expires iso={r.expiration} /> },
        ]
      : []),
  ]
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <VeleroWorkspaceHeader title="Backups" data={data} installation={installation} onInstallation={onInstallation} />
      <ScreenBody>
        <div className="flex flex-wrap items-center gap-3">
          <Segments label="Backups" value={view} onChange={setView} options={runViewOptions(failed.length, rows.length, query)} />
          <SearchBox value={search} onChange={setSearch} placeholder="Search backups" />
          <CreateButton onCreate={onCreate} kind="Backup" />
        </div>
        <RunsReadNotice query={query} noun="backups" />
        <RunTable
          columns={columns}
          rows={shown}
          kind="Backup"
          onInspect={onInspect}
          inspected={inspected}
          minWidth={wide ? 1000 : 520}
          empty={runsEmpty(query, 'backups', view === 'failed' ? 'No failed backups' : 'No backups match')}
          footer="Failed counts runs that failed, partially failed or were rejected. Velero deletes each backup at the first garbage collection after its retention."
        />
      </ScreenBody>
    </div>
  )
}

/** The Restore kind's list: every restore, newest first, with the failed ones a segment away. */
export function VeleroRestores({ data, installation, onInstallation, onInspect, inspected, onCreate }: VeleroScreenProps) {
  const query = useVeleroRuns('restores')
  const wide = useMediaQuery(VELERO_WIDE)
  const [search, setSearch] = useState('')
  const rows = useMemo(
    () => inInstallation(query.data?.runs ?? [], installation).filter((r) => !search || r.name.toLowerCase().includes(search.toLowerCase()) || (r.backupName ?? '').toLowerCase().includes(search.toLowerCase())),
    [query.data, installation, search],
  )
  const { failed, view, setView, shown } = useRunView(rows)
  const fromBackup = (r: VeleroRunRow) =>
    r.backupName ? (
      <button
        type="button"
        onClick={(e) => {
          e.stopPropagation()
          onInspect(veleroResource('Backup', r.namespace, r.backupName!))
        }}
        className="block max-w-full text-left text-accent-text hover:underline"
      >
        <VeleroName name={r.backupName} middle />
      </button>
    ) : r.scheduleName ? (
      <WholeNames text={`newest of ${r.scheduleName}`} />
    ) : (
      '—'
    )
  // Narrow: the backup goes under the restore's name, and errors and warnings under its outcome.
  const columns: TableColumn<VeleroRestoreRow>[] = [
    {
      header: 'Restore',
      cell: (r) => (
        <>
          <VeleroName name={r.name} middle />
          {!wide && (
            <Sub>
              <span className="flex min-w-0 items-baseline gap-1">
                <span className="shrink-0">from</span>
                <span className="min-w-0">{fromBackup(r)}</span>
              </span>
            </Sub>
          )}
        </>
      ),
    },
    { header: 'Outcome', width: '170px', cell: (r) => <OutcomeCell kind="Restore" run={r} counts={!wide} /> },
    ...(wide ? [{ header: 'From backup', cell: fromBackup }] : []),
    {
      header: 'Into',
      width: wide ? '170px' : '150px',
      cell: (r) => <RestoreIntoCell run={r} />,
    },
    { header: 'Started', width: wide ? '100px' : '120px', cell: (r) => <StartedCell run={r} folded={!wide} /> },
    ...(wide
      ? [
          { header: 'Took', width: '75px', cell: (r: VeleroRunRow) => durationText(r.started, r.completed) ?? '—' },
          { header: 'Errors · warnings', width: '130px', cell: (r: VeleroRunRow) => <RunCounts errors={r.errors} warnings={r.warnings} /> },
        ]
      : []),
  ]
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <VeleroWorkspaceHeader title="Restores" data={data} installation={installation} onInstallation={onInstallation} />
      <ScreenBody>
        <div className="flex flex-wrap items-center gap-3">
          <Segments label="Restores" value={view} onChange={setView} options={runViewOptions(failed.length, rows.length, query)} />
          <SearchBox value={search} onChange={setSearch} placeholder="Search restores" />
          <CreateButton onCreate={onCreate} kind="Restore" />
        </div>
        <RunsReadNotice query={query} noun="restores" />
        <RunTable
          columns={columns}
          rows={shown}
          kind="Restore"
          onInspect={onInspect}
          inspected={inspected}
          minWidth={wide ? 1000 : 560}
          empty={runsEmpty(query, 'restores', view === 'failed' ? 'No failed restores' : 'No restores match')}
        />
      </ScreenBody>
    </div>
  )
}
