import { useState, useMemo, useRef, useEffect } from 'react'
import { ShieldAlert, AlertTriangle, ChevronRight, CheckCircle2, Search, ExternalLink, MoreHorizontal, EyeOff, Layers } from 'lucide-react'
import { clsx } from 'clsx'
import type { AuditFinding } from './AuditAlerts'
import { SEVERITY_TEXT, BP_CATEGORY_BADGE, DEFAULT_BADGE_COLOR } from '../../utils/badge-colors'
import { EmptyState } from '../ui/EmptyState'
import { FilterPill } from '../ui/FilterPill'
import { pluralize } from '../../utils/pluralize'

const CATEGORIES = ['Security', 'Reliability', 'Efficiency'] as const
const SEVERITIES = ['danger', 'warning'] as const

export interface ResourceGroup {
  kind: string
  namespace: string
  name: string
  warning: number
  danger: number
  findings: AuditFinding[]
}

export interface CheckMeta {
  id: string
  title: string
  description: string
  remediation: string
  frameworks?: string[]
}

export interface AuditFindingsTableProps {
  groups?: ResourceGroup[]
  findings?: AuditFinding[]
  checks?: Record<string, CheckMeta>
  onResourceClick?: (kind: string, namespace: string, name: string) => void
  onHideCheck?: (checkID: string, title: string) => void
  onHideCategory?: (category: string) => void
  onHideNamespace?: (namespace: string) => void
  /** When true, render a Cluster column on flat findings rows. Set this
   *  when findings come from multiple clusters (cross-cluster aggregation).
   *  Each finding's clusterId/clusterName fields populate the column.
   *  (Currently flat-rows only; grouped views don't surface a Cluster
   *  column today.) */
  multiCluster?: boolean
  /** Click-through for cluster-name links in the multi-cluster view.
   *  Defaults to no-op; multi-cluster hosts pass a navigator that opens
   *  the cluster's per-cluster audit page. */
  onClusterClick?: (clusterId: string) => void
}

