import { HPARenderer as BaseHPARenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/HPARenderer'
import { HPACharts } from '../../resource/HPACharts'
import type { HPADiagnosis } from '@skyhook-io/k8s-ui'

interface HPARendererProps {
  data: any
  onNavigate?: (ref: { kind: string; namespace: string; name: string }) => void
  hpaDiagnosis?: HPADiagnosis
}

export function HPARenderer({ data, onNavigate, hpaDiagnosis }: HPARendererProps) {
  return (
    <BaseHPARenderer
      data={data}
      onNavigate={onNavigate}
      hpaDiagnosis={hpaDiagnosis}
      extraSections={<HPACharts data={data} />}
    />
  )
}
