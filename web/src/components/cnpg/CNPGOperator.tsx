import { useMemo } from 'react'
import { Badge, getCNPGImageCatalogEntries, isApiGroup, PaneLoader, Tooltip } from '@skyhook-io/k8s-ui'
import { useCNPGOperator, type CNPGOperatorComponent, type CNPGOperatorConfig } from '../../api/cnpg'
import { Notice } from '../capacity/shared'
import { CNPGOperatorDiagnosisSection } from './CNPGOperatorDiagnosis'
import {
  CNPGWorkspaceHeader,
  CoverageNotice,
  coverageEmpty,
  worstCoverage,
  Mono,
  ScreenBody,
  SectionTable,
  Sub,
  cnpgResource,
  type CNPGScreenProps,
} from './shared'

interface CatalogRow {
  key: string
  kind: 'ImageCatalog' | 'ClusterImageCatalog'
  namespace: string
  name: string
  images: { major: number; image: string }[]
  users: { namespace: string; name: string; major?: number }[]
}

function readiness(c: CNPGOperatorComponent) {
  if (c.readyReplicas === null || c.replicas === null) return <span className="text-theme-text-tertiary">Unknown</span>
  if (c.replicas === 0) return <Badge severity="warning" size="sm">Scaled to 0</Badge>
  const ok = c.readyReplicas >= c.replicas
  return <Badge severity={ok ? 'success' : c.readyReplicas === 0 ? 'error' : 'warning'} size="sm">{c.readyReplicas}/{c.replicas} ready</Badge>
}

export function CNPGOperator({ data, fleet, onInspect, inspected }: CNPGScreenProps) {
  const operator = useCNPGOperator()

  const catalogs = useMemo<CatalogRow[]>(() => {
    const out: CatalogRow[] = []
    const clusters = fleet.rows
    for (const [key, kind] of [['imageCatalogs', 'ImageCatalog'], ['clusterImageCatalogs', 'ClusterImageCatalog']] as const) {
      for (const cat of data.objects[key] ?? []) {
        if (!isApiGroup(cat.apiVersion, 'postgresql.cnpg.io')) continue
        const ns = cat.metadata?.namespace ?? ''
        const name = cat.metadata?.name ?? ''
        const users = clusters
          .filter((r) => {
            const ref = r.cluster?.spec?.imageCatalogRef
            if (!ref || ref.name !== name) return false
            const refKind = ref.kind || 'ImageCatalog'
            if (refKind !== kind) return false
            return kind === 'ClusterImageCatalog' || r.namespace === ns
          })
          .map((r) => ({ namespace: r.namespace, name: r.name, major: r.cluster?.spec?.imageCatalogRef?.major }))
        out.push({ key: `${kind}/${ns}/${name}`, kind, namespace: ns, name, images: getCNPGImageCatalogEntries(cat), users })
      }
    }
    return out
  }, [data.objects, fleet.rows])

  const direct = fleet.rows.filter((r) => !r.cluster?.spec?.imageCatalogRef)
  const op = operator.data
  const coverageGaps = op
    ? (['deployments', 'services'] as const).filter((k) => op.coverage[k]?.state !== 'full')
    : []

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <CNPGWorkspaceHeader
        title="Operator"
        subtitle="Operator and plugin workloads, whether the operator is leading, watching and reachable, image catalogs and operator configuration."
      />
      <ScreenBody>
        <CoverageNotice fleet={fleet} data={data} />
        {operator.isLoading && !op ? (
          <PaneLoader label="Loading operator…" className="h-32" />
        ) : !op ? (
          <Notice>Operator details could not be loaded{operator.error instanceof Error ? `: ${operator.error.message}` : '.'}</Notice>
        ) : (
          <>
            {coverageGaps.length > 0 && (
              <Notice>
                Some workloads are not readable ({coverageGaps.map((k) => `${k}: ${op.coverage[k].state}`).join(', ')}), so an operator or plugin running in those namespaces may be missing below.
              </Notice>
            )}
            <SectionTable
              title="Operator and plugins"
              columns={[
                {
                  header: 'Component',
                  width: '30%',
                  cell: (c: CNPGOperatorComponent) => (
                    <>
                      <div className="font-medium">{c.role === 'operator' ? 'CloudNativePG operator' : c.pluginName ?? 'Plugin'}</div>
                      <Sub>{c.role === 'operator' ? 'controller manager' : 'CNPG-I plugin'}</Sub>
                    </>
                  ),
                },
                {
                  header: 'Version',
                  width: '14%',
                  cell: (c) => (
                    <Tooltip content={c.image || 'No image recorded'}>
                      {c.version ? <Mono>{c.version}</Mono> : <span className="text-theme-text-tertiary">Unknown</span>}
                    </Tooltip>
                  ),
                },
                { header: 'Ready', width: '14%', cell: readiness },
                {
                  header: 'Workload',
                  width: '42%',
                  cell: (c) =>
                    c.deployment ? (
                      <>Deployment <Mono>{c.deployment}</Mono><Sub>{c.namespace}</Sub></>
                    ) : (
                      <>
                        <span className="text-theme-text-tertiary">No Deployment matches its Service</span>
                        <Sub>{c.namespace}</Sub>
                      </>
                    ),
                },
              ]}
              rows={op.components}
              rowKey={(c) => `${c.namespace}/${c.deployment}/${c.pluginName ?? c.role}`}
              rowResource={(c) => (c.deployment ? { kind: 'deployments', group: 'apps', namespace: c.namespace, name: c.deployment } : null)}
              onInspect={onInspect}
              inspected={inspected}
              empty="No operator or plugin Deployments found in the namespaces you can read."
            />
            {op.diagnosis && <CNPGOperatorDiagnosisSection diagnosis={op.diagnosis} fleet={fleet} />}
          </>
        )}

        <SectionTable
          title="Image catalogs"
          columns={[
            { header: 'Catalog', width: '22%', cell: (c: CatalogRow) => <>{c.name}<Sub>{c.kind}</Sub></> },
            { header: 'Scope', width: '14%', cell: (c) => (c.kind === 'ClusterImageCatalog' ? 'Cluster-wide' : `Namespace ${c.namespace}`) },
            {
              header: 'Images',
              width: '40%',
              cell: (c) =>
                c.images.length === 0 ? (
                  <span className="text-theme-text-tertiary">None</span>
                ) : (
                  <div className="space-y-0.5">
                    {c.images.map((i) => (
                      <div key={i.major} className="flex gap-2">
                        <span className="w-6 shrink-0 font-mono text-theme-text-tertiary">{i.major}</span>
                        <Mono>{i.image}</Mono>
                      </div>
                    ))}
                  </div>
                ),
            },
            {
              header: 'Used by',
              width: '24%',
              cell: (c) =>
                c.users.length === 0 ? (
                  <span className="text-theme-text-tertiary">No visible cluster</span>
                ) : (
                  c.users.map((u) => `${u.name}${u.major !== undefined ? ` (${u.major})` : ''}`).join(', ')
                ),
            },
          ]}
          rows={catalogs}
          rowKey={(c) => c.key}
          rowResource={(c) => cnpgResource(c.kind === 'ImageCatalog' ? 'imagecatalogs' : 'clusterimagecatalogs', c.namespace, c.name)}
          onInspect={onInspect}
          inspected={inspected}
          empty={coverageEmpty(worstCoverage(data.coverage.imageCatalogs, data.coverage.clusterImageCatalogs), 'image catalogs')}
          footer={
            <>
              Used-by lists only clusters you can see; the catalog detail asks the server for every user.
              {direct.length > 0 && <> Not using a catalog (direct <span className="font-mono">imageName</span>): {direct.map((r) => r.name).join(', ')}.</>}
            </>
          }
        />

        {op && op.config.length > 0 && (
          <section>
            <h2 className="mb-2 text-sm font-semibold text-theme-text-primary">Operator configuration</h2>
            <div className="space-y-3">
              {op.config.map((c) => (
                <ConfigBlock key={`${c.kind}/${c.namespace}/${c.name}`} config={c} onInspect={onInspect} />
              ))}
            </div>
          </section>
        )}
      </ScreenBody>
    </div>
  )
}

