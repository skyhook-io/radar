import { useState } from 'react'
import { useOpenCostSummary, useOpenCostWorkloads, useOpenCostNodes } from '../../api/client'
import type { OpenCostNamespaceCost, OpenCostWorkloadCost, OpenCostNodeCost } from '../../api/client'
import { ArrowLeft, ChevronDown, ChevronRight, DollarSign, Loader2, Server } from 'lucide-react'
import { CostTrendChart } from './CostTrendChart'

interface CostViewProps {
  onBack: () => void
}

export function CostView({ onBack }: CostViewProps) {
  const { data, isLoading } = useOpenCostSummary()
  const { data: nodeData } = useOpenCostNodes()

  if (isLoading) {
    return (
      <div className="flex-1 flex items-center justify-center">
        <div className="flex flex-col items-center gap-3">
          <Loader2 className="w-6 h-6 animate-spin text-theme-text-tertiary" />
          <span className="text-sm text-theme-text-tertiary">Loading cost data...</span>
        </div>
      </div>
    )
  }

  if (!data || !data.available) {
    const reason = data?.reason
    const message = reason === 'no_prometheus'
      ? 'Prometheus not found — OpenCost requires Prometheus or VictoriaMetrics'
      : reason === 'no_metrics'
        ? 'OpenCost metrics not found — Prometheus is available but no cost metrics were detected'
        : reason === 'query_error'
          ? 'Cost data temporarily unavailable — Prometheus was found but queries failed'
          : 'OpenCost not detected — install OpenCost for cost visibility'

    return (
      <div className="flex-1 flex items-center justify-center">
        <div className="flex flex-col items-center gap-3 text-theme-text-secondary">
          <DollarSign className="w-8 h-8 text-theme-text-tertiary/40" />
          <p className="text-sm">{message}</p>
          <button
            onClick={onBack}
            className="text-xs text-blue-400 hover:text-blue-300 transition-colors"
          >
            Back to Dashboard
          </button>
        </div>
      </div>
    )
  }

  const hourlyCost = data.totalHourlyCost ?? 0
  const monthlyCost = hourlyCost * 730
  const namespaces = data.namespaces ?? []
  const totalCpu = namespaces.reduce((sum, ns) => sum + ns.cpuCost, 0)
  const totalMem = namespaces.reduce((sum, ns) => sum + ns.memoryCost, 0)
  const totalStorage = data.totalStorageCost ?? 0
  const hasStorage = totalStorage > 0
  const hasEfficiency = (data.clusterEfficiency ?? 0) > 0

  // Compute split percentages (CPU + Memory + optional Storage)
  const allocTotal = totalCpu + totalMem + totalStorage
  const cpuPct = allocTotal > 0 ? (totalCpu / allocTotal) * 100 : 50
  const memPct = allocTotal > 0 ? (totalMem / allocTotal) * 100 : 50
  const storagePct = allocTotal > 0 ? (totalStorage / allocTotal) * 100 : 0

  const nodes = nodeData?.available ? nodeData.nodes ?? [] : []

  return (
    <div className="flex-1 overflow-y-auto">
      <div className="max-w-[1100px] mx-auto px-6 py-6 space-y-6">
        {/* Header */}
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-3">
            <button
              onClick={onBack}
              className="flex items-center gap-1.5 text-sm text-theme-text-secondary hover:text-theme-text-primary transition-colors"
            >
              <ArrowLeft className="w-4 h-4" />
              Dashboard
            </button>
            <div className="w-px h-5 bg-theme-border" />
            <div className="flex items-center gap-2">
              <DollarSign className="w-5 h-5 text-indigo-500" />
              <h1 className="text-lg font-semibold text-theme-text-primary">Cost Insights</h1>
            </div>
          </div>
          <div className="flex items-center gap-4">
            {hasEfficiency && (
              <div className="flex items-center gap-2 text-sm">
                <span className="text-theme-text-tertiary">
                  {formatCost(data.totalIdleCost ?? 0)} idle/hr
                </span>
                <span className="text-theme-text-quaternary">&middot;</span>
                <span className={efficiencyColor(data.clusterEfficiency ?? 0)}>
                  {(data.clusterEfficiency ?? 0).toFixed(0)}% efficient
                </span>
              </div>
            )}
            <div className="flex items-baseline gap-3">
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
          </div>
        </div>

        {/* CPU vs Memory (vs Storage) split bar */}
        <div className="rounded-lg border border-theme-border bg-theme-surface/50 p-4">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-medium text-theme-text-secondary">Cluster Total</span>
            <div className="flex items-center gap-4 text-xs text-theme-text-tertiary">
              <span className="flex items-center gap-1.5">
                <span className="w-2.5 h-2.5 rounded-sm bg-blue-500" />
                CPU {formatCost(totalCpu)}/hr
              </span>
              <span className="flex items-center gap-1.5">
                <span className="w-2.5 h-2.5 rounded-sm bg-purple-500" />
                Memory {formatCost(totalMem)}/hr
              </span>
              {hasStorage && (
                <span className="flex items-center gap-1.5">
                  <span className="w-2.5 h-2.5 rounded-sm bg-teal-500" />
                  Storage {formatCost(totalStorage)}/hr
                </span>
              )}
            </div>
          </div>
          <div className="h-3 rounded-full overflow-hidden bg-theme-hover flex">
            <div
              className="h-full bg-blue-500 transition-all duration-300"
              style={{ width: `${cpuPct}%` }}
            />
            <div
              className="h-full bg-purple-500 transition-all duration-300"
              style={{ width: `${memPct}%` }}
            />
            {hasStorage && (
              <div
                className="h-full bg-teal-500 transition-all duration-300"
                style={{ width: `${storagePct}%` }}
              />
            )}
          </div>
        </div>

        {/* Cost trend chart */}
        <CostTrendChart />

        {/* Namespace cost table */}
        <div className="rounded-lg border border-theme-border bg-theme-surface/50">
          <div className="px-4 py-3 border-b border-theme-border">
            <div className="flex items-center justify-between">
              <span className="text-sm font-semibold text-theme-text-primary">Namespace Breakdown</span>
              <span className="text-xs text-theme-text-tertiary">{namespaces.length} namespaces</span>
            </div>
          </div>

          {/* Table header */}
          <div className="grid grid-cols-[minmax(180px,1fr)_90px_90px_60px_minmax(160px,1fr)_120px] gap-2 px-4 py-2 border-b border-theme-border text-[11px] font-medium text-theme-text-tertiary uppercase tracking-wider">
            <span>Namespace</span>
            <span className="text-right">Hourly</span>
            <span className="text-right">Monthly</span>
            <span className="text-right">Eff.</span>
            <span>CPU / Memory</span>
            <span className="text-right">Cost Split</span>
          </div>

          {/* Namespace rows */}
          <div className="divide-y divide-theme-border/50">
            {namespaces.map((ns) => (
              <NamespaceCostRow key={ns.name} ns={ns} maxCost={namespaces[0]?.hourlyCost ?? 0} hasStorage={hasStorage} />
            ))}
          </div>
        </div>

        {/* Node cost table */}
        {nodes.length > 0 && <NodeCostTable nodes={nodes} />}

        {/* Footer */}
        <div className="flex items-center justify-between text-xs text-theme-text-tertiary pb-4">
          <span>{data.currency ?? 'USD'} &middot; {data.window ?? '1h'} window &middot; Monthly estimates based on 730 hrs/mo</span>
          <span className="text-indigo-500 font-medium">Powered by OpenCost</span>
        </div>
      </div>
    </div>
  )
}

