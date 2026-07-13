import { useState, useEffect } from 'react'
import { useLocation } from 'react-router-dom'
import {
  COST_DISCOVERY_GRACE_MS,
  useClusterInfo,
  useOpenCostSummary,
  useOpenCostWorkloads,
  useOpenCostNodes,
} from '../../api/client'
import type {
  OpenCostNamespaceCost,
  OpenCostWorkloadCost,
  OpenCostNodeCost,
} from '../../api/client'
import {
  ChevronDown,
  ChevronRight,
  DollarSign,
  ExternalLink,
  HelpCircle,
  Loader2,
  Server,
  X,
} from 'lucide-react'
import { PaneLoader, FreshnessControl, PageHeader } from '@skyhook-io/k8s-ui'
import { CostTrendChart } from './CostTrendChart'
import {
  COST_HOURS_PER_MONTH,
  formatCostPerHour,
  formatProjectedDailyRate,
  formatProjectedMonthlyCost,
  formatProjectedMonthlyRate,
} from './format'
import { Tooltip } from '../ui/Tooltip'
import { useConnection } from '../../context/ConnectionContext'
import type { SelectedResource } from '../../types'
import { kindToPlural, openExternal } from '../../utils/navigation'
import { clusterCloudConsoleLink, nodeCloudConsoleLink } from './cloud-console'
import { RightsizingScanView } from '../rightsizing/RightsizingScanView'
import { CostViewTabs } from './CostViewTabs'

interface CostViewProps {
  onBack: () => void
  onOpenResource?: (resource: SelectedResource) => void
  namespaces: string[]
}

const SYSTEM_COST_NAMESPACES = new Set(['kube-system', 'kube-public', 'kube-node-lease'])

export function CostView(props: CostViewProps) {
  const { pathname } = useLocation()
  if (pathname.startsWith('/cost/rightsizing')) {
    return <RightsizingScanView namespaces={props.namespaces} />
  }
  return <CostOverview {...props} />
}

