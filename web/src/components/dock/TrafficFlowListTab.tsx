import { useTrafficFlowList } from '../traffic/TrafficFlowListContext'
import { TrafficFlowList } from '../traffic/TrafficFlowList'
import { List, Loader2 } from 'lucide-react'

export function TrafficFlowListTab() {
  const { flows, responsesCallerOriented, graphSelection, loading, note } = useTrafficFlowList()

  if (loading) {
    return (
      <div className="flex items-center justify-center h-full text-sm text-theme-text-tertiary gap-2">
        <Loader2 className="w-4 h-4 animate-spin" />
        Loading flows for the selection…
      </div>
    )
  }

  if (flows.length === 0) {
    return (
      <div className="flex flex-col items-center justify-center h-full gap-1 px-4 text-center">
        <div className="flex items-center gap-2 text-sm text-theme-text-tertiary">
          <List className="w-4 h-4" />
          {graphSelection ? 'No flow records for this selection' : 'Navigate to Traffic view to see flows'}
        </div>
        {note && <div className="text-[11px] text-theme-text-tertiary">{note}</div>}
      </div>
    )
  }

  return (
    <div className="flex flex-col h-full">
      {note && (
        <div className="px-3 py-1 border-b border-theme-border text-[11px] text-theme-text-tertiary">{note}</div>
      )}
      <div className="relative flex-1 min-h-0">
        <TrafficFlowList flows={flows} responsesCallerOriented={responsesCallerOriented} />
      </div>
    </div>
  )
}
