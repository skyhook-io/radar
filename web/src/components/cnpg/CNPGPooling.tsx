import { useMemo } from 'react'
import {
  Badge,
  getCNPGPoolerMode,
  getCNPGPoolerStatus,
  getCNPGPoolerType,
  isApiGroup,
  type HealthLevel,
} from '@skyhook-io/k8s-ui'
import {
  CNPGWorkspaceHeader,
  CoverageNotice,
  FilterChips,
  Mono,
  ScreenBody,
  SectionTable,
  Sub,
  cnpgResource,
  coverageEmpty,
  namespaceChip,
  type CNPGScreenProps,
} from './shared'

const SEVERITY: Record<HealthLevel, 'success' | 'warning' | 'alert' | 'error' | 'neutral'> = {
  healthy: 'success',
  degraded: 'warning',
  alert: 'alert',
  unhealthy: 'error',
  unknown: 'neutral',
  neutral: 'neutral',
}

export function CNPGPooling({ data, fleet, namespaces, searchParams, onSetParams, onInspect, inspected, onClearNamespaces }: CNPGScreenProps) {
  const clusterFilter = searchParams.get('cluster')
  const poolers = useMemo(
    () =>
      (data.objects.poolers ?? [])
        .filter((p) => isApiGroup(p.apiVersion, 'postgresql.cnpg.io'))
        .filter((p) => !clusterFilter || `${p.metadata?.namespace}/${p.spec?.cluster?.name}` === clusterFilter),
    [data.objects.poolers, clusterFilter],
  )
  const chips = [
    ...(clusterFilter ? [{ label: `Cluster: ${clusterFilter}`, onClear: () => onSetParams({ cluster: null }) }] : []),
    ...namespaceChip(namespaces, onClearNamespaces),
  ]

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <CNPGWorkspaceHeader
        title="Pooling"
        subtitle="PgBouncer Poolers, the clusters they front, and their readiness."
      />
      <ScreenBody>
        <CoverageNotice fleet={fleet} data={data} />
        <FilterChips chips={chips} />
        <SectionTable
          title="Poolers"
          columns={[
            { header: 'Pooler', width: '22%', cell: (p: any) => <>{p.metadata?.name}<Sub>{p.metadata?.namespace}</Sub></> },
            {
              header: 'Target cluster',
              width: '18%',
              cell: (p) => {
                const name = p.spec?.cluster?.name
                const visible = fleet.rows.some((r) => r.namespace === p.metadata?.namespace && r.name === name)
                return (
                  <>
                    {name ?? '—'}
                    {name && !visible && <Sub>not visible in this scope</Sub>}
                  </>
                )
              },
            },
            { header: 'Type', width: '8%', cell: (p) => <Mono>{getCNPGPoolerType(p)}</Mono> },
            { header: 'Mode', width: '12%', cell: (p) => getCNPGPoolerMode(p) },
            {
              header: 'Instances',
              width: '10%',
              cell: (p) => (
                <span className="font-mono">
                  {typeof p.status?.instances === 'number' ? p.status.instances : '–'}/{typeof p.spec?.instances === 'number' ? p.spec.instances : '–'}
                </span>
              ),
            },
            {
              header: 'Status',
              width: '14%',
              cell: (p) => {
                const st = getCNPGPoolerStatus(p)
                return <Badge severity={SEVERITY[st.level]} size="sm">{st.text}</Badge>
              },
            },
            {
              header: 'Connection pressure',
              width: '16%',
              cell: () => (
                <>
                  <span className="text-theme-text-tertiary">Not measured</span>
                  <Sub>Needs PgBouncer metrics</Sub>
                </>
              ),
            },
          ]}
          rows={poolers}
          rowKey={(p) => `${p.metadata?.namespace}/${p.metadata?.name}`}
          rowResource={(p) => cnpgResource('poolers', p.metadata?.namespace, p.metadata?.name)}
          onInspect={onInspect}
          inspected={inspected}
          minWidth={880}
          empty={coverageEmpty(data.coverage.poolers, 'Poolers')}
          footer="Instances are the Pooler’s own ready count. Client waits and server-pool saturation come from PgBouncer metrics, which Radar does not read yet."
        />
      </ScreenBody>
    </div>
  )
}