function NamespaceCostRow({ ns, maxCost, hasStorage }: { ns: OpenCostNamespaceCost; maxCost: number; hasStorage: boolean }) {
  const [expanded, setExpanded] = useState(false)
  const monthlyCost = ns.hourlyCost * 730
  const allocTotal = ns.cpuCost + ns.memoryCost + (ns.storageCost ?? 0)
  const cpuPct = allocTotal > 0 ? (ns.cpuCost / allocTotal) * 100 : 50
  const memPct = allocTotal > 0 ? (ns.memoryCost / allocTotal) * 100 : 50
  const barWidth = maxCost > 0 ? (ns.hourlyCost / maxCost) * 100 : 0
  const hasEff = (ns.efficiency ?? 0) > 0

  return (
    <div>
      <button
        onClick={() => setExpanded(!expanded)}
        className="w-full grid grid-cols-[minmax(180px,1fr)_90px_90px_60px_minmax(160px,1fr)_120px] gap-2 px-4 py-2.5 text-left hover:bg-theme-hover/50 transition-colors group"
      >
        <span className="flex items-center gap-1.5 min-w-0">
          {expanded ? (
            <ChevronDown className="w-3.5 h-3.5 text-theme-text-tertiary shrink-0" />
          ) : (
            <ChevronRight className="w-3.5 h-3.5 text-theme-text-tertiary shrink-0" />
          )}
          <span className="text-sm text-theme-text-primary truncate font-medium">{ns.name}</span>
        </span>
        <span className="text-sm text-theme-text-primary tabular-nums text-right">{formatCost(ns.hourlyCost)}</span>
        <span className="text-sm text-theme-text-secondary tabular-nums text-right">~{formatCost(monthlyCost)}</span>
        <span className="text-right">
          {hasEff ? (
            <span className={`text-xs font-medium tabular-nums ${efficiencyColor(ns.efficiency ?? 0)}`}>
              {(ns.efficiency ?? 0).toFixed(0)}%
            </span>
          ) : (
            <span className="text-xs text-theme-text-quaternary">-</span>
          )}
        </span>
        <span className="flex items-center gap-2">
          <div className="flex-1 h-2 rounded-full overflow-hidden bg-theme-hover flex" style={{ maxWidth: `${Math.max(barWidth, 3)}%` }}>
            <div className="h-full bg-blue-500/70" style={{ width: `${cpuPct}%` }} />
            <div className="h-full bg-purple-500/70" style={{ width: `${memPct}%` }} />
            {hasStorage && (ns.storageCost ?? 0) > 0 && (
              <div className="h-full bg-teal-500/70" style={{ width: `${100 - cpuPct - memPct}%` }} />
            )}
          </div>
        </span>
        <span className="text-[11px] text-theme-text-tertiary tabular-nums text-right">
          {formatCost(ns.cpuCost)} / {formatCost(ns.memoryCost)}
          {hasStorage && (ns.storageCost ?? 0) > 0 && ` / ${formatCost(ns.storageCost ?? 0)}`}
        </span>
      </button>

      {/* Expanded workload rows */}
      {expanded && (
        <WorkloadRows namespace={ns.name} />
      )}
    </div>
  )
}

