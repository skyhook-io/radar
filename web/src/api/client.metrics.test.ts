import { describe, expect, it } from 'vitest'
import {
  ApiError,
  isMetricsUnavailableError,
  isMetricsUnavailableMessage,
  normalizeNodeMetricsHistory,
  normalizePodMetricsHistory,
  shouldFetchLiveMetrics,
} from './client'

describe('metrics unavailable classification', () => {
  it('recognizes metrics-server absence messages', () => {
    expect(isMetricsUnavailableMessage('Pod metrics not found (metrics-server may not be installed)')).toBe(true)
    expect(isMetricsUnavailableMessage('the server could not find the requested resource (get pods.metrics.k8s.io)')).toBe(true)
    expect(isMetricsUnavailableMessage('no matches for kind "PodMetrics" in version "metrics.k8s.io/v1beta1"')).toBe(true)
    expect(isMetricsUnavailableMessage('no resource matches "pods.metrics.k8s.io"')).toBe(true)
    expect(isMetricsUnavailableMessage('the server is currently unable to handle the request (get pods.metrics.k8s.io)')).toBe(true)
    expect(isMetricsUnavailableMessage('the server could not find the requested resource')).toBe(false)
    expect(isMetricsUnavailableMessage('no access to nodes')).toBe(false)
    expect(isMetricsUnavailableMessage('metrics-server forbidden')).toBe(false)
  })

  it('only treats metrics-shaped API failures as metrics unavailable', () => {
    expect(isMetricsUnavailableError(new ApiError('Node metrics not found (metrics-server may not be installed)', 404))).toBe(true)
    expect(isMetricsUnavailableError(new ApiError('the server could not find the requested resource (get nodes.metrics.k8s.io)', 500))).toBe(true)
    expect(isMetricsUnavailableError(new ApiError('failed to get node metrics: the server could not find the requested resource', 500))).toBe(true)
    expect(isMetricsUnavailableError(new ApiError('the server is currently unable to handle the request (get nodes.metrics.k8s.io)', 500))).toBe(true)
    expect(isMetricsUnavailableError(new ApiError('the server could not find the requested resource', 500))).toBe(false)
    expect(isMetricsUnavailableError(new ApiError('no access to nodes', 403))).toBe(false)
    expect(isMetricsUnavailableError(new ApiError('metrics-server forbidden', 500))).toBe(false)
    expect(isMetricsUnavailableError(new ApiError('database unavailable', 500))).toBe(false)
  })

  it('turns metrics API collection errors into an unavailable state', () => {
    const podHistory = normalizePodMetricsHistory({
      namespace: 'default',
      name: 'api',
      containers: [],
      collectionError: 'the server could not find the requested resource (get pods.metrics.k8s.io)',
      rawCollectionError: 'the server could not find the requested resource',
      metricsUnavailableDiagnosis: 'The v1beta1.metrics.k8s.io APIService is not registered. Install metrics-server or restore that APIService.',
    })
    expect(podHistory.collectionError).toBeUndefined()
    expect(podHistory.rawCollectionError).toBeUndefined()
    expect(podHistory.metricsUnavailable).toBe(true)
    expect(podHistory.metricsUnavailableReason).toBe('the server could not find the requested resource')
    expect(podHistory.metricsUnavailableDiagnosis).toBe('The v1beta1.metrics.k8s.io APIService is not registered. Install metrics-server or restore that APIService.')

    const nodeHistory = normalizeNodeMetricsHistory({
      name: 'kind-worker',
      dataPoints: [],
      collectionError: 'Node metrics not found (metrics-server may not be installed)',
      rawCollectionError: 'the server could not find the requested resource',
      metricsUnavailableDiagnosis: 'The v1beta1.metrics.k8s.io APIService is not Available (FailedDiscoveryCheck). Check the metrics-server Service, endpoints, and API aggregation/TLS configuration.',
    })
    expect(nodeHistory.collectionError).toBeUndefined()
    expect(nodeHistory.rawCollectionError).toBeUndefined()
    expect(nodeHistory.metricsUnavailable).toBe(true)
    expect(nodeHistory.metricsUnavailableReason).toBe('the server could not find the requested resource')
    expect(nodeHistory.metricsUnavailableDiagnosis).toBe('The v1beta1.metrics.k8s.io APIService is not Available (FailedDiscoveryCheck). Check the metrics-server Service, endpoints, and API aggregation/TLS configuration.')
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

  it('keeps generic not-found collection errors visible without a metrics API signal', () => {
    const history = normalizeNodeMetricsHistory({
      name: 'kind-worker',
      dataPoints: [],
      collectionError: 'the server could not find the requested resource',
    })
    expect(history.collectionError).toBe('the server could not find the requested resource')
    expect(history.metricsUnavailable).toBeUndefined()
  })

  it('waits for history classification before live metrics fetches', () => {
    expect(shouldFetchLiveMetrics(false, false)).toBe(false)
    expect(shouldFetchLiveMetrics(true, false)).toBe(true)
    expect(shouldFetchLiveMetrics(true, true)).toBe(false)
  })
})
