import type { KueueQueueWorkloadsResponse } from '../types/kueue-queues'
import { readActionFor } from './read-actions'

/**
 * A response part that is still loading: a nested read error or gap Radar
 * resolves by itself. A permission check that didn't run (the request's
 * check budget ran out, or the check failed) also clears on its own, but each
 * poll spends more checks, so it waits for the ordinary refresh.
 */
export function kueueQueueRetryable(page: KueueQueueWorkloadsResponse | undefined): boolean {
  if (!page) return false
  const codes = [
    page.workloadsError?.error_code, page.localQueuesError?.error_code,
    ...(page.workloads?.coverage.gaps ?? []).map(gap => gap.code),
    ...(page.localQueues?.coverage.gaps ?? []).map(gap => gap.code),
  ]
  return codes.some(code => code !== 'user_permission_check_failed' && (readActionFor(code) === 'retry' || readActionFor(code) === 'wait'))
}
