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

  it.each([
    ['1024000m', '2048000m'],
    ['1024000000u', '2048000000u'],
    ['1024000000000n', '2048000000000n'],
    ['1.024k', '2.048k'],
    ['1.024K', '2.048K'],
  ])('reads memory request %s and limit %s as bytes', (request, limit) => {
    const resource = { spec: { containers: [
      { resources: { requests: { memory: request }, limits: { memory: limit } } },
    ] } }
    expect(computeRequestLimitLines(resource, 'Pod', 'memory')).toEqual([
      { value: 1024, label: 'request 1KiB', kind: 'request' },
      { value: 2048, label: 'limit 2KiB', kind: 'limit' },
    ])
  })

  it('sums binary and millibyte quantities across containers and native sidecars', () => {
    const resource = { spec: { template: { spec: {
      containers: [{ resources: { requests: { memory: '16Gi' }, limits: { memory: '16Gi' } } }],
      initContainers: [{ restartPolicy: 'Always', resources: {
        requests: { memory: '1181116006400m' }, limits: { memory: '1073741824000m' },
      } }],
    } } } }
    const lines = computeRequestLimitLines(resource, 'Deployment', 'memory')!
    expect(lines[0].value).toBeCloseTo(18360985190.4)
    expect(lines[0].label).toBe('request 17.1GiB')
    expect(lines[1]).toEqual({ value: 17 * 1024 ** 3, label: 'limit 17.0GiB', kind: 'limit' })
  })

  it.each(['abcMi', 'abcm', 'abcu', 'abcn', '', undefined])('omits unreadable memory quantities: %s', (memory) => {
    const resource = { spec: { containers: [
      { resources: { requests: { memory }, limits: { memory } } },
    ] } }
    expect(computeRequestLimitLines(resource, 'Pod', 'memory')).toBeUndefined()
  })

  it('retains a genuine zero request while omitting a zero limit', () => {
    const resource = { spec: { containers: [
      { resources: { requests: { memory: '0m' }, limits: { memory: '0m' } } },
    ] } }
    expect(computeRequestLimitLines(resource, 'Pod', 'memory')).toEqual([
      { value: 0, label: 'request 0KiB', kind: 'request' },
    ])
  })
})
