// FetchErrorShape is the duck-typed contract every fetch error in the
// app already satisfies — both web/'s ApiError and radar-hub-web's
// ApiError expose .status and .message. Living in @skyhook-io/k8s-ui
// without importing either lets presentational components (FetchResult,
// ResourcesView's forbidden-kind sidebar) classify errors uniformly
// across the OSS binary and Radar Hub.
export interface FetchErrorShape {
  status: number
  message: string
}

export function isFetchError(error: unknown): error is FetchErrorShape {
  if (typeof error !== 'object' || error === null) return false
  const e = error as Record<string, unknown>
  return typeof e.status === 'number' && typeof e.message === 'string'
}

export function isForbiddenError(error: unknown): boolean {
  return isFetchError(error) && error.status === 403
}

// RadarUpgradeRequirement describes a feature the connected Radar is too old to
// serve. An embedding host (Radar Hub) runs a newer frontend against whatever
// Radar is installed in the cluster, so "this endpoint does not exist yet" is an
// expected state that deserves an upgrade prompt, not a red error.
export interface RadarUpgradeRequirement {
  /** What the user is missing, phrased to start a sentence ("Policy results"). */
  feature: string
  /**
   * First Radar release that serves the feature, e.g. "v1.10.0". Absent when
   * it isn't known yet (a feature still awaiting its release); the note then
   * asks for the latest Radar instead.
   */
  minimumVersion?: string
  /** The connected Radar's version, when known. */
  currentVersion?: string
  /** Newest Radar release, when known. */
  latestVersion?: string
}

/**
 * Reads the upgrade requirement off an error, duck-typed like FetchErrorShape so
 * the host's error class (web/'s or Radar Hub's) never has to be imported here.
 */
export function getRadarUpgradeRequirement(error: unknown): RadarUpgradeRequirement | null {
  if (typeof error !== 'object' || error === null) return null
  const requirement = (error as { radarUpgrade?: unknown }).radarUpgrade
  if (typeof requirement !== 'object' || requirement === null) return null
  const r = requirement as Record<string, unknown>
  if (typeof r.feature !== 'string') return null
  if (r.minimumVersion !== undefined && typeof r.minimumVersion !== 'string') return null
  return requirement as RadarUpgradeRequirement
}
