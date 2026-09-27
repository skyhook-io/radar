import { SearchPillInput } from '@skyhook-io/k8s-ui'
import type { SearchModifier } from '@skyhook-io/k8s-ui'
import { Search } from 'lucide-react'

const noop = () => {}
const box = {
  width: 460,
  maxWidth: '100%',
  padding: '7px 10px',
  borderRadius: 8,
  border: '1px solid var(--border-default)',
  background: 'var(--bg-surface)',
}
const leftSlot = <Search style={{ width: 15, height: 15, color: 'var(--text-secondary)', flexShrink: 0 }} />

export function Empty() {
  return (
    <div style={box}>
      <SearchPillInput text="" pills={[]} onChange={noop} leftSlot={leftSlot} placeholder="Search resources…  try ns:  kind:  label:" />
    </div>
  )
}

export function WithModifierPills() {
  const pills: SearchModifier[] = [
    { key: 'ns', value: 'production' },
    { key: 'kind', value: 'Deployment' },
  ]
  return (
    <div style={box}>
      <SearchPillInput text="checkout" pills={pills} onChange={noop} leftSlot={leftSlot} placeholder="Search resources…" />
    </div>
  )
}

export function ManyPillsCollapsed() {
  const pills: SearchModifier[] = [
    { key: 'ns', value: 'production' },
    { key: 'ns', value: 'staging' },
    { key: 'kind', value: 'Deployment' },
    { key: 'label', value: 'app=checkout' },
    { key: 'image', value: 'registry.io/checkout' },
  ]
  return (
    <div style={box}>
      <SearchPillInput text="" pills={pills} onChange={noop} leftSlot={leftSlot} placeholder="Search resources…" />
    </div>
  )
}
