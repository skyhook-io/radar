import { Badge, EmptyState, FetchResult } from '@skyhook-io/k8s-ui'
import { formatCPUNanocores, formatMemoryBytes } from '@skyhook-io/k8s-ui/utils/format'
import type { JobSetResources, JobSetUsage, WorkloadRun } from '../../api/client'

interface Props {
  runs: WorkloadRun[]
  total: number
  filteredTotal: number
  truncated: boolean
  loading: boolean
  error: unknown
  roles: string[]
  role: string
  onRoleChange: (value: string) => void
  search: string
  onSearchChange: (value: string) => void
  state: 'all' | 'active' | 'failed'
  onStateChange: (value: 'all' | 'active' | 'failed') => void
  selected: string
  onSelect?: (key: string) => void
  resources?: JobSetResources
  resourcesLoading: boolean
  resourceError: unknown
  durationFor: (run: WorkloadRun) => string
}

function UsageValue({ usage, resource }: { usage?: JobSetUsage; resource: 'cpu' | 'memory' }) {
  const value = usage?.[resource]
  if (value === undefined || value === null) return <span className="text-theme-text-tertiary">—</span>
  const format = resource === 'cpu' ? formatCPUNanocores : formatMemoryBytes
  const request = resource === 'cpu' ? usage!.cpuRequest : usage!.memoryRequest
  return <><span className="tabular-nums text-theme-text-primary">{format(value)}</span><span className="block text-[10px] text-theme-text-tertiary">{request > 0 ? `${format(request)} requested` : 'No request reported'}</span></>
}

function Coverage({ usage }: { usage?: JobSetUsage }) {
  if (!usage) return <span className="text-theme-text-tertiary">Unavailable</span>
  return <span className="text-theme-text-secondary">{usage.runningPods === 0 ? 'No running Pods' : `${usage.reportingPods}/${usage.runningPods} running Pods reporting`}{usage.stalePods > 0 && ` · ${usage.stalePods} stale`}</span>
}

