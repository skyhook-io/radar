import { useState, useMemo, useEffect, useRef, forwardRef } from 'react'
import {
  Search,
  ChevronDown,
  ChevronRight,
  Eye,
  EyeOff,
  Pin,
  Shield,
  X,
} from 'lucide-react'
import { clsx } from 'clsx'
import type { APIResource } from '../../types'
import { categorizeResources, CORE_RESOURCES } from '../../utils/api-resources'
import { getResourceIcon } from '../../utils/resource-icons'
import { Tooltip } from '../ui/Tooltip'

// Selected resource type info (need both name for API and kind for display)
export interface SelectedKindInfo {
  name: string      // Plural name for API calls (e.g., 'pods')
  kind: string      // Kind for display (e.g., 'Pod')
  group: string     // API group for disambiguation (e.g., '', 'metrics.k8s.io')
}

// Pinned item shape
export interface PinnedItem {
  name: string
  kind: string
  group: string
}

export interface ResourcesSidebarProps {
  selectedKind: SelectedKindInfo | null
  onSelectedKindChange: (kind: SelectedKindInfo) => void
  onKindChange?: () => void
  apiResources?: APIResource[]
  resourceCounts?: Record<string, number>
  resourceForbidden?: string[]
  pinned?: PinnedItem[]
  togglePin?: (item: PinnedItem) => void
  isPinned?: (kind: string, group?: string) => boolean
  className?: string
  /** When provided, kind clicks navigate via this callback instead of only updating state */
  onNavigate?: (path: string) => void
  /** Base path for generating navigation URLs (e.g., '/org/clusters/id/k8s-resources') */
  basePath?: string
}

// Persisted across remounts so collapsed categories survive tab switches
let persistedExpandedCategories: Set<string> | null = null

// Core kinds that are always shown even with 0 instances
// These are the most commonly used Kubernetes resources (using Kind names, not plural names)
const ALWAYS_SHOWN_KINDS = new Set([
  'Pod',
  'Deployment',
  'DaemonSet',
  'StatefulSet',
  'ReplicaSet',
  'Service',
  'Ingress',
  'ConfigMap',
  'Secret',
  'Job',
  'CronJob',
  'HorizontalPodAutoscaler',
  'PersistentVolumeClaim',
  'Node',
  'Namespace',
  'ServiceAccount',
  'NetworkPolicy',
  'Event',
])

// Fallback resource types when API resources aren't loaded yet
const CORE_RESOURCE_TYPES = [
  { kind: 'pods', label: 'Pods' },
  { kind: 'deployments', label: 'Deployments' },
  { kind: 'daemonsets', label: 'DaemonSets' },
  { kind: 'statefulsets', label: 'StatefulSets' },
  { kind: 'replicasets', label: 'ReplicaSets' },
  { kind: 'services', label: 'Services' },
  { kind: 'ingresses', label: 'Ingresses' },
  { kind: 'configmaps', label: 'ConfigMaps' },
  { kind: 'secrets', label: 'Secrets' },
  { kind: 'jobs', label: 'Jobs' },
  { kind: 'cronjobs', label: 'CronJobs' },
  { kind: 'hpas', label: 'HPAs' },
] as const

// Resource type button in sidebar
interface ResourceTypeButtonProps {
  resource: APIResource
  count: number
  isSelected: boolean
  isForbidden?: boolean
  isPinned?: boolean
  onTogglePin?: () => void
  onClick: () => void
}

