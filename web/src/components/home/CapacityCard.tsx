import { Layers3 } from 'lucide-react'
import { clsx } from 'clsx'
import { useCapacityOverview } from '../../api/client'
import { useCapabilitiesContext } from '../../contexts/CapabilitiesContext'
import { coverageIsLowerBound, humanizeCode } from '../capacity/shared'

// CapacityCard surfaces Karpenter fleet posture on the Home dashboard —
// pool count, pending demand, and the worst operational signal — so morning
// triage reaches the Capacity view without knowing it exists.
//
// Capability-gated twice: rendered only when the RBAC-aware Karpenter
// capability is available, and content only when the overview responds
// available. Numbers the backend omitted (RBAC-denied / unobserved sources)
// render as "—", never zero.
export function CapacityCard({ onNavigate }: { onNavigate: () => void }) {
  const karpenterAvailable = useCapabilitiesContext().karpenter?.state === 'available'
  const { data } = useCapacityOverview({ enabled: karpenterAvailable })
  if (!karpenterAvailable || !data || data.state !== 'available') return null

  const actions = data.summary.actions ?? []
  const worst = actions.find((a) => a.highestSeverity === 'critical') ?? actions.find((a) => a.highestSeverity === 'warning')
  const headerTone = worst
    ? worst.highestSeverity === 'critical'
      ? 'text-red-500'
      : 'text-amber-400'
    : 'text-emerald-500'
  const headerLabel = worst ? humanizeCode(worst.code) : 'No active signals'

  // Namespace-scoped pod coverage hides pending pods this identity cannot see,
  // so the count is a floor — it must not read as the cluster total.
  const pendingIsLowerBound = coverageIsLowerBound(data.coverage.pods)
  const stats: { label: string; value: string }[] = [
    { label: 'NodePools', value: absentAsDash(data.summary.poolCount) },
    {
      label: 'Pending pods',
      value: absentAsDash(data.summary.pendingPodCount, pendingIsLowerBound ? '≥' : ''),
    },
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
