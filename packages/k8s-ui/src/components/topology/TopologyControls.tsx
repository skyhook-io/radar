import { FolderTree, ShieldCheck } from 'lucide-react'
import type { TopologyMode, GroupingMode } from '../../types/core'

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
}: TopologyControlsProps) {
  return (
    <div className="absolute top-4 right-4 z-10 flex items-center gap-2">
      {/* Policy effect toggle + legend */}
      {onShowPolicyEffectChange && (
        <>
          {showPolicyEffect && (
            <div
              className="flex items-center gap-2 px-2.5 py-1.5 text-[10px] uppercase tracking-wide rounded-lg border bg-theme-surface/90 backdrop-blur text-theme-text-secondary border-theme-border"
              role="legend"
              aria-label="NetworkPolicy effect legend"
            >
              <span className="flex items-center gap-1">
                <span className="inline-block w-3 h-[2px]" style={{ background: '#10b981' }} />
                allowed
              </span>
              <span className="flex items-center gap-1">
                <span
                  className="inline-block w-3 h-[2.5px]"
                  style={{ background: 'repeating-linear-gradient(90deg, #ef4444 0 4px, transparent 4px 6px)' }}
                />
                blocked
              </span>
              <span className="flex items-center gap-1">
                <span className="inline-block w-3 h-[2px]" style={{ background: '#f59e0b' }} />
                unprotected
              </span>
            </div>
          )}
          <button
            onClick={() => onShowPolicyEffectChange(!showPolicyEffect)}
            className={`flex items-center gap-1.5 px-2.5 py-1.5 text-xs rounded-lg border transition-colors ${
              showPolicyEffect
                ? 'bg-indigo-600 text-white border-indigo-600'
                : 'bg-theme-surface/90 backdrop-blur text-theme-text-secondary border-theme-border hover:text-theme-text-primary'
            }`}
            title="Show NetworkPolicy effects on edges (allowed / blocked / unprotected)"
            aria-pressed={showPolicyEffect}
          >
            <ShieldCheck className="w-3.5 h-3.5" />
            Policies
          </button>
        </>
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
        <button
          onClick={() => onViewModeChange('traffic')}
          className={`px-2.5 py-1 text-xs rounded-md transition-colors ${
            viewMode === 'traffic'
              ? 'bg-skyhook-600 text-white'
              : 'text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-elevated'
          }`}
        >
          Traffic
        </button>
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