const ResourceTypeButton = forwardRef<HTMLButtonElement, ResourceTypeButtonProps>(
  function ResourceTypeButton({ resource, count, isSelected, isForbidden: forbidden, isPinned, onTogglePin, onClick }, ref) {
    const Icon = getResourceIcon(resource.kind)
    return (
      <button
        ref={ref}
        onClick={onClick}
        className={clsx(
          'w-full flex items-center gap-2 px-2 xl:px-3 py-1.5 rounded-lg text-sm transition-colors group/kind min-w-0',
          isSelected
            ? 'bg-blue-500/20 text-blue-700 dark:text-blue-300'
            : forbidden
              ? 'text-theme-text-disabled hover:bg-theme-elevated hover:text-theme-text-secondary'
              : 'text-theme-text-secondary hover:bg-theme-elevated hover:text-theme-text-primary'
        )}
      >
        <Icon className="w-4 h-4 shrink-0" />
        <Tooltip content={forbidden ? `${resource.kind} (no access)` : resource.kind} position="right" wrapperClassName="min-w-0 flex-1 overflow-hidden">
          <span className="text-left truncate block">
            {resource.kind}
          </span>
        </Tooltip>
        <div className="ml-auto flex items-center gap-1 shrink-0">
          {onTogglePin && (
            <span
              role="button"
              onClick={(e) => {
                e.stopPropagation()
                onTogglePin()
              }}
              className={clsx(
                'p-0.5 rounded transition-all hover:bg-theme-hover',
                isPinned
                  ? 'text-theme-text-secondary'
                  : 'opacity-0 group-hover/kind:opacity-100 text-theme-text-disabled'
              )}
              title={isPinned ? 'Unpin from favorites' : 'Pin to favorites'}
            >
              <Pin className={clsx('w-3 h-3', isPinned && 'fill-current')} />
            </span>
          )}
          {forbidden ? (
            <Tooltip content="Insufficient permissions" position="left">
              <Shield className="w-3.5 h-3.5 text-amber-400/60" />
            </Tooltip>
          ) : (
            <span className={clsx(
              'text-xs py-0.5 rounded text-center',
              isSelected ? 'bg-blue-500/30 text-blue-700 dark:text-blue-300' : 'bg-theme-elevated',
              count < 1000 ? 'w-8' : 'w-9'
            )}>
              {count}
            </span>
          )}
        </div>
      </button>
    )
  }
)