function WorkloadRows({ namespace }: { namespace: string }) {
  const { data, isLoading } = useOpenCostWorkloads(namespace)

  if (isLoading) {
    return (
      <div className="px-4 py-3 flex items-center gap-2 text-xs text-theme-text-tertiary bg-theme-elevated/30">
        <Loader2 className="w-3.5 h-3.5 animate-spin" />
        Loading workloads...
      </div>
    )
  }

  const workloads = data?.workloads ?? []
  if (workloads.length === 0) {
    return (
      <div className="px-4 py-3 text-xs text-theme-text-tertiary bg-theme-elevated/30 pl-10">
        No workload cost data available
      </div>
    )
  }

  return (
    <div className="bg-theme-elevated/30 border-t border-theme-border/30">
      {workloads.map((wl) => (
        <WorkloadCostRow key={`${wl.kind}-${wl.name}`} wl={wl} maxCost={workloads[0]?.hourlyCost ?? 0} />
      ))}
    </div>
  )
}

function WorkloadCostRow({ wl, maxCost }: { wl: OpenCostWorkloadCost; maxCost: number }) {
  const monthlyCost = wl.hourlyCost * 730
  const cpuPct = wl.hourlyCost > 0 ? (wl.cpuCost / wl.hourlyCost) * 100 : 50
  const barWidth = maxCost > 0 ? (wl.hourlyCost / maxCost) * 100 : 0
  const hasEff = (wl.efficiency ?? 0) > 0

  return (
    <div className="grid grid-cols-[minmax(180px,1fr)_90px_90px_60px_minmax(160px,1fr)_120px] gap-2 px-4 py-2 text-left">
      <span className="flex items-center gap-1.5 min-w-0 pl-5">
        <span className="text-[10px] text-theme-text-tertiary bg-theme-surface px-1 py-0.5 rounded shrink-0">{wl.kind}</span>
        <span className="text-xs text-theme-text-secondary truncate">{wl.name}</span>
        {wl.replicas > 1 && (
          <span className="text-[10px] text-theme-text-tertiary shrink-0">{wl.replicas}x</span>
        )}
      </span>
      <span className="text-xs text-theme-text-secondary tabular-nums text-right">{formatCost(wl.hourlyCost)}</span>
      <span className="text-xs text-theme-text-tertiary tabular-nums text-right">~{formatCost(monthlyCost)}</span>
      <span className="text-right">
        {hasEff ? (
          <span className={`text-[10px] font-medium tabular-nums ${efficiencyColor(wl.efficiency ?? 0)}`}>
            {(wl.efficiency ?? 0).toFixed(0)}%
          </span>
        ) : (
          <span className="text-[10px] text-theme-text-quaternary">-</span>
        )}
      </span>
      <span className="flex items-center gap-2">
        <div className="flex-1 h-1.5 rounded-full overflow-hidden bg-theme-hover flex" style={{ maxWidth: `${Math.max(barWidth, 3)}%` }}>
          <div className="h-full bg-blue-500/50" style={{ width: `${cpuPct}%` }} />
          <div className="h-full bg-purple-500/50" style={{ width: `${100 - cpuPct}%` }} />
        </div>
      </span>
      <span className="text-[10px] text-theme-text-tertiary tabular-nums text-right">
        {formatCost(wl.cpuCost)} / {formatCost(wl.memoryCost)}
      </span>
    </div>
  )
}

