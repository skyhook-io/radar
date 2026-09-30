import { useTrafficFlowList } from '../traffic/TrafficFlowListContext'
import { TrafficFlowList } from '../traffic/TrafficFlowList'
import { List } from 'lucide-react'

export function TrafficFlowListTab() {
  const { flows, responsesCallerOriented } = useTrafficFlowList()

  if (flows.length === 0) {
    return (
      <div className="flex items-center justify-center h-full text-sm text-theme-text-tertiary gap-2">
        <List className="w-4 h-4" />
        Navigate to Traffic view to see flows
      </div>
    )
  }

  return <TrafficFlowList flows={flows} responsesCallerOriented={responsesCallerOriented} />
}