function CostOverview({ onBack, onOpenResource }: CostViewProps) {
  const { data, isLoading, isFetching, dataUpdatedAt, refetch } = useOpenCostSummary()
  const { data: nodeData } = useOpenCostNodes()
  const { data: clusterInfo } = useClusterInfo()
  const { connection } = useConnection()
  const [showHelp, setShowHelp] = useState(false)
  const [noPrometheusSince, setNoPrometheusSince] = useState<number | null>(null)

  useEffect(() => {
    if (data?.available === false && data.reason === 'no_prometheus') {
      setNoPrometheusSince((prev) => prev ?? Date.now())
    } else {
      setNoPrometheusSince(null)
    }
  }, [data?.available, data?.reason])

  if (isLoading) {
    return (
      <CostOverviewState>
        <PaneLoader label="Loading cost data…" className="min-h-64" />
      </CostOverviewState>
    )
  }

  if (!data || !data.available) {
    const reason = data?.reason
    const discoveryAgeMs = noPrometheusSince == null ? 0 : Date.now() - noPrometheusSince
    if (reason === 'no_prometheus' && discoveryAgeMs < COST_DISCOVERY_GRACE_MS) {
      return (
        <CostOverviewState>
          <div className="flex min-h-64 items-center justify-center">
            <div className="flex max-w-md flex-col items-center gap-3 text-center text-theme-text-secondary">
              <Loader2 className="w-8 h-8 animate-spin text-theme-text-tertiary/60" />
              <div>
                <p className="text-sm font-medium text-theme-text-primary">
                  Looking for Prometheus cost data…
                </p>
                <p className="mt-1 text-xs text-theme-text-tertiary">
                  First discovery can take a few seconds while Radar checks cluster services and
                  opens a local port-forward.
                </p>
              </div>
              <button
                onClick={() => {
                  setNoPrometheusSince(Date.now())
                  refetch()
                }}
                disabled={isFetching}
                className="text-xs text-accent-text hover:text-theme-text-primary disabled:cursor-not-allowed disabled:text-theme-text-disabled transition-colors"
              >
                {isFetching ? 'Checking…' : 'Check again'}
              </button>
            </div>
          </div>
        </CostOverviewState>
      )
    }
    const message =
      reason === 'no_prometheus'
        ? 'Prometheus not found — OpenCost requires Prometheus or VictoriaMetrics'
        : reason === 'no_metrics'
          ? 'OpenCost metrics not found — Prometheus is available but no cost metrics were detected'
          : reason === 'query_error'
            ? 'Cost data temporarily unavailable — Prometheus was found but queries failed'
            : 'OpenCost not detected — install OpenCost for cost visibility'

    return (
      <CostOverviewState>
        <div className="flex min-h-64 items-center justify-center">
          <div className="flex flex-col items-center gap-3 text-theme-text-secondary">
            <DollarSign className="w-8 h-8 text-theme-text-tertiary/40" />
            <p className="text-sm">{message}</p>
            <button
              onClick={onBack}
              className="text-xs text-skyhook-400 hover:text-skyhook-300 transition-colors"
            >
              Back to Dashboard
            </button>
            {reason === 'no_prometheus' && (
              <button
                onClick={() => {
                  setNoPrometheusSince(Date.now())
                  refetch()
                }}
                disabled={isFetching}
                className="text-xs text-accent-text hover:text-theme-text-primary disabled:cursor-not-allowed disabled:text-theme-text-disabled transition-colors"
              >
                {isFetching ? 'Checking…' : 'Check again'}
              </button>
            )}
          </div>
        </div>
      </CostOverviewState>
    )
  }

  const hourlyCost = data.totalHourlyCost ?? 0
  const namespaces = data.namespaces ?? []
  const totalCpu = namespaces.reduce((sum, ns) => sum + ns.cpuCost, 0)
  const totalMem = namespaces.reduce((sum, ns) => sum + ns.memoryCost, 0)
  const totalStorage = data.totalStorageCost ?? 0
  const hasStorage = totalStorage > 0
  const hasSystemNamespaces = namespaces.some((ns) => isSystemCostNamespace(ns.name))

  // Compute split percentages (CPU + Memory + optional Storage)
  const allocTotal = totalCpu + totalMem + totalStorage
  const cpuPct = allocTotal > 0 ? (totalCpu / allocTotal) * 100 : 50
  const memPct = allocTotal > 0 ? (totalMem / allocTotal) * 100 : 50
  const storagePct = allocTotal > 0 ? (totalStorage / allocTotal) * 100 : 0

  const nodes = nodeData?.available ? (nodeData.nodes ?? []) : []
  const clusterConsoleLink = clusterCloudConsoleLink(clusterInfo?.context)

  return (
    <div className="flex-1 overflow-y-auto">
      <div className="mx-auto w-full max-w-[1920px] px-6 py-6 space-y-6">
        {/* Header */}
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-3">
            <div className="flex items-center gap-2">
              <DollarSign className="w-5 h-5 text-indigo-500" />
              <h1 className="text-lg font-semibold text-theme-text-primary">Cost Insights</h1>
            </div>
            <span className="text-theme-text-quaternary">·</span>
            <button
              onClick={() => setShowHelp(true)}
              className="flex items-center gap-1 text-xs text-theme-text-tertiary hover:text-indigo-400 cursor-help transition-colors duration-150"
            >
              <HelpCircle className="w-3.5 h-3.5" />
              How this works
            </button>
            {clusterConsoleLink && (
              <Tooltip content={clusterConsoleLink.label}>
                <button
                  type="button"
                  onClick={() => openExternal(clusterConsoleLink.url)}
                  className="flex items-center gap-1 text-xs text-theme-text-tertiary hover:text-accent-text transition-colors duration-150"
                  aria-label={clusterConsoleLink.label}
                >
                  <ExternalLink className="w-3.5 h-3.5" />
                  Cloud console
                </button>
              </Tooltip>
            )}
          </div>
          <div className="flex items-center gap-4">
            {/* Tracks the headline monthly summary (the primary query); its load
                time is the representative freshness signal for the view. */}
            <FreshnessControl
              mode="auto"
              dataUpdatedAt={dataUpdatedAt}
              onRefresh={() => refetch()}
              connectionState={connection.state}
            />
            <div className="flex flex-col items-end">
              <div className="flex items-baseline gap-1">
                <span className="text-2xl font-bold text-theme-text-primary tabular-nums">
                  {formatProjectedMonthlyCost(hourlyCost)}
                </span>
                <span className="text-xs text-theme-text-tertiary">/mo</span>
              </div>
              <div className="mt-0.5 flex items-baseline gap-2 text-theme-text-secondary">
                <span className="text-sm font-medium tabular-nums">
                  {formatProjectedDailyRate(hourlyCost)}
                </span>
                <span className="text-[10px] text-theme-text-quaternary">·</span>
                <span className="text-sm font-medium tabular-nums">
                  {formatCostPerHour(hourlyCost)}
                </span>
              </div>
              <span className="text-[10px] text-theme-text-quaternary">
                projected from last 1h average
              </span>
            </div>
          </div>
        </div>

        <CostViewTabs />

        {/* CPU vs Memory (vs Storage) split bar */}
        <div className="rounded-lg border border-theme-border bg-theme-surface/50 p-4">
          <div className="flex items-center justify-between mb-2">
            <span className="text-xs font-medium text-theme-text-secondary">
              Cluster Resource Cost
            </span>
            <div className="flex items-center gap-4 text-xs text-theme-text-tertiary">
              <span className="flex items-center gap-1.5">
                <span className="w-2.5 h-2.5 rounded-sm bg-accent" />
                CPU {formatProjectedMonthlyRate(totalCpu)}
              </span>
              <span className="flex items-center gap-1.5">
                <span className="w-2.5 h-2.5 rounded-sm bg-amber-500" />
                Memory {formatProjectedMonthlyRate(totalMem)}
              </span>
              {hasStorage && (
                <span className="flex items-center gap-1.5">
                  <span className="w-2.5 h-2.5 rounded-sm bg-teal-500" />
                  Storage {formatProjectedMonthlyRate(totalStorage)}
                </span>
              )}
            </div>
          </div>
          <div className="h-3 rounded-full overflow-hidden bg-theme-hover flex">
            <div
              className="h-full bg-accent transition-all duration-300"
              style={{ width: `${cpuPct}%` }}
            />
            <div
              className="h-full bg-amber-500 transition-all duration-300"
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
              <div>
                <span className="text-sm font-semibold text-theme-text-primary">
                  Namespace Breakdown
                </span>
                <span className="text-[10px] text-theme-text-quaternary ml-2">
                  projected monthly from current rate
                </span>
              </div>
              <span className="text-xs text-theme-text-tertiary">
                {namespaces.length} namespaces
              </span>
            </div>
          </div>
          {hasSystemNamespaces && <SystemNamespacesCostNote />}

          {/* Table header */}
          <div className="grid grid-cols-[minmax(180px,1fr)_110px_90px_minmax(160px,1fr)_150px] gap-2 px-4 py-2 border-b border-theme-border text-[11px] font-medium text-theme-text-tertiary uppercase tracking-wider">
            <span>Namespace</span>
            <Tooltip
              content="Projected from current hourly rate — not historical spend"
              wrapperClassName="!block text-right"
            >
              <span className="cursor-help">Projected/mo*</span>
            </Tooltip>
            <span className="text-right">Hourly</span>
            <span>CPU / Memory</span>
            <Tooltip
              content="Projected monthly CPU and memory allocation from the current hourly rate"
              wrapperClassName="!block text-right"
            >
              <span className="cursor-help">CPU / Memory/mo*</span>
            </Tooltip>
          </div>

          {/* Namespace rows */}
          <div className="divide-y divide-theme-border/50">
            {namespaces.map((ns) => (
              <NamespaceCostRow
                key={ns.name}
                ns={ns}
                maxCost={namespaces[0]?.hourlyCost ?? 0}
                hasStorage={hasStorage}
                onOpenResource={onOpenResource}
              />
            ))}
          </div>
        </div>

        {/* Node cost table */}
        {nodes.length > 0 && <NodeCostTable nodes={nodes} onOpenResource={onOpenResource} />}

        {/* Footer */}
        <div className="flex items-center justify-between text-xs text-theme-text-tertiary pb-4">
          <span>
            {data.currency ?? 'USD'} &middot; current rates based on last 1h average &middot;
            *monthly projections assume {COST_HOURS_PER_MONTH} hrs/mo
          </span>
          <span className="text-indigo-500 font-medium">Powered by OpenCost</span>
        </div>
      </div>

      {/* Help dialog */}
      {showHelp && <CostHelpDialog onClose={() => setShowHelp(false)} />}
    </div>
  )
}

