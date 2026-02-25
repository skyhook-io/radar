import type { OpenCostSummary } from '../../api/client'
import { useOpenCostSummary } from '../../api/client'
import { DollarSign, ExternalLink, ArrowRight } from 'lucide-react'

export function CostCard() {
  const { data, isLoading } = useOpenCostSummary()

  if (isLoading) {
    return (
      <div className="h-[260px] rounded-lg border-[3px] border-indigo-500/20 bg-theme-surface/50 animate-pulse" />
    )
  }

  if (!data || !data.available) {
    return <CostCardUnavailable />
  }

  return <CostCardContent data={data} />
}

function CostCardUnavailable() {
  return (
    <a
      href="https://www.opencost.io"
      target="_blank"
      rel="noopener noreferrer"
      className="group h-[260px] rounded-lg border-[3px] border-theme-border/40 bg-theme-surface/50 hover:-translate-y-1 hover:shadow-[0_12px_24px_rgba(0,0,0,0.12)] hover:border-theme-border/60 transition-all duration-200 text-left cursor-pointer block"
    >
      <div className="flex flex-col h-full w-full">
        <div className="flex items-center justify-between px-4 py-2 border-b border-theme-border">
          <div className="flex items-center gap-2">
            <DollarSign className="w-4 h-4 text-theme-text-tertiary" />
            <span className="text-sm font-semibold text-theme-text-tertiary">Cost Insights</span>
          </div>
        </div>

        <div className="flex-1 min-h-0 flex flex-col items-center justify-center px-4 py-4">
          <DollarSign className="w-8 h-8 text-theme-text-tertiary/40 mb-2" />
          <span className="text-xs font-medium text-theme-text-secondary">OpenCost not detected</span>
          <span className="text-[11px] text-theme-text-tertiary mt-1 text-center">
            Install OpenCost for Kubernetes cost visibility
          </span>
        </div>

        <div className="px-4 py-1.5 border-t border-theme-border flex items-center justify-end">
          <span className="flex items-center gap-1.5 text-xs font-medium text-theme-text-tertiary group-hover:text-theme-text-secondary transition-colors">
            opencost.io
            <ExternalLink className="w-3.5 h-3.5" />
          </span>
        </div>
      </div>
    </a>
  )
}

function CostCardContent({ data }: { data: OpenCostSummary }) {
  const hourlyCost = data.totalHourlyCost ?? 0
  const monthlyCost = hourlyCost * 730
  const namespaces = data.namespaces ?? []
  const topNamespaces = namespaces.slice(0, 5)

  // Find the max cost for bar scaling
  const maxCost = topNamespaces.length > 0 ? topNamespaces[0].hourlyCost : 0

  return (
    <div
      className="group h-[260px] rounded-lg border-[3px] border-indigo-500/30 bg-theme-surface/50 hover:-translate-y-1 hover:shadow-[0_12px_24px_rgba(0,0,0,0.12)] hover:border-indigo-500/60 transition-all duration-200 text-left"
    >
      <div className="flex flex-col h-full w-full">
        <div className="flex items-center justify-between px-4 py-2 border-b border-theme-border">
          <div className="flex items-center gap-2">
            <DollarSign className="w-4 h-4 text-indigo-500" />
            <span className="text-sm font-semibold text-indigo-500">Cost Insights</span>
            {namespaces.length > 0 && (
              <span className="text-[11px] bg-indigo-500/10 px-1.5 py-0.5 rounded text-indigo-500">
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

        <div className="px-4 py-1.5 border-t border-theme-border flex items-center justify-between">
          <span className="text-[10px] text-theme-text-tertiary">
            {data.currency ?? 'USD'} &middot; {data.window ?? '1h'} window
          </span>
          <span className="flex items-center gap-1.5 text-xs font-medium text-indigo-500 group-hover:text-indigo-400 transition-colors">
            OpenCost
            <ArrowRight className="w-3.5 h-3.5 transition-transform group-hover:translate-x-0.5" />
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
