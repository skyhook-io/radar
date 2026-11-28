import { useMemo } from 'react'
import { NetworkPolicyAppliesTo, NetworkPolicyRulesView } from './NetworkPolicyRulesView'
import { summarizeNetworkPolicy } from '../../../utils/network-policy-summary'

interface NetworkPolicyDiagramProps {
  spec: any
  staged?: boolean
  namespace?: string
}

/** A core NetworkPolicy as who it applies to, then one card per rule. */
export function NetworkPolicyDiagram({ spec, staged = false, namespace }: NetworkPolicyDiagramProps) {
  const summary = useMemo(() => summarizeNetworkPolicy(spec, namespace), [spec, namespace])
  return (
    <div className="space-y-3 min-w-0">
      <NetworkPolicyAppliesTo summary={summary} />
      <NetworkPolicyRulesView summary={summary} staged={staged} />
    </div>
  )
}
