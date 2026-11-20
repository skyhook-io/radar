import { useEffect, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { formatDuration } from '@skyhook-io/k8s-ui/components/resources/resource-utils'
import { apiFetch } from '../api/client'
import { apiUrl } from '../api/config'
import type { ContextSwitchOperation } from '../api/contextSwitch'
import { readErrorBody } from '../api/httpErrors'

export function ContextSwitchActions({ operation, onDismiss }: { operation: ContextSwitchOperation; onDismiss: () => void }) {
  const queryClient = useQueryClient()
  const [now, setNow] = useState(Date.now)
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [])
  const cancel = useMutation({
    mutationFn: async () => {
      const path = `/contexts/${encodeURIComponent(operation.target)}?cancelSwitch=${encodeURIComponent(operation.id)}`
      const response = await apiFetch(apiUrl(path), { method: 'POST' })
      if (!response.ok) throw new Error((await readErrorBody(response)).error || 'Could not cancel the switch')
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ['connection-status'] }),
  })

  return <span className="mt-2 block text-sm font-normal">
    <span aria-live="off" className="block text-xs text-theme-text-tertiary">Elapsed {formatDuration(Math.max(0, now - Date.parse(operation.startedAt)), true)}</span>
    {operation.phase === 'preflight'
      ? <button type="button" disabled={cancel.isPending} className="btn-secondary mt-3 px-3 py-1.5" onClick={() => cancel.mutate()}>Cancel switch</button>
      : <button type="button" className="btn-secondary mt-3 px-3 py-1.5" onClick={onDismiss}>Dismiss</button>}
    {cancel.error && operation.phase === 'preflight' && <span role="alert" className="block">{cancel.error.message}</span>}
  </span>
}
