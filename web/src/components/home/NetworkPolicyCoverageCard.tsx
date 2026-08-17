import type { DashboardNetworkPolicyCoverage } from '../../api/client'
import { ShieldCheck, ArrowRight } from 'lucide-react'
import { clsx } from 'clsx'

interface NetworkPolicyCoverageCardProps {
  data: DashboardNetworkPolicyCoverage
  onNavigate: () => void
}

export function NetworkPolicyCoverageCard({ data, onNavigate }: NetworkPolicyCoverageCardProps) {
  const hasStagedPolicies = (data.stagedPolicies ?? 0) > 0
  const coveredIfStaged = Math.max(data.coveredWorkloads, data.coveredWorkloadsIfStaged ?? data.coveredWorkloads)
  const percentage = data.totalWorkloads > 0
    ? Math.round((data.coveredWorkloads / data.totalWorkloads) * 100)
    : 0
  const percentageIfStaged = data.totalWorkloads > 0
    ? Math.round((coveredIfStaged / data.totalWorkloads) * 100)
    : 0
  const stagedOnlyPercentage = Math.max(0, percentageIfStaged - percentage)
  const hasPolicies = data.totalPolicies > 0
  const accentColor = !hasPolicies
    ? 'text-theme-text-tertiary'
    : percentage >= 75
      ? 'text-green-500'
      : percentage >= 40
        ? 'text-yellow-500'
        : 'text-red-500'

  return (
    <button
      onClick={onNavigate}
      className="group h-[260px] rounded-xl bg-theme-surface shadow-theme-sm hover:-translate-y-1 hover:shadow-theme-md transition-all duration-200 text-left animate-fade-in-up"
    >
      <div className="flex flex-col h-full w-full">
        <div className="flex items-center justify-between px-5 py-3 border-b border-theme-border/50">
          <div className="flex items-center gap-2">
            <ShieldCheck className={clsx('w-4 h-4', accentColor)} />
            <span className={clsx('text-xs font-semibold uppercase tracking-wider', accentColor)}>
              Network Policies
            </span>
            <span className={clsx('badge-sm', hasPolicies ? 'bg-theme-elevated text-theme-text-secondary' : 'bg-theme-hover text-theme-text-tertiary')}>
              {data.totalPolicies}
            </span>
          </div>
        </div>

        <div className="flex-1 min-h-0 flex flex-col items-center justify-center px-4 py-4">
          {!hasPolicies ? (
            <div className="flex flex-col items-center justify-center text-center gap-2">
              <ShieldCheck className="w-8 h-8 text-theme-text-tertiary/40" />
              <span className="text-xs text-theme-text-tertiary">No network policies configured</span>
            </div>
          ) : (
            <>
              <div className="flex items-center gap-3 w-full">
                <div className="flex-1 h-3 rounded-full overflow-hidden bg-theme-hover flex">
                  {data.coveredWorkloads > 0 && (
                    <div
                      className="h-full bg-green-500"
                      style={{ width: `${percentage}%` }}
                    />
                  )}
                  {hasStagedPolicies && stagedOnlyPercentage > 0 && (
                    <div
                      className="h-full text-yellow-500"
                      title={`${coveredIfStaged - data.coveredWorkloads} additional workloads if staged policies are applied`}
                      style={{
                        width: `${stagedOnlyPercentage}%`,
                        backgroundImage: 'repeating-linear-gradient(135deg, currentColor 0, currentColor 2px, transparent 2px, transparent 5px)',
                      }}
                    />
                  )}
                  {data.totalWorkloads - coveredIfStaged > 0 && (
                    <div
                      className="h-full bg-theme-hover"
                      style={{ width: `${100 - percentageIfStaged}%` }}
                    />
                  )}
                </div>
                <div className="flex shrink-0 flex-col items-end tabular-nums">
                  <span className={clsx('text-sm font-semibold', accentColor)}>{percentage}%</span>
                  {hasStagedPolicies && (
                    <span className="text-[10px] text-theme-text-tertiary">({percentageIfStaged}% if staged applied)</span>
                  )}
                </div>
              </div>

              <div className="grid grid-cols-1 gap-y-2 mt-4 w-full">
                <StatRow label="Policies" value={data.totalPolicies} />
                <StatRow label="Covered workloads" value={data.coveredWorkloads} total={data.totalWorkloads} />
                {hasStagedPolicies && (
                  <StatRow label="Covered if staged" value={coveredIfStaged} total={data.totalWorkloads} />
                )}
                <StatRow label="Uncovered workloads" value={data.totalWorkloads - data.coveredWorkloads} warn />
                {hasStagedPolicies && (
                  <StatRow label="Uncovered if staged" value={data.totalWorkloads - coveredIfStaged} warn />
                )}
              </div>
            </>
          )}
        </div>

        <div className="px-4 py-1.5 border-t border-theme-border/50 flex items-center justify-end">
          <span className={clsx(
            'flex items-center gap-1.5 text-[10px] font-semibold uppercase tracking-wider transition-colors',
            accentColor,
          )}>
            View Policies
            <ArrowRight className="w-3.5 h-3.5 transition-transform group-hover:translate-x-0.5" />
          </span>
        </div>
      </div>
    </button>
  )
}

function StatRow({ label, value, total, warn }: {
  label: string
  value: number
  total?: number
  warn?: boolean
}) {
  return (
    <div className="flex items-center justify-between">
      <span className="text-xs text-theme-text-secondary">{label}</span>
      <span className={clsx(
        'text-sm font-semibold tabular-nums',
        warn && value > 0 ? 'text-yellow-400' : 'text-theme-text-primary',
      )}>
        {total !== undefined ? `${value}/${total}` : value}
      </span>
    </div>
  )
}