export function JobSetMemberComparison(props: Props) {
  const { resources, runs, role, roles } = props
  const ordered = [...runs].sort((a, b) => (a.jobset?.replicatedJob ?? '').localeCompare(b.jobset?.replicatedJob ?? ''))
  const inputClass = 'rounded-md border border-theme-border bg-theme-elevated px-2 py-1.5 text-xs text-theme-text-primary'
  return <section className="rounded-lg border border-theme-border bg-theme-surface" aria-label="Member Jobs">
    <div className="space-y-3 border-b border-theme-border p-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <h3 className="text-sm font-semibold text-theme-text-primary">Member Jobs <span className="font-normal text-theme-text-tertiary">{props.loading ? '· Loading Jobs…' : runs.length === props.total ? `· ${props.total} Jobs` : `· ${runs.length} shown · ${props.filteredTotal} matching · ${props.total} retained`}</span></h3>
        <span className="text-xs text-theme-text-tertiary">Controller-owned Jobs currently in Kubernetes</span>
      </div>
      <div className="flex flex-wrap items-center gap-2">
        <select aria-label="Filter member role" value={role} onChange={e => props.onRoleChange(e.target.value)} className={inputClass}>
          <option value="">All roles</option>{[...new Set([...roles, ...runs.map(run => run.jobset?.replicatedJob).filter((name): name is string => Boolean(name)), ...(role ? [role] : [])])].map(name => <option key={name} value={name}>{name}</option>)}
        </select>
        <select aria-label="Filter member state" value={props.state} onChange={e => props.onStateChange(e.target.value as Props['state'])} className={inputClass}><option value="all">All states</option><option value="active">Active</option><option value="failed">Failed</option></select>
        <input aria-label="Search all member Jobs" value={props.search} onChange={e => props.onSearchChange(e.target.value)} placeholder="Search all member Jobs" className={`${inputClass} min-w-48 flex-1`} />
        {(role || props.state !== 'all' || props.search) && <button type="button" className="text-xs text-accent-text hover:underline" onClick={() => { props.onRoleChange(''); props.onStateChange('all'); props.onSearchChange('') }}>Clear filters</button>}
      </div>
      {resources && <div className="flex flex-wrap items-start gap-x-5 gap-y-2 text-xs">
        <span className="font-medium text-theme-text-secondary">Whole JobSet<br/><span className="font-normal text-theme-text-tertiary">Current resource usage</span></span>
        <span>CPU <UsageValue usage={resources.total} resource="cpu" /></span>
        <span>Memory <UsageValue usage={resources.total} resource="memory" /></span>
        <span><Coverage usage={resources.total}/>{resources.total.observedAt && <span className="block text-[10px] text-theme-text-tertiary">Oldest sample: {new Date(resources.total.observedAt).toLocaleTimeString()}</span>}</span>
        {resources.total.extendedRequests && Object.entries(resources.total.extendedRequests).map(([name, count]) => <span key={name} className="text-theme-text-secondary">{name}: {count} requested<span className="block text-[10px] text-theme-text-tertiary">Nonterminal Pods · usage not collected</span></span>)}
      </div>}
      {Boolean(resources?.unavailable || props.resourceError) && <p className="text-xs text-theme-text-secondary">{resources?.unavailable || (props.resourceError instanceof Error ? props.resourceError.message : 'Resource usage unavailable.')}</p>}
      {!resources && !props.resourceError && props.resourcesLoading && <p className="text-xs text-theme-text-tertiary">Loading resource usage…</p>}
    </div>
    {props.loading ? <FetchResult loading className="min-h-24" /> : props.error ? <EmptyState variant="card" tone="neutral" headline="Member Jobs unavailable" body={props.error instanceof Error ? props.error.message : 'Unable to read child Jobs.'} /> : runs.length === 0 ? <EmptyState variant="card" tone="neutral" headline={props.total ? 'No Jobs match these filters' : 'No child Jobs currently retained'} body={props.total ? 'Change the role, state, or name filter.' : 'Jobs may not exist yet, may be managed externally, or may already have been cleaned up.'} /> : <div className="max-h-72 overflow-auto">
      <table className="w-full text-left text-xs">
        <thead className="sticky top-0 bg-theme-surface text-theme-text-tertiary"><tr>{['Role', 'Job', 'State', 'Duration', 'CPU usage', 'Memory usage', 'Metric coverage'].map(label => <th key={label} className="whitespace-nowrap border-b border-theme-border px-3 py-2 font-medium">{label}</th>)}</tr></thead>
        <tbody>{ordered.map(run => {
          const key = `${run.kind}/${run.namespace}/${run.name}`
          const usage = resources?.members[run.name]
          return <tr key={key} className={`border-b border-theme-border-subtle last:border-0 ${props.selected === key ? 'selection' : 'hover:bg-theme-hover'}`}>
            <td className="px-3 py-2 text-theme-text-secondary">{run.jobset?.replicatedJob ?? 'Not reported'}{run.jobset?.jobIndex !== undefined && ` #${run.jobset.jobIndex}`}</td>
            <th scope="row" className="max-w-xs px-3 py-2 font-medium"><button type="button" aria-pressed={props.selected === key} className="block max-w-full truncate text-accent-text hover:underline" onClick={() => props.onSelect?.(key)}>{run.name}</button></th>
            <td className="px-3 py-2"><Badge severity={run.phase === 'Failed' || run.phase === 'Error' ? 'error' : run.phase === 'Succeeded' ? 'success' : run.active ? 'info' : 'neutral'}>{run.phase}{run.deleting ? ' · deleting' : ''}</Badge></td>
            <td className="whitespace-nowrap px-3 py-2 text-theme-text-secondary">{props.durationFor(run) || '—'}</td>
            <td className="whitespace-nowrap px-3 py-2"><UsageValue usage={usage} resource="cpu" /></td>
            <td className="whitespace-nowrap px-3 py-2"><UsageValue usage={usage} resource="memory" /></td>
            <td className="px-3 py-2"><Coverage usage={usage}/>{usage?.extendedRequests && Object.entries(usage.extendedRequests).map(([name, count]) => <span key={name} className="block text-[10px] text-theme-text-tertiary">{name}: {count} requested · usage not collected</span>)}</td>
          </tr>
        })}</tbody>
      </table>
    </div>}
    <p className="border-t border-theme-border px-3 py-2 text-[10px] text-theme-text-tertiary">Usage and requests cover reporting running Pods only; missing and stale samples are excluded. Whole-JobSet totals include members outside these filters.{props.truncated && ' Only the first 200 matching Jobs are shown. Refine the search to find another member.'}</p>
  </section>
}