export function AuditFindingsTable({ groups, findings, checks, onResourceClick, onHideCheck, onHideCategory, onHideNamespace, multiCluster, onClusterClick }: AuditFindingsTableProps) {
  const [categoryFilter, setCategoryFilter] = useState<string | null>(null)
  const [severityFilter, setSeverityFilter] = useState<string | null>(null)
  const [frameworkFilter, setFrameworkFilter] = useState<string | null>(null)
  const [searchTerm, setSearchTerm] = useState('')
  const [expanded, setExpanded] = useState<Set<string>>(new Set())
  const [expandedNS, setExpandedNS] = useState<Set<string>>(new Set())
  const [groupByNS, setGroupByNS] = useState(false)
  const searchInputRef = useRef<HTMLInputElement>(null)

  // "/" keyboard shortcut to focus search
  useEffect(() => {
    const handler = (e: KeyboardEvent) => {
      if (e.key === '/' && !e.ctrlKey && !e.metaKey && document.activeElement?.tagName !== 'INPUT') {
        e.preventDefault()
        searchInputRef.current?.focus()
      }
    }
    document.addEventListener('keydown', handler)
    return () => document.removeEventListener('keydown', handler)
  }, [])

  // Compute totals from whichever data source we have
  const allFindings = useMemo(() => {
    if (groups) return groups.flatMap(g => g.findings)
    return findings ?? []
  }, [groups, findings])

  const totalDangerCount = allFindings.filter(f => f.severity === 'danger').length
  const totalWarningCount = allFindings.filter(f => f.severity === 'warning').length

  // Derive available frameworks from checks metadata
  const frameworks = useMemo(() => {
    if (!checks) return []
    const set = new Set<string>()
    Object.values(checks).forEach(c => c.frameworks?.forEach(f => set.add(f)))
    return Array.from(set).sort()
  }, [checks])

  const searchLower = searchTerm.toLowerCase()

  // Match a finding against category/severity/framework filters
  const matchesFinding = (f: AuditFinding) => {
    if (categoryFilter && f.category !== categoryFilter) return false
    if (severityFilter && f.severity !== severityFilter) return false
    if (frameworkFilter && checks) {
      const meta = checks[f.checkID]
      if (!meta?.frameworks?.includes(frameworkFilter)) return false
    }
    return true
  }

  // Match a resource group against search term
  const matchesSearch = (g: ResourceGroup) => {
    if (!searchLower) return true
    if (g.name.toLowerCase().includes(searchLower)) return true
    if (g.namespace.toLowerCase().includes(searchLower)) return true
    if (g.kind.toLowerCase().includes(searchLower)) return true
    return g.findings.some(f => f.message.toLowerCase().includes(searchLower))
  }

  // Filter groups: a group is visible if it matches search AND has findings matching filters
  const filteredGroups = useMemo(() => {
    if (!groups) return undefined
    return groups
      .filter(g => matchesSearch(g))
      .map(g => {
        const filtered = g.findings.filter(matchesFinding)
        if (filtered.length === 0) return null
        return { ...g, findings: filtered, danger: filtered.filter(f => f.severity === 'danger').length, warning: filtered.filter(f => f.severity === 'warning').length }
      })
      .filter((g): g is ResourceGroup => g !== null)
  }, [groups, categoryFilter, severityFilter, frameworkFilter, searchLower]) // eslint-disable-line react-hooks/exhaustive-deps

  // Filter flat findings (fallback mode)
  const filteredFindings = useMemo(() => {
    if (groups) return undefined
    return (findings ?? []).filter(f => {
      if (!matchesFinding(f)) return false
      if (searchLower && !f.message.toLowerCase().includes(searchLower) && !f.name.toLowerCase().includes(searchLower)) return false
      return true
    })
  }, [groups, findings, categoryFilter, severityFilter, frameworkFilter, checks, searchLower]) // eslint-disable-line react-hooks/exhaustive-deps

  const toggle = (key: string) => {
    setExpanded(prev => {
      const next = new Set(prev)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }

  // Compute counts from filtered results (so summary reflects active filters)
  const hasActiveFilters = !!(categoryFilter || severityFilter || frameworkFilter || searchTerm)
  const filteredAllFindings = filteredGroups
    ? filteredGroups.flatMap(g => g.findings)
    : filteredFindings ?? []
  const dangerCount = hasActiveFilters ? filteredAllFindings.filter(f => f.severity === 'danger').length : totalDangerCount
  const warningCount = hasActiveFilters ? filteredAllFindings.filter(f => f.severity === 'warning').length : totalWarningCount

  // Group resources by namespace when enabled
  const namespacedGroups = useMemo(() => {
    if (!groupByNS || !filteredGroups) return undefined
    const nsMap = new Map<string, ResourceGroup[]>()
    for (const g of filteredGroups) {
      const ns = g.namespace || '(cluster-scoped)'
      const list = nsMap.get(ns) || []
      list.push(g)
      nsMap.set(ns, list)
    }
    // Sort namespaces: most severe first
    return Array.from(nsMap.entries()).sort((a, b) => {
      const aDanger = a[1].reduce((n, g) => n + g.danger, 0)
      const bDanger = b[1].reduce((n, g) => n + g.danger, 0)
      if (aDanger !== bDanger) return bDanger - aDanger
      return a[0].localeCompare(b[0])
    })
  }, [groupByNS, filteredGroups])

  const toggleNS = (ns: string) => {
    const isOpening = !expandedNS.has(ns)
    setExpandedNS(prev => {
      const next = new Set(prev)
      if (next.has(ns)) next.delete(ns)
      else next.add(ns)
      return next
    })
    // When opening a namespace, auto-expand all its resource groups
    if (isOpening && namespacedGroups) {
      const nsEntry = namespacedGroups.find(([n]) => n === ns)
      if (nsEntry) {
        setExpanded(prev => {
          const next = new Set(prev)
          for (const g of nsEntry[1]) {
            next.add(`${g.kind}/${g.namespace}/${g.name}`)
          }
          return next
        })
      }
    }
  }

  // Auto-enable grouping for large result sets; auto-expand all in flat view
  const resourceCount = filteredGroups?.length ?? 0
  useEffect(() => {
    if (resourceCount > 20) {
      setGroupByNS(true)
    } else if (filteredGroups) {
      // Small result set — start with all resource groups expanded
      setExpanded(new Set(filteredGroups.map(g => `${g.kind}/${g.namespace}/${g.name}`)))
    }
  }, []) // eslint-disable-line react-hooks/exhaustive-deps

  const isEmpty = filteredGroups ? filteredGroups.length === 0 : (filteredFindings?.length ?? 0) === 0
  const totalEmpty = allFindings.length === 0

  return (
    <div className="flex flex-col gap-4">
      {/* Toolbar */}
      <div className="flex flex-col gap-2 px-4 py-3 border-b border-theme-border bg-theme-base rounded-xl shrink-0">
        {/* Row 1: Counts + Search + View toggle */}
        <div className="flex items-center gap-4">
          <SummaryBadge label="Critical" count={dangerCount} color={SEVERITY_TEXT.error} />
          <SummaryBadge label="Warning" count={warningCount} color={SEVERITY_TEXT.warning} />

          <div className="relative">
            <Search className="absolute left-3 top-1/2 -translate-y-1/2 w-4 h-4 text-theme-text-tertiary" />
            <input
              ref={searchInputRef}
              type="text"
              placeholder="Search... (press /)"
              value={searchTerm}
              onChange={(e) => setSearchTerm(e.target.value)}
              className="w-56 pl-10 pr-4 py-1.5 bg-theme-elevated border border-theme-border-light rounded-lg text-sm text-theme-text-primary placeholder-theme-text-disabled focus:outline-none focus:ring-2 focus:ring-skyhook-500"
            />
          </div>

          <div className="flex-1" />

          {groups && (
            <div className="flex items-center gap-1.5">
              <button
                onClick={() => setGroupByNS(!groupByNS)}
                className={clsx(
                  'flex items-center gap-1 px-2.5 py-1 text-xs rounded-md border transition-colors',
                  groupByNS ? 'bg-theme-text-primary/10 text-theme-text-primary font-medium border-theme-border' : 'text-theme-text-tertiary hover:text-theme-text-secondary border-transparent'
                )}
              >
                <Layers className="w-3.5 h-3.5" />
                Group by namespace
              </button>
            </div>
          )}
        </div>

        {/* Row 2: Filter chips — three groups separated by dividers (All | Categories | Severities | Frameworks). */}
        <div className="flex flex-wrap items-center gap-1.5">
          <FilterPill label="All" active={!categoryFilter && !severityFilter && !frameworkFilter} onClick={() => { setCategoryFilter(null); setSeverityFilter(null); setFrameworkFilter(null) }} />
          <span className="w-px h-5 bg-theme-border mx-2" />
          {CATEGORIES.map(cat => (
            <FilterPill key={cat} label={cat} active={categoryFilter === cat} onClick={() => setCategoryFilter(categoryFilter === cat ? null : cat)} />
          ))}
          <span className="w-px h-5 bg-theme-border mx-2" />
          {SEVERITIES.map(sev => (
            <FilterPill
              key={sev}
              label={sev === 'danger' ? 'Critical' : 'Warning'}
              active={severityFilter === sev}
              tone={sev === 'danger' ? 'danger' : 'warn'}
              onClick={() => setSeverityFilter(severityFilter === sev ? null : sev)}
            />
          ))}
          {frameworks.length > 0 && (
            <>
              <span className="w-px h-5 bg-theme-border mx-2" />
              {frameworks.map(fw => (
                <FilterPill key={fw} label={fw} active={frameworkFilter === fw} onClick={() => setFrameworkFilter(frameworkFilter === fw ? null : fw)} />
              ))}
            </>
          )}
        </div>
      </div>

      {/* Content */}
      {isEmpty ? (
        totalEmpty ? (
          // Healthy state — communicates "you're winning", not "no data".
          // Replaces a grey one-liner that conflated both meanings.
          <EmptyState
            tone="healthy"
            variant="card"
            icon={CheckCircle2}
            headline="All checks passing"
            body="No issues found across these resources."
          />
        ) : (
          <EmptyState
            tone="filtered"
            variant="card"
            headline="No findings match the current filters"
            body="Try clearing one or more filters to see more results."
            action={
              <button
                type="button"
                onClick={() => { setCategoryFilter(null); setSeverityFilter(null); setFrameworkFilter(null); setSearchTerm('') }}
                className="badge badge-sm border border-theme-border bg-theme-elevated text-theme-text-primary hover:bg-theme-hover transition-colors"
              >
                Clear all filters
              </button>
            }
          />
        )
      ) : namespacedGroups ? (
        /* Namespace-grouped view */
        <div className="flex flex-col gap-1">
          {namespacedGroups.map(([ns, nsGroups]) => {
            const nsExpanded = expandedNS.has(ns)
            const nsDanger = nsGroups.reduce((n, g) => n + g.danger, 0)
            const nsWarning = nsGroups.reduce((n, g) => n + g.warning, 0)
            return (
              <div key={ns}>
                <button
                  onClick={() => toggleNS(ns)}
                  className="group flex items-center gap-3 w-full px-4 py-2 rounded-lg hover:bg-theme-hover/30 transition-colors text-left"
                >
                  <ChevronRight className={clsx('w-4 h-4 text-theme-text-tertiary shrink-0 transition-transform duration-200', nsExpanded && 'rotate-90')} />
                  <span className="text-sm font-semibold text-theme-text-primary">{ns}</span>
                  <span className="text-xs text-theme-text-tertiary">{pluralize(nsGroups.length, 'resource')}</span>
                  <span className="flex-1" />
                  <div className="flex items-center gap-3 shrink-0">
                    {nsDanger > 0 && <span className={clsx('text-xs font-semibold tabular-nums', SEVERITY_TEXT.error)}>{nsDanger} critical</span>}
                    {nsWarning > 0 && <span className={clsx('text-xs font-semibold tabular-nums', SEVERITY_TEXT.warning)}>{nsWarning} warning</span>}
                  </div>
                  {onHideNamespace && ns !== '(cluster-scoped)' && (
                    <ContextMenu items={[{ label: `Hide ${ns} namespace`, onClick: () => onHideNamespace(ns) }]} />
                  )}
                </button>
                <div
                  className="grid transition-[grid-template-rows] duration-200 ease-out"
                  style={{ gridTemplateRows: nsExpanded ? '1fr' : '0fr' }}
                >
                  <div className="overflow-hidden">
                    <div className="pl-4">
                      {nsGroups.map(g => (
                        <ResourceGroupRow key={`${g.kind}/${g.namespace}/${g.name}`} group={g} checks={checks} expanded={expanded} onToggle={toggle} onResourceClick={onResourceClick} onHideCheck={onHideCheck} onHideCategory={onHideCategory} />
                      ))}
                    </div>
                  </div>
                </div>
              </div>
            )
          })}
        </div>
      ) : filteredGroups ? (
        /* Flat grouped view */
        <div className="flex flex-col gap-0.5">
          {filteredGroups.map(g => (
            <ResourceGroupRow key={`${g.kind}/${g.namespace}/${g.name}`} group={g} checks={checks} expanded={expanded} onToggle={toggle} onResourceClick={onResourceClick} onHideCheck={onHideCheck} onHideCategory={onHideCategory} onHideNamespace={onHideNamespace} showNamespace />
          ))}
        </div>
      ) : (
        /* Flat fallback (per-resource view) */
        <div className="flex flex-col gap-1">
          {filteredFindings?.map((f, i) => (
            <FlatFindingRow
              key={`${f.cluster?.id ?? ''}-${f.checkID}-${i}`}
              finding={f}
              onResourceClick={onResourceClick}
              showCluster={multiCluster}
              onClusterClick={onClusterClick}
            />
          ))}
        </div>
      )}
    </div>
  )
}

function FindingDetail({ finding, meta, onHideCheck, onHideCategory }: {
  finding: AuditFinding
  meta?: CheckMeta
  onHideCheck?: (checkID: string, title: string) => void
  onHideCategory?: (category: string) => void
}) {
  const isDanger = finding.severity === 'danger'
  const menuItems: ContextMenuItem[] = []
  if (onHideCheck) {
    menuItems.push({ label: `Hide "${meta?.title || finding.checkID}" check`, onClick: () => onHideCheck(finding.checkID, meta?.title || finding.checkID) })
  }
  if (onHideCategory) {
    menuItems.push({ label: `Hide all ${finding.category} checks`, onClick: () => onHideCategory(finding.category) })
  }

  return (
    <div className="flex flex-col gap-0.5 px-3 py-2 rounded group/finding">
      <div className="flex items-center gap-3">
        {isDanger ? (
          <ShieldAlert className={clsx('w-4 h-4 shrink-0', SEVERITY_TEXT.error)} />
        ) : (
          <AlertTriangle className={clsx('w-4 h-4 shrink-0', SEVERITY_TEXT.warning)} />
        )}
        <span className="text-sm text-theme-text-primary flex-1 min-w-0">{finding.message}</span>
        <span className={clsx('badge-sm text-[10px]', BP_CATEGORY_BADGE[finding.category] || DEFAULT_BADGE_COLOR)}>
          {finding.category}
        </span>
        {menuItems.length > 0 && <ContextMenu items={menuItems} />}
      </div>
      {meta && (
        <div className="pl-7 flex flex-col gap-0.5">
          <span className="text-xs text-theme-text-tertiary">{meta.description}</span>
          <span className="text-xs text-theme-text-secondary">Fix: {meta.remediation}</span>
        </div>
      )}
    </div>
  )
}

function ResourceGroupRow({ group: g, checks, expanded, onToggle, onResourceClick, onHideCheck, onHideCategory, onHideNamespace, showNamespace = false }: {
  group: ResourceGroup
  checks?: Record<string, CheckMeta>
  expanded: Set<string>
  onToggle: (key: string) => void
  onResourceClick?: (kind: string, namespace: string, name: string) => void
  onHideCheck?: (checkID: string, title: string) => void
  onHideCategory?: (category: string) => void
  onHideNamespace?: (namespace: string) => void
  showNamespace?: boolean
}) {
  const key = `${g.kind}/${g.namespace}/${g.name}`
  const isExpanded = expanded.has(key)
  const hasDanger = g.danger > 0

  return (
    <div>
      <button
        onClick={() => onToggle(key)}
        className="group flex items-center gap-3 w-full px-4 py-2.5 rounded-lg hover:bg-theme-hover/50 transition-colors text-left focus-visible:ring-2 focus-visible:ring-theme-text-primary/20 focus-visible:outline-none"
      >
        <ChevronRight className={clsx('w-3.5 h-3.5 text-theme-text-tertiary shrink-0 transition-transform duration-200', isExpanded && 'rotate-90')} />
        {hasDanger ? (
          <ShieldAlert className={clsx('w-4 h-4 shrink-0', SEVERITY_TEXT.error)} />
        ) : (
          <AlertTriangle className={clsx('w-4 h-4 shrink-0', SEVERITY_TEXT.warning)} />
        )}
        <span className="text-xs text-theme-text-tertiary shrink-0">{g.kind}</span>
        {onResourceClick ? (
          <span
            role="link"
            tabIndex={0}
            onClick={(e) => { e.stopPropagation(); onResourceClick(g.kind, g.namespace, g.name) }}
            onKeyDown={(e) => { if (e.key === 'Enter') { e.stopPropagation(); onResourceClick(g.kind, g.namespace, g.name) } }}
            className="text-sm font-medium text-skyhook-500 hover:text-skyhook-400 hover:underline cursor-pointer truncate max-w-[300px] inline-flex items-center gap-1"
          >
            {showNamespace && g.namespace ? `${g.namespace} / ` : ''}{g.name}
            <ExternalLink className="w-3 h-3 shrink-0 opacity-0 group-hover:opacity-100 transition-opacity" />
          </span>
        ) : (
          <span className="text-sm font-medium text-theme-text-primary truncate max-w-[300px]">
            {showNamespace && g.namespace ? `${g.namespace} / ` : ''}{g.name}
          </span>
        )}
        <span className="flex-1" />
        <div className="flex items-center gap-3 shrink-0">
          {g.danger > 0 && <span className={clsx('text-xs font-semibold tabular-nums', SEVERITY_TEXT.error)}>{g.danger} critical</span>}
          {g.warning > 0 && <span className={clsx('text-xs font-semibold tabular-nums', SEVERITY_TEXT.warning)}>{g.warning} warning</span>}
        </div>
        {showNamespace && onHideNamespace && g.namespace && (
          <ContextMenu items={[{ label: `Hide ${g.namespace} namespace`, onClick: () => onHideNamespace(g.namespace) }]} />
        )}
      </button>
      <div
        className="grid transition-[grid-template-rows] duration-200 ease-out"
        style={{ gridTemplateRows: isExpanded ? '1fr' : '0fr' }}
      >
        <div className="overflow-hidden">
          <div className="pl-11 pb-1">
            {g.findings.map((f, i) => (
              <FindingDetail key={`${f.checkID}-${i}`} finding={f} meta={checks?.[f.checkID]} onHideCheck={onHideCheck} onHideCategory={onHideCategory} />
            ))}
          </div>
        </div>
      </div>
    </div>
  )
}

function FlatFindingRow({ finding, onResourceClick, showCluster, onClusterClick }: { finding: AuditFinding; onResourceClick?: (kind: string, namespace: string, name: string) => void; showCluster?: boolean; onClusterClick?: (clusterId: string) => void }) {
  const isDanger = finding.severity === 'danger'
  const severityColor = isDanger ? SEVERITY_TEXT.error : SEVERITY_TEXT.warning

  return (
    <div className="flex items-center gap-3 px-4 py-2.5 rounded-lg hover:bg-theme-hover/50 transition-colors">
      {isDanger ? (
        <ShieldAlert className={clsx('w-4 h-4 shrink-0', severityColor)} />
      ) : (
        <AlertTriangle className={clsx('w-4 h-4 shrink-0', severityColor)} />
      )}
      {showCluster && finding.cluster && (
        // Cluster column for multi-cluster contexts. Renders before
        // the kind/namespace/name path so cluster scope is established
        // first in the read order.
        onClusterClick ? (
          <button
            onClick={() => onClusterClick(finding.cluster!.id)}
            className="text-xs text-[var(--color-radar-accent)] hover:underline shrink-0 max-w-[160px] truncate text-left"
          >
            {finding.cluster.name}
          </button>
        ) : (
          <span className="text-xs text-theme-text-secondary shrink-0 max-w-[160px] truncate">
            {finding.cluster.name}
          </span>
        )
      )}
      {onResourceClick ? (
        <button
          onClick={() => onResourceClick(finding.kind, finding.namespace, finding.name)}
          className="text-xs font-medium text-theme-text-secondary hover:text-theme-text-primary transition-colors shrink-0 max-w-[200px] truncate text-left focus-visible:ring-1 focus-visible:ring-theme-text-primary/30 focus-visible:outline-none rounded"
        >
          {finding.kind}/{finding.namespace ? `${finding.namespace}/` : ''}{finding.name}
        </button>
      ) : (
        <span className="text-xs font-medium text-theme-text-secondary shrink-0 max-w-[200px] truncate">
          {finding.kind}/{finding.namespace ? `${finding.namespace}/` : ''}{finding.name}
        </span>
      )}
      <span className="text-xs text-theme-text-primary flex-1 min-w-0">{finding.message}</span>
      <span className={clsx('badge-sm text-[10px]', BP_CATEGORY_BADGE[finding.category] || DEFAULT_BADGE_COLOR)}>
        {finding.category}
      </span>
    </div>
  )
}

function SummaryBadge({ label, count, color }: { label: string; count: number; color: string }) {
  return (
    <div className="flex items-center gap-2">
      <span className={clsx('text-2xl font-bold tabular-nums', count > 0 ? color : 'text-theme-text-tertiary')}>{count}</span>
      <span className="text-xs text-theme-text-secondary">{label}</span>
    </div>
  )
}

interface ContextMenuItem {
  label: string
  onClick: () => void
}

function ContextMenu({ items }: { items: ContextMenuItem[] }) {
  const [open, setOpen] = useState(false)
  const ref = useRef<HTMLDivElement>(null)

  useEffect(() => {
    if (!open) return
    const handler = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) setOpen(false)
    }
    document.addEventListener('mousedown', handler)
    return () => document.removeEventListener('mousedown', handler)
  }, [open])

  return (
    <div ref={ref} className="relative">
      <button
        onClick={(e) => { e.stopPropagation(); setOpen(!open) }}
        className="p-1 rounded hover:bg-theme-hover text-theme-text-tertiary hover:text-theme-text-secondary opacity-0 group-hover:opacity-100 group-hover/finding:opacity-100 transition-opacity"
      >
        <MoreHorizontal className="w-4 h-4" />
      </button>
      {open && (
        <div className="absolute right-0 top-full mt-1 min-w-48 bg-theme-surface border border-theme-border rounded-lg shadow-xl z-50 py-1">
          {items.map((item, i) => (
            <button
              key={i}
              onClick={(e) => { e.stopPropagation(); item.onClick(); setOpen(false) }}
              className="w-full text-left px-3 py-1.5 text-xs text-theme-text-secondary hover:text-theme-text-primary hover:bg-theme-hover transition-colors flex items-center gap-2"
            >
              <EyeOff className="w-3.5 h-3.5 shrink-0" />
              {item.label}
            </button>
          ))}
        </div>
      )}
    </div>
  )
}
