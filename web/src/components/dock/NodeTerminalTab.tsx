import { NodeTerminalTab as SharedNodeTerminalTab, type NodeTerminalTabProps as SharedNodeTerminalTabProps } from '@skyhook-io/k8s-ui'
import { apiUrl, getWsUrl, getAuthHeaders, getCredentialsMode } from '../../api/config'

interface NodeTerminalTabProps {
  nodeName: string
  isActive?: boolean
}

export function NodeTerminalTab({ nodeName, isActive }: NodeTerminalTabProps) {
  const createNodeDebugPod = async (name: string) => {
    const response = await fetch(apiUrl(`/nodes/${encodeURIComponent(name)}/debug`), {
      method: 'POST',
      credentials: getCredentialsMode(),
      headers: { 'Content-Type': 'application/json', ...getAuthHeaders() },
      body: JSON.stringify({}),
    })
    if (!response.ok) {
      const err = await response.json().catch(() => ({ error: 'Unknown error' }))
      throw new Error(err.error || `HTTP ${response.status}`)
    }
    return response.json()
  }

  const cleanupNodeDebugPod: SharedNodeTerminalTabProps['cleanupNodeDebugPod'] = async (name, pod) => {
    const query = new URLSearchParams({ namespace: pod.namespace, podName: pod.podName, uid: pod.uid })
    try {
      const response = await fetch(apiUrl(`/nodes/${encodeURIComponent(name)}/debug?${query}`), {
        method: 'DELETE',
        credentials: getCredentialsMode(),
        headers: getAuthHeaders(),
        keepalive: true,
      })
      if (!response.ok) throw new Error(`HTTP ${response.status}`)
    } catch (err) {
      console.warn(`[NodeTerminal] Failed to cleanup debug pod for node ${name}:`, err)
    }
  }

  const createSession = async (namespace: string, podName: string, containerName: string) => ({
    wsUrl: getWsUrl(`/pods/${encodeURIComponent(namespace)}/${encodeURIComponent(podName)}/exec?container=${encodeURIComponent(containerName)}`),
  })

  return (
    <SharedNodeTerminalTab
      nodeName={nodeName}
      isActive={isActive}
      createNodeDebugPod={createNodeDebugPod}
      cleanupNodeDebugPod={cleanupNodeDebugPod}
      createSession={createSession}
    />
  )
}
