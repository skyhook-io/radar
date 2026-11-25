import { useState } from 'react'
import { TerminalTab as SharedTerminalTab, type DockTab } from '@skyhook-io/k8s-ui'
import { getApiBase, getWsUrl } from '../../api/config'
import { MissingShellDebug } from './MissingShellDebug'
import { PodDebugSession } from './PodDebugSession'
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
  const [openedBase] = useState(getApiBase)
  const connection = debugSession ? debugSession.connectionId : openedConnection
  const base = debugSession?.apiBase ?? openedBase
  const createSession = async (container: string, failedPodUID?: string) => {
    const uid = debugSession?.podUID ?? failedPodUID
    if (getApiBase() !== base) throw new Error('The cluster connection changed. Reopen the Pod to start a terminal.')
    return {
      wsUrl: withConnectionParam(getWsUrl(`/pods/${namespace}/${podName}/exec?container=${encodeURIComponent(container)}${uid ? `&uid=${encodeURIComponent(uid)}` : ''}${shell ? `&shell=${encodeURIComponent(shell)}` : ''}`), connection),
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
      renderDebugAction={shell ? undefined : debugSession ? () => <p className="max-w-md text-xs text-theme-text-secondary">
        This debug image has no usable shell. Open Pod details to choose another running container or start a debugger with a different image.
      </p> : (target, uid) =>
        <MissingShellDebug key={JSON.stringify([uid,target])} namespace={namespace} name={podName} target={target} uid={uid} connection={connection} base={base} />}
      note={note}
      command={shell}
    />
  )
  return debugSession ? <PodDebugSession namespace={namespace} podName={podName} containerName={containerName} session={debugSession} isActive={isActive}>
    {terminal}
  </PodDebugSession> : terminal
}