export function ResourcesSidebar({
  selectedKind,
  onSelectedKindChange,
  onKindChange,
  apiResources,
  resourceCounts,
  resourceForbidden,
  pinned = [],
  togglePin = () => {},
  isPinned = () => false,
  className,
  onNavigate,
  basePath,
}: ResourcesSidebarProps) {
  // Wraps kind selection to also navigate when basePath/onNavigate are provided
  const selectKind = (kind: SelectedKindInfo) => {
    onSelectedKindChange(kind)
    onKindChange?.()
    if (onNavigate && basePath) {
      const path = `${basePath}/${kind.name}${kind.group ? `?apiGroup=${kind.group}` : ''}`
      onNavigate(path)
    }
  }

  // --- Sidebar-local state ---
  const [kindFilter, setKindFilter] = useState('')
  const [expandedCategories, setExpandedCategories] = useState<Set<string>>(
    () => persistedExpandedCategories ?? new Set(['Workloads', 'Networking', 'Configuration'])
  )
  useEffect(() => { persistedExpandedCategories = expandedCategories }, [expandedCategories])
  const [showEmptyKinds, setShowEmptyKinds] = useState(false)
  const [favoritesExpanded, setFavoritesExpanded] = useState(() => pinned.length > 0)

  // Ref to selected sidebar item for scrolling into view on deeplink
  const selectedSidebarRef = useRef<HTMLButtonElement>(null)

  // Effective selected kind — fall back to a safe default
  const effectiveSelectedKind = selectedKind ?? { name: 'pods', kind: 'Pod', group: '' }

  // Categorize resources for sidebar
  const categories = useMemo(() => {
    if (!apiResources) return null
    return categorizeResources(apiResources)
  }, [apiResources])

  // Auto-expand the sidebar category containing the selected kind
  const lastAutoExpandedKind = useRef<string | null>(null)
  useEffect(() => {
    if (!categories) return
    const kindKey = `${effectiveSelectedKind.group}/${effectiveSelectedKind.kind}`
    if (lastAutoExpandedKind.current === kindKey) return
    lastAutoExpandedKind.current = kindKey
    for (const cat of categories) {
      const match = cat.resources.some(r => r.kind === effectiveSelectedKind.kind || r.name === effectiveSelectedKind.name)
      if (match && !expandedCategories.has(cat.name)) {
        setExpandedCategories(prev => new Set([...prev, cat.name]))
        break
      }
    }
  }, [categories, effectiveSelectedKind.kind, effectiveSelectedKind.name]) // eslint-disable-line react-hooks/exhaustive-deps

  // Derive counts from resourceCounts prop
  const resourcesToCount = useMemo(() => {
    if (categories) {
      return categories.flatMap(c => c.resources).map(r => ({
        kind: r.kind,
        name: r.name,
        group: r.group,
      }))
    }
    return CORE_RESOURCES.map(r => ({
      kind: r.kind,
      name: r.name,
      group: r.group,
    }))
  }, [categories])

  const counts = useMemo(() => {
    if (!resourceCounts) return {} as Record<string, number>
    const results: Record<string, number> = {}
    for (const resource of resourcesToCount) {
      const key = resource.group ? `${resource.group}/${resource.kind}` : resource.kind
      results[key] = resourceCounts[key] ?? 0
    }
    return results
  }, [resourcesToCount, resourceCounts])

  // Track which resource kinds returned 403 Forbidden
  const forbiddenKinds = useMemo(() => {
    return new Set(resourceForbidden ?? [])
  }, [resourceForbidden])

  // Calculate category totals, filter empty kinds/groups, and sort (empty categories at bottom)
  const { sortedCategories, hiddenKindsCount, hiddenGroupsCount } = useMemo(() => {
    if (!categories) return { sortedCategories: null, hiddenKindsCount: 0, hiddenGroupsCount: 0 }

    let totalHiddenKinds = 0
    let totalHiddenGroups = 0

    const withTotals = categories.map(category => {
      const total = category.resources.reduce(
        (sum, resource) => sum + (counts?.[(resource.group ? `${resource.group}/${resource.kind}` : resource.kind)] ?? 0),
        0
      )

      // Filter resources: show if has instances, is core kind, or showEmptyKinds is true
      const visibleResources = category.resources.filter(resource => {
        const count = counts?.[(resource.group ? `${resource.group}/${resource.kind}` : resource.kind)] ?? 0
        const isCore = ALWAYS_SHOWN_KINDS.has(resource.kind)
        const shouldShow = count > 0 || isCore || showEmptyKinds
        if (!shouldShow) totalHiddenKinds++
        return shouldShow
      })

      return { ...category, total, visibleResources }
    })

    // Sort: categories with resources first, empty ones at bottom
    const sorted = withTotals.sort((a, b) => {
      if (a.total === 0 && b.total > 0) return 1
      if (a.total > 0 && b.total === 0) return -1
      return 0
    })

    // Filter out empty groups unless they have visible resources (core kinds) or showEmptyKinds is true
    const visibleCategories = sorted.filter(category => {
      // Show if: has resources with instances, OR has visible resources (core kinds), OR showEmptyKinds
      const shouldShow = category.total > 0 || category.visibleResources.length > 0 || showEmptyKinds
      if (!shouldShow) totalHiddenGroups++
      return shouldShow
    })

    return { sortedCategories: visibleCategories, hiddenKindsCount: totalHiddenKinds, hiddenGroupsCount: totalHiddenGroups }
  }, [categories, counts, showEmptyKinds])

  // Filter sidebar categories/kinds by the kind search term
  const filteredCategories = useMemo(() => {
    if (!sortedCategories || !kindFilter.trim()) return sortedCategories
    const term = kindFilter.toLowerCase()
    return sortedCategories
      .map(category => {
        const categoryMatches = category.name.toLowerCase().includes(term)
        // If the group name matches, show all its resources
        if (categoryMatches) return category
        const matchingResources = category.visibleResources.filter((resource: any) =>
          resource.kind.toLowerCase().includes(term) ||
          resource.name.toLowerCase().includes(term)
        )
        if (matchingResources.length === 0) return null
        return {
          ...category,
          visibleResources: matchingResources,
        }
      })
      .filter(Boolean) as typeof sortedCategories
  }, [sortedCategories, kindFilter])

  // Auto-expand all categories when filtering
  const isKindFiltering = kindFilter.trim().length > 0
  const effectiveExpandedCategories = useMemo(() => {
    if (!isKindFiltering || !filteredCategories) return expandedCategories
    return new Set(filteredCategories.map(c => c.name))
  }, [isKindFiltering, filteredCategories, expandedCategories])

  const toggleCategory = (categoryName: string) => {
    setExpandedCategories(prev => {
      const next = new Set(prev)
      if (next.has(categoryName)) {
        next.delete(categoryName)
      } else {
        next.add(categoryName)
      }
      return next
    })
  }

  // Scroll sidebar to show selected kind on mount (deep linking) and on kind changes (keyboard nav)
  const lastScrolledKind = useRef<string | null>(null)
  const isInitialScroll = useRef(true)
  useEffect(() => {
    const kindKey = `${effectiveSelectedKind.group}/${effectiveSelectedKind.name}`
    if (lastScrolledKind.current === kindKey) return
    lastScrolledKind.current = kindKey

    const instant = isInitialScroll.current
    isInitialScroll.current = false

    requestAnimationFrame(() => {
      if (selectedSidebarRef.current) {
        selectedSidebarRef.current.scrollIntoView({
          behavior: instant ? 'instant' : 'smooth',
          block: 'center',
        })
      }
    })
  }, [effectiveSelectedKind.name, effectiveSelectedKind.group])

  return (
    <div className={clsx('w-56 2xl:w-72 bg-theme-surface border-r border-theme-border overflow-y-auto overflow-x-hidden shrink-0', className)}>
      <div className="px-2 py-2 border-b border-theme-border">
        <div className="relative">
          <Search className="absolute left-2 top-1/2 -translate-y-1/2 w-3.5 h-3.5 text-theme-text-tertiary" />
          <input
            type="text"
            placeholder="Filter resources..."
            value={kindFilter}
            onChange={(e) => setKindFilter(e.target.value)}
            onKeyDown={(e) => { if (e.key === 'Escape') { setKindFilter(''); (e.target as HTMLInputElement).blur() } }}
            className="w-full pl-7 pr-7 py-2 bg-theme-elevated border border-theme-border-light rounded-lg text-sm text-theme-text-primary placeholder-theme-text-disabled focus:outline-none focus:ring-2 focus:ring-blue-500"
          />
          {kindFilter && (
            <button
              onClick={() => setKindFilter('')}
              className="absolute right-1.5 top-1/2 -translate-y-1/2 p-0.5 rounded hover:bg-theme-surface text-theme-text-tertiary hover:text-theme-text-secondary"
            >
              <X className="w-3 h-3" />
            </button>
          )}
        </div>
      </div>
      <nav className="p-2">
        {/* Favorites (pinned kinds) section — always visible */}
        <div className="mb-2">
          <button
            onClick={() => setFavoritesExpanded((v) => !v)}
            className="w-full flex items-center gap-2 px-2 py-1.5 text-xs font-medium text-theme-text-tertiary hover:text-theme-text-secondary uppercase tracking-wide"
          >
            {favoritesExpanded ? (
              <ChevronDown className="w-3 h-3" />
            ) : (
              <ChevronRight className="w-3 h-3" />
            )}
            <span className="flex-1 text-left">Favorites</span>
            {!favoritesExpanded && pinned.length > 0 && (
              <span className={clsx('text-xs py-0.5 rounded bg-theme-elevated text-theme-text-secondary font-normal normal-case text-center', pinned.length < 1000 ? 'w-8' : 'w-9')}>
                {pinned.length}
              </span>
            )}
          </button>
          {favoritesExpanded && (
            <div className="space-y-0.5">
              {pinned.length === 0 ? (
                <div className="px-3 py-2 text-xs text-theme-text-disabled">
                  No pinned resources. Click <Pin className="w-3 h-3 inline" /> on any resource type to pin it here.
                </div>
              ) : (
                pinned.map((p) => {
                  const isResourceSelected =
                    (effectiveSelectedKind.name === p.name && effectiveSelectedKind.group === p.group) ||
                    (effectiveSelectedKind.kind.toLowerCase() === p.kind.toLowerCase() && effectiveSelectedKind.group === p.group)
                  return (
                    <ResourceTypeButton
                      key={`${p.name}-${p.group}`}
                      ref={isResourceSelected ? selectedSidebarRef : null}
                      resource={{ name: p.name, kind: p.kind, group: p.group, version: '', namespaced: true, isCrd: false, verbs: [] }}
                      count={counts?.[(p.group ? `${p.group}/${p.kind}` : p.kind)] ?? 0}
                      isSelected={isResourceSelected}
                      isForbidden={forbiddenKinds.has(p.group ? `${p.group}/${p.kind}` : p.kind)}
                      isPinned={true}
                      onTogglePin={() => togglePin(p)}
                      onClick={() => selectKind({ name: p.name, kind: p.kind, group: p.group })}
                    />
                  )
                })
              )}
            </div>
          )}
        </div>
        {filteredCategories ? (
          // Dynamic categories from API
          filteredCategories.map((category) => {
            const isExpanded = effectiveExpandedCategories.has(category.name)
            return (
              <div key={category.name} className="mb-2">
                <button
                  onClick={() => toggleCategory(category.name)}
                  className="w-full flex items-center gap-2 px-2 py-1.5 text-xs font-semibold text-theme-text-tertiary hover:text-theme-text-secondary uppercase tracking-wide"
                >
                  {isExpanded ? (
                    <ChevronDown className="w-3 h-3" />
                  ) : (
                    <ChevronRight className="w-3 h-3" />
                  )}
                  <span className="flex-1 text-left">{category.name}</span>
                  {!isExpanded && (
                    <span className={clsx('text-xs py-0.5 rounded bg-theme-elevated text-theme-text-secondary font-normal normal-case text-center', category.total < 1000 ? 'w-8' : 'w-9')}>
                      {category.total}
                    </span>
                  )}
                </button>
                {isExpanded && (
                  <div className="space-y-0.5">
                    {category.visibleResources.map((resource) => {
                      const resourceIsPinned = isPinned(resource.name, resource.group)
                      const isResourceSelected =
                        (effectiveSelectedKind.name === resource.name && effectiveSelectedKind.group === resource.group) ||
                        (effectiveSelectedKind.kind.toLowerCase() === resource.kind.toLowerCase() && effectiveSelectedKind.group === resource.group)
                      // If the resource is pinned, let the Favorites section own the highlight
                      const showSelected = isResourceSelected && !resourceIsPinned
                      return (
                      <ResourceTypeButton
                        key={resource.name}
                        ref={isResourceSelected ? selectedSidebarRef : null}
                        resource={resource}
                        count={counts?.[(resource.group ? `${resource.group}/${resource.kind}` : resource.kind)] ?? 0}
                        isSelected={showSelected}
                        isForbidden={forbiddenKinds.has(resource.group ? `${resource.group}/${resource.kind}` : resource.kind)}
                        isPinned={resourceIsPinned}
                        onTogglePin={() => togglePin({ name: resource.name, kind: resource.kind, group: resource.group })}
                        onClick={() => selectKind({ name: resource.name, kind: resource.kind, group: resource.group })}
                      />
                      )
                    })}
                  </div>
                )}
              </div>
            )
          })
        ) : (
          // Fallback to core resources while loading
          CORE_RESOURCE_TYPES.map((type) => {
            // Fallback: type.label is display name like 'Pods', counts are keyed by Kind like 'Pod'
            // Remove trailing 's' for singular kind lookup (hacky but works for fallback)
            const kindKey = type.label.endsWith('s') && !type.label.endsWith('ss')
              ? type.label.slice(0, -1)
              : type.label
            const Icon = getResourceIcon(kindKey)
            const count = counts?.[kindKey] ?? 0
            const isSelected = effectiveSelectedKind.name === type.kind && !effectiveSelectedKind.group
            return (
              <button
                key={type.kind}
                onClick={() => {
                  selectKind({ name: type.kind, kind: type.label, group: '' })
                }}
                className={clsx(
                  'w-full flex items-center gap-3 px-3 py-2 rounded-lg text-sm transition-colors',
                  isSelected
                    ? 'bg-blue-500/20 text-blue-700 dark:text-blue-300'
                    : 'text-theme-text-secondary hover:bg-theme-elevated hover:text-theme-text-primary'
                )}
              >
                <Icon className="w-4 h-4 shrink-0" />
                <span className="flex-1 text-left">{type.label}</span>
                <span className={clsx(
                  'text-xs px-2 py-0.5 rounded',
                  isSelected ? 'bg-blue-500/30 text-blue-700 dark:text-blue-300' : 'bg-theme-elevated'
                )}>
                  {count}
                </span>
              </button>
            )
          })
        )}

        {/* Toggle for showing/hiding empty kinds and groups */}
        {hiddenKindsCount > 0 || hiddenGroupsCount > 0 || showEmptyKinds ? (
          <button
            onClick={() => setShowEmptyKinds(!showEmptyKinds)}
            className="w-full flex items-center gap-2 px-3 py-2 mt-2 text-xs text-theme-text-tertiary hover:text-theme-text-secondary border-t border-theme-border"
          >
            {showEmptyKinds ? (
              <>
                <EyeOff className="w-3.5 h-3.5" />
                <span>Hide empty</span>
              </>
            ) : (
              <>
                <Eye className="w-3.5 h-3.5" />
                <span>
                  Show {hiddenKindsCount + hiddenGroupsCount} empty
                  {hiddenGroupsCount > 0 && ` (${hiddenGroupsCount} groups)`}
                </span>
              </>
            )}
          </button>
        ) : null}
      </nav>
    </div>
  )
}
