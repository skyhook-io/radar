import type { DashboardNetworkPolicyCoverage } from '../../api/client'
import { ShieldCheck, ArrowRight } from 'lucide-react'
import { clsx } from 'clsx'
import { Tooltip } from '../ui/Tooltip'

interface NetworkPolicyCoverageCardProps {
  data: DashboardNetworkPolicyCoverage
  onNavigate: () => void
}

export function NetworkPolicyCoverageCard({ data, onNavigate }: NetworkPolicyCoverageCardProps) {
  const hasStagedPolicies = (data.stagedPolicies ?? 0) > 0
  // A staged policy can stage a deletion, so the projected coverage is allowed
  // to be lower than today's. Clamping it would hide exactly the case an
  // operator most needs to see before promoting the staged set.
  const coveredIfStaged = data.coveredWorkloadsIfStaged ?? data.coveredWorkloads
  const stagedDelta = coveredIfStaged - data.coveredWorkloads
  const percentage = data.totalWorkloads > 0
    ? Math.round((data.coveredWorkloads / data.totalWorkloads) * 100)
    : 0
  const percentageIfStaged = data.totalWorkloads > 0
    ? Math.round((coveredIfStaged / data.totalWorkloads) * 100)
    : 0
  const enforcedPercentage = hasStagedPolicies ? Math.min(percentage, percentageIfStaged) : percentage
  // Gated on the same condition as the segment that draws it, so the three
  // widths always sum to the full track even for a host that supplies a
  // projection without any staged policies.
  const stagedDeltaPercentage = hasStagedPolicies ? Math.abs(percentageIfStaged - percentage) : 0
  const hasPolicies = data.totalPolicies > 0
  const ignoredNamespaceWorkloads = data.ignoredNamespaceWorkloads ?? 0
  const hostNetworkWorkloads = data.hostNetworkWorkloads ?? 0
  const notCounted = ignoredNamespaceWorkloads + hostNetworkWorkloads
  const notCountedReasons = [
    ignoredNamespaceWorkloads > 0 && `${ignoredNamespaceWorkloads} in namespaces ignored in Checks settings.`,
    hostNetworkWorkloads > 0 && `${hostNetworkWorkloads} on the host network, where network policies may not apply.`,
  ].filter(Boolean).join(' ')
  // With nothing to count, 0% would read as a coverage failure.
  const hasWorkloads = data.totalWorkloads > 0
  const accentColor = !hasPolicies || !hasWorkloads
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
          ) : !hasWorkloads ? (
            <div className="flex flex-col items-center justify-center text-center gap-2">
              <ShieldCheck className="w-8 h-8 text-theme-text-tertiary/40" />
              <span className="text-xs text-theme-text-tertiary">No workloads counted</span>
            </div>
          ) : (
            <>
              <div className="flex items-center gap-3 w-full">
                <div className="flex-1 h-3 rounded-full overflow-hidden bg-theme-hover flex">
                  {enforcedPercentage > 0 && (
                    <div
                      className="h-full bg-green-500"
                      style={{ width: `${enforcedPercentage}%` }}
                    />
                  )}
                  {hasStagedPolicies && stagedDeltaPercentage > 0 && (
                    // The width belongs on the flex child; the tooltip wrapper
                    // inside it carries the hover target.
                    <div className="h-full" style={{ width: `${stagedDeltaPercentage}%` }}>
                      <Tooltip
                        content={stagedDelta > 0
                          ? `${stagedDelta} more workloads covered if staged policies are applied`
                          : `${-stagedDelta} workloads lose coverage if staged policies are applied`}
                        wrapperClassName="!block h-full w-full"
                      >
                        <div
                          className={clsx('h-full w-full', stagedDelta > 0 ? 'text-yellow-500' : 'text-red-500')}
                          style={{
                            backgroundImage: 'repeating-linear-gradient(135deg, currentColor 0, currentColor 2px, transparent 2px, transparent 5px)',
                          }}
                        />
                      </Tooltip>
                    </div>
                  )}
                  {100 - enforcedPercentage - stagedDeltaPercentage > 0 && (
                    <div
                      className="h-full bg-theme-hover"
                      style={{ width: `${100 - enforcedPercentage - stagedDeltaPercentage}%` }}
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

        <div className={clsx(
          'px-4 py-1.5 border-t border-theme-border/50 flex items-center',
          notCounted > 0 ? 'justify-between' : 'justify-end',
        )}>
          {notCounted > 0 && (
            <Tooltip content={`Workloads not counted: ${notCountedReasons}`}>
              <span className="text-[10px] text-theme-text-tertiary">
                {notCounted} not counted
              </span>
            </Tooltip>
          )}
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
