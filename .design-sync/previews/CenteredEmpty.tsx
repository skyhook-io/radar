import { CenteredEmpty } from '@skyhook-io/k8s-ui'
import { CircleCheck, Search } from 'lucide-react'
import type { CSSProperties } from 'react'

const panel: CSSProperties = {
  width: 560,
  maxWidth: '100%',
  borderRadius: 10,
  border: '1px solid var(--border-default, #D8E0EE)',
  background: 'var(--bg-base, #F0F3F9)',
  overflow: 'hidden',
}

export function NotFound() {
  return (
    <div style={panel}>
      <CenteredEmpty
        tone="neutral"
        icon={Search}
        headline="Resource not found"
        body="No Deployment named checkout-api exists in namespace payments. It may have been deleted or renamed."
      />
    </div>
  )
}

export function Healthy() {
  return (
    <div style={panel}>
      <CenteredEmpty
        tone="healthy"
        icon={CircleCheck}
        headline="No operational issues"
        body="All 42 workloads in prod-cluster-us-east1 are healthy and reconciled."
      />
    </div>
  )
}
