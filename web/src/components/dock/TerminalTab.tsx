import { useState } from 'react'
import { TerminalTab as SharedTerminalTab, type DockTab } from '@skyhook-io/k8s-ui'
import { getApiBase, getWsUrl } from '../../api/config'
import { useResource } from '../../api/client'
import { useNamespacedCapabilities } from '../../contexts/CapabilitiesContext'
import { useNavCustomization } from '../../context/NavCustomization'
import { PodDebugAction, type PodDebugTarget } from '../resources/PodDebugAction'
import { PodDebugSession } from './PodDebugSession'
import { connectionHeaderValue, useOpenedConnection, withConnectionParam } from '../../api/connectionBinding'

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
  const [openedBase] = useState(getApiBase)
  const connection = debugSession ? debugSession.connectionId : openedConnection
  const base = debugSession?.apiBase ?? openedBase
  const createSession = async (container: string) => {
    if (getApiBase() !== base) throw new Error('The cluster connection changed. Reopen the Pod to start a terminal.')
    return {
      wsUrl: withConnectionParam(getWsUrl(`/pods/${namespace}/${podName}/exec?container=${container}${shell ? `&shell=${encodeURIComponent(shell)}` : ''}`), connection),
    }
  }

  const terminal = (
    <SharedTerminalTab
      namespace={namespace}
      podName={podName}
      containerName={containerName}
      containers={containers}
      isActive={isActive}
      createSession={createSession}
      renderDebugAction={shell || debugSession ? undefined : (target, uid) =>
        <MissingShellDebug key={uid} namespace={namespace} name={podName} target={target} uid={uid} connection={connection} base={base} />}
      note={note}
      command={shell}
    />
  )
  return debugSession ? <PodDebugSession namespace={namespace} podName={podName} containerName={containerName} session={debugSession} isActive={isActive}>
    {terminal}
  </PodDebugSession> : terminal
}

function MissingShellDebug({ namespace, name, uid, target, connection, base }: {
  namespace: string; name: string; uid: string; target: string; connection?: string; base: string
}) {
  const { podDebugImage } = useNamespacedCapabilities(namespace)
  const { embedded } = useNavCustomization()
  const eligible = !!podDebugImage && !embedded && getApiBase() === base && connectionHeaderValue() === connection
  const { data: pod } = useResource<PodDebugTarget>('pods', namespace, name, undefined, { enabled: eligible })
  if (!eligible || !pod || pod.metadata.uid !== uid) return null
  return <PodDebugAction pod={pod} defaultImage={podDebugImage} failedContainer={target} />
}
