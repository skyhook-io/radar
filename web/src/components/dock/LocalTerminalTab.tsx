import { useCallback, useRef, useState } from 'react'
import { AlertTriangle } from 'lucide-react'
import { ClusterName, LocalTerminalTab as SharedLocalTerminalTab, Tooltip, parseContextName, useDock, useOpenLocalTerminal, type LocalTerminalSessionInfo } from '@skyhook-io/k8s-ui'
import { getWsUrl } from '../../api/config'
import { useConnection } from '../../context/ConnectionContext'

interface LocalTerminalTabProps {
  tabId: string
  title: string
  isActive?: boolean
  initialCommand?: string
}

export function LocalTerminalTab({ tabId, title, isActive, initialCommand }: LocalTerminalTabProps) {
  const { connection } = useConnection()
  const { setTabTitle } = useDock()
  const openLocalTerminal = useOpenLocalTerminal()
  const baseTitle = useRef(title)
  const [sessionInfo, setSessionInfo] = useState<LocalTerminalSessionInfo | null>(null)
  const handleSessionInfo = useCallback((info: LocalTerminalSessionInfo | null) => {
    setSessionInfo(info)
    const suffix = info
      ? info.kubeconfigIsolated ? parseContextName(info.context).clusterName : 'context not confirmed'
      : null
    setTabTitle(tabId, suffix ? `${baseTitle.current} · ${suffix}` : baseTitle.current, info?.kubeconfigIsolated ? info.context : undefined)
  }, [tabId, setTabTitle])
  const context = sessionInfo?.kubeconfigIsolated ? sessionInfo.context : null
  const mismatched = context && connection.context && context !== connection.context
  const createSession = () =>
    Promise.resolve({
      wsUrl: getWsUrl('/local-terminal'),
    })

  return (
    <SharedLocalTerminalTab
      isActive={isActive}
      createSession={createSession}
      initialCommand={initialCommand}
      onSessionInfo={handleSessionInfo}
      toolbarExtra={
        <div className="flex items-center gap-2 min-w-0">
          {context ? (
            <Tooltip content={<>Radar supplied a temporary kubeconfig for <strong>{context}</strong>. Shell settings and commands can override it.</>} wrapperClassName="min-w-0">
              <span className="flex items-center gap-1 min-w-0 text-xs text-theme-text-secondary">
                <span className="shrink-0">Opened for:</span>
                <ClusterName name={context} noBadge noTooltip />
              </span>
            </Tooltip>
          ) : (
            <Tooltip content={sessionInfo
              ? 'Radar could not create a temporary kubeconfig. This shell uses the original or inherited kubeconfig.'
              : 'The server has not reported this terminal’s kubeconfig.'}>
              <span className="text-xs text-warning-text">Context not confirmed</span>
            </Tooltip>
          )}
          {mismatched && (
            <>
              <Tooltip content={<>Radar is showing <strong>{connection.context}</strong>. This terminal was opened for <strong>{context}</strong>.</>} wrapperClassName="shrink-0">
                <span
                  role="status"
                  aria-label={`Radar is showing ${connection.context}. This terminal was opened for ${context}.`}
                  className="flex items-center gap-1 text-xs text-warning-text whitespace-nowrap"
                >
                  <AlertTriangle className="w-3 h-3" />
                  Different context
                </span>
              </Tooltip>
              <Tooltip content={<>Open a new terminal for <strong>{connection.context}</strong>.</>} wrapperClassName="shrink-0">
                <button className="btn-brand px-2 py-0.5 text-xs whitespace-nowrap" aria-label={`New terminal for ${connection.context}`} onClick={() => openLocalTerminal()}>
                  New terminal
                </button>
              </Tooltip>
            </>
          )}
        </div>
      }
    />
  )
}
