import { KarpenterNodePoolRenderer as BaseKarpenterNodePoolRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/KarpenterNodePoolRenderer'
import { useNavigate } from 'react-router-dom'
import { useCapabilitiesContext } from '../../../contexts/CapabilitiesContext'

interface KarpenterNodePoolRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string; group?: string }) => void
}

// The Capacity pool-detail page is a strictly richer view of a NodePool than
// the raw drawer (ledger, workloads, demand, activity) — bridge to it whenever
// the Capacity view is actually reachable for this user.
export function KarpenterNodePoolRenderer({ data, onNavigate }: KarpenterNodePoolRendererProps) {
  const navigate = useNavigate()
  const karpenterAvailable = useCapabilitiesContext().karpenter?.state === 'available'
  const name = data?.metadata?.name

  return (
    <BaseKarpenterNodePoolRenderer
      data={data}
      onNavigate={onNavigate}
      onOpenCapacity={
        karpenterAvailable && name
          ? () => navigate(`/capacity/pools/${encodeURIComponent(name)}`)
          : undefined
      }
    />
  )
}
