import { useEffect, useState } from 'react'
import { Badge, Collapse, TimeValue, formatBytes, pluralize } from '@skyhook-io/k8s-ui'
import { useVeleroRunVolumes, useVeleroVolumeOperations, isVeleroRunFinished, type VeleroVolumeRecord, type VeleroVolumeDetails, type VeleroVolumeOperation } from '../../api/velero'
import { getApiBase } from '../../api/config'
import { Notice, SectionTable } from '../workspace/layout'
import { ArtifactError } from './VeleroRunArtifacts'

function volumeMethod(row: VeleroVolumeRecord, restore: boolean) {
  switch (row.method) {
    case 'NativeSnapshot': return 'Native snapshot'
    case 'CSISnapshot': return row.snapshotDataMoved ? `CSI snapshot · Copy ${restore ? 'from' : 'to'} the bucket (data mover)` : 'CSI snapshot'
    case 'PodVolumeBackup': return 'File-system backup'
    case 'PodVolumeRestore': return 'File-system restore'
    default: return row.method || 'Not recorded'
  }
}

function VolumeDetails({ label, info }: { label: string; info?: VeleroVolumeDetails }) {
  if (!info) return null
  const fields = [
    ['Snapshot', info.snapshotHandle], ['Driver', info.driver], ['Snapshot content', info.vscName],
    ['Operation', info.operationID], ['Data mover', info.dataMover], ['Uploader', info.uploaderType],
    ['Retained snapshot', info.retainedSnapshot], ['Volume group snapshot', info.volumeGroupSnapshotHandle],
    ['Volume type', info.volumeType], ['Zone', info.volumeAZ], ['IOPS', info.iops], ['Phase', info.phase],
    ['Pod', info.podName ? `${info.podNamespace ? info.podNamespace + '/' : ''}${info.podName}` : undefined],
    ['Volume', info.volumeName], ['Node', info.nodeName],
    ['Snapshot ready', info.readyToUse === undefined ? undefined : info.readyToUse ? 'Yes' : 'No'],
    ['Size', info.size === undefined || info.size === 0 ? undefined : formatBytes(info.size)],
    ['Incremental size', info.incrementalSize === undefined ? undefined : formatBytes(info.incrementalSize)],
  ].filter(([, value]) => value !== undefined && value !== '')
  return <div className="space-y-1 text-xs text-theme-text-secondary"><div className="font-medium">{label}</div>{fields.map(([key, value]) => <div key={key} className="break-all">{key}: {value}</div>)}</div>
}

function OperationGroup({ phase, rows }: { phase: string; rows: VeleroVolumeOperation[] }) {
  const [all, setAll] = useState(false)
  const table = (items: VeleroVolumeOperation[]) => <SectionTable
    minWidth={820}
    columns={[
      { header: 'Operation', cell: (row) => <div className="break-all"><Badge kind={row.kind}>{row.kind}</Badge><div className="mt-1 font-mono text-xs">{row.name}</div></div> },
      { header: 'Volume', cell: (row) => <div className="break-all">{row.pvc ? `PVC ${row.pvcNamespace ? row.pvcNamespace + '/' : ''}${row.pvc}` : row.pod ? `${row.podNamespace ? row.podNamespace + '/' : ''}${row.pod} · ${row.volume || 'Volume not recorded'}` : 'Not recorded'}</div> },
      { header: 'Progress', cell: (row) => <div className="space-y-1"><div>{row.bytesDone === undefined ? 'Unknown' : formatBytes(row.bytesDone)} / {row.bytesTotal === undefined ? 'Unknown' : formatBytes(row.bytesTotal)}</div>{row.bytesDone !== undefined && row.bytesTotal !== undefined && row.bytesTotal > 0 && <progress className="w-full accent-accent" aria-label={`${row.name} bytes`} value={row.bytesDone} max={row.bytesTotal} />}</div> },
      { header: 'Node', cell: (row) => <div className="break-all">{row.node || 'Not recorded'}{row.longRunning && <div className="mt-1 text-xs text-theme-text-secondary">Over 10 min · {row.nodeAgent === 'running' ? 'Node-agent pod phase: Running on this node' : row.nodeAgent === 'notRunning' ? 'No Running node-agent pod found on this node' : 'Node-agent status unknown'}</div>}</div> },
      { header: 'Status', cell: (row) => <div className="space-y-1 break-words">{row.message && <div>{row.message}</div>}<div>Started: {row.started ? <TimeValue timestamp={row.started} /> : 'Not recorded'}</div><div>Finished: {row.completed ? <TimeValue timestamp={row.completed} /> : 'Not recorded'}</div></div> },
    ]}
    rows={items}
    rowKey={(row) => `${row.kind}/${row.name}`}
    empty="No operations."
  />
  return <section className="space-y-2">
    <h3 className="text-sm font-semibold text-theme-text-primary"><Badge severity={phase === 'Failed' ? 'error' : phase === 'Completed' ? 'success' : 'neutral'}>{phase}</Badge> · {rows.length}</h3>
    {table(rows.slice(0, 10))}
    {rows.length > 10 && <>
      <button type="button" aria-expanded={all} className="btn-secondary px-3 py-1.5 text-sm" onClick={() => setAll(!all)}>{all ? 'Show first 10' : `Show all ${rows.length}`}</button>
      <Collapse open={all} mountLazily>{table(rows.slice(10))}</Collapse>
    </>}
  </section>
}