function CostOverviewState({ children }: { children: React.ReactNode }) {
  return (
    <div className="flex-1 overflow-y-auto">
      <div className="mx-auto flex w-full max-w-[1920px] flex-col gap-4 px-6 py-6">
        <PageHeader
          icon={DollarSign}
          title="Cost Insights"
          description="Understand current allocation and find CPU and memory requests worth tuning."
        />
        <CostViewTabs />
        {children}
      </div>
    </div>
  )
}

function NamespaceCostRow({
  ns,
  maxCost,
  hasStorage,
  onOpenResource,
}: {
  ns: OpenCostNamespaceCost
  maxCost: number
  hasStorage: boolean
  onOpenResource?: (resource: SelectedResource) => void
}) {
  const [expanded, setExpanded] = useState(false)
  const allocTotal = ns.cpuCost + ns.memoryCost + (ns.storageCost ?? 0)
  const cpuPct = allocTotal > 0 ? (ns.cpuCost / allocTotal) * 100 : 50
  const memPct = allocTotal > 0 ? (ns.memoryCost / allocTotal) * 100 : 50
  const barWidth = maxCost > 0 ? (ns.hourlyCost / maxCost) * 100 : 0
  return (
    <div>
      <button
        onClick={() => setExpanded(!expanded)}
        className="w-full grid grid-cols-[minmax(180px,1fr)_110px_90px_minmax(160px,1fr)_150px] gap-2 px-4 py-2.5 text-left hover:bg-theme-hover/50 transition-colors group"
      >
        <span className="flex items-center gap-1.5 min-w-0">
          {expanded ? (
            <ChevronDown className="w-3.5 h-3.5 text-theme-text-tertiary shrink-0" />
          ) : (
            <ChevronRight className="w-3.5 h-3.5 text-theme-text-tertiary shrink-0" />
          )}
          <Tooltip content={`Namespace ${ns.name}`} wrapperClassName="min-w-0">
            <span className="block truncate text-sm text-theme-text-primary font-medium">
              {ns.name}
            </span>
          </Tooltip>
          {isSystemCostNamespace(ns.name) && (
            <Tooltip
              content="Usually platform overhead from Kubernetes and cluster add-ons. Review enabled add-ons before treating this as an application optimization target."
              wrapperClassName="shrink-0"
            >
              <span className="rounded bg-theme-elevated px-1.5 py-0.5 text-[10px] font-medium text-theme-text-tertiary">
                system
              </span>
            </Tooltip>
          )}
        </span>
        <span className="text-sm font-medium text-theme-text-primary tabular-nums text-right">
          {formatProjectedMonthlyCost(ns.hourlyCost)}
        </span>
        <span className="text-sm text-theme-text-secondary tabular-nums text-right">
          {formatCostPerHour(ns.hourlyCost)}
        </span>
        <span className="flex items-center gap-2">
          <div
            className="flex-1 h-2 rounded-full overflow-hidden bg-theme-hover flex"
            style={{ maxWidth: `${Math.max(barWidth, 3)}%` }}
          >
            <div className="h-full bg-accent" style={{ width: `${cpuPct}%` }} />
            <div className="h-full bg-amber-500" style={{ width: `${memPct}%` }} />
            {hasStorage && (ns.storageCost ?? 0) > 0 && (
              <div className="h-full bg-teal-500" style={{ width: `${100 - cpuPct - memPct}%` }} />
            )}
          </div>
        </span>
        <span className="text-[11px] text-theme-text-tertiary tabular-nums text-right">
          {formatProjectedMonthlyCost(ns.cpuCost)} / {formatProjectedMonthlyCost(ns.memoryCost)}
          {hasStorage &&
            (ns.storageCost ?? 0) > 0 &&
            ` / ${formatProjectedMonthlyCost(ns.storageCost ?? 0)}`}
        </span>
      </button>

      {/* Expanded workload rows */}
      {expanded && isSystemCostNamespace(ns.name) && (
        <SystemNamespaceCostNote namespace={ns.name} />
      )}
      {expanded && <WorkloadRows namespace={ns.name} onOpenResource={onOpenResource} />}
    </div>
  )
}

