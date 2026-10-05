import { SectionHeading, formatAge, formatGrant, toneTextClass } from '@skyhook-io/k8s-ui'
import { useCNPGParameters } from '../../api/cnpg-inspect'
import { cnpgParametersView, type CNPGParameterRow } from './inspectModel'
import { RefreshFailedNotice } from '../workspace/layout'

/**
 * The parameters the Cluster declares beside what each instance's PostgreSQL
 * reports: whether a change took effect, and whether it waits for a restart.
 * Observation only — changes go through the Cluster spec.
 */
export function CNPGParametersInEffect({ namespace, name, declared, scope }: { namespace: string; name: string; declared: Record<string, string>; scope?: { declaredInstances?: number; expectedInstances?: string[] } }) {
  const q = useCNPGParameters(namespace, name)
  const d = q.data
  const declarations = Object.entries(declared).sort(([a], [b]) => a.localeCompare(b)).map(([name, value]) => ({ name, value }))
  const view = cnpgParametersView(d ?? {
    cluster: { namespace, name, uid: '' }, sampledAt: '', permission: { exec: 'unknown' }, state: 'error', instances: [],
    declared: declarations,
  }, declarations, scope)
  const unavailable = !d && q.error instanceof Error
    ? `Instance values could not be read: ${q.error.message}`
    : !d ? (q.isLoading ? 'Reading each instance…' : 'Instance values could not be read')
      : d.state === 'denied' ? `Reading instance values needs ${formatGrant(d.permission.grant) ?? `create pods/exec in namespace ${namespace}`}.`
        : d.error ? `Instance values could not be read: ${d.error}` : null
  const content = (
    <>
      <RefreshFailedNotice queries={[q]} className="mb-2" />
      {unavailable && <p className="mb-2 text-sm text-theme-text-tertiary">{unavailable}</p>}
      {d && !unavailable && declarations.length > 0 && <p className={`mb-2 text-sm ${toneTextClass(view.summary.tone)}`}>{view.summary.text}</p>}
      {declarations.length === 0 && <p className="text-sm text-theme-text-tertiary">No parameters declared in spec.postgresql.parameters.</p>}
      {declarations.length > 0 && (
      <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-theme-border text-left text-[11px] uppercase tracking-wide text-theme-text-tertiary">
              <th className="py-1.5 pr-4 font-medium">Parameter</th>
              <th className="py-1.5 pr-4 font-medium">Declared</th>
              <th className="py-1.5 pr-4 font-medium">Reported by instance</th>
              <th className="py-1.5 font-medium">A change applies on</th>
            </tr>
          </thead>
          <tbody>
            {view.rows.map((r) => (
              <ParameterRow key={r.name} row={r} readPods={view.read.map((i) => i.pod)}
                unreadText={d?.skipped?.includes(r.name) ? 'Not read: not a parameter name'
                  : !d ? (q.isLoading ? 'Not read: loading' : 'Not read: request failed')
                    : d.state === 'denied' ? 'Not read: pods/exec denied'
                      : !d.declared.some((p) => p.name.toLowerCase() === r.name.toLowerCase()) ? 'Not sampled'
                        : view.read.length === 0 ? (d.instances.length === 0 ? 'Not read: no instance Pod' : 'Not read: no instance answered')
                          : 'Not reported'}
                reports={view.read.flatMap((i) => {
                const setting = i.settings!.find((s) => s.name.toLowerCase() === r.name.toLowerCase())
                return setting?.value != null ? [{ pod: i.pod, value: setting.value }] : []
              })} />
            ))}
          </tbody>
        </table>
      </div>
      )}
      {d && view.read.length > 0 && <p className="mt-2 text-[11.5px] text-theme-text-tertiary">
        Read {d.sampledAt ? `${formatAge(d.sampledAt)} ago` : 'just now'} from a new Radar session on each of {view.read.map((i) => i.pod).join(', ')}. A role, database or
        client can set its own value for a parameter a session can change. PostgreSQL normalizes units and boolean spellings (such as false to off), so different text alone does not mean a different value.
      </p>}
      {d?.skipped && d.skipped.length > 0 && <p className="mt-1 text-xs text-theme-text-tertiary">Not read (not a parameter name): {d.skipped.join(', ')}.</p>}
      {!!d?.omitted && <p className="mt-1 text-xs text-theme-text-tertiary">{d.omitted} declarations were not sampled.</p>}
      {view.unread.length > 0 && (
        <p className={`mt-1 text-xs ${toneTextClass('unknown')}`}>
          Not read on {view.unread.map((i) => `${i.pod} (${i.error ?? i.state})`).join(', ')}.
        </p>
      )}
    </>
  )
  return <><SectionHeading hint="spec.postgresql.parameters">PostgreSQL parameters</SectionHeading>{content}</>
}

