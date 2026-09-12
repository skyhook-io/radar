import { describe, expect, it } from 'vitest'
import { computeRequestLimitLines } from './PrometheusCharts'

describe('Pod request and limit overlays', () => {
  it('does not draw a Pod limit when one runtime container is unlimited', () => {
    const resource = { spec: { containers: [
      { resources: { requests: { cpu: '100m' }, limits: { cpu: '1' } } },
      { resources: { requests: { cpu: '200m' } } },
    ] } }
    const lines = computeRequestLimitLines(resource, 'Pod', 'cpu')!
    expect(lines).toHaveLength(1)
    expect(lines[0].kind).toBe('request')
    expect(lines[0].value).toBeCloseTo(0.3)
  })
  it('sums fully limited runtime containers at per-Pod scale', () => {
    const resource = { spec: { replicas: 5, template: { spec: {
      containers: [{ resources: { limits: { cpu: '1' } } }],
      initContainers: [{ restartPolicy: 'Always', resources: { limits: { cpu: '200m' } } }],
    } } } }
    expect(computeRequestLimitLines(resource, 'Deployment', 'cpu')![0].value).toBeCloseTo(1.2)
  })
})
