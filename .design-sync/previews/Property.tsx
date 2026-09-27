import { Badge, Property, PropertyList } from '@skyhook-io/k8s-ui'

const card = {
  width: 460,
  maxWidth: '100%',
  padding: 16,
  borderRadius: 8,
  border: '1px solid var(--border-default)',
  background: 'var(--bg-surface)',
}
const noop = () => {}

export function ResourceProperties() {
  return (
    <div style={card}>
      <PropertyList>
        <Property label="Name" value="checkout-api-7d9c4b8f6-x2kqz" />
        <Property label="Namespace" value="payments" />
        <Property label="Node" value="gke-prod-pool-a-3f1c" />
        <Property label="QoS Class" value="Burstable" />
        <Property label="Restarts" value={3} />
        <Property label="Age" value="6d" />
      </PropertyList>
    </div>
  )
}

export function ElementValues() {
  return (
    <div style={card}>
      <PropertyList>
        <Property label="Phase" value={<Badge severity="success">Running</Badge>} />
        <Property label="Kind" value={<Badge kind="Pod">Pod</Badge>} />
        <Property label="Image" value="us-docker.pkg.dev/koala/checkout-api:v2.14.3" />
      </PropertyList>
    </div>
  )
}

export function Copyable() {
  return (
    <div style={card}>
      <PropertyList>
        <Property label="Pod IP" value="10.44.2.187" copyable onCopy={noop} />
        <Property label="UID" value="8f2c1a4e-6b0d-4e9a-9f3c-1d7b2e5a0c44" copyable onCopy={noop} />
      </PropertyList>
    </div>
  )
}
