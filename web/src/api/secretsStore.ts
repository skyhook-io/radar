import type { ResourceCoverage } from '@skyhook-io/k8s-ui/types/resource-coverage'
import type { SecretsStoreRead } from '@skyhook-io/k8s-ui/components/resources/resource-utils-secrets-store'
import { ApiError, isForbiddenError, isStillLoadingError } from './client'

interface ListQuery<T> {
  data?: T[]
  coverage?: ResourceCoverage
  error: unknown
  isLoading: boolean
}

/**
 * One namespace list read, as the Secrets Store renderers need it: what came
 * back, and whether its absence of an object can be trusted. A denial, a kind
 * that is not served and a read still warming up stay distinct from an empty
 * answer.
 */
export function toSecretsStoreRead<T>(query: ListQuery<T>): SecretsStoreRead<T> {
  if (query.error) {
    if (isForbiddenError(query.error)) return { state: 'forbidden' }
    if (isStillLoadingError(query.error)) return { state: 'loading' }
    if (query.error instanceof ApiError && query.error.data?.error_code === 'kind_unknown') {
      return { state: 'unavailable', message: 'The Secrets Store CSI driver’s API is not served in this cluster.' }
    }
    return { state: 'unavailable', message: query.error instanceof Error ? query.error.message : String(query.error) }
  }
  if (query.isLoading || !query.data) return { state: 'loading' }
  const coverage = query.coverage
  if (coverage?.status === 'partial') {
    // The caller asked for one namespace, so a user denial leaves nothing read.
    if (query.data.length === 0 && coverage.gaps.some(gap => gap.code === 'user_forbidden')) return { state: 'forbidden' }
    if (query.data.length === 0 && coverage.gaps.some(gap => gap.code === 'kind_sync_pending')) return { state: 'loading' }
    return { state: 'ok', items: query.data, complete: false }
  }
  // Without coverage (an older Radar) a list cannot establish absence.
  return { state: 'ok', items: query.data, complete: coverage?.status === 'complete' }
}