function isSystemCostNamespace(namespace: string): boolean {
  return SYSTEM_COST_NAMESPACES.has(namespace)
}

function SystemNamespaceCostNote({ namespace }: { namespace: string }) {
  return (
    <div className="border-t border-theme-border/30 bg-theme-elevated/30 px-10 py-2 text-xs text-theme-text-tertiary">
      <span className="font-medium text-theme-text-secondary">{namespace}</span> is usually baseline
      cluster overhead: Kubernetes components, cloud-provider agents, and installed add-ons.
      Optimize by reviewing add-ons, logging, monitoring, or node count rather than deleting system
      workloads directly.
    </div>
  )
}

function SystemNamespacesCostNote() {
  return (
    <div className="border-b border-theme-border/50 bg-theme-elevated/30 px-4 py-2 text-xs text-theme-text-tertiary">
      Rows marked <span className="font-medium text-theme-text-secondary">system</span> are usually
      baseline Kubernetes, cloud-provider, or add-on overhead. Optimize by reviewing enabled
      add-ons, telemetry, or node count before treating them as application optimization targets.
    </div>
  )
}

function WorkloadRows({
  namespace,
  onOpenResource,
}: {
  namespace: string
  onOpenResource?: (resource: SelectedResource) => void
}) {
  const { data, isLoading } = useOpenCostWorkloads(namespace)

  if (isLoading) {
    return (
      <div className="px-4 py-3 flex items-center gap-2 text-xs text-theme-text-tertiary bg-theme-elevated/30">
        <Loader2 className="w-3.5 h-3.5 animate-spin" />
        Loading workloads…
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
        <WorkloadCostRow
          key={`${wl.kind}-${wl.name}`}
          wl={wl}
          namespace={namespace}
          maxCost={workloads[0]?.hourlyCost ?? 0}
          onOpenResource={onOpenResource}
        />
      ))}
    </div>
  )
}

