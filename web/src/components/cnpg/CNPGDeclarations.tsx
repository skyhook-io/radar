import { useMemo, type ReactNode } from 'react'
import { clsx } from 'clsx'
import { AlertTriangle } from 'lucide-react'
import {
  Badge,
  CNPGLogicalPathView,
  cnpgDatabaseRoleFacts,
  cnpgRoleState,
  cnpgDatabaseRoleMeta,
  cnpgManagedBy,
  managedByLabel,
  type CNPGWorkspaceResponse,
  cnpgLogicalPaths,
  cnpgLogicalSlotFact,
  isApiGroup,
  refToSelectedResource,
  relationUnavailable,
  toneTextClass,
  type CNPGFleetRow,
  type CNPGLogicalPath,
} from '@skyhook-io/k8s-ui'
import { useCNPGPublisherSlots } from './logicalSlots'
import type { SelectedResource } from '../../types'
import { CNPGWorkspaceHeader, CoverageNotice, clusterResource, cnpgResource, coverageEmpty, worstCoverage, type CNPGScreenProps } from './shared'
import { FilterChips, namespaceChip, RefreshFailedNotice, ScreenBody, Segments, Sub } from '../workspace/layout'
import { sameSelectedResource } from '../../utils/drawer-trail'

type State = 'applied' | 'failed' | 'pending'

export interface DeclItem {
  key: string
  kind: 'Database' | 'Publication' | 'Subscription' | 'Managed role' | 'DatabaseRole'
  pgName: string
  /** The PostgreSQL database the item lives in (a Database's own name); unset for roles, which are cluster-wide. */
  database?: string
  state: State
  meta?: string
  error?: string
  source?: string
  resource: SelectedResource
  isField: boolean
}

interface DeclGroup {
  namespace: string
  cluster: string
  row: CNPGFleetRow | null
  items: DeclItem[]
}

/** A database and the Publications and Subscriptions in it; without `database`, the cluster's roles. */
export interface DeclBlock {
  database?: string
  /** The Database declaration; absent when the database is not declared (the bootstrap database, or one that does not exist). */
  head?: DeclItem
  children: DeclItem[]
}

/**
 * Groups a cluster's declarations for display: each database with what lives
 * in it, declared databases first, then roles. A filter keeps a database's
 * row as context for a matching Publication or Subscription in it.
 */
export function cnpgDeclarationBlocks(items: DeclItem[], show: State | null): DeclBlock[] {
  const match = (i: DeclItem) => !show || i.state === show
  const byDatabase = new Map<string, DeclBlock>()
  const roles: DeclItem[] = []
  for (const i of items) {
    if (i.database === undefined) {
      if (match(i)) roles.push(i)
      continue
    }
    let b = byDatabase.get(i.database)
    if (!b) {
      b = { database: i.database, children: [] }
      byDatabase.set(i.database, b)
    }
    if (i.kind === 'Database' && !b.head) b.head = i
    else b.children.push(i)
  }
  const out: DeclBlock[] = []
  const blocks = [...byDatabase.values()].sort((a, b) => (a.head ? 0 : 1) - (b.head ? 0 : 1))
  for (const b of blocks) {
    const children = b.children.filter(match)
    if ((b.head && match(b.head)) || children.length > 0) out.push({ ...b, children })
  }
  if (roles.length > 0) out.push({ children: roles })
  return out
}

function gitopsSource(managedBy: CNPGWorkspaceResponse['managedBy'], obj: any): string | undefined {
  return managedByLabel(cnpgManagedBy({ managedBy }, obj))
}

const STATE_BADGE: Record<State, { severity: 'success' | 'warning' | 'neutral'; text: string }> = {
  applied: { severity: 'success', text: 'Applied' },
  failed: { severity: 'warning', text: 'Not applied' },
  pending: { severity: 'neutral', text: 'Pending' },
}

function roleState(cluster: any, role: string): { state: State; error?: string } {
  const status = cluster?.status?.managedRolesStatus
  const errs = status?.cannotReconcile?.[role]
  if (Array.isArray(errs) && errs.length > 0) return { state: 'failed', error: errs.join('; ') }
  const by = status?.byStatus ?? {}
  if ((by.reconciled ?? []).includes(role)) return { state: 'applied' }
  return { state: 'pending' }
}

