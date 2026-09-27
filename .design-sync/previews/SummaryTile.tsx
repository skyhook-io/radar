import { SummaryTile } from '@skyhook-io/k8s-ui'

const row = { display: 'flex', gap: 10, alignItems: 'stretch', flexWrap: 'wrap', padding: 10 } as const
const noop = () => {}

export function Tones() {
  return (
    <div style={row}>
      <SummaryTile label="Healthy" value={42} tone="success" />
      <SummaryTile label="Progressing" value={4} tone="info" />
      <SummaryTile label="Degraded" value={6} tone="warning" />
      <SummaryTile label="Failed" value={2} tone="error" />
      <SummaryTile label="Total" value={54} tone="neutral" />
    </div>
  )
}

export function Interactive() {
  return (
    <div style={row}>
      <SummaryTile label="Synced" value={48} tone="success" active onClick={noop} />
      <SummaryTile label="OutOfSync" value={5} tone="warning" onClick={noop} />
      <SummaryTile label="Missing" value={1} tone="error" onClick={noop} />
    </div>
  )
}

export function Loading() {
  return (
    <div style={row}>
      <SummaryTile label="Healthy" value={0} tone="success" loading />
      <SummaryTile label="Degraded" value={0} tone="warning" loading />
      <SummaryTile label="Total" value={0} tone="neutral" loading />
    </div>
  )
}
