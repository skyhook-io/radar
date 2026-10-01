import { describe, expect, it, vi } from 'vitest'
import { getRadarUpgradeRequirement } from '@skyhook-io/k8s-ui'
import { ApiError } from './client'
import {
  RadarFeatureUnsupportedError,
  guardRadarFeature,
  isRadarFeatureUnsupported,
  radarFeatureSupport,
  shouldRetryRadarQuery,
} from './radarFeatures'

describe('radarFeatureSupport', () => {
  it('trusts the advertised flag over the version', () => {
    expect(radarFeatureSupport('policyResource', { policyResource: true }, 'v1.7.2')).toBe('supported')
    expect(radarFeatureSupport('policyResource', { policyResource: true }, undefined)).toBe('supported')
  })

  it('gates a legacy agent that predates the flag by its release version', () => {
    expect(radarFeatureSupport('resourceIssues', undefined, 'v1.7.2')).toBe('unsupported')
    expect(radarFeatureSupport('resourceIssues', undefined, 'v1.8.0')).toBe('supported')
    expect(radarFeatureSupport('podEnvironment', {}, '1.8.7')).toBe('unsupported')
    expect(radarFeatureSupport('podEnvironment', {}, '1.9.0')).toBe('supported')
    // v1.10 advertised a features block, just not this flag.
    expect(radarFeatureSupport('policyResource', { yamlReview: true }, 'v1.9.2')).toBe('unsupported')
    expect(radarFeatureSupport('policyResource', { yamlReview: true }, 'v1.12.0')).toBe('supported')
  })

  it('never gates on a version it cannot place', () => {
    for (const version of [undefined, '', 'dev', 'unknown', 'v1.7.2-dirty', 'v1.7.2-3-gdeadbee', 'v1.10.0-rc.1', 'v1.7']) {
      expect(radarFeatureSupport('policyResource', undefined, version)).toBe('unknown')
    }
  })

  it('gates features without a flag on version alone', () => {
    expect(radarFeatureSupport('drainPlan', { resourceIssues: true }, 'v1.13.1')).toBe('unsupported')
    expect(radarFeatureSupport('drainPlan', undefined, 'v1.14.0')).toBe('supported')
    expect(radarFeatureSupport('capacity', undefined, 'dev')).toBe('unknown')
  })
})

describe('guardRadarFeature', () => {
  const versions = { currentVersion: 'v1.7.2', latestVersion: 'v1.15.0' }

  it('never asks a Radar known to be too old', async () => {
    const request = vi.fn(() => Promise.resolve('data'))
    await expect(guardRadarFeature('policyResource', 'unsupported', versions, request))
      .rejects.toBeInstanceOf(RadarFeatureUnsupportedError)
    expect(request).not.toHaveBeenCalled()
  })

  it("reads the router's unknown-route 404 as unsupported", async () => {
    const error = await guardRadarFeature('policyResource', 'unknown', versions, () =>
      Promise.reject(new ApiError('HTTP 404 (Not Found)', 404, {}, { unknownRoute: true })),
    ).catch((e: unknown) => e)
    expect(isRadarFeatureUnsupported(error, 'policyResource')).toBe(true)
    expect(getRadarUpgradeRequirement(error)).toEqual({
      feature: 'Policy results',
      minimumVersion: 'v1.10.0',
      currentVersion: 'v1.7.2',
      latestVersion: 'v1.15.0',
    })
  })

  it('passes every other failure through untouched', async () => {
    const handler404 = new ApiError('pods "web" not found', 404, { error: 'pods "web" not found' })
    await expect(guardRadarFeature('policyResource', 'unknown', versions, () => Promise.reject(handler404)))
      .rejects.toBe(handler404)
    const hubNotFound = new ApiError('HTTP 404 (Not Found)', 404)
    await expect(guardRadarFeature('policyResource', 'supported', versions, () => Promise.reject(hubNotFound)))
      .rejects.toBe(hubNotFound)
    expect(getRadarUpgradeRequirement(hubNotFound)).toBeNull()
  })

  it('returns the data when the request succeeds', async () => {
    await expect(guardRadarFeature('policyResource', 'unknown', {}, () => Promise.resolve(42))).resolves.toBe(42)
  })
})

describe('shouldRetryRadarQuery', () => {
  it('does not retry an unsupported feature or a settled client error', () => {
    expect(shouldRetryRadarQuery(0, new RadarFeatureUnsupportedError('resourceIssues', {}))).toBe(false)
    for (const status of [400, 401, 403, 404, 409]) {
      expect(shouldRetryRadarQuery(0, new ApiError('x', status))).toBe(false)
    }
  })

  it('retries timeouts, rate limits, server errors and network failures once', () => {
    for (const error of [new ApiError('x', 408), new ApiError('x', 429), new ApiError('x', 503), new TypeError('Failed to fetch')]) {
      expect(shouldRetryRadarQuery(0, error)).toBe(true)
      expect(shouldRetryRadarQuery(1, error)).toBe(false)
    }
  })
})
