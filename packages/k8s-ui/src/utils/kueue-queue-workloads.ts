import type { KueueQueueWorkloadsResponse } from '../types/kueue-queues'
import { readActionFor } from './read-actions'

/** A response part a retry can complete: a nested read error or gap whose remedy is to retry or wait. */
export function kueueQueueRetryable(page: KueueQueueWorkloadsResponse | undefined): boolean {
  if (!page) return false
  const codes = [
    page.workloadsError?.error_code, page.localQueuesError?.error_code,
    ...(page.workloads?.coverage.gaps ?? []).map(gap => gap.code),
    ...(page.localQueues?.coverage.gaps ?? []).map(gap => gap.code),
  ]
  return codes.some(code => {
    const action = readActionFor(code)
    return action === 'retry' || action === 'wait'
  })
}
