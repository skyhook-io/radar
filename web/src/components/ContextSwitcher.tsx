import { useMemo, useState } from 'react'
import { AlertTriangle } from 'lucide-react'
import {
  ClusterSwitcher,
  type ClusterSwitcherItem,
  pluralize,
} from '@skyhook-io/k8s-ui'
import { useContexts, useSwitchContext, useClusterInfo, fetchSessionCounts, type SessionCounts } from '../api/client'
import { useContextSwitch } from '../context/ContextSwitchContext'
import { useToast } from '../components/ui/Toast'
import { useDock } from '../components/dock'
import type { ContextInfo } from '../types'
import { parseContextName, type ParsedContextName } from '../utils/context-name'

interface ContextSwitcherProps {
  className?: string
}

interface ParsedContext extends ParsedContextName {
  context: ContextInfo
}

export function ContextSwitcher({ className = '' }: ContextSwitcherProps) {
  const [showConfirm, setShowConfirm] = useState(false)
  const [pendingSwitch, setPendingSwitch] = useState<ParsedContext | null>(null)
  const [sessionCounts, setSessionCounts] = useState<SessionCounts | null>(null)

  const { data: contexts, isLoading: contextsLoading } = useContexts()
  const { data: clusterInfo } = useClusterInfo()
  const switchContext = useSwitchContext()
  const { startSwitch, endSwitch } = useContextSwitch()
  const { showError } = useToast()
  const { tabs } = useDock()

  // Parse contexts and decide whether to render group headers (multi-account only).
  const { parsedById, hasMultipleAccounts } = useMemo(() => {
    if (!contexts) return { parsedById: new Map<string, ParsedContext>(), hasMultipleAccounts: false }
    const parsed: ParsedContext[] = contexts.map(ctx => ({ context: ctx, ...parseContextName(ctx.name) }))
    const accounts = new Set(parsed.map(p => `${p.provider}:${p.account}`))
    const byId = new Map<string, ParsedContext>()
    for (const p of parsed) byId.set(p.context.name, p)
    return { parsedById: byId, hasMultipleAccounts: accounts.size > 1 }
  }, [contexts])

  // Map parsed contexts → generic ClusterSwitcher items, sorted GKE/EKS/AKS/Other → account → name.
  const items = useMemo<ClusterSwitcherItem[]>(() => {
    const order: Record<string, number> = { GKE: 0, EKS: 1, AKS: 2 }
    const arr = Array.from(parsedById.values())
    arr.sort((a, b) => {
      const oa = order[a.provider || ''] ?? 3
      const ob = order[b.provider || ''] ?? 3
      if (oa !== ob) return oa - ob
      const acc = (a.account || '').localeCompare(b.account || '')
      if (acc !== 0) return acc
      return a.clusterName.localeCompare(b.clusterName)
    })
    return arr.map(p => {
      const groupKey = `${p.provider || 'other'}:${p.account || 'default'}`
      const groupLabel = hasMultipleAccounts && p.provider
        ? `${p.provider}${p.account ? ` · ${p.account}` : ''}`
        : hasMultipleAccounts
          ? 'Other'
          : undefined
      // `name` is the raw context — ClusterSwitcher renders it through
      // ClusterName, which collapses GKE/EKS/AKS shapes to the meaningful
      // tail. `secondary` shows the original raw when we collapsed it,
      // so users always see the full context at a glance (rather than
      // having to hover to reveal it).
      return {
        id: p.context.name,
        name: p.context.name,
        secondary: p.provider ? p.raw : undefined,
        badge: p.region || undefined,
        group: { key: groupKey, label: groupLabel },
      }
    })
  }, [parsedById, hasMultipleAccounts])

  const performSwitch = async (parsed: ParsedContext) => {
    startSwitch({
      raw: parsed.raw,
      provider: parsed.provider,
      account: parsed.account,
      region: parsed.region,
      clusterName: parsed.clusterName,
    })
    try {
      await switchContext.mutateAsync({ name: parsed.context.name })
    } catch (error) {
      console.error('Failed to switch context:', error)
      endSwitch()
      // Backend may not transition to StateDisconnected on client-side errors
      // (network, timeout) — without this toast the user gets no feedback.
      const message = error instanceof Error ? error.message : 'Unknown error'
      showError('Failed to switch context', message)
    }
  }

  const handleSelect = async (item: ClusterSwitcherItem) => {
    const parsed = parsedById.get(item.id)
    if (!parsed || parsed.context.isCurrent || switchContext.isPending) return

    // Active sessions (port forwards from API + terminal tabs from dock) get
    // a confirmation prompt — switching contexts kills both.
    try {
      const counts = await fetchSessionCounts()
      const terminalTabs = tabs.filter(t => t.type === 'terminal').length
      const total = counts.portForwards + terminalTabs
      if (total > 0) {
        setSessionCounts({ ...counts, execSessions: terminalTabs, total })
        setPendingSwitch(parsed)
        setShowConfirm(true)
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
    performSwitch(parsed)
  }

  const handleConfirmSwitch = () => {
    setShowConfirm(false)
    if (pendingSwitch) {
      performSwitch(pendingSwitch)
      setPendingSwitch(null)
    }
  }

  const handleCancelSwitch = () => {
    setShowConfirm(false)
    setPendingSwitch(null)
    setSessionCounts(null)
  }

  // In-cluster mode renders a static badge instead of a switcher (only one
  // synthetic context, no kubeconfig to choose from).
  const isInClusterMode = contexts?.length === 1 && contexts[0].name === 'in-cluster'
  if (isInClusterMode) {
    return (
      <div className={`flex items-center gap-2 ${className}`}>
        <span className="px-2 py-1 bg-theme-elevated rounded text-sm font-medium text-blue-300">
          in-cluster
        </span>
      </div>
    )
  }

  const currentRaw = clusterInfo?.context || contexts?.find(c => c.isCurrent)?.name || 'Unknown'
  const currentId = contexts?.find(c => c.isCurrent)?.name

  return (
    <>
      <ClusterSwitcher
        className={className}
        currentId={currentId}
        currentName={currentRaw}
        items={items}
        onSelect={handleSelect}
        loading={switchContext.isPending}
        disabled={contextsLoading}
        searchable={items.length > 1}
        showGroupHeaders={hasMultipleAccounts}
        errorSlot={
          switchContext.isError ? (
            <span className="text-xs text-red-400">{switchContext.error?.message}</span>
          ) : undefined
        }
      />

      {showConfirm && sessionCounts && pendingSwitch && (
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
                Switch to: <span className="text-theme-text-secondary">{pendingSwitch.clusterName}</span>
              </p>
            </div>
            <div className="px-4 py-3 border-t border-theme-border flex justify-end gap-2">
              <button
                onClick={handleCancelSwitch}
                className="px-3 py-1.5 text-sm rounded-md bg-theme-elevated hover:bg-theme-hover text-theme-text-secondary transition-colors"
              >
                Cancel
              </button>
              <button
                onClick={handleConfirmSwitch}
                className="px-3 py-1.5 text-sm rounded-md bg-amber-500 hover:bg-amber-600 text-white transition-colors"
              >
                Switch Anyway
              </button>
            </div>
          </div>
        </div>
      )}
    </>
  )
}
