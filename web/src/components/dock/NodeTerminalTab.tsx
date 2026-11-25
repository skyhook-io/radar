import { NodeTerminalTab as SharedNodeTerminalTab, type NodeTerminalTabProps as SharedNodeTerminalTabProps } from '@skyhook-io/k8s-ui'
import { apiUrl, getWsUrl } from '../../api/config'
import { apiFetch } from '../../api/client'
import { connectionHeaders, useOpenedConnection, withConnectionParam } from '../../api/connectionBinding'
import { readErrorBody } from '../../api/httpErrors'

interface NodeTerminalTabProps {
  nodeName: string
  isActive?: boolean
}

export function NodeTerminalTab({ nodeName, isActive }: NodeTerminalTabProps) {
  const connection = useOpenedConnection()
  const createNodeDebugPod = async (name: string) => {
    const response = await apiFetch(apiUrl(`/nodes/${encodeURIComponent(name)}/debug`), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', ...connectionHeaders(connection) },
      body: JSON.stringify({}),
    })
    if (!response.ok) {
      const err = await readErrorBody(response)
      throw new Error(err.error || `HTTP ${response.status}`)
    }
    return response.json()
  }

  const cleanupNodeDebugPod: SharedNodeTerminalTabProps['cleanupNodeDebugPod'] = async (name, pod) => {
    const query = new URLSearchParams({ namespace: pod.namespace, podName: pod.podName, uid: pod.uid })
    try {
      const response = await apiFetch(apiUrl(`/nodes/${encodeURIComponent(name)}/debug?${query}`), {
        method: 'DELETE',
        keepalive: true,
      })
      if (!response.ok) throw new Error(`HTTP ${response.status}`)
    } catch (err) {
      console.warn(`[NodeTerminal] Failed to cleanup debug pod for node ${name}:`, err)
    }
  }

  const createSession = async (namespace: string, podName: string, containerName: string) => ({
    wsUrl: withConnectionParam(getWsUrl(`/pods/${encodeURIComponent(namespace)}/${encodeURIComponent(podName)}/exec?container=${encodeURIComponent(containerName)}`), connection),
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
