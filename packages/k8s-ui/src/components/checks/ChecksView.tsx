import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { ChevronDown, ChevronRight, ExternalLink, EyeOff, MoreHorizontal, Search, ShieldCheck, Wrench, X } from 'lucide-react'
import { ClusterName, EmptyState, FilterPill } from '../ui'
import type { CheckMeta, CheckReference } from '../audit'
import { CHECK_SEVERITIES, CHECK_SEVERITY_RANK, type Check, type CheckSeverity, type EffectiveCheckFinding, type CheckResourceRef } from './types'
import {
  SEVERITY_BADGE_CLASS,
  SEVERITY_FILL_CLASS,
  SEVERITY_LABEL,
  SEVERITY_RAIL_CLASS,
  SEVERITY_TEXT_CLASS,
  categoryBadgeClass,
} from './severity'

const CATEGORIES: readonly string[] = ['Security', 'Reliability', 'Efficiency']

// Affected-resources shown inline before "View all". A check can fail on
// thousands of resources; the card stays scannable and only the rare big-list
// case pays the cost of a full expand.
const RESOURCE_CAP = 8
// Clusters shown before "View all clusters" when a check spans the fleet.
const CLUSTER_CAP = 12
// At/below this many clusters, a check's cluster groups open with their
// resources visible; above it they start collapsed (just cluster · count).
const AUTO_EXPAND_CLUSTERS = 2

export interface ChecksViewProps {
  /** Per-(cluster, check) rows. The component groups them by check itself, so
   *  the same check across many clusters collapses to one fleet row (cluster
   *  becomes a drill-down sub-level). Single-cluster hosts pass one row per
   *  check and get a flat resource list with no sub-level. */
  checks: Check[]
  /** Check catalog (checkID → definition): how-to-fix / description / framework
   *  filter / reference links. */
  catalog: Record<string, CheckMeta>
  /** True when at least one source returned audit data. */
  anyData: boolean
  /** Deep-link href for a resource (host routing). Omit for non-link text. */
  resourceHref?: (ref: CheckResourceRef) => string
  /** In-app resource navigation (client-side). Takes precedence over href. */
  onResourceClick?: (ref: CheckResourceRef) => void
  /** Display label for a check's source cluster. Omit (single-cluster OSS) to
   *  drop the cluster sub-level + cluster filter entirely. */
  clusterLabel?: (check: Check) => string | undefined
  /** Controlled cluster facet. When `onClusterFilterChange` is supplied the
   *  selection is owned by the host (e.g. synced to a `?clusters=` URL param);
   *  otherwise ChecksView keeps it in internal state. `clusterFilter` is the
   *  current selection (cluster IDs); ignored unless controlled.
   *
   *  Controlled contract: apply the update *synchronously* (the new value is
   *  read back via `clusterFilter` on the next render). Toggling computes from
   *  the current `clusterFilter`, so a host that *defers* propagation (e.g.
   *  `startTransition`) could let a rapid follow-up toggle read stale state. */
  clusterFilter?: string[]
  onClusterFilterChange?: (clusterIds: string[]) => void
  /** Resolve a cluster ID → display label for the selected-cluster chips.
   *  Needed because a filter can target a cluster with no checks (offline /
   *  errored / clean) that won't appear in the in-data cluster options; the
   *  host knows the name regardless. Falls back to the id when unresolved. */
  clusterLabelById?: (clusterId: string) => string | undefined
  /** Empty-state CTA when there's no data. */
  emptyAction?: ReactNode
  /** Optional per-check "hide" actions (OSS local tuning; Hub omits — Policy
   *  tab governs hiding there). Operate on the whole check, fleet-wide. */
  onHideCheck?: (checkID: string, title: string) => void
  onHideCategory?: (category: string) => void
}

// A check rolled up across every cluster it fires on — one row of the fleet
// queue. `clusters` holds the per-cluster Check rows underneath.
interface FleetCheck {
  checkID: string
  title: string
  category: string
  severity: CheckSeverity
  totalResources: number
  totalFindings: number
  clusters: Check[]
}