function WorkloadCostRow({
  wl,
  namespace,
  maxCost,
  onOpenResource,
}: {
  wl: OpenCostWorkloadCost
  namespace: string
  maxCost: number
  onOpenResource?: (resource: SelectedResource) => void
}) {
  const cpuPct = wl.hourlyCost > 0 ? (wl.cpuCost / wl.hourlyCost) * 100 : 50
  const barWidth = maxCost > 0 ? (wl.hourlyCost / maxCost) * 100 : 0
  const kindLabel = displayCostWorkloadKind(wl.kind)
  const resource = costWorkloadResource(wl, namespace)
  const canOpen = Boolean(resource && onOpenResource)
  const content = (
    <>
      <span className="flex items-center gap-1.5 min-w-0 pl-5">
        <span className="text-[10px] text-theme-text-tertiary bg-theme-surface px-1 py-0.5 rounded shrink-0">
          {kindLabel}
        </span>
        <Tooltip content={`${kindLabel} ${namespace}/${wl.name}`} wrapperClassName="min-w-0">
          <span className="block truncate text-xs text-theme-text-secondary">{wl.name}</span>
        </Tooltip>
        {wl.replicas > 1 && (
          <span className="text-[10px] text-theme-text-tertiary shrink-0">{wl.replicas}x</span>
        )}
      </span>
      <span className="text-xs font-medium text-theme-text-secondary tabular-nums text-right">
        {formatProjectedMonthlyCost(wl.hourlyCost)}
      </span>
      <span className="text-xs text-theme-text-tertiary tabular-nums text-right">
        {formatCostPerHour(wl.hourlyCost)}
      </span>
      <span className="flex items-center gap-2">
        <div
          className="flex-1 h-1.5 rounded-full overflow-hidden bg-theme-hover flex"
          style={{ maxWidth: `${Math.max(barWidth, 3)}%` }}
        >
          <div className="h-full bg-accent" style={{ width: `${cpuPct}%` }} />
          <div className="h-full bg-amber-500" style={{ width: `${100 - cpuPct}%` }} />
        </div>
      </span>
      <span className="text-[10px] text-theme-text-tertiary tabular-nums text-right">
        {formatProjectedMonthlyCost(wl.cpuCost)} / {formatProjectedMonthlyCost(wl.memoryCost)}
      </span>
    </>
  )

  const rowClass =
    'grid grid-cols-[minmax(180px,1fr)_110px_90px_minmax(160px,1fr)_150px] gap-2 px-4 py-2 text-left'

  if (canOpen && resource) {
    return (
      <button
        type="button"
        onClick={() => onOpenResource?.(resource)}
        className={`${rowClass} w-full transition-colors hover:bg-theme-hover/60 focus:outline-none focus:ring-2 focus:ring-accent focus:ring-inset`}
        aria-label={`Open ${kindLabel} ${wl.name}`}
      >
        {content}
      </button>
    )
  }

  return <div className={rowClass}>{content}</div>
}

