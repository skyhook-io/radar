import { LogsViewer } from '../logs/LogsViewer'

interface LogsTabProps {
  namespace: string
  podName: string
  containers: string[]
  initialContainer?: string
  /** Open on History (logs from Loki). */
  history?: boolean
}

export function LogsTab({
  namespace,
  podName,
  containers,
  initialContainer,
  history = false,
}: LogsTabProps) {
  return (
    <div className="h-full">
      <LogsViewer
        namespace={namespace}
        podName={podName}
        containers={containers}
        initialContainer={initialContainer}
        history={history}
      />
    </div>
  )
}
