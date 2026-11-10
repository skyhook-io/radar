import { ApiError, crossOriginRefusalMessage, type CostUnavailableReason } from '../../api/client'

export function costUnavailableReasonFromError(error: unknown): CostUnavailableReason | undefined {
  if (!(error instanceof ApiError)) return undefined
  // Not a permission problem: the server's message says what to fix.
  if (crossOriginRefusalMessage(error)) return undefined
  if (error.status === 403) return 'access_denied'
  if (error.status === 404) return 'not_found'
  return undefined
}
