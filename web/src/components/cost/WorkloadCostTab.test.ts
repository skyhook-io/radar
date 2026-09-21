import { describe, expect, it } from 'vitest'
import { getWorkloadCostState } from './WorkloadCostTab'
import { ApiError, type OpenCostWorkloadDetailResponse, type OpenCostWorkloadTrendResponse } from '../../api/client'

describe('getWorkloadCostState', () => {
  it('treats scaled-to-zero as a valid zero state', () => {
    const current: OpenCostWorkloadDetailResponse = {
      available: true,
      currency: 'USD',
      namespace: 'default',
      kind: 'Deployment',
      name: 'checkout',
      current: {
        name: 'checkout',
        kind: 'Deployment',
        hourlyCost: 0,
        cpuCost: 0,
        memoryCost: 0,
        replicas: 0,
        cpuUsageAvailable: false,
        memoryUsageAvailable: false,
        cpuAllocationUse: 0,
        memoryAllocationUse: 0,
      },
    }
    const trend: OpenCostWorkloadTrendResponse = {
      available: false,
      currency: 'USD',
      reason: 'no_metrics',
      namespace: 'default',
      kind: 'Deployment',
      name: 'checkout',
      range: '24h',
    }

    expect(getWorkloadCostState(current, trend, false)).toBe('zero')
  })

  it('keeps current cost visible when only historical owner metrics are missing', () => {
    const current: OpenCostWorkloadDetailResponse = {
      available: true,
      currency: 'USD',
      namespace: 'default',
      kind: 'Deployment',
      name: 'checkout',
      current: {
        name: 'checkout',
        kind: 'Deployment',
        hourlyCost: 0.2,
        cpuCost: 0.12,
        memoryCost: 0.08,
        replicas: 2,
        cpuUsageAvailable: true,
        memoryUsageAvailable: true,
        cpuAllocationUse: 25,
        memoryAllocationUse: 25,
      },
    }
    const trend: OpenCostWorkloadTrendResponse = {
      available: false,
      currency: 'USD',
      reason: 'no_metrics',
      namespace: 'default',
      kind: 'Deployment',
      name: 'checkout',
      range: '24h',
    }

    expect(getWorkloadCostState(current, trend, false)).toBe('partial_missing_history')
  })

  it('keeps Kubecost current cost visible when history is unsupported', () => {
    const current: OpenCostWorkloadDetailResponse = {
      available: true,
      source: 'kubecost',
      currency: 'USD',
      namespace: 'default',
      kind: 'StatefulSet',
      name: 'queue',
      current: {
        name: 'queue', kind: 'StatefulSet', hourlyCost: 0.2, cpuCost: 0.12,
        memoryCost: 0.08, replicas: 1, cpuUsageAvailable: true,
        memoryUsageAvailable: true, cpuAllocationUse: 25, memoryAllocationUse: 25,
      },
    }
    const trend: OpenCostWorkloadTrendResponse = {
      available: false,
      source: 'kubecost',
      reason: 'history_unsupported',
      currency: 'USD',
      namespace: 'default',
      kind: 'StatefulSet',
      name: 'queue',
      range: '24h',
    }

    expect(getWorkloadCostState(current, trend, false)).toBe('partial_missing_history')
  })

  it('does not let unsupported history mask current loading or errors', () => {
    const trend: OpenCostWorkloadTrendResponse = {
      available: false,
      source: 'kubecost',
      reason: 'history_unsupported',
      currency: 'USD',
      namespace: 'default',
      kind: 'StatefulSet',
      name: 'queue',
      range: '24h',
    }

    expect(getWorkloadCostState(undefined, trend, { currentLoading: true })).toBe('loading')
    expect(getWorkloadCostState(undefined, trend, { currentError: true })).toBe('load_error')
    expect(
      getWorkloadCostState(undefined, trend, { currentError: new ApiError('denied', 403) }),
    ).toBe('access_denied')
  })

  it('keeps current cost visible while historical owner metrics are still loading', () => {
    const current: OpenCostWorkloadDetailResponse = {
      available: true,
      currency: 'USD',
      namespace: 'default',
      kind: 'Deployment',
      name: 'checkout',
      current: {
        name: 'checkout',
        kind: 'Deployment',
        hourlyCost: 0.2,
        cpuCost: 0.12,
        memoryCost: 0.08,
        replicas: 2,
        cpuUsageAvailable: true,
        memoryUsageAvailable: true,
        cpuAllocationUse: 25,
        memoryAllocationUse: 25,
      },
    }

    expect(getWorkloadCostState(current, undefined, { trendLoading: true })).toBe('data')
  })

  it('uses historical data when current metrics are absent but history exists', () => {
    const current: OpenCostWorkloadDetailResponse = {
      available: false,
      currency: 'USD',
      reason: 'no_metrics',
      namespace: 'default',
      kind: 'Deployment',
      name: 'checkout',
    }
    const trend: OpenCostWorkloadTrendResponse = {
      available: true,
      currency: 'USD',
      namespace: 'default',
      kind: 'Deployment',
      name: 'checkout',
      range: '7d',
      dataPoints: [
        { timestamp: 1700000000, value: 0.1 },
        { timestamp: 1700003600, value: 0.2 },
      ],
    }

    expect(getWorkloadCostState(current, trend, false)).toBe('partial_missing_current')
  })

  it('separates load failures from absent workload metrics', () => {
    expect(getWorkloadCostState(undefined, undefined, { currentError: true })).toBe('load_error')
  })

  it('surfaces workload access and existence failures', () => {
    const current: OpenCostWorkloadDetailResponse = {
      available: false,
      currency: 'USD',
      reason: 'access_denied',
      namespace: 'prod',
      kind: 'Deployment',
      name: 'checkout',
    }

    const missing: OpenCostWorkloadTrendResponse = {
      available: false,
      currency: 'USD',
      reason: 'not_found',
      namespace: 'prod',
      kind: 'Deployment',
      name: 'checkout',
      range: '24h',
    }

    expect(getWorkloadCostState(current, undefined, false)).toBe('access_denied')
    expect(getWorkloadCostState(undefined, missing, false)).toBe('not_found')
    expect(getWorkloadCostState(undefined, undefined, { currentError: new ApiError('denied', 403) })).toBe('access_denied')
    expect(getWorkloadCostState(undefined, undefined, { trendError: new ApiError('missing', 404) })).toBe('not_found')

    current.reason = 'configuration_mismatch'
    expect(getWorkloadCostState(current, undefined, false)).toBe('configuration_mismatch')
  })

  it('shows Prometheus discovery as soon as one query reports it', () => {
    const current: OpenCostWorkloadDetailResponse = {
      available: false,
      currency: 'USD',
      reason: 'no_prometheus',
      namespace: 'default',
      kind: 'Deployment',
      name: 'checkout',
    }

    expect(getWorkloadCostState(current, undefined, { trendLoading: true })).toBe('no_prometheus')
  })
})