const phaseOrder = ['Failed', 'Canceling', 'Canceled', 'InProgress', 'Prepared', 'Accepted', 'New', 'Completed']
const operationNames: Record<string, string> = { podVolumeBackups: 'Pod volume backups', podVolumeRestores: 'Pod volume restores', dataUploads: 'Data uploads', dataDownloads: 'Data downloads' }
const coverageText: Record<string, string> = {
  partial: 'Only part of this kind could be read.', denied: 'Access denied.', notInstalled: 'Not available now; this kind is not served by the cluster.',
  syncing: 'Still loading; try refreshing.', uncached: 'Not collected by Radar in this namespace.', error: 'Could not read this kind; try refreshing.',
}

export function VeleroRunVolumes({ kind, namespace, name }: { kind: 'backups' | 'restores'; namespace: string; name: string }) {
  const record = useVeleroRunVolumes(kind, namespace, name)
  const live = useVeleroVolumeOperations(kind, namespace, name)
  const restore = kind === 'restores'
  const data = record.data
  const operations = live.data?.operations ?? []
  const phases = [...new Set(operations.map((row) => row.phase || 'Not recorded'))].sort((a, b) => {
    const rank = (p: string) => phaseOrder.includes(p) ? phaseOrder.indexOf(p) : phaseOrder.length
    return rank(a) - rank(b) || a.localeCompare(b)
  })
  const inFlight = live.data && !isVeleroRunFinished(live.data.runPhase)
  const source = JSON.stringify([getApiBase(), kind, namespace, name])
  const recordReadAt = data ? record.dataUpdatedAt : record.errorUpdatedAt
  const [recordRead, setRecordRead] = useState<{ source: string; at: number; phase?: string }>({ source, at: 0 })
  useEffect(() => {
    if (!recordReadAt) return
    setRecordRead((previous) => previous.source === source && previous.at === recordReadAt ? previous : { source, at: recordReadAt, phase: live.data?.runPhase })
  }, [source, recordReadAt, live.data?.runPhase])
  const readBeforeCompletion = recordRead.source === source && recordRead.at === recordReadAt && recordRead.at > 0 && !isVeleroRunFinished(recordRead.phase) && isVeleroRunFinished(live.data?.runPhase)
  const coverage = Object.entries(live.data?.coverage ?? {})
  const complete = coverage.length > 0 && coverage.every(([, value]) => value.state === 'full')
  const recordedNone = data?.total === 0
  return <div className="space-y-5 p-4">
    <section className="space-y-3">
      <div className="flex items-center justify-between gap-2"><h2 className="text-sm font-semibold text-theme-text-primary">Recorded volumes</h2><button type="button" className="btn-secondary px-3 py-1.5 text-sm" onClick={() => void record.refetch()}>Refresh record</button></div>
      <p className="text-sm text-theme-text-secondary">{restore ? 'How each volume was to be restored. Velero writes this record before plugin operations finish; it does not record a restore result.' : 'How Velero protected each volume, as recorded with this backup. A reported success does not verify that it can be restored.'}</p>
      {inFlight && <Notice>This run is still in progress. Velero may write or update its volume record later. The volume record does not update with live operations. If this run has finished since you opened or refreshed this tab, refresh the record for final results.</Notice>}
      {readBeforeCompletion && <Notice>This run is now finished, but the volume record was requested before completion was confirmed. Refresh the record for final results.</Notice>}
      {record.dataUpdatedAt > 0 && <p className="text-xs text-theme-text-tertiary">Record read: <TimeValue timestamp={new Date(record.dataUpdatedAt).toISOString()} /></p>}
      {record.error && <ArtifactError label="Velero run volumes" error={record.error} hasData={!!data} retry={() => void record.refetch()} />}
      {record.isPending && <p className="text-sm text-theme-text-secondary">Loading volume record…</p>}
      {data?.truncated && <Notice>Showing the first 5,000 of {data.total} recorded volumes.</Notice>}
      {recordedNone && <p className="text-sm text-theme-text-secondary">Velero recorded no volumes for this run.</p>}
      {data && !recordedNone && <SectionTable
        title={pluralize(data.total, 'recorded volume')}
        minWidth={760}
        columns={[
          { header: 'Volume', cell: (row) => <div className="space-y-1 break-all">{row.pvcName && <div>PVC {row.pvcNamespace ? row.pvcNamespace + '/' : ''}{row.pvcName}</div>}{row.pvName && <div className="text-xs text-theme-text-secondary">PV {row.pvName}</div>}{!row.pvcName && !row.pvName && <div>{row.podVolumeInfo?.podName ? `${row.podVolumeInfo.podNamespace || ''}/${row.podVolumeInfo.podName} · ${row.podVolumeInfo.volumeName || 'Volume not recorded'}` : 'Not recorded'}</div>}</div> },
          { header: 'Method', cell: (row) => <div className="space-y-1">{volumeMethod(row, restore)}{row.method === 'CSISnapshot' && row.preserveLocalSnapshot !== undefined && <div className="text-xs text-theme-text-secondary">Local snapshot retained: {row.preserveLocalSnapshot ? 'Yes' : 'No'}</div>}</div> },
          ...(!restore ? [{ header: 'Result', cell: (row: VeleroVolumeRecord) => <div className="space-y-1">{row.skipped ? <><Badge severity="neutral">Skipped</Badge><div>{row.skippedReason || 'Reason not recorded'}</div></> : row.result ? <Badge severity={row.result === 'failed' ? 'error' : row.result === 'succeeded' ? 'success' : 'neutral'}>{row.result}</Badge> : 'Not recorded'}</div> }] : []),
          { header: 'Recorded details', cell: (row) => <div className="space-y-2"><VolumeDetails label="Native snapshot" info={row.nativeSnapshotInfo} /><VolumeDetails label="CSI snapshot" info={row.csiSnapshotInfo} /><VolumeDetails label="Data mover" info={row.snapshotDataMovementInfo} /><VolumeDetails label="File-system copy" info={row.podVolumeInfo} />{!row.nativeSnapshotInfo && !row.csiSnapshotInfo && !row.snapshotDataMovementInfo && !row.podVolumeInfo && 'Not recorded'}</div> },
        ]}
        rows={data.volumes.map((row, index) => ({ ...row, index }))}
        rowKey={(row) => String(row.index)}
        empty="Velero recorded no volumes in this file."
      />}
    </section>
    <section className="space-y-3">
      <div className="flex items-center justify-between gap-2"><h2 className="text-sm font-semibold text-theme-text-primary">Current volume operations</h2><button type="button" className="btn-secondary px-3 py-1.5 text-sm" onClick={() => void live.refetch()}>Refresh operations</button></div>
      {live.error && <ArtifactError label="Velero run volumes" error={live.error} hasData={!!live.data} retry={() => void live.refetch()} />}
      {live.isPending && <p className="text-sm text-theme-text-secondary">Loading volume operations…</p>}
      {coverage.filter(([, value]) => value.state !== 'full').map(([key, value]) => <Notice key={key}>{operationNames[key] || key}: {coverageText[value.state] || 'Coverage unknown' } No conclusion about this run’s volume method can be drawn from missing operations.</Notice>)}
      {live.data && operations.length === 0 && !(complete && recordedNone) && <Notice>{complete ? (inFlight ? 'No volume operation objects found yet for this run.' : 'No current volume operation objects found. They may have been removed; use the recorded volumes above for this run’s history.') : 'No readable volume operations found. Unavailable kinds do not mean this run used none.'}</Notice>}
      {phases.map((phase) => <OperationGroup key={phase} phase={phase} rows={operations.filter((row) => (row.phase || 'Not recorded') === phase)} />)}
    </section>
  </div>
}
