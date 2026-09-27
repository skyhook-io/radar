import { Badge } from '@skyhook-io/k8s-ui'

const row = { display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center' } as const

export function Severities() {
  return (
    <div style={row}>
      <Badge severity="success">Healthy</Badge>
      <Badge severity="info">Syncing</Badge>
      <Badge severity="warning">Warning</Badge>
      <Badge severity="alert">Degraded</Badge>
      <Badge severity="error">Failed</Badge>
      <Badge severity="neutral">Unknown</Badge>
    </div>
  )
}

export function ResourceKinds() {
  return (
    <div style={row}>
      <Badge kind="Deployment">Deployment</Badge>
      <Badge kind="Pod">Pod</Badge>
      <Badge kind="Service">Service</Badge>
      <Badge kind="Ingress">Ingress</Badge>
      <Badge kind="ConfigMap">ConfigMap</Badge>
      <Badge kind="Secret">Secret</Badge>
    </div>
  )
}

export function Sizes() {
  return (
    <div style={row}>
      <Badge severity="success" size="sm">sm</Badge>
      <Badge severity="success" size="default">default</Badge>
    </div>
  )
}
