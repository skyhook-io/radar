import type { KueueQueueWorkloadsResponse } from '../types/kueue-queues'
import { readActionFor } from './read-actions'

/**
 * A response part a quick retry can complete: a nested read error or gap
 * whose remedy is to retry (still loading, a failed read). A permission check
 * that didn't run also clears on its own, but each poll spends more checks,
 * and a load that is overdue (`wait`) won't clear in seconds: both wait for
 * the ordinary refresh.
 */
export function kueueQueueRetryable(page: KueueQueueWorkloadsResponse | undefined): boolean {
  if (!page) return false
  const codes = [
    page.workloadsError?.error_code, page.localQueuesError?.error_code,
    ...(page.workloads?.coverage.gaps ?? []).map(gap => gap.code),
    ...(page.localQueues?.coverage.gaps ?? []).map(gap => gap.code),
  ]
  return codes.some(code => code !== 'user_permission_check_failed' && readActionFor(code) === 'retry')
}