export function ChecksView({ checks, catalog, anyData, resourceHref, onResourceClick, clusterLabel, clusterLabelById, clusterFilter: clusterFilterProp, onClusterFilterChange, emptyAction, onHideCheck, onHideCategory }: ChecksViewProps) {
  const [severityFilter, setSeverityFilter] = useState<Set<CheckSeverity>>(new Set())
  const [categoryFilter, setCategoryFilter] = useState<Set<string>>(new Set())
  const [frameworkFilter, setFrameworkFilter] = useState<Set<string>>(new Set())
  const [search, setSearch] = useState('')
  const [openId, setOpenId] = useState<string | null>(null)

  // Cluster facet is controlled when the host opts in (onClusterFilterChange);
  // otherwise it lives in internal state. Either way the rest of the component
  // reads `clusterFilter` (a Set) and writes via `setClusterFilter`, which
  // accepts both value and updater forms like a useState setter.
  const [clusterFilterInternal, setClusterFilterInternal] = useState<Set<string>>(new Set())
  const clusterControlled = onClusterFilterChange !== undefined
  const clusterFilterKey = (clusterFilterProp ?? []).join(',')
  const clusterFilter = useMemo(
    () => (clusterControlled ? new Set(clusterFilterProp ?? []) : clusterFilterInternal),
    // clusterFilterKey collapses array identity churn from the host to its contents.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [clusterControlled, clusterFilterKey, clusterFilterInternal],
  )
  const setClusterFilter = (action: React.SetStateAction<Set<string>>) => {
    if (clusterControlled) {
      const next = typeof action === 'function' ? action(clusterFilter) : action
      onClusterFilterChange!([...next])
    } else {
      setClusterFilterInternal(action)
    }
  }

  // Clusters present in the data (id → label), for the cluster facet. Empty
  // when the host doesn't supply clusterLabel (single-cluster OSS).
  const clusterOptions = useMemo(() => {
    if (!clusterLabel) return []
    const m = new Map<string, string>()
    for (const c of checks) {
      const id = c.subject.cluster_id
      if (!m.has(id)) m.set(id, clusterLabel(c) || id)
    }
    return [...m.entries()].map(([id, label]) => ({ id, label })).sort((a, b) => a.label.localeCompare(b.label))
  }, [checks, clusterLabel])
  const multiCluster = clusterOptions.length > 1

  const frameworks = useMemo(() => {
    const set = new Set<string>()
    for (const m of Object.values(catalog)) m.frameworks?.forEach((f) => set.add(f))
    return Array.from(set).sort()
  }, [catalog])

  const searchLower = search.toLowerCase()

  // Filter the per-cluster rows, then group by checkID into fleet rows.
  const fleetChecks = useMemo(() => {
    const kept = checks.filter((c) => {
      if (severityFilter.size > 0 && !severityFilter.has(c.effectiveSeverity)) return false
      if (categoryFilter.size > 0 && !categoryFilter.has(c.category)) return false
      if (clusterFilter.size > 0 && !clusterFilter.has(c.subject.cluster_id)) return false
      if (frameworkFilter.size > 0) {
        const fws = catalog[c.checkID]?.frameworks
        if (!fws || !fws.some((f) => frameworkFilter.has(f))) return false
      }
      if (searchLower) {
        const hay = `${c.title} ${c.checkID} ${c.message} ${c.subject.namespace} ${c.subject.name}`.toLowerCase()
        if (!hay.includes(searchLower)) return false
      }
      return true
    })

    const byCheck = new Map<string, Check[]>()
    for (const c of kept) {
      const arr = byCheck.get(c.checkID)
      if (arr) arr.push(c)
      else byCheck.set(c.checkID, [c])
    }

    const out: FleetCheck[] = []
    for (const [checkID, group] of byCheck) {
      // Worst severity wins for the fleet row (a check's tier is set by check +
      // org policy, so it's consistent across clusters; this is just defensive).
      const severity = group.reduce<CheckSeverity>(
        (worst, c) => (CHECK_SEVERITY_RANK[c.effectiveSeverity] > CHECK_SEVERITY_RANK[worst] ? c.effectiveSeverity : worst),
        'low',
      )
      const clusters = [...group].sort((a, b) => {
        const r = CHECK_SEVERITY_RANK[b.effectiveSeverity] - CHECK_SEVERITY_RANK[a.effectiveSeverity]
        if (r !== 0) return r
        if (b.affectedResources !== a.affectedResources) return b.affectedResources - a.affectedResources
        return (clusterLabel?.(a) || '').localeCompare(clusterLabel?.(b) || '')
      })
      out.push({
        checkID,
        title: group[0].title,
        category: group[0].category,
        severity,
        totalResources: group.reduce((n, c) => n + c.affectedResources, 0),
        totalFindings: group.reduce((n, c) => n + c.affectedFindings, 0),
        clusters,
      })
    }
    // Worst-first across the whole queue (severity, then blast radius, then title).
    return out.sort((a, b) => {
      const r = CHECK_SEVERITY_RANK[b.severity] - CHECK_SEVERITY_RANK[a.severity]
      if (r !== 0) return r
      if (b.totalResources !== a.totalResources) return b.totalResources - a.totalResources
      return a.title.localeCompare(b.title)
    })
  }, [checks, catalog, severityFilter, categoryFilter, frameworkFilter, clusterFilter, searchLower, clusterLabel])

  const { totals, totalFindings, clusterCount } = useMemo(() => {
    const totals: Record<CheckSeverity, number> = { critical: 0, high: 0, medium: 0, low: 0 }
    let totalFindings = 0
    // Distinct clusters spanned by the *filtered* queue — so the header count
    // tracks the cluster facet rather than the full fleet (a deep-link or
    // facet pick to one cluster reads "1 cluster", not the unfiltered total).
    const clusterIds = new Set<string>()
    for (const fc of fleetChecks) {
      totals[fc.severity] += 1
      totalFindings += fc.totalFindings
      for (const c of fc.clusters) clusterIds.add(c.subject.cluster_id)
    }
    return { totals, totalFindings, clusterCount: clusterIds.size }
  }, [fleetChecks])

  const toggle = <T,>(setter: React.Dispatch<React.SetStateAction<Set<T>>>, v: T) =>
    setter((prev) => {
      const next = new Set(prev)
      if (next.has(v)) next.delete(v)
      else next.add(v)
      return next
    })

  const hasFilters = severityFilter.size > 0 || categoryFilter.size > 0 || frameworkFilter.size > 0 || clusterFilter.size > 0 || search !== ''
  const clearAll = () => {
    setSeverityFilter(new Set())
    setCategoryFilter(new Set())
    setFrameworkFilter(new Set())
    setClusterFilter(new Set())
    setSearch('')
  }

  return (
    <div className="flex flex-col gap-4">
      {/* Triage header: distribution bar + filter chips + search. */}
      <div className="flex flex-col gap-3.5 rounded-2xl border border-theme-border bg-theme-surface p-4 shadow-theme-sm">
        <div className="flex flex-wrap items-end justify-between gap-3">
          <div className="flex items-baseline gap-2">
            <span className="text-2xl font-semibold tabular-nums text-theme-text-primary">{fleetChecks.length}</span>
            <span className="text-sm text-theme-text-secondary">
              {fleetChecks.length === 1 ? 'check' : 'checks'}
              {totalFindings > fleetChecks.length && <span className="text-theme-text-tertiary"> · {totalFindings} findings</span>}
              {clusterOptions.length > 1 && clusterCount > 0 && (
                <span className="text-theme-text-tertiary"> · {clusterCount} {clusterCount === 1 ? 'cluster' : 'clusters'}</span>
              )}
            </span>
          </div>
          <div className="relative">
            <Search className="absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-theme-text-tertiary" />
            <input
              type="text"
              placeholder="Search checks…"
              value={search}
              onChange={(e) => setSearch(e.target.value)}
              className="w-64 rounded-lg border border-theme-border-light bg-theme-base py-1.5 pl-9 pr-8 text-sm text-theme-text-primary placeholder-theme-text-disabled focus:outline-none focus:ring-2 focus:ring-[var(--color-radar-accent)]"
            />
            {search && (
              <button
                type="button"
                onClick={() => setSearch('')}
                aria-label="Clear search"
                className="absolute right-2 top-1/2 -translate-y-1/2 rounded p-0.5 text-theme-text-tertiary hover:text-theme-text-primary"
              >
                <X className="h-3.5 w-3.5" />
              </button>
            )}
          </div>
        </div>

        <CheckSeverityBar totals={totals} />

        <div className="flex flex-wrap items-center gap-1.5">
          {CHECK_SEVERITIES.map((s) => (
            <CheckSeverityChip key={s} severity={s} count={totals[s]} active={severityFilter.has(s)} onClick={() => toggle(setSeverityFilter, s)} />
          ))}
          <span className="mx-1.5 h-5 w-px bg-theme-border" />
          {CATEGORIES.map((c) => (
            <FilterPill key={c} label={c} active={categoryFilter.has(c)} onClick={() => toggle(setCategoryFilter, c)} />
          ))}
          {frameworks.length > 0 && (
            <>
              <span className="mx-1.5 h-5 w-px bg-theme-border" />
              {frameworks.map((fw) => (
                <FilterPill key={fw} label={fw} active={frameworkFilter.has(fw)} onClick={() => toggle(setFrameworkFilter, fw)} />
              ))}
            </>
          )}
          {multiCluster && (
            <>
              <span className="mx-1.5 h-5 w-px bg-theme-border" />
              <ClusterFilter options={clusterOptions} selected={clusterFilter} onToggle={(id) => toggle(setClusterFilter, id)} onClear={() => setClusterFilter(new Set())} />
            </>
          )}
          {/* Selected clusters as removable chips. The dropdown is the
              add/discover affordance (clusters are high-cardinality); the chips
              make the *active* selection visible + clearable. Gated on the
              selection itself — NOT on multiCluster — because a deep-link can
              filter to a cluster while ≤1 cluster currently has checks (so the
              dropdown is hidden); the chip is then the only thing that reveals
              and clears the filter, which is exactly the deep-link case it's
              for. Own leading divider when the dropdown isn't rendering. */}
          {clusterFilter.size > 0 && (
            <>
              {!multiCluster && <span className="mx-1.5 h-5 w-px bg-theme-border" />}
              {[...clusterFilter].map((id) => {
                const label = clusterLabelById?.(id) ?? clusterOptions.find((o) => o.id === id)?.label ?? id
                return (
                  <span
                    key={id}
                    className="inline-flex items-center gap-1 rounded-full border border-[var(--color-radar-accent)]/30 bg-[var(--color-radar-accent)]/10 py-1 pl-2.5 pr-1 text-xs text-theme-text-primary"
                  >
                    <span className="min-w-0 max-w-[12rem] truncate">
                      <ClusterName name={label} />
                    </span>
                    <button
                      type="button"
                      onClick={() => toggle(setClusterFilter, id)}
                      aria-label={`Remove ${label} filter`}
                      className="rounded-full p-0.5 text-theme-text-tertiary transition-colors hover:bg-theme-hover hover:text-theme-text-primary"
                    >
                      <X className="h-3 w-3" />
                    </button>
                  </span>
                )
              })}
            </>
          )}
        </div>
      </div>

      {fleetChecks.length === 0 ? (
        hasFilters ? (
          <EmptyState
            tone="filtered"
            variant="card"
            headline="No checks match the current filters"
            body="Clear a filter to see more of the queue."
            action={
              <button
                type="button"
                onClick={clearAll}
                className="badge badge-sm border border-theme-border bg-theme-elevated text-theme-text-primary transition-colors hover:bg-theme-hover"
              >
                Clear all filters
              </button>
            }
          />
        ) : anyData ? (
          <EmptyState
            tone="healthy"
            variant="card"
            icon={ShieldCheck}
            headline="Nothing to remediate"
            body="Every audited resource passed its checks."
          />
        ) : (
          <EmptyState headline="No check data yet" body="Run an audit to populate the remediation queue." action={emptyAction} />
        )
      ) : (
        <ol className="flex flex-col gap-1.5">
          {fleetChecks.map((fc) => (
            <FleetCheckRow
              key={fc.checkID}
              fc={fc}
              meta={catalog[fc.checkID]}
              clusterLabel={clusterLabel}
              open={openId === fc.checkID}
              onToggle={() => setOpenId((cur) => (cur === fc.checkID ? null : fc.checkID))}
              resourceHref={resourceHref}
              onResourceClick={onResourceClick}
              onHideCheck={onHideCheck}
              onHideCategory={onHideCategory}
            />
          ))}
        </ol>
      )}
    </div>
  )
}

