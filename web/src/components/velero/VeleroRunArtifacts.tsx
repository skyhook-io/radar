import { useState } from 'react'
import { Badge, RadarUpgradeAction, pluralize, getRadarUpgradeRequirement, radarUpgradeDetail, radarUpgradeHeadline } from '@skyhook-io/k8s-ui'
import { useVeleroRunContents, useVeleroRunLog, type VeleroLogLevel } from '../../api/velero'
import { Notice, SectionTable, Segments } from '../workspace/layout'

type RunProps = { kind: 'backups' | 'restores'; namespace: string; name: string }
const actions = ['failed', 'warned', 'created', 'updated', 'skipped'] as const
// warned is a "failed" entry of a restore that settled with no errors
// (classified by the server).
const actionLabels: Record<string, string> = { failed: 'Failed', warned: 'With warnings', created: 'Created', updated: 'Updated', skipped: 'Skipped' }
const actionSeverity = (action: string) => action === 'failed' ? 'error' : action === 'warned' ? 'warning' : 'neutral'
const levels: VeleroLogLevel[] = ['error', 'warning', 'info', 'debug', 'other']
const inputClass = 'rounded-lg border border-theme-border bg-theme-elevated px-3 py-1.5 text-sm text-theme-text-primary'

export function ArtifactError({ error, retry, hasData, label = 'Velero run contents and log' }: { error: Error; retry: () => void; hasData: boolean; label?: string }) {
  const upgrade = getRadarUpgradeRequirement(error)
  return <Notice>{hasData && <div>The last refresh failed; the previous result is shown below.</div>}{upgrade ? <>{radarUpgradeHeadline(label)}<div>{radarUpgradeDetail(upgrade)}</div><RadarUpgradeAction requirement={upgrade} /></> : <>{error.message} <button type="button" className="btn-secondary px-2 py-1" onClick={retry}>Retry</button></>}</Notice>
}

export function VeleroRunContents({ kind, namespace, name }: RunProps) {
  const [text, setText] = useState('')
  const [q, setQuery] = useState('')
  const [action, setAction] = useState('')
  const [selectedNamespace, setNamespace] = useState('')
  const query = useVeleroRunContents(kind, namespace, name, { q, action, namespace: selectedNamespace })
  const data = query.data
  return <div className="space-y-3 p-4">
    <form className="flex flex-wrap items-center gap-2" onSubmit={(e) => { e.preventDefault(); setQuery(text) }}>
      <input aria-label="Filter contents" placeholder="Kind, namespace or name" className={`${inputClass} w-64 max-w-full`} value={text} onChange={(e) => setText(e.target.value)} />
      <button type="submit" className="btn-secondary px-3 py-1.5 text-sm">Search</button>
      <select aria-label="Contents namespace" className={inputClass} value={selectedNamespace} onChange={(e) => setNamespace(e.target.value)}>
        <option value="">All namespaces</option>
        {(data?.namespaces ?? (selectedNamespace ? [selectedNamespace] : [])).map((ns) => <option key={ns} value={ns}>{ns}</option>)}
      </select>
    </form>
    {kind === 'restores' && <Segments label="Restore action" value={action} onChange={setAction} options={[
      { id: '', label: 'All actions', count: data?.total },
      ...actions.filter((id) => id !== 'warned' || (data?.actions[id] ?? 0) > 0 || action === id).map((id) => ({ id, label: actionLabels[id], count: data ? data.actions[id] ?? 0 : undefined })),
    ]} />}
    {query.error && <ArtifactError error={query.error} hasData={!!data} retry={() => void query.refetch()} />}
    {query.isPending && <p className="text-sm text-theme-text-secondary">Loading contents…</p>}
    {data && <>
      <p className="text-sm text-theme-text-secondary">{pluralize(data.total, 'item')} · {pluralize(data.namespaces.length, 'namespace')} · {pluralize(data.matched, 'matching item')}</p>
      {kind === 'restores' && ((data.actions.failed ?? 0) > 0 || (data.actions.warned ?? 0) > 0) && <p className="text-xs text-theme-text-secondary">
        Velero records an item as failed when any warning or error was reported for it. {(data.actions.warned ?? 0) > 0 ? 'This restore finished with no errors, so those items read With warnings. ' : ''}These counts are items; the restore’s warning count is Velero’s messages, and the Log counts log lines, so the three can differ.
      </p>}
      {data.truncated && <Notice>Showing the first 5,000 matching items. Counts and filters cover the whole file.</Notice>}
      {data.matched === 0 && <Notice>{data.total === 0 ? 'Velero recorded no items in this run.' : 'No contents match these filters.'}</Notice>}
      {data.groups.filter((g) => g.matched > 0).map((group) => <SectionTable
        key={group.kind}
        title={group.kind}
        subtitle={`${group.count} total · ${group.matched} matching`}
        minWidth={480}
        columns={[
          { header: 'Namespace', width: '180px', cell: (item) => item.namespace || 'Cluster-scoped' },
          { header: 'Name', cell: (item) => <span className="font-mono text-xs break-all">{item.name}</span> },
          ...(kind === 'restores' ? [{ header: 'Action', width: '140px', cell: (item: (typeof group.items)[number]) => item.action ? <Badge severity={actionSeverity(item.action)}>{actionLabels[item.action] ?? item.action}</Badge> : 'Not recorded' }] : []),
        ]}
        rows={group.items}
        rowKey={(item) => `${item.namespace}/${item.name}/${item.action ?? ''}`}
        empty="Matching items omitted by the row limit. Narrow the filters to see them."
      />)}
    </>}
  </div>
}

