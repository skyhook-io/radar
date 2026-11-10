export function ViewModeToggle({ viewMode, onViewModeChange }: { viewMode: 'list' | 'swimlane', onViewModeChange: (mode: 'list' | 'swimlane') => void }) {
  return (
    <div className="flex items-center gap-1 bg-theme-base rounded-lg p-0.5 border border-theme-border">
      <button
        type="button"
        onClick={() => onViewModeChange('list')}
        className={`px-2 py-1 text-xs rounded-md transition-colors ${viewMode === 'list' ? 'bg-theme-surface text-theme-text-primary shadow-sm' : 'text-theme-text-secondary hover:text-theme-text-primary'}`}
      >
        List
      </button>
      <button
        type="button"
        onClick={() => onViewModeChange('swimlane')}
        className={`px-2 py-1 text-xs rounded-md transition-colors ${viewMode === 'swimlane' ? 'bg-theme-surface text-theme-text-primary shadow-sm' : 'text-theme-text-secondary hover:text-theme-text-primary'}`}
      >
        Swimlane
      </button>
    </div>
  )
}
