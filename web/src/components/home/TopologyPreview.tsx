import { useMemo } from 'react'
import type { Topology } from '../../types'
import type { DashboardTopologySummary } from '../../api/client'
import { Network, ArrowRight } from 'lucide-react'
import { clsx } from 'clsx'

interface TopologyPreviewProps {
  topology: Topology | null
  summary: DashboardTopologySummary
  onNavigate: () => void
}

/**
 * Static schematic SVG that hints at the topology graph layout.
 * Two groups with interconnected nodes using real topology color palette.
 */
function TopologySchematic() {
  // Colors matching the real topology graph
  const ingress = '#a78bfa'  // violet-400
  const service = '#60a5fa'  // blue-400
  const deploy  = '#34d399'  // emerald-400
  const pod     = '#84cc16'  // lime-500
  const config  = '#fbbf24'  // amber-400
  const groupBorder = 'rgba(59, 130, 246, 0.25)'
  const groupBg = 'rgba(59, 130, 246, 0.04)'

  // Edge color — use a concrete color so markers render reliably
  const ec = '#64748b' // slate-500

  return (
    <svg viewBox="0 0 160 120" className="w-full h-full" aria-hidden="true">
      <defs>
        <marker id="arr" viewBox="0 0 8 6" refX="7" refY="3"
          markerWidth="6" markerHeight="5" orient="auto">
          <path d="M 0 0 L 8 3 L 0 6 z" fill={ec} opacity="0.6" />
        </marker>
        <marker id="arr-f" viewBox="0 0 8 6" refX="7" refY="3"
          markerWidth="6" markerHeight="5" orient="auto">
          <path d="M 0 0 L 8 3 L 0 6 z" fill={ec} opacity="0.35" />
        </marker>
      </defs>

      {/* Group 1 — top: Ingress → Service → Deploys → Pods */}
      <rect x="4" y="4" width="100" height="52" rx="6"
        fill={groupBg} stroke={groupBorder} strokeWidth="1" strokeDasharray="3 2" />

      {/* Nodes */}
      <rect x="10" y="24" width="16" height="10" rx="3" fill={ingress} opacity="0.85" />
      <rect x="38" y="24" width="16" height="10" rx="3" fill={service} opacity="0.85" />
      <rect x="66" y="14" width="16" height="10" rx="3" fill={deploy} opacity="0.85" />
      <rect x="66" y="34" width="16" height="10" rx="3" fill={deploy} opacity="0.85" />
      <circle cx="96" cy="19" r="4.5" fill={pod} opacity="0.65" />
      <circle cx="96" cy="39" r="4.5" fill={pod} opacity="0.65" />

      {/* Edges — group 1 */}
      <line x1="26" y1="29" x2="37" y2="29" stroke={ec} strokeWidth="1" opacity="0.55" markerEnd="url(#arr)" />
      <line x1="54" y1="29" x2="65" y2="20" stroke={ec} strokeWidth="1" opacity="0.55" markerEnd="url(#arr)" />
      <line x1="54" y1="29" x2="65" y2="38" stroke={ec} strokeWidth="1" opacity="0.55" markerEnd="url(#arr)" />
      <line x1="82" y1="19" x2="90" y2="19" stroke={ec} strokeWidth="1" opacity="0.55" markerEnd="url(#arr)" />
      <line x1="82" y1="39" x2="90" y2="39" stroke={ec} strokeWidth="1" opacity="0.55" markerEnd="url(#arr)" />

      {/* Group 2 — bottom: Service → Deploys → Pods + ConfigMap */}
      <rect x="30" y="64" width="126" height="52" rx="6"
        fill={groupBg} stroke={groupBorder} strokeWidth="1" strokeDasharray="3 2" />

      {/* Nodes */}
      <rect x="36" y="82" width="16" height="10" rx="3" fill={service} opacity="0.85" />
      <rect x="66" y="72" width="16" height="10" rx="3" fill={deploy} opacity="0.85" />
      <rect x="66" y="92" width="16" height="10" rx="3" fill={deploy} opacity="0.85" />
      <circle cx="96" cy="73" r="4.5" fill={pod} opacity="0.65" />
      <circle cx="96" cy="87" r="4.5" fill={pod} opacity="0.65" />
      <circle cx="96" cy="101" r="4.5" fill={pod} opacity="0.65" />

      {/* Config node */}
      <rect x="120" y="82" width="16" height="10" rx="3" fill={config} opacity="0.55" />

      {/* Edges — group 2 */}
      <line x1="52" y1="87" x2="65" y2="78" stroke={ec} strokeWidth="1" opacity="0.55" markerEnd="url(#arr)" />
      <line x1="52" y1="87" x2="65" y2="96" stroke={ec} strokeWidth="1" opacity="0.55" markerEnd="url(#arr)" />
      <line x1="82" y1="77" x2="90" y2="74" stroke={ec} strokeWidth="1" opacity="0.55" markerEnd="url(#arr)" />
      <line x1="82" y1="77" x2="90" y2="87" stroke={ec} strokeWidth="1" opacity="0.55" markerEnd="url(#arr)" />
      <line x1="82" y1="97" x2="90" y2="101" stroke={ec} strokeWidth="1" opacity="0.55" markerEnd="url(#arr)" />

      {/* Config dashed edge */}
      <line x1="82" y1="77" x2="119" y2="87" stroke={ec} strokeWidth="0.7" opacity="0.3" strokeDasharray="2 2" markerEnd="url(#arr-f)" />

      {/* Cross-group curved edge */}
      <path d="M 18 34 Q 18 64, 36 82" fill="none" stroke={ec} strokeWidth="0.8" opacity="0.3" markerEnd="url(#arr-f)" />
    </svg>
  )
}