export function CNPGDeclarations({ data, fleet, namespaces, searchParams, onSetParams, onInspect, inspected, onClearNamespaces }: CNPGScreenProps) {
  const clusterFilter = searchParams.get('cluster')
  const show = (searchParams.get('show') as 'failed' | 'pending' | null) ?? null

  const scopedGroups = useMemo(() => {
    const byCluster = new Map<string, DeclGroup>()
    const group = (ns: string, cluster: string) => {
      const k = `${ns}/${cluster}`
      let g = byCluster.get(k)
      if (!g) {
        g = { namespace: ns, cluster, row: fleet.rows.find((r) => r.namespace === ns && r.name === cluster) ?? null, items: [] }
        byCluster.set(k, g)
      }
      return g
    }
    const valid = (o: any) => isApiGroup(o?.apiVersion, 'postgresql.cnpg.io')
    const dbs = (data.objects.databases ?? []).filter(valid)
    const pubs = (data.objects.publications ?? []).filter(valid)
    const subs = (data.objects.subscriptions ?? []).filter(valid)
    const placed = new Set<any>()
    for (const d of dbs) {
      const ns = d.metadata?.namespace ?? ''
      const cluster = d.spec?.cluster?.name ?? '(no cluster)'
      const g = group(ns, cluster)
      const st = cnpgRoleState(d)
      g.items.push({
        key: `db/${ns}/${d.metadata?.name}`,
        kind: 'Database',
        pgName: d.spec?.name ?? d.metadata?.name,
        database: d.spec?.name ?? d.metadata?.name,
        state: st,
        meta: d.spec?.owner ? `owner ${d.spec.owner}` : undefined,
        error: st === 'failed' ? d.status?.message : undefined,
        source: gitopsSource(data.managedBy, d),
        resource: cnpgResource('databases', ns, d.metadata?.name),
        isField: false,
      })
      for (const [list, kind] of [[pubs, 'Publication'], [subs, 'Subscription']] as const) {
        for (const p of list) {
          if (p.metadata?.namespace !== ns || p.spec?.cluster?.name !== d.spec?.cluster?.name || p.spec?.dbname !== d.spec?.name) continue
          placed.add(p)
          const pst = cnpgRoleState(p)
          g.items.push({
            key: `${kind}/${ns}/${p.metadata?.name}`,
            kind,
            pgName: p.spec?.name ?? p.metadata?.name,
            database: d.spec?.name ?? d.metadata?.name,
            state: pst,
            meta: logicalMeta(kind, p),
            error: pst === 'failed' ? p.status?.message : undefined,
            source: gitopsSource(data.managedBy, p),
            resource: cnpgResource(kind === 'Publication' ? 'publications' : 'subscriptions', ns, p.metadata?.name),
            isField: false,
          })
        }
      }
    }
    for (const [list, kind] of [[pubs, 'Publication'], [subs, 'Subscription']] as const) {
      for (const p of list) {
        if (placed.has(p)) continue
        const ns = p.metadata?.namespace ?? ''
        const g = group(ns, p.spec?.cluster?.name ?? '(no cluster)')
        const pst = cnpgRoleState(p)
        g.items.push({
          key: `${kind}/${ns}/${p.metadata?.name}`,
          kind,
          pgName: p.spec?.name ?? p.metadata?.name,
          database: p.spec?.dbname ?? '(no database named)',
          state: pst,
          meta: logicalMeta(kind, p),
          error: pst === 'failed' ? p.status?.message : undefined,
          source: gitopsSource(data.managedBy, p),
          resource: cnpgResource(kind === 'Publication' ? 'publications' : 'subscriptions', ns, p.metadata?.name),
          isField: false,
        })
      }
    }
    for (const r of (data.objects.databaseRoles ?? []).filter(valid)) {
      const ns = r.metadata?.namespace ?? ''
      const clusterName = r.spec?.cluster?.name ?? '(no cluster)'
      const g = group(ns, clusterName)
      const f = cnpgDatabaseRoleFacts(r, g.row?.cluster ?? null)
      g.items.push({
        key: `databaserole/${ns}/${r.metadata?.name}`,
        kind: 'DatabaseRole',
        pgName: f.pgName,
        state: f.state,
        meta: cnpgDatabaseRoleMeta(f),
        error: f.state === 'failed' ? f.message : undefined,
        source: gitopsSource(data.managedBy, r),
        resource: cnpgResource('databaseroles', ns, r.metadata?.name),
        isField: false,
      })
    }
    for (const row of fleet.rows) {
      const roles: any[] = Array.isArray(row.cluster?.spec?.managed?.roles) ? row.cluster.spec.managed.roles : []
      if (roles.length === 0) continue
      const g = group(row.namespace, row.name)
      for (const r of roles) {
        if (!r?.name) continue
        const rs = roleState(row.cluster, r.name)
        g.items.push({
          key: `role/${row.namespace}/${row.name}/${r.name}`,
          kind: 'Managed role',
          pgName: r.name,
          state: rs.state,
          meta: r.ensure === 'absent' ? 'ensure absent' : 'spec.managed.roles',
          error: rs.error,
          source: `Cluster ${row.name} spec`,
          resource: clusterResource(row.namespace, row.name),
          isField: true,
        })
      }
    }
    return [...byCluster.values()]
      .filter((g) => !clusterFilter || `${g.namespace}/${g.cluster}` === clusterFilter)
  }, [data.objects.databases, data.objects.publications, data.objects.subscriptions, data.objects.databaseRoles, data.managedBy, fleet.rows, clusterFilter])

  const groups = useMemo(() =>
    scopedGroups
      .map((g) => ({ ...g, matched: show ? g.items.filter((i) => i.state === show) : g.items, blocks: cnpgDeclarationBlocks(g.items, show) }))
      .filter((g) => g.matched.length > 0)
      .sort((a, b) => {
        const fa = a.matched.some((i) => i.state === 'failed') ? 0 : 1
        const fb = b.matched.some((i) => i.state === 'failed') ? 0 : 1
        return fa - fb || a.namespace.localeCompare(b.namespace) || a.cluster.localeCompare(b.cluster)
      }),
    [scopedGroups, show],
  )

  const totals = useMemo(() => {
    let failed = 0
    let pending = 0
    for (const g of scopedGroups) {
      failed += g.items.filter((i) => i.state === 'failed').length
      pending += g.items.filter((i) => i.state === 'pending').length
    }
    return { failed, pending }
  }, [scopedGroups])
  const logicalPaths = useMemo(() => {
    const valid = (o: any) => isApiGroup(o?.apiVersion, 'postgresql.cnpg.io')
    const paths = cnpgLogicalPaths(
      (data.objects.subscriptions ?? []).filter(valid),
      data.objects.clusters ?? [],
      (data.objects.publications ?? []).filter(valid),
      data.objects.poolers ?? [],
      (ns) => relationUnavailable(data, 'publications', ns, 'Publications'),
    )
    if (!clusterFilter) return paths
    return paths.filter(
      (p) =>
        `${p.subscription.namespace}/${p.subscription.cluster}` === clusterFilter ||
        (p.publisher.kind === 'cluster' && `${p.publisher.namespace}/${p.publisher.name}` === clusterFilter),
    )
  }, [data, clusterFilter])

  const declCoverage = worstCoverage(data.coverage.databases, data.coverage.publications, data.coverage.subscriptions, data.coverage.databaseRoles)

  const chips = [
    ...(clusterFilter ? [{ label: `Cluster: ${clusterFilter}`, onClear: () => onSetParams({ cluster: null }) }] : []),
    ...namespaceChip(namespaces, onClearNamespaces),
  ]

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <CNPGWorkspaceHeader
        title="Declarations"
        subtitle="Databases, roles, publications and subscriptions, by PostgreSQL cluster. Declared is not the same as reconciled."
      />
      <ScreenBody>
        <CoverageNotice fleet={fleet} data={data} kinds={['clusters', 'databases', 'databaseRoles', 'publications', 'subscriptions', 'poolers']} />
        <div className="flex flex-wrap items-center gap-2">
          <Segments
            label="Declarations"
            value={show ?? 'all'}
            onChange={(id) => onSetParams({ show: id === 'all' ? null : id })}
            options={[
              { id: 'all', label: 'All declarations' },
              { id: 'failed', label: 'Not applied', count: totals.failed },
              { id: 'pending', label: 'Pending', count: totals.pending },
            ]}
          />
        </div>
        <FilterChips chips={chips} />

        {groups.length === 0 ? (
          <div className="rounded-xl border border-theme-border bg-theme-surface px-4 py-5 text-sm text-theme-text-tertiary">
            {show === 'failed'
              ? 'No declaration in this scope is reported as not applied.'
              : show === 'pending'
                ? 'No declaration in this scope is waiting for the operator.'
                : coverageEmpty(declCoverage, 'declarations')}
            {show && declCoverage?.state !== 'full' ? ' Some declarations are not readable with your access.' : ''}
          </div>
        ) : (
          groups.map((g) => {
            const failed = g.matched.filter((i) => i.state === 'failed').length
            const noSources = g.items.every((i) => !i.source)
            const hasDatabases = g.blocks.some((b) => b.database !== undefined)
            return (
              <section key={`${g.namespace}/${g.cluster}`} className="overflow-hidden rounded-xl border border-theme-border bg-theme-surface shadow-theme-sm">
                <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1 border-b border-theme-border px-4 py-2.5">
                  {g.row ? (
                    <button type="button" onClick={() => onInspect(clusterResource(g.namespace, g.cluster))} className="text-sm font-semibold text-accent-text hover:underline">
                      {g.cluster}
                    </button>
                  ) : (
                    <span className="text-sm font-semibold text-theme-text-primary">
                      {g.cluster}
                    </span>
                  )}
                  <span className="text-xs text-theme-text-tertiary">{g.namespace}</span>
                  <span className={clsx('text-xs', failed > 0 ? toneTextClass('degraded') : 'text-theme-text-tertiary')}>
                    {g.matched.length} {g.matched.length === 1 ? 'declaration' : 'declarations'}
                    {failed > 0 ? ` · ${failed} not reconciled` : ''}
                    {!g.row ? ' · target cluster not visible' : ''}
                  </span>
                  {noSources && <span className="text-xs text-theme-text-tertiary">no GitOps source recorded on any of them</span>}
                </div>
                <div className="table-divide-subtle">
                  {g.blocks.map((b) => (
                    <div key={b.database === undefined ? 'roles' : `db/${b.database}`}>
                      {b.database === undefined ? (
                        hasDatabases && <div className="px-4 pb-1 pt-2.5 text-[11px] font-medium uppercase tracking-wide text-theme-text-tertiary">Roles</div>
                      ) : b.head ? (
                        <DeclarationRow
                          item={b.head}
                          sourceStated={noSources}
                          active={sameSelectedResource(inspected, b.head.resource)}
                          onInspect={() => onInspect(b.head!.resource)}
                        />
                      ) : (
                        <div className="flex flex-wrap items-center gap-2 px-4 pb-1 pt-2.5 text-sm">
                          <span className="text-xs text-theme-text-tertiary">Database</span>
                          <span className="font-mono text-theme-text-secondary">{b.database}</span>
                          <span className="text-xs text-theme-text-tertiary">no Database declaration</span>
                        </div>
                      )}
                      {b.children.map((i) => (
                        <DeclarationRow
                          key={i.key}
                          item={i}
                          nested={b.database !== undefined}
                          sourceStated={noSources}
                          active={!i.isField && sameSelectedResource(inspected, i.resource)}
                          onInspect={() => onInspect(i.resource)}
                        />
                      ))}
                    </div>
                  ))}
                </div>
              </section>
            )
          })
        )}

        {!show && logicalPaths.length > 0 && (
          <section className="overflow-hidden rounded-xl border border-theme-border bg-theme-surface shadow-theme-sm">
            <div className="flex flex-wrap items-baseline gap-x-3 border-b border-theme-border px-4 py-2.5">
              <span className="text-sm font-semibold text-theme-text-primary">Logical replication</span>
              <span className="text-xs text-theme-text-tertiary">
                Each Subscription to its publication and the slot the publisher keeps for it · Subscriptions created in SQL are not listed
              </span>
            </div>
            <div className="table-divide-subtle">
              {logicalPaths.map((p) => (
                <LogicalPathRow key={`${p.subscription.namespace}/${p.subscription.name}`} path={p} onInspect={onInspect} />
              ))}
            </div>
          </section>
        )}
      </ScreenBody>
    </div>
  )
}

