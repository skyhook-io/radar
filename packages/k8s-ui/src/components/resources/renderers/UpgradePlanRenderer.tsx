import { Badge } from '../../ui/Badge'
import { healthToSeverity } from '../../../utils/badge-colors'
import { Section, Property, PropertyList, ResourceLink, ConditionsSection, AlertBanner, useOperationalIssuesShown } from '../../ui/drawer-components'
import type { ResourceRef } from '../../../types'
import { getUpgradePlanStatus, upgradePlanProgress, upgradePlanTarget, type PlanNode, type PlanNodeState } from '../resource-utils-upgrade-plan'

const ROW_CAP = 50
const STATE_BADGE: Record<PlanNodeState, { label: string; severity: 'success' | 'info' | 'neutral' }> = {
  upgraded: { label: 'Upgraded', severity: 'success' },
  upgrading: { label: 'Upgrading', severity: 'info' },
  pending: { label: 'Pending', severity: 'neutral' },
}

function Progress({ data, nodes, onNavigate }: { data: any; nodes: PlanNode[] | null; onNavigate?: (ref: ResourceRef) => void }) {
  if (nodes === null) {
    return <div className="text-sm text-theme-text-tertiary">Nodes could not be read, so progress is unknown.</div>
  }
  const p = upgradePlanProgress(data, nodes)
  if (p.state === 'unsupported') {
    return <div className="text-sm text-theme-text-tertiary">The node selector uses {p.operator}, which Radar does not evaluate.</div>
  }
  if (p.total === 0) return <div className="text-sm text-theme-text-tertiary">The node selector matches no Node.</div>
  const pct = Math.round((p.upgraded / p.total) * 100)
  return (
    <>
      <div className="mb-1 text-sm text-theme-text-primary">{p.upgraded} of {p.total} nodes upgraded{p.upgrading > 0 ? ` · ${p.upgrading} upgrading` : ''}</div>
      <div className="mb-3 h-1.5 w-full overflow-hidden rounded bg-theme-elevated">
        <div className="h-full bg-accent" style={{ width: `${pct}%` }} />
      </div>
      <table className="w-full text-xs">
        <tbody>
          {p.rows.slice(0, ROW_CAP).map((r) => (
            <tr key={r.name} className="border-t border-theme-border">
              <td className="py-1 pr-3"><ResourceLink name={r.name} kind="nodes" onNavigate={onNavigate} /></td>
              <td className="py-1"><Badge severity={STATE_BADGE[r.state].severity} size="sm">{STATE_BADGE[r.state].label}</Badge></td>
            </tr>
          ))}
        </tbody>
      </table>
      {p.rows.length > ROW_CAP && <div className="mt-1 text-xs text-theme-text-tertiary">+{p.rows.length - ROW_CAP} more</div>}
    </>
  )
}

export function UpgradePlanRenderer({ data, onNavigate, nodes, nodesLoading }: { data: any; onNavigate?: (ref: ResourceRef) => void; nodes?: PlanNode[] | null; nodesLoading?: boolean }) {
  const status = getUpgradePlanStatus(data)
  const spec = data.spec ?? {}
  const invalid = (data.status?.conditions ?? []).find((c: any) => c?.type === 'Validated' && c?.status === 'False')
  const operationalIssuesShown = useOperationalIssuesShown()
  const secrets: string[] = (spec.secrets ?? []).map((s: any) => s?.name).filter(Boolean)
  // Env values can hold credentials: only how many are set is shown.
  const envCount = (spec.upgrade?.envs?.length ?? 0) + (spec.prepare?.envs?.length ?? 0)
  const envSources = (spec.upgrade?.envFrom?.length ?? 0) + (spec.prepare?.envFrom?.length ?? 0)
  const envText = [
    envCount > 0 ? `${envCount} variable${envCount === 1 ? '' : 's'}` : '',
    envSources > 0 ? `from ${envSources} source${envSources === 1 ? '' : 's'}` : '',
  ].filter(Boolean).join(' · ')
  return (
    <>
      {invalid && !operationalIssuesShown && <AlertBanner variant="error" title="Plan is not valid" message={invalid.message || 'The system-upgrade-controller rejected this Plan; no Node is upgraded while it is invalid.'} />}
      <Section title="Status" defaultExpanded={true}>
        <PropertyList>
          <Property label="State" value={<Badge severity={healthToSeverity(status.level)}>{status.text}</Badge>} />
          {data.status?.latestVersion && <Property label="Resolved version" value={data.status.latestVersion} />}
        </PropertyList>
      </Section>
      <Section title="Target" defaultExpanded={true}>
        <PropertyList>
          <Property label="Version" value={upgradePlanTarget(data)} />
          {spec.upgrade?.image && <Property label="Upgrade image" value={spec.upgrade.image} />}
          {spec.concurrency != null && <Property label="Concurrency" value={String(spec.concurrency)} />}
          {spec.serviceAccountName && <Property label="Service account" value={spec.serviceAccountName} />}
          {spec.cordon != null && <Property label="Cordon" value={spec.cordon ? 'yes' : 'no'} />}
          {spec.drain && <Property label="Drain" value="yes" />}
          {secrets.length > 0 && <Property label="Secrets" value={secrets.join(', ')} />}
          {envText && <Property label="Environment" value={`${envText} (values not shown)`} />}
        </PropertyList>
      </Section>
      {(nodes !== undefined || nodesLoading) && (
        <Section title="Progress" defaultExpanded={true}>
          {nodesLoading && nodes === undefined ? <div className="text-sm text-theme-text-tertiary">Reading Nodes…</div> : <Progress data={data} nodes={nodes ?? null} onNavigate={onNavigate} />}
        </Section>
      )}
      <ConditionsSection conditions={data.status?.conditions} />
    </>
  )
}
