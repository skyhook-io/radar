import { FilterPill } from '@skyhook-io/k8s-ui'
import type { CSSProperties } from 'react'

const row: CSSProperties = { display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center' }
const noop = () => {}

export function Tones() {
  return (
    <div style={row}>
      <FilterPill label="All" active tone="neutral" onClick={noop} />
      <FilterPill label="Critical" active tone="danger" onClick={noop} />
      <FilterPill label="Warning" active tone="warn" onClick={noop} />
      <FilterPill label="Healthy" active tone="ok" onClick={noop} />
      <FilterPill label="Managed" active tone="brand" onClick={noop} />
    </div>
  )
}

export function ActiveVsInactive() {
  return (
    <div style={row}>
      <FilterPill label="OutOfSync" active tone="danger" onClick={noop} />
      <FilterPill label="OutOfSync" active={false} tone="danger" onClick={noop} />
      <FilterPill label="Suspended" active tone="warn" onClick={noop} />
      <FilterPill label="Suspended" active={false} tone="warn" onClick={noop} />
    </div>
  )
}

export function WithCounts() {
  return (
    <div style={row}>
      <FilterPill label="Deployments" active tone="neutral" count={24} onClick={noop} />
      <FilterPill label="Failing" active={false} tone="danger" count={3} onClick={noop} />
      <FilterPill label="Degraded" active={false} tone="warn" count={7} onClick={noop} />
      <FilterPill label="Running" active={false} tone="ok" count={142} onClick={noop} />
    </div>
  )
}
