import type { OpenCostSummary } from '../../api/client'
import { useOpenCostSummary } from '../../api/client'
import { DollarSign } from 'lucide-react'

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
  const monthlyCost = hourlyCost * 730
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
            <DollarSign className="w-4 h-4 text-indigo-500" />
            <span className="text-xs font-semibold uppercase tracking-wider text-indigo-500">Cost Insights</span>
            {namespaces.length > 0 && (
              <span className="badge-sm bg-indigo-100 text-indigo-700 border-indigo-300 dark:bg-indigo-950/50 dark:text-indigo-400 dark:border-indigo-700/40">
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
                {formatCost(hourlyCost)}
              </span>
              <span className="text-xs text-theme-text-tertiary">/hr</span>
            </div>
            <div className="flex items-baseline gap-1 text-theme-text-secondary">
              <span className="text-sm font-medium tabular-nums">~{formatCost(monthlyCost)}</span>
              <span className="text-[10px] text-theme-text-tertiary">/mo</span>
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
                    <div
                      className="h-full rounded-full bg-indigo-500/60"
                      style={{ width: `${Math.max(pct, 2)}%` }}
                    />
                  </div>
                  <span className="text-[10px] text-theme-text-tertiary tabular-nums w-14 text-right shrink-0">
                    {formatCost(ns.hourlyCost)}/h
                  </span>
                </div>
              )
            })}
            {namespaces.length > 5 && (
              <span className="text-[10px] text-theme-text-tertiary">
                +{namespaces.length - 5} more namespaces
              </span>
            )}
          </div>
        </div>

        <div className="px-4 py-1.5 border-t border-theme-border/50 flex items-center justify-between">
          <span className="text-[10px] text-theme-text-tertiary">
            {data.currency ?? 'USD'} &middot; {data.window ?? '1h'} window
          </span>
          <span className="flex items-center gap-1.5 text-[10px] font-semibold uppercase tracking-wider text-indigo-500">
            OpenCost
          </span>
        </div>
      </div>
    </div>
  )
}

function formatCost(value: number): string {
  if (value >= 1000) {
    return `$${(value / 1000).toFixed(1)}k`
  }
  if (value >= 1) {
    return `$${value.toFixed(2)}`
  }
  if (value >= 0.01) {
    return `$${value.toFixed(3)}`
  }
  if (value > 0) {
    return `$${value.toFixed(4)}`
  }
  return '$0.00'
}