export function CheckSeverityBar({ totals }: { totals: Record<CheckSeverity, number> }) {
  const sum = CHECK_SEVERITIES.reduce((n, s) => n + totals[s], 0)
  return (
    <div className="flex h-1.5 overflow-hidden rounded-full bg-theme-elevated" role="img" aria-label="Severity distribution">
      {sum === 0
        ? null
        : CHECK_SEVERITIES.map((s) =>
            totals[s] > 0 ? (
              <div key={s} className={`${SEVERITY_FILL_CLASS[s]} transition-[width] duration-500 ease-out`} style={{ width: `${(totals[s] / sum) * 100}%` }} />
            ) : null,
          )}
    </div>
  )
}

export function CheckSeverityChip({ severity, count, active, onClick }: { severity: CheckSeverity; count: number; active: boolean; onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-pressed={active}
      className={[
        'group inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-xs transition-colors',
        active ? 'border-theme-border bg-theme-elevated text-theme-text-primary' : 'border-transparent text-theme-text-secondary hover:bg-theme-hover/60',
      ].join(' ')}
    >
      <span className={`h-2 w-2 rounded-full ${SEVERITY_FILL_CLASS[severity]} ${count === 0 ? 'opacity-30' : ''}`} />
      <span className={`font-semibold tabular-nums ${count > 0 ? SEVERITY_TEXT_CLASS[severity] : 'text-theme-text-tertiary'}`}>{count}</span>
      <span>{SEVERITY_LABEL[severity]}</span>
    </button>
  )
}

