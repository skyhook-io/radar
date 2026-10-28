import { describe, expect, it, vi } from 'vitest'
import { getRadarUpgradeRequirement } from '@skyhook-io/k8s-ui'
import { ApiError } from './client'
import {
  RADAR_FEATURES,
  RadarFeatureUnsupportedError,
  guardRadarFeature,
  isRadarFeatureUnsupported,
  radarControlBlocked,
  radarControlBlockedReason,
  radarFeatureSupport,
  radarSpecSupport,
  type RadarFeatureSpec,
  shouldRetryRadarQuery,
} from './radarFeatures'

describe('radarFeatureSupport', () => {
  it('trusts the advertised flag over the version', () => {
    expect(radarFeatureSupport('policyResource', { features: { policyResource: true } }, 'v1.7.2')).toBe('supported')
    expect(radarFeatureSupport('policyResource', { features: { policyResource: true } }, undefined)).toBe('supported')
  })

  it('gates a legacy agent that predates the flag by its release version', () => {
    expect(radarFeatureSupport('resourceIssues', undefined, 'v1.7.2')).toBe('unsupported')
    expect(radarFeatureSupport('resourceIssues', undefined, 'v1.8.0')).toBe('supported')
    expect(radarFeatureSupport('podEnvironment', { features: {} }, '1.8.7')).toBe('unsupported')
    expect(radarFeatureSupport('podEnvironment', { features: {} }, '1.9.0')).toBe('supported')
    // v1.10 advertised a features block, just not this flag.
    expect(radarFeatureSupport('policyResource', { features: { yamlReview: true } }, 'v1.9.2')).toBe('unsupported')
    expect(radarFeatureSupport('policyResource', { features: { yamlReview: true } }, 'v1.12.0')).toBe('supported')
  })

  it('never gates on a version it cannot place', () => {
    for (const version of [undefined, '', 'dev', 'unknown', 'v1.7.2-dirty', 'v1.7.2-3-gdeadbee', 'v1.10.0-rc.1', 'v1.7']) {
      expect(radarFeatureSupport('policyResource', undefined, version)).toBe('unknown')
    }
  })

  it('gates features without a flag on version alone', () => {
    expect(radarFeatureSupport('drainPlan', { features: { yamlReview: true } }, 'v1.13.1')).toBe('unsupported')
    expect(radarFeatureSupport('drainPlan', undefined, 'v1.14.0')).toBe('supported')
    expect(radarFeatureSupport('capacity', undefined, 'dev')).toBe('unknown')
    expect(radarFeatureSupport('applications', undefined, 'v1.7.6')).toBe('unsupported')
    expect(radarFeatureSupport('applications', undefined, 'v1.7.7')).toBe('supported')
  })

  it('keeps the unflagged entries to those that predate the first flag', () => {
    // radarSpecSupport treats them as served by any Radar advertising
    // resourceIssues; a new entry must ship its own flag instead.
    const unflagged = Object.entries(RADAR_FEATURES).filter(([, spec]) => !('flag' in spec)).map(([name]) => name)
    expect(unflagged.sort()).toEqual(['applications', 'capacity', 'drainPlan', 'upgradeReadiness'])
  })

  it('lifts a stale host version for unflagged features once any table flag is advertised', () => {
    expect(radarFeatureSupport('drainPlan', { features: { resourceIssues: true } }, 'v1.7.2')).toBe('supported')
    expect(radarFeatureSupport('capacity', { features: { resourceIssues: true } }, 'v1.7.2')).toBe('supported')
    expect(radarFeatureSupport('upgradeReadiness', { features: { resourceIssues: true } }, undefined)).toBe('supported')
  })
})