// Derive stats from real topology data
function useTopologyStats(topology: Topology | null) {
  return useMemo(() => {
    if (!topology || topology.nodes.length === 0) return null

    const kindCounts: Record<string, number> = {}
    const healthCounts = { healthy: 0, degraded: 0, unhealthy: 0, unknown: 0 }

    for (const node of topology.nodes) {
      kindCounts[node.kind] = (kindCounts[node.kind] || 0) + 1
      if (node.status in healthCounts) {
        healthCounts[node.status as keyof typeof healthCounts]++
      }
    }

    // Top kinds sorted by display priority
    const kindPriority: Record<string, number> = {
      Deployment: 1, Rollout: 1, StatefulSet: 2, DaemonSet: 2,
      Service: 3, Ingress: 4, Gateway: 4,
      HTTPRoute: 4, GRPCRoute: 4, TCPRoute: 4, TLSRoute: 4,
      Pod: 5, PodGroup: 5,
      Job: 6, CronJob: 6, ConfigMap: 7, Secret: 7,
    }
    const topKinds = Object.entries(kindCounts)
      .filter(([kind]) => kind !== 'Internet')
      .sort(([a], [b]) => (kindPriority[a] || 99) - (kindPriority[b] || 99))
      .slice(0, 8)

    return { topKinds, healthCounts }
  }, [topology])
}

const kindDotColors: Record<string, string> = {
  Deployment: 'bg-emerald-400', Rollout: 'bg-emerald-400',
  StatefulSet: 'bg-cyan-400', DaemonSet: 'bg-teal-400',
  Service: 'bg-blue-400', Ingress: 'bg-violet-400', Gateway: 'bg-violet-400',
  HTTPRoute: 'bg-purple-400', GRPCRoute: 'bg-purple-400', TCPRoute: 'bg-purple-400', TLSRoute: 'bg-purple-400',
  Pod: 'bg-lime-500', PodGroup: 'bg-lime-500',
  Job: 'bg-purple-400', CronJob: 'bg-purple-400',
  ConfigMap: 'bg-amber-400', Secret: 'bg-red-400',
  ReplicaSet: 'bg-green-400', HPA: 'bg-pink-500', PVC: 'bg-cyan-400',
}