function NodeCostTable({ nodes }: { nodes: OpenCostNodeCost[] }) {
  return (
    <div className="rounded-lg border border-theme-border bg-theme-surface/50">
      <div className="px-4 py-3 border-b border-theme-border">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2">
            <Server className="w-4 h-4 text-theme-text-tertiary" />
            <span className="text-sm font-semibold text-theme-text-primary">Node Costs</span>
          </div>
          <span className="text-xs text-theme-text-tertiary">{nodes.length} nodes</span>
        </div>
      </div>

      {/* Table header */}
      <div className="grid grid-cols-[minmax(200px,1fr)_minmax(120px,1fr)_90px_100px_140px] gap-2 px-4 py-2 border-b border-theme-border text-[11px] font-medium text-theme-text-tertiary uppercase tracking-wider">
        <span>Node</span>
        <span>Instance Type</span>
        <span className="text-right">Hourly</span>
        <span className="text-right">Monthly</span>
        <span className="text-right">CPU / Memory</span>
      </div>

      {/* Node rows */}
      <div className="divide-y divide-theme-border/50">
        {nodes.map((node) => (
          <NodeCostRow key={node.name} node={node} />
        ))}
      </div>
    </div>
  )
}

function NodeCostRow({ node }: { node: OpenCostNodeCost }) {
  const monthlyCost = node.hourlyCost * 730

  return (
    <div className="grid grid-cols-[minmax(200px,1fr)_minmax(120px,1fr)_90px_100px_140px] gap-2 px-4 py-2.5">
      <span className="text-sm text-theme-text-primary truncate font-medium" title={node.name}>
        {node.name}
      </span>
      <span className="text-xs text-theme-text-secondary truncate">
        {node.instanceType || '-'}
        {node.region && <span className="text-theme-text-quaternary ml-1.5">({node.region})</span>}
      </span>
      <span className="text-sm text-theme-text-primary tabular-nums text-right">{formatCost(node.hourlyCost)}</span>
      <span className="text-sm text-theme-text-secondary tabular-nums text-right">~{formatCost(monthlyCost)}</span>
      <span className="text-[11px] text-theme-text-tertiary tabular-nums text-right">
        {formatCost(node.cpuCost)} / {formatCost(node.memoryCost)}
      </span>
    </div>
  )
}

function efficiencyColor(efficiency: number): string {
  if (efficiency >= 70) return 'text-emerald-400'
  if (efficiency >= 40) return 'text-amber-400'
  return 'text-red-400'
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
