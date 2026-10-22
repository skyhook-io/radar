import { useCallback, useSyncExternalStore } from 'react'
import { useMutation, useQueryClient, type MutationFunction, type QueryClient, type UseMutationOptions } from '@tanstack/react-query'
import { actionOutcomeLocked } from './actions'
import { ApiError } from './client'

interface Outcome {
  pending: boolean
  error: Error | null
}
const EMPTY: Outcome = { pending: false, error: null }
const stores = new WeakMap<QueryClient, { entries: Map<string, Outcome>; listeners: Set<() => void> }>()

/** Result memory belongs to the host, so dismissing a dialog cannot unlock a second write. */
export function useReviewedActionMutation<Data, Variables extends { request: { reviewedContext: string } }>(
  target: string,
  reviewedContext: string | undefined,
  options: Omit<UseMutationOptions<Data, Error, Variables>, 'mutationFn' | 'mutationKey'> & { mutationFn: MutationFunction<Data, Variables> },
) {
  const client = useQueryClient()
  let store = stores.get(client)
  if (!store) {
    store = { entries: new Map(), listeners: new Set() }
    stores.set(client, store)
  }
  const outcomes = store
  const keyFor = (context: string | undefined) => JSON.stringify([target, context])
  const key = keyFor(reviewedContext)
  const subscribe = useCallback((listener: () => void) => {
    outcomes.listeners.add(listener)
    return () => { outcomes.listeners.delete(listener) }
  }, [outcomes])
  const snapshot = useCallback(() => outcomes.entries.get(key) ?? EMPTY, [outcomes, key])
  // Connection recovery purges query/mutation caches. It must not forget a
  // request still in flight or a result that may already have changed the target.
  const retained = useSyncExternalStore(subscribe, snapshot, () => EMPTY)
  const publish = (key: string, value: Outcome) => {
    if (value.pending || value.error) outcomes.entries.set(key, value)
    else outcomes.entries.delete(key)
    for (const listener of outcomes.listeners) listener()
  }
  const mutation = useMutation<Data, Error, Variables>({
    ...options,
    mutationKey: ['reviewed-action', target, reviewedContext],
    mutationFn: async (variables, context) => {
      const boundKey = keyFor(variables.request.reviewedContext)
      const previous = outcomes.entries.get(boundKey)
      if (previous?.error) throw previous.error
      if (previous?.pending) throw new ApiError('A write to this target is still in progress. Wait for its result.', 409, { code: 'blocked' })
      publish(boundKey, { pending: true, error: null })
      let retainedError: Error | null = null
      try {
        return await options.mutationFn(variables, context)
      } catch (error) {
        if (actionOutcomeLocked(error)) {
          const failure = error as Error
          const message = `${failure.message} Inspect the current state, then reload this page and review the action again.`
          retainedError = failure instanceof ApiError ? new ApiError(message, failure.status, failure.data) : new Error(message)
        }
        throw error
      } finally {
        publish(boundKey, { pending: false, error: retainedError })
      }
    },
  })
  return { ...mutation, isPending: mutation.isPending || retained.pending, outcomeError: retained.error }
}
