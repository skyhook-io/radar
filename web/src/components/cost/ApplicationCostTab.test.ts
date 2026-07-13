import { describe, expect, it } from 'vitest'
import type { AppWorkload } from '@skyhook-io/k8s-ui'
import { applicationCostWorkloads, getApplicationCostState } from './ApplicationCostTab'
import {
  ApiError,
  type OpenCostApplicationCostResponse,
  type OpenCostApplicationCostTrendResponse,
} from '../../api/client'

describe('getApplicationCostState', () => {
  it('keeps current app cost visible when historical owner metrics are missing', () => {
    const current: OpenCostApplicationCostResponse = {
      available: true,
      partial: true,
      totals: {
        hourlyCost: 0.4,
        cpuCost: 0.25,
        memoryCost: 0.15,
        replicas: 3,
        cpuUsageAvailable: true,
        memoryUsageAvailable: true,
        cpuAllocationUse: 40,
        memoryAllocationUse: 60,
      },
      coverage: {
        total: 3,
        included: 2,
        unavailable: [
          {
            kind: 'Deployment',
            namespace: 'prod',
            name: 'worker',
            reason: 'no_metrics',
          },
        ],
      },
      workloads: [],
    }
    const trend: OpenCostApplicationCostTrendResponse = {
      available: false,
      reason: 'no_metrics',
      range: '24h',
      coverage: { total: 3, included: 0 },
    }

    expect(
      getApplicationCostState(current, trend, {
        currentLoading: false,
        trendLoading: false,
      }),
    ).toBe('partial_missing_history')
  })

  it('uses historical data when current app metrics are absent but history exists', () => {
    const current: OpenCostApplicationCostResponse = {
      available: false,
      reason: 'no_metrics',
      totals: {
        hourlyCost: 0,
        cpuCost: 0,
        memoryCost: 0,
        replicas: 0,
        cpuUsageAvailable: false,
        memoryUsageAvailable: false,
        cpuAllocationUse: 0,
        memoryAllocationUse: 0,
      },
      coverage: { total: 2, included: 0 },
    }
    const trend: OpenCostApplicationCostTrendResponse = {
      available: true,
      range: '7d',
      windowTotalCost: 2,
      dataPoints: [
        { timestamp: 1700000000, value: 0.2 },
        { timestamp: 1700003600, value: 0.3 },
      ],
      coverage: { total: 2, included: 2 },
    }

    expect(
      getApplicationCostState(current, trend, {
        currentLoading: false,
        trendLoading: false,
      }),
    ).toBe('partial_missing_current')
  })

  it('treats all tracked workloads scaled to zero as valid zero state', () => {
    const current: OpenCostApplicationCostResponse = {
      available: true,
      totals: {
        hourlyCost: 0,
        cpuCost: 0,
        memoryCost: 0,
        replicas: 0,
        cpuUsageAvailable: false,
        memoryUsageAvailable: false,
        cpuAllocationUse: 0,
        memoryAllocationUse: 0,
      },
      coverage: { total: 1, included: 1 },
      workloads: [
        {
          kind: 'Deployment',
          namespace: 'prod',
          name: 'api',
          available: true,
          scaledToZero: true,
          current: {
            name: 'api',
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
        },
      ],
    }

    expect(
      getApplicationCostState(current, undefined, {
        currentLoading: false,
        trendLoading: false,
      }),
    ).toBe('zero')
  })

  it('separates query load failures from absent app metrics', () => {
    expect(getApplicationCostState(undefined, undefined, { currentError: true })).toBe('load_error')
  })

  it('preserves transport error specificity', () => {
    expect(
      getApplicationCostState(undefined, undefined, {
        currentError: new ApiError('denied', 403),
      }),
    ).toBe('access_denied')
    expect(
      getApplicationCostState(undefined, undefined, {
        trendError: new ApiError('missing', 404),
      }),
    ).toBe('not_found')
  })

  it('queries only steady-state workload kinds', () => {
    const workloads = [
      { kind: 'Deployment', namespace: 'prod', name: 'api' },
      { kind: 'StatefulSet', namespace: 'prod', name: 'db' },
      { kind: 'DaemonSet', namespace: 'ops', name: 'agent' },
      { kind: 'Job', namespace: 'prod', name: 'migrate' },
      { kind: 'CronJob', namespace: 'prod', name: 'cleanup' },
    ] as AppWorkload[]

    expect(applicationCostWorkloads(workloads).map((workload) => workload.kind)).toEqual([
      'Deployment',
      'StatefulSet',
      'DaemonSet',
    ])
    expect(applicationCostWorkloads(workloads.slice(3))).toEqual([])
  })

  it('surfaces app workload access and existence failures', () => {
    const current: OpenCostApplicationCostResponse = {
      available: false,
      reason: 'access_denied',
      totals: {
        hourlyCost: 0,
        cpuCost: 0,
        memoryCost: 0,
        replicas: 0,
        cpuUsageAvailable: false,
        memoryUsageAvailable: false,
        cpuAllocationUse: 0,
        memoryAllocationUse: 0,
      },
      coverage: {
        total: 1,
        included: 0,
        unavailable: [
          {
            kind: 'Deployment',
            namespace: 'prod',
            name: 'api',
            reason: 'access_denied',
          },
        ],
      },
    }

    expect(
      getApplicationCostState(current, undefined, {
        currentLoading: false,
        trendLoading: false,
      }),
    ).toBe('access_denied')
    expect(getApplicationCostState(current, undefined, { trendError: true })).toBe('access_denied')
  })
})
