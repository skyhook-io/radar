import { useEffect, useState } from 'react'
import { Clock, X } from 'lucide-react'
import { AlertBanner } from '@skyhook-io/k8s-ui'
import { useNodeDrainActivity } from '../../api/client'

function duration(milliseconds: number): string {
  const seconds = Math.max(0, Math.floor(milliseconds / 1000))
  return `${Math.floor(seconds / 60)}m ${seconds % 60}s`
}

export function NodeDrainStatus({ name, nodeUID }: { name: string; nodeUID: string }) {
  const { activity, dismiss } = useNodeDrainActivity(name, nodeUID)
  const [now, setNow] = useState(Date.now)
  const pending = activity?.status === 'pending'
  useEffect(() => {
    if (!pending) return
    const timer = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [pending, activity?.startedAt])

  if (!activity) return null
  if (activity.status === 'pending') {
    const elapsed = now - activity.startedAt
    return (
      <section aria-label="Drain progress">
        <AlertBanner variant="info" icon={Clock} title="Drain in progress">
          <p>Requesting evictions and waiting for pod deletion.</p>
          <p className="mt-1">Elapsed {duration(elapsed)}. Shared eviction and deletion deadline: {duration(activity.timeoutSeconds * 1000)}. A final verification check can take up to 5 seconds more.</p>
          {elapsed > (activity.timeoutSeconds + 5) * 1000 && (
            <p className="mt-1">Still waiting for Radar's response. Check the connection and the node's pods before retrying or taking it down.</p>
          )}
          <p className="mt-1">You can leave this page and return while this tab stays open.</p>
        </AlertBanner>
      </section>
    )
  }

  return (
    <section aria-label="Last drain in this tab">
      <AlertBanner
        variant={activity.summary.type}
        title={activity.summary.title}
        action={(
          <button type="button" aria-label="Dismiss drain result" onClick={dismiss} className="text-theme-text-secondary hover:text-theme-text-primary p-1 rounded focus-visible:outline focus-visible:outline-2">
            <X className="w-4 h-4" />
          </button>
        )}
      >
        <p className="text-xs mt-1">Last drain in this tab: <time dateTime={new Date(activity.finishedAt).toISOString()}>{new Date(activity.finishedAt).toLocaleString()}</time>. Duration {duration(activity.finishedAt - activity.startedAt)}.</p>
        <p className="text-xs mt-2 whitespace-pre-line break-words">{activity.summary.detail}</p>
        <p className="text-xs mt-2">Recorded result. Later node or workload changes are not reflected here. Reloading or switching contexts clears this tab's record.</p>
      </AlertBanner>
    </section>
  )
}