function NodeCostTable({
  nodes,
  onOpenResource,
}: {
  nodes: OpenCostNodeCost[]
  onOpenResource?: (resource: SelectedResource) => void
}) {
  return (
    <div className="rounded-lg border border-theme-border bg-theme-surface/50">
      <div className="px-4 py-3 border-b border-theme-border">
        <div className="flex items-center justify-between">
          <div>
            <div className="flex items-center gap-2">
              <Server className="w-4 h-4 text-theme-text-tertiary" />
              <span className="text-sm font-semibold text-theme-text-primary">Node Costs</span>
              <span className="text-[10px] text-theme-text-quaternary">current pricing</span>
            </div>
            <p className="text-[11px] text-theme-text-tertiary mt-0.5 ml-6">
              Per-machine cloud pricing — namespace costs above show how this capacity is allocated
            </p>
          </div>
          <span className="text-xs text-theme-text-tertiary">{nodes.length} nodes</span>
        </div>
      </div>

      {/* Table header */}
      <div className="grid grid-cols-[minmax(200px,1fr)_minmax(120px,1fr)_110px_90px_150px] gap-2 px-4 py-2 border-b border-theme-border text-[11px] font-medium text-theme-text-tertiary uppercase tracking-wider">
        <span>Node</span>
        <span>Instance Type</span>
        <Tooltip
          content="Projected from current hourly rate — not historical spend"
          wrapperClassName="!block text-right"
        >
          <span className="cursor-help">Projected/mo*</span>
        </Tooltip>
        <span className="text-right">Hourly</span>
        <Tooltip
          content="Projected monthly CPU and memory portions of this node's current price"
          wrapperClassName="!block text-right"
        >
          <span className="cursor-help">CPU / Memory/mo*</span>
        </Tooltip>
      </div>

      {/* Node rows */}
      <div className="divide-y divide-theme-border/50">
        {nodes.map((node) => (
          <NodeCostRow key={node.name} node={node} onOpenResource={onOpenResource} />
        ))}
      </div>
    </div>
  )
}

