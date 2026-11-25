import { useMemo, useState } from 'react'
import { useSearchParams } from 'react-router-dom'
import { SearchBox, StatusDot, Tooltip, formatAge, formatBytes, type LonghornVolumeHealth, type LonghornVolumeReading } from '@skyhook-io/k8s-ui'
import { ScreenBody, SectionTable, Segments, Sub, type TableColumn } from '../workspace/layout'
import { coverageEmpty } from '../workspace/coverage'
import { longhornUseText } from './attention'
import { LONGHORN_HEALTH, LonghornCoverageNotice, LonghornHeader, longhornVolumeResource, type LonghornScreenProps } from './shared'

const HEALTHS: LonghornVolumeHealth[] = ['faulted', 'degraded', 'healthy', 'detached', 'unknown']

const HEALTHY_REPLICAS_UNKNOWN: Record<NonNullable<LonghornVolumeReading['healthyReplicasUnknown']>, string> = {
  engineUnread: 'You can’t read Longhorn Engines in this namespace',
  engineAmbiguous: 'More than one Engine claims this volume',
  noEngine: 'Longhorn has no Engine for this volume',
  noModes: 'The Engine reports no replica modes, as when it is stopped',
}

const NOT_REPORTED = <span className="text-theme-text-tertiary">not reported</span>

function ReplicasCell({ v }: { v: LonghornVolumeReading }) {
  const desired = v.numberOfReplicas ?? '?'
  if (v.healthyReplicas !== null) return <span className="font-mono">{v.healthyReplicas} / {desired}</span>
  return (
    <Tooltip content={`Healthy replicas unknown: ${v.healthyReplicasUnknown ? HEALTHY_REPLICAS_UNKNOWN[v.healthyReplicasUnknown] : 'not reported'}`}>
      <span className="font-mono"><span className="text-theme-text-tertiary">?</span> / {desired}</span>
    </Tooltip>
  )
}

function ClaimCell({ v }: { v: LonghornVolumeReading }) {
  if (v.use.claim) {
    return (
      <>
        <Tooltip content={`${v.use.claim.namespace}/${v.use.claim.name}`}><span className="block min-w-0 truncate font-mono text-sm">{v.use.claim.name}</span></Tooltip>
        <Sub>{v.use.claim.namespace}</Sub>
      </>
    )
  }
  const label = v.use.state === 'unknown' ? 'unknown' : v.use.state === 'none' ? 'none' : v.use.claimWithheld ? 'a claim you can’t read' : 'PV not bound'
  return <Tooltip content={longhornUseText(v) ?? label}><span className="text-theme-text-tertiary">{label}</span></Tooltip>
}

const columns: TableColumn<LonghornVolumeReading>[] = [
  {
    header: 'Volume',
    cell: (v) => (
      <Tooltip content={`${v.namespace}/${v.name}`}>
        <span className="block min-w-0 truncate font-mono text-sm">{v.name}</span>
      </Tooltip>
    ),
  },
  {
    header: 'Health',
    width: '150px',
    cell: (v) => (
      <span className="flex items-center gap-1.5">
        <StatusDot tone={LONGHORN_HEALTH[v.health].tone} />
        {LONGHORN_HEALTH[v.health].label}
        {v.health === 'degraded' && v.lastDegradedAt && <span className="text-xs text-theme-text-tertiary">for {formatAge(v.lastDegradedAt)}</span>}
      </span>
    ),
  },
  { header: 'State', width: '100px', cell: (v) => v.state ?? NOT_REPORTED },
  { header: 'Robustness', width: '110px', cell: (v) => v.robustness ?? NOT_REPORTED },
  {
    header: 'Size',
    width: '110px',
    cell: (v) => <>{v.size === undefined ? NOT_REPORTED : formatBytes(v.size)}{v.actualSize !== undefined && <Sub>{formatBytes(v.actualSize)} used</Sub>}</>,
  },
  { header: 'Healthy replicas', width: '130px', cell: (v) => <ReplicasCell v={v} /> },
  { header: 'Claim', width: '190px', cell: (v) => <ClaimCell v={v} /> },
]

export function LonghornVolumes({ data, onInspect, inspected }: LonghornScreenProps) {
  const [params, setParams] = useSearchParams()
  const raw = params.get('health')
  const health = HEALTHS.includes(raw as LonghornVolumeHealth) ? (raw as LonghornVolumeHealth) : ''
  const [search, setSearch] = useState('')
  const setHealth = (id: LonghornVolumeHealth | '') => {
    const next = new URLSearchParams(params)
    if (id) next.set('health', id)
    else next.delete('health')
    setParams(next, { replace: true })
  }
  const rows = useMemo(() => {
    const q = search.trim().toLowerCase()
    return data.volumes.filter(
      (v) => (!health || v.health === health) && (!q || [v.name, v.namespace, v.use.claim?.name, v.use.persistentVolume].some((s) => s?.toLowerCase().includes(q))),
    )
  }, [data.volumes, health, search])
  const options = [
    { id: '' as const, label: 'All', count: data.volumes.length },
    ...HEALTHS.map((h) => ({ id: h, label: LONGHORN_HEALTH[h].label, count: data.volumes.filter((v) => v.health === h).length })),
  ]

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <LonghornHeader title="Volumes" data={data} />
      <ScreenBody>
        <LonghornCoverageNotice data={data} />
        <div className="flex flex-wrap items-center gap-3">
          <Segments label="Volume health" value={health} onChange={setHealth} options={options} />
          <SearchBox value={search} onChange={setSearch} scope="global" shortcutId="longhorn-volumes-search" placeholder="Search volumes or claims" className="w-64" />
        </div>
        <SectionTable
          columns={columns}
          rows={rows}
          rowKey={(v) => v.uid || `${v.namespace}/${v.name}`}
          rowResource={(v) => longhornVolumeResource(v.namespace, v.name)}
          onInspect={onInspect}
          inspected={inspected}
          empty={data.volumes.length === 0 ? coverageEmpty(data.coverage.volumes, 'Longhorn volumes') : 'No volume matches.'}
          minWidth={900}
        />
      </ScreenBody>
    </div>
  )
}
