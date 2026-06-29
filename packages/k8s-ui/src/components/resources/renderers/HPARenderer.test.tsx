import { describe, expect, it } from 'vitest'
import { renderToString } from 'react-dom/server'
import { HPARenderer } from './HPARenderer'

const baseHPA = {
  metadata: { name: 'api-hpa', namespace: 'prod' },
  spec: {
    maxReplicas: 10,
    scaleTargetRef: { apiVersion: 'apps/v1', kind: 'Deployment', name: 'api' },
  },
  status: {
    currentReplicas: 3,
    desiredReplicas: 3,
  },
}

describe('HPARenderer', () => {
  it('does not count ScalingLimited=False as a failing condition', () => {
    const html = renderToString(
      <HPARenderer
        data={{
          ...baseHPA,
          status: {
            ...baseHPA.status,
            conditions: [
              { type: 'ScalingActive', status: 'False', reason: 'FailedGetResourceMetric' },
              { type: 'AbleToScale', status: 'True', reason: 'SucceededGetScale' },
              { type: 'ScalingLimited', status: 'False', reason: 'DesiredWithinRange' },
            ],
          },
        }}
      />,
    )

    expect(html).toContain('Conditions (3) · 1 failing')
    expect(html).not.toContain('2 failing')
  })

  it('renders ScalingLimited=True at max as a warning instead of a healthy condition', () => {
    const html = renderToString(
      <HPARenderer
        data={{
          ...baseHPA,
          status: {
            ...baseHPA.status,
            conditions: [
              {
                type: 'ScalingLimited',
                status: 'True',
                reason: 'TooManyReplicas',
                message: 'the desired replica count is more than the maximum replica count',
              },
            ],
          },
        }}
      />,
    )

    expect(html).toContain('border-amber-400/60')
    expect(html).toContain('text-amber-600')
    expect(html).not.toContain('failing')
  })

  it('does not repeat max-limited controller text as a second diagnosis headline', () => {
    const html = renderToString(
      <HPARenderer
        data={baseHPA}
        hpaDiagnosis={{
          state: 'limited_max',
          summary: 'HPA wants more replicas but is capped at maxReplicas=10',
          target: { kind: 'Deployment', name: 'api' },
          bounds: { min: 1, max: 10, current: 10, desired: 10 },
          reasons: [
            {
              id: 'limited_max',
              message: 'the desired replica count is more than the maximum replica count',
              conditionType: 'ScalingLimited',
              conditionReason: 'TooManyReplicas',
            },
          ],
        }}
      />,
    )

    expect(html).toContain('HPA wants more replicas but is capped at maxReplicas=10')
    expect(html).toContain('Evidence')
    expect(html).toContain('ScalingLimited')
    expect(html).toContain('TooManyReplicas')
    expect(html).not.toContain('the desired replica count is more than the maximum replica count')
  })
})
