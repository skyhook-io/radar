import { useState } from 'react'
import { useContexts } from '../api/client'
import type { ConnectionState } from '../context/ConnectionContext'
import { useContextSwitch } from '../context/ContextSwitchContext'
import type { ContextInfo } from '../types'
import { parseContextForSwitcher } from '../utils/context-name'
import { ConnectionErrorView } from './ConnectionErrorView'
import { useContextSwitchFlow } from './useContextSwitchFlow'

function clusterLabel(contexts: ContextInfo[] | undefined, name: string): string {
  const match = contexts?.find(context => context.name === name)
  return match ? parseContextForSwitcher(match).clusterName : name
}

// A switch Radar refused before leaving the current cluster: the connection
// error screen for the cluster that didn't answer, with its fix steps. The
// current cluster is still connected underneath, so Back only closes this.
export function FailedSwitchView({ currentContext }: { currentContext: string }) {
  const { failedSwitch, setFailedSwitch } = useContextSwitch()
  const { data: contexts } = useContexts()
  const { requestSwitch, isPending, confirmDialog } = useContextSwitchFlow()
  // Retry checks for open sessions before the switch itself is pending; Back
  // stays disabled through that, so it can't close the screen and then switch.
  const [starting, setStarting] = useState(false)
  if (!failedSwitch) return null
  const target = contexts?.find(context => context.name === failedSwitch.context)
  const busy = starting || isPending

  return (
    <>
      <ConnectionErrorView
        connection={{
          state: 'disconnected',
          context: failedSwitch.context,
          error: failedSwitch.error,
          errorType: failedSwitch.errorType,
        }}
        onRetry={() => {
          if (!target || busy) return
          setStarting(true)
          void requestSwitch(target).finally(() => setStarting(false))
        }}
        isRetrying={busy}
        back={{ label: clusterLabel(contexts, currentContext), onBack: () => setFailedSwitch(null), disabled: busy }}
        variant="switch"
      />
      {confirmDialog}
    </>
  )
}

// Disconnected after leaving a cluster that worked (an older Radar server, or
// a new cluster that answered and then failed to load): Back switches to the
// last cluster that was connected.
export function DisconnectedView({
  connection,
  onRetry,
  isRetrying,
  lastConnectedContext,
}: {
  connection: ConnectionState
  onRetry: () => void
  isRetrying: boolean
  lastConnectedContext: string | null
}) {
  const { data: contexts } = useContexts()
  const { requestSwitch, isPending, confirmDialog } = useContextSwitchFlow()
  const previous = lastConnectedContext && lastConnectedContext !== connection.context
    ? contexts?.find(context => context.name === lastConnectedContext)
    : undefined

  return (
    <>
      <ConnectionErrorView
        connection={connection}
        onRetry={onRetry}
        isRetrying={isRetrying}
        back={previous
          ? { label: parseContextForSwitcher(previous).clusterName, onBack: () => void requestSwitch(previous), disabled: isPending }
          : undefined}
      />
      {confirmDialog}
    </>
  )
}
