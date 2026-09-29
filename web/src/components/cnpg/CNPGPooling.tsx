import { useMemo } from 'react'
import {
  Badge,
  toneTextClass,
  getCNPGPoolerMode,
  getCNPGPoolerStatus,
  getCNPGPoolerType,
  isApiGroup,
  isCNPGPoolerPaused,
  poolerReadiness,
  type HealthLevel,
} from '@skyhook-io/k8s-ui'
import { useCNPGPoolerRuntime } from '../../api/cnpg'
import { useCNPGPoolerCapabilities } from '../../api/cnpg-sessions'
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
              header: 'Readiness',
              width: '24%',
              cell: (p) => <PoolerReadiness pooler={p} />,
            },
            {
              header: 'Connection pressure',
              width: '16%',
              cell: (p) => <PoolerPressure namespace={p.metadata?.namespace} name={p.metadata?.name} />,
            },
          ]}
          rows={poolers}
          rowKey={(p) => `${p.metadata?.namespace}/${p.metadata?.name}`}
          rowResource={(p) => cnpgResource('poolers', p.metadata?.namespace, p.metadata?.name)}
          onInspect={onInspect}
          inspected={inspected}
          minWidth={880}
          empty={coverageEmpty(data.coverage.poolers, 'Poolers')}
          footer="Readiness is the Pooler’s Deployment (the Pooler itself only counts scheduled Pods). Pressure is read live from each PgBouncer's metrics through the Kubernetes API proxy."
        />
      </ScreenBody>
    </div>
  )
}

function PoolerReadiness({ pooler }: { pooler: any }) {
  const namespace = pooler.metadata?.namespace
  const name = pooler.metadata?.name
  const caps = useCNPGPoolerCapabilities(namespace, name)
  const paused = isCNPGPoolerPaused(pooler)
  if (!caps.data) {
    const st = getCNPGPoolerStatus(pooler)
    return (
      <>
        <Badge severity={SEVERITY[st.level]} size="sm">{st.text}</Badge>
        <Sub>{caps.isLoading ? 'reading Deployment…' : 'Pooler status only; its Deployment was not read'}</Sub>
      </>
    )
  }
  const r = poolerReadiness(caps.data.facts.deployment)
  return (
    <>
      <span className="inline-flex flex-wrap items-center gap-1">
        <Badge severity={SEVERITY[r.level]} size="sm">{r.text}</Badge>
        {paused && <Badge severity="warning" size="sm">Paused (requested)</Badge>}
      </span>
      <Sub>{r.detail}</Sub>
    </>
  )
}

function PoolerPressure({ namespace, name }: { namespace: string; name: string }) {
  const q = useCNPGPoolerRuntime(namespace, name)
  if (!q.data) return <span className="text-theme-text-tertiary">{q.isLoading ? 'Reading…' : 'Unavailable'}</span>
  if (q.data.permission.proxy === 'denied') {
    return (
      <>
        <span className="text-theme-text-tertiary">No access</span>
        <Sub>needs get pods/proxy</Sub>
      </>
    )
  }
  const ok = q.data.pods.filter((p) => p.state === 'ok' || p.state === 'partial')
  if (ok.length === 0) {
    return (
      <>
        <span className="text-theme-text-tertiary">Not measured</span>
        <Sub>{q.data.pods[0]?.error ?? q.data.pods[0]?.reason ?? 'no PgBouncer answered'}</Sub>
      </>
    )
  }
  const pools = ok.flatMap((p) => p.pools ?? [])
  if (pools.length === 0 && ok.every((p) => p.state === 'ok')) {
    return (
      <>
        <span>Idle</span>
        <Sub>
          no client pools open · {ok.length}/{q.data.pods.length} pods reporting
        </Sub>
      </>
    )
  }
  // Totals are exact only when every pod answered in full and every pool
  // reported the field; otherwise they are lower bounds.
  const allPods = ok.length === q.data.pods.length && ok.every((p) => p.state === 'ok')
  const total = (field: 'clWaiting' | 'svActive') => {
    const reported = pools.filter((x) => x[field] !== undefined)
    if (reported.length === 0) return { sum: 0, text: 'not reported' }
    const sum = reported.reduce((acc, x) => acc + (x[field] as number), 0)
    return { sum, text: allPods && reported.length === pools.length ? `${sum}` : `≥ ${sum}` }
  }
  const waiting = total('clWaiting')
  const waits = pools.map((x) => x.maxwaitSeconds).filter((v): v is number => v !== undefined)
  const maxwait = waits.length > 0 ? Math.max(...waits) : undefined
  return (
    <>
      <span className={waiting.sum > 0 ? toneTextClass('degraded') : undefined}>
        {waiting.text} waiting · {total('svActive').text} server in use
      </span>
      <Sub>
        {maxwait !== undefined && maxwait > 0 ? `longest wait ${maxwait.toFixed(1)} s · ` : ''}
        {ok.length}/{q.data.pods.length} pods reporting
      </Sub>
    </>
  )
}
