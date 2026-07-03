import { describe, expect, it } from 'vitest'
import {
  ApiError,
  getVisibleLiveMetrics,
  isMetricsUnavailableError,
  isLiveMetricsUnavailable,
  normalizeNodeMetricsHistory,
  normalizePodMetricsHistory,
  shouldFetchLiveMetrics,
} from './client'

describe('metrics unavailable classification', () => {
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

  it('uses the server-owned unavailable flag for history responses', () => {
    const podHistory = normalizePodMetricsHistory({
      namespace: 'default',
      name: 'api',
      containers: [],
      metricsUnavailable: true,
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
      metricsUnavailable: true,
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

  it('does not infer history unavailability from collection-error copy', () => {
    const history = normalizeNodeMetricsHistory({
      name: 'kind-worker',
      dataPoints: [],
      collectionError: 'Node metrics not found (metrics-server may not be installed)',
      rawCollectionError: 'the server could not find the requested resource',
    })
    expect(history.collectionError).toBe('Node metrics not found (metrics-server may not be installed)')
    expect(history.rawCollectionError).toBe('the server could not find the requested resource')
    expect(history.metricsUnavailable).toBeUndefined()
    expect(history.metricsUnavailableReason).toBeUndefined()
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

  it('does not expose cached live metrics while the live query is disabled', () => {
    const cachedMetrics = { usage: { cpu: '10m', memory: '20Mi' } }
    expect(getVisibleLiveMetrics(false, false, cachedMetrics)).toBeUndefined()
    expect(getVisibleLiveMetrics(true, true, cachedMetrics)).toBeUndefined()
    expect(getVisibleLiveMetrics(true, false, cachedMetrics)).toBe(cachedMetrics)
  })

  it('treats null live metrics as unavailable only after live fetch is enabled', () => {
    expect(isLiveMetricsUnavailable(false, null)).toBe(false)
    expect(isLiveMetricsUnavailable(true, null)).toBe(true)
    expect(isLiveMetricsUnavailable(true, undefined)).toBe(false)
  })
})