// Re-export the audit CheckReference (single source of truth) — CheckMeta.references
// is typed against this same shape, so the two must not drift apart.
export type { CheckReference }

export interface CheckRemediationBlockProps {
  description?: string
  remediation?: string
  references?: CheckReference[]
  layout?: 'columns' | 'stack'
}

export function CheckRemediationBlock({ description, remediation, references, layout = 'columns' }: CheckRemediationBlockProps) {
  if (!description && !remediation && (!references || references.length === 0)) return null

  if (layout === 'stack') {
    return (
      <div className="flex flex-col gap-2">
        {description && <p className="text-[13px] leading-relaxed text-theme-text-secondary">{description}</p>}
        {remediation && (
          <div>
            <div className="text-[11px] uppercase tracking-wider text-theme-text-tertiary">How to fix</div>
            <p className="mt-0.5 text-[13px] leading-relaxed text-theme-text-secondary">{remediation}</p>
          </div>
        )}
        {references && references.length > 0 && <CheckReferenceLinks references={references} />}
      </div>
    )
  }

  return (
    <>
      <div className="flex flex-col gap-4 md:flex-row md:gap-8">
        {remediation && (
          <section className="md:flex-1">
            <h4 className="mb-1 flex items-center gap-1.5 text-[11px] font-semibold uppercase tracking-wide text-[var(--color-radar-accent)]">
              <Wrench className="h-3.5 w-3.5" /> How to fix
            </h4>
            <p className="text-sm leading-relaxed text-theme-text-primary">{remediation}</p>
          </section>
        )}
        {description && (
          <section className="md:flex-1">
            <h4 className="mb-1 text-[11px] font-semibold uppercase tracking-wide text-theme-text-tertiary">What this checks</h4>
            <p className="text-sm leading-relaxed text-theme-text-secondary">{description}</p>
          </section>
        )}
      </div>
      {references && references.length > 0 && <CheckReferenceLinks references={references} />}
    </>
  )
}