function NodeCostRow({
  node,
  onOpenResource,
}: {
  node: OpenCostNodeCost
  onOpenResource?: (resource: SelectedResource) => void
}) {
  const cloudLink = nodeCloudConsoleLink(node.providerID)
  const openNode = () => onOpenResource?.({ kind: 'nodes', namespace: '', name: node.name })

  return (
    <div className="grid grid-cols-[minmax(200px,1fr)_minmax(120px,1fr)_110px_90px_150px] gap-2 px-4 py-2.5">
      <span className="flex min-w-0 items-center gap-1.5">
        <Tooltip content={`Open Node ${node.name}`} wrapperClassName="!block min-w-0">
          {onOpenResource ? (
            <button
              type="button"
              onClick={openNode}
              className="block min-w-0 truncate text-left text-sm font-medium text-theme-text-primary transition-colors hover:text-accent-text focus:outline-none focus:ring-2 focus:ring-accent focus:ring-offset-1 focus:ring-offset-theme-base"
            >
              {node.name}
            </button>
          ) : (
            <span className="text-sm text-theme-text-primary truncate font-medium block">
              {node.name}
            </span>
          )}
        </Tooltip>
        {cloudLink && (
          <Tooltip content={cloudLink.label}>
            <button
              type="button"
              onClick={() => openExternal(cloudLink.url)}
              className="shrink-0 rounded p-0.5 text-theme-text-tertiary transition-colors hover:bg-theme-hover hover:text-accent-text focus:outline-none focus:ring-2 focus:ring-accent focus:ring-offset-1 focus:ring-offset-theme-base"
              aria-label={`${cloudLink.label}: ${node.name}`}
            >
              <ExternalLink className="h-3.5 w-3.5" />
            </button>
          </Tooltip>
        )}
      </span>
      <span className="text-xs text-theme-text-secondary truncate">
        {node.instanceType || '-'}
        {node.region && <span className="text-theme-text-quaternary ml-1.5">({node.region})</span>}
      </span>
      <span className="text-sm font-medium text-theme-text-primary tabular-nums text-right">
        {formatProjectedMonthlyCost(node.hourlyCost)}
      </span>
      <span className="text-sm text-theme-text-secondary tabular-nums text-right">
        {formatCostPerHour(node.hourlyCost)}
      </span>
      <span className="text-[11px] text-theme-text-tertiary tabular-nums text-right">
        {formatProjectedMonthlyCost(node.cpuCost)} / {formatProjectedMonthlyCost(node.memoryCost)}
      </span>
    </div>
  )
}

function displayCostWorkloadKind(kind: string): string {
  if (kind === 'standalone') return 'pod'
  if (kind === 'staticpod') return 'static pod'
  return kind
}

function costWorkloadResource(
  wl: OpenCostWorkloadCost,
  namespace: string,
): SelectedResource | null {
  const kind = resourceKindForCostWorkload(wl.kind)
  if (!kind || !wl.name) return null
  return {
    kind: kindToPlural(kind),
    namespace: kind === 'Node' ? '' : namespace,
    name: wl.name,
    group: apiGroupForCostWorkload(kind),
  }
}