export function TopologyPreview({ topology, summary, onNavigate }: TopologyPreviewProps) {
  const stats = useTopologyStats(topology)

  return (
    <button
      onClick={onNavigate}
      className="group flex flex-col h-[260px] rounded-lg border-[3px] border-blue-500/30 bg-theme-surface/50 hover:-translate-y-1 hover:shadow-[0_12px_24px_rgba(0,0,0,0.12)] hover:border-blue-500/60 transition-all duration-200 text-left overflow-hidden cursor-pointer"
    >
      <div className="flex items-center justify-between px-4 py-2 border-b border-theme-border">
        <div className="flex items-center gap-2">
          <Network className="w-4 h-4 text-blue-500" />
          <span className="text-sm font-semibold text-blue-500">Topology</span>
        </div>
        <span className="text-[11px] text-theme-text-tertiary">
          {summary.nodeCount} resources &middot; {summary.edgeCount} conn
        </span>
      </div>

      {/* Stats (left) + Schematic (right) */}
      <div className="flex-1 flex items-stretch min-h-0 px-3 py-1.5 gap-2">
        {/* Left: compact stats */}
        <div className="flex flex-col justify-center gap-0.5 min-w-0 w-[105px] shrink-0">
          {stats ? (
            <>
              {stats.topKinds.map(([kind, count]) => (
                <div key={kind} className="flex items-center gap-1.5 text-[10px] leading-tight">
                  <span className={clsx(
                    'w-1.5 h-1.5 rounded-full shrink-0',
                    kindDotColors[kind] || 'bg-theme-text-tertiary',
                  )} />
                  <span className="text-theme-text-primary font-medium w-5 text-right tabular-nums">{count}</span>
                  <span className="text-theme-text-tertiary truncate">{kind}</span>
                </div>
              ))}

              {(stats.healthCounts.degraded > 0 || stats.healthCounts.unhealthy > 0) && (
                <div className="flex items-center gap-1.5 text-[10px] text-theme-text-tertiary mt-0.5 pt-1 border-t border-theme-border/50">
                  {stats.healthCounts.unhealthy > 0 && (
                    <span className="flex items-center gap-0.5">
                      <span className="w-1.5 h-1.5 rounded-full bg-red-500" />
                      {stats.healthCounts.unhealthy}
                    </span>
                  )}
                  {stats.healthCounts.degraded > 0 && (
                    <span className="flex items-center gap-0.5">
                      <span className="w-1.5 h-1.5 rounded-full bg-yellow-500" />
                      {stats.healthCounts.degraded}
                    </span>
                  )}
                </div>
              )}
            </>
          ) : (
            // Show summary-based placeholder while full topology loads via SSE
            <div className="flex flex-col gap-0.5">
              <div className="flex items-center gap-1.5 text-[10px] leading-tight">
                <span className="w-1.5 h-1.5 rounded-full bg-blue-400 shrink-0" />
                <span className="text-theme-text-primary font-medium w-5 text-right tabular-nums">{summary.nodeCount}</span>
                <span className="text-theme-text-tertiary">resources</span>
              </div>
              <div className="flex items-center gap-1.5 text-[10px] leading-tight">
                <span className="w-1.5 h-1.5 rounded-full bg-theme-text-tertiary shrink-0" />
                <span className="text-theme-text-primary font-medium w-5 text-right tabular-nums">{summary.edgeCount}</span>
                <span className="text-theme-text-tertiary">connections</span>
              </div>
            </div>
          )}
        </div>

        {/* Right: schematic illustration */}
        <div className="flex-1 flex items-center min-w-0">
          <TopologySchematic />
        </div>
      </div>

      <div className="px-4 py-1.5 border-t border-theme-border flex items-center justify-end gap-1.5 text-xs font-medium text-blue-500 group-hover:text-blue-400 transition-colors">
        Open Topology
        <ArrowRight className="w-3.5 h-3.5 transition-transform group-hover:translate-x-0.5" />
      </div>
    </button>
  )
}