function CheckReferenceLinks({ references }: { references: CheckReference[] }) {
  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-1.5">
      {references.map((r) => (
        <a
          key={r.url}
          href={r.url}
          target="_blank"
          rel="noreferrer"
          className="inline-flex items-center gap-1 text-xs font-medium text-[var(--color-radar-accent)] hover:underline"
        >
          {r.label}
          <ExternalLink className="h-3 w-3" />
        </a>
      ))}
    </div>
  )
}

export interface CheckCardShellProps {
  title: ReactNode
  category: string
  severity: CheckSeverity
  open: boolean
  onToggle: () => void
  summary?: ReactNode
  description?: ReactNode
  renderActions?: () => ReactNode
  children?: ReactNode
  as?: 'li' | 'div'
  className?: string
  dimmed?: boolean
}

export function CheckCardShell({
  title,
  category,
  severity,
  open,
  onToggle,
  summary,
  description,
  renderActions,
  children,
  as = 'li',
  className,
  dimmed,
}: CheckCardShellProps) {
  const Container = as
  return (
    <Container
      className={[
        'overflow-hidden rounded-xl border border-theme-border bg-theme-surface shadow-theme-sm',
        dimmed ? 'opacity-60' : '',
        className ?? '',
      ]
        .filter(Boolean)
        .join(' ')}
    >
      <div
        role="button"
        tabIndex={0}
        aria-expanded={open}
        onClick={onToggle}
        onKeyDown={(e) => {
          if (e.target !== e.currentTarget) return
          if (e.key === 'Enter' || e.key === ' ') {
            e.preventDefault()
            onToggle()
          }
        }}
        className={`group flex cursor-pointer items-start gap-3 border-l-2 py-3 pl-3 pr-4 transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-[var(--color-radar-accent)]/40 ${SEVERITY_RAIL_CLASS[severity]}`}
      >
        <ChevronRight className={`mt-0.5 h-4 w-4 shrink-0 text-theme-text-tertiary transition-transform duration-200 ${open ? 'rotate-90' : ''}`} />

        <div className="flex min-w-0 flex-1 flex-col gap-1.5">
          <div className="flex flex-wrap items-center gap-2">
            <span className="truncate text-sm font-semibold text-theme-text-primary">{title}</span>
            <span className={`badge-sm shrink-0 text-[10px] ${categoryBadgeClass(category)}`}>{category}</span>
          </div>
          {description}
          {summary}
        </div>

        <span className={`badge-sm mt-0.5 shrink-0 text-[10px] font-semibold ${SEVERITY_BADGE_CLASS[severity]}`}>
          {SEVERITY_LABEL[severity]}
        </span>
        {renderActions?.()}
      </div>

      {/* Kept mounted (not `open &&`) so the grid-rows transition animates the
          collapse too, matching IssueRow; inert when closed so SR + tab skip
          the clipped content. */}
      <div className="grid transition-[grid-template-rows] duration-200 ease-out" style={{ gridTemplateRows: open ? '1fr' : '0fr' }}>
        <div className="overflow-hidden" inert={!open || undefined}>
          <div className="flex flex-col gap-4 border-t border-theme-border bg-theme-base/40 px-4 py-4 pl-11">{children}</div>
        </div>
      </div>
    </Container>
  )
}

export interface CheckClusterBreakdownGroup {
  id: string
  label: ReactNode
  environment?: string
  count: ReactNode
}

export interface CheckClusterBreakdownShellProps<T extends CheckClusterBreakdownGroup> {
  heading: ReactNode
  groups: T[]
  renderGroupBody: (group: T) => ReactNode
  clusterCap?: number
  autoExpandLimit?: number
}

export function CheckClusterBreakdownShell<T extends CheckClusterBreakdownGroup>({
  heading,
  groups,
  renderGroupBody,
  clusterCap = CLUSTER_CAP,
  autoExpandLimit = AUTO_EXPAND_CLUSTERS,
}: CheckClusterBreakdownShellProps<T>) {
  const [openClusters, setOpenClusters] = useState<Set<string>>(
    () => new Set(groups.length <= autoExpandLimit ? groups.map((g) => g.id) : []),
  )
  const [showAllClusters, setShowAllClusters] = useState(false)
  const shown = showAllClusters ? groups : groups.slice(0, clusterCap)
  const hiddenClusters = groups.length - shown.length

  return (
    <section className="flex flex-col gap-1.5">
      <h4 className="text-[11px] font-semibold uppercase tracking-wide text-theme-text-tertiary">{heading}</h4>
      <ul className="flex flex-col gap-1">
        {shown.map((group) => {
          const isOpen = openClusters.has(group.id)
          return (
            <li key={group.id} className="rounded-lg border border-theme-border/70 bg-theme-surface">
              <button
                type="button"
                aria-expanded={isOpen}
                onClick={() =>
                  setOpenClusters((prev) => {
                    const next = new Set(prev)
                    if (next.has(group.id)) next.delete(group.id)
                    else next.add(group.id)
                    return next
                  })
                }
                className="flex w-full items-center gap-2 rounded-lg px-2.5 py-1.5 text-left transition-colors hover:bg-theme-hover/50"
              >
                <ChevronDown className={`h-3.5 w-3.5 shrink-0 text-theme-text-tertiary transition-transform duration-200 ${isOpen ? '' : '-rotate-90'}`} />
                <span className="min-w-0 max-w-[260px] truncate text-sm font-medium text-theme-text-primary">{group.label}</span>
                {group.environment && (
                  <span className="shrink-0 rounded bg-theme-elevated px-1.5 py-0.5 text-[10px] font-medium uppercase tracking-wide text-theme-text-secondary ring-1 ring-theme-border">
                    {group.environment}
                  </span>
                )}
                <span className="flex-1" />
                <span className="shrink-0 text-xs tabular-nums text-theme-text-secondary">{group.count}</span>
              </button>
              <div className="grid transition-[grid-template-rows] duration-200 ease-out" style={{ gridTemplateRows: isOpen ? '1fr' : '0fr' }}>
                <div className="overflow-hidden" inert={!isOpen || undefined}>
                  <div className="px-2.5 pb-2 pl-7">{renderGroupBody(group)}</div>
                </div>
              </div>
            </li>
          )
        })}
      </ul>
      {hiddenClusters > 0 && (
        <button
          type="button"
          onClick={() => setShowAllClusters(true)}
          className="mt-0.5 inline-flex w-fit items-center gap-1 rounded px-2 py-1 text-xs font-medium text-[var(--color-radar-accent)] hover:underline"
        >
          View all {groups.length} clusters →
        </button>
      )}
    </section>
  )
}

