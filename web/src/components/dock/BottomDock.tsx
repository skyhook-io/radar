import { BottomDock as K8sBottomDock, DockTab, useDock, Input } from '@skyhook-io/k8s-ui'
import { TerminalTab } from './TerminalTab'
import { LogsTab } from './LogsTab'
import { WorkloadLogsTab } from './WorkloadLogsTab'
import { NodeTerminalTab } from './NodeTerminalTab'
import { LocalTerminalTab } from './LocalTerminalTab'
import { TrafficFlowListTab } from './TrafficFlowListTab'
import { useFlowSearch } from '../traffic/TrafficFlowListContext'
import { Search } from 'lucide-react'

function renderTabContent(tab: DockTab, isActive: boolean) {
  if (tab.type === 'terminal') {
    return (
      <TerminalTab
        namespace={tab.namespace!}
        podName={tab.podName!}
        containerName={tab.containerName!}
        containers={tab.containers!}
        isActive={isActive}
      />
    )
  }

  if (tab.type === 'logs') {
    return (
      <LogsTab
        namespace={tab.namespace!}
        podName={tab.podName!}
        containers={tab.containers!}
        initialContainer={tab.containerName}
      />
    )
  }

  if (tab.type === 'workload-logs') {
    return (
      <WorkloadLogsTab
        namespace={tab.namespace!}
        workloadKind={tab.workloadKind!}
        workloadName={tab.workloadName!}
      />
    )
  }

  if (tab.type === 'node-terminal') {
    return (
      <NodeTerminalTab
        nodeName={tab.nodeName!}
        isActive={isActive}
      />
    )
  }

  if (tab.type === 'local-terminal') {
    return (
      <LocalTerminalTab isActive={isActive} initialCommand={tab.initialCommand} />
    )
  }

  if (tab.type === 'traffic-flows') {
    return <TrafficFlowListTab />
  }

  return null
}

function TrafficFlowSearchHeader() {
  const [search, setSearch] = useFlowSearch()
  return (
    <div className="flex-1 flex items-center ml-2">
      <div className="relative">
        <Search className="absolute left-1.5 top-1/2 -translate-y-1/2 w-3 h-3 text-theme-text-tertiary" />
        <Input
          value={search}
          onChange={e => setSearch(e.target.value)}
          placeholder="Filter flows..."
          className="w-48 pl-6 pr-2 py-1 text-[11px] rounded bg-theme-elevated border border-theme-border text-theme-text-primary placeholder:text-theme-text-tertiary focus:outline-none focus:ring-1 focus:ring-blue-500/50"
        />
      </div>
    </div>
  )
}

function renderTabHeaderExtra(tab: DockTab) {
  if (tab.type === 'traffic-flows') {
    return <TrafficFlowSearchHeader />
  }
  return null
}

export function BottomDock() {
  const { leftOffset, tabs } = useDock()
  const hasTrafficFlows = tabs.some(t => t.type === 'traffic-flows')
  return <K8sBottomDock renderTabContent={renderTabContent} renderTabHeaderExtra={renderTabHeaderExtra} leftOffset={leftOffset} defaultHeight={hasTrafficFlows ? 300 : undefined} />
}
