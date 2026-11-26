import type { ReactNode } from 'react'
import { clsx } from 'clsx'
import { Badge } from '../../ui/Badge'
import { NetworkPolicyGroupList } from './NetworkPolicyGroupList'
import type { NetworkPolicySummary, PolicyDirectionSummary } from '../../../utils/network-policy-summary'

type Direction = 'ingress' | 'egress'

const WORDS: Record<Direction, { label: string; flow: string; restrictedClass: string }> = {
  ingress: { label: 'Ingress', flow: 'inbound', restrictedClass: 'status-blue' },
  egress: { label: 'Egress', flow: 'outbound', restrictedClass: 'status-purple' },
}

export function NetworkPolicyAppliesTo({ summary }: { summary: NetworkPolicySummary }) {
  return (
    <div data-applies-to className="min-w-0 flex-1 space-y-1.5">
      <div className="text-xs text-theme-text-tertiary">Applies to</div>
      <div className="flex flex-wrap items-center gap-1.5 min-w-0">
        <span className="inline-flex max-w-full items-center gap-1.5 rounded border border-theme-border bg-theme-elevated px-2 py-0.5 text-xs text-theme-text-primary">
          <span className="w-1.5 h-1.5 rounded-full shrink-0 bg-indigo-500" />
          <span className="min-w-0 [overflow-wrap:anywhere]">{summary.target}</span>
        </span>
      </div>
      <div className="flex flex-wrap items-center gap-1">
        {(['ingress', 'egress'] as const).map((dir) => (
          <span key={dir} className={clsx('badge', summary[dir].restricted ? WORDS[dir].restrictedClass : 'bg-theme-elevated text-theme-text-tertiary')}>
            {`${WORDS[dir].label} ${summary[dir].restricted ? 'restricted' : 'not restricted'}`}
          </span>
        ))}
        {summary.defaultedTypes && <span className="text-xs text-theme-text-tertiary">by default — policyTypes not set</span>}
      </div>
    </div>
  )
}

export function NetworkPolicyRulesView({ summary, staged = false }: { summary: NetworkPolicySummary; staged?: boolean }) {
  const directions = (['ingress', 'egress'] as const).filter((dir) => summary[dir].restricted)
  return (
    <div className="space-y-4">
      {staged && <StagedNote />}
      {directions.map((dir) => (
        <DirectionRules key={dir} direction={dir} summary={summary[dir]} staged={staged} />
      ))}
      {directions.length === 0 && <div className="text-xs text-theme-text-tertiary">No policy types specified</div>}
    </div>
  )
}

function StagedNote() {
  return (
    <div className="flex items-center gap-2 text-[11px] text-theme-text-tertiary">
      <Badge severity="warning" size="sm">Staged preview</Badge>
      <span>Evaluated but not enforced</span>
    </div>
  )
}

function DirectionRules({ direction, summary, staged }: { direction: Direction; summary: PolicyDirectionSummary; staged: boolean }) {
  const { flow } = WORDS[direction]
  if (summary.denyAll) {
    return <DenyLine>{`All ${flow} denied`}</DenyLine>
  }
  const count = summary.rules.length
  return (
    <div className="space-y-1.5">
      <div className="text-xs font-medium text-theme-text-secondary">
        {`Allowed ${flow} `}
        <span className="font-normal text-theme-text-tertiary">{count === 1 ? '· 1 rule' : `· ${count} rules, any one matches`}</span>
      </div>
      {summary.rules.map((rule) => (
        <div
          key={rule.index}
          className={clsx('min-w-0 rounded-md border bg-theme-elevated/50 px-2.5 py-2 space-y-2', staged ? 'border-dashed border-theme-border' : 'border-theme-border')}
        >
          <div className="flex items-start justify-between gap-2">
            <span className="text-[10px] uppercase tracking-wider text-theme-text-tertiary">{`Rule ${rule.index}`}</span>
            <PortChips ports={rule.ports} />
          </div>
          {rule.groups.map((group, i) => (
            <NetworkPolicyGroupList key={i} group={group} />
          ))}
        </div>
      ))}
      <DenyLine>{`Everything else ${flow} is denied`}</DenyLine>
    </div>
  )
}

function PortChips({ ports }: { ports: string[] }) {
  if (ports.length === 0) {
    return <span className="rounded border border-dashed border-theme-border px-1.5 text-[10px] text-theme-text-tertiary">any port</span>
  }
  return (
    <span className="flex flex-wrap justify-end gap-1">
      {ports.map((port, i) => (
        <span key={i} className="badge-sm status-blue font-mono">{port}</span>
      ))}
    </span>
  )
}

function DenyLine({ children }: { children: ReactNode }) {
  return (
    <div className="flex items-center gap-1.5 rounded-md border border-red-500/30 bg-red-500/8 px-2.5 py-1.5 text-xs text-theme-text-secondary">
      <span className="w-1.5 h-1.5 rounded-full shrink-0 bg-red-500" />
      {children}
    </div>
  )
}