export function VeleroRunLog({ kind, namespace, name }: RunProps) {
  const [text, setText] = useState('')
  const [q, setQuery] = useState('')
  // null until the first answer: a run with errors or warnings opens on them,
  // and a clean run opens on every level rather than an empty view.
  const [chosen, setSelected] = useState<string | null>(null)
  const selected = chosen ?? 'problems'
  const [wrap, setWrap] = useState(true)
  const requested = selected === 'all' ? levels.join(',') : selected === 'problems' ? 'error,warning' : selected
  const query = useVeleroRunLog(kind, namespace, name, requested, q)
  const data = query.data
  if (chosen === null && data) setSelected(data.counts.error + data.counts.warning > 0 ? 'problems' : 'all')
  const count = (level: VeleroLogLevel) => data ? `${data.partial ? '≥' : ''}${data.counts[level]}` : undefined
  return <div className="space-y-3 p-4">
    <div className="flex flex-wrap items-center gap-2">
      <Segments label="Log levels" value={selected} onChange={setSelected} options={[
        { id: 'problems', label: 'Errors + warnings' },
        ...levels.map((id) => ({ id, label: id[0].toUpperCase() + id.slice(1), count: count(id) })),
      ]} />
      <button type="button" aria-pressed={selected === 'all'} className="btn-secondary px-3 py-1.5 text-sm" onClick={() => setSelected('all')}>Show all levels</button>
    </div>
    <form className="flex flex-wrap items-center gap-3" onSubmit={(e) => { e.preventDefault(); setQuery(text) }}>
      <input aria-label="Search log" placeholder="Search log text" className={inputClass} value={text} onChange={(e) => setText(e.target.value)} />
      <button type="submit" className="btn-secondary px-3 py-1.5 text-sm">Search</button>
      <label className="flex items-center gap-2 text-sm text-theme-text-secondary"><input type="checkbox" checked={wrap} onChange={(e) => setWrap(e.target.checked)} />Wrap lines</label>
    </form>
    {query.error && <ArtifactError error={query.error} hasData={!!data} retry={() => void query.refetch()} />}
    {query.isPending && <p className="text-sm text-theme-text-secondary">Loading log…</p>}
    {data?.partial && <Notice>Counts and search cover the first {(data.decodedBytes / (1 << 20)).toFixed(2)} MiB of decoded log ({data.limit}). Later lines were not read.</Notice>}
    {data?.truncated && <Notice>Showing the first 5,000 matching lines. Counts and search cover {data.partial ? 'the decoded prefix' : 'the whole log'}.</Notice>}
    {data && <SectionTable
      title="Run log"
      subtitle={`${data.matched} matching lines`}
      minWidth={480}
      columns={[
        { header: 'Line', width: '70px', cell: (line) => line.number },
        { header: 'Level', width: '100px', cell: (line) => <Badge severity={line.level === 'error' ? 'error' : line.level === 'warning' ? 'warning' : 'neutral'}>{line.level}</Badge> },
        { header: 'Text', cell: (line) => <div className={wrap ? 'font-mono text-xs whitespace-pre-wrap [overflow-wrap:anywhere]' : 'overflow-x-auto font-mono text-xs whitespace-pre'}>{line.text}</div> },
      ]}
      rows={data.lines}
      rowKey={(line) => String(line.number)}
      empty={data.partial ? 'No matching log lines in the decoded prefix.' : 'No matching log lines. Try another level or search.'}
    />}
  </div>
}
