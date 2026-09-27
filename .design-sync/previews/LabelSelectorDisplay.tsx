import { LabelSelectorDisplay } from '@skyhook-io/k8s-ui'

const stack = { display: 'flex', flexDirection: 'column', gap: 16, maxWidth: 480 } as const
const field = { display: 'flex', flexDirection: 'column', gap: 4 } as const
const label = {
  fontSize: 10,
  fontWeight: 600,
  letterSpacing: '0.05em',
  textTransform: 'uppercase',
  color: 'var(--text-tertiary)',
} as const
const sentence = { fontSize: 13, color: 'var(--text-secondary)', lineHeight: 1.6 }

export function FlatSelector() {
  return (
    <div style={field}>
      <span style={label}>Service selector</span>
      <LabelSelectorDisplay selector={{ app: 'payment-service', tier: 'backend' }} />
    </div>
  )
}

export function MatchExpressions() {
  return (
    <div style={stack}>
      <div style={field}>
        <span style={label}>Deployment selector</span>
        <LabelSelectorDisplay
          selector={{
            matchLabels: { app: 'checkout', 'app.kubernetes.io/instance': 'checkout-prod' },
            matchExpressions: [
              { key: 'environment', operator: 'In', values: ['production', 'staging'] },
              { key: 'tier', operator: 'Exists' },
            ],
          }}
        />
      </div>
    </div>
  )
}

export function EmptyState() {
  return (
    <div style={field}>
      <span style={label}>NetworkPolicy podSelector</span>
      <LabelSelectorDisplay selector={null} emptyText="All pods in namespace" />
    </div>
  )
}

export function Inline() {
  return (
    <p style={sentence}>
      This PodDisruptionBudget protects pods matching{' '}
      <LabelSelectorDisplay selector={{ app: 'redis', role: 'primary' }} inline /> in the production namespace.
    </p>
  )
}