export function resourceKindForCostWorkload(kind: string): string | null {
  if (kind === 'staticpod') return 'Pod'
  if (kind === 'standalone') return null
  if (kind === 'Deployment' || kind === 'StatefulSet' || kind === 'DaemonSet') return kind
  if (kind === 'Job' || kind === 'CronJob') return kind
  if (kind === 'Node') return kind
  return null
}

function apiGroupForCostWorkload(kind: string): string | undefined {
  if (kind === 'Deployment' || kind === 'StatefulSet' || kind === 'DaemonSet') return 'apps'
  if (kind === 'Job' || kind === 'CronJob') return 'batch'
  return undefined
}

// --- Help dialog ---

function CostHelpDialog({ onClose }: { onClose: () => void }) {
  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose()
    }
    document.addEventListener('keydown', handleKeyDown)
    return () => document.removeEventListener('keydown', handleKeyDown)
  }, [onClose])

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center">
      <div className="absolute inset-0 bg-black/60 backdrop-blur-sm" onClick={onClose} />
      <div className="relative dialog max-w-2xl w-full mx-4 max-h-[80vh] overflow-y-auto">
        {/* Header */}
        <div className="flex items-center justify-between p-4 border-b border-theme-border sticky top-0 bg-theme-surface rounded-t-lg">
          <div className="flex items-center gap-2">
            <HelpCircle className="w-5 h-5 text-indigo-500" />
            <h2 className="text-base font-semibold text-theme-text-primary">
              Understanding Cost Data
            </h2>
          </div>
          <button
            onClick={onClose}
            className="p-1 text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated rounded"
          >
            <X className="w-5 h-5" />
          </button>
        </div>

        <div className="p-4 space-y-5 text-sm text-theme-text-secondary">
          {/* Where costs come from */}
          <section>
            <h3 className="text-sm font-semibold text-theme-text-primary mb-1.5">
              Where do these costs come from?
            </h3>
            <p>
              Cost data comes from <strong>OpenCost</strong>, an open-source tool that combines your
              cloud provider's pricing (how much each node costs per hour) with Kubernetes resource
              allocation data. This gives you a dollar value for each workload running on your
              cluster.
            </p>
          </section>

          {/* What costs represent */}
          <section>
            <h3 className="text-sm font-semibold text-theme-text-primary mb-1.5">
              What do monthly, daily, and hourly cost mean?
            </h3>
            <p>
              OpenCost assigns CPU and memory allocation using the greater of a workload's request
              or observed use, then applies the node's cloud pricing. Allocation cost is therefore
              useful for attribution, but it is not a direct measurement of request headroom.
            </p>
            <p className="mt-1.5">
              Projected monthly and daily numbers multiply the current hourly allocation rate. They
              are useful for budget impact, but they are not a historical invoice total. Historical
              spend on application and workload tabs uses the selected range.
            </p>
          </section>

          {/* Time context */}
          <section>
            <h3 className="text-sm font-semibold text-theme-text-primary mb-1.5">
              How fresh is this data?
            </h3>
            <p>
              Cost rates and breakdowns are <strong>snapshots based on the last 1 hour</strong> of
              data. They update automatically every minute. The trend chart shows historical hourly
              allocation rate over the selected time range (6 hours, 24 hours, or 7 days).
            </p>
            <p className="mt-1.5">
              Because costs are based on a 1-hour window, short-lived spikes or dips may not be
              reflected. The trend chart gives you the longer-term rate picture.
            </p>
          </section>

          {/* Node costs */}
          <section>
            <h3 className="text-sm font-semibold text-theme-text-primary mb-1.5">
              What are node costs?
            </h3>
            <p>
              Node costs show the hourly price of each machine in your cluster, based on instance
              type and cloud pricing. This is the total capacity cost — the namespace and workload
              breakdowns above show how that capacity is allocated across your workloads.
            </p>
          </section>
        </div>
      </div>
    </div>
  )
}
