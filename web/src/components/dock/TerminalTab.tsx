import { TerminalTab as SharedTerminalTab, type DockTab } from '@skyhook-io/k8s-ui'
import { getWsUrl } from '../../api/config'
import { createPodDebugContainer } from '../../api/podDebug'
import { useOpenedConnection, withConnectionParam } from '../../api/connectionBinding'

interface TerminalTabProps {
  namespace: string
  podName: string
  containerName: string
  containers: string[]
  isActive?: boolean
  shell?: string
  note?: string
  debugSession?: DockTab['debugSession']
}

export function TerminalTab({ namespace, podName, containerName, containers, isActive, shell, note, debugSession }: TerminalTabProps) {
  const openedConnection = useOpenedConnection()
  const connection = debugSession ? debugSession.connectionId : openedConnection
  const createSession = (container: string) =>
    Promise.resolve({
      wsUrl: withConnectionParam(getWsUrl(`/pods/${namespace}/${podName}/exec?container=${container}${shell ? `&shell=${encodeURIComponent(shell)}` : ''}`), connection),
    })

  const createDebugContainer = (targetContainer: string) =>
    createPodDebugContainer(namespace, podName, connection, { targetContainer })

  return (
    <SharedTerminalTab
      namespace={namespace}
      podName={podName}
      containerName={containerName}
      containers={containers}
      isActive={isActive}
      createSession={createSession}
      createDebugContainer={shell || debugSession ? undefined : createDebugContainer}
      note={note}
      command={shell}
    />
  )
}
