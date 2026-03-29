import { FolderTree } from 'lucide-react'
import type { TopologyMode, GroupingMode } from '../../types/core'

interface TopologyControlsProps {
  viewMode: TopologyMode
  onViewModeChange: (mode: TopologyMode) => void
  groupingMode: GroupingMode
  onGroupingModeChange: (mode: GroupingMode) => void
  showNoGrouping?: boolean
}

export function TopologyControls({
  viewMode,
  onViewModeChange,
  groupingMode,
  onGroupingModeChange,
  showNoGrouping = true,
}: TopologyControlsProps) {
  return (
    <div className="absolute top-4 right-4 z-10 flex items-center gap-2">
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
      </div>
    </div>
  )
}