function LogicalPathRow({ path, onInspect }: { path: CNPGLogicalPath; onInspect: CNPGScreenProps['onInspect'] }) {
  const { observed, query } = useCNPGPublisherSlots(path.publisher)
  return (
    <div className="px-4 py-3">
      <CNPGLogicalPathView
        path={path}
        slot={cnpgLogicalSlotFact(path, observed)}
        notice={<RefreshFailedNotice queries={[query]} />}
        onNavigate={(ref) => onInspect(refToSelectedResource(ref))}
        compact
      />
    </div>
  )
}

function logicalMeta(kind: 'Publication' | 'Subscription', p: { spec?: { target?: { allTables?: boolean }; publicationName?: string; externalClusterName?: string } }): string {
  if (kind === 'Publication') return p.spec?.target?.allTables ? 'all tables' : 'selected objects'
  return `from ${p.spec?.publicationName ?? 'an unnamed publication'} on ${p.spec?.externalClusterName ?? 'an unnamed external cluster'}`
}

// `sourceStated`: the cluster header already says none of its rows records a
// GitOps source, so the row has no source column. `nested`: it lives in the
// database above it; a guide line joins it to that row.
function DeclarationRow({
  item,
  active,
  onInspect,
  sourceStated,
  nested,
}: {
  item: DeclItem
  active: boolean
  onInspect: () => void
  sourceStated?: boolean
  nested?: boolean
}) {
  const badge = STATE_BADGE[item.state]
  let detail: ReactNode = null
  if (item.error) {
    detail = (
      <div className={clsx('mt-1 flex items-start gap-1.5 text-xs', toneTextClass('degraded'))}>
        <AlertTriangle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
        <span className="break-words">Controller error: {item.error}</span>
      </div>
    )
  }
  return (
    <div
      role="button"
      tabIndex={0}
      onClick={onInspect}
      onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onInspect() } }}
      className={clsx(
        'relative grid cursor-pointer gap-x-4 px-4 py-2.5 text-sm transition-colors hover:bg-theme-hover/50',
        sourceStated ? 'grid-cols-[minmax(0,1fr)_auto]' : 'grid-cols-[minmax(0,1.6fr)_minmax(0,0.8fr)_minmax(0,1fr)]',
        active && 'selection',
      )}
    >
      {nested && <span aria-hidden className="absolute bottom-0 left-[1.6rem] top-0 w-px bg-theme-border" />}
      <div className={clsx('min-w-0', nested && 'pl-6')}>
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-mono font-medium text-theme-text-primary">{item.pgName}</span>
          <span className="text-xs text-theme-text-tertiary">· {item.kind}{!item.isField && ` ${item.resource.name}`}</span>
          {item.meta && <span className="text-xs text-theme-text-tertiary">{item.meta}</span>}
        </div>
        {detail}
      </div>
      <div className={clsx(sourceStated && 'text-right')}>
        <Badge severity={badge.severity} size="sm">{badge.text}</Badge>
        <div className="mt-1 text-xs text-accent-text">Inspect →</div>
        {item.state === 'pending' && <Sub>awaiting the operator for the current spec</Sub>}
        {item.isField && <Sub>field of the Cluster</Sub>}
      </div>
      {!sourceStated && (
        <div className="min-w-0 text-xs text-theme-text-secondary break-words">
          {item.source ?? <span className="text-theme-text-tertiary">GitOps source not recorded</span>}
        </div>
      )}
    </div>
  )
}
