import { useState, type ReactNode } from 'react'
import { AlertTriangle } from 'lucide-react'
import { pluralize } from '@skyhook-io/k8s-ui'
import { useSwitchContext, fetchSessionCounts, type SessionCounts } from '../api/client'
import { useContextSwitch } from '../context/ContextSwitchContext'
import { useToast } from './ui/Toast'
import { useDock } from './dock'
import type { ContextInfo, SelectedResource } from '../types'
import { parseContextForSwitcher } from '../utils/context-name'

function shouldSuppressSwitchErrorToast(error: unknown): boolean {
  const message = error instanceof Error ? error.message : ''
  return message.includes('cluster connection failed:')
}

interface PendingSwitch {
  context: ContextInfo
  openAfter?: SelectedResource
}

// The one way to switch contexts from the UI: the switching overlay, a
// confirmation when port-forwards or terminals would be killed, and an error
// toast when the switch fails. `openAfter` is opened once the new context is
// live. Callers render `confirmDialog`.
export function useContextSwitchFlow() {
  const switchContext = useSwitchContext()
  const { startSwitch, endSwitch, setOpenAfterSwitch } = useContextSwitch()
  const { showError } = useToast()
  const { tabs } = useDock()
  const [pending, setPending] = useState<PendingSwitch | null>(null)
  const [sessionCounts, setSessionCounts] = useState<SessionCounts | null>(null)

  const performSwitch = async ({ context, openAfter }: PendingSwitch) => {
    const parsed = parseContextForSwitcher(context)
    startSwitch({
      raw: parsed.raw,
      provider: parsed.provider,
      account: parsed.account,
      region: parsed.region,
      clusterName: parsed.clusterName,
    })
    setOpenAfterSwitch(openAfter ? { context: context.name, resource: openAfter } : null)
    try {
      await switchContext.mutateAsync({ name: context.name })
    } catch (error) {
      console.error('Failed to switch context:', error)
      setOpenAfterSwitch(null)
      endSwitch()
      // Backend may not transition to StateDisconnected on client-side errors
      // (network, timeout) — without this toast the user gets no feedback.
      if (!shouldSuppressSwitchErrorToast(error)) {
        const message = error instanceof Error ? error.message : 'Unknown error'
        showError('Failed to switch context', message)
      }
    }
  }

  const requestSwitch = async (context: ContextInfo, openAfter?: SelectedResource) => {
    if (context.isCurrent || switchContext.isPending) return
    // Active sessions (port forwards from API + terminal tabs from dock) get
    // a confirmation prompt — switching contexts kills both.
    try {
      const counts = await fetchSessionCounts()
      const terminalTabs = tabs.filter(t => t.type === 'terminal').length
      const total = counts.portForwards + terminalTabs
      if (total > 0) {
        setSessionCounts({ ...counts, execSessions: terminalTabs, total })
        setPending({ context, openAfter })
        return
      }
    } catch (error) {
      // Session-counts is best-effort; failing it shouldn't block the user.
      // But warn — if there ARE active sessions we couldn't see, the switch
      // will silently kill them.
      console.error('Failed to check sessions:', error)
      showError(
        'Could not check active sessions',
        'Switching anyway. Any open port-forwards or terminals will be terminated.',
      )
    }
    performSwitch({ context, openAfter })
  }

  const confirm = () => {
    if (pending) performSwitch(pending)
    setPending(null)
  }

  const cancel = () => {
    setPending(null)
    setSessionCounts(null)
  }

  const confirmDialog: ReactNode = pending && sessionCounts ? (
    <div className="fixed inset-0 z-[100] flex items-center justify-center bg-black/50">
      <div className="bg-theme-surface border border-theme-border rounded-lg shadow-xl max-w-md mx-4 overflow-hidden">
        <div className="px-4 py-3 border-b border-theme-border flex items-center gap-2">
          <AlertTriangle className="w-5 h-5 text-amber-400" />
          <span className="font-medium text-theme-text-primary">Active Sessions</span>
        </div>
        <div className="px-4 py-4">
          <p className="text-sm text-theme-text-secondary mb-3">
            Switching contexts will terminate active sessions:
          </p>
          <ul className="text-sm text-theme-text-primary space-y-1 mb-4">
            {sessionCounts.portForwards > 0 && (
              <li className="flex items-center gap-2">
                <span className="w-1.5 h-1.5 rounded-full bg-blue-400" />
                {pluralize(sessionCounts.portForwards, 'port forward')}
              </li>
            )}
            {sessionCounts.execSessions > 0 && (
              <li className="flex items-center gap-2">
                <span className="w-1.5 h-1.5 rounded-full bg-green-400" />
                {pluralize(sessionCounts.execSessions, 'terminal session')}
              </li>
            )}
          </ul>
          <p className="text-xs text-theme-text-tertiary">
            Switch to: <span className="text-theme-text-secondary">{parseContextForSwitcher(pending.context).clusterName}</span>
          </p>
        </div>
        <div className="px-4 py-3 border-t border-theme-border flex justify-end gap-2">
          <button
            onClick={cancel}
            className="px-3 py-1.5 text-sm rounded-md bg-theme-elevated hover:bg-theme-hover text-theme-text-secondary transition-colors"
          >
            Cancel
          </button>
          <button
            onClick={confirm}
            className="px-3 py-1.5 text-sm rounded-md bg-amber-500 hover:bg-amber-600 text-white transition-colors"
          >
            Switch Anyway
          </button>
        </div>
      </div>
    </div>
  ) : null

  return {
    requestSwitch,
    isPending: switchContext.isPending,
    error: switchContext.isError ? switchContext.error : null,
    confirmDialog,
  }
}
