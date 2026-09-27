import { KeyValueBadgeList, Property, PropertyList, Section } from '@skyhook-io/k8s-ui'
import { Activity, Info, Tag } from 'lucide-react'

const card = {
  width: 480,
  maxWidth: '100%',
  padding: 16,
  borderRadius: 8,
  border: '1px solid var(--border-default)',
  background: 'var(--bg-surface)',
}

export function Details() {
  return (
    <div style={card}>
      <Section title="Details" icon={Info}>
        <PropertyList>
          <Property label="Replicas" value="3 / 3 ready" />
          <Property label="Strategy" value="RollingUpdate" />
          <Property label="Selector" value="app=checkout-api" />
        </PropertyList>
      </Section>
    </div>
  )
}

export function LabelsSection() {
  return (
    <div style={card}>
      <Section title="Labels (4)" icon={Tag}>
        <KeyValueBadgeList
          items={{
            app: 'checkout-api',
            'app.kubernetes.io/managed-by': 'Helm',
            environment: 'production',
            team: 'payments',
          }}
        />
      </Section>
    </div>
  )
}

export function Collapsed() {
  return (
    <div style={card}>
      <Section title="Annotations (12)" defaultExpanded={false}>
        <div style={{ fontSize: 13, color: 'var(--text-tertiary)' }}>hidden</div>
      </Section>
    </div>
  )
}

export function CardVariant() {
  return (
    <div style={{ width: 480, maxWidth: '100%' }}>
      <Section variant="card" title="Runtime Health" icon={Activity} defaultExpanded>
        <PropertyList>
          <Property label="Head" value="1 / 1 ready" />
          <Property label="Workers" value="4 / 4 ready" />
          <Property label="Dashboard" value="http://raycluster-llm-head-svc.ml:8265" />
        </PropertyList>
      </Section>
    </div>
  )
}
