import { ExpandableSection, KeyValueBadgeList } from '@skyhook-io/k8s-ui'
import type { CSSProperties } from 'react'

const card: CSSProperties = {
  width: 460,
  maxWidth: '100%',
  padding: 16,
  borderRadius: 8,
  border: '1px solid var(--border-default, #D8E0EE)',
  background: 'var(--bg-surface, #F8FBFE)',
}
const mono: CSSProperties = {
  fontFamily: 'var(--font-mono, monospace)',
  fontSize: 12,
  color: 'var(--text-secondary, #555860)',
  lineHeight: 1.7,
}

export function Expanded() {
  return (
    <div style={card}>
      <ExpandableSection title="Environment (3)">
        <div style={mono}>
          DATABASE_URL=postgres://checkout-db:5432/orders
          <br />
          REDIS_HOST=redis-cache.payments.svc
          <br />
          LOG_LEVEL=info
        </div>
      </ExpandableSection>
    </div>
  )
}

export function WithBadges() {
  return (
    <div style={card}>
      <ExpandableSection title="Ports (2)">
        <KeyValueBadgeList items={{ http: '8080/TCP', metrics: '9090/TCP' }} />
      </ExpandableSection>
    </div>
  )
}

export function Collapsed() {
  return (
    <div style={card}>
      <ExpandableSection title="Volume mounts (5)" defaultExpanded={false}>
        <div style={mono}>
          /var/run/secrets/kubernetes.io/serviceaccount
          <br />
          /etc/checkout/config
        </div>
      </ExpandableSection>
    </div>
  )
}
