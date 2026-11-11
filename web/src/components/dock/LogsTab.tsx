import { LogsViewer } from '../logs/LogsViewer'

interface LogsTabProps {
  namespace: string
  podName: string
  containers: string[]
  initialContainer?: string
  initContainers?: string[]
}

export function LogsTab({
  namespace,
  podName,
  containers,
  initialContainer,
  initContainers,
}: LogsTabProps) {
  return (
    <div className="h-full">
      <LogsViewer
        namespace={namespace}
        podName={podName}
        containers={containers}
        initialContainer={initialContainer}
        initContainers={initContainers}
      />
    </div>
  )
}
