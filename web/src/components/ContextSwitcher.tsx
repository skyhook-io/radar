import { useState, useRef, useEffect, useMemo } from 'react'
import { ChevronDown, Check, Loader2, Server, AlertTriangle, XCircle, Search, X } from 'lucide-react'
import { useContexts, useSwitchContext, useClusterInfo, fetchSessionCounts, type SessionCounts } from '../api/client'
import { useContextSwitch } from '../context/ContextSwitchContext'
import { useDock } from '../components/dock'
import type { ContextInfo } from '../types'

interface SwitchError {
  contextName: string
  clusterName: string
  message: string
}

interface ContextSwitcherProps {
  className?: string
}

interface ParsedContext {
  context: ContextInfo
  provider: 'GKE' | 'EKS' | 'AKS' | null
  account: string | null // Project (GCP) or Account ID (AWS) or Resource Group (Azure)
  region: string | null
  clusterName: string
  raw: string // Original context name
}

// Parse context name to extract structured fields
function parseContextName(name: string): Omit<ParsedContext, 'context'> {
  // GKE format: gke_{project}_{region}_{cluster-name}
  const gkeMatch = name.match(/^gke_([^_]+)_([^_]+)_(.+)$/)
  if (gkeMatch) {
    const [, project, region, cluster] = gkeMatch
    return {
      provider: 'GKE',
      account: project,
      region,
      clusterName: cluster,
      raw: name,
    }
  }

  // EKS ARN format: arn:aws:eks:{region}:{account}:cluster/{cluster-name}
  const eksArnMatch = name.match(/^arn:aws:eks:([^:]+):(\d+):cluster\/(.+)$/)
  if (eksArnMatch) {
    const [, region, account, cluster] = eksArnMatch
    return {
      provider: 'EKS',
      account,
      region,
      clusterName: cluster,
      raw: name,
    }
  }

  // eksctl format: {user}@{cluster}.{region}.eksctl.io
  const eksctlMatch = name.match(/^(.+)@([^.]+)\.([^.]+)\.eksctl\.io$/)
  if (eksctlMatch) {
    const [, , cluster, region] = eksctlMatch
    return {
      provider: 'EKS',
      account: 'eksctl',
      region,
      clusterName: cluster,
      raw: name,
    }
  }

  // AKS format: try to detect
  if (name.toLowerCase().includes('aks') || name.includes('.azure.') || name.includes('azurecr')) {
    return {
      provider: 'AKS',
      account: null,
      region: null,
      clusterName: name,
      raw: name,
    }
  }

  // Other/unknown - just use the name as cluster name
  return {
    provider: null,
    account: null,
    region: null,
    clusterName: name,
    raw: name,
  }
}

// Group contexts by provider, then by account
interface ContextGroup {
  provider: string | null
  account: string | null
  items: ParsedContext[]
}

