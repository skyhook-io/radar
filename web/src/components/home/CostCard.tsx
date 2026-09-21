import type { OpenCostSummary } from '../../api/client'
import { useOpenCostSummary } from '../../api/client'
import { Coins } from 'lucide-react'
import {
  DEFAULT_COST_CURRENCY,
  formatCostPerHour,
  formatProjectedDailyRate,
  formatProjectedMonthlyCost,
  formatProjectedMonthlyRate,
} from '../cost/format'
import { costFreshnessLabel } from '../cost/source'

export function CostCard({ onNavigate }: { onNavigate?: () => void }) {
  const { data } = useOpenCostSummary()

  // Only show when OpenCost data is actually available — no placeholder card
  if (!data || !data.available) {
    return null
  }

  return <CostCardContent data={data} onNavigate={onNavigate} />
}

function CostCardContent({ data, onNavigate }: { data: OpenCostSummary; onNavigate?: () => void }) {
  const hourlyCost = data.totalHourlyCost ?? 0
  const currency = data.currency ?? DEFAULT_COST_CURRENCY
  const namespaces = data.namespaces ?? []
  const topNamespaces = namespaces.slice(0, 5)

  // Find the max cost for bar scaling
  const maxCost = topNamespaces.length > 0 ? topNamespaces[0].hourlyCost : 0

  return (
    <div
      onClick={onNavigate}
      className={`h-[260px] rounded-xl bg-theme-surface shadow-theme-sm text-left animate-fade-in-up ${onNavigate ? 'cursor-pointer hover:-translate-y-1 hover:shadow-theme-md transition-all duration-200' : ''}`}
    >
      <div className="flex flex-col h-full w-full">
        <div className="flex items-center justify-between px-5 py-3 border-b border-theme-border/50">
          <div className="flex items-center gap-2">
            <Coins className="w-4 h-4 text-accent-text" />
            <span className="text-xs font-semibold uppercase tracking-wider text-accent-text">Cost Insights</span>
            {namespaces.length > 0 && (
              <span className="badge-sm border border-theme-border bg-accent-muted text-accent-text">
                {namespaces.length} ns
              </span>
            )}
          </div>
        </div>

        <div className="flex-1 min-h-0 flex flex-col px-4 py-3">
          {/* Hero cost numbers */}
          <div className="flex items-baseline gap-3 mb-3">
            <div className="flex items-baseline gap-1">
              <span className="text-2xl font-bold text-theme-text-primary tabular-nums">
                {formatProjectedMonthlyCost(hourlyCost, currency)}
              </span>
              <span className="text-xs text-theme-text-tertiary">/mo</span>
            </div>
            <div className="flex items-baseline gap-1.5 text-theme-text-secondary">
              <span className="text-xs font-medium tabular-nums">{formatProjectedDailyRate(hourlyCost, currency)}</span>
              <span className="text-[10px] text-theme-text-quaternary">·</span>
              <span className="text-xs font-medium tabular-nums">{formatCostPerHour(hourlyCost, currency)}</span>
            </div>
          </div>

          {/* Top namespaces */}
          <div className="flex-1 min-h-0 space-y-1.5">
            {topNamespaces.map((ns) => {
              const pct = maxCost > 0 ? (ns.hourlyCost / maxCost) * 100 : 0
              return (
                <div key={ns.name} className="flex items-center gap-2">
                  <span className="text-[11px] text-theme-text-secondary truncate w-24 shrink-0">{ns.name}</span>
                  <div className="flex-1 h-2 rounded-full overflow-hidden bg-theme-hover">
                    <div className="h-full rounded-full bg-indigo-500/60" style={{ width: `${Math.max(pct, 2)}%` }} />
                  </div>
                  <span className="text-[10px] text-theme-text-tertiary tabular-nums w-20 text-right shrink-0">
                    {formatProjectedMonthlyRate(ns.hourlyCost, currency)}
                  </span>
                </div>
              )
            })}
            {namespaces.length > 5 && (
              <span className="text-[10px] text-theme-text-tertiary">+{namespaces.length - 5} more namespaces</span>
            )}
          </div>
        </div>

        <div className="px-4 py-1.5 border-t border-theme-border/50 flex items-center justify-between">
          <span className="text-[10px] text-theme-text-tertiary">
            {currency} &middot; projected from {costFreshnessLabel(data.source, data.window, data.dataThrough)}
            {currency !== DEFAULT_COST_CURRENCY && (
              <> &middot; no conversion</>
            )}
          </span>
          <span className="flex items-center gap-1.5 text-[10px] font-semibold uppercase tracking-wider text-accent-text">
            {data.source === 'kubecost' ? 'Kubecost' : 'OpenCost'}
          </span>
        </div>
      </div>
    </div>
  )
}
