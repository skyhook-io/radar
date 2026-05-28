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
