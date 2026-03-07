import { NodeRenderer as BaseNodeRenderer } from '@skyhook-io/k8s-ui/components/resources/renderers/NodeRenderer'
import { useNavigate } from 'react-router-dom'
import { useNodeMetrics, useNodeMetricsHistory, usePrometheusResourceMetrics, usePrometheusStatus } from '../../../api/client'
import { serializeColumnFilters } from '../resource-utils'

interface NodeRendererProps {
  data: any
  relationships?: { pods?: any[] }
}

export function NodeRenderer({ data, relationships }: NodeRendererProps) {
  const navigate = useNavigate()
  const nodeName = data.metadata?.name

  // Fetch node metrics
  const { data: metrics } = useNodeMetrics(nodeName)
  const { data: metricsHistory } = useNodeMetricsHistory(nodeName)

  // Determine whether to hide metrics-server section (Prometheus has data)
  const { data: prometheusStatus } = usePrometheusStatus()
  const prometheusConnected = prometheusStatus?.connected === true
  const { data: prometheusCPU, isLoading: prometheusCPULoading, error: prometheusCPUError } = usePrometheusResourceMetrics(
    'Node', '', nodeName ?? '', 'cpu', '1h', prometheusConnected,
  )
  const prometheusHasCPU = !prometheusCPUError && (prometheusCPU?.result?.series?.some(
    s => s.dataPoints?.length > 0,
  ) ?? false)
  const hideMetricsServer = prometheusHasCPU || (prometheusConnected && prometheusCPULoading)

  return (
    <BaseNodeRenderer
      data={data}
      relationships={relationships}
      onViewPods={nodeName ? () => {
        const params = new URLSearchParams()
        params.set('filters', serializeColumnFilters({ node: [nodeName] }))
        navigate(`/resources/pods?${params.toString()}`)
      } : undefined}
      metrics={metrics}
      metricsHistory={metricsHistory}
      hideMetricsServer={hideMetricsServer}
    />
  )
}
