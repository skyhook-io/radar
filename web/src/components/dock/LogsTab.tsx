import { LogsViewer } from '../logs/LogsViewer'

interface LogsTabProps {
  namespace: string
  podName: string
  containers: string[]
  initialContainer?: string
}

export function LogsTab({
  namespace,
  podName,
  containers,
  initialContainer,
}: LogsTabProps) {
  return (
    <div className="h-full">
      <LogsViewer
        namespace={namespace}
        podName={podName}
        containers={containers}
        initialContainer={initialContainer}
      />
    </div>
  )
}