function FleetCheckRow({
  fc,
  meta,
  clusterLabel,
  open,
  onToggle,
  resourceHref,
  onResourceClick,
  onHideCheck,
  onHideCategory,
}: {
  fc: FleetCheck
  meta?: CheckMeta
  clusterLabel?: (check: Check) => string | undefined
  open: boolean
  onToggle: () => void
  resourceHref?: (ref: CheckResourceRef) => string
  onResourceClick?: (ref: CheckResourceRef) => void
  onHideCheck?: (checkID: string, title: string) => void
  onHideCategory?: (category: string) => void
}) {
  const clusterCount = fc.clusters.length
  const single = clusterCount <= 1

  const menuItems: { label: string; onClick: () => void }[] = []
  if (onHideCheck) menuItems.push({ label: `Hide "${fc.title}" check`, onClick: () => onHideCheck(fc.checkID, fc.title) })
  if (onHideCategory) menuItems.push({ label: `Hide all ${fc.category} checks`, onClick: () => onHideCategory(fc.category) })

  return (
    <CheckCardShell
      title={fc.title}
      category={fc.category}
      severity={fc.severity}
      open={open}
      onToggle={onToggle}
      summary={
        <div className="flex min-w-0 items-center gap-1.5 text-xs text-theme-text-tertiary">
          <span className="shrink-0 font-medium text-theme-text-secondary tabular-nums">
            {fc.totalResources} {fc.totalResources === 1 ? 'resource' : 'resources'}
          </span>
          {clusterCount > 1 && (
            <>
              <span aria-hidden>·</span>
              <span className="shrink-0 tabular-nums">{clusterCount} clusters</span>
            </>
          )}
        </div>
      }
      renderActions={() => (menuItems.length > 0 ? <RowMenu items={menuItems} /> : null)}
    >
      <CheckRemediationBlock description={meta?.description} remediation={meta?.remediation} references={meta?.references} />

      <div className="border-t border-theme-border/70 pt-3">
        {single ? (
          <ResourceList
            label={`Affected resources (${fc.totalResources})`}
            check={fc.clusters[0]}
            resourceHref={resourceHref}
            onResourceClick={onResourceClick}
          />
        ) : (
          <ClusterBreakdown fc={fc} clusterLabel={clusterLabel} resourceHref={resourceHref} onResourceClick={onResourceClick} />
        )}
      </div>
    </CheckCardShell>
  )
}

// Per-cluster sub-groups for a check that spans the fleet. Each cluster is a
// collapsible row ("cluster · K resources"); open it for that cluster's
// resources. Caps the cluster list so a check across hundreds of clusters
// stays scannable.
function ClusterBreakdown({
  fc,
  clusterLabel,
  resourceHref,
  onResourceClick,
}: {
  fc: FleetCheck
  clusterLabel?: (check: Check) => string | undefined
  resourceHref?: (ref: CheckResourceRef) => string
  onResourceClick?: (ref: CheckResourceRef) => void
}) {
  return (
    <CheckClusterBreakdownShell
      heading={
        <>
          Affected resources <span className="tabular-nums">({fc.totalResources})</span> · {fc.clusters.length} clusters
        </>
      }
      groups={fc.clusters.map((c) => ({
        id: c.subject.cluster_id,
        label: <ClusterName name={clusterLabel?.(c) || c.subject.cluster_id} />,
        environment: c.environment,
        count: `${c.affectedResources} ${c.affectedResources === 1 ? 'resource' : 'resources'}`,
        check: c,
      }))}
      renderGroupBody={(group) => (
        <ResourceList check={group.check} resourceHref={resourceHref} onResourceClick={onResourceClick} />
      )}
    />
  )
}

