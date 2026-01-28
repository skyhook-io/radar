import type { DashboardTrafficSummary } from '../../api/client'
import { Activity, ArrowRight, ArrowRightLeft } from 'lucide-react'

interface TrafficSummaryProps {
  data: DashboardTrafficSummary | null
  onNavigate: () => void
}

/**
 * Static schematic SVG mimicking the real traffic view layout:
 * vertical spine on the left, Internet node, chunky arrows branching
 * right to colored service nodes, some with further connections.
 */
function TrafficSchematic() {
  // Service node colors matching the real traffic view
  const green  = '#22c55e'  // skyhook-connector style
  const blue   = '#3b82f6'  // dev services
  const brown  = '#a3734c'  // staging services
  const teal   = '#14b8a6'  // keda style
  const red    = '#ef4444'  // unhealthy / alert
  const purple = '#a855f7'  // envoy / system
  const inet   = '#93c5fd'  // light blue — Internet node bg
  const inetBorder = '#60a5fa'

  // Edge color
  const ec = '#6b7280' // gray-500

  return (
    <svg viewBox="0 0 160 120" className="w-full h-auto max-h-full" aria-hidden="true">
      <defs>
        <marker id="trf-a" viewBox="0 0 10 8" refX="9" refY="4"
          markerWidth="7" markerHeight="6" orient="auto">
          <path d="M 0 0 L 10 4 L 0 8 z" fill={ec} opacity="0.55" />
        </marker>
        <marker id="trf-a2" viewBox="0 0 10 8" refX="9" refY="4"
          markerWidth="7" markerHeight="6" orient="auto">
          <path d="M 0 0 L 10 4 L 0 8 z" fill={ec} opacity="0.35" />
        </marker>
      </defs>

      {/* Vertical spine */}
      <line x1="38" y1="6" x2="38" y2="114" stroke={ec} strokeWidth="2.5" opacity="0.3" strokeLinecap="round" />

      {/* Internet node — rounded rect with light fill */}
      <rect x="3" y="43" width="24" height="16" rx="5" fill={inet} opacity="0.35" stroke={inetBorder} strokeWidth="0.8" />
      <circle cx="12" cy="51" r="2.5" fill={inetBorder} opacity="0.6" />

      {/* Arrow: Internet → spine */}
      <line x1="27" y1="51" x2="36" y2="51" stroke={ec} strokeWidth="2.5" opacity="0.45" markerEnd="url(#trf-a)" />

      {/* === Branch 1: green service (top) === */}
      <line x1="39" y1="14" x2="56" y2="14" stroke={ec} strokeWidth="2" opacity="0.4" markerEnd="url(#trf-a)" />
      <rect x="58" y="8" width="28" height="12" rx="3" fill={green} opacity="0.85" />
      {/* status dot */}
      <circle cx="62" cy="14" r="1.5" fill="#fbbf24" opacity="0.9" />

      {/* === Branch 2: blue service → right node === */}
      <line x1="39" y1="33" x2="56" y2="33" stroke={ec} strokeWidth="1.8" opacity="0.4" markerEnd="url(#trf-a)" />
      <rect x="58" y="27" width="28" height="12" rx="3" fill={blue} opacity="0.85" />
      <circle cx="62" cy="33" r="1.5" fill={green} opacity="0.9" />
      {/* → right node (external) */}
      <line x1="86" y1="33" x2="108" y2="24" stroke={ec} strokeWidth="1.2" opacity="0.35" markerEnd="url(#trf-a2)" />
      <rect x="110" y="18" width="28" height="12" rx="3" fill={red} opacity="0.7" />

      {/* === Branch 3: brown service === */}
      <line x1="39" y1="51" x2="56" y2="51" stroke={ec} strokeWidth="2.2" opacity="0.45" markerEnd="url(#trf-a)" />
      <rect x="58" y="45" width="28" height="12" rx="3" fill={brown} opacity="0.85" />
      <circle cx="62" cy="51" r="1.5" fill={green} opacity="0.9" />

      {/* === Branch 4: teal service → right node === */}
      <line x1="39" y1="69" x2="56" y2="69" stroke={ec} strokeWidth="1.5" opacity="0.38" markerEnd="url(#trf-a)" />
      <rect x="58" y="63" width="28" height="12" rx="3" fill={teal} opacity="0.85" />
      <circle cx="62" cy="69" r="1.5" fill={green} opacity="0.9" />
      {/* → right node */}
      <line x1="86" y1="69" x2="108" y2="76" stroke={ec} strokeWidth="1.2" opacity="0.35" markerEnd="url(#trf-a2)" />
      <rect x="110" y="70" width="28" height="12" rx="3" fill={blue} opacity="0.7" />

      {/* === Branch 5: purple service (bottom) === */}
      <line x1="39" y1="87" x2="56" y2="87" stroke={ec} strokeWidth="1.3" opacity="0.35" markerEnd="url(#trf-a)" />
      <rect x="58" y="81" width="28" height="12" rx="3" fill={purple} opacity="0.85" />
      <circle cx="62" cy="87" r="1.5" fill={green} opacity="0.9" />

      {/* === Branch 6: small red service === */}
      <line x1="39" y1="103" x2="56" y2="103" stroke={ec} strokeWidth="1" opacity="0.3" markerEnd="url(#trf-a)" />
      <rect x="58" y="97" width="28" height="12" rx="3" fill={red} opacity="0.65" />
      <circle cx="62" cy="103" r="1.5" fill={red} opacity="0.9" />
    </svg>
  )
}