export function ContextSwitcher({ className = '' }: ContextSwitcherProps) {
  const [isOpen, setIsOpen] = useState(false)
  const [search, setSearch] = useState('')
  const [highlightedIndex, setHighlightedIndex] = useState(-1)
  const [showConfirm, setShowConfirm] = useState(false)
  const [pendingSwitch, setPendingSwitch] = useState<ParsedContext | null>(null)
  const [sessionCounts, setSessionCounts] = useState<SessionCounts | null>(null)
  const [switchError, setSwitchError] = useState<SwitchError | null>(null)
  const dropdownRef = useRef<HTMLDivElement>(null)
  const searchInputRef = useRef<HTMLInputElement>(null)

  const { data: contexts, isLoading: contextsLoading } = useContexts()
  const { data: clusterInfo } = useClusterInfo()
  const switchContext = useSwitchContext()
  const { startSwitch, endSwitch } = useContextSwitch()
  const { tabs } = useDock()

  // Parse, group, and sort contexts
  const { groups, hasMultipleAccounts } = useMemo(() => {
    if (!contexts) return { groups: [], hasMultipleProviders: false, hasMultipleAccounts: false }

    // Parse all contexts
    const parsed: ParsedContext[] = contexts.map(ctx => ({
      context: ctx,
      ...parseContextName(ctx.name),
    }))

    // Check if we have multiple accounts (to decide whether to show group headers)
    const accounts = new Set(parsed.map(p => `${p.provider}:${p.account}`))
    const hasMultipleAccounts = accounts.size > 1

    // Group by provider + account
    const groupMap = new Map<string, ContextGroup>()
    for (const p of parsed) {
      const key = `${p.provider || 'other'}:${p.account || 'default'}`
      if (!groupMap.has(key)) {
        groupMap.set(key, { provider: p.provider, account: p.account, items: [] })
      }
      groupMap.get(key)!.items.push(p)
    }

    // Sort groups: GKE first, then EKS, then AKS, then Other
    // Within provider, sort by account name
    const providerOrder: Record<string, number> = { 'GKE': 0, 'EKS': 1, 'AKS': 2 }
    const groups = Array.from(groupMap.values()).sort((a, b) => {
      const orderA = providerOrder[a.provider || ''] ?? 3
      const orderB = providerOrder[b.provider || ''] ?? 3
      if (orderA !== orderB) return orderA - orderB
      return (a.account || '').localeCompare(b.account || '')
    })

    // Sort items within each group by cluster name
    for (const group of groups) {
      group.items.sort((a, b) => a.clusterName.localeCompare(b.clusterName))
    }

    return { groups, hasMultipleAccounts }
  }, [contexts])

  // Filter groups by search query
  const { filteredGroups, flatItems, itemIndexMap } = useMemo(() => {
    const filteredGroups = search.trim()
      ? groups
          .map(group => ({
            ...group,
            items: group.items.filter(item => {
              const searchLower = search.toLowerCase()
              return (
                item.clusterName.toLowerCase().includes(searchLower) ||
                item.raw.toLowerCase().includes(searchLower) ||
                (item.region && item.region.toLowerCase().includes(searchLower)) ||
                (item.account && item.account.toLowerCase().includes(searchLower))
              )
            }),
          }))
          .filter(group => group.items.length > 0)
      : groups

    const flatItems = filteredGroups.flatMap(g => g.items)
    const itemIndexMap = new Map<string, number>()
    flatItems.forEach((item, i) => itemIndexMap.set(item.context.name, i))

    return { filteredGroups, flatItems, itemIndexMap }
  }, [groups, search])

  // Reset search and highlight when dropdown opens/closes
  useEffect(() => {
    if (isOpen) {
      setSearch('')
      setHighlightedIndex(-1)
      requestAnimationFrame(() => {
        searchInputRef.current?.focus()
      })
    }
  }, [isOpen])

  // Reset highlighted index when filtered results change
  useEffect(() => {
    setHighlightedIndex(-1)
  }, [search])

  // Keyboard navigation for search
  const handleSearchKeyDown = (e: React.KeyboardEvent) => {
    switch (e.key) {
      case 'ArrowDown':
        e.preventDefault()
        setHighlightedIndex(prev => (prev < flatItems.length - 1 ? prev + 1 : prev))
        break
      case 'ArrowUp':
        e.preventDefault()
        setHighlightedIndex(prev => (prev > 0 ? prev - 1 : 0))
        break
      case 'Enter':
        e.preventDefault()
        if (highlightedIndex >= 0 && flatItems[highlightedIndex]) {
          handleContextSwitch(flatItems[highlightedIndex])
        } else if (flatItems.length > 0) {
          setHighlightedIndex(0)
        }
        break
      case 'Escape':
        e.preventDefault()
        setIsOpen(false)
        break
    }
  }

  // Scroll highlighted item into view
  useEffect(() => {
    if (!isOpen || highlightedIndex < 0 || !dropdownRef.current) return
    const highlighted = dropdownRef.current.querySelector('[data-highlighted="true"]')
    if (highlighted) {
      highlighted.scrollIntoView({ block: 'nearest' })
    }
  }, [highlightedIndex, isOpen])

  // Close dropdown when clicking outside
  useEffect(() => {
    function handleClickOutside(event: MouseEvent) {
      if (dropdownRef.current && !dropdownRef.current.contains(event.target as Node)) {
        setIsOpen(false)
      }
    }

    document.addEventListener('mousedown', handleClickOutside)
    return () => document.removeEventListener('mousedown', handleClickOutside)
  }, [])

  // Close dropdown on escape
  useEffect(() => {
    function handleEscape(event: KeyboardEvent) {
      if (event.key === 'Escape') {
        setIsOpen(false)
      }
    }

    document.addEventListener('keydown', handleEscape)
    return () => document.removeEventListener('keydown', handleEscape)
  }, [])

  // Check for active sessions and show confirmation if needed
  const handleContextSwitch = async (parsed: ParsedContext) => {
    if (parsed.context.isCurrent || switchContext.isPending) return

    setIsOpen(false)

    // Check for active sessions (port forwards from API + terminal tabs from dock)
    try {
      const counts = await fetchSessionCounts()
      const terminalTabs = tabs.filter(t => t.type === 'terminal').length
      const totalSessions = counts.portForwards + terminalTabs

      if (totalSessions > 0) {
        // Show confirmation dialog
        setSessionCounts({ ...counts, execSessions: terminalTabs, total: totalSessions })
        setPendingSwitch(parsed)
        setShowConfirm(true)
        return
      }
    } catch (error) {
      console.error('Failed to check sessions:', error)
      // Continue with switch even if check fails
    }

    // No active sessions, proceed with switch
    performSwitch(parsed)
  }

  // Actually perform the context switch
  const performSwitch = async (parsed: ParsedContext) => {
    // Clear any previous error
    setSwitchError(null)

    startSwitch({
      raw: parsed.raw,
      provider: parsed.provider,
      account: parsed.account,
      region: parsed.region,
      clusterName: parsed.clusterName,
    })
    try {
      await switchContext.mutateAsync({ name: parsed.context.name })
      // Success - endSwitch is called by the overlay when it detects success
    } catch (error) {
      console.error('Failed to switch context:', error)
      // On error, end the switch state so the overlay goes away
      endSwitch()
      // Show error dialog with context details
      setSwitchError({
        contextName: parsed.context.name,
        clusterName: parsed.clusterName,
        message: error instanceof Error ? error.message : 'Unknown error',
      })
    }
  }

  // Handle confirmation dialog actions
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

  // Get current context info - parse it to extract cluster name
  const currentContextRaw = clusterInfo?.context || contexts?.find(c => c.isCurrent)?.name || 'Unknown'
  const currentParsed = useMemo(() => parseContextName(currentContextRaw), [currentContextRaw])
  const currentDisplayName = currentParsed.clusterName

  // Check if in-cluster mode (only one context named "in-cluster")
  const isInClusterMode = contexts?.length === 1 && contexts[0].name === 'in-cluster'

  // If in-cluster mode, just show a static badge
  if (isInClusterMode) {
    return (
      <div className={`flex items-center gap-2 ${className}`}>
        <span className="px-2 py-1 bg-theme-elevated rounded text-sm font-medium text-blue-300">
          in-cluster
        </span>
      </div>
    )
  }

  return (
    <div className={`relative ${className}`} ref={dropdownRef}>
      {/* Trigger button */}
      <button
        onClick={() => setIsOpen(!isOpen)}
        disabled={switchContext.isPending || contextsLoading}
        className={`
          flex items-center gap-1.5 px-2.5 py-1.5
          bg-theme-elevated border border-theme-border rounded text-sm font-medium
          text-theme-text-primary hover:bg-theme-hover hover:border-theme-border-light
          transition-colors cursor-pointer
          disabled:opacity-50 disabled:cursor-not-allowed
        `}
        title={currentContextRaw}
      >
        {switchContext.isPending ? (
          <Loader2 className="w-3.5 h-3.5 animate-spin" />
        ) : (
          <Server className="w-3.5 h-3.5 text-theme-text-secondary" />
        )}
        <span className="max-w-[220px] truncate">
          {switchContext.isPending ? 'Switching...' : currentDisplayName}
        </span>
        <ChevronDown className={`w-3 h-3 transition-transform ${isOpen ? 'rotate-180' : ''}`} />
      </button>

      {/* Dropdown menu */}
      {isOpen && !contextsLoading && contexts && (
        <div className="absolute top-full left-0 mt-1 z-50 min-w-[280px] max-w-[420px] bg-theme-surface border border-theme-border-light rounded-lg shadow-xl overflow-hidden">
          {/* Search input */}
          {contexts.length > 1 && (
            <div className="p-2 border-b border-theme-border">
              <div className="relative">
                <Search className="absolute left-2 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-theme-text-tertiary" />
                <input
                  ref={searchInputRef}
                  type="text"
                  value={search}
                  onChange={(e) => setSearch(e.target.value)}
                  onKeyDown={handleSearchKeyDown}
                  placeholder="Search clusters..."
                  className="w-full bg-theme-base text-theme-text-primary text-xs rounded px-2 py-1.5 pl-7 pr-7 border border-theme-border-light focus:outline-none focus:ring-1 focus:ring-blue-500 placeholder:text-theme-text-tertiary"
                />
                {search && (
                  <button
                    type="button"
                    onClick={() => setSearch('')}
                    className="absolute right-2 top-1/2 -translate-y-1/2 text-theme-text-tertiary hover:text-theme-text-secondary"
                  >
                    <X className="w-3.5 h-3.5" />
                  </button>
                )}
              </div>
            </div>
          )}

          <div className="max-h-[400px] overflow-y-auto">
            {flatItems.length === 0 ? (
              <div className="px-3 py-6 text-center text-xs text-theme-text-tertiary">
                No clusters match "{search}"
              </div>
            ) : (
              filteredGroups.map((group, groupIndex) => {
                const showHeader = hasMultipleAccounts
                const headerLabel = group.provider
                  ? `${group.provider}${group.account ? ` · ${group.account}` : ''}`
                  : 'Other'

                return (
                  <div key={`${group.provider}:${group.account}`}>
                    {groupIndex > 0 && (
                      <div className="border-t border-theme-border-light my-1" />
                    )}
                    {showHeader && (
                      <div className="px-3 py-1.5 bg-theme-elevated/30">
                        <span className="text-[10px] text-theme-text-tertiary font-medium">
                          {headerLabel}
                        </span>
                      </div>
                    )}
                    {group.items.map((item) => {
                      const itemIndex = itemIndexMap.get(item.context.name) ?? -1
                      return (
                        <button
                          key={item.context.name}
                          data-highlighted={itemIndex === highlightedIndex}
                          onClick={() => handleContextSwitch(item)}
                          onMouseEnter={() => setHighlightedIndex(itemIndex)}
                          disabled={item.context.isCurrent || switchContext.isPending}
                          className={`
                            w-full flex items-center gap-2 px-3 py-2 text-left
                            transition-colors
                            ${item.context.isCurrent
                              ? 'bg-blue-500/10'
                              : itemIndex === highlightedIndex
                                ? 'bg-theme-hover cursor-pointer'
                                : 'hover:bg-theme-hover cursor-pointer'
                            }
                            disabled:opacity-50
                          `}
                        >
                          <div className="shrink-0 w-4 h-4 flex items-center justify-center">
                            {item.context.isCurrent ? (
                              <Check className="w-3.5 h-3.5 text-blue-600 dark:text-blue-400" />
                            ) : (
                              <div className="w-1.5 h-1.5 rounded-full bg-theme-text-tertiary/30" />
                            )}
                          </div>
                          <div className="flex-1 min-w-0">
                            <div className="flex items-center gap-1.5">
                              <span className={`text-sm font-medium truncate ${item.context.isCurrent ? 'text-blue-600 dark:text-blue-400' : 'text-theme-text-primary'}`}>
                                {item.clusterName}
                              </span>
                              {item.region && (
                                <span className="shrink-0 text-[10px] text-theme-text-tertiary bg-theme-elevated px-1 rounded">
                                  {item.region}
                                </span>
                              )}
                              {item.context.isCurrent && (
                                <span className="shrink-0 text-[9px] text-blue-600 dark:text-blue-400">
                                  ●
                                </span>
                              )}
                            </div>
                            {item.provider && (
                              <div className="text-[10px] text-theme-text-tertiary truncate mt-0.5" title={item.raw}>
                                {item.raw}
                              </div>
                            )}
                          </div>
                        </button>
                      )
                    })}
                  </div>
                )
              })
            )}
          </div>

          {/* Footer with count */}
          {contexts.length > 1 && search && flatItems.length > 0 && (
            <div className="px-3 py-1.5 text-[10px] text-theme-text-tertiary border-t border-theme-border bg-theme-base">
              {flatItems.length === contexts.length
                ? `${contexts.length} clusters`
                : `${flatItems.length} of ${contexts.length} clusters`}
            </div>
          )}

          {/* Error message if switch failed */}
          {switchContext.isError && (
            <div className="px-3 py-2 bg-red-500/10 border-t border-red-500/20">
              <span className="text-xs text-red-400">
                {switchContext.error?.message}
              </span>
            </div>
          )}
        </div>
      )}

      {/* Confirmation modal */}
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
                    {sessionCounts.portForwards} port forward{sessionCounts.portForwards !== 1 ? 's' : ''}
                  </li>
                )}
                {sessionCounts.execSessions > 0 && (
                  <li className="flex items-center gap-2">
                    <span className="w-1.5 h-1.5 rounded-full bg-green-400" />
                    {sessionCounts.execSessions} terminal session{sessionCounts.execSessions !== 1 ? 's' : ''}
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

      {/* Error dialog when context switch fails */}
      {switchError && (
        <div className="fixed inset-0 z-[100] flex items-center justify-center bg-black/50">
          <div className="bg-theme-surface border border-theme-border rounded-lg shadow-xl max-w-md mx-4 overflow-hidden">
            <div className="px-4 py-3 border-b border-red-500/30 bg-red-500/10 flex items-center gap-2">
              <XCircle className="w-5 h-5 text-red-400" />
              <span className="font-medium text-theme-text-primary">Connection Failed</span>
            </div>
            <div className="px-4 py-4">
              <p className="text-sm text-theme-text-secondary mb-3">
                Failed to switch to cluster <span className="font-medium text-theme-text-primary">{switchError.clusterName}</span>
              </p>
              <div className="bg-theme-base rounded-md p-3 mb-4">
                <p className="text-xs text-red-400 font-mono break-all">
                  {switchError.message}
                </p>
              </div>
              <p className="text-xs text-theme-text-tertiary">
                The cluster may be unreachable, or your credentials may have expired.
                Try running <code className="bg-theme-elevated px-1 py-0.5 rounded text-theme-text-secondary">kubectl get nodes</code> to verify connectivity.
              </p>
            </div>
            <div className="px-4 py-3 border-t border-theme-border flex justify-end">
              <button
                onClick={() => setSwitchError(null)}
                className="px-4 py-1.5 text-sm rounded-md bg-theme-elevated hover:bg-theme-hover text-theme-text-primary transition-colors"
              >
                OK
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
