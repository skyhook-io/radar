import { FoldSection, formatAge, formatGrant, toneTextClass } from '@skyhook-io/k8s-ui'
import { useCNPGParameters } from '../../api/cnpg-inspect'
import { cnpgParametersView, type CNPGParameterRow } from './inspectModel'

/**
 * The parameters the Cluster declares beside what each instance's PostgreSQL
 * reports: whether a change took effect, and whether it waits for a restart.
 * Observation only — changes go through the Cluster spec.
 */
export function CNPGParametersInEffect({ namespace, name }: { namespace: string; name: string }) {
  const q = useCNPGParameters(namespace, name)
  const d = q.data
  if (!d) {
    return (
      <FoldSection title="Parameters in effect" summary={q.isLoading ? 'Reading each instance…' : 'Could not be read'} attention={false}>
        <p className="text-sm text-theme-text-tertiary">{q.error instanceof Error ? q.error.message : 'Reading each instance…'}</p>
      </FoldSection>
    )
  }
  if (d.declared.length === 0) {
    return (
      <FoldSection title="Parameters in effect" summary="None declared in spec.postgresql.parameters" attention={false}>
        <p className="text-sm text-theme-text-tertiary">The Cluster declares no PostgreSQL parameters; CloudNativePG’s defaults apply.</p>
      </FoldSection>
    )
  }
  if (d.state === 'denied') {
    return (
      <FoldSection title="Parameters in effect" summary={`${d.declared.length} declared · values need pods/exec`} attention={false}>
        <p className="text-sm text-theme-text-secondary">
          Reading each instance’s values needs {formatGrant(d.permission.grant) ?? `create pods/exec in namespace ${namespace}`}. The declared values are on this tab below.
        </p>
      </FoldSection>
    )
  }
  const view = cnpgParametersView(d)
  return (
    <FoldSection
      title="Parameters in effect"
      hint="spec.postgresql.parameters, including the defaults CloudNativePG fills in, as each instance reports them"
      summary={<span className={view.summary.attention ? toneTextClass('degraded') : undefined}>{view.summary.text}</span>}
      attention={view.summary.attention}
    >
      <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-theme-border text-left text-[11px] uppercase tracking-wide text-theme-text-tertiary">
              <th className="py-1.5 pr-4 font-medium">Parameter</th>
              <th className="py-1.5 pr-4 font-medium">Declared</th>
              <th className="py-1.5 pr-4 font-medium">In effect</th>
              <th className="py-1.5 font-medium">A change applies on</th>
            </tr>
          </thead>
          <tbody>
            {view.rows.map((r) => (
              <ParameterRow key={r.name} row={r} />
            ))}
          </tbody>
        </table>
      </div>
      <p className="mt-2 text-[11.5px] text-theme-text-tertiary">
        Read {d.sampledAt ? `${formatAge(d.sampledAt)} ago` : 'just now'} from a new Radar session on each of {view.read.map((i) => i.pod).join(', ') || 'no instance'}. A role, database or
        client can set its own value for a parameter a session can change. PostgreSQL shows values in its own units, so 1024MB reads 1GB.
        {d.skipped && d.skipped.length > 0 ? ` Not read (not a parameter name): ${d.skipped.join(', ')}.` : ''}
        {d.omitted ? ` ${d.omitted} more declared, not shown.` : ''}
      </p>
      {view.unread.length > 0 && (
        <p className={`mt-1 text-xs ${toneTextClass('unknown')}`}>
          Not read on {view.unread.map((i) => `${i.pod} (${i.error ?? i.state})`).join(', ')}.
        </p>
      )}
    </FoldSection>
  )
}

function ParameterRow({ row }: { row: CNPGParameterRow }) {
  return (
    <tr className="border-b border-theme-border/60 align-top last:border-0">
      <td className="py-1.5 pr-4 font-mono text-xs text-theme-text-primary">{row.name}</td>
      <td className="py-1.5 pr-4 font-mono text-xs text-theme-text-secondary">{row.declared === '' ? <span className="text-theme-text-tertiary">(empty)</span> : row.declared}</td>
      <td className="py-1.5 pr-4 text-xs">
        {row.setByClient ? (
          <span className="text-theme-text-tertiary">Set by each connection, so not readable here</span>
        ) : row.perInstance ? (
          <span className={toneTextClass('degraded')}>{row.perInstance.map((p) => `${p.pod}: ${p.value}`).join(' · ')}</span>
        ) : row.value !== undefined ? (
          <span className="font-mono text-theme-text-primary">{row.value === '' ? '(empty)' : row.value}</span>
        ) : (
          <span className="text-theme-text-tertiary">Not reported</span>
        )}
        {row.source && row.source !== 'configuration file' && !row.setByClient && <span className="ml-1.5 text-theme-text-tertiary">source: {row.source}</span>}
        {row.pendingRestart.length > 0 && <div className={toneTextClass('degraded')}>Restart pending on {row.pendingRestart.join(', ')}</div>}
        {row.unreported.length > 0 && (row.value !== undefined || row.perInstance || row.setByClient) && (
          <div className="text-theme-text-tertiary">Not reported by {row.unreported.join(', ')}</div>
        )}
      </td>
      <td className="py-1.5 text-xs text-theme-text-secondary">{row.takesEffect}</td>
    </tr>
  )
}