function ConfigBlock({ config, onInspect }: { config: CNPGOperatorConfig; onInspect: CNPGScreenProps['onInspect'] }) {
  const open = () => onInspect({ kind: config.kind === 'ConfigMap' ? 'configmaps' : 'secrets', group: '', namespace: config.namespace, name: config.name })
  const title = (
    <div className="flex flex-wrap items-baseline gap-x-2 border-b border-theme-border px-4 py-2.5">
      <span className="text-xs text-theme-text-tertiary">{config.kind}</span>
      <button type="button" onClick={open} className="font-mono text-sm text-accent-text hover:underline">{config.name}</button>
      <span className="text-xs text-theme-text-tertiary">
        {config.namespace} · {config.purpose === 'monitoring' ? 'monitoring queries' : 'operator settings'}
      </span>
    </div>
  )
  let body
  if (config.kind === 'Secret') {
    body = <div className="px-4 py-2.5 text-sm text-theme-text-secondary">Referenced by the operator. Secret contents are not shown here.</div>
  } else if (config.exists === false) {
    body = <div className="px-4 py-2.5 text-sm text-theme-text-secondary">Referenced but does not exist; the operator runs with its defaults.</div>
  } else if (!config.readable) {
    body = <div className="px-4 py-2.5 text-sm text-theme-text-tertiary">{config.reason ?? 'Not readable with your access.'}</div>
  } else {
    const entries = Object.entries(config.data ?? {})
    body =
      entries.length === 0 ? (
        <div className="px-4 py-2.5 text-sm text-theme-text-tertiary">No keys set.</div>
      ) : (
        <dl className="grid grid-cols-[minmax(0,14rem)_minmax(0,1fr)] gap-x-4 gap-y-1.5 px-4 py-2.5 text-sm">
          {entries.map(([k, v]) => (
            <div key={k} className="contents">
              <dt className="min-w-0 font-mono text-theme-text-secondary"><Tooltip content={k} wrapperClassName="block min-w-0"><span className="block truncate">{k}</span></Tooltip></dt>
              <dd className="break-all font-mono text-theme-text-primary">{v.length > 400 ? `${v.slice(0, 400)}…` : v}</dd>
            </div>
          ))}
        </dl>
      )
  }
  return (
    <div className="overflow-hidden rounded-xl border border-theme-border bg-theme-surface shadow-theme-sm">
      {title}
      {body}
    </div>
  )
}
