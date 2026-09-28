import { useMemo, type ReactNode } from 'react'
import { clsx } from 'clsx'
import { AlertTriangle } from 'lucide-react'
import { Badge, isApiGroup, toneTextClass, type CNPGFleetRow } from '@skyhook-io/k8s-ui'
import type { SelectedResource } from '../../types'
import {
  CNPGWorkspaceHeader,
  CoverageNotice,
  FilterChips,
  ScreenBody,
  Segments,
  Sub,
  clusterResource,
  cnpgResource,
  namespaceChip,
  type CNPGScreenProps,
} from './shared'
import { sameResource } from './routes'

type State = 'applied' | 'failed' | 'pending'

interface DeclItem {
  key: string
  kind: 'Database' | 'Publication' | 'Subscription' | 'Managed role'
  pgName: string
  indent: boolean
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

function stateOf(obj: any): State {
  const applied = obj?.status?.applied
  if (applied === true) return 'applied'
  if (applied === false) return 'failed'
  return 'pending'
}

function gitopsSource(obj: any): string | undefined {
  const labels = obj?.metadata?.labels ?? {}
  if (labels['argocd.argoproj.io/instance']) return `Argo CD ${labels['argocd.argoproj.io/instance']}`
  const flux = labels['kustomize.toolkit.fluxcd.io/name'] || labels['helm.toolkit.fluxcd.io/name']
  if (flux) return `Flux ${flux}`
  return undefined
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
  const onlyFailed = searchParams.get('show') === 'failed'

  const groups = useMemo<DeclGroup[]>(() => {
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
      const st = stateOf(d)
      g.items.push({
        key: `db/${ns}/${d.metadata?.name}`,
        kind: 'Database',
        pgName: d.spec?.name ?? d.metadata?.name,
        indent: false,
        state: st,
        meta: d.spec?.owner ? `owner ${d.spec.owner}` : undefined,
        error: st === 'failed' ? d.status?.message : undefined,
        source: gitopsSource(d),
        resource: cnpgResource('databases', ns, d.metadata?.name),
        isField: false,
      })
      for (const [list, kind] of [[pubs, 'Publication'], [subs, 'Subscription']] as const) {
        for (const p of list) {
          if (p.metadata?.namespace !== ns || p.spec?.cluster?.name !== d.spec?.cluster?.name || p.spec?.dbname !== d.spec?.name) continue
          placed.add(p)
          const pst = stateOf(p)
          g.items.push({
            key: `${kind}/${ns}/${p.metadata?.name}`,
            kind,
            pgName: p.spec?.name ?? p.metadata?.name,
            indent: true,
            state: pst,
            meta:
              kind === 'Publication'
                ? p.spec?.target?.allTables ? 'all tables' : 'selected objects'
                : `from ${p.spec?.publicationName ?? '?'} on ${p.spec?.externalClusterName ?? '?'}`,
            error: pst === 'failed' ? p.status?.message : undefined,
            source: gitopsSource(p),
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
        const pst = stateOf(p)
        g.items.push({
          key: `${kind}/${ns}/${p.metadata?.name}`,
          kind,
          pgName: p.spec?.name ?? p.metadata?.name,
          indent: false,
          state: pst,
          meta: p.spec?.dbname ? `database ${p.spec.dbname}` : undefined,
          error: pst === 'failed' ? p.status?.message : undefined,
          source: gitopsSource(p),
          resource: cnpgResource(kind === 'Publication' ? 'publications' : 'subscriptions', ns, p.metadata?.name),
          isField: false,
        })
      }
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
          indent: false,
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
      .map((g) => ({ ...g, items: onlyFailed ? g.items.filter((i) => i.state === 'failed') : g.items }))
      .filter((g) => g.items.length > 0)
      .sort((a, b) => {
        const fa = a.items.some((i) => i.state === 'failed') ? 0 : 1
        const fb = b.items.some((i) => i.state === 'failed') ? 0 : 1
        return fa - fb || a.namespace.localeCompare(b.namespace) || a.cluster.localeCompare(b.cluster)
      })
  }, [data.objects.databases, data.objects.publications, data.objects.subscriptions, fleet.rows, clusterFilter, onlyFailed])

  const failedTotal = useMemo(() => {
    let n = 0
    for (const k of ['databases', 'publications', 'subscriptions'] as const) {
      n += (data.objects[k] ?? []).filter((o) => o?.status?.applied === false).length
    }
    for (const r of fleet.rows) n += Object.keys(r.cluster?.status?.managedRolesStatus?.cannotReconcile ?? {}).length
    return n
  }, [data.objects, fleet.rows])

  const chips = [
    ...(clusterFilter ? [{ label: `Cluster: ${clusterFilter}`, onClear: () => onSetParams({ cluster: null }) }] : []),
    ...namespaceChip(namespaces, onClearNamespaces),
  ]

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <CNPGWorkspaceHeader
        title="Declarations"
        subtitle="Databases, managed roles, publications and subscriptions, by PostgreSQL cluster. Declared is not the same as reconciled."
      />
      <ScreenBody>
        <CoverageNotice fleet={fleet} data={data} />
        <div className="flex flex-wrap items-center gap-2">
          <Segments
            label="Declarations"
            value={onlyFailed ? 'failed' : 'all'}
            onChange={(id) => onSetParams({ show: id === 'failed' ? 'failed' : null })}
            options={[
              { id: 'all', label: 'All declarations' },
              { id: 'failed', label: 'Not reconciled', count: failedTotal },
            ]}
          />
        </div>
        <FilterChips chips={chips} />

        {groups.length === 0 ? (
          <div className="rounded-xl border border-theme-border bg-theme-surface px-4 py-5 text-sm text-theme-text-tertiary">
            {onlyFailed ? 'Every declaration in this scope is reconciled.' : 'No declarations in this scope.'}
          </div>
        ) : (
          groups.map((g) => {
            const failed = g.items.filter((i) => i.state === 'failed').length
            return (
              <section key={`${g.namespace}/${g.cluster}`} className="overflow-hidden rounded-xl border border-theme-border bg-theme-surface shadow-theme-sm">
                <div className="flex flex-wrap items-baseline gap-x-3 gap-y-1 border-b border-theme-border px-4 py-2.5">
                  {g.row ? (
                    <button type="button" onClick={() => onInspect(clusterResource(g.namespace, g.cluster))} className="text-sm font-semibold text-accent-text hover:underline">
                      {g.cluster}
                    </button>
                  ) : (
                    <span className="text-sm font-semibold text-theme-text-primary" title="The target cluster is not visible in this scope">
                      {g.cluster}
                    </span>
                  )}
                  <span className="text-xs text-theme-text-tertiary">{g.namespace}</span>
                  <span className={clsx('text-xs', failed > 0 ? toneTextClass('degraded') : 'text-theme-text-tertiary')}>
                    {g.items.length} {g.items.length === 1 ? 'declaration' : 'declarations'}
                    {failed > 0 ? ` · ${failed} not reconciled` : ''}
                    {!g.row ? ' · target cluster not visible' : ''}
                  </span>
                </div>
                <div className="table-divide-subtle">
                  {g.items.map((i) => (
                    <DeclarationRow
                      key={i.key}
                      item={i}
                      active={!i.isField && sameResource(inspected, i.resource)}
                      onInspect={() => onInspect(i.resource)}
                    />
                  ))}
                </div>
              </section>
            )
          })
        )}
      </ScreenBody>
    </div>
  )
}

function DeclarationRow({ item, active, onInspect }: { item: DeclItem; active: boolean; onInspect: () => void }) {
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
      onKeyDown={(e) => { if (e.key === 'Enter') onInspect() }}
      className={clsx(
        'grid cursor-pointer grid-cols-[minmax(0,1.6fr)_minmax(0,0.8fr)_minmax(0,1fr)] gap-x-4 px-4 py-2.5 text-sm transition-colors hover:bg-theme-hover/50',
        active && 'selection',
      )}
    >
      <div className={clsx('min-w-0', item.indent && 'pl-6')}>
        <div className="flex flex-wrap items-center gap-2">
          <span className="text-xs text-theme-text-tertiary">{item.kind}</span>
          <span className="font-mono font-medium text-theme-text-primary">{item.pgName}</span>
          {item.meta && <span className="text-xs text-theme-text-tertiary">{item.meta}</span>}
        </div>
        {detail}
      </div>
      <div>
        <Badge severity={badge.severity} size="sm">{badge.text}</Badge>
        {item.isField && <Sub>field of the Cluster</Sub>}
      </div>
      <div className="min-w-0 text-xs text-theme-text-secondary break-words">
        {item.source ?? <span className="text-theme-text-tertiary">Applied directly (no GitOps owner label)</span>}
      </div>
    </div>
  )
}
