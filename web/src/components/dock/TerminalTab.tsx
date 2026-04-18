import { TerminalTab as SharedTerminalTab } from '@skyhook-io/k8s-ui'
import { apiUrl, getWsUrl } from '../../api/config'

interface TerminalTabProps {
  namespace: string
  podName: string
  containerName: string
  containers: string[]
  isActive?: boolean
}

export function TerminalTab({ namespace, podName, containerName, containers, isActive }: TerminalTabProps) {
  const createSession = (container: string) =>
    Promise.resolve({
      wsUrl: getWsUrl(`/pods/${namespace}/${podName}/exec?container=${container}`),
    })

  const createDebugContainer = async (targetContainer: string) => {
    const response = await fetch(apiUrl(`/pods/${namespace}/${podName}/debug`), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ targetContainer, image: 'busybox:latest' }),
    })
    if (!response.ok) {
      const err = await response.json().catch(() => ({ error: 'Unknown error' }))
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
      createDebugContainer={createDebugContainer}
    />
  )
}
