import { useCallback, useRef, useState } from 'react'
import { AlertTriangle } from 'lucide-react'
import { useQueryClient } from '@tanstack/react-query'
import { ClusterName, LocalTerminalTab as SharedLocalTerminalTab, Tooltip, parseContextName, useDock, type LocalTerminalSessionInfo } from '@skyhook-io/k8s-ui'
import { getWsUrl } from '../../api/config'
import { useConnection } from '../../context/ConnectionContext'
import { useOpenLocalTerminal } from './DockContext'

interface LocalTerminalTabProps {
  tabId: string
  title: string
  intendedContext?: string
  isActive?: boolean
  initialCommand?: string
}

export function LocalTerminalTab({ tabId, title, intendedContext, isActive, initialCommand }: LocalTerminalTabProps) {
  const { connection } = useConnection()
  const queryClient = useQueryClient()
  const { setTabTitle } = useDock()
  const openLocalTerminal = useOpenLocalTerminal()
  const baseTitle = useRef(title)
  const [sessionInfo, setSessionInfo] = useState<LocalTerminalSessionInfo | null>(null)
  const handleSessionInfo = useCallback((info: LocalTerminalSessionInfo | null) => {
    if (!info) return
    setSessionInfo(info)
    const suffix = info.kubeconfigIsolated ? parseContextName(info.context).clusterName : 'context not confirmed'
    setTabTitle(tabId, `${baseTitle.current} · ${suffix}`, info.kubeconfigIsolated ? info.context : undefined)
  }, [tabId, setTabTitle])
  const unconfirmed = sessionInfo !== null && !sessionInfo.kubeconfigIsolated
  const contextNotice = unconfirmed
    ? `Requested for ${intendedContext || 'no active context'}; kubeconfig not confirmed.`
    : `This terminal is for ${intendedContext || 'no active context'}.`
  const context = sessionInfo?.kubeconfigIsolated ? sessionInfo.context : null
  const mismatched = intendedContext !== undefined && !!connection.context && intendedContext !== connection.context
  const createSession = async () => {
    if (intendedContext === undefined) throw new Error('No context selected. Open a new terminal for the selected context.')
    return { wsUrl: getWsUrl(`/local-terminal?expectedContext=${encodeURIComponent(intendedContext)}`) }
  }

  return (
    <SharedLocalTerminalTab
      isActive={isActive}
      createSession={createSession}
      initialCommand={initialCommand}
      canConnect={() => intendedContext !== undefined && connection.context === intendedContext && (!!intendedContext || connection.state === 'disconnected')}
      onConnectionError={() => { void queryClient.invalidateQueries({ queryKey: ['connection-status'] }) }}
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
          ) : !sessionInfo && intendedContext ? (
            <Tooltip content={<>A new shell has been requested for <strong>{intendedContext}</strong>.</>} wrapperClassName="min-w-0">
              <span className="flex items-center gap-1 min-w-0 text-xs text-theme-text-secondary">
                <span className="shrink-0">Requested:</span>
                <ClusterName name={intendedContext} noBadge noTooltip />
              </span>
            </Tooltip>
          ) : intendedContext === undefined ? (
            <span className="text-xs text-warning-text whitespace-nowrap">No context selected</span>
          ) : (
            <Tooltip content={sessionInfo
              ? 'Radar could not create a temporary kubeconfig. This shell uses the original or inherited kubeconfig.'
              : 'The server has not reported this terminal’s kubeconfig.'}>
              <span className="text-xs text-warning-text">Context not confirmed</span>
            </Tooltip>
          )}
          {mismatched && (
            <Tooltip content={<>Radar is showing <strong>{connection.context}</strong>. {contextNotice} Switch back to reconnect, or open a new terminal.</>} wrapperClassName="shrink-0">
              <span
                role="status"
                aria-label={`Radar is showing ${connection.context}. ${contextNotice}`}
                className="flex items-center gap-1 text-xs text-warning-text whitespace-nowrap"
              >
                <AlertTriangle className="w-3 h-3" />
                {unconfirmed ? 'Requested context differs' : 'Different context'}
              </span>
            </Tooltip>
          )}
          {connection.context && (mismatched || intendedContext === undefined) && (
            <Tooltip content={<>Open a new terminal for <strong>{connection.context}</strong>.</>} wrapperClassName="shrink-0">
              <button className="btn-brand px-2 py-0.5 text-xs whitespace-nowrap" aria-label={`New terminal for ${connection.context}`} onClick={() => openLocalTerminal()}>
                New terminal
              </button>
            </Tooltip>
          )}
        </div>
      }
    />
  )
}
