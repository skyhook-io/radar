import { TerminalTab as SharedTerminalTab } from '@skyhook-io/k8s-ui'
import { apiUrl, getWsUrl } from '../../api/config'
import { apiFetch } from '../../api/client'
import { connectionHeaders, useOpenedConnection, withConnectionParam } from '../../api/connectionBinding'
import { readErrorBody } from '../../api/httpErrors'

interface TerminalTabProps {
  namespace: string
  podName: string
  containerName: string
  containers: string[]
  isActive?: boolean
  shell?: string
  note?: string
}

export function TerminalTab({ namespace, podName, containerName, containers, isActive, shell, note }: TerminalTabProps) {
  const connection = useOpenedConnection()
  const createSession = (container: string) =>
    Promise.resolve({
      wsUrl: withConnectionParam(getWsUrl(`/pods/${namespace}/${podName}/exec?container=${container}${shell ? `&shell=${encodeURIComponent(shell)}` : ''}`), connection),
    })

  const createDebugContainer = async (targetContainer: string) => {
    const response = await apiFetch(apiUrl(`/pods/${namespace}/${podName}/debug`), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', ...connectionHeaders(connection) },
      body: JSON.stringify({ targetContainer }),
    })
    if (!response.ok) {
      const err = await readErrorBody(response)
      throw new Error(err.error || `HTTP ${response.status}`)
    }
    return response.json()
  }

  return (
    <SharedTerminalTab
      namespace={namespace}
      podName={podName}
      containerName={containerName}
      containers={containers}
      isActive={isActive}
      createSession={createSession}
      createDebugContainer={shell ? undefined : createDebugContainer}
      note={note}
      command={shell}
    />
  )
}
