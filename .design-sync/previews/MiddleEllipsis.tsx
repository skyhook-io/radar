import { MiddleEllipsis } from '@skyhook-io/k8s-ui'

const label = {
  fontSize: 11,
  textTransform: 'uppercase',
  letterSpacing: '0.06em',
  color: 'var(--text-tertiary)',
  marginBottom: 4,
} as const
const box = (width: number) => ({
  width,
  padding: '6px 10px',
  borderRadius: 6,
  border: '1px solid var(--border-default)',
  background: 'var(--bg-surface)',
  color: 'var(--text-primary)',
  fontFamily: 'var(--font-mono, monospace)',
  fontSize: 13,
})
const group = { display: 'flex', flexDirection: 'column', gap: 16 } as const

const GKE = 'gke_koala-prod_us-east1-b_prod-cluster-us-east1'
const ARN = 'arn:aws:eks:us-east-1:412210987654:cluster/staging-cluster-eu'

export function TruncatedContext() {
  return (
    <div style={group}>
      <div>
        <div style={label}>240px — middle-truncates</div>
        <div style={box(240)}>
          <MiddleEllipsis text={GKE} title={GKE} />
        </div>
      </div>
      <div>
        <div style={label}>200px — EKS ARN</div>
        <div style={box(200)}>
          <MiddleEllipsis text={ARN} title={ARN} />
        </div>
      </div>
    </div>
  )
}

export function FitsFull() {
  return (
    <div style={group}>
      <div>
        <div style={label}>420px — fits, no ellipsis</div>
        <div style={box(420)}>
          <MiddleEllipsis text={GKE} />
        </div>
      </div>
      <div>
        <div style={label}>220px — short name fits</div>
        <div style={box(220)}>
          <MiddleEllipsis text="prod-cluster-us-east1" />
        </div>
      </div>
    </div>
  )
}
