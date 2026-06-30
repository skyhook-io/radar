import { describe, expect, it } from 'vitest'
import {
  ApiError,
  isMetricsUnavailableError,
  isMetricsUnavailableMessage,
  normalizeNodeMetricsHistory,
  normalizePodMetricsHistory,
} from './client'

describe('metrics unavailable classification', () => {
  it('recognizes metrics-server absence messages', () => {
    expect(isMetricsUnavailableMessage('Pod metrics not found (metrics-server may not be installed)')).toBe(true)
    expect(isMetricsUnavailableMessage('the server could not find the requested resource (get pods.metrics.k8s.io)')).toBe(true)
    expect(isMetricsUnavailableMessage('the server could not find the requested resource')).toBe(true)
    expect(isMetricsUnavailableMessage('no access to nodes')).toBe(false)
  })

  it('only treats metrics-shaped API failures as metrics unavailable', () => {
    expect(isMetricsUnavailableError(new ApiError('Node metrics not found (metrics-server may not be installed)', 404))).toBe(true)
    expect(isMetricsUnavailableError(new ApiError('the server could not find the requested resource (get nodes.metrics.k8s.io)', 500))).toBe(true)
    expect(isMetricsUnavailableError(new ApiError('failed to get node metrics: the server could not find the requested resource', 500))).toBe(true)
    expect(isMetricsUnavailableError(new ApiError('the server could not find the requested resource', 500))).toBe(false)
    expect(isMetricsUnavailableError(new ApiError('no access to nodes', 403))).toBe(false)
    expect(isMetricsUnavailableError(new ApiError('database unavailable', 500))).toBe(false)
  })

  it('turns metrics API collection errors into an unavailable state', () => {
    const podHistory = normalizePodMetricsHistory({
      namespace: 'default',
      name: 'api',
      containers: [],
      collectionError: 'the server could not find the requested resource (get pods.metrics.k8s.io)',
    })
    expect(podHistory.collectionError).toBeUndefined()
    expect(podHistory.metricsUnavailable).toBe(true)

    const nodeHistory = normalizeNodeMetricsHistory({
      name: 'kind-worker',
      dataPoints: [],
      collectionError: 'Node metrics not found (metrics-server may not be installed)',
    })
    expect(nodeHistory.collectionError).toBeUndefined()
    expect(nodeHistory.metricsUnavailable).toBe(true)
  })

  it('keeps non-metrics collection errors visible', () => {
    const history = normalizeNodeMetricsHistory({
      name: 'kind-worker',
      dataPoints: [],
      collectionError: 'forbidden: no access to nodes',
    })
    expect(history.collectionError).toBe('forbidden: no access to nodes')
    expect(history.metricsUnavailable).toBeUndefined()
  })
})