// The resource lines for one cluster's check. `label` (set in the single-cluster
// case) renders a section heading; in the cluster-breakdown case the cluster row
// already provides the heading, so it's omitted.
function ResourceList({
  check,
  label,
  resourceHref,
  onResourceClick,
}: {
  check: Check
  label?: string
  resourceHref?: (ref: CheckResourceRef) => string
  onResourceClick?: (ref: CheckResourceRef) => void
}) {
  const [showAll, setShowAll] = useState(false)
  // The per-finding message only earns a place when it adds something the line
  // doesn't already show. Normalize each message by removing its own resource
  // name, then compare: all-same → it repeats the check or varies only by the
  // object name (already on the line) → drop it; still-different → real new info
  // (e.g. a container name) → keep it.
  const showMessage = useMemo(() => {
    if (check.findings.length === 0) return false
    const norm = (f: EffectiveCheckFinding) => {
      const n = f.resource.name
      return n ? (f.message ?? '').split(n).join('') : f.message ?? ''
    }
    const first = norm(check.findings[0])
    return check.findings.some((f) => norm(f) !== first)
  }, [check.findings])
  const list = showAll ? check.findings : check.findings.slice(0, RESOURCE_CAP)
  const hidden = check.findings.length - list.length

  return (
    <section className="flex flex-col gap-1.5">
      {label && <h4 className="text-[11px] font-semibold uppercase tracking-wide text-theme-text-tertiary">{label}</h4>}
      <ul className="flex flex-col gap-px">
        {list.map((f, i) => (
          <FindingLine
            key={`${f.resource.group}/${f.resource.kind}/${f.resource.namespace}/${f.resource.name}#${i}`}
            finding={f}
            showMessage={showMessage}
            resourceHref={resourceHref}
            onResourceClick={onResourceClick}
          />
        ))}
      </ul>
      {hidden > 0 && (
        <button
          type="button"
          onClick={() => setShowAll(true)}
          className="mt-0.5 inline-flex w-fit items-center gap-1 rounded px-2 py-1 text-xs font-medium text-[var(--color-radar-accent)] hover:underline"
        >
          View all {check.findings.length} →
        </button>
      )}
    </section>
  )
}

function FindingLine({
  finding,
  showMessage,
  resourceHref,
  onResourceClick,
}: {
  finding: EffectiveCheckFinding
  showMessage?: boolean
  resourceHref?: (ref: CheckResourceRef) => string
  onResourceClick?: (ref: CheckResourceRef) => void
}) {
  const r = finding.resource
  const linkable = !!(onResourceClick || resourceHref)
  const body = (
    <>
      <span className="shrink-0 font-mono text-[11px] uppercase tracking-wide text-theme-text-tertiary">{r.kind}</span>
      <span className={`shrink-0 font-medium ${linkable ? 'text-[var(--color-radar-accent)]' : 'text-theme-text-primary'}`}>
        {r.namespace ? `${r.namespace} / ` : ''}
        {r.name}
      </span>
      {linkable && <ExternalLink className="h-3 w-3 shrink-0 text-theme-text-tertiary opacity-0 transition-opacity group-hover/f:opacity-100" />}
      {showMessage && <span className="ml-1 truncate text-xs text-theme-text-tertiary">{finding.message}</span>}
    </>
  )
  const cls = 'group/f flex w-full items-center gap-2 rounded-md px-2 py-1 text-left text-sm transition-colors hover:bg-theme-hover/60'
  return (
    <li>
      {onResourceClick ? (
        <button type="button" onClick={() => onResourceClick(r)} className={cls}>
          {body}
        </button>
      ) : resourceHref ? (
        // Opens in a new tab to match the ExternalLink glyph in `body`. For the
        // Hub fleet view this href crosses the Hub→Cluster SPA boundary (a full
        // document nav); a new tab keeps the Checks queue intact and avoids the
        // cross-tree browser-Back dead-end (SKY-931).
        <a href={resourceHref(r)} target="_blank" rel="noreferrer" className={cls}>
          {body}
        </a>
      ) : (
        <span className="flex items-center gap-2 rounded-md px-2 py-1 text-sm">{body}</span>
      )}
    </li>
  )
}