export function ParameterRow({ row, reports, unreadText, readPods }: { row: CNPGParameterRow; reports: { pod: string; value: string }[]; unreadText: string; readPods: string[] }) {
  const namesNeeded = !!row.perInstance || row.pendingRestart.length > 0 || row.unreported.length > 0 || reports.length !== readPods.length || reports.some((p) => !readPods.includes(p.pod))
  const declaredBoolean = cnpgParameterBoolean(row.declared)
  const sameBoolean = declaredBoolean !== undefined && reports.length > 0 && reports.some((p) => p.value !== row.declared) && reports.every((p) => ['on', 'off'].includes(p.value.trim().toLowerCase()) && cnpgParameterBoolean(p.value) === declaredBoolean)

  return (
    <tr className="border-b border-theme-border/60 align-top last:border-0">
      <td className="py-1.5 pr-4 font-mono text-xs text-theme-text-primary">{row.name}</td>
      <td className="py-1.5 pr-4 font-mono text-xs text-theme-text-secondary">{row.declared === '' ? <span className="text-theme-text-tertiary">(empty)</span> : row.declared}</td>
      <td className="py-1.5 pr-4 text-xs">
        {reports.length > 0 ? (
          row.perInstance
            ? <span className={toneTextClass('degraded')}>{reports.map((p) => `${p.pod}: ${p.value === '' ? '(empty)' : p.value}`).join(' · ')}</span>
            : <><span className="font-mono text-theme-text-primary">{row.value === '' ? '(empty)' : row.value}</span>{namesNeeded && <div className="text-theme-text-tertiary">on {reports.map((p) => p.pod).join(', ')}</div>}</>
        ) : row.setByClient ? (
          <span className="text-theme-text-tertiary">Set by each connection, so not readable here</span>
        ) : (
          <span className="text-theme-text-tertiary">{unreadText}</span>
        )}
        {sameBoolean && <span className="ml-1.5 text-theme-text-tertiary">same as declared</span>}
        {row.source && row.source !== 'configuration file' && !row.setByClient && <span className="ml-1.5 text-theme-text-tertiary">source: {row.source}</span>}
        {row.pendingRestart.length > 0 && <div className={toneTextClass('degraded')}>Restart pending on {row.pendingRestart.join(', ')}</div>}
        {row.unreported.length > 0 && unreadText === 'Not reported' && (
          <div className="text-theme-text-tertiary">Not reported by {row.unreported.join(', ')}</div>
        )}
      </td>
      <td className="py-1.5 text-xs text-theme-text-secondary">{row.takesEffect}</td>
    </tr>
  )
}

export function cnpgParameterBoolean(value: string): boolean | undefined {
  const normalized = value.trim().toLowerCase()
  if (normalized === '1') return true
  if (normalized === '0') return false
  if (!normalized) return undefined
  const matches = ['on', 'off', 'true', 'false', 'yes', 'no'].filter((spelling) => spelling.startsWith(normalized))
  if (matches.length !== 1) return undefined
  return ['on', 'true', 'yes'].includes(matches[0])
}