describe('workload history, whose flag shipped with its endpoint', () => {
  it('trusts a loaded capabilities answer, and probes before it arrives', () => {
    expect(radarFeatureSupport('workloadHistory', { features: { workloadHistory: true } }, undefined)).toBe('supported')
    expect(radarFeatureSupport('workloadHistory', { features: { policyResource: true } }, 'v1.15.0')).toBe('unsupported')
    expect(radarFeatureSupport('workloadHistory', { features: {} }, 'dev')).toBe('unsupported')
    expect(radarFeatureSupport('workloadHistory', undefined, 'v1.15.0')).toBe('unknown')
  })
})

// A behavior change on endpoints every Radar serves: only the flag tells.
describe('Argo CD rollback and auto-sync, gated on argoRollbackSync', () => {
  it.each(['argoRollback', 'argoAutoSyncEnable'] as const)('%s trusts the flag and reads its absence as too old', (feature) => {
    expect(radarFeatureSupport(feature, { features: { argoRollbackSync: true } }, 'v1.16.3')).toBe('supported')
    expect(radarFeatureSupport(feature, { features: { workloadHistory: true } }, 'v1.16.3')).toBe('unsupported')
    expect(radarFeatureSupport(feature, {}, 'dev')).toBe('unsupported')
    expect(radarFeatureSupport(feature, undefined, 'v1.16.3')).toBe('unknown')
  })
})

describe('radarControlBlocked', () => {
  // A control can't be probed like a request: its outcome on an older Radar
  // is wrong, not a 404. So it fails closed when /api/capabilities failed,
  // and stays usable only while the answer is still loading.
  it('blocks a known-old Radar, and an unknown one whose capabilities failed', () => {
    expect(radarControlBlocked('unsupported', false)).toBe(true)
    expect(radarControlBlocked('unknown', true)).toBe(true)
    expect(radarControlBlocked('unknown', false)).toBe(false)
    expect(radarControlBlocked('supported', true)).toBe(false)
  })
})

describe('radarSpecSupport for a feature whose flag shipped with its endpoint', () => {
  const driftAlerts: RadarFeatureSpec = { label: 'Drift alerts', flag: 'resourceIssues', flagShippedWithEndpoint: true }

  it('reads a loaded answer without the flag as too old, whatever the version', () => {
    expect(radarSpecSupport(driftAlerts, { features: {} }, 'dev')).toBe('unsupported')
    expect(radarSpecSupport(driftAlerts, {}, 'v1.7.2')).toBe('unsupported')
    expect(radarSpecSupport(driftAlerts, { features: { resourceIssues: true } }, 'v1.7.2')).toBe('supported')
  })

  it('waits for /api/capabilities before deciding, and never version-gates without a first release', () => {
    expect(radarSpecSupport(driftAlerts, undefined, 'v1.7.2')).toBe('unknown')
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

  it("keeps the router's 404 an error on a Radar known to serve the feature", async () => {
    const routing = new ApiError('HTTP 404 (Not Found)', 404, {}, { unknownRoute: true })
    const error = await guardRadarFeature('policyResource', 'supported', versions, () => Promise.reject(routing))
      .catch((e: unknown) => e)
    expect(error).toBe(routing)
    expect(getRadarUpgradeRequirement(error)).toBeNull()
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

describe('affirmative mutation support', () => {
  it.each(['argoRollback', 'argoAutoSyncEnable'] as const)('%s distinguishes checking, failed and known answers', feature => {
    expect(radarControlBlockedReason(feature, 'unknown', false)).toBe("Checking this cluster's Radar…")
    expect(radarControlBlockedReason(feature, 'unknown', true)).toBe("Couldn't check this cluster's Radar support. Refresh to try again.")
    expect(radarControlBlockedReason(feature, 'supported', false)).toBeUndefined()
    expect(radarControlBlockedReason(feature, 'supported', true)).toBe("Couldn't check this cluster's Radar support. Refresh to try again.")
    expect(radarControlBlockedReason(feature, 'unsupported', false)).toBeUndefined()
  })
  it('preserves permissive loading for other features', () => {
    expect(radarControlBlockedReason('workloadHistory', 'unknown', false)).toBeUndefined()
    expect(radarControlBlocked('unknown', false)).toBe(false)
  })
})
