import type { FeatureCapabilities, RadarUpgradeRequirement } from '@skyhook-io/k8s-ui'
import { compareVersions } from '../utils/version'

// Radar Hub embeds the newest @skyhook-io/radar-app against whatever Radar is
// installed in each cluster, so these endpoints can be missing on a connected
// Radar. A Radar that advertises the feature's `features` flag is always
// trusted; otherwise the release that first served the endpoint decides.
//
// Adding an optional endpoint: advertise a flag for it in
// FeatureCapabilities (internal/k8s/capabilities.go) in the same change, add
// an entry here with that flag and `flagShippedWithEndpoint: true`, and guard
// its hook with useRadarFeature. TestFeatureFlagsHaveFrontendGates keeps the
// two lists in step. `minimumVersion` can wait for the release to be cut:
// until then the note asks for the latest Radar.
export interface RadarFeatureSpec {
  /** Plural noun phrase; the upgrade note reads "<label> need Radar vX or newer." */
  label: string
  /** First release that served the endpoint, once known. */
  minimumVersion?: string
  flag?: keyof FeatureCapabilities
  /**
   * The flag shipped in the same release as the endpoint, so a Radar whose
   * capabilities omit it is too old regardless of its version. False for the
   * entries below, whose flags were added after their endpoints.
   */
  flagShippedWithEndpoint?: boolean
}

export const RADAR_FEATURES = {
  resourceIssues: { label: 'Operational issues', minimumVersion: 'v1.8.0', flag: 'resourceIssues' },
  podEnvironment: { label: 'Values from ConfigMaps and Secrets', minimumVersion: 'v1.9.0', flag: 'podEnvironment' },
  policyResource: { label: 'Policy results', minimumVersion: 'v1.10.0', flag: 'policyResource' },
  capacity: { label: 'Capacity views', minimumVersion: 'v1.9.0' },
  upgradeReadiness: { label: 'Upgrade impact reports', minimumVersion: 'v1.9.0' },
  drainPlan: { label: 'Drain plans', minimumVersion: 'v1.14.0' },
  applications: { label: 'Applications', minimumVersion: 'v1.7.7' },
  workloadHistory: { label: 'Workload history', flag: 'workloadHistory', flagShippedWithEndpoint: true },
  serviceEndpointSlices: { label: 'Service EndpointSlice inventories', flag: 'serviceEndpointSlices', flagShippedWithEndpoint: true },
} as const satisfies Record<string, RadarFeatureSpec>

export type RadarFeature = keyof typeof RADAR_FEATURES

export type RadarFeatureSupport = 'supported' | 'unsupported' | 'unknown'

export interface RadarVersions {
  currentVersion?: string
  latestVersion?: string
}

// Only a plain release can be placed against a minimum. Dev, dirty and
// pre-release builds stay unknown so a custom build is never wrongly gated.
const RELEASE_VERSION = /^v?\d+\.\d+\.\d+$/

/**
 * `capabilities` is the /api/capabilities answer, or undefined while it is
 * still loading; only a loaded answer can prove a flag is absent.
 */
export function radarSpecSupport(
  spec: RadarFeatureSpec,
  capabilities: { features?: FeatureCapabilities } | undefined,
  currentVersion: string | undefined,
): RadarFeatureSupport {
  if (spec.flag && capabilities?.features?.[spec.flag] === true) return 'supported'
  // Unflagged entries all predate the first flag added for this table, so a
  // Radar that advertises it serves them, whatever version the host reported.
  if (!spec.flag && capabilities?.features?.resourceIssues === true) return 'supported'
  if (spec.flag && spec.flagShippedWithEndpoint && capabilities) return 'unsupported'
  const version = currentVersion?.trim()
  if (!spec.minimumVersion || !version || !RELEASE_VERSION.test(version)) return 'unknown'
  const order = compareVersions(version, spec.minimumVersion)
  if (order === null) return 'unknown'
  return order < 0 ? 'unsupported' : 'supported'
}

export function radarFeatureSupport(
  feature: RadarFeature,
  capabilities: { features?: FeatureCapabilities } | undefined,
  currentVersion: string | undefined,
): RadarFeatureSupport {
  return radarSpecSupport(RADAR_FEATURES[feature], capabilities, currentVersion)
}

export function radarUpgradeRequirement(feature: RadarFeature, versions: RadarVersions): RadarUpgradeRequirement {
  const spec: RadarFeatureSpec = RADAR_FEATURES[feature]
  return {
    feature: spec.label,
    ...(spec.minimumVersion ? { minimumVersion: spec.minimumVersion } : {}),
    ...(versions.currentVersion ? { currentVersion: versions.currentVersion } : {}),
    ...(versions.latestVersion ? { latestVersion: versions.latestVersion } : {}),
  }
}

/**
 * The connected Radar predates `feature`. Carries `radarUpgrade` so k8s-ui
 * components can render the upgrade note without importing this class.
 */
export class RadarFeatureUnsupportedError extends Error {
  readonly feature: RadarFeature
  readonly radarUpgrade: RadarUpgradeRequirement

  constructor(feature: RadarFeature, versions: RadarVersions) {
    const requirement = radarUpgradeRequirement(feature, versions)
    super(`${requirement.feature} need a newer Radar`)
    this.name = 'RadarFeatureUnsupportedError'
    this.feature = feature
    this.radarUpgrade = requirement
  }
}

export function isRadarFeatureUnsupported(error: unknown, feature?: RadarFeature): boolean {
  return error instanceof RadarFeatureUnsupportedError && (feature === undefined || error.feature === feature)
}

/**
 * Runs `request` unless the connected Radar is known to predate `feature`.
 * When its version is unknown, chi's unknown-route 404 (ApiError.unknownRoute)
 * is read as the same answer; on a Radar known to serve the feature that 404
 * is a routing failure and stays an error.
 */
export async function guardRadarFeature<T>(
  feature: RadarFeature,
  support: RadarFeatureSupport,
  versions: RadarVersions,
  request: () => Promise<T>,
): Promise<T> {
  if (support === 'unsupported') throw new RadarFeatureUnsupportedError(feature, versions)
  try {
    return await request()
  } catch (error) {
    if (support === 'unknown' && (error as { unknownRoute?: unknown } | null)?.unknownRoute === true) {
      throw new RadarFeatureUnsupportedError(feature, versions)
    }
    throw error
  }
}

/**
 * Query retry policy: an unsupported feature or a client error (other than a
 * timeout or rate limit) gives the same answer on every attempt, so retrying
 * only repeats the request against the cluster.
 */
export function shouldRetryRadarQuery(failureCount: number, error: unknown): boolean {
  if (error instanceof RadarFeatureUnsupportedError) return false
  const status = (error as { status?: unknown } | null)?.status
  if (typeof status === 'number' && status >= 400 && status < 500 && status !== 408 && status !== 429) {
    return false
  }
  return failureCount < 1
}