// Multi-select cluster facet — clusters are high-cardinality, so a dropdown
// (portaled out of the row, like the row menu) rather than chips.
function ClusterFilter({
  options,
  selected,
  onToggle,
  onClear,
}: {
  options: { id: string; label: string }[]
  selected: Set<string>
  onToggle: (id: string) => void
  onClear: () => void
}) {
  const [menu, setMenu] = useState<{ top: number; left: number } | null>(null)
  const btnRef = useRef<HTMLButtonElement>(null)
  useEffect(() => {
    if (!menu) return
    const close = () => setMenu(null)
    const onDown = (e: MouseEvent) => {
      if (btnRef.current?.contains(e.target as Node)) return
      // keep open when clicking inside the menu
      if ((e.target as HTMLElement)?.closest?.('[data-cluster-menu]')) return
      close()
    }
    document.addEventListener('mousedown', onDown)
    window.addEventListener('scroll', close, true)
    window.addEventListener('resize', close)
    return () => {
      document.removeEventListener('mousedown', onDown)
      window.removeEventListener('scroll', close, true)
      window.removeEventListener('resize', close)
    }
  }, [menu])

  const open = (e: React.MouseEvent) => {
    e.stopPropagation()
    if (menu) {
      setMenu(null)
      return
    }
    const r = btnRef.current?.getBoundingClientRect()
    if (r) setMenu({ top: r.bottom + 4, left: r.left })
  }
  const active = selected.size > 0

  return (
    <>
      <button
        ref={btnRef}
        type="button"
        aria-haspopup="listbox"
        aria-expanded={menu != null}
        onClick={open}
        className={[
          'inline-flex items-center gap-1.5 rounded-full border px-2.5 py-1 text-xs transition-colors',
          active ? 'border-theme-border bg-theme-elevated text-theme-text-primary' : 'border-theme-border-light text-theme-text-secondary hover:bg-theme-hover/60',
        ].join(' ')}
      >
        Clusters
        <ChevronDown className="h-3 w-3" />
      </button>
      {menu &&
        createPortal(
          <div
            data-cluster-menu
            role="listbox"
            aria-multiselectable
            className="fixed z-[60] max-h-80 w-64 overflow-auto rounded-lg border border-theme-border bg-theme-surface py-1 shadow-xl"
            style={{ top: menu.top, left: menu.left }}
          >
            {active && (
              <button
                type="button"
                onClick={onClear}
                className="flex w-full items-center px-3 py-1.5 text-left text-xs text-theme-text-tertiary hover:bg-theme-hover hover:text-theme-text-primary"
              >
                Clear selection
              </button>
            )}
            {options.map((o) => {
              const on = selected.has(o.id)
              return (
                <button
                  key={o.id}
                  type="button"
                  role="option"
                  aria-selected={on}
                  onClick={() => onToggle(o.id)}
                  className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-sm text-theme-text-secondary transition-colors hover:bg-theme-hover hover:text-theme-text-primary"
                >
                  <span className={`flex h-3.5 w-3.5 shrink-0 items-center justify-center rounded-sm border ${on ? 'border-[var(--color-radar-accent)] bg-[var(--color-radar-accent)] text-white' : 'border-theme-border'}`}>
                    {on && <span className="text-[9px] leading-none">✓</span>}
                  </span>
                  <span className="min-w-0 truncate">
                    <ClusterName name={o.label} />
                  </span>
                </button>
              )
            })}
          </div>,
          document.body,
        )}
    </>
  )
}

// Quiet per-row overflow menu for the OSS local-tuning actions (hide check /
// category). Portaled to document.body so the row's overflow-hidden can't clip
// it; position captured at open time, any scroll/resize closes it.
function RowMenu({ items }: { items: { label: string; onClick: () => void }[] }) {
  const [menu, setMenu] = useState<{ top: number; right: number } | null>(null)
  const btnRef = useRef<HTMLButtonElement>(null)

  useEffect(() => {
    if (!menu) return
    const close = () => setMenu(null)
    const onDown = (e: MouseEvent) => {
      if (btnRef.current?.contains(e.target as Node)) return
      close()
    }
    document.addEventListener('mousedown', onDown)
    window.addEventListener('scroll', close, true)
    window.addEventListener('resize', close)
    return () => {
      document.removeEventListener('mousedown', onDown)
      window.removeEventListener('scroll', close, true)
      window.removeEventListener('resize', close)
    }
  }, [menu])

  const toggle = (e: React.MouseEvent) => {
    e.stopPropagation()
    if (menu) {
      setMenu(null)
      return
    }
    const r = btnRef.current?.getBoundingClientRect()
    if (r) setMenu({ top: r.bottom + 4, right: window.innerWidth - r.right })
  }

  return (
    <>
      <button
        ref={btnRef}
        type="button"
        aria-label="More actions"
        aria-haspopup="menu"
        aria-expanded={menu != null}
        onClick={toggle}
        onKeyDown={(e) => e.stopPropagation()}
        className="shrink-0 rounded p-1 text-theme-text-tertiary opacity-0 transition-opacity hover:bg-theme-hover hover:text-theme-text-secondary group-hover:opacity-100"
      >
        <MoreHorizontal className="h-4 w-4" />
      </button>
      {menu &&
        createPortal(
          <div
            role="menu"
            className="fixed z-[60] min-w-48 rounded-lg border border-theme-border bg-theme-surface py-1 shadow-xl"
            style={{ top: menu.top, right: menu.right }}
            onClick={(e) => e.stopPropagation()}
          >
            {items.map((it, i) => (
              <button
                key={i}
                type="button"
                role="menuitem"
                onClick={(e) => {
                  e.stopPropagation()
                  it.onClick()
                  setMenu(null)
                }}
                className="flex w-full items-center gap-2 px-3 py-1.5 text-left text-xs text-theme-text-secondary transition-colors hover:bg-theme-hover hover:text-theme-text-primary"
              >
                <EyeOff className="h-3.5 w-3.5 shrink-0" />
                {it.label}
              </button>
            ))}
          </div>,
          document.body,
        )}
    </>
  )
}
