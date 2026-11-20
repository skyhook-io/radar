import { useEffect, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { formatDuration } from '@skyhook-io/k8s-ui/components/resources/resource-utils'
import { apiFetch } from '../api/client'
import { apiUrl } from '../api/config'
import type { ContextSwitchOperation } from '../api/contextSwitch'
import { readErrorBody } from '../api/httpErrors'

export function ContextSwitchActions({ operation, syncing, onDismiss }: { operation: ContextSwitchOperation; syncing: boolean; onDismiss: () => void }) {
  const queryClient = useQueryClient()
  const [now, setNow] = useState(Date.now)
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), 1000)
    return () => window.clearInterval(timer)
  }, [])
  const cancel = useMutation({
    mutationFn: async (action: 'cancelSwitch' | 'stopSwitch') => {
      const path = `/contexts/${encodeURIComponent(operation.target)}?${action}=${encodeURIComponent(operation.id)}`
      const response = await apiFetch(apiUrl(path), { method: 'POST' })
      if (!response.ok) throw new Error((await readErrorBody(response)).error || (action === 'cancelSwitch' ? 'Could not cancel the switch' : 'Could not stop the switch'))
    },
    onSettled: () => queryClient.invalidateQueries({ queryKey: ['connection-status'] }),
  })

  let stageLimit = ''
  if (operation.phase === 'preflight') {
    stageLimit = `Connectivity check limit ${formatDuration(operation.preflightTimeoutSeconds * 1000, true)}`
  } else if (syncing) {
    stageLimit = `Initial workload sync limit ${formatDuration(operation.syncTimeoutSeconds * 1000, true)}`
  }

  return <span className="mt-2 block text-sm font-normal">
    <span aria-live="off" className="block text-xs text-theme-text-tertiary">Elapsed {formatDuration(Math.max(0, now - Date.parse(operation.startedAt)), true)}{stageLimit && ` · ${stageLimit}`}</span>
    {operation.phase === 'preflight'
      ? <button type="button" disabled={cancel.isPending} className="btn-secondary mt-3 px-3 py-1.5" onClick={() => cancel.mutate('cancelSwitch')}>Cancel switch</button>
      : <>
        {operation.canStop && <span className="mt-2 block text-theme-text-secondary">The previous connection is closed. Stop to choose a cluster.</span>}
        {operation.canStop && <button type="button" disabled={cancel.isPending} className="btn-secondary mt-3 px-3 py-1.5" onClick={() => cancel.mutate('stopSwitch')}>Stop switch</button>}
        <button type="button" className="ml-2 mt-3 px-3 py-1.5 text-theme-text-secondary" onClick={onDismiss}>Hide progress</button>
      </>}
    {cancel.error && ((cancel.variables === 'cancelSwitch' && operation.phase === 'preflight') || (cancel.variables === 'stopSwitch' && operation.canStop)) && <span role="alert" className="block">{cancel.error.message}</span>}
  </span>
}
