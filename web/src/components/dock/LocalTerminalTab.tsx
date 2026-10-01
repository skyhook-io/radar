import { useCallback, useRef, useState } from 'react'
import { AlertBanner, ClusterName, LocalTerminalTab as SharedLocalTerminalTab, Tooltip, parseContextName, useDock, useOpenLocalTerminal, type LocalTerminalSessionInfo } from '@skyhook-io/k8s-ui'
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
    <div className="h-full flex flex-col">
      {mismatched && (
        <div className="shrink-0 px-2 pt-2" role="status">
          <AlertBanner
            variant="warning"
            title="Radar is showing another context"
            message={<>Radar is showing <strong>{connection.context}</strong>. This terminal was opened for <strong>{context}</strong>.</>}
          >
            <button className="mt-2 btn-brand px-2 py-1 text-xs" onClick={() => openLocalTerminal()}>
              New terminal for {parseContextName(connection.context).clusterName}
            </button>
          </AlertBanner>
        </div>
      )}
      <div className="flex-1 min-h-0">
        <SharedLocalTerminalTab
          isActive={isActive}
          createSession={createSession}
          initialCommand={initialCommand}
          onSessionInfo={handleSessionInfo}
          toolbarExtra={
            context ? (
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
            )
          }
        />
      </div>
    </div>
  )
}
