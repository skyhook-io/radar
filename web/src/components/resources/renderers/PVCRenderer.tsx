import { PVCRenderer as BasePVCRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/PVCRenderer'
import { PVCUsageBar } from '../../resource/PVCUsageBar'

interface PVCRendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

export function PVCRenderer({ data, onNavigate }: PVCRendererProps) {
  const namespace = data?.metadata?.namespace ?? ''
  const name = data?.metadata?.name ?? ''
  return (
    <BasePVCRenderer
      data={data}
      onNavigate={onNavigate}
      extraSections={namespace && name ? <PVCUsageBar namespace={namespace} name={name} /> : undefined}
    />
  )
}
