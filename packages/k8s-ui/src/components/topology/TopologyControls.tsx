import { FolderTree, ShieldCheck } from 'lucide-react'
import type { TopologyMode, GroupingMode } from '../../types/core'
import { Tooltip } from '../ui/Tooltip'

interface TopologyControlsProps {
  viewMode: TopologyMode
  onViewModeChange: (mode: TopologyMode) => void
  groupingMode: GroupingMode
  onGroupingModeChange: (mode: GroupingMode) => void
  showNoGrouping?: boolean
  showPolicyEffect?: boolean
  onShowPolicyEffectChange?: (show: boolean) => void
  /** Show the "Fleet" button (CAPI cluster management view) */
  showFleetMode?: boolean
  /**
   * Navigate to the observed-traffic view. When provided, the "Network Flow"
   * tooltip offers a link to it — disambiguating the config-derived flow graph
   * here from the live, observed Traffic view. Omitted by hosts without one.
   */
  onNavigateToTraffic?: () => void
}

export function TopologyControls({
  viewMode,
  onViewModeChange,
  groupingMode,
  onGroupingModeChange,
  showNoGrouping = true,
  showPolicyEffect = false,
  onShowPolicyEffectChange,
  showFleetMode = false,
  onNavigateToTraffic,
}: TopologyControlsProps) {
  return (
    <div className="absolute top-4 right-4 z-10 flex items-center gap-2">
      {/* Policy effect toggle */}
      {onShowPolicyEffectChange && (
        <button
          onClick={() => onShowPolicyEffectChange(!showPolicyEffect)}
          className={`flex items-center gap-1.5 px-2.5 py-1.5 text-xs rounded-lg border transition-colors ${
            showPolicyEffect
              ? 'bg-indigo-600 text-white border-indigo-600'
              : 'bg-theme-surface/90 backdrop-blur text-theme-text-secondary border-theme-border hover:text-theme-text-primary'
          }`}
          title="Show NetworkPolicy effects on edges"
        >
          <ShieldCheck className="w-3.5 h-3.5" />
          Policies
        </button>
      )}

      {/* Grouping selector */}
      <div className="flex items-center gap-1.5 px-2 py-1.5 bg-theme-surface/90 backdrop-blur border border-theme-border rounded-lg">
        <FolderTree className="w-3.5 h-3.5 text-theme-text-secondary" />
        <select
          value={groupingMode}
          onChange={(e) => onGroupingModeChange(e.target.value as GroupingMode)}
          className="appearance-none bg-transparent text-theme-text-primary text-xs focus:outline-none"
        >
          {showNoGrouping && (
            <option value="none" className="bg-theme-surface">No Grouping</option>
          )}
          <option value="namespace" className="bg-theme-surface">By Namespace</option>
          <option value="app" className="bg-theme-surface">By App Label</option>
        </select>
      </div>

      {/* View mode toggle */}
      <div className="flex items-center gap-0.5 p-1 bg-theme-surface/90 backdrop-blur border border-theme-border rounded-lg">
        <button
          onClick={() => onViewModeChange('resources')}
          className={`px-2.5 py-1 text-xs rounded-md transition-colors ${
            viewMode === 'resources'
              ? 'bg-skyhook-600 text-white'
              : 'text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated'
          }`}
        >
          Resources
        </button>
        <Tooltip
          position="bottom"
          content={
            <div className="space-y-1.5 text-left">
              <p className="text-theme-text-secondary">
                How requests <em>should</em> route, derived from Ingress, Services and
                routing CRDs (Traefik, Gateway API, Istio…) — not observed packets.
              </p>
              {onNavigateToTraffic && (
                <p className="text-theme-text-tertiary">
                  Looking for observed, measured traffic?{' '}
                  <button
                    type="button"
                    onClick={onNavigateToTraffic}
                    className="text-skyhook-400 hover:text-skyhook-300 underline underline-offset-2"
                  >
                    Open Live Traffic →
                  </button>
                </p>
              )}
            </div>
          }
        >
          <button
            onClick={() => onViewModeChange('traffic')}
            className={`px-2.5 py-1 text-xs rounded-md transition-colors whitespace-nowrap ${
              viewMode === 'traffic'
                ? 'bg-skyhook-600 text-white'
                : 'text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated'
            }`}
          >
            Network Flow <span className="opacity-70">(config)</span>
          </button>
        </Tooltip>
        {showFleetMode && (
          <button
            onClick={() => onViewModeChange('fleet')}
            className={`px-2.5 py-1 text-xs rounded-md transition-colors ${
              viewMode === 'fleet'
                ? 'bg-skyhook-600 text-white'
                : 'text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated'
            }`}
            title="Cluster API fleet view — shows only CAPI resources and nodes"
          >
            Fleet
          </button>
        )}
      </div>
    </div>
  )
}
