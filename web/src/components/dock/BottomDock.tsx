import { BottomDock as K8sBottomDock, DockTab } from '@skyhook-io/k8s-ui'
import { TerminalTab } from './TerminalTab'
import { LogsTab } from './LogsTab'
import { WorkloadLogsTab } from './WorkloadLogsTab'

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

  return null
}

export function BottomDock() {
  return <K8sBottomDock renderTabContent={renderTabContent} />
}
