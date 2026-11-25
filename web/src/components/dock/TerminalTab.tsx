import { useState } from 'react'
import { Link } from 'react-router-dom'
import { relatedResourcePath } from '../../utils/navigation'
import { TerminalTab as SharedTerminalTab, type DockTab } from '@skyhook-io/k8s-ui'
import { getApiBase, getWsUrl } from '../../api/config'
import { MissingShellDebug } from './MissingShellDebug'
import { PodDebugSession } from './PodDebugSession'
import { useCurrentConnection, useOpenedConnection, withConnectionParam } from '../../api/connectionBinding'

interface TerminalTabProps {
  namespace: string
  podName: string
  containerName: string
  containers: string[]
  isActive?: boolean
  shell?: string
  note?: string
  debugSession?: DockTab['debugSession']
  onLeaveTerminal?: () => void
}

export function TerminalTab({ namespace, podName, containerName, containers, isActive, shell, note, debugSession, onLeaveTerminal }: TerminalTabProps) {
  const openedConnection = useOpenedConnection()
  const [openedBase] = useState(getApiBase)
  const connection = debugSession ? debugSession.connectionId : openedConnection
  const base = debugSession?.apiBase ?? openedBase
  const currentConnection = useCurrentConnection()
  const podDetailsLink = currentConnection === connection && getApiBase() === base && <Link
    className="mb-2 inline-block text-xs text-accent underline underline-offset-2"
    title="Close this terminal tab and open the current Pod"
    onClick={event => { if (!event.ctrlKey && !event.metaKey && !event.shiftKey && !event.altKey && event.button === 0) onLeaveTerminal?.() }}
    to={relatedResourcePath({ kind: 'Pod', namespace, name: podName })}
  >Open current Pod details</Link>
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
      podDetailsLink={podDetailsLink}
      renderDebugAction={shell ? undefined : debugSession ? () => <p className="max-w-md text-xs text-theme-text-secondary">
        This debug image has no usable shell. Open Pod details to choose another running container or start a debugger with a different image. {podDetailsLink}
      </p> : (target, uid) =>
        <MissingShellDebug key={JSON.stringify([uid,target])} namespace={namespace} name={podName} target={target} uid={uid} connection={connection} base={base} podDetailsLink={podDetailsLink} />}
      note={note}
      command={shell}
    />
  )
  return debugSession ? <PodDebugSession namespace={namespace} podName={podName} containerName={containerName} session={debugSession} isActive={isActive} podDetailsLink={podDetailsLink}>
    {terminal}
  </PodDebugSession> : terminal
}
