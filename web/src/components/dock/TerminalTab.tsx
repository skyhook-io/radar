import { TerminalTab as SharedTerminalTab } from '@skyhook-io/k8s-ui'
import { apiUrl, getWsUrl } from '../../api/config'
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
  const createSession = (container: string) =>
    Promise.resolve({
      wsUrl: getWsUrl(`/pods/${namespace}/${podName}/exec?container=${container}${shell ? `&shell=${encodeURIComponent(shell)}` : ''}`),
    })

  const createDebugContainer = async (targetContainer: string) => {
    const response = await fetch(apiUrl(`/pods/${namespace}/${podName}/debug`), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
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
    />
  )
}
