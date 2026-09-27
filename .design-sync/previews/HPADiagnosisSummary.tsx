import { HPADiagnosisSummary, type HPADiagnosisView } from '@skyhook-io/k8s-ui'

const maxed: HPADiagnosisView = {
  state: 'limited_max',
  summary: 'HPA wants more replicas but is capped at maxReplicas=10',
  bounds: { min: 2, max: 10, current: 10, desired: 10 },
  reasons: [
    {
      id: 'limited_max',
      message: 'HPA is capped at maxReplicas=10',
      detail: 'the desired replica count is more than the maximum replica count',
      conditionType: 'ScalingLimited',
      conditionReason: 'TooManyReplicas',
    },
    { id: 'missing_current_metric', message: 'HPA is missing current metric values', detail: 'http_requests_per_second' },
  ],
}

const metricsDown: HPADiagnosisView = {
  state: 'metrics_unavailable',
  summary: 'HPA cannot read its metrics, so it is not scaling',
  bounds: { min: 3, max: 20, current: 3, desired: 3 },
  reasons: [
    {
      id: 'metrics_unavailable',
      message: 'Failed to get cpu utilization',
      detail: 'unable to get metrics for resource cpu: unable to fetch metrics from resource metrics API: the server could not find the requested resource (get pods.metrics.k8s.io)',
      conditionType: 'ScalingActive',
      conditionReason: 'FailedGetResourceMetric',
    },
  ],
}

const healthy: HPADiagnosisView = {
  state: 'ok',
  summary: 'HPA is scaling normally within its bounds',
  bounds: { min: 2, max: 12, current: 4, desired: 4 },
  reasons: [],
}

const header = (
  <>
    <span className="font-medium text-theme-text-primary">HorizontalPodAutoscaler checkout-api</span>
  </>
)

const wrap = { width: 480 }

export function DetailCapped() {
  return <div style={wrap}><HPADiagnosisSummary diagnosis={maxed} variant="detail" /></div>
}

export function DetailMetricsUnavailable() {
  return <div style={wrap}><HPADiagnosisSummary diagnosis={metricsDown} variant="detail" /></div>
}

export function InlineCapped() {
  return <div style={wrap}><HPADiagnosisSummary diagnosis={maxed} variant="inline" header={header} /></div>
}

export function InlineHealthy() {
  return (
    <div style={wrap}>
      <HPADiagnosisSummary
        diagnosis={healthy}
        variant="inline"
        header={<span className="font-medium text-theme-text-primary">HorizontalPodAutoscaler ledger-worker</span>}
      />
    </div>
  )
}
