import { Layers3 } from 'lucide-react'
import { clsx } from 'clsx'
import { useCapacityOverview } from '../../api/client'
import { useCapabilitiesContext } from '../../contexts/CapabilitiesContext'
import { coverageIsLowerBound, humanizeCode } from '../capacity/shared'

// CapacityCard surfaces the cluster's capacity posture on the Home dashboard
// so morning triage reaches the Capacity view without knowing it exists. It
// renders whenever the cluster has a capacity story: Karpenter available, a
// Karpenter the caller is denied (softened rows, honest header — the denied
// Overview shape carries real node/pod truth), or detected managers/groups on
// a Karpenter-less cluster. Bare clusters with no story keep a quiet Home.
// Numbers the backend omitted (RBAC-denied / unobserved) render as "—",
// never zero.
export function CapacityCard({ onNavigate }: { onNavigate: () => void }) {
  const karpenterState = useCapabilitiesContext().karpenter?.state
  const mayHaveStory =
    karpenterState === 'available' || karpenterState === 'denied' || karpenterState === 'not_detected'
  const { data } = useCapacityOverview({ enabled: mayHaveStory })
  if (!mayHaveStory || !data) return null
  const karpenterDenied = data.state === 'denied'
  const karpenterless = data.state === 'not_detected'
  if (!karpenterDenied && !karpenterless && data.state !== 'available') return null
  if (karpenterless && (data.summary.managers?.length ?? 0) === 0 && data.groups.length === 0)
    return null

  const actions = data.summary.actions ?? []
  const worst = actions.find((a) => a.highestSeverity === 'critical') ?? actions.find((a) => a.highestSeverity === 'warning')
  // A denied Karpenter must not read as healthy: "no signals" would be a
  // claim about a fleet this identity cannot see.
  const headerTone = karpenterDenied
    ? 'text-theme-text-tertiary'
    : worst
      ? worst.highestSeverity === 'critical'
        ? 'text-red-500'
        : 'text-amber-400'
      : 'text-emerald-500'
  const headerLabel = karpenterDenied
    ? 'Karpenter view unavailable'
    : worst
      ? humanizeCode(worst.code)
      : 'No active signals'

  // Namespace-scoped pod coverage hides pending pods this identity cannot see,
  // so the count is a floor — it must not read as the cluster total.
  const pendingIsLowerBound = coverageIsLowerBound(data.coverage.pods)
  const pendingStat = {
    label: 'Pending pods',
    value: absentAsDash(data.summary.pendingPodCount, pendingIsLowerBound ? '≥' : ''),
  }
  const stats: { label: string; value: string }[] = karpenterless
    ? [
        { label: 'Node groups', value: `${data.groups.length}` },
        pendingStat,
        { label: 'Managers', value: `${data.summary.managers?.length ?? 0}` },
        { label: 'Nodes', value: absentAsDash(data.summary.nodeCount) },
      ]
    : [
        { label: 'NodePools', value: absentAsDash(data.summary.poolCount) },
        pendingStat,
        { label: 'NodeClaims', value: absentAsDash(data.summary.claimCount) },
        { label: 'Nodes', value: absentAsDash(data.summary.nodeCount) },
      ]

  return (
    <button
      type="button"
      onClick={onNavigate}
      className="group h-[260px] rounded-xl bg-theme-surface shadow-theme-sm hover:-translate-y-1 hover:shadow-theme-md transition-all duration-200 text-left animate-fade-in-up"
    >
      <div className="flex flex-col h-full w-full">
        <div className="flex items-center justify-between px-5 py-3 border-b border-theme-border/50">
          <div className="flex items-center gap-2">
            <Layers3 className={clsx('h-4 w-4', headerTone)} />
            <span className={clsx('text-xs font-semibold uppercase tracking-wider', headerTone)}>
              Capacity
            </span>
          </div>
          <span className={clsx('max-w-[55%] truncate text-[11px] font-medium', headerTone)}>{headerLabel}</span>
        </div>

        <div className="flex-1 min-h-0 px-5 py-3">
          <div className="grid grid-cols-2 gap-x-4 gap-y-3">
            {stats.map((stat) => (
              <div key={stat.label}>
                <div className="text-lg font-semibold tabular-nums text-theme-text-primary">{stat.value}</div>
                <div className="text-[11px] text-theme-text-tertiary">{stat.label}</div>
              </div>
            ))}
          </div>
          {actions.length > 0 && (
            <div className="mt-3 space-y-1">
              {actions.slice(0, 2).map((action) => (
                <div key={action.code} className="flex items-center gap-1.5 text-[11px] text-theme-text-secondary">
                  <span
                    className={clsx(
                      'h-1.5 w-1.5 shrink-0 rounded-full',
                      action.highestSeverity === 'critical'
                        ? 'bg-red-500'
                        : action.highestSeverity === 'warning'
                          ? 'bg-amber-400'
                          : 'bg-sky-400',
                    )}
                  />
                  <span className="truncate">
                    {humanizeCode(action.code)}
                    {action.count > 1 ? ` (${action.count})` : ''}
                  </span>
                </div>
              ))}
            </div>
          )}
        </div>

        <div className="px-5 py-2.5 border-t border-theme-border/50 text-[11px] text-theme-text-tertiary group-hover:text-theme-text-secondary transition-colors">
          Open Capacity →
        </div>
      </div>
    </button>
  )
}

function absentAsDash(value: number | undefined, prefix = ''): string {
  return value === undefined ? '—' : `${prefix}${value}`
}
