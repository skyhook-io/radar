import { HPARenderer as BaseHPARenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/HPARenderer'
import { HPACharts } from '../../resource/HPACharts'

interface HPARendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
}

export function HPARenderer({ data, onNavigate }: HPARendererProps) {
  return (
    <BaseHPARenderer
      data={data}
      onNavigate={onNavigate}
      extraSections={<HPACharts data={data} />}
    />
  )
}
