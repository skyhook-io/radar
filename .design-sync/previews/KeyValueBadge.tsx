import { KeyValueBadge } from '@skyhook-io/k8s-ui'

const row = { display: 'flex', gap: 4, flexWrap: 'wrap', alignItems: 'center', width: 420 } as const

export function PodLabels() {
  return (
    <div style={row}>
      <KeyValueBadge k="app.kubernetes.io/name" v="checkout" />
      <KeyValueBadge k="app.kubernetes.io/version" v="2.14.1" />
      <KeyValueBadge k="pod-template-hash" v="7c9f8d6b4d" />
      <KeyValueBadge k="tier" v="backend" />
    </div>
  )
}

export function NodeSelector() {
  return (
    <div style={row}>
      <KeyValueBadge k="kubernetes.io/os" v="linux" />
      <KeyValueBadge k="karpenter.sh/capacity-type" v="spot" />
      <KeyValueBadge k="topology.kubernetes.io/zone" v="us-east-1a" />
    </div>
  )
}

export function EmptyValue() {
  return (
    <div style={row}>
      <KeyValueBadge k="node-role.kubernetes.io/control-plane" v="" />
      <KeyValueBadge k="sidecar.istio.io/inject" v="false" />
    </div>
  )
}
