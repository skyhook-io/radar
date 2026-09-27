import { ProvenanceBadge } from '@skyhook-io/k8s-ui'

const row = { display: 'flex', gap: 8, flexWrap: 'wrap', alignItems: 'center' } as const

export function Sources() {
  return (
    <div style={row}>
      <ProvenanceBadge tier={3} appKey="argocd/checkout-api" confidence="high" />
      <ProvenanceBadge tier={1} appKey="flux-system/ingress-nginx" confidence="high" />
      <ProvenanceBadge tier={5} appKey="payments/redis" confidence="high" />
      <ProvenanceBadge tier={7} appKey="payments/billing" confidence="medium" />
      <ProvenanceBadge appKey="default/debug-shell" />
    </div>
  )
}

export function Confidence() {
  return (
    <div style={row}>
      <ProvenanceBadge tier={6} appKey="payments/checkout-api" confidence="high" />
      <ProvenanceBadge tier={8} appKey="payments/ledger-worker" confidence="medium" />
      <ProvenanceBadge tier={9} appKey="payments/fraud-scorer" confidence="low" />
    </div>
  )
}
