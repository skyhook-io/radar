import { useMemo, forwardRef } from 'react'
import {
  ClusterSwitcher,
  type ClusterSwitcherItem,
} from '@skyhook-io/k8s-ui'
import { useContexts, useClusterInfo, useCapabilities } from '../api/client'
import type { ContextInfo } from '../types'
import { useContextSwitchFlow } from './useContextSwitchFlow'
import { parseContextForSwitcher, visibleContextQualifier, type ParsedContextName } from '../utils/context-name'

interface ContextSwitcherProps {
  className?: string
  variant?: 'chip' | 'segment'
  label?: string
  triggerName?: string
}

export interface ContextSwitcherHandle {
  open: () => void
}

interface ParsedContext extends ParsedContextName {
  context: ContextInfo
  nameQualifier?: string
}

export const ContextSwitcher = forwardRef<ContextSwitcherHandle, ContextSwitcherProps>(({ className = '', variant, label, triggerName }, ref) => {
  const { data: contexts, isLoading: contextsLoading } = useContexts()
  const { data: clusterInfo } = useClusterInfo()
  const { data: capabilities } = useCapabilities()
  const switchFlow = useContextSwitchFlow()

  // Parse contexts and decide whether to render group headers (multi-account only).
  // hasMultipleSources gates the kubeconfig-source chip — only useful when 2+
  // distinct kubeconfig files are in play. Single-source setups (the common
  // case) skip the chip entirely so the dropdown stays clean.
  const { parsedById, hasMultipleAccounts, hasMultipleSources } = useMemo(() => {
    if (!contexts) return {
      parsedById: new Map<string, ParsedContext>(),
      hasMultipleAccounts: false,
      hasMultipleSources: false,
    }
    const parsed: ParsedContext[] = contexts.map(ctx => ({
      context: ctx,
      ...parseContextForSwitcher(ctx),
    }))
    const accounts = new Set(parsed.map(p => `${p.provider}:${p.account}`))
    const sources = new Set(contexts.map(c => c.source).filter(Boolean))
    const byId = new Map<string, ParsedContext>()
    for (const p of parsed) byId.set(p.context.name, p)
    return {
      parsedById: byId,
      hasMultipleAccounts: accounts.size > 1,
      hasMultipleSources: sources.size > 1,
    }
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
      return {
        id: p.context.name,
        name: p.raw,
        nameQualifier: visibleContextQualifier(p.nameQualifier, p.context.source, hasMultipleSources),
        secondary: p.provider ? p.raw : undefined,
        badge: p.region || undefined,
        sourceLabel: hasMultipleSources ? p.context.source : undefined,
        group: { key: groupKey, label: groupLabel },
      }
    })
  }, [parsedById, hasMultipleAccounts, hasMultipleSources])

  const handleSelect = (item: ClusterSwitcherItem) => {
    const parsed = parsedById.get(item.id)
    if (!parsed) return
    switchFlow.requestSwitch(parsed.context)
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

  const currentCtx = contexts?.find(c => c.isCurrent)
  const currentId = currentCtx?.name
  // Keep the trigger name parseable and render any collision qualifier as a
  // separate suffix so cloud-provider metadata remains intact.
  // Fall back to clusterInfo.context for the very-early window before
  // /api/contexts has resolved.
  const currentParsed = currentId ? parsedById.get(currentId) : undefined
  const currentRaw = triggerName || currentParsed?.raw || clusterInfo?.context || currentCtx?.name || 'Unknown'
  const currentNameQualifier = triggerName
    ? undefined
    : visibleContextQualifier(currentParsed?.nameQualifier, currentCtx?.source, hasMultipleSources)
  const currentSourceLabel = triggerName ? undefined : hasMultipleSources ? currentCtx?.source || undefined : undefined

  return (
    <>
      <ClusterSwitcher
        ref={ref}
        className={className}
        variant={variant}
        label={label}
        currentId={currentId}
        currentName={currentRaw}
        currentNameQualifier={currentNameQualifier}
        currentSourceLabel={currentSourceLabel}
        items={items}
        onSelect={handleSelect}
        loading={switchFlow.isPending}
        disabled={contextsLoading || !capabilities || capabilities.configManagement === 'operator'}
        searchable={items.length > 1}
        showGroupHeaders={hasMultipleAccounts}
        errorSlot={
          switchFlow.error ? (
            <span className="text-xs text-red-400">{switchFlow.error.message}</span>
          ) : undefined
        }
      />

      {switchFlow.confirmDialog}
    </>
  )
})
