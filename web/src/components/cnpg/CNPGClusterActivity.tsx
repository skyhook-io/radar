import { useState } from 'react'
import { PaneLoader, TimelineList, formatAge, type NavigateToResource } from '@skyhook-io/k8s-ui'
import { useCNPGClusterActivity } from '../../api/cnpg'
import { useCNPGClusterActivityWindow } from '../../api/cnpg-history'
import { CNPGIntervalBanner, useCNPGIntervalParams } from './CNPGTrends'
import { Notice, Segments } from '../workspace/layout'

const RANGES = [
  { id: '6', label: '6 h' },
  { id: '24', label: '24 h' },
  { id: '168', label: '7 d' },
] as const

/**
 * Kubernetes events and changes for the Cluster, its instance Pods and the
 * CNPG objects attributed to it — including Backups and declarations deleted
 * since. Attribution of child objects relies on a label Radar records at
 * ingestion, so history older than that is marked incomplete.
 */
export function CNPGClusterActivity({ namespace, name, onNavigate }: { namespace: string; name: string; onNavigate?: NavigateToResource }) {
  const [hours, setHours] = useState<string>('24')
  const interval = useCNPGIntervalParams()
  const ranged = useCNPGClusterActivity(namespace, name, Number(hours))
  const windowed = useCNPGClusterActivityWindow(namespace, name, interval?.since ?? '', interval?.until ?? '', !!interval)
  const q = interval ? windowed : ranged

  return (
    <div className="flex h-full min-h-0 flex-col gap-3 p-4">
      {interval ? (
        <CNPGIntervalBanner since={interval.since} until={interval.until} note="Selected on a History chart: events and changes inside this interval only." onClear={interval.clear} />
      ) : (
        <div className="flex flex-wrap items-center gap-3">
          <Segments label="Range" value={hours} onChange={setHours} options={RANGES.map((r) => ({ id: r.id, label: r.label }))} />
          <span className="text-xs text-theme-text-tertiary">
            Kubernetes events and changes for the Cluster, its instances, Backups, Poolers and declarations.
          </span>
        </div>
      )}
      <div className="text-xs text-theme-text-tertiary">
        {q.data?.attributionSince
          ? `The earliest recorded event linking a Backup, Pooler or declaration to this cluster is ${formatAge(q.data.attributionSince)} old. Deleted child objects from before Radar recorded that link are not shown.`
          : 'Radar has not recorded any Backup, Pooler or declaration events linked to this cluster yet, so deleted child objects may be missing.'}
        {' '}Events for kinds you cannot list are omitted.
      </div>
      {q.data?.truncated && <Notice>Showing the most recent events only; narrow the range to see all of them.</Notice>}
      {q.error && !q.data ? (
        <Notice>Activity could not be loaded: {q.error instanceof Error ? q.error.message : 'unknown error'}</Notice>
      ) : !q.data ? (
        <PaneLoader label="Loading activity…" className="h-40" />
      ) : (
        <div className="min-h-0 flex-1">
          <TimelineList events={q.data.events} isLoading={q.isFetching && !q.data} onResourceClick={onNavigate} compact hideRangeSelector />
        </div>
      )}
    </div>
  )
}
