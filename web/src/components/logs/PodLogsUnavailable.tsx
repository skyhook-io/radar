import { Terminal } from 'lucide-react'
import { isNotFoundError } from '../../api/client'

// Generic Pod pickers can list Pods recalled from history that no longer
// exist, and the API server serves no logs for a deleted Pod.
export function PodLogsUnavailable({ podName, error }: { podName: string; error: unknown }) {
  const gone = isNotFoundError(error)
  return (
    <div role="status" className="flex flex-1 flex-col items-center justify-center gap-1 px-6 text-center text-theme-text-tertiary">
      <Terminal className="mb-3 h-12 w-12 opacity-50" />
      <p className="text-sm font-medium text-theme-text-secondary">
        {gone ? `Radar can't find ${podName}` : `Logs for ${podName} are unavailable`}
      </p>
      <p className="text-sm">
        {gone
          ? 'It may have been deleted, and Kubernetes serves no logs for a deleted Pod. Choose another Pod.'
          : error instanceof Error ? error.message : 'The log request failed.'}
      </p>
    </div>
  )
}