export function TrafficSummary({ data, onNavigate }: TrafficSummaryProps) {
  const hasFlows = data && data.flowCount > 0

  return (
    <button
      onClick={onNavigate}
      className="group flex flex-col h-[260px] rounded-lg border-[3px] border-blue-500/30 bg-theme-surface/50 hover:-translate-y-1 hover:shadow-[0_12px_24px_rgba(0,0,0,0.12)] hover:border-blue-500/60 transition-all duration-200 text-left overflow-hidden"
    >
      <div className="flex items-center justify-between px-4 py-2 border-b border-theme-border">
        <div className="flex items-center gap-2">
          <Activity className="w-4 h-4 text-blue-500" />
          <span className="text-sm font-semibold text-blue-500">Traffic</span>
        </div>
        {hasFlows && (
          <span className="text-[11px] text-theme-text-tertiary">
            {data.source} &middot; {data.flowCount} flows
          </span>
        )}
      </div>

      <div className="flex-1 min-h-0 overflow-hidden">
        {hasFlows ? (
          /* Real data: show top flows */
          <div className="px-4 py-2 space-y-1.5">
            {data.topFlows.map((flow, i) => (
              <div key={i} className="flex items-center gap-2 text-xs">
                <span className="text-theme-text-primary truncate">{flow.src}</span>
                <ArrowRightLeft className="w-3 h-3 text-theme-text-tertiary shrink-0" />
                <span className="text-theme-text-primary truncate">{flow.dst}</span>
                <span className="text-xs text-theme-text-tertiary shrink-0 ml-auto">
                  {flow.connections} conn
                </span>
              </div>
            ))}
          </div>
        ) : (
          /* No data yet: schematic + description */
          <div className="flex items-stretch gap-2 px-3 py-1.5 h-full">
            <div className="flex flex-col justify-center gap-1.5 w-[105px] shrink-0">
              <p className="text-[11px] leading-snug text-theme-text-secondary">
                Visualize live service-to-service network flows and external dependencies
              </p>
              {data && (
                <span className="text-[10px] text-theme-text-tertiary">
                  Source: {data.source}
                </span>
              )}
            </div>
            <div className="flex-1 min-w-0 overflow-hidden">
              <TrafficSchematic />
            </div>
          </div>
        )}
      </div>

      <div className="px-4 py-1.5 border-t border-theme-border flex items-center justify-end gap-1.5 text-xs font-medium text-blue-500 group-hover:text-blue-400 transition-colors">
        Open Traffic
        <ArrowRight className="w-3.5 h-3.5 transition-transform group-hover:translate-x-0.5" />
      </div>
    </button>
  )
}
